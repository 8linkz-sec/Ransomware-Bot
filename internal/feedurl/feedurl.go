package feedurl

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	AllowPlainHTTP       bool
	AllowPrivateNetworks bool
}

type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type defaultResolver struct{}

func (defaultResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

var DefaultResolver Resolver = defaultResolver{}

type GuardedDialer struct {
	Resolver Resolver
	Options  Options
	Dialer   net.Dialer
}

func (d GuardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid feed dial address: %w", err)
	}

	addrs, err := ResolveAllowedIPs(ctx, host, d.Resolver, d.Options)
	if err != nil {
		return nil, err
	}

	var lastErr error
	dialer := d.Dialer
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("feed host %q resolved to no dialable addresses", host)
}

func Validate(rawURL string, opts Options) error {
	parsed, err := Parse(rawURL)
	if err != nil {
		return err
	}
	if err := validateScheme(parsed.Scheme, opts); err != nil {
		return err
	}
	return validateHostWithoutDNS(parsed.Hostname(), opts)
}

func ValidateForFetch(ctx context.Context, rawURL string, resolver Resolver, opts Options) error {
	parsed, err := Parse(rawURL)
	if err != nil {
		return err
	}
	if err := validateScheme(parsed.Scheme, opts); err != nil {
		return err
	}
	return ValidateResolvedHost(ctx, parsed.Hostname(), resolver, opts)
}

func Parse(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("feed URL is empty")
	}
	if strings.TrimSpace(rawURL) != rawURL {
		return nil, fmt.Errorf("feed URL must not contain surrounding whitespace")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("feed URL is malformed: %w", err)
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("feed URL must use http or https scheme")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("feed URL scheme must be http or https (got %q)", parsed.Scheme)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("feed URL host is empty")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("feed URL must not include userinfo")
	}
	if key := sensitiveQueryKey(parsed.RawQuery); key != "" {
		return nil, fmt.Errorf("feed URL query parameter %q is not allowed", key)
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("feed URL must not include a fragment")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("feed URL port is invalid")
		}
	}

	return parsed, nil
}

// sensitiveQueryKey reports the first sensitive key present in a raw query
// string. It splits on "&" and the legacy ";" separator directly instead of
// going through url.ParseQuery (via url.Values), because url.ParseQuery
// silently drops pairs it cannot parse -- most commonly a ";"-separated
// query -- which would let a sensitive parameter through unnoticed. This
// mirrors internal/textutil's redactRawQuery, which solves the same
// silent-drop problem for log redaction.
func sensitiveQueryKey(rawQuery string) string {
	for _, ampPart := range strings.Split(rawQuery, "&") {
		for _, semiPart := range strings.Split(ampPart, ";") {
			// A segment with no "=" is a valueless key (e.g. "?token"), which
			// url.Values also treats as a key present with an empty value --
			// mirror that instead of skipping it, so a bare sensitive key is
			// still rejected.
			rawKey, _, _ := strings.Cut(semiPart, "=")
			decodedKey, err := url.QueryUnescape(rawKey)
			if err != nil {
				decodedKey = rawKey
			}
			if IsSensitiveQueryKey(decodedKey) {
				return decodedKey
			}
		}
	}
	return ""
}

func IsSensitiveQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "api_key", "apikey", "auth", "authorization", "key", "password", "pass", "secret", "token":
		return true
	default:
		return false
	}
}

func validateScheme(scheme string, opts Options) error {
	if scheme == "http" && !opts.AllowPlainHTTP {
		return fmt.Errorf("feed URL scheme must be https (got %q)", scheme)
	}
	return nil
}

func ValidateResolvedHost(ctx context.Context, host string, resolver Resolver, opts Options) error {
	if err := validateHostWithoutDNS(host, opts); err != nil {
		return err
	}
	if opts.AllowPrivateNetworks {
		return nil
	}

	addrs, err := ResolveAllowedIPs(ctx, host, resolver, opts)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("feed host %q resolved to no addresses", host)
	}
	return nil
}

func ResolveAllowedIPs(ctx context.Context, host string, resolver Resolver, opts Options) ([]netip.Addr, error) {
	if resolver == nil {
		resolver = DefaultResolver
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if !opts.AllowPrivateNetworks && isBlockedAddress(addr) {
			return nil, fmt.Errorf("feed host resolves to blocked address range")
		}
		return []netip.Addr{addr}, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resolved, err := resolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve feed host: %w", err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("feed host resolved to no addresses")
	}

	addrs := make([]netip.Addr, 0, len(resolved))
	for _, resolvedAddr := range resolved {
		addr, ok := netip.AddrFromSlice(resolvedAddr.IP)
		if !ok {
			return nil, fmt.Errorf("feed host resolved to an invalid IP address")
		}
		addr = addr.Unmap()
		if !opts.AllowPrivateNetworks && isBlockedAddress(addr) {
			return nil, fmt.Errorf("feed host resolves to blocked address range")
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func validateHostWithoutDNS(host string, opts Options) error {
	host = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(host), "."))
	if host == "" {
		return fmt.Errorf("feed URL host is empty")
	}
	if !opts.AllowPrivateNetworks && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
		return fmt.Errorf("feed URL host is blocked")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if !opts.AllowPrivateNetworks && isBlockedAddress(addr) {
			return fmt.Errorf("feed URL host is blocked")
		}
	}
	return nil
}

// isBlockedAddress reports whether addr must not be dialed for a feed fetch.
//
// IPv6 transition addresses are decided on the IPv4 address they carry: an IPv6
// literal such as 64:ff9b::a9fe:a9fe is globally routable as far as netip is
// concerned, but a NAT64 gateway turns it into 169.254.169.254.
func isBlockedAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range blockedTunnelPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	if embedded, ok := embeddedIPv4(addr); ok && isBlockedBaseAddress(embedded) {
		return true
	}
	return isBlockedBaseAddress(addr)
}

func isBlockedBaseAddress(addr netip.Addr) bool {
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedSpecialUsePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return !addr.IsGlobalUnicast()
}

// embeddedIPv4 returns the IPv4 address an IPv6 transition address carries, for
// the mechanisms whose embedding is fixed and unambiguous.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() || addr.Is4In6() {
		return netip.Addr{}, false
	}
	raw := addr.As16()
	switch {
	case nat64WellKnownPrefix.Contains(addr):
		return netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}), true
	case sixToFourPrefix.Contains(addr):
		return netip.AddrFrom4([4]byte{raw[2], raw[3], raw[4], raw[5]}), true
	case isISATAPInterfaceID(raw):
		return netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}), true
	}
	return netip.Addr{}, false
}

func isISATAPInterfaceID(raw [16]byte) bool {
	if raw[10] != 0x5e || raw[11] != 0xfe {
		return false
	}
	return (raw[8] == 0x00 || raw[8] == 0x02) && raw[9] == 0x00
}

var (
	nat64WellKnownPrefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourPrefix      = netip.MustParsePrefix("2002::/16")
)

// blockedTunnelPrefixes carry an IPv4 endpoint that cannot be recovered
// unambiguously, so the whole prefix is refused.
var blockedTunnelPrefixes = []netip.Prefix{
	netip.MustParsePrefix("::/96"),          // RFC 4291 IPv4-compatible, deprecated
	netip.MustParsePrefix("64:ff9b:1::/48"), // RFC 8215 local-use NAT64
	netip.MustParsePrefix("2001::/32"),      // RFC 4380 Teredo
}

var blockedSpecialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), // RFC 1122 "this network"
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"), // RFC 1112 reserved
	netip.MustParsePrefix("fec0::/10"),   // RFC 3879 deprecated IPv6 site-local
}
