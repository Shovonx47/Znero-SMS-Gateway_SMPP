package pdu

import (
	"bytes"
	"testing"
)

func TestPDU_BindAndResp(t *testing.T) {
	bind := NewBind(CmdBindTransceiver, 101, "my_system", "s3cret", "SMPP")
	bind.AddrTON = TONInternational
	bind.AddrNPI = NPIISDN
	bind.AddressRange = "^44.*"

	data, err := bind.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	pdu, err := ReadPDU(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPDU failed: %v", err)
	}

	b, ok := pdu.(*Bind)
	if !ok {
		t.Fatalf("expected *Bind, got %T", pdu)
	}
	if b.Header.CommandID != CmdBindTransceiver || b.Header.SequenceNumber != 101 {
		t.Errorf("header mismatch: %+v", b.Header)
	}
	if b.SystemID != "my_system" || b.Password != "s3cret" || b.SystemType != "SMPP" {
		t.Errorf("body mismatch: %+v", b)
	}
	if b.AddrTON != TONInternational || b.AddrNPI != NPIISDN || b.AddressRange != "^44.*" {
		t.Errorf("address mismatch: %+v", b)
	}

	resp := NewBindResp(CmdBindTransceiverResp, StatusROK, 101, "SMSC_MOCK")
	respData, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal resp failed: %v", err)
	}
	respPdu, err := ReadPDU(bytes.NewReader(respData))
	if err != nil {
		t.Fatalf("ReadPDU resp failed: %v", err)
	}
	br, ok := respPdu.(*BindResp)
	if !ok {
		t.Fatalf("expected *BindResp, got %T", respPdu)
	}
	if br.SystemID != "SMSC_MOCK" || br.Header.CommandStatus != StatusROK {
		t.Errorf("resp mismatch: %+v", br)
	}
}

func TestPDU_SubmitSMAndRespWithTLVs(t *testing.T) {
	submit := NewSubmitSM(42)
	submit.SourceAddrTON = TONAlphanumeric
	submit.SourceAddr = "ZNERO"
	submit.DestAddrTON = TONInternational
	submit.DestAddrNPI = NPIISDN
	submit.DestAddr = "447700900077"
	submit.ESMClass = ESMClassUDHIIndicator
	submit.DataCoding = DataCodingUCS2
	submit.ShortMessage = []byte{0x05, 0x00, 0x03, 0xAB, 0x02, 0x01, 0x00, 'H', 0x00, 'i'}
	submit.TLVs = []TLV{
		{Tag: TagSarMsgRefNum, Length: 2, Value: []byte{0x00, 0x2A}},
		{Tag: TagSarTotalSegments, Length: 1, Value: []byte{0x02}},
		{Tag: TagSarSegmentSeqnum, Length: 1, Value: []byte{0x01}},
	}

	data, err := submit.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	pdu, err := ReadPDU(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPDU failed: %v", err)
	}

	s, ok := pdu.(*SubmitSM)
	if !ok {
		t.Fatalf("expected *SubmitSM, got %T", pdu)
	}
	if s.Header.SequenceNumber != 42 || s.DestAddr != "447700900077" {
		t.Errorf("field mismatch: %+v", s)
	}
	if len(s.TLVs) != 3 {
		t.Fatalf("expected 3 TLVs, got %d", len(s.TLVs))
	}
	tlv := GetTLV(s.TLVs, TagSarMsgRefNum)
	if tlv == nil || len(tlv.Value) != 2 || tlv.Value[1] != 0x2A {
		t.Errorf("TLV mismatch: %+v", tlv)
	}

	// Response
	resp := NewSubmitSMResp(StatusROK, 42, "msg-123456789")
	respData, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	pduResp, err := ReadPDU(bytes.NewReader(respData))
	if err != nil {
		t.Fatalf("ReadPDU failed: %v", err)
	}
	sr, ok := pduResp.(*SubmitSMResp)
	if !ok || sr.MessageID != "msg-123456789" {
		t.Errorf("SubmitSMResp mismatch: %+v", sr)
	}
}

func TestPDU_DeliverSM(t *testing.T) {
	deliver := NewDeliverSM(999)
	deliver.SourceAddr = "447700900077"
	deliver.DestAddr = "ZNERO"
	receiptText := "id:msg-123 sub:001 dlvrd:001 submit date:2611082000 done date:2611082001 stat:DELIVRD err:000 text:Test"
	deliver.ShortMessage = []byte(receiptText)
	deliver.TLVs = []TLV{
		{Tag: TagReceiptedMessageId, Length: uint16(len("msg-123")), Value: []byte("msg-123")},
		{Tag: TagMessageState, Length: 1, Value: []byte{2}}, // 2 = DELIVRD
	}

	data, err := deliver.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	pdu, err := ReadPDU(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPDU failed: %v", err)
	}
	d, ok := pdu.(*DeliverSM)
	if !ok || string(d.ShortMessage) != receiptText {
		t.Errorf("DeliverSM mismatch: %+v", d)
	}
	tlv := GetTLV(d.TLVs, TagReceiptedMessageId)
	if tlv == nil || string(tlv.Value) != "msg-123" {
		t.Errorf("TLV receipted_message_id mismatch: %+v", tlv)
	}
}

func TestPDU_EnquireLinkAndUnbind(t *testing.T) {
	el := NewEnquireLink(7)
	data, _ := el.Marshal()
	p, err := ReadPDU(bytes.NewReader(data))
	if err != nil || p.GetHeader().CommandID != CmdEnquireLink {
		t.Fatalf("EnquireLink mismatch: %v", err)
	}

	elResp := NewEnquireLinkResp(StatusROK, 7)
	data, _ = elResp.Marshal()
	p, err = ReadPDU(bytes.NewReader(data))
	if err != nil || p.GetHeader().CommandID != CmdEnquireLinkResp {
		t.Fatalf("EnquireLinkResp mismatch: %v", err)
	}

	ub := NewUnbind(8)
	data, _ = ub.Marshal()
	p, err = ReadPDU(bytes.NewReader(data))
	if err != nil || p.GetHeader().CommandID != CmdUnbind {
		t.Fatalf("Unbind mismatch: %v", err)
	}

	ubResp := NewUnbindResp(StatusROK, 8)
	data, _ = ubResp.Marshal()
	p, err = ReadPDU(bytes.NewReader(data))
	if err != nil || p.GetHeader().CommandID != CmdUnbindResp {
		t.Fatalf("UnbindResp mismatch: %v", err)
	}
}
