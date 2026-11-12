package smppserver

import (
	"testing"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func TestDecodeSubmitPayload_DataCoding(t *testing.T) {
	// GSM-7 default + 0x1B 0x65 (€ extension)
	gsmBytes := []byte{'H', 'i', ' ', 0x1B, 0x65}
	got, status := decodeSubmitPayload(gsmBytes, pdu.DataCodingSMSCDefault)
	if status != pdu.StatusROK || got != "Hi €" {
		t.Fatalf("GSM-7 decode got (%q, %#x), want (%q, 0)", got, status, "Hi €")
	}

	// UCS-2 UTF-16BE (Bangla + Arabic + Emoji)
	var ucsBytes []byte
	for _, r := range "ধন্যবাদ مرحبا 👋" {
		switch {
		case r <= 0xFFFF:
			ucsBytes = append(ucsBytes, byte(r>>8), byte(r))
		default:
			hi, lo := rune(0xD800+((r-0x10000)>>10)), rune(0xDC00+((r-0x10000)&0x3FF))
			ucsBytes = append(ucsBytes, byte(hi>>8), byte(hi), byte(lo>>8), byte(lo))
		}
	}
	got, status = decodeSubmitPayload(ucsBytes, pdu.DataCodingUCS2)
	if status != pdu.StatusROK || got != "ধন্যবাদ مرحبا 👋" {
		t.Fatalf("UCS-2 decode got (%q, %#x)", got, status)
	}

	// Odd-byte UCS-2 rejected with StatusRInvMsgLen
	if _, st := decodeSubmitPayload([]byte{0x00, 0x41, 0x00}, pdu.DataCodingUCS2); st != pdu.StatusRInvMsgLen {
		t.Fatalf("odd UCS-2 status=%#x, want StatusRInvMsgLen", st)
	}

	// Latin-1 (0x03)
	if g, st := decodeSubmitPayload([]byte{'C', 'a', 'f', 0xE9}, 0x03); st != pdu.StatusROK || g != "Café" {
		t.Fatalf("Latin-1 decode got (%q, %#x)", g, st)
	}

	// Unsupported binary DCS (0x04) rejected cleanly
	if _, st := decodeSubmitPayload([]byte{0xFF, 0xFE, 0x00, 0x01}, 0x04); st != pdu.StatusRInvDCS {
		t.Fatalf("binary DCS status=%#x, want StatusRInvDCS", st)
	}
}

func TestBindLimiter_LocksOutAfter5Failures(t *testing.T) {
	lim := NewBindLimiter()
	ip := "127.0.0.1"
	sys := "test_esme"
	for i := 1; i <= 4; i++ {
		if lim.IsLockedOut(ip, sys) {
			t.Fatalf("unexpected lockout on attempt %d before threshold", i)
		}
		lim.RecordFailure(ip, sys)
	}
	if lim.IsLockedOut(ip, sys) {
		t.Fatalf("unexpected lockout before 5th failure")
	}
	lim.RecordFailure(ip, sys) // 5th failure triggers 30s backoff
	if !lim.IsLockedOut(ip, sys) {
		t.Fatalf("expected lockout after 5 consecutive failures (BUG-030)")
	}
	// Isolated per system_id + IP
	if lim.IsLockedOut(ip, "other_esme") {
		t.Fatalf("lockout leaked to other_esme on same IP")
	}
	lim.RecordSuccess(ip, sys)
	if lim.IsLockedOut(ip, sys) {
		t.Fatalf("expected RecordSuccess to clear lockout")
	}
}
