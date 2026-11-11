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
	"strconv"
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
		eventID := r.Header.Get("X-Znero-Event-Id")
		eventType := r.Header.Get("X-Znero-Event")
		tsHeader := r.Header.Get("X-Znero-Timestamp")
		sigHeader := r.Header.Get("X-Znero-Signature")

		fmt.Printf("\n--- [%s] Incoming Webhook ---\n", timestamp)
		fmt.Printf("Method:      %s %s\n", r.Method, r.URL.Path)
		fmt.Printf("Event-Id:    %s\n", eventID)
		fmt.Printf("Event:       %s\n", eventType)
		fmt.Printf("Timestamp:   %s\n", tsHeader)
		fmt.Printf("Signature:   %s\n", sigHeader)
		fmt.Printf("Content-Len: %d bytes\n", len(body))
		fmt.Printf("Payload:\n%s\n", string(body))

		if *secret != "" {
			valid := verifySignature(tsHeader, sigHeader, body, *secret)
			if valid {
				fmt.Printf("Signature Verification: [VALID ✓]\n")
			} else {
				fmt.Printf("Signature Verification: [INVALID ✗]\n")
			}
		}

		if *delayMs > 0 {
			time.Sleep(time.Duration(*delayMs) * time.Millisecond)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(*statusCode)
		_, _ = fmt.Fprintf(w, `{"received":true,"event_id":"%s"}`+"\n", eventID)
	})

	log.Printf("Webhook Echo Server listening on %s (Secret: %t, Status: %d, Delay: %dms)", *addr, *secret != "", *statusCode, *delayMs)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// verifySignature reconstructs the signed payload "{timestamp}.{body}" and verifies the
// HMAC-SHA256 against the "v1=<hex>" value in X-Znero-Signature.
func verifySignature(tsHeader, sigHeader string, payload []byte, secret string) bool {
	if tsHeader == "" || sigHeader == "" {
		return false
	}

	// Validate timestamp is a reasonable unix timestamp (not more than 5 minutes old)
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > 5*time.Minute {
		fmt.Printf("Warning: webhook timestamp is %v old (tolerance: 5m)\n", age.Round(time.Second))
	}

	// Extract v1=<hex> from signature header
	v1Hex := ""
	if len(sigHeader) > 3 && sigHeader[:3] == "v1=" {
		v1Hex = sigHeader[3:]
	}
	if v1Hex == "" {
		return false
	}

	payloadToSign := fmt.Sprintf("%s.%s", tsHeader, string(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadToSign))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(v1Hex), []byte(expectedSig))
}
