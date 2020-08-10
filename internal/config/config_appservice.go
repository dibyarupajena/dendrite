// Copyright 2017 Andrew Morgan <andrew@amorgan.xyz>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

type AppServiceAPI struct {
	Matrix  *Global  `json:"-"`
	Derived *Derived `json:"-"` // TODO: Nuke Derived from orbit

	Listen      Address         `json:"Listen" comment:"Listen address for this component."`
	Bind        Address         `json:"Bind" comment:"Bind address for this component."`
	Database    DatabaseOptions `json:"Database" comment:"Database options for this component."`
	ConfigFiles []string        `json:"ConfigFiles" comment:"List of paths to appservice configuration files."`
}

func (c *AppServiceAPI) Defaults() {
	c.Listen = "localhost:7777"
	c.Bind = "localhost:7777"
	c.Database.Defaults()
	c.Database.ConnectionString = "file:appservice.db"
}

func (c *AppServiceAPI) Verify(configErrs *ConfigErrors, isMonolith bool) {
	checkNotEmpty(configErrs, "app_service_api.listen", string(c.Listen))
	checkNotEmpty(configErrs, "app_service_api.bind", string(c.Bind))
	checkNotEmpty(configErrs, "app_service_api.database.connection_string", string(c.Database.ConnectionString))
}

// ApplicationServiceNamespace is the namespace that a specific application
// service has management over.
type ApplicationServiceNamespace struct {
	// Whether or not the namespace is managed solely by this application service
	Exclusive bool `json:"Exclusive"`
	// A regex pattern that represents the namespace
	Regex string `json:"Regex"`
	// The ID of an existing group that all users of this application service will
	// be added to. This field is only relevant to the `users` namespace.
	// Note that users who are joined to this group through an application service
	// are not to be listed when querying for the group's members, however the
	// group should be listed when querying an application service user's groups.
	// This is to prevent making spamming all users of an application service
	// trivial.
	GroupID string `json:"GroupID"`
	// Regex object representing our pattern. Saves having to recompile every time
	RegexpObject *regexp.Regexp
}

// ApplicationService represents a Matrix application service.
// https://matrix.org/docs/spec/application_service/unstable.html
type ApplicationService struct {
	// User-defined, unique, persistent ID of the application service
	ID string `json:"ID"`
	// Base URL of the application service
	URL string `json:"URL"`
	// Application service token provided in requests to a homeserver
	ASToken string `json:"ASToken"`
	// Homeserver token provided in requests to an application service
	HSToken string `json:"HSToken"`
	// Localpart of application service user
	SenderLocalpart string `json:"SenderLocalpart"`
	// Information about an application service's namespaces. Key is either
	// "users", "aliases" or "rooms"
	NamespaceMap map[string][]ApplicationServiceNamespace `json:"Namespaces"`
	// Whether rate limiting is applied to each application service user
	RateLimited bool `json:"RateLimited"`
	// Any custom protocols that this application service provides (e.g. IRC)
	Protocols []string `json:"Protocols"`
}

// IsInterestedInRoomID returns a bool on whether an application service's
// namespace includes the given room ID
func (a *ApplicationService) IsInterestedInRoomID(
	roomID string,
) bool {
	if namespaceSlice, ok := a.NamespaceMap["rooms"]; ok {
		for _, namespace := range namespaceSlice {
			if namespace.RegexpObject.MatchString(roomID) {
				return true
			}
		}
	}

	return false
}

// IsInterestedInUserID returns a bool on whether an application service's
// namespace includes the given user ID
func (a *ApplicationService) IsInterestedInUserID(
	userID string,
) bool {
	if namespaceSlice, ok := a.NamespaceMap["users"]; ok {
		for _, namespace := range namespaceSlice {
			if namespace.RegexpObject.MatchString(userID) {
				return true
			}
		}
	}

	return false
}

// OwnsNamespaceCoveringUserId returns a bool on whether an application service's
// namespace is exclusive and includes the given user ID
func (a *ApplicationService) OwnsNamespaceCoveringUserId(
	userID string,
) bool {
	if namespaceSlice, ok := a.NamespaceMap["users"]; ok {
		for _, namespace := range namespaceSlice {
			if namespace.Exclusive && namespace.RegexpObject.MatchString(userID) {
				return true
			}
		}
	}

	return false
}

// IsInterestedInRoomAlias returns a bool on whether an application service's
// namespace includes the given room alias
func (a *ApplicationService) IsInterestedInRoomAlias(
	roomAlias string,
) bool {
	if namespaceSlice, ok := a.NamespaceMap["aliases"]; ok {
		for _, namespace := range namespaceSlice {
			if namespace.RegexpObject.MatchString(roomAlias) {
				return true
			}
		}
	}

	return false
}

// loadAppServices iterates through all application service config files
// and loads their data into the config object for later access.
func loadAppServices(config *AppServiceAPI, derived *Derived) error {
	for _, configPath := range config.ConfigFiles {
		// Create a new application service with default options
		appservice := ApplicationService{
			RateLimited: true,
		}

		// Create an absolute path from a potentially relative path
		absPath, err := filepath.Abs(configPath)
		if err != nil {
			return err
		}

		// Read the application service's config file
		configData, err := ioutil.ReadFile(absPath)
		if err != nil {
			return err
		}

		// Load the config data into our struct
		if err = yaml.UnmarshalStrict(configData, &appservice); err != nil {
			return err
		}

		// Append the parsed application service to the global config
		derived.ApplicationServices = append(
			derived.ApplicationServices, appservice,
		)
	}

	// Check for any errors in the loaded application services
	return checkErrors(config, derived)
}

// setupRegexps will create regex objects for exclusive and non-exclusive
// usernames, aliases and rooms of all application services, so that other
// methods can quickly check if a particular string matches any of them.
func setupRegexps(_ *AppServiceAPI, derived *Derived) (err error) {
	// Combine all exclusive namespaces for later string checking
	var exclusiveUsernameStrings, exclusiveAliasStrings []string

	// If an application service's regex is marked as exclusive, add
	// its contents to the overall exlusive regex string. Room regex
	// not necessary as we aren't denying exclusive room ID creation
	for _, appservice := range derived.ApplicationServices {
		for key, namespaceSlice := range appservice.NamespaceMap {
			switch key {
			case "users":
				appendExclusiveNamespaceRegexs(&exclusiveUsernameStrings, namespaceSlice)
			case "aliases":
				appendExclusiveNamespaceRegexs(&exclusiveAliasStrings, namespaceSlice)
			}
		}
	}

	// Join the regexes together into one big regex.
	// i.e. "app1.*", "app2.*" -> "(app1.*)|(app2.*)"
	// Later we can check if a username or alias matches any exclusive regex and
	// deny access if it isn't from an application service
	exclusiveUsernames := strings.Join(exclusiveUsernameStrings, "|")
	exclusiveAliases := strings.Join(exclusiveAliasStrings, "|")

	// If there are no exclusive regexes, compile string so that it will not match
	// any valid usernames/aliases/roomIDs
	if exclusiveUsernames == "" {
		exclusiveUsernames = "^$"
	}
	if exclusiveAliases == "" {
		exclusiveAliases = "^$"
	}

	// Store compiled Regex
	if derived.ExclusiveApplicationServicesUsernameRegexp, err = regexp.Compile(exclusiveUsernames); err != nil {
		return err
	}
	if derived.ExclusiveApplicationServicesAliasRegexp, err = regexp.Compile(exclusiveAliases); err != nil {
		return err
	}

	return nil
}

// appendExclusiveNamespaceRegexs takes a slice of strings and a slice of
// namespaces and will append the regexes of only the exclusive namespaces
// into the string slice
func appendExclusiveNamespaceRegexs(
	exclusiveStrings *[]string, namespaces []ApplicationServiceNamespace,
) {
	for index, namespace := range namespaces {
		if namespace.Exclusive {
			// We append parenthesis to later separate each regex when we compile
			// i.e. "app1.*", "app2.*" -> "(app1.*)|(app2.*)"
			*exclusiveStrings = append(*exclusiveStrings, "("+namespace.Regex+")")
		}

		// Compile this regex into a Regexp object for later use
		namespaces[index].RegexpObject, _ = regexp.Compile(namespace.Regex)
	}
}

// checkErrors checks for any configuration errors amongst the loaded
// application services according to the application service spec.
func checkErrors(config *AppServiceAPI, derived *Derived) (err error) {
	var idMap = make(map[string]bool)
	var tokenMap = make(map[string]bool)

	// Compile regexp object for checking groupIDs
	groupIDRegexp := regexp.MustCompile(`\+.*:.*`)

	// Check each application service for any config errors
	for _, appservice := range derived.ApplicationServices {
		// Namespace-related checks
		for key, namespaceSlice := range appservice.NamespaceMap {
			for _, namespace := range namespaceSlice {
				if err := validateNamespace(&appservice, key, &namespace, groupIDRegexp); err != nil {
					return err
				}
			}
		}

		// Check if the url has trailing /'s. If so, remove them
		appservice.URL = strings.TrimRight(appservice.URL, "/")

		// Check if we've already seen this ID. No two application services
		// can have the same ID or token.
		if idMap[appservice.ID] {
			return ConfigErrors([]string{fmt.Sprintf(
				"Application service ID %s must be unique", appservice.ID,
			)})
		}
		// Check if we've already seen this token
		if tokenMap[appservice.ASToken] {
			return ConfigErrors([]string{fmt.Sprintf(
				"Application service Token %s must be unique", appservice.ASToken,
			)})
		}

		// Add the id/token to their respective maps if we haven't already
		// seen them.
		idMap[appservice.ID] = true
		tokenMap[appservice.ASToken] = true

		// TODO: Remove once rate_limited is implemented
		if appservice.RateLimited {
			log.Warn("WARNING: Application service option rate_limited is currently unimplemented")
		}
		// TODO: Remove once protocols is implemented
		if len(appservice.Protocols) > 0 {
			log.Warn("WARNING: Application service option protocols is currently unimplemented")
		}
	}

	return setupRegexps(config, derived)
}

// validateNamespace returns nil or an error based on whether a given
// application service namespace is valid. A namespace is valid if it has the
// required fields, and its regex is correct.
func validateNamespace(
	appservice *ApplicationService,
	key string,
	namespace *ApplicationServiceNamespace,
	groupIDRegexp *regexp.Regexp,
) error {
	// Check that namespace(s) are valid regex
	if !IsValidRegex(namespace.Regex) {
		return ConfigErrors([]string{fmt.Sprintf(
			"Invalid regex string for Application Service %s", appservice.ID,
		)})
	}

	// Check if GroupID for the users namespace is in the correct format
	if key == "users" && namespace.GroupID != "" {
		// TODO: Remove once group_id is implemented
		log.Warn("WARNING: Application service option group_id is currently unimplemented")

		correctFormat := groupIDRegexp.MatchString(namespace.GroupID)
		if !correctFormat {
			return ConfigErrors([]string{fmt.Sprintf(
				"Invalid user group_id field for application service %s.",
				appservice.ID,
			)})
		}
	}

	return nil
}

// IsValidRegex returns true or false based on whether the
// given string is valid regex or not
func IsValidRegex(regexString string) bool {
	_, err := regexp.Compile(regexString)

	return err == nil
}
