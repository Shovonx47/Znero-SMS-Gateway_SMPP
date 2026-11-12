package customeredge

import "testing"

func TestEdgeAuthenticator_IsLockedOut_After5Failures(t *testing.T) {
	a := NewEdgeAuthenticator(nil, nil, nil)
	key := "127.0.0.1|znk_live_testpfx1"
	for i := 1; i <= 4; i++ {
		if a.isLockedOut(key) {
			t.Fatalf("unexpected lockout on attempt %d", i)
		}
		a.recordFailure(key)
	}
	if a.isLockedOut(key) {
		t.Fatalf("unexpected lockout after 4 failures")
	}
	a.recordFailure(key) // 5th failure sets 30s lockout
	if !a.isLockedOut(key) {
		t.Fatalf("expected lockout after 5 consecutive failures (BUG-031)")
	}
	if a.isLockedOut("127.0.0.1|znk_live_otherpfx") {
		t.Fatalf("lockout must stay isolated per IP+prefix")
	}
}
