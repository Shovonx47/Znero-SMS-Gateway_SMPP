// Command e2ehttpprov runs a standalone scriptable fake HTTP SMS carrier server for black-box E2E tests.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type recordedSend struct {
	ProviderMsgID string `json:"provider_message_id"`
	To            string `json:"to"`
	From          string `json:"from"`
	Text          string `json:"text"`
	Ref           string `json:"ref"`
	ReceivedAt    string `json:"received_at"`
}

func validateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("address %q must bind to loopback only", addr)
	}
	return nil
}

func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:19102", "Loopback HTTP listen address")
	authToken := flag.String("token", "prov_http_tok_default", "Expected Bearer auth token")
	cbURL := flag.String("callback-url", "", "Default Znero DLR callback URL")
	cbSecret := flag.String("callback-secret", "prov_cb_secret_default", "HMAC SHA256 secret for DLR callbacks")
	flag.Parse()

	if err := validateLoopback(*addr); err != nil {
		log.Fatalf("invalid listen addr: %v", err)
	}

	var seq atomic.Int64
	var mu sync.Mutex
	mode := "ok"
	pollStatus := "DELIVRD"
	autoCallback := false
	callbackTarget := *cbURL
	callbackSec := *cbSecret
	pollMap := make(map[string]string)
	recorded := make([]recordedSend, 0, 128)

	fireCallback := func(targetURL, sec, pid, stat, errCode string) (int, error) {
		payload, _ := json.Marshal(map[string]any{
			"provider_message_id": pid,
			"status":              stat,
			"error_code":          errCode,
		})
		req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(payload))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Provider-Signature", signHMAC(sec, payload))
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+*authToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var bodyDoc map[string]any
		_ = json.Unmarshal(raw, &bodyDoc)

		mu.Lock()
		curMode := mode
		curPoll := pollStatus
		doAutoCB := autoCallback
		cbT := callbackTarget
		cbS := callbackSec
		mu.Unlock()

		switch curMode {
		case "429":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		case "400":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_destination"}`))
			return
		case "502":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"bad_gateway"}`))
			return
		case "slow":
			time.Sleep(15 * time.Second)
			return
		case "html":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body>Bad Proxy</body></html>`))
			return
		case "binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0xff, 0x00, 0xfe, 0x89, 0x50, 0x4e, 0x47})
			return
		}

		pid := fmt.Sprintf("ext-http-%d", seq.Add(1))
		to, _ := bodyDoc["to"].(string)
		from, _ := bodyDoc["from"].(string)
		text, _ := bodyDoc["text"].(string)
		ref, _ := bodyDoc["ref"].(string)

		mu.Lock()
		pollMap[pid] = curPoll
		recorded = append(recorded, recordedSend{
			ProviderMsgID: pid,
			To:            to,
			From:          from,
			Text:          text,
			Ref:           ref,
			ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"id":%q}}`, pid)

		if doAutoCB && cbT != "" {
			go func(id, target, sec, st string) {
				time.Sleep(80 * time.Millisecond)
				_, _ = fireCallback(target, sec, id, st, "000")
			}(pid, cbT, cbS, curPoll)
		}
	})

	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+*authToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		pid := r.URL.Query().Get("id")
		mu.Lock()
		st := pollMap[pid]
		if st == "" {
			st = pollStatus
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"status":%q,"error_code":"000"}}`, pid, st)
	})

	mux.HandleFunc("/ctrl/mode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Mode           *string `json:"mode"`
			PollStatus     *string `json:"poll_status"`
			AutoCallback   *bool   `json:"auto_callback"`
			CallbackURL    *string `json:"callback_url"`
			CallbackSecret *string `json:"callback_secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		if req.Mode != nil {
			mode = *req.Mode
		}
		if req.PollStatus != nil {
			pollStatus = *req.PollStatus
		}
		if req.AutoCallback != nil {
			autoCallback = *req.AutoCallback
		}
		if req.CallbackURL != nil {
			callbackTarget = *req.CallbackURL
		}
		if req.CallbackSecret != nil {
			callbackSec = *req.CallbackSecret
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/ctrl/fire-callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			URL               string `json:"url"`
			ProviderMessageID string `json:"provider_message_id"`
			Status            string `json:"status"`
			ErrorCode         string `json:"error_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		target := callbackTarget
		sec := callbackSec
		mu.Unlock()
		if req.URL != "" {
			target = req.URL
		}
		if req.ErrorCode == "" {
			req.ErrorCode = "000"
		}
		code, err := fireCallback(target, sec, req.ProviderMessageID, req.Status, req.ErrorCode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status_code":%d}`, code)
	})

	mux.HandleFunc("/ctrl/requests", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		out := make([]recordedSend, len(recorded))
		copy(out, recorded)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("e2ehttpprov listening on http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
