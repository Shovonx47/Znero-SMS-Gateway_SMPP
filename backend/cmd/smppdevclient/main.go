package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func main() {
	addr := flag.String("addr", "localhost:2776", "SMPP server host:port")
	systemID := flag.String("system-id", "dev_client", "SMPP system_id")
	password := flag.String("password", "", "SMPP password (required; use the value printed by seedcustomeredge)")
	bindType := flag.String("bind-type", "trx", "Bind type: tx, rx, or trx")
	dest := flag.String("to", "", "Destination phone number (E.164) to submit SMS")
	src := flag.String("from", "DEVCLIENT", "Source sender ID")
	text := flag.String("text", "Test SMS via smppdevclient", "Message body text")
	keepAlive := flag.Bool("listen", false, "Keep running and listening for DLRs/MO deliver_sm PDUs")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatalf("Failed to connect to %s: %v", *addr, err)
	}
	defer func() { _ = conn.Close() }()

	log.Printf("Connected to SMPP server at %s", *addr)

	// Send Bind PDU
	seq := uint32(1)
	var cmdID uint32
	switch *bindType {
	case "tx":
		cmdID = pdu.CmdBindTransmitter
	case "rx":
		cmdID = pdu.CmdBindReceiver
	case "trx":
		cmdID = pdu.CmdBindTransceiver
	default:
		log.Fatalf("Invalid bind-type: %s (must be tx, rx, or trx)", *bindType)
	}

	bindPDU := pdu.NewBind(cmdID, seq, *systemID, *password, "")
	if err := pdu.WritePDU(conn, bindPDU); err != nil {
		log.Fatalf("Failed to write bind PDU: %v", err)
	}

	// Read Bind Response
	resp, err := pdu.ReadPDU(conn)
	if err != nil {
		log.Fatalf("Failed to read bind response: %v", err)
	}

	hdr := resp.GetHeader()
	if hdr.CommandStatus != pdu.StatusROK {
		log.Fatalf("Bind failed with status 0x%08X", hdr.CommandStatus)
	}
	log.Printf("Bind successful as %s (CommandID: 0x%08X)", *bindType, hdr.CommandID)

	// If destination provided, submit SMS
	if *dest != "" {
		seq++
		submit := pdu.NewSubmitSM(seq)
		submit.SourceAddr = *src
		submit.DestAddr = *dest
		submit.ShortMessage = []byte(*text)
		submit.RegisteredDelivery = 0x01 // Request DLR

		if err := pdu.WritePDU(conn, submit); err != nil {
			log.Fatalf("Failed to write submit_sm: %v", err)
		}
		log.Printf("Submitted SMS to %s (Text: %q, Seq: %d)", *dest, *text, seq)

		submitResp, err := pdu.ReadPDU(conn)
		if err != nil {
			log.Fatalf("Failed to read submit_sm_resp: %v", err)
		}

		sHdr := submitResp.GetHeader()
		if sHdr.CommandStatus != pdu.StatusROK {
			log.Fatalf("SubmitSM rejected with status 0x%08X", sHdr.CommandStatus)
		}
		if sr, ok := submitResp.(*pdu.SubmitSMResp); ok {
			log.Printf("SubmitSM accepted! Message ID: %s", sr.MessageID)
		} else {
			log.Printf("SubmitSM accepted (CommandStatus: 0x%08X)", sHdr.CommandStatus)
		}
	}

	if !*keepAlive && *dest != "" {
		// Clean unbind
		seq++
		_ = pdu.WritePDU(conn, pdu.NewUnbind(seq))
		log.Printf("Unbound cleanly. Done.")
		return
	}

	// Listen loop for incoming deliver_sm and enquire_link
	log.Printf("Listening for incoming PDUs (enquire_link, deliver_sm DLRs/MO)... Press Ctrl+C to quit.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		for {
			p, err := pdu.ReadPDU(conn)
			if err != nil {
				log.Printf("Connection closed: %v", err)
				os.Exit(0)
			}

			pHdr := p.GetHeader()
			switch pHdr.CommandID {
			case pdu.CmdEnquireLink:
				_ = pdu.WritePDU(conn, pdu.NewEnquireLinkResp(pdu.StatusROK, pHdr.SequenceNumber))
			case pdu.CmdDeliverSM:
				if d, ok := p.(*pdu.DeliverSM); ok {
					isDLR := (d.ESMClass & 0x04) != 0
					if isDLR {
						fmt.Printf("\n--- [DLR Received] ---\nFrom: %s\nTo:   %s\nText: %s\n\n",
							d.SourceAddr, d.DestAddr, string(d.ShortMessage))
					} else {
						fmt.Printf("\n--- [Inbound MO Received] ---\nFrom: %s\nTo:   %s\nText: %s\n\n",
							d.SourceAddr, d.DestAddr, string(d.ShortMessage))
					}
					// Acknowledge deliver_sm
					_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, pHdr.SequenceNumber))
				}
			}
		}
	}()

	<-sigChan
	log.Printf("Shutting down... sending unbind")
	seq++
	_ = pdu.WritePDU(conn, pdu.NewUnbind(seq))
	time.Sleep(200 * time.Millisecond)
}
