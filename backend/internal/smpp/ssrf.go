package smpp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Blocked IP networks for SSRF protection
var blockedCIDRs []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",          // RFC 1918 Private-Use
		"172.16.0.0/12",       // RFC 1918 Private-Use
		"192.168.0.0/16",      // RFC 1918 Private-Use
		"169.254.0.0/16",      // RFC 3927 Link-Local / Cloud Metadata
		"100.64.0.0/10",       // RFC 6598 Shared Address Space (Carrier NAT)
		"198.18.0.0/15",       // RFC 2544 Benchmarking
		"192.0.0.0/24",        // RFC 6890 IETF Protocol Assignments
		"192.0.2.0/24",        // RFC 5737 Documentation (TEST-NET-1)
		"198.51.100.0/24",     // RFC 5737 Documentation (TEST-NET-2)
		"203.0.113.0/24",      // RFC 5737 Documentation (TEST-NET-3)
		"224.0.0.0/4",         // RFC 5771 Multicast
		"240.0.0.0/4",         // RFC 1112 Reserved
		"0.0.0.0/8",           // RFC 1122 This host on this network
		"fc00::/7",            // RFC 4193 Unique Local IPv6
		"fe80::/10",           // RFC 4291 Link-Local IPv6
		"ff00::/8",            // RFC 4291 Multicast IPv6
	}

	for _, cidr := range cidrs {
		_, block, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedCIDRs = append(blockedCIDRs, block)
		}
	}
}

// Resolver defines the DNS resolution interface for testing and production.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DialOptions holds dialer parameters.
type DialOptions struct {
	Timeout       time.Duration
	AllowLoopback bool // True only when APP_ENV=development
	TLS           bool
	TLSConfig     *tls.Config
	Resolver      Resolver
}

// ValidateIP checks if an IP belongs to any prohibited SSRF ranges.
func ValidateIP(ip net.IP, allowLoopback bool) error {
	if ip == nil {
		return fmt.Errorf("ssrf: nil IP address")
	}

	// Check loopback
	if ip.IsLoopback() {
		if allowLoopback {
			return nil
		}
		return fmt.Errorf("ssrf: loopback address %s is prohibited in non-development environments", ip.String())
	}

	// Check unspecified (0.0.0.0 or ::)
	if ip.IsUnspecified() {
		return fmt.Errorf("ssrf: unspecified address %s is prohibited", ip.String())
	}

	// Check blocked CIDRs
	for _, block := range blockedCIDRs {
		if block.Contains(ip) {
			return fmt.Errorf("ssrf: address %s is within blocked range %s", ip.String(), block.String())
		}
	}

	return nil
}

// SafeDialer resolves DNS, checks ALL IPs against SSRF rules, and connects directly to a validated IP.
type SafeDialer struct {
	opts DialOptions
}

// NewSafeDialer creates a new SafeDialer.
func NewSafeDialer(opts DialOptions) *SafeDialer {
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	return &SafeDialer{opts: opts}
}

// DialContext resolves the host, verifies each resolved IP, and dials the validated IP directly.
func (d *SafeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("ssrf dial: invalid address %s: %w", address, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("ssrf dial: invalid port %s", portStr)
	}

	// Resolve all IP addresses for the hostname
	addrs, err := d.opts.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf dial: dns resolution failed for %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("ssrf dial: no IP addresses returned for host %s", host)
	}

	// CRITICAL: Validate EVERY resolved IP address to prevent DNS rebinding / mixed-record attacks
	var validatedIPs []net.IP
	for _, addr := range addrs {
		if err := ValidateIP(addr.IP, d.opts.AllowLoopback); err != nil {
			return nil, fmt.Errorf("ssrf dial: host %s resolved to prohibited IP: %w", host, err)
		}
		validatedIPs = append(validatedIPs, addr.IP)
	}

	if len(validatedIPs) == 0 {
		return nil, fmt.Errorf("ssrf dial: no valid public IPs for host %s", host)
	}

	// Connect directly to the first validated IP
	dialer := &net.Dialer{
		Timeout: d.opts.Timeout,
	}

	targetAddr := net.JoinHostPort(validatedIPs[0].String(), portStr)
	rawConn, err := dialer.DialContext(ctx, network, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("ssrf dial: connection to %s failed: %w", targetAddr, err)
	}

	// Wrap in TLS if requested
	if d.opts.TLS {
		tlsConfig := d.opts.TLSConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		} else {
			tlsConfig = tlsConfig.Clone()
		}

		// Ensure MinVersion is at least TLS 1.2
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}

		// CRITICAL: Set ServerName to original hostname for SNI and strict certificate validation!
		if tlsConfig.ServerName == "" && !strings.Contains(host, ":") {
			tlsConfig.ServerName = host
		}

		tlsConn := tls.Client(rawConn, tlsConfig)
		handshakeCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
		defer cancel()

		if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("ssrf dial: tls handshake with %s (%s) failed: %w", host, targetAddr, err)
		}
		return tlsConn, nil
	}

	return rawConn, nil
}

