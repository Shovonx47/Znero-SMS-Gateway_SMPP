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
		"none":         true,
		"basic":        true,
		"header_token": true,
		"query_token":  true,
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
// address. Private/loopback addresses are rejected for all connections unless appEnv == "development"
// and connStatus == "draft" (and even then, 169.254.0.0/16 / link-local metadata is always rejected).
func ValidateHostSSRF(rawHost string, appEnv string, connStatus string) error {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return fmt.Errorf("%w: host is required", ErrValidation)
	}
	if strings.ContainsAny(host, " \t\r\n/\\@") {
		return fmt.Errorf("%w: host contains invalid characters", ErrValidation)
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	lower := strings.ToLower(host)

	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || lower == "metadata.google.internal" {
		return fmt.Errorf("%w: SSRF protection rejected forbidden host %q", ErrValidation, rawHost)
	}

	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() {
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
			if resolvedIP.IsLinkLocalUnicast() || resolvedIP.IsLinkLocalMulticast() || resolvedIP.IsUnspecified() || resolvedIP.IsLoopback() || resolvedIP.IsPrivate() {
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
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: invalid http_base_url", ErrValidation)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: http_base_url must use http or https scheme", ErrValidation)
	}
	if (!strings.EqualFold(appEnv, "development") || strings.EqualFold(connStatus, "production") || strings.EqualFold(connStatus, "active")) && scheme != "https" {
		return fmt.Errorf("%w: active/production HTTP connections must use https://", ErrValidation)
	}
	if u.User != nil {
		return fmt.Errorf("%w: http_base_url must not embed credentials in the URL", ErrValidation)
	}
	// If u.Host contains more than one colon without brackets, it is an unbracketed IPv6 literal
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return ValidateHostSSRF(u.Host, appEnv, connStatus)
	}
	hostname := u.Hostname()
	return ValidateHostSSRF(hostname, appEnv, connStatus)
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

func validateConnectionFields(c *ProviderConnection, appEnv string) error {
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
		if err := ValidateHostSSRF(c.SMPPHost, appEnv, c.Status); err != nil {
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
		if err := ValidateHTTPBaseURL(c.HTTPBaseURL, c.Protocol, appEnv, c.Status); err != nil {
			return err
		}
		if !validHTTPAuthTypes[c.HTTPAuthType] {
			return fmt.Errorf("%w: invalid http_auth_type %q", ErrValidation, c.HTTPAuthType)
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
