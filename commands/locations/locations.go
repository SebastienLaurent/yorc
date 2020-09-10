// Copyright 2018 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package locations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ystia/yorc/v4/commands"
	"github.com/ystia/yorc/v4/commands/httputil"
	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/rest"
)

// LOCPATH defines the base path for locations requests
const LOCPATH = "/locations/"

// APPJSON defines the Accept header value for requests
const APPJSON = "application/json"

var opNames []string

// Internal constants for operations on locations
const (
	locationDeletion = iota
	locationUpdate
	locationCreation
	locationShow
)

func init() {
	commands.RootCmd.AddCommand(LocationsCmd)
	commands.ConfigureYorcClientCommand(LocationsCmd, DepViper, &cfgFile, &noColor)
	opNames = make([]string, 3)
	opNames[locationDeletion] = "delete"
	opNames[locationCreation] = "create"
	opNames[locationUpdate] = "update"
}

// DepViper is the viper configuration for the locations command and its children
var DepViper = viper.New()

// ClientConfig is the Yorc client configuration resolved by cobra/viper
var ClientConfig config.Client
var cfgFile string

// noColor returns true if no-color option is set
var noColor bool

// LocationsCmd is the locations-based command
var LocationsCmd = &cobra.Command{
	Use:           "locations",
	Aliases:       []string{"locs", "loc", "l"},
	Short:         "Perform commands on locations",
	Long:          `Perform different commands on locations`,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		config.DefaultConfigTemplateResolver.Disable()
		ClientConfig = commands.GetYorcClientConfig(DepViper, cfgFile)
	},
	Run: func(cmd *cobra.Command, args []string) {
		err := cmd.Help()
		if err != nil {
			fmt.Print(err)
		}
	},
}

// getColoredText returns a text colored according to the operation in
// argument :
// - red for a deletion
// - yellow for an update (bold for the new version, regular for the old one)
// - green for a creation
func getColoredText(colorize bool, text string, operation int) string {
	if !colorize {
		return text
	}
	switch operation {
	case locationCreation:
		return color.New(color.FgHiGreen, color.Bold).SprintFunc()(text)
	case locationUpdate:
		return color.New(color.FgHiYellow, color.Bold).SprintFunc()(text)
	case locationDeletion:
		return color.New(color.FgHiRed, color.Bold).SprintFunc()(text)
	default:
		return text
	}
}

// getLocationConfig makes a GET request to get a given location's configuration
func getLocationConfig(client httputil.HTTPClient, locationName string) (rest.LocationConfiguration, error) {
	var locConfig rest.LocationConfiguration
	request, err := client.NewRequest("GET", getLocationPath(locationName), nil)
	if err != nil {
		return locConfig, err
	}
	request.Header.Add("Accept", APPJSON)
	response, err := client.Do(request)
	if err != nil {
		return locConfig, err
	}

	defer response.Body.Close()
	httputil.HandleHTTPStatusCode(response, locationName, "locations", http.StatusOK)

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return locConfig, err
	}
	err = json.Unmarshal(body, &locConfig)
	if err != nil {
		return locConfig, err
	}
	return locConfig, nil
}

// putLocationsConfig makes a PUT request to locations API
func putLocationConfig(client httputil.HTTPClient, locConfig rest.LocationConfiguration) (string, error) {

	locationName := locConfig.Name
	locationRequest := rest.LocationRequest{
		Type:       locConfig.Type,
		Properties: locConfig.Properties,
	}
	bArray, err := json.Marshal(locationRequest)
	request, err := client.NewRequest("PUT", getLocationPath(locationName), bytes.NewBuffer(bArray))
	if err != nil {
		return locationName, err
	}
	request.Header.Add("Content-Type", APPJSON)
	response, err := client.Do(request)
	if err != nil {
		return locationName, err
	}
	defer response.Body.Close()

	httputil.HandleHTTPStatusCode(response, "", "locations", http.StatusCreated)
	return locationName, nil
}

// patchLocation makes a PATCH request to loactions API
func patchLocationConfig(client httputil.HTTPClient, locConfig rest.LocationConfiguration) (string, error) {

	locationName := locConfig.Name
	locationRequest := rest.LocationRequest{
		Type:       locConfig.Type,
		Properties: locConfig.Properties,
	}
	bArray, err := json.Marshal(locationRequest)
	request, err := client.NewRequest("PATCH", getLocationPath(locationName), bytes.NewBuffer(bArray))
	if err != nil {
		return locationName, err
	}
	request.Header.Add("Content-Type", APPJSON)
	response, err := client.Do(request)
	if err != nil {
		return locationName, err
	}
	defer response.Body.Close()

	httputil.HandleHTTPStatusCode(response, "", "locations", http.StatusOK)
	return locationName, nil
}

func deleteLocationConfig(client httputil.HTTPClient, locationName string) error {

	request, err := client.NewRequest("DELETE", getLocationPath(locationName), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	httputil.HandleHTTPStatusCode(response, locationName, "locations", http.StatusOK)

	return nil
}

func getLocationPath(locName string) string {
	return LOCPATH + locName
}
