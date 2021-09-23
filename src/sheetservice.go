package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache"

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

type CharacterSheetServiceApp struct {
	Characters            map[string]ConfigEntry
	ValidUrlsResponseJson []byte
	GoogleSheetService    *sheets.Service
	Cache                 *cache.Cache
}

func getConfig() map[string]ConfigEntry {

	log.Println("-- loading character configuration")

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
		log.Printf("  * loaded config for '%s'\n", configEntry.CharacterKey)
		configMap[configEntry.CharacterKey] = configEntry
	}

	return configMap
}

func getService() *sheets.Service {
	log.Println("-- connecting to Google Sheet API")

	ctx := context.Background()

	fileBytes, err := ioutil.ReadFile("api-key.json")
	if err != nil {
		log.Fatalf("Unable to read API config file: %v", err)
	}
	log.Println("  * loaded api-key.json")

	var apiConfig ApiConfig

	err = json.Unmarshal([]byte(fileBytes), &apiConfig)
	if err != nil {
		log.Fatalf("Invalid api-key.json: %v", err)
	}
	log.Println("  * parsed key")

	log.Print("  * creating Google Sheet Service")
	googleSheetService, err := sheets.NewService(ctx, option.WithAPIKey(apiConfig.ApiKey))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	return googleSheetService
}

func NewCharacterSheetApp() *CharacterSheetServiceApp {
	app := CharacterSheetServiceApp{
		Characters:         getConfig(),
		GoogleSheetService: getService(),
		// setup cache to cache items for maximum of 1 hours, default of 5 minutes
		Cache: cache.New(1*time.Minute, time.Hour),
	}

	validUrls := []string{}
	for key := range app.Characters {
		validUrls = append(validUrls, "/"+key)
	}
	app.ValidUrlsResponseJson, _ = json.MarshalIndent(validUrls, "", "  ")

	return &app
}

func (app CharacterSheetServiceApp) fetchCharacterAttributesFromSheetsApi(charConfig ConfigEntry) string {
	// Construct array of ranges to call from sheet in batch
	ranges := []string{}
	for _, attr := range charConfig.Attributes {
		ranges = append(ranges, attr.Range)
	}

	// Query sheet for list of ranges
	batchResp, err := app.GoogleSheetService.Spreadsheets.Values.BatchGet(charConfig.SheetId).Ranges(ranges...).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from sheet: %v", err)
	}

	// map ranges to names from config attributes
	charMap := map[string]interface{}{}
	for i, attr := range charConfig.Attributes {
		valueRange := batchResp.ValueRanges[i]
		if len(valueRange.Values) == 0 {
			log.Println("No data found.")
		} else {
			charMap[attr.Name] = valueRange.Values[0][0]
		}
	}

	jsonBytes, _ := json.MarshalIndent(charMap, "", "  ")

	return string(jsonBytes)
}

func (app CharacterSheetServiceApp) LookupCharacter(charKey string) (string, bool) {
	log.Println("---")
	log.Printf("Looking for character '%s'... ", charKey)

	charConfig, keyExists := app.Characters[charKey]
	if !keyExists {
		return "{}", false
	}

	cachedCharMap, found := app.Cache.Get(charKey)

	if found {
		log.Printf("CACHE hit - '%s'... ", charConfig.CharacterKey)
		return cachedCharMap.(string), true
	}

	log.Printf("CACHE miss - Retrieving attributes for '%s'... ", charConfig.CharacterKey)
	charMap := app.fetchCharacterAttributesFromSheetsApi(charConfig)
	app.Cache.Set(charKey, charMap, cache.DefaultExpiration)
	log.Println(charMap)

	return charMap, true
}

func (app CharacterSheetServiceApp) HandleNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)

	w.Write(app.ValidUrlsResponseJson)
}

func (app CharacterSheetServiceApp) HandleCharacterRequest(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	charKey := vars["characterKey"]

	charJson, found := app.LookupCharacter(charKey)

	if !found {
		log.Printf("Character '%s' not found.\n", charKey)
		app.HandleNotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(charJson))
}

func main() {
	log.Println("Starting Character Sheet Service Application... ")

	app := NewCharacterSheetApp()

	router := mux.NewRouter()

	// set up route for character lookup
	router.HandleFunc("/{characterKey}", app.HandleCharacterRequest).Methods("GET")

	// default 404 handler
	router.NotFoundHandler = router.NewRoute().HandlerFunc(app.HandleNotFound).GetHandler()

	log.Println("Character Sheet Service Application running on port 9090")
	log.Fatal(http.ListenAndServe(":9090", router))
}
