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
	Cache              map[string]*CacheEntry
	/////////////////////////////////////////////////////////////////////////
	// FIXME: use sync.Map instead, as map isn't necessarily threadsafe.   //
	//        Unfortunately, that trades type safety for thread safety...  //
	/////////////////////////////////////////////////////////////////////////
}

type ResponseMetadata struct {
	StatusCode       int        `json:"statusCode"`
	StatusMessage    string     `json:"statusMessage"`
	ErrorMessage     string     `json:"errorMessage,omitempty"`
	RequestUri       string     `json:"request"`
	RequestTimestamp *time.Time `json:"requestTimestamp"`
}

type ApiResponse struct {
	Attributes    *map[string]string `json:"attributes,omitempty"`
	CharacterUrls []string           `json:"characterUrls,omitempty"`
	Metadata      ResponseMetadata   `json:"metadata"`
}

type CacheEntry struct {
	Attributes   *map[string]string
	Expires      time.Time
	UpdatingFlag bool
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
	}

	// create a map for the purpose of cacheing character attributes
	app.Cache = make(map[string]*CacheEntry, len(app.Characters))

	// build list of character keys from map
	for key := range app.Characters {
		// create relative link to character endpoint
		app.ValidUrls = append(app.ValidUrls, "/"+key)

		// prime cache by fetching values for character
		log.Printf("-- Querying attributes for '%s'... ", key)
		app.FetchCharacterAttributesFromSheetsApi(key)
	}

	return &app
}

func NewMetadata(requestPath string, httpStatusCode int, errorMessage string) ResponseMetadata {
	now := time.Now()
	return ResponseMetadata{
		StatusCode:       httpStatusCode,
		StatusMessage:    http.StatusText(httpStatusCode),
		ErrorMessage:     errorMessage,
		RequestTimestamp: &now,
		RequestUri:       requestPath,
	}
}

func WriteApiResponseJson(w http.ResponseWriter, response ApiResponse) {
	responseJson, _ := json.MarshalIndent(response, "", "  ")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*") // CORS allow everything
	w.WriteHeader(response.Metadata.StatusCode)
	w.Write(responseJson)

	message := response.Metadata.ErrorMessage
	if message == "" {
		bytes, _ := json.Marshal(response.Attributes)
		message = string(bytes)
	}
	log.Printf("--- request: %s -> %s", response.Metadata.RequestUri, message)
}

func (app *CharacterSheetServiceApp) UpdateCachedEntry(charKey string, charAttributes *map[string]string) {
	app.Cache[charKey] = &CacheEntry{
		Attributes:   charAttributes,
		Expires:      time.Now().Add(30 * time.Second),
		UpdatingFlag: false,
	}
}

func (app *CharacterSheetServiceApp) FetchCharacterAttributesFromSheetsApi(charKey string) {
	charConfig := app.Characters[charKey]

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
	charMap := make(map[string]string, len(charConfig.Attributes))
	for i, attr := range charConfig.Attributes {
		valueRange := batchResp.ValueRanges[i]
		if len(valueRange.Values) == 0 {
			log.Println("No data found.")
		} else {
			charMap[attr.Name] = fmt.Sprintf("%v", valueRange.Values[0][0])
		}
	}

	app.UpdateCachedEntry(charKey, &charMap)
	log.Printf("***** done updating cache for '%s' *****", charKey)
}

func (app *CharacterSheetServiceApp) LookupCharacter(charKey string) (*map[string]string, bool) {
	entry, found := app.Cache[charKey]
	if !found {
		return nil, false
	}

	// Check to see if cache should expire, and fetch update in parallel if expiry is past. 
	now := time.Now()
	if entry.UpdatingFlag == false && now.After(entry.Expires) {
		entry.UpdatingFlag = true
		app.Cache[charKey] = entry

		log.Printf("***** cache expired for '%s'; fetching update *****", charKey)

		// Run fetch routine in a seperate thread
		go app.FetchCharacterAttributesFromSheetsApi(charKey)
	}

	return entry.Attributes, true
}

func (app *CharacterSheetServiceApp) HandleRequest(w http.ResponseWriter, r *http.Request) {
	requestPath := r.URL.Path

	if r.Method != http.MethodGet {
		// Not GET - 405 Method Not Allowederror
		WriteApiResponseJson(w, ApiResponse{
			CharacterUrls: app.ValidUrls,
			Metadata: NewMetadata(requestPath, http.StatusMethodNotAllowed,
				fmt.Sprintf("Method '%s' not allowed; you must use GET for this web service.", r.Method)),
		})
		return
	}

	// as we're a single endpoint, we want to use all of the path as the character key,
	// once the leading and trailing slash are stripped.
	charKey := strings.Trim(requestPath, "/")

	// looking for character
	charAttributes, found := app.LookupCharacter(charKey)

	if !found {
		// Result not found - 404 Not Found error
		WriteApiResponseJson(w, ApiResponse{
			CharacterUrls: app.ValidUrls,
			Metadata: NewMetadata(requestPath, http.StatusNotFound,
				fmt.Sprintf("No character '%s' found; see list of valid character paths in the payload.", charKey)),
		})
		return
	}

	WriteApiResponseJson(w, ApiResponse{
		Attributes: charAttributes,
		Metadata:   NewMetadata(requestPath, http.StatusOK, ""),
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
