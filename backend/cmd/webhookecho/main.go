package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":8085", "HTTP listen address")
	secret := flag.String("secret", os.Getenv("WEBHOOK_SECRET"), "Expected HMAC-SHA256 signing secret for signature verification")
	statusCode := flag.Int("status", 200, "HTTP status code to return")
	delayMs := flag.Int("delay", 0, "Artificial response latency in milliseconds")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
		defer func() { _ = r.Body.Close() }()

		timestamp := time.Now().Format(time.RFC3339)
		deliveryID := r.Header.Get("X-Znero-Delivery-ID")
		eventType := r.Header.Get("X-Znero-Event")
		sigHeader := r.Header.Get("X-Znero-Signature")

		fmt.Printf("\n--- [%s] Incoming Webhook ---\n", timestamp)
		fmt.Printf("Method:      %s %s\n", r.Method, r.URL.Path)
		fmt.Printf("Delivery ID: %s\n", deliveryID)
		fmt.Printf("Event:       %s\n", eventType)
		fmt.Printf("Signature:   %s\n", sigHeader)
		fmt.Printf("Content-Len: %d bytes\n", len(body))
		fmt.Printf("Payload:\n%s\n", string(body))

		if *secret != "" {
			valid := verifySignature(sigHeader, body, *secret)
			if valid {
				fmt.Printf("Signature Verification: [VALID âœ“]\n")
			} else {
				fmt.Printf("Signature Verification: [INVALID âœ—]\n")
			}
		}

		if *delayMs > 0 {
			time.Sleep(time.Duration(*delayMs) * time.Millisecond)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(*statusCode)
		_, _ = fmt.Fprintf(w, `{"received":true,"delivery_id":"%s"}`+"\n", deliveryID)
	})

	log.Printf("Webhook Echo Server listening on %s (Secret: %t, Status: %d, Delay: %dms)", *addr, *secret != "", *statusCode, *delayMs)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func verifySignature(sigHeader string, payload []byte, secret string) bool {
	if sigHeader == "" {
		return false
	}

	parts := strings.Split(sigHeader, ",")
	var tsPart, v1Part string
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			if kv[0] == "t" {
				tsPart = kv[1]
			} else if kv[0] == "v1" {
				v1Part = kv[1]
			}
		}
	}

	if tsPart == "" || v1Part == "" {
		return false
	}

	payloadToSign := fmt.Sprintf("%s.%s", tsPart, string(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadToSign))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(v1Part), []byte(expectedSig))
}
