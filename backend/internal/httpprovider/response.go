package httpprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
)

// ParsedSendResponse contains the normalized outcome from an HTTP provider response.
type ParsedResponse struct {
	Status            messaging.SendStatus
	ProviderMessageID string
	ErrorCode         string
	ErrorMessage      string
}

// ExtractJSONPath extracts a scalar value from a JSON document using a dot-separated path
// (e.g. "message_id", "data.id", or "messages.0.id").
func ExtractJSONPath(raw []byte, path string) (string, bool) {
	path = strings.TrimSpace(path)
	if len(raw) == 0 || path == "" {
		return "", false
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", false
	}
	return extractFromAny(doc, path)
}

func extractFromAny(doc any, path string) (string, bool) {
	if doc == nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts {
		switch node := cur.(type) {
		case map[string]any:
			val, exists := node[p]
			if !exists {
				// Fallback case-insensitive key lookup
				for k, v := range node {
					if strings.EqualFold(k, p) {
						val = v
						exists = true
						break
					}
				}
			}
			if !exists {
				return "", false
			}
			cur = val
		case []any:
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	return scalarToString(cur)
}

func scalarToString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, t != ""
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// ParseSendResponse maps HTTP status + body into Znero's 4-outcome SendStatus contract:
//   - SendStatusAccepted
//   - SendStatusRetryableReject (throttle: 429 or configured throttle error codes)
//   - SendStatusFatalReject (permanent rejection: 4xx, redirect, or failed success condition)
//   - SendStatusUnknown (ambiguous: 5xx after request sent — NEVER auto-resend)
func ParseSendResponse(cfg provider.HTTPConnectorConfig, httpStatus int, body []byte) ParsedResponse {
	norm := cfg.Normalized()
	var errCode, errMsg string
	if c, ok := ExtractJSONPath(body, norm.ErrorCodePath); ok {
		errCode = c
	}
	if m, ok := ExtractJSONPath(body, norm.ErrorMsgPath); ok {
		errMsg = m
	}

	// Explicit throttle classification takes precedence over generic status buckets
	if intInSlice(httpStatus, norm.ThrottleHTTPCodes) || (errCode != "" && strInSliceFold(errCode, norm.ThrottleErrorCodes)) {
		return ParsedResponse{
			Status:       messaging.SendStatusRetryableReject,
			ErrorCode:    firstNonEmpty(errCode, fmt.Sprintf("HTTP_%d", httpStatus)),
			ErrorMessage: firstNonEmpty(errMsg, http.StatusText(httpStatus)),
		}
	}

	// Explicit permanent HTTP/error codes
	if intInSlice(httpStatus, norm.PermanentHTTPCodes) || (errCode != "" && strInSliceFold(errCode, norm.PermanentErrorCodes)) {
		return ParsedResponse{
			Status:       messaging.SendStatusFatalReject,
			ErrorCode:    firstNonEmpty(errCode, fmt.Sprintf("HTTP_%d", httpStatus)),
			ErrorMessage: firstNonEmpty(errMsg, http.StatusText(httpStatus)),
		}
	}

	// 5xx responses are AMBIGUOUS because upstream may have accepted/forwarded the SMS before failing
	if httpStatus >= 500 && httpStatus <= 599 {
		return ParsedResponse{
			Status:       messaging.SendStatusUnknown,
			ErrorCode:    firstNonEmpty(errCode, fmt.Sprintf("HTTP_%d", httpStatus)),
			ErrorMessage: firstNonEmpty(errMsg, fmt.Sprintf("upstream server error HTTP %d (ambiguous)", httpStatus)),
		}
	}

	// Success HTTP window check
	if httpStatus < norm.SuccessHTTPMin || httpStatus > norm.SuccessHTTPMax {
		// Unlisted 3xx/4xx -> permanent rejection
		return ParsedResponse{
			Status:       messaging.SendStatusFatalReject,
			ErrorCode:    firstNonEmpty(errCode, fmt.Sprintf("HTTP_%d", httpStatus)),
			ErrorMessage: firstNonEmpty(errMsg, fmt.Sprintf("unexpected HTTP status %d", httpStatus)),
		}
	}

	// Optional body success predicate
	if norm.SuccessJSONPath != "" {
		gotVal, ok := ExtractJSONPath(body, norm.SuccessJSONPath)
		if !ok || (norm.SuccessEquals != "" && !strings.EqualFold(gotVal, norm.SuccessEquals)) {
			return ParsedResponse{
				Status:       messaging.SendStatusFatalReject,
				ErrorCode:    firstNonEmpty(errCode, "BODY_CONDITION_FAILED"),
				ErrorMessage: firstNonEmpty(errMsg, fmt.Sprintf("success condition %s=%s not met (got %q)", norm.SuccessJSONPath, norm.SuccessEquals, gotVal)),
			}
		}
	}

	// Extract provider message ID
	var msgID string
	if norm.ProviderMsgIDPath != "" {
		if id, ok := ExtractJSONPath(body, norm.ProviderMsgIDPath); ok {
			msgID = id
		}
	}
	if msgID == "" && norm.ProviderMsgIDRegex != "" {
		if re, err := regexp.Compile(norm.ProviderMsgIDRegex); err == nil {
			if m := re.FindStringSubmatch(string(body)); len(m) > 1 {
				msgID = strings.TrimSpace(m[1])
			} else if len(m) == 1 {
				msgID = strings.TrimSpace(m[0])
			}
		}
	}

	return ParsedResponse{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: msgID,
	}
}

// MapDLRStatus normalizes a provider HTTP DLR status string (via custom StatusMap or standard synonyms)
// to SMPP canonical status tokens ("DELIVRD", "UNDELIV", "EXPIRED", "REJECTD", "ACCEPTD", "UNKNOWN").
func MapDLRStatus(rawStat string, customMap map[string]string) string {
	clean := strings.TrimSpace(rawStat)
	if clean == "" {
		return "UNKNOWN"
	}
	for k, v := range customMap {
		if strings.EqualFold(strings.TrimSpace(k), clean) {
			clean = strings.TrimSpace(v)
			break
		}
	}
	switch strings.ToLower(clean) {
	case "delivrd", "delivered", "success", "ok", "sent_delivered", "2":
		return "DELIVRD"
	case "undeliv", "undelivered", "failed", "fail", "error", "undeliverable", "5":
		return "UNDELIV"
	case "expired", "expire", "timeout", "3":
		return "EXPIRED"
	case "rejectd", "rejected", "reject", "blocked", "8":
		return "REJECTD"
	case "acceptd", "accepted", "queued", "pending", "sent", "submitted", "enroute", "buffered", "6":
		return "ACCEPTD"
	default:
		return strings.ToUpper(clean)
	}
}

func intInSlice(v int, list []int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func strInSliceFold(v string, list []string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
