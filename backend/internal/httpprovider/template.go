package httpprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
)

// RenderedDebug provides a credential-redacted preview of an HTTP provider request.
type RenderedDebug struct {
	Method       string
	RedactedURL  string
	RedactedBody string
}

func jsonEscapeString(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return ""
	}
	return string(b[1 : len(b)-1])
}

func buildPlaceholderReplacer(attempt messaging.SendAttempt, escaper func(string) string) *strings.Replacer {
	destNoPlus := strings.TrimPrefix(attempt.Destination, "+")
	udhHex := hex.EncodeToString(attempt.UDH)
	partStr := strconv.Itoa(maxInt(attempt.PartNumber, 1))
	totalStr := strconv.Itoa(maxInt(attempt.TotalParts, 1))
	encoding := attempt.Encoding
	if encoding == "" {
		encoding = "GSM7"
	}
	return strings.NewReplacer(
		"{{destination}}", escaper(attempt.Destination),
		"{{destination_no_plus}}", escaper(destNoPlus),
		"{{sender}}", escaper(attempt.SenderID),
		"{{text}}", escaper(attempt.Text),
		"{{message_id}}", escaper(attempt.MessageID.String()),
		"{{encoding}}", escaper(encoding),
		"{{udh}}", escaper(udhHex),
		"{{part_number}}", partStr,
		"{{total_parts}}", totalStr,
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// MaskURL strips sensitive query credentials from a URL for safe logging and UI previews.
func MaskURL(raw string, secretParam string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = url.UserPassword("***", "***")
	}
	q := u.Query()
	for k := range q {
		low := strings.ToLower(k)
		if (secretParam != "" && strings.EqualFold(k, secretParam)) ||
			strings.Contains(low, "key") ||
			strings.Contains(low, "token") ||
			strings.Contains(low, "secret") ||
			strings.Contains(low, "pass") ||
			strings.Contains(low, "auth") {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RenderRequest builds an outbound *http.Request for a messaging.SendAttempt using
// the connection's HTTP template and decrypted auth credentials.
func RenderRequest(
	ctx context.Context,
	cfg provider.HTTPConnectorConfig,
	baseURL string,
	authType string,
	plainCred string,
	attempt messaging.SendAttempt,
) (*http.Request, *RenderedDebug, error) {
	norm := cfg.Normalized()

	targetURL, err := resolveTargetURL(baseURL, norm.URLTemplate, attempt)
	if err != nil {
		return nil, nil, err
	}

	var bodyBytes []byte
	var contentType string
	switch norm.BodyFormat {
	case "json":
		rendered := buildPlaceholderReplacer(attempt, jsonEscapeString).Replace(norm.BodyTemplate)
		if !json.Valid([]byte(rendered)) {
			return nil, nil, fmt.Errorf("rendered JSON body_template is not valid JSON")
		}
		bodyBytes = []byte(rendered)
		contentType = "application/json"
	case "form":
		rendered := buildPlaceholderReplacer(attempt, url.QueryEscape).Replace(norm.BodyTemplate)
		bodyBytes = []byte(rendered)
		contentType = "application/x-www-form-urlencoded"
	case "none":
		bodyBytes = nil
	default:
		return nil, nil, fmt.Errorf("unsupported body_format %q", norm.BodyFormat)
	}

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid target url: %w", err)
	}

	secretQueryParam, headerName, headerVal, err := buildAuthInjection(norm, authType, plainCred)
	if err != nil {
		return nil, nil, err
	}
	if secretQueryParam != "" {
		q := parsedURL.Query()
		q.Set(secretQueryParam, plainCred)
		parsedURL.RawQuery = q.Encode()
	}

	var bodyReader *bytes.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, norm.Method, parsedURL.String(), bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("build http request: %w", err)
	}
	if contentType != "" && norm.Method != http.MethodGet {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")

	for k, v := range norm.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if headerName != "" {
		req.Header.Set(headerName, headerVal)
	}

	debug := &RenderedDebug{
		Method:       norm.Method,
		RedactedURL:  MaskURL(parsedURL.String(), secretQueryParam),
		RedactedBody: redactBodyPreview(string(bodyBytes), norm.BodyFormat),
	}
	return req, debug, nil
}

// RenderPollRequest builds an outbound *http.Request to query a single message's delivery status.
func RenderPollRequest(
	ctx context.Context,
	cfg provider.HTTPConnectorConfig,
	authType string,
	plainCred string,
	providerMsgID string,
) (*http.Request, string, error) {
	norm := cfg.Normalized()
	if strings.TrimSpace(norm.PollURLTemplate) == "" {
		return nil, "", fmt.Errorf("poll_url_template is empty")
	}
	renderedURL := strings.ReplaceAll(strings.TrimSpace(norm.PollURLTemplate), "{{provider_message_id}}", url.QueryEscape(providerMsgID))
	parsedURL, err := url.Parse(renderedURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid poll url: %w", err)
	}

	secretQueryParam, headerName, headerVal, err := buildAuthInjection(norm, authType, plainCred)
	if err != nil {
		return nil, "", err
	}
	if secretQueryParam != "" {
		q := parsedURL.Query()
		q.Set(secretQueryParam, plainCred)
		parsedURL.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, norm.PollMethod, parsedURL.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("build poll request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	for k, v := range norm.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if headerName != "" {
		req.Header.Set(headerName, headerVal)
	}
	return req, MaskURL(parsedURL.String(), secretQueryParam), nil
}

func resolveTargetURL(baseURL, urlTemplate string, attempt messaging.SendAttempt) (string, error) {
	base := strings.TrimSpace(baseURL)
	tpl := strings.TrimSpace(urlTemplate)
	urlRepl := buildPlaceholderReplacer(attempt, url.QueryEscape)
	if tpl == "" {
		if base == "" {
			return "", fmt.Errorf("http_base_url is required")
		}
		return urlRepl.Replace(base), nil
	}
	renderedTpl := urlRepl.Replace(tpl)
	low := strings.ToLower(renderedTpl)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return renderedTpl, nil
	}
	bURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid http_base_url: %w", err)
	}
	if strings.HasPrefix(renderedTpl, "?") {
		if bURL.RawQuery != "" {
			bURL.RawQuery += "&" + strings.TrimPrefix(renderedTpl, "?")
		} else {
			bURL.RawQuery = strings.TrimPrefix(renderedTpl, "?")
		}
		return bURL.String(), nil
	}
	rel, err := url.Parse(renderedTpl)
	if err != nil {
		return "", fmt.Errorf("invalid url_template: %w", err)
	}
	return bURL.ResolveReference(rel).String(), nil
}

func buildAuthInjection(norm provider.HTTPConnectorConfig, authType, plainCred string) (queryParam, headerName, headerVal string, err error) {
	authType = strings.ToLower(strings.TrimSpace(authType))
	switch authType {
	case "", "none":
		return "", "", "", nil
	case "basic":
		if plainCred == "" {
			return "", "", "", nil
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(plainCred))
		return "", "Authorization", "Basic " + encoded, nil
	case "bearer", "header_token":
		if plainCred == "" {
			return "", "", "", nil
		}
		hdr := norm.AuthHeaderName
		if hdr == "" {
			hdr = "Authorization"
		}
		val := plainCred
		if strings.EqualFold(hdr, "Authorization") && !strings.HasPrefix(strings.ToLower(val), "bearer ") {
			val = "Bearer " + val
		}
		return "", hdr, val, nil
	case "api_key_header":
		if plainCred == "" {
			return "", "", "", nil
		}
		hdr := norm.AuthHeaderName
		if hdr == "" {
			hdr = "X-API-Key"
		}
		return "", hdr, plainCred, nil
	case "query_token", "api_key_query":
		if plainCred == "" {
			return "", "", "", nil
		}
		qp := norm.AuthQueryParam
		if qp == "" {
			qp = "api_key"
		}
		return qp, "", "", nil
	default:
		return "", "", "", fmt.Errorf("unsupported http_auth_type %q", authType)
	}
}

func redactBodyPreview(body, format string) string {
	if body == "" {
		return ""
	}
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	// Never expose plaintext message text in test preview if needed, though test-send shows template rendering.
	_ = format
	return body
}
