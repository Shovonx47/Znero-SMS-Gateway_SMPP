package number

import (
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// Engine handles phone number normalization, ITU validation, and carrier intelligence.
type Engine struct {
	trie *PrefixTrie
}

func NewEngine(trie *PrefixTrie) *Engine {
	return &Engine{trie: trie}
}

// NormalizeE164 normalizes a raw number to the international E.164 format.
// Preserves the original input number in the returned struct.
func (e *Engine) NormalizeE164(rawNumber, defaultCountryISO string) (string, bool, string) {
	trimmed := strings.TrimSpace(rawNumber)
	if trimmed == "" {
		return "", false, "empty number"
	}

	// Clean basic whitespace, dashes, parens
	cleaned := strings.ReplaceAll(trimmed, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, "(", "")
	cleaned = strings.ReplaceAll(cleaned, ")", "")
	cleaned = strings.ReplaceAll(cleaned, ".", "")

	// Parse with nyaruka/phonenumbers
	iso := strings.ToUpper(strings.TrimSpace(defaultCountryISO))
	num, err := phonenumbers.Parse(cleaned, iso)
	if err != nil {
		// If parse fails without +, try adding + if it looks like an international number
		if !strings.HasPrefix(cleaned, "+") {
			num2, err2 := phonenumbers.Parse("+"+cleaned, "")
			if err2 == nil && phonenumbers.IsValidNumber(num2) {
				return phonenumbers.Format(num2, phonenumbers.E164), true, mapNumberType(phonenumbers.GetNumberType(num2))
			}
		}
		return "", false, "invalid_phone_number"
	}

	isValid := phonenumbers.IsValidNumber(num)
	formatted := phonenumbers.Format(num, phonenumbers.E164)
	numType := mapNumberType(phonenumbers.GetNumberType(num))

	return formatted, isValid, numType
}

// Lookup resolves the carrier and country for a phone number using the in-memory prefix trie.
func (e *Engine) Lookup(rawNumber string, defaultCountryISO string) NumberLookupResult {
	result := NumberLookupResult{
		OriginalNumber: rawNumber,
		NumberType:     "unknown",
	}

	normalized, isValid, numType := e.NormalizeE164(rawNumber, defaultCountryISO)
	if !isValid || normalized == "" {
		result.IsValid = false
		result.Error = "invalid phone number format"
		// Still attempt best-effort prefix match on cleaned raw number
		if match, found := e.trie.LongestMatch(rawNumber); found {
			result.MatchedPrefix = match.Prefix
			result.Country = &CountryInfo{
				ID:          match.CountryID,
				Name:        match.CountryName,
				ISO2:        match.CountryISO2,
				CallingCode: match.CallingCode,
			}
			if match.OperatorName != "" && match.OperatorName != "unknown" {
				result.Operator = &OperatorInfo{
					ID:   match.OperatorID,
					Name: match.OperatorName,
				}
			} else {
				result.Operator = &OperatorInfo{
					Name: "unknown",
				}
			}
			result.NumberType = match.NumberType
		}
		return result
	}

	result.NormalizedE164 = &normalized
	result.IsValid = true
	result.NumberType = numType

	// Longest prefix match against in-memory trie
	match, found := e.trie.LongestMatch(normalized)
	if found {
		result.MatchedPrefix = match.Prefix
		result.Country = &CountryInfo{
			ID:          match.CountryID,
			Name:        match.CountryName,
			ISO2:        match.CountryISO2,
			CallingCode: match.CallingCode,
		}
		if match.OperatorName != "" && match.OperatorName != "unknown" {
			result.Operator = &OperatorInfo{
				ID:   match.OperatorID,
				Name: match.OperatorName,
			}
		} else {
			result.Operator = &OperatorInfo{
				Name: "unknown",
			}
		}
		if match.NumberType != "" {
			result.NumberType = match.NumberType
		}
	} else {
		// Lookup country from nyaruka parsed phone number as fallback
		parsed, err := phonenumbers.Parse(normalized, "")
		if err == nil {
			region := phonenumbers.GetRegionCodeForNumber(parsed)
			if region != "" {
				result.Country = &CountryInfo{
					ISO2: region,
				}
			}
		}
		result.Operator = &OperatorInfo{
			Name: "unknown",
		}
	}

	return result
}

func mapNumberType(t phonenumbers.PhoneNumberType) string {
	switch t {
	case phonenumbers.MOBILE:
		return "mobile"
	case phonenumbers.FIXED_LINE:
		return "fixed"
	case phonenumbers.FIXED_LINE_OR_MOBILE:
		return "mobile"
	case phonenumbers.VOIP:
		return "voip"
	case phonenumbers.TOLL_FREE:
		return "toll_free"
	case phonenumbers.PREMIUM_RATE:
		return "premium_rate"
	case phonenumbers.SHARED_COST:
		return "shared_cost"
	case phonenumbers.UAN:
		return "uan"
	case phonenumbers.PAGER:
		return "pager"
	default:
		return "other"
	}
}
