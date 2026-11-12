package provider

import (
	"fmt"
	"net"
	"strings"
)

// validateRawHostSafety rejects control characters, trailing-dot numeric IPs,
// and non-canonical/obfuscated IPv4 literals (octal, hex, decimal integer, short dotted).
func validateRawHostSafety(rawHost string) error {
	if strings.ContainsAny(rawHost, "\r\n\t\x00 ") {
		return fmt.Errorf("%w: host contains invalid control or whitespace characters", ErrValidation)
	}
	h := strings.TrimSpace(rawHost)
	if strings.HasPrefix(h, "[") {
		if end := strings.IndexByte(h, ']'); end > 0 {
			h = h[1:end]
		}
	} else if hostPart, _, err := net.SplitHostPort(h); err == nil {
		h = hostPart
	}
	if zone := strings.IndexByte(h, '%'); zone >= 0 {
		h = h[:zone]
	}

	hasTrailingDot := strings.HasSuffix(h, ".")
	trimmed := strings.TrimSuffix(h, ".")
	if trimmed == "" {
		return fmt.Errorf("%w: host is empty", ErrValidation)
	}

	// Canonical IP written with trailing dot (e.g. 127.0.0.1.) can bypass naive host checks.
	if hasTrailingDot && net.ParseIP(trimmed) != nil {
		return fmt.Errorf("%w: numeric IP host must not include a trailing dot", ErrValidation)
	}

	// Reject obfuscated / non-canonical IPv4 literals (e.g. 0177.0.0.1, 0x7f000001, 2130706433, 127.1).
	if net.ParseIP(trimmed) == nil && isObfuscatedIPv4Literal(trimmed) {
		return fmt.Errorf("%w: non-canonical or obfuscated numeric host %q is rejected", ErrValidation, rawHost)
	}
	return nil
}

func isObfuscatedIPv4Literal(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if !isCIntToken(part) {
			return false
		}
	}
	return true
}

func isCIntToken(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		hexPart := s[2:]
		if hexPart == "" {
			return false
		}
		for i := 0; i < len(hexPart); i++ {
			c := hexPart[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
