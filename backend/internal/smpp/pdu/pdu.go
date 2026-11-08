package pdu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Header represents the 16-byte SMPP 3.4 PDU header.
type Header struct {
	CommandLength  uint32
	CommandID      uint32
	CommandStatus  uint32
	SequenceNumber uint32
}

// TLV represents a Tag-Length-Value optional parameter.
type TLV struct {
	Tag    uint16
	Length uint16
	Value  []byte
}

// PDU represents an SMPP protocol data unit.
type PDU interface {
	GetHeader() *Header
	Marshal() ([]byte, error)
	Unmarshal(body []byte) error
}

// RawPDU holds an unparsed or generic PDU.
type RawPDU struct {
	Header Header
	Body   []byte
}

func (r *RawPDU) GetHeader() *Header { return &r.Header }
func (r *RawPDU) Marshal() ([]byte, error) {
	totalLen := HeaderLen + len(r.Body)
	r.Header.CommandLength = uint32(totalLen)
	buf := make([]byte, totalLen)
	binary.BigEndian.PutUint32(buf[0:4], r.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], r.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], r.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], r.Header.SequenceNumber)
	copy(buf[16:], r.Body)
	return buf, nil
}
func (r *RawPDU) Unmarshal(body []byte) error {
	r.Body = make([]byte, len(body))
	copy(r.Body, body)
	return nil
}

// Bind represents bind_transmitter, bind_receiver, or bind_transceiver.
type Bind struct {
	Header           Header
	SystemID         string
	Password         string
	SystemType       string
	InterfaceVersion uint8
	AddrTON          uint8
	AddrNPI          uint8
	AddressRange     string
}

func NewBind(commandID uint32, seqNum uint32, systemID, password, systemType string) *Bind {
	return &Bind{
		Header: Header{
			CommandID:      commandID,
			CommandStatus:  StatusROK,
			SequenceNumber: seqNum,
		},
		SystemID:         systemID,
		Password:         password,
		SystemType:       systemType,
		InterfaceVersion: 0x34, // SMPP 3.4
	}
}

func (b *Bind) GetHeader() *Header { return &b.Header }

func (b *Bind) Marshal() ([]byte, error) {
	var body bytes.Buffer
	writeCString(&body, b.SystemID)
	writeCString(&body, b.Password)
	writeCString(&body, b.SystemType)
	body.WriteByte(b.InterfaceVersion)
	body.WriteByte(b.AddrTON)
	body.WriteByte(b.AddrNPI)
	writeCString(&body, b.AddressRange)

	totalLen := HeaderLen + body.Len()
	b.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], b.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], b.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], b.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], b.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (b *Bind) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	var err error
	if b.SystemID, err = readCString(r); err != nil {
		return err
	}
	if b.Password, err = readCString(r); err != nil {
		return err
	}
	if b.SystemType, err = readCString(r); err != nil {
		return err
	}
	if b.InterfaceVersion, err = r.ReadByte(); err != nil {
		return err
	}
	if b.AddrTON, err = r.ReadByte(); err != nil {
		return err
	}
	if b.AddrNPI, err = r.ReadByte(); err != nil {
		return err
	}
	if b.AddressRange, err = readCString(r); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// BindResp represents bind_transmitter_resp, bind_receiver_resp, or bind_transceiver_resp.
type BindResp struct {
	Header   Header
	SystemID string
	TLVs     []TLV
}

func NewBindResp(commandID uint32, status uint32, seqNum uint32, systemID string) *BindResp {
	return &BindResp{
		Header: Header{
			CommandID:      commandID,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
		SystemID: systemID,
	}
}

func (b *BindResp) GetHeader() *Header { return &b.Header }

func (b *BindResp) Marshal() ([]byte, error) {
	var body bytes.Buffer
	if b.Header.CommandStatus == StatusROK || b.SystemID != "" {
		writeCString(&body, b.SystemID)
	}
	for _, tlv := range b.TLVs {
		writeTLV(&body, tlv)
	}

	totalLen := HeaderLen + body.Len()
	b.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], b.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], b.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], b.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], b.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (b *BindResp) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	if r.Len() > 0 {
		var err error
		if b.SystemID, err = readCString(r); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		b.TLVs = readTLVs(r)
	}
	return nil
}

// Unbind represents an unbind request.
type Unbind struct {
	Header Header
}

func NewUnbind(seqNum uint32) *Unbind {
	return &Unbind{
		Header: Header{
			CommandLength:  HeaderLen,
			CommandID:      CmdUnbind,
			CommandStatus:  StatusROK,
			SequenceNumber: seqNum,
		},
	}
}

func (u *Unbind) GetHeader() *Header { return &u.Header }
func (u *Unbind) Marshal() ([]byte, error) {
	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], HeaderLen)
	binary.BigEndian.PutUint32(buf[4:8], u.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], u.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], u.Header.SequenceNumber)
	return buf, nil
}
func (u *Unbind) Unmarshal(body []byte) error { return nil }

// UnbindResp represents an unbind response.
type UnbindResp struct {
	Header Header
}

func NewUnbindResp(status uint32, seqNum uint32) *UnbindResp {
	return &UnbindResp{
		Header: Header{
			CommandLength:  HeaderLen,
			CommandID:      CmdUnbindResp,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
	}
}

func (u *UnbindResp) GetHeader() *Header { return &u.Header }
func (u *UnbindResp) Marshal() ([]byte, error) {
	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], HeaderLen)
	binary.BigEndian.PutUint32(buf[4:8], u.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], u.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], u.Header.SequenceNumber)
	return buf, nil
}
func (u *UnbindResp) Unmarshal(body []byte) error { return nil }

// EnquireLink represents an enquire_link heartbeat request.
type EnquireLink struct {
	Header Header
}

func NewEnquireLink(seqNum uint32) *EnquireLink {
	return &EnquireLink{
		Header: Header{
			CommandLength:  HeaderLen,
			CommandID:      CmdEnquireLink,
			CommandStatus:  StatusROK,
			SequenceNumber: seqNum,
		},
	}
}

func (e *EnquireLink) GetHeader() *Header { return &e.Header }
func (e *EnquireLink) Marshal() ([]byte, error) {
	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], HeaderLen)
	binary.BigEndian.PutUint32(buf[4:8], e.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], e.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], e.Header.SequenceNumber)
	return buf, nil
}
func (e *EnquireLink) Unmarshal(body []byte) error { return nil }

// EnquireLinkResp represents an enquire_link heartbeat response.
type EnquireLinkResp struct {
	Header Header
}

func NewEnquireLinkResp(status uint32, seqNum uint32) *EnquireLinkResp {
	return &EnquireLinkResp{
		Header: Header{
			CommandLength:  HeaderLen,
			CommandID:      CmdEnquireLinkResp,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
	}
}

func (e *EnquireLinkResp) GetHeader() *Header { return &e.Header }
func (e *EnquireLinkResp) Marshal() ([]byte, error) {
	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], HeaderLen)
	binary.BigEndian.PutUint32(buf[4:8], e.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], e.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], e.Header.SequenceNumber)
	return buf, nil
}
func (e *EnquireLinkResp) Unmarshal(body []byte) error { return nil }

// SubmitSM represents a submit_sm PDU.
type SubmitSM struct {
	Header               Header
	ServiceType          string
	SourceAddrTON        uint8
	SourceAddrNPI        uint8
	SourceAddr           string
	DestAddrTON          uint8
	DestAddrNPI          uint8
	DestAddr             string
	ESMClass             uint8
	ProtocolID           uint8
	PriorityFlag         uint8
	ScheduleDeliveryTime string
	ValidityPeriod       string
	RegisteredDelivery   uint8
	ReplaceIfPresent     uint8
	DataCoding           uint8
	SMDefaultMsgID       uint8
	ShortMessage         []byte
	TLVs                 []TLV
}

func NewSubmitSM(seqNum uint32) *SubmitSM {
	return &SubmitSM{
		Header: Header{
			CommandID:      CmdSubmitSM,
			CommandStatus:  StatusROK,
			SequenceNumber: seqNum,
		},
		RegisteredDelivery: RegDeliverySMSCDeliveryReceipt,
	}
}

func (s *SubmitSM) GetHeader() *Header { return &s.Header }

func (s *SubmitSM) Marshal() ([]byte, error) {
	var body bytes.Buffer
	writeCString(&body, s.ServiceType)
	body.WriteByte(s.SourceAddrTON)
	body.WriteByte(s.SourceAddrNPI)
	writeCString(&body, s.SourceAddr)
	body.WriteByte(s.DestAddrTON)
	body.WriteByte(s.DestAddrNPI)
	writeCString(&body, s.DestAddr)
	body.WriteByte(s.ESMClass)
	body.WriteByte(s.ProtocolID)
	body.WriteByte(s.PriorityFlag)
	writeCString(&body, s.ScheduleDeliveryTime)
	writeCString(&body, s.ValidityPeriod)
	body.WriteByte(s.RegisteredDelivery)
	body.WriteByte(s.ReplaceIfPresent)
	body.WriteByte(s.DataCoding)
	body.WriteByte(s.SMDefaultMsgID)

	smLen := len(s.ShortMessage)
	if smLen > 255 {
		return nil, fmt.Errorf("short_message length %d exceeds 255 bytes", smLen)
	}
	body.WriteByte(uint8(smLen))
	body.Write(s.ShortMessage)

	for _, tlv := range s.TLVs {
		writeTLV(&body, tlv)
	}

	totalLen := HeaderLen + body.Len()
	s.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], s.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], s.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], s.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], s.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (s *SubmitSM) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	var err error
	if s.ServiceType, err = readCString(r); err != nil {
		return err
	}
	if s.SourceAddrTON, err = r.ReadByte(); err != nil {
		return err
	}
	if s.SourceAddrNPI, err = r.ReadByte(); err != nil {
		return err
	}
	if s.SourceAddr, err = readCString(r); err != nil {
		return err
	}
	if s.DestAddrTON, err = r.ReadByte(); err != nil {
		return err
	}
	if s.DestAddrNPI, err = r.ReadByte(); err != nil {
		return err
	}
	if s.DestAddr, err = readCString(r); err != nil {
		return err
	}
	if s.ESMClass, err = r.ReadByte(); err != nil {
		return err
	}
	if s.ProtocolID, err = r.ReadByte(); err != nil {
		return err
	}
	if s.PriorityFlag, err = r.ReadByte(); err != nil {
		return err
	}
	if s.ScheduleDeliveryTime, err = readCString(r); err != nil {
		return err
	}
	if s.ValidityPeriod, err = readCString(r); err != nil {
		return err
	}
	if s.RegisteredDelivery, err = r.ReadByte(); err != nil {
		return err
	}
	if s.ReplaceIfPresent, err = r.ReadByte(); err != nil {
		return err
	}
	if s.DataCoding, err = r.ReadByte(); err != nil {
		return err
	}
	if s.SMDefaultMsgID, err = r.ReadByte(); err != nil {
		return err
	}
	smLen, err := r.ReadByte()
	if err != nil {
		return err
	}
	if smLen > 0 {
		s.ShortMessage = make([]byte, smLen)
		if _, err := io.ReadFull(r, s.ShortMessage); err != nil {
			return err
		}
	}
	s.TLVs = readTLVs(r)
	return nil
}

// SubmitSMResp represents a submit_sm_resp PDU.
type SubmitSMResp struct {
	Header    Header
	MessageID string
	TLVs      []TLV
}

func NewSubmitSMResp(status uint32, seqNum uint32, messageID string) *SubmitSMResp {
	return &SubmitSMResp{
		Header: Header{
			CommandID:      CmdSubmitSMResp,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
		MessageID: messageID,
	}
}

func (s *SubmitSMResp) GetHeader() *Header { return &s.Header }

func (s *SubmitSMResp) Marshal() ([]byte, error) {
	var body bytes.Buffer
	if s.Header.CommandStatus == StatusROK || s.MessageID != "" {
		writeCString(&body, s.MessageID)
	}
	for _, tlv := range s.TLVs {
		writeTLV(&body, tlv)
	}

	totalLen := HeaderLen + body.Len()
	s.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], s.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], s.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], s.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], s.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (s *SubmitSMResp) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	if r.Len() > 0 {
		var err error
		if s.MessageID, err = readCString(r); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		s.TLVs = readTLVs(r)
	}
	return nil
}

// DeliverSM represents a deliver_sm PDU (used for incoming messages or delivery receipts).
type DeliverSM struct {
	Header               Header
	ServiceType          string
	SourceAddrTON        uint8
	SourceAddrNPI        uint8
	SourceAddr           string
	DestAddrTON          uint8
	DestAddrNPI          uint8
	DestAddr             string
	ESMClass             uint8
	ProtocolID           uint8
	PriorityFlag         uint8
	ScheduleDeliveryTime string
	ValidityPeriod       string
	RegisteredDelivery   uint8
	ReplaceIfPresent     uint8
	DataCoding           uint8
	SMDefaultMsgID       uint8
	ShortMessage         []byte
	TLVs                 []TLV
}

func NewDeliverSM(seqNum uint32) *DeliverSM {
	return &DeliverSM{
		Header: Header{
			CommandID:      CmdDeliverSM,
			CommandStatus:  StatusROK,
			SequenceNumber: seqNum,
		},
	}
}

func (d *DeliverSM) GetHeader() *Header { return &d.Header }

func (d *DeliverSM) Marshal() ([]byte, error) {
	var body bytes.Buffer
	writeCString(&body, d.ServiceType)
	body.WriteByte(d.SourceAddrTON)
	body.WriteByte(d.SourceAddrNPI)
	writeCString(&body, d.SourceAddr)
	body.WriteByte(d.DestAddrTON)
	body.WriteByte(d.DestAddrNPI)
	writeCString(&body, d.DestAddr)
	body.WriteByte(d.ESMClass)
	body.WriteByte(d.ProtocolID)
	body.WriteByte(d.PriorityFlag)
	writeCString(&body, d.ScheduleDeliveryTime)
	writeCString(&body, d.ValidityPeriod)
	body.WriteByte(d.RegisteredDelivery)
	body.WriteByte(d.ReplaceIfPresent)
	body.WriteByte(d.DataCoding)
	body.WriteByte(d.SMDefaultMsgID)

	smLen := len(d.ShortMessage)
	if smLen > 255 {
		return nil, fmt.Errorf("short_message length %d exceeds 255 bytes", smLen)
	}
	body.WriteByte(uint8(smLen))
	body.Write(d.ShortMessage)

	for _, tlv := range d.TLVs {
		writeTLV(&body, tlv)
	}

	totalLen := HeaderLen + body.Len()
	d.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], d.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], d.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], d.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], d.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (d *DeliverSM) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	var err error
	if d.ServiceType, err = readCString(r); err != nil {
		return err
	}
	if d.SourceAddrTON, err = r.ReadByte(); err != nil {
		return err
	}
	if d.SourceAddrNPI, err = r.ReadByte(); err != nil {
		return err
	}
	if d.SourceAddr, err = readCString(r); err != nil {
		return err
	}
	if d.DestAddrTON, err = r.ReadByte(); err != nil {
		return err
	}
	if d.DestAddrNPI, err = r.ReadByte(); err != nil {
		return err
	}
	if d.DestAddr, err = readCString(r); err != nil {
		return err
	}
	if d.ESMClass, err = r.ReadByte(); err != nil {
		return err
	}
	if d.ProtocolID, err = r.ReadByte(); err != nil {
		return err
	}
	if d.PriorityFlag, err = r.ReadByte(); err != nil {
		return err
	}
	if d.ScheduleDeliveryTime, err = readCString(r); err != nil {
		return err
	}
	if d.ValidityPeriod, err = readCString(r); err != nil {
		return err
	}
	if d.RegisteredDelivery, err = r.ReadByte(); err != nil {
		return err
	}
	if d.ReplaceIfPresent, err = r.ReadByte(); err != nil {
		return err
	}
	if d.DataCoding, err = r.ReadByte(); err != nil {
		return err
	}
	if d.SMDefaultMsgID, err = r.ReadByte(); err != nil {
		return err
	}
	smLen, err := r.ReadByte()
	if err != nil {
		return err
	}
	if smLen > 0 {
		d.ShortMessage = make([]byte, smLen)
		if _, err := io.ReadFull(r, d.ShortMessage); err != nil {
			return err
		}
	}
	d.TLVs = readTLVs(r)
	return nil
}

// DeliverSMResp represents a deliver_sm_resp PDU.
type DeliverSMResp struct {
	Header    Header
	MessageID string
}

func NewDeliverSMResp(status uint32, seqNum uint32) *DeliverSMResp {
	return &DeliverSMResp{
		Header: Header{
			CommandID:      CmdDeliverSMResp,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
		MessageID: "",
	}
}

func (d *DeliverSMResp) GetHeader() *Header { return &d.Header }

func (d *DeliverSMResp) Marshal() ([]byte, error) {
	var body bytes.Buffer
	writeCString(&body, d.MessageID)

	totalLen := HeaderLen + body.Len()
	d.Header.CommandLength = uint32(totalLen)

	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], d.Header.CommandLength)
	binary.BigEndian.PutUint32(buf[4:8], d.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], d.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], d.Header.SequenceNumber)

	return append(buf, body.Bytes()...), nil
}

func (d *DeliverSMResp) Unmarshal(body []byte) error {
	r := bytes.NewReader(body)
	if r.Len() > 0 {
		var err error
		if d.MessageID, err = readCString(r); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

// GenericNack represents a generic_nack PDU.
type GenericNackPDU struct {
	Header Header
}

func NewGenericNack(status uint32, seqNum uint32) *GenericNackPDU {
	return &GenericNackPDU{
		Header: Header{
			CommandLength:  HeaderLen,
			CommandID:      CmdGenericNack,
			CommandStatus:  status,
			SequenceNumber: seqNum,
		},
	}
}

func (g *GenericNackPDU) GetHeader() *Header { return &g.Header }
func (g *GenericNackPDU) Marshal() ([]byte, error) {
	buf := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(buf[0:4], HeaderLen)
	binary.BigEndian.PutUint32(buf[4:8], g.Header.CommandID)
	binary.BigEndian.PutUint32(buf[8:12], g.Header.CommandStatus)
	binary.BigEndian.PutUint32(buf[12:16], g.Header.SequenceNumber)
	return buf, nil
}
func (g *GenericNackPDU) Unmarshal(body []byte) error { return nil }

// ReadPDU reads a full SMPP PDU from an io.Reader and decodes it into a concrete PDU.
func ReadPDU(r io.Reader) (PDU, error) {
	hdrBuf := make([]byte, HeaderLen)
	if _, err := io.ReadFull(r, hdrBuf); err != nil {
		return nil, err
	}

	hdr := Header{
		CommandLength:  binary.BigEndian.Uint32(hdrBuf[0:4]),
		CommandID:      binary.BigEndian.Uint32(hdrBuf[4:8]),
		CommandStatus:  binary.BigEndian.Uint32(hdrBuf[8:12]),
		SequenceNumber: binary.BigEndian.Uint32(hdrBuf[12:16]),
	}

	if hdr.CommandLength < HeaderLen {
		return nil, fmt.Errorf("invalid command_length %d (less than header %d)", hdr.CommandLength, HeaderLen)
	}
	if hdr.CommandLength > 65536 {
		return nil, fmt.Errorf("command_length %d exceeds maximum safe limit 64KB", hdr.CommandLength)
	}

	bodyLen := int(hdr.CommandLength - HeaderLen)
	var body []byte
	if bodyLen > 0 {
		body = make([]byte, bodyLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
	}

	var pdu PDU
	switch hdr.CommandID {
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver:
		pdu = &Bind{Header: hdr}
	case CmdBindTransmitterResp, CmdBindReceiverResp, CmdBindTransceiverResp:
		pdu = &BindResp{Header: hdr}
	case CmdUnbind:
		pdu = &Unbind{Header: hdr}
	case CmdUnbindResp:
		pdu = &UnbindResp{Header: hdr}
	case CmdEnquireLink:
		pdu = &EnquireLink{Header: hdr}
	case CmdEnquireLinkResp:
		pdu = &EnquireLinkResp{Header: hdr}
	case CmdSubmitSM:
		pdu = &SubmitSM{Header: hdr}
	case CmdSubmitSMResp:
		pdu = &SubmitSMResp{Header: hdr}
	case CmdDeliverSM:
		pdu = &DeliverSM{Header: hdr}
	case CmdDeliverSMResp:
		pdu = &DeliverSMResp{Header: hdr}
	case CmdGenericNack:
		pdu = &GenericNackPDU{Header: hdr}
	default:
		pdu = &RawPDU{Header: hdr, Body: body}
		return pdu, nil
	}

	if err := pdu.Unmarshal(body); err != nil {
		return nil, fmt.Errorf("error unmarshaling pdu %x: %w", hdr.CommandID, err)
	}
	return pdu, nil
}

// WritePDU serializes and writes a PDU to an io.Writer.
func WritePDU(w io.Writer, pdu PDU) error {
	data, err := pdu.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Helper functions for C-strings and TLVs
func writeCString(buf *bytes.Buffer, s string) {
	buf.WriteString(s)
	buf.WriteByte(0x00)
}

func readCString(r *bytes.Reader) (string, error) {
	var b bytes.Buffer
	for {
		c, err := r.ReadByte()
		if err != nil {
			return b.String(), err
		}
		if c == 0x00 {
			break
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

func writeTLV(buf *bytes.Buffer, tlv TLV) {
	_ = binary.Write(buf, binary.BigEndian, tlv.Tag)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(tlv.Value)))
	buf.Write(tlv.Value)
}

func readTLVs(r *bytes.Reader) []TLV {
	var tlvs []TLV
	for r.Len() >= 4 {
		var tag uint16
		var length uint16
		if err := binary.Read(r, binary.BigEndian, &tag); err != nil {
			break
		}
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			break
		}
		if int(length) > r.Len() {
			break
		}
		val := make([]byte, length)
		if _, err := io.ReadFull(r, val); err != nil {
			break
		}
		tlvs = append(tlvs, TLV{
			Tag:    tag,
			Length: length,
			Value:  val,
		})
	}
	return tlvs
}

// GetTLV searches for a TLV with the given tag.
func GetTLV(tlvs []TLV, tag uint16) *TLV {
	for i := range tlvs {
		if tlvs[i].Tag == tag {
			return &tlvs[i]
		}
	}
	return nil
}
