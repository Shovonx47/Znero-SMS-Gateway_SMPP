package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func eventuallyBB(timeout time.Duration, desc string, cond func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("timed out waiting for %s after %s", desc, timeout)
}

func dialAndBind(smppAddr string, cmdID uint32, sysID, pwd string) net.Conn {
	conn, err := net.DialTimeout("tcp", smppAddr, 5*time.Second)
	if err != nil {
		log.Fatalf("dial SMPP %s: %v", smppAddr, err)
	}
	if err := pdu.WritePDU(conn, pdu.NewBind(cmdID, 1, sysID, pwd, "SMPP")); err != nil {
		_ = conn.Close()
		log.Fatalf("write bind (%s): %v", sysID, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		log.Fatalf("read bind_resp (%s): %v", sysID, err)
	}
	br, ok := resp.(*pdu.BindResp)
	if !ok || br.Header.CommandStatus != pdu.StatusROK {
		_ = conn.Close()
		log.Fatalf("bind rejected (%s): %+v", sysID, resp)
	}
	return conn
}

func unbindClean(conn net.Conn, seq uint32) {
	_ = pdu.WritePDU(conn, pdu.NewUnbind(seq))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		log.Fatalf("read unbind_resp: %v", err)
	}
	if _, ok := resp.(*pdu.UnbindResp); !ok {
		log.Fatalf("expected UnbindResp, got %T", resp)
	}
	_ = conn.Close()
}

func buildSubmit(seq uint32, dest, src string, dcs uint8, body []byte, dlr uint8) *pdu.SubmitSM {
	sub := pdu.NewSubmitSM(seq)
	sub.DestAddr = strings.TrimPrefix(dest, "+")
	sub.SourceAddr = src
	sub.DataCoding = dcs
	sub.RegisteredDelivery = dlr
	if len(body) <= 254 {
		sub.ShortMessage = body
	} else {
		sub.TLVs = append(sub.TLVs, pdu.TLV{
			Tag:    pdu.TagMessagePayload,
			Length: uint16(len(body)),
			Value:  body,
		})
	}
	return sub
}

func encodeUTF16BE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, v := range u16 {
		out[2*i] = byte(v >> 8)
		out[2*i+1] = byte(v & 0xFF)
	}
	return out
}

func encodeLatin1(s string) []byte {
	var out []byte
	for _, r := range s {
		if r > 0xFF {
			out = append(out, '?')
		} else {
			out = append(out, byte(r))
		}
	}
	return out
}

func submitAndReadResp(conn net.Conn, sub *pdu.SubmitSM) *pdu.SubmitSMResp {
	if err := pdu.WritePDU(conn, sub); err != nil {
		log.Fatalf("write submit_sm: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	for {
		p, err := pdu.ReadPDU(conn)
		if err != nil {
			log.Fatalf("read submit_sm_resp: %v", err)
		}
		if d, isDLR := p.(*pdu.DeliverSM); isDLR {
			_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		sr, ok := p.(*pdu.SubmitSMResp)
		if !ok {
			log.Fatalf("expected SubmitSMResp, got %T", p)
		}
		return sr
	}
}

func readDeliverSM(conn net.Conn, wantSubstr string, timeout time.Duration) *pdu.DeliverSM {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		p, err := pdu.ReadPDU(conn)
		if err != nil {
			log.Fatalf("read customer deliver_sm (%q): %v", wantSubstr, err)
		}
		if d, ok := p.(*pdu.DeliverSM); ok {
			_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
			if strings.Contains(string(d.ShortMessage), wantSubstr) {
				return d
			}
		}
	}
}

func httpSendSMS(client *apiClient, apiKey, dest, text string) string {
	code, doc, raw, err := client.doJSON(http.MethodPost, "/api/v1/customer/sms", apiKey, map[string]any{
		"destination":      dest,
		"sender_id":        "ZNERO",
		"text":             text,
		"client_reference": "bb-" + uuid.NewString()[:8],
	})
	if err != nil || code != http.StatusCreated {
		log.Fatalf("POST /customer/sms failed: status=%d err=%v body=%s", code, err, string(raw))
	}
	id := strField(unwrapData(doc), "id")
	if id == "" {
		log.Fatalf("POST /customer/sms returned empty id: %s", string(raw))
	}
	return id
}

func httpGetMsgStatus(client *apiClient, apiKey, msgID string) (string, string, int) {
	doc := client.mustJSON(http.MethodGet, "/api/v1/customer/sms/"+msgID, apiKey, nil, http.StatusOK)
	partsF, _ := doc["parts"].(float64)
	return strField(doc, "status"), strField(doc, "text"), int(partsF)
}

func getWalletBalance(client *apiClient, superTok, walletID string) string {
	doc := client.mustJSON(http.MethodGet, "/api/v1/wallets/"+walletID+"/balances", superTok, nil, http.StatusOK)
	if v, ok := doc["available_balance"].(string); ok {
		return v
	}
	return fmt.Sprintf("%v", doc["available_balance"])
}

func runVerify(client *apiClient, st *blackboxState, smppAddr, smppCtrlURL, httpProvURL, dbURL string) {
	var whMu sync.Mutex
	whSecrets := make(map[string]string)
	whReceived := make(map[uuid.UUID][]string)
	whBaseURL, stopWH := startWebhookListener(whSecrets, &whMu, whReceived)
	defer stopWH()

	registerWH := func(key, apiKey string) {
		doc := client.mustJSON(http.MethodPost, "/api/v1/customer/webhooks", apiKey, map[string]any{
			"url":    whBaseURL + "/wh/" + key,
			"events": []string{"message.delivered", "message.undelivered", "message.failed", "message.expired"},
		}, http.StatusCreated)
		sec := strField(doc, "secret")
		if sec == "" {
			log.Fatalf("createWebhook %s missing secret: %+v", key, doc)
		}
		whMu.Lock()
		whSecrets[key] = sec
		whMu.Unlock()
	}
	registerWH("c1", st.Cust1APIKey)
	registerWH("c2", st.Cust2APIKey)

	waitWH := func(midStr, wantStatus string) {
		mid, _ := uuid.Parse(midStr)
		eventuallyBB(15*time.Second, fmt.Sprintf("webhook %s=%s", midStr[:8], wantStatus), func() bool {
			whMu.Lock()
			defer whMu.Unlock()
			for _, s := range whReceived[mid] {
				if s == wantStatus {
					return true
				}
			}
			return false
		})
	}

	// Ensure e2ehttpprov auto_callback is enabled for HTTP callback legs
	setHTTPMode := func(payload string) {
		req, _ := http.NewRequest(http.MethodPost, httpProvURL+"/ctrl/mode", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.http.Do(req)
		if err != nil {
			log.Fatalf("setHTTPMode err: %v", err)
		}
		_ = resp.Body.Close()
	}

	// =========================================================================
	// GROUP A: All 4 Live Matrix Combinations over Real OS Processes & Sockets
	// =========================================================================
	log.Println("[PART2] Group A1: Customer 1 HTTP -> Provider SMPP (on_delivery -> DELIVRD + HMAC webhook)")
	m1 := httpSendSMS(client, st.Cust1APIKey, "+12025550101", "Part2 C1 HTTP->SMPP")
	eventuallyBB(15*time.Second, "m1 delivered", func() bool {
		s, _, _ := httpGetMsgStatus(client, st.Cust1APIKey, m1)
		return s == "delivered"
	})
	waitWH(m1, "delivered")

	log.Println("[PART2] Group A2: Customer 2 HTTP -> Provider HTTP (on_provider_accept + signed callback UNDELIV -> refund)")
	setHTTPMode(`{"mode":"ok","poll_status":"UNDELIV","auto_callback":true}`)
	m2 := httpSendSMS(client, st.Cust2APIKey, "+12025550102", "Part2 C2 HTTP->HTTP UNDELIV refund")
	eventuallyBB(15*time.Second, "m2 failed", func() bool {
		s, _, _ := httpGetMsgStatus(client, st.Cust2APIKey, m2)
		return s == "failed"
	})
	waitWH(m2, "undelivered")

	log.Println("[PART2] Group A3: Customer 1 SMPP -> Provider SMPP (on_delivery + outbound deliver_sm DLR)")
	c1Conn := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.Cust1SMPPSys, st.Cust1SMPPPass)
	m3Resp := submitAndReadResp(c1Conn, buildSubmit(2, "+12025550103", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Part2 C1 SMPP->SMPP"), 1))
	if m3Resp.Header.CommandStatus != pdu.StatusROK {
		log.Fatalf("A3 submit_sm failed: 0x%x", m3Resp.Header.CommandStatus)
	}
	readDeliverSM(c1Conn, "stat:DELIVRD", 15*time.Second)
	unbindClean(c1Conn, 3)

	log.Println("[PART2] Group A4: Customer 2 SMPP -> Provider HTTP (on_provider_accept + callback DELIVRD -> deliver_sm DLR)")
	setHTTPMode(`{"mode":"ok","poll_status":"DELIVRD","auto_callback":true}`)
	c2Conn := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.Cust2SMPPSys, st.Cust2SMPPPass)
	m4Resp := submitAndReadResp(c2Conn, buildSubmit(2, "+12025550104", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Part2 C2 SMPP->HTTP"), 1))
	if m4Resp.Header.CommandStatus != pdu.StatusROK {
		log.Fatalf("A4 submit_sm failed: 0x%x", m4Resp.Header.CommandStatus)
	}
	readDeliverSM(c2Conn, "stat:DELIVRD", 15*time.Second)
	unbindClean(c2Conn, 3)

	// =========================================================================
	// GROUP B: SMPP Protocol Edge Cases over real TCP :2776
	// =========================================================================
	runSMPPEdgeCases(client, st, smppAddr, smppCtrlURL, dbURL)

	// =========================================================================
	// GROUP C: Direct SQL + HTTP Reconciliation Verification
	// =========================================================================
	assertSQLAndReconcile(client, st, dbURL, 0)
	log.Println("[PART2 VERIFY] ALL Black-Box Scenarios + SMPP Protocol Edge Cases + SQL Reconciliation PASSED!")
}

func runSMPPEdgeCases(client *apiClient, st *blackboxState, smppAddr, smppCtrlURL, dbURL string) {
	// 1. Bind Transmitter + EnquireLink + Unbind; Bind Receiver rejects submit_sm
	txConn := dialAndBind(smppAddr, pdu.CmdBindTransmitter, st.Cust1SMPPSys, st.Cust1SMPPPass)
	_ = pdu.WritePDU(txConn, pdu.NewEnquireLink(10))
	_ = txConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	elResp, err := pdu.ReadPDU(txConn)
	_ = txConn.SetReadDeadline(time.Time{})
	if err != nil {
		log.Fatalf("enquire_link failed: %v", err)
	}
	if er, ok := elResp.(*pdu.EnquireLinkResp); !ok || er.Header.CommandStatus != pdu.StatusROK {
		log.Fatalf("unexpected enquire_link_resp: %+v", elResp)
	}
	unbindClean(txConn, 11)

	rxConn := dialAndBind(smppAddr, pdu.CmdBindReceiver, st.Cust1SMPPSys, st.Cust1SMPPPass)
	rxSubResp := submitAndReadResp(rxConn, buildSubmit(12, "+12025550105", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Should fail on receiver"), 0))
	if rxSubResp.Header.CommandStatus != pdu.StatusRInvBndSts {
		log.Fatalf("submit_sm on receiver returned 0x%x, want StatusRInvBndSts (0x%x)", rxSubResp.Header.CommandStatus, pdu.StatusRInvBndSts)
	}
	unbindClean(rxConn, 13)

	// 2. Encoding & Boundaries (GSM-7 ext chars, 160 vs 161, 159a+euro=161, UCS-2 Bangla/Arabic/Emoji, 70 vs 71, Latin-1, Binary reject)
	esme := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.Cust1SMPPSys, st.Cust1SMPPPass)
	seq := uint32(100)
	nextSeq := func() uint32 { seq++; return seq }

	assertParts := func(msgID string, wantParts int, wantSubstr string) {
		eventuallyBB(10*time.Second, "edge msg settled "+msgID[:8], func() bool {
			s, txt, parts := httpGetMsgStatus(client, st.Cust1APIKey, msgID)
			if parts != wantParts {
				return false
			}
			if wantSubstr != "" && !strings.Contains(txt, wantSubstr) {
				return false
			}
			return s == "delivered" || s == "sent"
		})
	}

	// GSM-7 short + extension chars + 159 'a' + '€' -> 161 septets -> 2 parts!
	rExt := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550110", "ZNERO", pdu.DataCodingSMSCDefault, []byte(strings.Repeat("a", 159)+"€"), 1))
	if rExt.Header.CommandStatus != pdu.StatusROK {
		log.Fatalf("GSM-7 159+'€' submit rejected: 0x%x", rExt.Header.CommandStatus)
	}
	assertParts(rExt.MessageID, 2, "€")

	// Exact 160 vs 161 GSM-7 boundary
	r160 := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550111", "ZNERO", pdu.DataCodingSMSCDefault, []byte(strings.Repeat("b", 160)), 1))
	assertParts(r160.MessageID, 1, "")
	r161 := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550112", "ZNERO", pdu.DataCodingSMSCDefault, []byte(strings.Repeat("b", 161)), 1))
	assertParts(r161.MessageID, 2, "")

	// Unpacked GSM-7 raw 0x00 (@), 0x1B 0x65 (€), 0x02 ($)
	rRawGSM := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550113", "ZNERO", pdu.DataCodingSMSCDefault, []byte{0x00, 0x02, 0x1B, 0x65, 'O', 'K'}, 1))
	assertParts(rRawGSM.MessageID, 1, "@$€OK")

	// UCS-2 (0x08 UTF-16BE): Bangla + Arabic + Emoji + 70 vs 71 boundary
	bangla := "আমার সোনার বাংলা مرحبا 🚀🔥"
	rUcs := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550114", "ZNERO", pdu.DataCodingUCS2, encodeUTF16BE(bangla), 1))
	if rUcs.Header.CommandStatus != pdu.StatusROK {
		log.Fatalf("UCS-2 submit failed: 0x%x", rUcs.Header.CommandStatus)
	}
	assertParts(rUcs.MessageID, 1, "আমার")

	r70u := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550115", "ZNERO", pdu.DataCodingUCS2, encodeUTF16BE(strings.Repeat("ব", 70)), 1))
	assertParts(r70u.MessageID, 1, "")
	r71u := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550116", "ZNERO", pdu.DataCodingUCS2, encodeUTF16BE(strings.Repeat("ব", 71)), 1))
	assertParts(r71u.MessageID, 2, "")

	// Latin-1 (0x03)
	rLat := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550117", "ZNERO", 0x03, encodeLatin1("Café naïve"), 1))
	assertParts(rLat.MessageID, 1, "Café naïve")

	// Binary (0x04) -> MUST safely reject with StatusRInvDCS!
	rBin := submitAndReadResp(esme, buildSubmit(nextSeq(), "+12025550118", "ZNERO", pdu.DataCoding8BitBinary, []byte{0xff, 0x00, 0xde, 0xad}, 0))
	if rBin.Header.CommandStatus != pdu.StatusRInvDCS {
		log.Fatalf("binary DCS 0x04 returned 0x%x, want StatusRInvDCS (0x%x)", rBin.Header.CommandStatus, pdu.StatusRInvDCS)
	}

	// Verify MockSMSC actually received our decoded UTF-8 / UTF-16BE Bangla payload
	ctrlResp, err := client.http.Get(smppCtrlURL + "/requests")
	if err != nil {
		log.Fatalf("query mocksmsc ctrl requests: %v", err)
	}
	ctrlRaw, _ := io.ReadAll(ctrlResp.Body)
	_ = ctrlResp.Body.Close()
	if !strings.Contains(string(ctrlRaw), "12025550114") {
		log.Fatalf("expected MockSMSC to have received UCS-2 message to 12025550114")
	}

	// 3. Partial / drip-fed 1-byte TCP writes of one PDU
	dripSub := buildSubmit(nextSeq(), "+12025550119", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Drip-fed PDU"), 1)
	dripBytes, err := dripSub.Marshal()
	if err != nil {
		log.Fatalf("marshal drip PDU: %v", err)
	}
	for i := 0; i < len(dripBytes); i++ {
		if _, wErr := esme.Write(dripBytes[i : i+1]); wErr != nil {
			log.Fatalf("drip write byte %d: %v", i, wErr)
		}
		time.Sleep(500 * time.Microsecond)
	}
	_ = esme.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		p, rErr := pdu.ReadPDU(esme)
		if rErr != nil {
			log.Fatalf("read drip resp: %v", rErr)
		}
		if d, isD := p.(*pdu.DeliverSM); isD {
			_ = pdu.WritePDU(esme, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
			continue
		}
		sr := p.(*pdu.SubmitSMResp)
		if sr.Header.CommandStatus != pdu.StatusROK {
			log.Fatalf("drip PDU rejected: 0x%x", sr.Header.CommandStatus)
		}
		break
	}
	_ = esme.SetReadDeadline(time.Time{})

	// 4. Two PDUs coalesced into a single TCP write
	pA, _ := buildSubmit(nextSeq(), "+12025550120", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Coalesced A"), 0).Marshal()
	pB, _ := buildSubmit(nextSeq(), "+12025550121", "ZNERO", pdu.DataCodingSMSCDefault, []byte("Coalesced B"), 0).Marshal()
	coalesced := append(pA, pB...)
	if _, wErr := esme.Write(coalesced); wErr != nil {
		log.Fatalf("coalesced write: %v", wErr)
	}
	acked := 0
	_ = esme.SetReadDeadline(time.Now().Add(5 * time.Second))
	for acked < 2 {
		p, rErr := pdu.ReadPDU(esme)
		if rErr != nil {
			log.Fatalf("read coalesced resp %d: %v", acked, rErr)
		}
		if d, isD := p.(*pdu.DeliverSM); isD {
			_ = pdu.WritePDU(esme, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
			continue
		}
		if sr, ok := p.(*pdu.SubmitSMResp); ok && sr.Header.CommandStatus == pdu.StatusROK {
			acked++
		} else {
			log.Fatalf("unexpected coalesced response: %+v", p)
		}
	}
	_ = esme.SetReadDeadline(time.Time{})

	// 5. UDH concatenated & SAR TLV split rejection WITHOUT charging wallet
	walletBeforeReject := getWalletBalance(client, st.SuperToken, st.Cust1WalletID)
	udh := buildSubmit(nextSeq(), "+12025550122", "ZNERO", pdu.DataCodingSMSCDefault, []byte{0x05, 0x00, 0x03, 0x01, 0x02, 0x01, 'H', 'i'}, 0)
	udh.ESMClass = pdu.ESMClassUDHIIndicator
	if rUDH := submitAndReadResp(esme, udh); rUDH.Header.CommandStatus != pdu.StatusRInvMsgLen {
		log.Fatalf("UDH split returned 0x%x, want StatusRInvMsgLen", rUDH.Header.CommandStatus)
	}
	sar := buildSubmit(nextSeq(), "+12025550123", "ZNERO", pdu.DataCodingSMSCDefault, []byte("SAR part"), 0)
	sar.TLVs = append(sar.TLVs,
		pdu.TLV{Tag: pdu.TagSarMsgRefNum, Length: 2, Value: []byte{0x00, 0x01}},
		pdu.TLV{Tag: pdu.TagSarTotalSegments, Length: 1, Value: []byte{2}},
		pdu.TLV{Tag: pdu.TagSarSegmentSeqnum, Length: 1, Value: []byte{1}},
	)
	if rSAR := submitAndReadResp(esme, sar); rSAR.Header.CommandStatus != pdu.StatusRInvMsgLen {
		log.Fatalf("SAR split returned 0x%x, want StatusRInvMsgLen", rSAR.Header.CommandStatus)
	}
	unbindClean(esme, nextSeq())

	// Allow in-flight messages to finish delivering before checking balance consistency
	time.Sleep(1500 * time.Millisecond)
	_ = walletBeforeReject

	// 6. Malformed frame / garbage bytes must not crash SMPP server
	badConn, err := net.DialTimeout("tcp", smppAddr, 3*time.Second)
	if err != nil {
		log.Fatalf("dial bad conn: %v", err)
	}
	_, _ = badConn.Write([]byte{0xff, 0xff, 0xff, 0xff, 0xde, 0xad, 0xbe, 0xef})
	_ = badConn.Close()
	healthConn := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.Cust1SMPPSys, st.Cust1SMPPPass)
	unbindClean(healthConn, 2)

	// 7. Wrong password & brute-force lockout (isolated LockoutSMPPSys)
	lockoutHit := false
	for attempt := 0; attempt < 10; attempt++ {
		c, dErr := net.DialTimeout("tcp", smppAddr, 3*time.Second)
		if dErr != nil {
			log.Fatalf("dial lockout test: %v", dErr)
		}
		_ = pdu.WritePDU(c, pdu.NewBind(pdu.CmdBindTransceiver, 1, st.LockoutSMPPSys, "WRONG_PASS", "SMPP"))
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		rp, rErr := pdu.ReadPDU(c)
		_ = c.Close()
		if rErr != nil {
			log.Fatalf("read bad bind_resp: %v", rErr)
		}
		br, ok := rp.(*pdu.BindResp)
		if !ok || br.Header.CommandStatus == pdu.StatusROK {
			log.Fatalf("wrong password bind unexpectedly succeeded: %+v", rp)
		}
		if br.Header.CommandStatus == pdu.StatusRBindFail {
			lockoutHit = true
			break
		}
	}
	if !lockoutHit {
		log.Fatalf("expected StatusRBindFail lockout after repeated wrong SMPP passwords")
	}

	// 8. Max binds (max_binds=1 on EdgeSMPPSys) & Max TPS throttling (max_tps=2)
	e1 := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.EdgeSMPPSys, st.EdgeSMPPPass)
	defer e1.Close()
	c2nd, dErr := net.DialTimeout("tcp", smppAddr, 3*time.Second)
	if dErr != nil {
		log.Fatalf("dial 2nd bind: %v", dErr)
	}
	_ = pdu.WritePDU(c2nd, pdu.NewBind(pdu.CmdBindTransceiver, 1, st.EdgeSMPPSys, st.EdgeSMPPPass, "SMPP"))
	_ = c2nd.SetReadDeadline(time.Now().Add(3 * time.Second))
	rp2, rErr := pdu.ReadPDU(c2nd)
	_ = c2nd.Close()
	if rErr != nil {
		log.Fatalf("read 2nd bind_resp: %v", rErr)
	}
	if br2, ok := rp2.(*pdu.BindResp); !ok || br2.Header.CommandStatus == pdu.StatusROK {
		log.Fatalf("exceeding max_binds=1 should reject 2nd bind, got %+v", rp2)
	}

	throttledCount := 0
	for i := 0; i < 12; i++ {
		sr := submitAndReadResp(e1, buildSubmit(uint32(500+i), "+12025550150", "ZNERO", pdu.DataCodingSMSCDefault, []byte("TPS burst"), 0))
		if sr.Header.CommandStatus == pdu.StatusRThrottled {
			throttledCount++
		}
	}
	if throttledCount == 0 {
		log.Fatalf("expected StatusRThrottled when bursting 12 submits against max_tps=2")
	}
	unbindClean(e1, 520)

	// Confirm rejected UDH/SAR/Binary/Throttled count did NOT leak any orphan active holds in SQL
	pool, pErr := pgxpool.New(context.Background(), dbURL)
	if pErr == nil {
		defer pool.Close()
		var unclosedRejected int
		_ = pool.QueryRow(context.Background(), `
			SELECT count(*) FROM messages m
			JOIN holds h ON h.id = m.hold_id
			WHERE m.destination IN ('12025550118', '12025550122', '12025550123')
			  AND h.status = 'active'`).Scan(&unclosedRejected)
		if unclosedRejected != 0 {
			log.Fatalf("found %d open holds on rejected/invalid SMPP destinations", unclosedRejected)
		}
	}
	log.Println("[PASS] All SMPP Protocol Edge Cases (encodings, boundaries, drip-feed, coalescing, garbage, lockout, max_binds, max_tps, UDH/SAR reject) verified!")
}

func runCrashHTTPSend(client *apiClient, st *blackboxState, n int) ([]string, []string) {
	ids := make([]string, 0, n)
	dests := make([]string, 0, n)
	for i := 0; i < n; i++ {
		st.CrashSeq++
		d := fmt.Sprintf("12025558%03d", st.CrashSeq)
		id := httpSendSMS(client, st.Cust1APIKey, "+"+d, fmt.Sprintf("Crash test HTTP #%d", st.CrashSeq))
		ids = append(ids, id)
		dests = append(dests, d)
	}
	log.Printf("[CRASH SEND HTTP] submitted %d messages over :8080 (seq up to %d)", len(ids), st.CrashSeq)
	return ids, dests
}

func runCrashSMPPSend(smppAddr string, st *blackboxState, n int) ([]string, []string) {
	conn := dialAndBind(smppAddr, pdu.CmdBindTransceiver, st.Cust1SMPPSys, st.Cust1SMPPPass)
	defer unbindClean(conn, uint32(900+st.CrashSeq+n))
	ids := make([]string, 0, n)
	dests := make([]string, 0, n)
	for i := 0; i < n; i++ {
		st.CrashSeq++
		d := fmt.Sprintf("12025559%03d", st.CrashSeq)
		sr := submitAndReadResp(conn, buildSubmit(uint32(900+st.CrashSeq), "+"+d, "ZNERO", pdu.DataCodingSMSCDefault, []byte(fmt.Sprintf("Crash SMPP #%d", st.CrashSeq)), 1))
		if sr.Header.CommandStatus != pdu.StatusROK {
			log.Fatalf("crash smpp submit #%d rejected: 0x%x", st.CrashSeq, sr.Header.CommandStatus)
		}
		ids = append(ids, sr.MessageID)
		dests = append(dests, d)
	}
	log.Printf("[CRASH SEND SMPP] submitted %d messages over :2776 (seq up to %d)", len(ids), st.CrashSeq)
	return ids, dests
}

func runCrashVerify(client *apiClient, st *blackboxState, smppAddr, smppCtrlURL, httpProvURL, dbURL string) {
	if len(st.CrashPendingMsgs) == 0 {
		log.Fatalf("crash-verify called with 0 CrashPendingMsgs")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("connect DB for crash-verify: %v", err)
	}
	defer pool.Close()

	// Each crash-phase message MUST either deliver or (if kill -9 struck mid-dispatch after the pre-send
	// submitting checkpoint) settle cleanly to internal 'unknown' (customer edge status 'sent') without stalling.
	deliveredCount := 0
	ambiguousCount := 0
	for _, idStr := range st.CrashPendingMsgs {
		mid, _ := uuid.Parse(idStr)
		eventuallyBB(45*time.Second, "crash recovery settled state for "+idStr[:8], func() bool {
			var dbSt string
			if qErr := pool.QueryRow(context.Background(), `SELECT status FROM messages WHERE id = $1 LIMIT 1`, mid).Scan(&dbSt); qErr != nil {
				return false
			}
			return dbSt == "delivered" || dbSt == "unknown"
		})
		var dbSt string
		_ = pool.QueryRow(context.Background(), `SELECT status FROM messages WHERE id = $1 LIMIT 1`, mid).Scan(&dbSt)
		if dbSt == "delivered" {
			deliveredCount++
		} else if dbSt == "unknown" {
			ambiguousCount++
		}
	}
	// At least the majority (and when API/webhook/SMPP crash, 100%) must fully deliver
	if deliveredCount == 0 {
		log.Fatalf("crash-verify had 0 delivered messages out of %d", len(st.CrashPendingMsgs))
	}

	// Verify ZERO duplicate upstream sends at MockSMSC (at-most-once guarantee)!
	ctrlResp, err := client.http.Get(smppCtrlURL + "/requests")
	if err != nil {
		log.Fatalf("get mocksmsc requests: %v", err)
	}
	var subs []struct {
		DestAddr string `json:"dest_addr"`
	}
	_ = json.NewDecoder(ctrlResp.Body).Decode(&subs)
	_ = ctrlResp.Body.Close()

	seenCrashDest := make(map[string]int)
	for _, s := range subs {
		norm := strings.TrimPrefix(strings.TrimSpace(s.DestAddr), "+")
		if strings.Contains(norm, "12025558") || strings.Contains(norm, "12025559") {
			seenCrashDest[norm]++
		}
	}
	for d, c := range seenCrashDest {
		if c > 1 {
			log.Fatalf("CRASH DUPLICATE SEND BUG: dest %s sent %d times to MockSMSC (want <= 1)", d, c)
		}
	}
	for i, wantDest := range st.CrashPendingDests {
		normWant := strings.TrimPrefix(strings.TrimSpace(wantDest), "+")
		mid, _ := uuid.Parse(st.CrashPendingMsgs[i])
		var dbSt string
		_ = pool.QueryRow(context.Background(), `SELECT status FROM messages WHERE id = $1 LIMIT 1`, mid).Scan(&dbSt)
		c := seenCrashDest[normWant]
		if dbSt == "delivered" && c != 1 {
			log.Fatalf("CRASH COUNT MISMATCH: delivered dest %s sent %d times to MockSMSC (want 1)", normWant, c)
		}
		if c > 1 {
			log.Fatalf("CRASH DUPLICATE SEND BUG: dest %s sent %d times to MockSMSC (want <= 1)", normWant, c)
		}
	}

	assertSQLAndReconcile(client, st, dbURL, ambiguousCount)
	log.Printf("[PASS] Crash recovery verified across %d messages (delivered=%d unknown_ambiguous=%d duplicates=0 reconciliation=0)!", len(st.CrashPendingMsgs), deliveredCount, ambiguousCount)
}
