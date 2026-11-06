package number

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ApplyRule executes a single transformation rule on the given number.
func ApplyRule(rule TransformRule, num string) (string, bool) {
	// 1. Check match pattern if specified
	if rule.MatchPattern != "" && rule.MatchPattern != "*" {
		// Pattern could be a prefix or regex
		if strings.HasPrefix(rule.MatchPattern, "^") || strings.HasSuffix(rule.MatchPattern, "$") {
			re, err := regexp.Compile(rule.MatchPattern)
			if err == nil && !re.MatchString(num) {
				return num, false
			}
		} else {
			// Prefix match
			cleanNum := cleanNumber(num)
			cleanPattern := cleanNumber(rule.MatchPattern)
			if !strings.HasPrefix(cleanNum, cleanPattern) {
				return num, false
			}
		}
	}

	switch rule.Operation {
	case "add_prefix":
		pfx := rule.Params["prefix"]
		if pfx == "" {
			return num, false
		}
		return pfx + num, true

	case "remove_prefix":
		pfx := rule.Params["prefix"]
		if pfx == "" {
			return num, false
		}
		if strings.HasPrefix(num, pfx) {
			return strings.TrimPrefix(num, pfx), true
		}
		// Also try without leading +
		cleanPfx := cleanNumber(pfx)
		cleanNum := cleanNumber(num)
		if strings.HasPrefix(cleanNum, cleanPfx) {
			hasPlus := strings.HasPrefix(num, "+")
			res := strings.TrimPrefix(cleanNum, cleanPfx)
			if hasPlus {
				return "+" + res, true
			}
			return res, true
		}
		return num, false

	case "replace_prefix":
		fromPfx := rule.Params["from_prefix"]
		toPfx := rule.Params["to_prefix"]
		if fromPfx == "" {
			return num, false
		}
		if strings.HasPrefix(num, fromPfx) {
			return toPfx + strings.TrimPrefix(num, fromPfx), true
		}
		cleanFrom := cleanNumber(fromPfx)
		cleanNum := cleanNumber(num)
		if strings.HasPrefix(cleanNum, cleanFrom) {
			hasPlus := strings.HasPrefix(num, "+")
			res := toPfx + strings.TrimPrefix(cleanNum, cleanFrom)
			if hasPlus && !strings.HasPrefix(res, "+") {
				return "+" + res, true
			}
			return res, true
		}
		return num, false

	case "add_country_code":
		cc := cleanNumber(rule.Params["country_code"])
		if cc == "" {
			return num, false
		}
		cleanNum := cleanNumber(num)
		if !strings.HasPrefix(cleanNum, cc) {
			if strings.HasPrefix(num, "+") {
				return "+" + cc + strings.TrimPrefix(num, "+"), true
			}
			return "+" + cc + num, true
		}
		return num, false

	case "remove_country_code":
		cc := cleanNumber(rule.Params["country_code"])
		if cc == "" {
			return num, false
		}
		cleanNum := cleanNumber(num)
		if strings.HasPrefix(cleanNum, cc) {
			hasPlus := strings.HasPrefix(num, "+")
			res := strings.TrimPrefix(cleanNum, cc)
			if hasPlus {
				return "+" + res, true
			}
			return res, true
		}
		return num, false

	default:
		return num, false
	}
}

// OrderRules sorts rules in deterministic order:
// 1. Scope: Global (0) -> Country (1) -> Operator (2)
// 2. Priority: Ascending (1 is evaluated before 100)
// 3. Name: Lexicographic tie-breaker
func OrderRules(rules []TransformRule) []TransformRule {
	sorted := make([]TransformRule, len(rules))
	copy(sorted, rules)

	sort.SliceStable(sorted, func(i, j int) bool {
		scopeRank := func(s string) int {
			switch s {
			case "global":
				return 0
			case "country":
				return 1
			case "operator":
				return 2
			default:
				return 3
			}
		}

		rankI := scopeRank(sorted[i].Scope)
		rankJ := scopeRank(sorted[j].Scope)
		if rankI != rankJ {
			return rankI < rankJ
		}

		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}

		return sorted[i].Name < sorted[j].Name
	})

	return sorted
}

// ExecuteChain executes active rules in sequence and records each step.
func ExecuteChain(rules []TransformRule, num string, direction string) (string, []ChainStep) {
	currentNum := num
	var steps []ChainStep

	for _, rule := range rules {
		transformed, applied := ApplyRule(rule, currentNum)
		if applied && transformed != currentNum {
			steps = append(steps, ChainStep{
				RuleID:    rule.ID,
				RuleName:  rule.Name,
				Operation: rule.Operation,
				Before:    currentNum,
				After:     transformed,
			})
			currentNum = transformed
		}
	}

	return currentNum, steps
}
func FilterActiveRules(rules []TransformRule, direction string, countryID *uuid.UUID, operatorID *uuid.UUID, now time.Time) []TransformRule {
	var filtered []TransformRule

	for _, r := range rules {
		if r.Status != "active" {
			continue
		}

		// Direction check
		if r.Direction != "both" && direction != "" && r.Direction != direction {
			continue
		}

		// Active dates
		if r.ActiveFrom != nil && now.Before(*r.ActiveFrom) {
			continue
		}
		if r.ActiveTo != nil && now.After(*r.ActiveTo) {
			continue
		}

		// Scope checks
		if r.Scope == "country" {
			if countryID == nil || r.CountryID == nil || *countryID != *r.CountryID {
				continue
			}
		} else if r.Scope == "operator" {
			if operatorID == nil || r.OperatorID == nil || *operatorID != *r.OperatorID {
				continue
			}
		}

		filtered = append(filtered, r)
	}

	return filtered
}

// ValidateRule ensures rule definition is valid and has required parameters.
func ValidateRule(r TransformRule) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("rule name is required")
	}

	switch r.Operation {
	case "add_prefix", "remove_prefix":
		if strings.TrimSpace(r.Params["prefix"]) == "" {
			return fmt.Errorf("param 'prefix' is required for %s", r.Operation)
		}
	case "replace_prefix":
		if strings.TrimSpace(r.Params["from_prefix"]) == "" || strings.TrimSpace(r.Params["to_prefix"]) == "" {
			return fmt.Errorf("params 'from_prefix' and 'to_prefix' are required for replace_prefix")
		}
	case "add_country_code", "remove_country_code":
		if strings.TrimSpace(r.Params["country_code"]) == "" {
			return fmt.Errorf("param 'country_code' is required for %s", r.Operation)
		}
	default:
		return fmt.Errorf("unsupported operation: %s", r.Operation)
	}

	switch r.Scope {
	case "global":
		// valid
	case "country":
		if r.CountryID == nil || *r.CountryID == uuid.Nil {
			return fmt.Errorf("country_id is required when scope is 'country'")
		}
	case "operator":
		if r.OperatorID == nil || *r.OperatorID == uuid.Nil {
			return fmt.Errorf("operator_id is required when scope is 'operator'")
		}
	default:
		return fmt.Errorf("invalid scope %q: must be global, country, or operator", r.Scope)
	}

	return nil
}
