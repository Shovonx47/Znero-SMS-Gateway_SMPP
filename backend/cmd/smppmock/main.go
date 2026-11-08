// Command smppmock runs a standalone SMPP 3.4 mock SMSC simulator.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2775", "Listen address")
	systemID := flag.String("system-id", "mock_smsc", "Expected system_id")
	password := flag.String("password", "mock_pass", "Expected password")
	useTLS := flag.Bool("tls", false, "Enable TLS with self-signed certificate")
	singleSession := flag.Bool("single-session", false, "Enforce single bound session")
	dlrModeStr := flag.String("dlr-mode", "immediate", "DLR mode: none, immediate, delayed, lost, duplicate, before_submit_resp, failed, expired")
	dlrDelay := flag.Duration("dlr-delay", 2*time.Second, "Delay for delayed DLR mode")
	errCodeStr := flag.String("error-code", "0x00", "Injected error code (hex like 0x58 or decimal)")
	latency := flag.Duration("latency", 0, "Simulated submit latency")

	flag.Parse()

	var errCode uint32
	if strings.HasPrefix(*errCodeStr, "0x") || strings.HasPrefix(*errCodeStr, "0X") {
		val, err := strconv.ParseUint((*errCodeStr)[2:], 16, 32)
		if err != nil {
			log.Fatalf("invalid hex error code: %v", err)
		}
		errCode = uint32(val)
	} else {
		val, err := strconv.ParseUint(*errCodeStr, 10, 32)
		if err != nil {
			log.Fatalf("invalid decimal error code: %v", err)
		}
		errCode = uint32(val)
	}

	cfg := mocksmsc.ServerConfig{
		Addr:                 *addr,
		SystemID:             *systemID,
		Password:             *password,
		TLS:                  *useTLS,
		EnforceSingleSession: *singleSession,
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

	log.Printf("== Znero Mock SMPP SMSC Simulator ==")
	log.Printf("Listening on: %s (TLS: %v)", server.Addr().String(), *useTLS)
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
	_ = server.Close()
	fmt.Println("Mock SMSC stopped cleanly.")
}

