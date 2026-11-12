package messaging_test

import (
	"testing"

	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func FuzzParseDLRReceipt(f *testing.F) {
	seeds := [][]byte{
		[]byte("id:12345 sub:001 dlvrd:001 submit date:2611120400 done date:2611120401 stat:DELIVRD err:000 text:hello"),
		[]byte("id:msg-abc-999 stat:UNDELIV err:045"),
		[]byte("id:00000042 sub:001 dlvrd:000 submit date:2611120400 done date:2611120400 stat:EXPIRED err:255 text:"),
		[]byte("id:ABCDEF stat:REJECTD err:011"),
		[]byte(""),
		[]byte("id:stat:err:text:"),
		[]byte("\x00\xff\xfeid:123 stat:DELIVRD"),
	}
	for _, s := range seeds {
		f.Add(s, uint16(0x001E), s)
	}

	f.Fuzz(func(t *testing.T, shortMsg []byte, tag uint16, val []byte) {
		if len(shortMsg) > 4096 || len(val) > 1024 {
			return
		}
		tlvs := []pdu.TLV{
			{Tag: tag, Length: uint16(len(val)), Value: val},
			{Tag: 0x0427, Length: 1, Value: []byte{byte(len(shortMsg) % 8)}},
		}
		receipt, err := messaging.ParseDLRReceipt(shortMsg, tlvs)
		if err != nil {
			return
		}
		if receipt != nil {
			_ = messaging.CandidateProviderMessageIDs(receipt.ProviderMessageID)
		}
	})
}
