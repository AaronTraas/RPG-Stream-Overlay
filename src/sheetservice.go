package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type ApiConfig struct {
	ApiKey string `json:"apiKey"`
}

type AttributeRow struct {
	Name  string `json:"name"`
	Range string `json:"range"`
}

type ConfigEntry struct {
	CharacterKey string         `json:"characterKey"`
	SheetId      string         `json:"sheetId"`
	Attributes   []AttributeRow `json:"attributes"`
}

func getConfig() []ConfigEntry {

	fileBytes, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatalf("Unable to read config file: %v", err)
	}

	var config []ConfigEntry

	err = json.Unmarshal([]byte(fileBytes), &config)
	if err != nil {
		log.Fatalf("Invalid config.json: %v", err)
	}

	return config
}

func getService() *sheets.Service {
	ctx := context.Background()

	fileBytes, err := ioutil.ReadFile("api-key.json")
	if err != nil {
		log.Fatalf("Unable to read API config file: %v", err)
	}

	var apiConfig ApiConfig

	err = json.Unmarshal([]byte(fileBytes), &apiConfig)
	if err != nil {
		log.Fatalf("Invalid api-key.json: %v", err)
	}

	srv, err := sheets.NewService(ctx, option.WithAPIKey(apiConfig.ApiKey))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	return srv
}

func main() {
	config := getConfig()
	srv := getService()

	// map for serialization of results
	out := map[string]interface{}{}

	for _, c := range config {
		// Construct array of ranges to call from sheet in batch
		ranges := []string{}
		for _, attr := range c.Attributes {
			ranges = append(ranges, attr.Range)
		}

		// Query sheet for list of ranges
		fmt.Printf("---\nRetrieving attributes for '%s'... ", c.CharacterKey)
		batchResp, err := srv.Spreadsheets.Values.BatchGet(c.SheetId).Ranges(ranges...).Do()
		if err != nil {
			log.Fatalf("Unable to retrieve data from sheet: %v", err)
		} else {
			fmt.Println("Success!")
		}

		// map ranges to names from config attributes
		entry := map[string]interface{}{}
		for i, attr := range c.Attributes {
			valueRange := batchResp.ValueRanges[i]
			if len(valueRange.Values) == 0 {
				fmt.Println("No data found.")
			} else {
				entry[attr.Name] = valueRange.Values[0][0]
			}
		}

		out[c.CharacterKey] = entry
	}

	jsonString, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("\n%s\n", jsonString)
}
