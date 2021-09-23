package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/handlers"
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
	Characters         map[string]ConfigEntry
	ValidUrls          []string
	GoogleSheetService *sheets.Service
	Cache              *cache.Cache
}

type ResponseMetadata struct {
	StatusCode       int        `json:"statusCode"`
	StatusMessage    string     `json:"statusMessage"`
	ErrorMessage     string     `json:"errorMessage,omitempty"`
	RequestUri       string     `json:"request"`
	RequestTimestamp *time.Time `json:"requestTimestamp"`
	Cached           bool       `json:"cached"`
}

type ApiResponse struct {
	Attributes    *map[string]string `json:"attributes,omitempty"`
	CharacterUrls []string           `json:"characterUrls,omitempty"`
	Metadata      ResponseMetadata   `json:"metadata"`
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

func getGoogleSheetService() *sheets.Service {
	log.Println("-- connecting to Google Sheet API")

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
	log.Println("  * loaded key from api-key.json")

	googleSheetService, err := sheets.NewService(ctx, option.WithAPIKey(apiConfig.ApiKey))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}
	log.Println("  * created Google Sheet Service")

	return googleSheetService
}

func NewCharacterSheetApp() *CharacterSheetServiceApp {
	app := CharacterSheetServiceApp{
		Characters:         getConfig(),
		GoogleSheetService: getGoogleSheetService(),
		// setup cache to cache items for maximum of 1 hours, default of 5 minutes
		Cache: cache.New(1*time.Minute, time.Hour),
	}

	// build list of character keys from map
	for key := range app.Characters {
		app.ValidUrls = append(app.ValidUrls, "/"+key)
	}

	return &app
}

func (app CharacterSheetServiceApp) fetchCharacterAttributesFromSheetsApi(charConfig ConfigEntry) *map[string]string {
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
	charMap := map[string]string{}
	for i, attr := range charConfig.Attributes {
		valueRange := batchResp.ValueRanges[i]
		if len(valueRange.Values) == 0 {
			log.Println("No data found.")
		} else {
			charMap[attr.Name] = fmt.Sprintf("%v", valueRange.Values[0][0])
		}
	}

	return &charMap
}

func (app CharacterSheetServiceApp) LookupCharacter(charKey string) (*map[string]string, bool, bool) {
	log.Println("---")
	log.Printf("Looking for character '%s'... ", charKey)

	// invalid key; found is false
	charConfig, keyExists := app.Characters[charKey]
	if !keyExists {
		return nil, false, false
	}

	cachedCharMap, found := app.Cache.Get(charKey)

	// cache hit! Return cached result.
	if found {
		log.Printf("CACHE hit - '%s'... ", charConfig.CharacterKey)
		return cachedCharMap.(*map[string]string), true, true
	}

	// cache miss - get result from Google Sheet API and store in cache.
	log.Printf("CACHE miss - Retrieving attributes for '%s'... ", charConfig.CharacterKey)
	charMap := app.fetchCharacterAttributesFromSheetsApi(charConfig)
	app.Cache.Set(charKey, charMap, cache.DefaultExpiration)

	return charMap, true, false
}

func writeJsonResponse(w http.ResponseWriter, response ApiResponse) {
	responseJson, _ := json.MarshalIndent(response, "", "  ")

	w.WriteHeader(response.Metadata.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	w.Write(responseJson)
}

func (app CharacterSheetServiceApp) HandleNotFound(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	response := ApiResponse{
		CharacterUrls: app.ValidUrls,
		Metadata: ResponseMetadata{
			StatusCode:       http.StatusNotFound,
			StatusMessage:    http.StatusText(http.StatusNotFound),
			ErrorMessage:     "No character found; see list of valid character paths in the payload.",
			RequestTimestamp: &now,
			RequestUri:       r.URL.Path,
			Cached:           false,
		},
	}

	writeJsonResponse(w, response)
}

func (app CharacterSheetServiceApp) HandleCharacterRequest(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	charKey := vars["characterKey"]

	charAttributes, found, cached := app.LookupCharacter(charKey)

	if !found {
		log.Printf("Character '%s' not found.\n", charKey)
		app.HandleNotFound(w, r)
		return
	}

	now := time.Now()
	response := ApiResponse{
		Attributes: charAttributes,
		Metadata: ResponseMetadata{
			StatusCode:       http.StatusOK,
			StatusMessage:    http.StatusText(http.StatusOK),
			RequestTimestamp: &now,
			RequestUri:       r.URL.Path,
			Cached:           cached,
		},
	}

	writeJsonResponse(w, response)
}

func main() {
	log.Println("Starting Character Sheet Service Application... ")

	app := NewCharacterSheetApp()

	router := mux.NewRouter()

	// set up route for character lookup
	router.HandleFunc("/{characterKey}", app.HandleCharacterRequest).Methods("GET")

	// default 404 handler
	router.NotFoundHandler = router.NewRoute().HandlerFunc(app.HandleNotFound).GetHandler()

	credentials := handlers.AllowCredentials()
	methods := handlers.AllowedMethods([]string{"POST"})
	//ttl := handlers.MaxAge(3600)
	origins := handlers.AllowedOrigins([]string{"*"})

	log.Println("Character Sheet Service Application running on port 9090")
	log.Fatal(http.ListenAndServe(":9090", handlers.CORS(credentials, methods, origins)(router)))
}
