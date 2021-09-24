package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

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

func LoadCharacterSheetConfig() map[string]ConfigEntry {
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

	configMap := make(map[string]ConfigEntry, len(config))
	for _, configEntry := range config {
		log.Printf("  * loaded config for '%s'\n", configEntry.CharacterKey)
		configMap[configEntry.CharacterKey] = configEntry
	}

	return configMap
}

func NewGoogleSheetService() *sheets.Service {
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
		Characters:         LoadCharacterSheetConfig(),
		GoogleSheetService: NewGoogleSheetService(),
		// setup cache to cache items for maximum of 1 hours, default of 5 minutes
		Cache: cache.New(1*time.Minute, time.Hour),
	}

	// build list of character keys from map
	for key := range app.Characters {
		// create relative link to character endpoint
		app.ValidUrls = append(app.ValidUrls, "/"+key)

		// prime cache by fetching values for character
		log.Printf("-- Querying attributes for '%s'... ", key)
		app.LookupCharacter(key)
	}

	return &app
}

func NewMetadata(requestPath string, httpStatusCode int, cached bool, errorMessage string) ResponseMetadata {
	now := time.Now()
	return ResponseMetadata{
		StatusCode:       httpStatusCode,
		StatusMessage:    http.StatusText(httpStatusCode),
		ErrorMessage:     errorMessage,
		RequestTimestamp: &now,
		RequestUri:       requestPath,
		Cached:           cached,
	}
}

func (app CharacterSheetServiceApp) FetchCharacterAttributesFromSheetsApi(charConfig ConfigEntry) *map[string]string {
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
	// invalid key; found is false
	charConfig, keyExists := app.Characters[charKey]
	if !keyExists {
		return nil, false, false
	}

	cachedCharMap, found := app.Cache.Get(charKey)

	// cache hit! Return cached result.
	if found {
		return cachedCharMap.(*map[string]string), true, true
	}

	// cache miss - get result from Google Sheet API and store in cache.
	charMap := app.FetchCharacterAttributesFromSheetsApi(charConfig)
	app.Cache.Set(charKey, charMap, cache.DefaultExpiration)
	// FIXME - potential race condition here. I should probably manage cache expiry manually, and
	// trigger a goroutine locked behind a semaphor to update the cache in the background, and in
	// the meantime return the cached values. This way, we can never be making more than one query
	// at a time.

	return charMap, true, false
}

func WriteApiResponseJson(w http.ResponseWriter, response ApiResponse) {
	responseJson, _ := json.MarshalIndent(response, "", "  ")

	w.WriteHeader(response.Metadata.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*") // CORS allow everything
	w.Write(responseJson)

	message := response.Metadata.ErrorMessage
	if message == "" {
		bytes, _ := json.Marshal(response.Attributes)
		message = string(bytes)
	}
	log.Printf("--- request: %s -> %s", response.Metadata.RequestUri, message)
}

func (app CharacterSheetServiceApp) HandleRequest(w http.ResponseWriter, r *http.Request) {
	requestPath := r.URL.Path

	if r.Method != http.MethodGet {
		// Not GET - 405 Method Not Allowederror
		WriteApiResponseJson(w, ApiResponse{
			CharacterUrls: app.ValidUrls,
			Metadata: NewMetadata(requestPath, http.StatusMethodNotAllowed, false,
				fmt.Sprintf("Method '%s' not allowed; you must use GET for this web service.", r.Method)),
		})
		return
	}

	// as we're a single endpoint, we want to use all of the path as the character key,
	// once the leading and trailing slash are stripped.
	charKey := strings.Trim(requestPath, "/")

	log.Printf("Looking for character '%s'... ", charKey)
	charAttributes, found, cached := app.LookupCharacter(charKey)

	if !found {
		// Result not found - 404 Not Found error
		WriteApiResponseJson(w, ApiResponse{
			CharacterUrls: app.ValidUrls,
			Metadata: NewMetadata(requestPath, http.StatusNotFound, false,
				fmt.Sprintf("No character '%s' found; see list of valid character paths in the payload.", charKey)),
		})
		return
	}

	WriteApiResponseJson(w, ApiResponse{
		Attributes: charAttributes,
		Metadata:   NewMetadata(requestPath, http.StatusOK, cached, ""),
	})
}

func main() {
	log.Println("Starting Character Sheet Service Application... ")

	app := NewCharacterSheetApp()

	// set up route for character lookup
	http.HandleFunc("/", app.HandleRequest)

	log.Println("Character Sheet Service Application running on port 9090")
	log.Fatal(http.ListenAndServe(":9090", nil))
}
