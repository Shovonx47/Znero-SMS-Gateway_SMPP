package number_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/number"
)

func TestE164NormalizationAndOriginalPreservation(t *testing.T) {
	t.Parallel()

	trie := number.NewPrefixTrie()
	engine := number.NewEngine(trie)

	tests := []struct {
		name          string
		input         string
		expectedE164  string
		expectedValid bool
	}{
		{
			name:          "US format with punctuation",
			input:         "+1 (202) 555-0199",
			expectedE164:  "+12025550199",
			expectedValid: true,
		},
		{
			name:          "UK standard mobile with spaces",
			input:         "+44 7911 123456",
			expectedE164:  "+447911123456",
			expectedValid: true,
		},
		{
			name:          "Bangladesh national format with leading zero and country code",
			input:         "+880 1712-345678",
			expectedE164:  "+8801712345678",
			expectedValid: true,
		},
		{
			name:          "Germany format with spaces",
			input:         "+49 151 23456789",
			expectedE164:  "+4915123456789",
			expectedValid: true,
		},
		{
			name:          "Garbage short string",
			input:         "abc",
			expectedE164:  "",
			expectedValid: false,
		},
		{
			name:          "Too short number",
			input:         "12",
			expectedE164:  "",
			expectedValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e164, isValid, _ := engine.NormalizeE164(tc.input, "")
			if isValid != tc.expectedValid {
				t.Errorf("validity mismatch: expected %v, got %v", tc.expectedValid, isValid)
			}
			if tc.expectedValid && e164 != tc.expectedE164 {
				t.Errorf("E164 mismatch: expected %q, got %q", tc.expectedE164, e164)
			}

			// Verify Lookup preserves original number
			res := engine.Lookup(tc.input, "")
			if res.OriginalNumber != tc.input {
				t.Errorf("original number not preserved: expected %q, got %q", tc.input, res.OriginalNumber)
			}
		})
	}
}

func TestPrefixTrieLongestPrefixMatch(t *testing.T) {
	t.Parallel()

	trie := number.NewPrefixTrie()

	countryUSID := uuid.New()
	countryBDID := uuid.New()
	opGrameenphoneID := uuid.New()
	opBanglalinkID := uuid.New()

	prefixes := []prefix.NumberPrefix{
		{
			Prefix:      "1",
			CountryID:   countryUSID,
			CountryName: "United States",
			CountryISO2: "US",
			NumberType:  "fixed",
			Priority:    10,
		},
		{
			Prefix:      "880",
			CountryID:   countryBDID,
			CountryName: "Bangladesh",
			CountryISO2: "BD",
			NumberType:  "mobile",
			Priority:    10,
		},
		{
			Prefix:       "88017",
			CountryID:    countryBDID,
			CountryName:  "Bangladesh",
			CountryISO2:  "BD",
			OperatorID:   &opGrameenphoneID,
			OperatorName: "Grameenphone",
			NumberType:   "mobile",
			Priority:     100,
		},
		{
			Prefix:       "880171",
			CountryID:    countryBDID,
			CountryName:  "Bangladesh",
			CountryISO2:  "BD",
			OperatorID:   &opGrameenphoneID,
			OperatorName: "Grameenphone",
			NumberType:   "mobile",
			Priority:     150,
		},
		{
			Prefix:       "88019",
			CountryID:    countryBDID,
			CountryName:  "Bangladesh",
			CountryISO2:  "BD",
			OperatorID:   &opBanglalinkID,
			OperatorName: "Banglalink",
			NumberType:   "mobile",
			Priority:     100,
		},
	}

	trie.Reload(prefixes)

	// Test 1: +8801712345678 -> matches 880171 (longest prefix length 6)
	match, ok := trie.LongestMatch("+8801712345678")
	if !ok {
		t.Fatal("expected match for 8801712345678, got none")
	}
	if match.Prefix != "880171" {
		t.Errorf("expected longest prefix 880171, got %s", match.Prefix)
	}
	if match.OperatorName != "Grameenphone" {
		t.Errorf("expected operator Grameenphone, got %s", match.OperatorName)
	}

	// Test 2: +8801700000000 -> matches 88017 (prefix length 5)
	match, ok = trie.LongestMatch("+8801700000000")
	if !ok {
		t.Fatal("expected match for 8801700000000, got none")
	}
	if match.Prefix != "88017" {
		t.Errorf("expected prefix 88017, got %s", match.Prefix)
	}

	// Test 3: +8801812345678 (Robi, not in trie) -> falls back to 880 (country level)
	match, ok = trie.LongestMatch("+8801812345678")
	if !ok {
		t.Fatal("expected match for country level 880, got none")
	}
	if match.Prefix != "880" {
		t.Errorf("expected prefix 880, got %s", match.Prefix)
	}
	if match.OperatorID != nil {
		t.Errorf("expected nil operator for generic country prefix, got %v", match.OperatorID)
	}

	// Test 4: Completely unknown digits -> no match
	_, ok = trie.LongestMatch("+9999999999")
	if ok {
		t.Error("expected no match for unknown digits +9999999999, got match")
	}
}

func TestTransformRuleDeterministicExecution(t *testing.T) {
	t.Parallel()

	// 1. Add Prefix rule
	addRule := number.TransformRule{
		ID:           uuid.New(),
		Name:         "Add Country Code",
		Operation:    "add_prefix",
		MatchPattern: "^1[3-9]",
		Params: map[string]string{
			"prefix": "880",
		},
		Scope:     "global",
		Direction: "outbound",
		Priority:  100,
		Status:    "active",
	}

	res, applied := number.ApplyRule(addRule, "1712345678")
	if !applied {
		t.Fatal("expected rule to be applied")
	}
	if res != "8801712345678" {
		t.Errorf("expected 8801712345678, got %s", res)
	}

	// 2. Remove Prefix rule (e.g. strip leading 0)
	removeRule := number.TransformRule{
		ID:           uuid.New(),
		Name:         "Strip leading 0",
		Operation:    "remove_prefix",
		MatchPattern: "^0",
		Params: map[string]string{
			"prefix": "0",
		},
		Scope:     "global",
		Direction: "outbound",
		Priority:  200,
		Status:    "active",
	}

	res, applied = number.ApplyRule(removeRule, "01712345678")
	if !applied {
		t.Fatal("expected strip rule to apply")
	}
	if res != "1712345678" {
		t.Errorf("expected 1712345678, got %s", res)
	}

	// 3. Replace Prefix rule
	replaceRule := number.TransformRule{
		ID:           uuid.New(),
		Name:         "Replace domestic prefix",
		Operation:    "replace_prefix",
		MatchPattern: "^0",
		Params: map[string]string{
			"from_prefix": "0",
			"to_prefix":   "880",
		},
		Scope:     "global",
		Direction: "outbound",
		Priority:  300,
		Status:    "active",
	}

	res, applied = number.ApplyRule(replaceRule, "01712345678")
	if !applied {
		t.Fatal("expected replace rule to apply")
	}
	if res != "8801712345678" {
		t.Errorf("expected 8801712345678, got %s", res)
	}
}

func TestChainExecutionOrder(t *testing.T) {
	t.Parallel()

	// Chain of 2 rules:
	// Rule 1: Priority 10, strip leading 0
	// Rule 2: Priority 20, add 880 prefix
	r1 := number.TransformRule{
		ID:           uuid.New(),
		Name:         "1. Strip 0",
		Operation:    "remove_prefix",
		MatchPattern: "^0",
		Params:       map[string]string{"prefix": "0"},
		Scope:        "global",
		Direction:    "outbound",
		Priority:     10,
		Status:       "active",
	}

	r2 := number.TransformRule{
		ID:           uuid.New(),
		Name:         "2. Add 880",
		Operation:    "add_prefix",
		MatchPattern: "^1",
		Params:       map[string]string{"prefix": "880"},
		Scope:        "global",
		Direction:    "outbound",
		Priority:     20,
		Status:       "active",
	}

	rules := []number.TransformRule{r2, r1} // Unordered
	ordered := number.OrderRules(rules)

	final, steps := number.ExecuteChain(ordered, "01712345678", "outbound")
	if final != "8801712345678" {
		t.Errorf("expected final 8801712345678, got %s", final)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 execution steps, got %d", len(steps))
	}
	if steps[0].After != "1712345678" {
		t.Errorf("step 1 mismatch: %s", steps[0].After)
	}
	if steps[1].After != "8801712345678" {
		t.Errorf("step 2 mismatch: %s", steps[1].After)
	}
}

func TestBulkLookupLimit(t *testing.T) {
	t.Parallel()

	svc := &number.Service{}

	// Create 1001 numbers
	numbers := make([]string, 1001)
	for i := 0; i < 1001; i++ {
		numbers[i] = "+12025550100"
	}

	res, err := svc.BulkLookup(context.Background(), number.BulkLookupRequest{
		Numbers: numbers,
	})
	if err != number.ErrTooManyBulkNumbers {
		t.Fatalf("expected ErrTooManyBulkNumbers, got %v", err)
	}
	if res.Total != 0 {
		t.Errorf("expected 0 total results on error, got %d", res.Total)
	}
}
