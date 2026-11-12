package provider

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	reProviderCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,63}$`)
	reCurrencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	reValidDNSName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

	validProviderTypes = map[string]bool{
		"smpp":  true,
		"http":  true,
		"other": true,
	}
	validProviderStatuses = map[string]bool{
		"draft":     true,
		"active":    true,
		"suspended": true,
		"inactive":  true,
	}
	validProtocols = map[string]bool{
		"smpp": true,
		"http": true,
	}
	validBindTypes = map[string]bool{
		"transmitter": true,
		"receiver":    true,
		"transceiver": true,
	}
	validHTTPAuthTypes = map[string]bool{
		"none":           true,
		"basic":          true,
		"bearer":         true,
		"header_token":   true,
		"api_key_header": true,
		"query_token":    true,
		"api_key_query":  true,
	}
	validHTTPDLRModes = map[string]bool{
		"none":     true,
		"callback": true,
		"polling":  true,
	}
	validHTTPMethods = map[string]bool{
		"POST": true,
		"GET":  true,
		"PUT":  true,
	}
	validHTTPBodyFormats = map[string]bool{
		"json": true,
		"form": true,
		"none": true,
	}
	validCallbackAlgos = map[string]bool{
		"hmac_sha256":   true,
		"shared_secret": true,
		"none":          true,
	}
	validConnectionStatuses = map[string]bool{
		"draft":    true,
		"active":   true,
		"disabled": true,
	}
	validCoverageStatuses = map[string]bool{
		"declared":  true,
		"tested":    true,
		"active":    true,
		"suspended": true,
	}
)

var forbiddenCIDRs []*net.IPNet

func init() {
	ranges := []string{
		"0.0.0.0/8",
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::1/128",
		"::/128",
		"fe80::/10",
		"fc00::/7",
	}
	for _, r := range ranges {
		_, cidr, err := net.ParseCIDR(r)
		if err == nil {
			forbiddenCIDRs = append(forbiddenCIDRs, cidr)
		}
	}
}

// ValidateHostSSRF checks that host is not a loopback, RFC1918 private, link-local, or cloud metadata
// address. Private, link-local, and metadata addresses are always rejected; loopback addresses are
// rejected unless ValidateHostSSRFWithOptions is explicitly called with allowLoopback=true outside production.
func ValidateHostSSRF(rawHost string, appEnv string, connStatus string) error {
	return ValidateHostSSRFWithOptions(rawHost, appEnv, connStatus, false)
}

// ValidateHostSSRFWithOptions checks host safety against SSRF, permitting loopback addresses ONLY when
// allowLoopback is true and appEnv is NOT "production". Cloud metadata, RFC1918 private, and link-local
// addresses are always rejected.
func ValidateHostSSRFWithOptions(rawHost string, appEnv string, connStatus string, allowLoopback bool) error {
	loopbackAllowed := allowLoopback && !strings.EqualFold(strings.TrimSpace(appEnv), "production")

	host := strings.TrimSpace(rawHost)
	if host == "" {
		return fmt.Errorf("%w: host is required", ErrValidation)
	}
	if strings.ContainsAny(host, " \t\r\n/\\@") {
		return fmt.Errorf("%w: host contains invalid characters", ErrValidation)
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	lower := strings.ToLower(host)

	if lower == "metadata.google.internal" {
		return fmt.Errorf("%w: SSRF protection rejected forbidden host %q", ErrValidation, rawHost)
	}
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		if !loopbackAllowed {
			return fmt.Errorf("%w: SSRF protection rejected forbidden host %q", ErrValidation, rawHost)
		}
		return nil
	}

	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() {
			if !loopbackAllowed {
				return fmt.Errorf("%w: SSRF protection rejected private, loopback, or metadata IP %q", ErrValidation, rawHost)
			}
			return nil
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
			return fmt.Errorf("%w: SSRF protection rejected private, loopback, or metadata IP %q", ErrValidation, rawHost)
		}
		for _, cidr := range forbiddenCIDRs {
			if cidr.Contains(ip) {
				return fmt.Errorf("%w: SSRF protection rejected forbidden IP %q", ErrValidation, rawHost)
			}
		}
		return nil
	}

	if !reValidDNSName.MatchString(host) || !strings.Contains(host, ".") {
		return fmt.Errorf("%w: invalid hostname %q", ErrValidation, rawHost)
	}

	// DNS resolution with 2-second timeout to reject hostnames that resolve to private or loopback IPs
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err == nil {
		for _, resolvedIP := range ips {
			if resolvedIP.IsLoopback() {
				if !loopbackAllowed {
					return fmt.Errorf("%w: SSRF protection rejected hostname %q resolving to private or loopback IP %s", ErrValidation, rawHost, resolvedIP.String())
				}
				continue
			}
			if resolvedIP.IsLinkLocalUnicast() || resolvedIP.IsLinkLocalMulticast() || resolvedIP.IsUnspecified() || resolvedIP.IsPrivate() {
				return fmt.Errorf("%w: SSRF protection rejected hostname %q resolving to private or loopback IP %s", ErrValidation, rawHost, resolvedIP.String())
			}
			for _, cidr := range forbiddenCIDRs {
				if cidr.Contains(resolvedIP) {
					return fmt.Errorf("%w: SSRF protection rejected hostname %q resolving to forbidden IP %s", ErrValidation, rawHost, resolvedIP.String())
				}
			}
		}
	}

	return nil
}

// ValidateHTTPBaseURL validates URL scheme and applies SSRF checks to the URL hostname.
func ValidateHTTPBaseURL(rawURL string, protocol string, appEnv string, connStatus string) error {
	return ValidateHTTPBaseURLWithOptions(rawURL, protocol, appEnv, connStatus, false)
}

func isLoopbackHostname(host string) bool {
	h := strings.ToLower(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(host), "]"), "["))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ValidateHTTPBaseURLWithOptions validates URL scheme and SSRF safety with explicit test-only loopback override.
func ValidateHTTPBaseURLWithOptions(rawURL string, protocol string, appEnv string, connStatus string, allowLoopback bool) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: invalid http_base_url", ErrValidation)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: http_base_url must use http or https scheme", ErrValidation)
	}
	if u.User != nil {
		return fmt.Errorf("%w: http_base_url must not embed credentials in the URL", ErrValidation)
	}
	hostname := u.Hostname()
	loopbackHTTP := allowLoopback && !strings.EqualFold(strings.TrimSpace(appEnv), "production") && isLoopbackHostname(hostname)
	if !loopbackHTTP && (!strings.EqualFold(appEnv, "development") || strings.EqualFold(connStatus, "production") || strings.EqualFold(connStatus, "active")) && scheme != "https" {
		return fmt.Errorf("%w: active/production HTTP connections must use https://", ErrValidation)
	}
	// If u.Host contains more than one colon without brackets, it is an unbracketed IPv6 literal
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return ValidateHostSSRFWithOptions(u.Host, appEnv, connStatus, allowLoopback)
	}
	return ValidateHostSSRFWithOptions(hostname, appEnv, connStatus, allowLoopback)
}

func validateCreateProvider(req *CreateProviderRequest) error {
	req.Code = strings.ToUpper(strings.TrimSpace(req.Code))
	if !reProviderCode.MatchString(req.Code) {
		return fmt.Errorf("%w: code must be 3..64 uppercase alphanumeric, hyphen, or underscore characters", ErrValidation)
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 255 {
		return fmt.Errorf("%w: name is required (max 255 chars)", ErrValidation)
	}
	if req.ProviderType == "" {
		req.ProviderType = "smpp"
	}
	if !validProviderTypes[req.ProviderType] {
		return fmt.Errorf("%w: invalid provider_type %q (must be smpp, http, or other)", ErrValidation, req.ProviderType)
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	if req.CurrencyCode == "" {
		req.CurrencyCode = "USD"
	}
	req.CurrencyCode = strings.ToUpper(strings.TrimSpace(req.CurrencyCode))
	if !reCurrencyCode.MatchString(req.CurrencyCode) {
		return fmt.Errorf("%w: currency_code must be a 3-letter ISO 4217 code", ErrValidation)
	}
	if req.Status == "" {
		req.Status = "draft"
	}
	if !validProviderStatuses[req.Status] {
		return fmt.Errorf("%w: invalid status %q", ErrValidation, req.Status)
	}
	return nil
}

func validateConnectionFields(c *ProviderConnection, appEnv string, allowLoopback bool) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 255 {
		return fmt.Errorf("%w: connection name is required (max 255 chars)", ErrValidation)
	}
	if !validProtocols[c.Protocol] {
		return fmt.Errorf("%w: invalid protocol %q (must be smpp or http)", ErrValidation, c.Protocol)
	}
	if c.Status == "" {
		c.Status = "draft"
	}
	if !validConnectionStatuses[c.Status] {
		return fmt.Errorf("%w: invalid connection status %q (must be draft, active, or disabled)", ErrValidation, c.Status)
	}
	if c.MaxTPS < 1 || c.MaxTPS > 100000 {
		return fmt.Errorf("%w: max_tps must be between 1 and 100000", ErrValidation)
	}
	if c.SMPPBindType == "" {
		c.SMPPBindType = "transceiver"
	}
	if c.SMPPCharset == "" {
		c.SMPPCharset = "GSM7"
	}
	if c.HTTPAuthType == "" {
		c.HTTPAuthType = "none"
	}

	if c.Protocol == "smpp" {
		if strings.TrimSpace(c.SMPPHost) == "" {
			return fmt.Errorf("%w: smpp_host is required for SMPP connections", ErrValidation)
		}
		if err := ValidateHostSSRFWithOptions(c.SMPPHost, appEnv, c.Status, allowLoopback); err != nil {
			return err
		}
		if c.SMPPPort < 1 || c.SMPPPort > 65535 {
			return fmt.Errorf("%w: smpp_port must be between 1 and 65535", ErrValidation)
		}
		if !validBindTypes[c.SMPPBindType] {
			return fmt.Errorf("%w: invalid smpp_bind_type %q", ErrValidation, c.SMPPBindType)
		}
		if c.SMPPWindowSize < 1 || c.SMPPWindowSize > 512 {
			return fmt.Errorf("%w: smpp_window_size must be between 1 and 512", ErrValidation)
		}
		if c.SMPPEnquireLinkSec < 5 || c.SMPPEnquireLinkSec > 300 {
			return fmt.Errorf("%w: smpp_enquire_link_sec must be between 5 and 300", ErrValidation)
		}
	} else {
		if strings.TrimSpace(c.HTTPBaseURL) == "" {
			return fmt.Errorf("%w: http_base_url is required for HTTP connections", ErrValidation)
		}
		if err := ValidateHTTPBaseURLWithOptions(c.HTTPBaseURL, c.Protocol, appEnv, c.Status, allowLoopback); err != nil {
			return err
		}
		if !validHTTPAuthTypes[c.HTTPAuthType] {
			return fmt.Errorf("%w: invalid http_auth_type %q", ErrValidation, c.HTTPAuthType)
		}
		if c.HTTPDLRMode == "" {
			c.HTTPDLRMode = "none"
		}
		if !validHTTPDLRModes[c.HTTPDLRMode] {
			return fmt.Errorf("%w: invalid http_dlr_mode %q (must be none, callback, or polling)", ErrValidation, c.HTTPDLRMode)
		}
		if err := validateHTTPConnectorConfig(&c.HTTPConfig, c.HTTPDLRMode, appEnv, c.Status, allowLoopback); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPConnectorConfig(cfg *HTTPConnectorConfig, dlrMode, appEnv, connStatus string, allowLoopback bool) error {
	if cfg.Method != "" {
		cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
		if !validHTTPMethods[cfg.Method] {
			return fmt.Errorf("%w: invalid http_config.method %q (must be POST, GET, or PUT)", ErrValidation, cfg.Method)
		}
	}
	if cfg.BodyFormat != "" {
		cfg.BodyFormat = strings.ToLower(strings.TrimSpace(cfg.BodyFormat))
		if !validHTTPBodyFormats[cfg.BodyFormat] {
			return fmt.Errorf("%w: invalid http_config.body_format %q (must be json, form, or none)", ErrValidation, cfg.BodyFormat)
		}
	}
	for k, v := range cfg.Headers {
		if strings.ContainsAny(k, "\r\n:") || strings.TrimSpace(k) == "" {
			return fmt.Errorf("%w: invalid header name %q", ErrValidation, k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%w: header value for %q must not contain newlines", ErrValidation, k)
		}
	}
	if cfg.AuthHeaderName != "" && strings.ContainsAny(cfg.AuthHeaderName, "\r\n: \t") {
		return fmt.Errorf("%w: invalid http_config.auth_header_name %q", ErrValidation, cfg.AuthHeaderName)
	}
	if cfg.AuthQueryParam != "" && strings.ContainsAny(cfg.AuthQueryParam, "\r\n&=?# ") {
		return fmt.Errorf("%w: invalid http_config.auth_query_param %q", ErrValidation, cfg.AuthQueryParam)
	}
	if cfg.RequestTimeoutMs != 0 && (cfg.RequestTimeoutMs < 100 || cfg.RequestTimeoutMs > 60000) {
		return fmt.Errorf("%w: http_config.request_timeout_ms must be between 100 and 60000", ErrValidation)
	}
	if cfg.MaxResponseBytes != 0 && (cfg.MaxResponseBytes < 1024 || cfg.MaxResponseBytes > 1048576) {
		return fmt.Errorf("%w: http_config.max_response_bytes must be between 1024 and 1048576", ErrValidation)
	}
	if cfg.SuccessHTTPMin != 0 && (cfg.SuccessHTTPMin < 100 || cfg.SuccessHTTPMin > 599) {
		return fmt.Errorf("%w: http_config.success_http_min must be valid HTTP status code", ErrValidation)
	}
	if cfg.SuccessHTTPMax != 0 && (cfg.SuccessHTTPMax < 100 || cfg.SuccessHTTPMax > 599) {
		return fmt.Errorf("%w: http_config.success_http_max must be valid HTTP status code", ErrValidation)
	}
	if cfg.SuccessHTTPMin > 0 && cfg.SuccessHTTPMax > 0 && cfg.SuccessHTTPMax < cfg.SuccessHTTPMin {
		return fmt.Errorf("%w: http_config.success_http_max must be >= success_http_min", ErrValidation)
	}
	if cfg.ProviderMsgIDRegex != "" {
		if _, err := regexp.Compile(cfg.ProviderMsgIDRegex); err != nil {
			return fmt.Errorf("%w: invalid http_config.provider_msg_id_regex: %v", ErrValidation, err)
		}
	}
	if cfg.URLTemplate != "" {
		trimmed := strings.TrimSpace(cfg.URLTemplate)
		low := strings.ToLower(trimmed)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
			// Replace placeholders with dummy value before URL SSRF check
			probe := strings.NewReplacer(
				"{{destination}}", "15550001",
				"{{destination_no_plus}}", "15550001",
				"{{sender}}", "ZNERO",
				"{{text}}", "msg",
				"{{message_id}}", "id",
				"{{encoding}}", "GSM7",
			).Replace(trimmed)
			if err := ValidateHTTPBaseURLWithOptions(probe, "http", appEnv, connStatus, allowLoopback); err != nil {
				return fmt.Errorf("%w: url_template SSRF check failed: %v", ErrValidation, err)
			}
		}
	}
	if cfg.CallbackSignatureAlgo != "" && !validCallbackAlgos[strings.ToLower(cfg.CallbackSignatureAlgo)] {
		return fmt.Errorf("%w: invalid http_config.callback_signature_algo %q", ErrValidation, cfg.CallbackSignatureAlgo)
	}
	if cfg.PollMethod != "" {
		cfg.PollMethod = strings.ToUpper(strings.TrimSpace(cfg.PollMethod))
		if cfg.PollMethod != "GET" && cfg.PollMethod != "POST" {
			return fmt.Errorf("%w: invalid http_config.poll_method %q (must be GET or POST)", ErrValidation, cfg.PollMethod)
		}
	}
	if cfg.PollIntervalSec != 0 && (cfg.PollIntervalSec < 5 || cfg.PollIntervalSec > 3600) {
		return fmt.Errorf("%w: http_config.poll_interval_sec must be between 5 and 3600", ErrValidation)
	}
	if dlrMode == "polling" {
		if strings.TrimSpace(cfg.PollURLTemplate) == "" {
			return fmt.Errorf("%w: http_config.poll_url_template is required when http_dlr_mode is polling", ErrValidation)
		}
		probe := strings.ReplaceAll(strings.TrimSpace(cfg.PollURLTemplate), "{{provider_message_id}}", "probe-id")
		if err := ValidateHTTPBaseURLWithOptions(probe, "http", appEnv, connStatus, allowLoopback); err != nil {
			return fmt.Errorf("%w: poll_url_template SSRF check failed: %v", ErrValidation, err)
		}
	}
	return nil
}

func validateCoverageFields(cov *ProviderCoverage) error {
	if cov.Status == "" {
		cov.Status = "declared"
	}
	if !validCoverageStatuses[cov.Status] {
		return fmt.Errorf("%w: invalid coverage status %q (must be declared, tested, active, or suspended)", ErrValidation, cov.Status)
	}
	if cov.EffectiveFrom != nil && cov.EffectiveTo != nil && cov.EffectiveTo.Before(*cov.EffectiveFrom) {
		return fmt.Errorf("%w: effective_to cannot be before effective_from", ErrValidation)
	}
	return nil
}

func sanitizeCSVCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
