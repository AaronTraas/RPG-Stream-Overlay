package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/gorilla/mux"

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

func getConfig() map[string]ConfigEntry {

	fileBytes, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatalf("Unable to read config file: %v", err)
	}

	var config []ConfigEntry

	err = json.Unmarshal([]byte(fileBytes), &config)
	if err != nil {
		log.Fatalf("Invalid config.json: %v", err)
	}

	configMap := map[string]ConfigEntry{}
	for _, configEntry := range config {
		configMap[configEntry.CharacterKey] = configEntry
	}

	return configMap
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

	googleSheetService, err := sheets.NewService(ctx, option.WithAPIKey(apiConfig.ApiKey))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	return googleSheetService
}

func getCharacterMap(charConfig ConfigEntry, googleSheetService *sheets.Service) string {
	// Construct array of ranges to call from sheet in batch
	ranges := []string{}
	for _, attr := range charConfig.Attributes {
		ranges = append(ranges, attr.Range)
	}

	// Query sheet for list of ranges
	fmt.Printf("---\nRetrieving attributes for '%s'... ", charConfig.CharacterKey)
	batchResp, err := googleSheetService.Spreadsheets.Values.BatchGet(charConfig.SheetId).Ranges(ranges...).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from sheet: %v", err)
	} else {
		fmt.Println("Success!")
	}

	// map ranges to names from config attributes
	charMap := map[string]interface{}{}
	for i, attr := range charConfig.Attributes {
		valueRange := batchResp.ValueRanges[i]
		if len(valueRange.Values) == 0 {
			fmt.Println("No data found.")
		} else {
			charMap[attr.Name] = valueRange.Values[0][0]
		}
	}

	jsonBytes, _ := json.MarshalIndent(charMap, "", "  ")

	return string(jsonBytes)
}

func main() {
	config := getConfig()
	googleSheetService := getService()

	router := mux.NewRouter()

	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		urls := []string{}
		for key := range config {
			urls = append(urls, "/"+key)
		}

		response, _ := json.MarshalIndent(urls, "", "  ")
		w.Write(response)
	}).Methods("GET")

	router.HandleFunc("/{characterKey}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		vars := mux.Vars(r)
		charKey := vars["characterKey"]

		charMap := getCharacterMap(config[charKey], googleSheetService)

		fmt.Println(charMap)
		fmt.Fprintln(w, charMap)
	}).Methods("GET")

	log.Fatal(http.ListenAndServe(":9090", router))
}
