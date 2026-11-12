package pdu_test

import (
	"bytes"
	"testing"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func FuzzPDUUnmarshal(f *testing.F) {
	if b, err := pdu.NewBind(pdu.CmdBindTransceiver, 1, "sys", "pass", "SMPP").Marshal(); err == nil {
		f.Add(b)
	}
	if b, err := pdu.NewBindResp(pdu.CmdBindTransceiverResp, pdu.StatusROK, 1, "znero").Marshal(); err == nil {
		f.Add(b)
	}
	sub := &pdu.SubmitSM{
		Header:       pdu.Header{CommandID: pdu.CmdSubmitSM, SequenceNumber: 2},
		SourceAddr:   "ZNERO",
		DestAddr:     "12025550100",
		ShortMessage: []byte("Hello world"),
		TLVs:         []pdu.TLV{{Tag: pdu.TagMessagePayload, Length: 5, Value: []byte("12345")}},
	}
	if b, err := sub.Marshal(); err == nil {
		f.Add(b)
	}
	dlr := &pdu.DeliverSM{
		Header:       pdu.Header{CommandID: pdu.CmdDeliverSM, SequenceNumber: 3},
		SourceAddr:   "12025550100",
		DestAddr:     "ZNERO",
		ESMClass:     0x04,
		ShortMessage: []byte("id:123 stat:DELIVRD err:000"),
	}
	if b, err := dlr.Marshal(); err == nil {
		f.Add(b)
	}
	if b, err := pdu.NewEnquireLink(4).Marshal(); err == nil {
		f.Add(b)
	}
	f.Add([]byte{0, 0, 0, 16, 0, 0, 0, 21, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 4})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		parsed, err := pdu.ReadPDU(bytes.NewReader(data))
		if err != nil || parsed == nil {
			return
		}
		_, _ = parsed.Marshal()
	})
}
