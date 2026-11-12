// Command smppmock runs a standalone SMPP 3.4 mock SMSC simulator.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
)

func parseErrCode(s string) (uint32, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		val, err := strconv.ParseUint(s[2:], 16, 32)
		return uint32(val), err
	}
	val, err := strconv.ParseUint(s, 10, 32)
	return uint32(val), err
}

func validateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("ctrl-addr %q must be loopback-only", addr)
	}
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:2775", "Listen address")
	ctrlAddr := flag.String("ctrl-addr", "", "Optional loopback HTTP control address (e.g. 127.0.0.1:19101)")
	systemID := flag.String("system-id", "mock_smsc", "Expected system_id")
	password := flag.String("password", "mock_pass", "Expected password")
	useTLS := flag.Bool("tls", false, "Enable TLS with self-signed certificate")
	singleSession := flag.Bool("single-session", false, "Enforce single bound session")
	dlrModeStr := flag.String("dlr-mode", "immediate", "DLR mode: none, immediate, delayed, lost, duplicate, before_submit_resp, failed, undeliverable, expired")
	dlrDelay := flag.Duration("dlr-delay", 2*time.Second, "Delay for delayed DLR mode")
	errCodeStr := flag.String("error-code", "0x00", "Injected error code (hex like 0x58 or decimal)")
	latency := flag.Duration("latency", 0, "Simulated submit latency")
	replayUnacked := flag.Bool("replay-unacked-dlrs", true, "Replay unacknowledged deliver_sm DLRs on ESME reconnect")

	flag.Parse()

	errCode, err := parseErrCode(*errCodeStr)
	if err != nil {
		log.Fatalf("invalid error code: %v", err)
	}

	cfg := mocksmsc.ServerConfig{
		Addr:                 *addr,
		SystemID:             *systemID,
		Password:             *password,
		TLS:                  *useTLS,
		EnforceSingleSession: *singleSession,
		ReplayUnackedDLRs:    *replayUnacked,
		DLRMode:              mocksmsc.DLRMode(*dlrModeStr),
		DLRDelay:             *dlrDelay,
		InjectedError:        errCode,
		Latency:              *latency,
	}

	server, err := mocksmsc.NewServer(cfg)
	if err != nil {
		log.Fatalf("failed to create mock SMSC: %v", err)
	}

	if err := server.Start(); err != nil {
		log.Fatalf("failed to start mock SMSC: %v", err)
	}

	var ctrlSrv *http.Server
	if *ctrlAddr != "" {
		if err := validateLoopbackAddr(*ctrlAddr); err != nil {
			log.Fatalf("invalid ctrl-addr: %v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
			subs := server.GetSubmittedMessages()
			type item struct {
				MessageID   string `json:"message_id"`
				SourceAddr  string `json:"source_addr"`
				DestAddr    string `json:"dest_addr"`
				Text        string `json:"text"`
				ShortMsgHex string `json:"short_msg_hex"`
				DataCoding  uint8  `json:"data_coding"`
				ESMClass    uint8  `json:"esm_class"`
				SubmittedAt string `json:"submitted_at"`
			}
			out := make([]item, 0, len(subs))
			for _, m := range subs {
				out = append(out, item{
					MessageID:   m.MessageID,
					SourceAddr:  m.SourceAddr,
					DestAddr:    m.DestAddr,
					Text:        string(m.ShortMsg),
					ShortMsgHex: hex.EncodeToString(m.ShortMsg),
					DataCoding:  m.DataCoding,
					ESMClass:    m.ESMClass,
					SubmittedAt: m.SubmittedAt.UTC().Format(time.RFC3339Nano),
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		})
		mux.HandleFunc("/ctrl", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				DLRMode   *string `json:"dlr_mode"`
				ErrorCode *string `json:"error_code"`
				LatencyMs *int    `json:"latency_ms"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.DLRMode != nil {
				server.SetDLRMode(mocksmsc.DLRMode(*req.DLRMode))
			}
			if req.ErrorCode != nil {
				code, pErr := parseErrCode(*req.ErrorCode)
				if pErr != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				server.SetInjectedError(code)
			}
			if req.LatencyMs != nil {
				server.SetLatency(time.Duration(*req.LatencyMs) * time.Millisecond)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		ctrlSrv = &http.Server{Addr: *ctrlAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := ctrlSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("ctrl server stopped: %v", err)
			}
		}()
	}

	log.Printf("== Znero Mock SMPP SMSC Simulator ==")
	log.Printf("Listening on: %s (TLS: %v)", server.Addr().String(), *useTLS)
	if *ctrlAddr != "" {
		log.Printf("Control API : http://%s", *ctrlAddr)
	}
	log.Printf("System ID   : %s", *systemID)
	log.Printf("Password    : [REDACTED]")
	log.Printf("DLR Mode    : %s", *dlrModeStr)
	if errCode != 0 {
		log.Printf("Injected Err: 0x%08x", errCode)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down Mock SMPP SMSC...")
	if ctrlSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = ctrlSrv.Shutdown(ctx)
		cancel()
	}
	_ = server.Close()
	fmt.Println("Mock SMSC stopped cleanly.")
}
