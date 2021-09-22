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

func getService() *sheets.Service {
	ctx := context.Background()

	apiKeyFile, err := ioutil.ReadFile("api-key.json")
	if err != nil {
		log.Fatalf("Unable to read API config file: %v", err)
	}

	var apiConfig ApiConfig

	err = json.Unmarshal([]byte(apiKeyFile), &apiConfig)
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
	srv := getService()

	spreadsheetId := "19J5G8y9jKLAYc7xkMwaRj0eGuykzS46mrV4dOFCvYRE"
	readRange := "HP_CURRENT"
	resp, err := srv.Spreadsheets.Values.Get(spreadsheetId, readRange).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from sheet: %v", err)
	}

	if len(resp.Values) == 0 {
		fmt.Println("No data found.")
	} else {
		for _, row := range resp.Values {
			fmt.Printf("Current HP: %s\n", row[0])
		}
	}
}
