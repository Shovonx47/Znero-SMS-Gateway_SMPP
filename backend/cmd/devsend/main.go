package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	baseURL := flag.String("url", "http://localhost:8080/api/v1/customer", "Customer API base URL")
	apiKey := flag.String("key", os.Getenv("CUSTOMER_API_KEY"), "Customer API Key (Bearer token)")
	dest := flag.String("to", "+14155550199", "Destination phone number (E.164)")
	src := flag.String("from", "ZNERO", "Source sender ID")
	text := flag.String("text", "Test SMS via devsend tool", "Message text body")
	clientRef := flag.String("ref", "", "Optional client reference for idempotency")
	bulkCount := flag.Int("bulk", 0, "Number of bulk messages to generate and send (max 1000)")
	estimateOnly := flag.Bool("estimate", false, "Only request pricing estimate without sending")
	flag.Parse()

	if *apiKey == "" {
		log.Fatal("Error: -key flag or CUSTOMER_API_KEY environment variable is required")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	if *estimateOnly {
		runEstimate(client, *baseURL, *apiKey, *dest, *src, *text)
		return
	}

	if *bulkCount > 0 {
		runBulk(client, *baseURL, *apiKey, *dest, *src, *text, *bulkCount)
		return
	}

	runSingle(client, *baseURL, *apiKey, *dest, *src, *text, *clientRef)
}

func runSingle(client *http.Client, baseURL, apiKey, dest, src, text, clientRef string) {
	payload := map[string]interface{}{
		"to":   dest,
		"from": src,
		"text": text,
	}
	if clientRef != "" {
		payload["client_reference"] = clientRef
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/sms", bytes.NewReader(bodyBytes))
	if err != nil {
		log.Fatalf("Create request failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("HTTP Status: %d (%s) in %dms\n", resp.StatusCode, resp.Status, time.Since(start).Milliseconds())
	fmt.Printf("Response Body:\n%s\n", string(respBody))
}

func runBulk(client *http.Client, baseURL, apiKey, dest, src, text string, count int) {
	if count > 1000 {
		count = 1000
	}

	type msgItem struct {
		To              string `json:"to"`
		From            string `json:"from,omitempty"`
		Text            string `json:"text"`
		ClientReference string `json:"client_reference,omitempty"`
	}

	items := make([]msgItem, count)
	for i := 0; i < count; i++ {
		items[i] = msgItem{
			To:              dest,
			From:            src,
			Text:            fmt.Sprintf("%s [#%d]", text, i+1),
			ClientReference: fmt.Sprintf("bulk-ref-%d-%d", time.Now().Unix(), i+1),
		}
	}

	payload := map[string]interface{}{
		"messages": items,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/sms/bulk", bytes.NewReader(bodyBytes))
	if err != nil {
		log.Fatalf("Create bulk request failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Bulk request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("HTTP Status: %d in %dms\n", resp.StatusCode, time.Since(start).Milliseconds())
	fmt.Printf("Bulk Response:\n%s\n", string(respBody))
}

func runEstimate(client *http.Client, baseURL, apiKey, dest, src, text string) {
	payload := map[string]interface{}{
		"to":   dest,
		"from": src,
		"text": text,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/sms/estimate", bytes.NewReader(bodyBytes))
	if err != nil {
		log.Fatalf("Create estimate request failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Estimate request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("Estimate Result (Status %d):\n%s\n", resp.StatusCode, string(respBody))
}
