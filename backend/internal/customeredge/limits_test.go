package customeredge

import (
	"testing"

	"github.com/google/uuid"
)

func TestCustomerLimiter_DistinctKeysAndDynamicLimitShrink(t *testing.T) {
	limiter := NewCustomerLimiter()
	highAcct := uuid.New()
	lowAcct := uuid.New()

	// Warm high-throughput key (500 TPS) and verify low-throughput key (2 TPS) stays isolated.
	for i := 0; i < 10; i++ {
		if ok, _ := limiter.AllowN(highAcct, 500, 1); !ok {
			t.Fatalf("high-TPS key unexpectedly throttled on submit %d", i)
		}
	}

	throttled := 0
	for i := 0; i < 6; i++ {
		if ok, _ := limiter.AllowN(lowAcct, 2, 1); !ok {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatalf("expected low-TPS key (max_tps=2) to throttle burst of 6")
	}

	// BUG-032 regression: dynamically lowering maxTPS on an already-warm bucket must clamp tokens and throttle.
	throttledAfterShrink := 0
	for i := 0; i < 6; i++ {
		if ok, _ := limiter.AllowN(highAcct, 2, 1); !ok {
			throttledAfterShrink++
		}
	}
	if throttledAfterShrink == 0 {
		t.Fatalf("expected bucket with shrunk max_tps (500 -> 2) to clamp tokens and throttle burst of 6")
	}
}
