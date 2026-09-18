package feedurl

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type fakeResolver map[string][]net.IPAddr

func (r fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r[host], nil
}

type errorResolver struct{}

func (errorResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return nil, errors.New("resolver unavailable")
}

func TestValidateRejectsUnsafeFeedURLs(t *testing.T) {
	tests := []string{
		"http://example.com/feed.xml",
		"http://localhost/feed.xml",
		"http://127.0.0.1/feed.xml",
		"http://[::1]/feed.xml",
		"https://user:pass@example.com/feed.xml",
		"https://example.com/feed.xml?token=secret",
		"https://example.com/feed.xml?api_key=secret",
		"https://example.com/feed.xml#fragment",
		"https://example.com:bad/feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{}); err == nil {
				t.Fatal("Validate() succeeded, want rejection")
			}
		})
	}
}

func TestValidateForFetchRejectsResolvedPrivateAddress(t *testing.T) {
	resolver := fakeResolver{
		"news.example": {{IP: net.ParseIP("10.0.0.12")}},
	}

	err := ValidateForFetch(context.Background(), "https://news.example/feed.xml", resolver, Options{})
	if err == nil {
		t.Fatal("ValidateForFetch() succeeded, want private-address rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "blocked") {
		t.Fatalf("ValidateForFetch() error = %v, want blocked-address context", err)
	}
}

func TestValidateForFetchAllowsPublicAddress(t *testing.T) {
	resolver := fakeResolver{
		"news.example": {{IP: net.ParseIP("93.184.216.34")}},
	}

	if err := ValidateForFetch(context.Background(), "https://news.example/feed.xml", resolver, Options{}); err != nil {
		t.Fatalf("ValidateForFetch() error = %v", err)
	}
}

func TestValidateAcceptsPublicFeedURLs(t *testing.T) {
	tests := []struct {
		rawURL string
		opts   Options
	}{
		{"https://example.com/feed.xml", Options{}},
		{"https://example.com:8443/feed.xml?page=2", Options{}},
		{"http://example.com/feed.xml", Options{AllowPlainHTTP: true}},
	}

	for _, tt := range tests {
		t.Run(tt.rawURL, func(t *testing.T) {
			if err := Validate(tt.rawURL, tt.opts); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestValidateRejectsBlockedHostsOverHTTPS(t *testing.T) {
	tests := []string{
		"https://localhost/feed.xml",
		"https://internal.localhost/feed.xml",
		"https://127.0.0.1/feed.xml",
		"https://[::1]/feed.xml",
		"https://10.0.0.1/feed.xml",
		"https://192.168.1.10/feed.xml",
		"https://169.254.1.1/feed.xml",
		"https://100.64.1.1/feed.xml",
		"https://198.18.0.1/feed.xml",
		"https://0.0.0.0/feed.xml",
		"https://255.255.255.255/feed.xml",
		"https://[ff02::1]/feed.xml",
		"https://./feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{}); err == nil {
				t.Fatal("Validate() succeeded, want blocked-host rejection")
			}
		})
	}
}

func TestValidateAllowsPrivateHostsWhenConfigured(t *testing.T) {
	tests := []string{
		"https://localhost/feed.xml",
		"https://127.0.0.1/feed.xml",
		"https://10.0.0.1/feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{AllowPrivateNetworks: true}); err != nil {
				t.Fatalf("Validate() error = %v, want private host accepted", err)
			}
		})
	}
}

func TestParseRejectsMalformedFeedURLs(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		wantText string
	}{
		{"empty", "", "empty"},
		{"leading whitespace", " https://example.com/feed.xml", "whitespace"},
		{"trailing whitespace", "https://example.com/feed.xml ", "whitespace"},
		{"control character", "https://example.com/\nfeed.xml", "malformed"},
		{"missing scheme", "example.com/feed.xml", "scheme"},
		{"unsupported scheme", "ftp://example.com/feed.xml", "scheme"},
		{"empty host", "https:///feed.xml", "host is empty"},
		{"userinfo", "https://user@example.com/feed.xml", "userinfo"},
		{"sensitive query key uppercase", "https://example.com/feed.xml?Token=secret", "not allowed"},
		{"fragment", "https://example.com/feed.xml#top", "fragment"},
		{"port zero", "https://example.com:0/feed.xml", "port"},
		{"port out of range", "https://example.com:99999/feed.xml", "port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.rawURL)
			if err == nil {
				t.Fatal("Parse() succeeded, want rejection")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.wantText) {
				t.Fatalf("Parse() error = %v, want %q context", err, tt.wantText)
			}
		})
	}
}

func TestParseAcceptsValidFeedURL(t *testing.T) {
	parsed, err := Parse("https://example.com:8443/feed.xml?page=2")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Hostname() != "example.com" {
		t.Fatalf("Hostname() = %q, want example.com", parsed.Hostname())
	}
	if parsed.Port() != "8443" {
		t.Fatalf("Port() = %q, want 8443", parsed.Port())
	}
}

func TestIsSensitiveQueryKey(t *testing.T) {
	sensitive := []string{"token", "API_KEY", " secret ", "Authorization", "password"}
	for _, key := range sensitive {
		if !IsSensitiveQueryKey(key) {
			t.Fatalf("IsSensitiveQueryKey(%q) = false, want true", key)
		}
	}

	harmless := []string{"page", "format", "category", ""}
	for _, key := range harmless {
		if IsSensitiveQueryKey(key) {
			t.Fatalf("IsSensitiveQueryKey(%q) = true, want false", key)
		}
	}
}

// TestParseRejectsSensitiveQueryKeysAcrossQueryShapes pins the scope of the
// sensitive-key scan: it must see pairs that url.Query() silently drops,
// it must keep scanning past the
// first "&"-separated segment (review 2026-09-03 finding 1: a mutant that
// only inspected the first segment passed the whole suite undetected), it
// must treat a valueless key the way url.Values does (review 2026-09-03
// finding 2: "?token" alone is a key, not a skippable non-pair), while still
// accepting URLs that carry no sensitive key at all.
func TestParseRejectsSensitiveQueryKeysAcrossQueryShapes(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		wantReject bool
	}{
		{"semicolon separator", "https://example.com/feed.xml?token=secret;x=1", true},
		{"semicolon separator, key second", "https://example.com/feed.xml?x=1;token=secret", true},
		{"token in path, not query", "https://example.com/token/feed.xml", false},
		{"percent-encoded key", "https://example.com/feed.xml?%74oken=secret", true},
		{"repeated key", "https://example.com/feed.xml?token=a&token=b", true},
		{"harmless semicolon-separated query", "https://example.com/feed.xml?page=2;format=json", false},
		// Malformed percent-encoding falls back to the raw key, same as
		// textutil's redactRawQuery. This only pins that a malformed
		// percent-escape does not itself cause a rejection -- it does NOT
		// pin the fallback assignment itself: url.QueryUnescape errors
		// solely on a malformed "%" escape, and no sensitive key contains
		// "%", so returning the raw key on error and returning "" are
		// indistinguishable by any input, and no test can tell them apart.
		{"malformed percent-encoding, harmless key", "https://example.com/feed.xml?%zztoken=secret", false},
		// A harmless first parameter followed by a sensitive second one --
		// the most common real shape, "?format=rss&api_key=...". A mutation
		// that only scans the first "&"-segment passes every other row in
		// this table and is caught only by this one.
		{"harmless first param, sensitive second", "https://example.com/feed.xml?page=2&token=secret", true},
		// Bare keys (no "="): url.Values treats a valueless key as present
		// with an empty value, so these must still be rejected/accepted the
		// same way a keyed pair would be.
		{"bare sensitive key, no value", "https://example.com/feed.xml?token", true},
		{"bare sensitive key followed by harmless pair", "https://example.com/feed.xml?token&page=2", true},
		{"bare harmless key, no value", "https://example.com/feed.xml?embed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.rawURL)
			if tt.wantReject {
				if err == nil {
					t.Fatal("Parse() succeeded, want sensitive-query-key rejection")
				}
				if !strings.Contains(err.Error(), "not allowed") {
					t.Fatalf("Parse() error = %v, want sensitive-query-key context", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v, want accepted", err)
			}
		})
	}
}

func TestValidateForFetchRejectsBeforeDNS(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
	}{
		{"malformed URL", ""},
		{"plain http without opt-in", "http://example.com/feed.xml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateForFetch(context.Background(), tt.rawURL, errorResolver{}, Options{})
			if err == nil {
				t.Fatal("ValidateForFetch() succeeded, want rejection")
			}
			if strings.Contains(err.Error(), "resolver unavailable") {
				t.Fatalf("ValidateForFetch() consulted DNS before static validation: %v", err)
			}
		})
	}
}

func TestValidateResolvedHostBlocksLocalhostWithoutDNS(t *testing.T) {
	err := ValidateResolvedHost(context.Background(), "localhost", errorResolver{}, Options{})
	if err == nil {
		t.Fatal("ValidateResolvedHost() succeeded, want blocked-host rejection")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("ValidateResolvedHost() error = %v, want blocked-host context", err)
	}
}

func TestValidateResolvedHostSkipsDNSWhenPrivateNetworksAllowed(t *testing.T) {
	err := ValidateResolvedHost(context.Background(), "intranet.corp", errorResolver{}, Options{AllowPrivateNetworks: true})
	if err != nil {
		t.Fatalf("ValidateResolvedHost() error = %v, want DNS lookup skipped", err)
	}
}

func TestResolveAllowedIPsWithIPLiterals(t *testing.T) {
	ctx := context.Background()

	if _, err := ResolveAllowedIPs(ctx, "10.1.2.3", errorResolver{}, Options{}); err == nil {
		t.Fatal("ResolveAllowedIPs(private literal) succeeded, want blocked-range rejection")
	}

	public, err := ResolveAllowedIPs(ctx, "93.184.216.34", errorResolver{}, Options{})
	if err != nil {
		t.Fatalf("ResolveAllowedIPs(public literal) error = %v", err)
	}
	if len(public) != 1 || public[0] != netip.MustParseAddr("93.184.216.34") {
		t.Fatalf("ResolveAllowedIPs(public literal) = %v, want the literal address", public)
	}

	private, err := ResolveAllowedIPs(ctx, "10.1.2.3", errorResolver{}, Options{AllowPrivateNetworks: true})
	if err != nil {
		t.Fatalf("ResolveAllowedIPs(private literal, allowed) error = %v", err)
	}
	if len(private) != 1 || private[0] != netip.MustParseAddr("10.1.2.3") {
		t.Fatalf("ResolveAllowedIPs(private literal, allowed) = %v, want the literal address", private)
	}

	mapped, err := ResolveAllowedIPs(ctx, "::ffff:93.184.216.34", errorResolver{}, Options{})
	if err != nil {
		t.Fatalf("ResolveAllowedIPs(mapped literal) error = %v", err)
	}
	if len(mapped) != 1 || mapped[0] != netip.MustParseAddr("93.184.216.34") {
		t.Fatalf("ResolveAllowedIPs(mapped literal) = %v, want the unmapped IPv4 address", mapped)
	}

	// A NAT64 address wrapping a public IPv4 is accepted, and the ORIGINAL address
	// is returned: the dialer must connect to the literal that was validated, never
	// to the translated IPv4.
	nat64, err := ResolveAllowedIPs(ctx, "64:ff9b::5db8:d822", errorResolver{}, Options{})
	if err != nil {
		t.Fatalf("ResolveAllowedIPs(NAT64 literal) error = %v", err)
	}
	if len(nat64) != 1 || nat64[0] != netip.MustParseAddr("64:ff9b::5db8:d822") {
		t.Fatalf("ResolveAllowedIPs(NAT64 literal) = %v, want the original IPv6 address", nat64)
	}
}

func TestResolveAllowedIPsLookupFailure(t *testing.T) {
	_, err := ResolveAllowedIPs(context.Background(), "news.example", errorResolver{}, Options{})
	if err == nil {
		t.Fatal("ResolveAllowedIPs() succeeded, want lookup failure")
	}
	if !strings.Contains(err.Error(), "failed to resolve feed host") {
		t.Fatalf("ResolveAllowedIPs() error = %v, want resolve-failure context", err)
	}
}

func TestResolveAllowedIPsNoAddresses(t *testing.T) {
	_, err := ResolveAllowedIPs(context.Background(), "unknown.example", fakeResolver{}, Options{})
	if err == nil {
		t.Fatal("ResolveAllowedIPs() succeeded, want no-addresses rejection")
	}
	if !strings.Contains(err.Error(), "no addresses") {
		t.Fatalf("ResolveAllowedIPs() error = %v, want no-addresses context", err)
	}
}

func TestResolveAllowedIPsInvalidResolvedIP(t *testing.T) {
	resolver := fakeResolver{
		"broken.example": {{IP: net.IP{1, 2, 3}}},
	}

	_, err := ResolveAllowedIPs(context.Background(), "broken.example", resolver, Options{})
	if err == nil {
		t.Fatal("ResolveAllowedIPs() succeeded, want invalid-IP rejection")
	}
	if !strings.Contains(err.Error(), "invalid IP address") {
		t.Fatalf("ResolveAllowedIPs() error = %v, want invalid-IP context", err)
	}
}

func TestResolveAllowedIPsUsesDefaultResolverWhenNil(t *testing.T) {
	addrs, err := ResolveAllowedIPs(context.Background(), "localhost", nil, Options{AllowPrivateNetworks: true})
	if err != nil {
		t.Fatalf("ResolveAllowedIPs(localhost, nil resolver) error = %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("ResolveAllowedIPs(localhost) returned no addresses")
	}
	for _, addr := range addrs {
		if !addr.IsLoopback() {
			t.Fatalf("ResolveAllowedIPs(localhost) returned non-loopback address %v", addr)
		}
	}
}

func TestGuardedDialerRejectsInvalidAddress(t *testing.T) {
	dialer := GuardedDialer{Resolver: fakeResolver{}}

	_, err := dialer.DialContext(context.Background(), "tcp", "missing-port")
	if err == nil {
		t.Fatal("DialContext() succeeded, want invalid-address rejection")
	}
	if !strings.Contains(err.Error(), "invalid feed dial address") {
		t.Fatalf("DialContext() error = %v, want invalid-address context", err)
	}
}

func TestGuardedDialerBlocksPrivateResolution(t *testing.T) {
	dialer := GuardedDialer{
		Resolver: fakeResolver{
			"internal.example": {{IP: net.ParseIP("192.168.1.10")}},
		},
	}

	_, err := dialer.DialContext(context.Background(), "tcp", "internal.example:443")
	if err == nil {
		t.Fatal("DialContext() succeeded, want blocked-range rejection")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("DialContext() error = %v, want blocked-address context", err)
	}
}

func TestGuardedDialerDialsResolvedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Errorf("listener.Close() error = %v", err)
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}

	dialer := GuardedDialer{
		Resolver: fakeResolver{
			"feed.example": {{IP: net.ParseIP("127.0.0.1")}},
		},
		Options: Options{AllowPrivateNetworks: true},
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("feed.example", port))
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close() error = %v", err)
	}
}

func TestGuardedDialerReturnsLastDialError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}

	dialer := GuardedDialer{
		Resolver: fakeResolver{},
		Options:  Options{AllowPrivateNetworks: true},
	}

	if _, err := dialer.DialContext(context.Background(), "tcp", address); err == nil {
		t.Fatal("DialContext() succeeded against closed listener, want dial error")
	}
}

func TestValidateRejectsIPv6TranslationPrefixes(t *testing.T) {
	tests := []string{
		"https://[64:ff9b::a9fe:a9fe]/feed.xml",
		"https://[64:ff9b::7f00:1]/feed.xml",
		"https://[64:ff9b::a00:1]/feed.xml",
		"https://[64:ff9b:1::a9fe:a9fe]/feed.xml",
		"https://[64:ff9b:1:ffff::a9fe:a9fe]/feed.xml",
		"https://[2002:a9fe:a9fe::]/feed.xml",
		"https://[2002:7f00:1::]/feed.xml",
		"https://[2002:a00:1::]/feed.xml",
		"https://[::a9fe:a9fe]/feed.xml",
		"https://[::7f00:1]/feed.xml",
		"https://[::a00:1]/feed.xml",
		"https://[2001:0:a9fe:a9fe::]/feed.xml",
		"https://[2001:0:4136:e378:8000:63bf:3fff:fdd2]/feed.xml",
		"https://[2606:4700::5efe:a9fe:a9fe]/feed.xml",
		"https://[2606:4700::200:5efe:a00:1]/feed.xml",
		// ISATAP interface identifier under a blocked prefix, embedding a PUBLIC
		// IPv4: only the check of the original address can reject these.
		"https://[fe80::5efe:5db8:d822]/feed.xml",
		"https://[fc00::200:5efe:5db8:d822]/feed.xml",
		"https://[::240.0.0.1]/feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			err := Validate(rawURL, Options{})
			if err == nil {
				t.Fatal("Validate() succeeded, want IPv6-transition rejection")
			}
			// The address must be refused as a blocked host, not incidentally as a
			// malformed URL or an unsupported literal.
			if err.Error() != "feed URL host is blocked" {
				t.Fatalf("Validate() error = %v, want %q", err, "feed URL host is blocked")
			}
		})
	}
}

func TestValidateRejectsReservedIPv4Space(t *testing.T) {
	tests := []string{
		"https://240.0.0.1/feed.xml",
		"https://240.255.255.254/feed.xml",
		"https://255.255.255.254/feed.xml",
		// RFC 1122 "this network", 0.0.0.0/8: same class as 240.0.0.0/4 above,
		// found while reviewing that fix. Only the exact
		// 0.0.0.0 was previously caught, by IsUnspecified().
		"https://0.0.0.0/feed.xml",
		"https://0.0.0.1/feed.xml",
		"https://0.255.255.255/feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			err := Validate(rawURL, Options{})
			if err == nil {
				t.Fatal("Validate() succeeded, want reserved-range rejection")
			}
			if err.Error() != "feed URL host is blocked" {
				t.Fatalf("Validate() error = %v, want %q", err, "feed URL host is blocked")
			}
		})
	}
}

// TestValidateAcceptsFirstAddressOutsideReservedIPv4Space pins the upper
// boundary of the 0.0.0.0/8 block: 1.0.0.0 is the first address outside the
// range and must not be newly rejected by the fix.
func TestValidateAcceptsFirstAddressOutsideReservedIPv4Space(t *testing.T) {
	if err := Validate("https://1.0.0.0/feed.xml", Options{}); err != nil {
		t.Fatalf("Validate() error = %v, want 1.0.0.0 accepted", err)
	}
}

// TestValidateRejectsIPv6SiteLocalSpace pins fec0::/10, deprecated IPv6
// site-local (RFC 3879): the IPv6 analogue of RFC 1918 that neither
// netip.Addr.IsPrivate() (Go covers only fc00::/7) nor IsLinkLocalUnicast
// catches, and that can address a reachable host inside the operator's
// network, unlike the neighbouring ranges left out below.
func TestValidateRejectsIPv6SiteLocalSpace(t *testing.T) {
	tests := []string{
		"https://[fec0::]/feed.xml", // first address inside fec0::/10
		"https://[fec0::1]/feed.xml",
		"https://[fedf::]/feed.xml",                                  // mid-range
		"https://[feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff]/feed.xml", // last address inside fec0::/10
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			err := Validate(rawURL, Options{})
			if err == nil {
				t.Fatal("Validate() succeeded, want IPv6 site-local rejection")
			}
			if err.Error() != "feed URL host is blocked" {
				t.Fatalf("Validate() error = %v, want %q", err, "feed URL host is blocked")
			}
		})
	}
}

// TestBlockedSpecialUsePrefixesBoundaryIPv6SiteLocal pins the exact edges of
// the fec0::/10 entry against blockedSpecialUsePrefixes directly (rather than
// through Validate), because the addresses immediately adjacent to the range
// on both sides -- febf:...:ffff (fe80::/10, link-local) and ff00:: (multicast)
// -- are already blocked for unrelated reasons and so cannot show a boundary
// regression through Validate's combined result alone.
func TestBlockedSpecialUsePrefixesBoundaryIPv6SiteLocal(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"first address inside fec0::/10", "fec0::", true},
		{"last address inside fec0::/10", "feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"first address below fec0::/10 (link-local range, blocked separately)", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"first address above fec0::/10 (multicast range, blocked separately)", "ff00::", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			var blocked bool
			for _, prefix := range blockedSpecialUsePrefixes {
				if prefix.Contains(addr) {
					blocked = true
					break
				}
			}
			if blocked != tt.want {
				t.Fatalf("blockedSpecialUsePrefixes membership for %s = %v, want %v", tt.addr, blocked, tt.want)
			}
		})
	}
}

// TestValidateAcceptsIPv6RangesLeftOutOfSiteLocalBlock pins that the five
// weaker ranges considered alongside fec0::/10 stay accepted: they cannot
// address a reachable internal host, the same reasoning that kept the IPv4
// TEST-NETs out.
func TestValidateAcceptsIPv6RangesLeftOutOfSiteLocalBlock(t *testing.T) {
	tests := []string{
		"https://[2001:db8::1]/feed.xml", // documentation (RFC 3849)
		"https://[3fff::1]/feed.xml",     // documentation (RFC 9637)
		"https://[2001:2::1]/feed.xml",   // BMWG (RFC 5180)
		"https://[100::1]/feed.xml",      // discard-only (RFC 6666)
		"https://[5f00::1]/feed.xml",     // SRv6 SIDs (RFC 9602)
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{}); err != nil {
				t.Fatalf("Validate() error = %v, want address accepted (left out of scope)", err)
			}
		})
	}
}

// TestValidateForFetchRejectsResolvedIPv6SiteLocalAddress pins the DNS-resolve
// layer: a feed hostname that resolves into fec0::/10 at fetch time -- the
// interesting case, since a literal in the config is caught by Validate
// alone -- must be rejected too.
func TestValidateForFetchRejectsResolvedIPv6SiteLocalAddress(t *testing.T) {
	resolver := fakeResolver{
		"internal-v6.example": {{IP: net.ParseIP("fec0::1")}},
	}

	err := ValidateForFetch(context.Background(), "https://internal-v6.example/feed.xml", resolver, Options{})
	if err == nil {
		t.Fatal("ValidateForFetch() succeeded, want blocked-range rejection")
	}
	if !strings.Contains(err.Error(), "blocked address range") {
		t.Fatalf("ValidateForFetch() error = %v, want blocked-address context", err)
	}
}

// TestGuardedDialerBlocksIPv6SiteLocalResolution pins the runtime dialer that
// re-resolves on every redirect hop: a host resolving into fec0::/10 at dial
// time must never be dialed.
func TestGuardedDialerBlocksIPv6SiteLocalResolution(t *testing.T) {
	dialer := GuardedDialer{
		Resolver: fakeResolver{
			"internal-v6.example": {{IP: net.ParseIP("fec0::1")}},
		},
	}

	_, err := dialer.DialContext(context.Background(), "tcp", "internal-v6.example:443")
	if err == nil {
		t.Fatal("DialContext() succeeded, want blocked-range rejection")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("DialContext() error = %v, want blocked-address context", err)
	}
}

func TestValidateAcceptsIPv6TranslationOfPublicIPv4(t *testing.T) {
	tests := []string{
		"https://[64:ff9b::5db8:d822]/feed.xml",
		"https://[2002:5db8:d822::1]/feed.xml",
		"https://[2001:4860:4860::8888]/feed.xml",
		"https://[2606:4700:4700::1111]/feed.xml",
		"https://[2a00:1450:4001:80b::200e]/feed.xml",
		"https://[2606:4700::5efe:5db8:d822]/feed.xml",
		"https://[2606:4700::200:5efe:5db8:d822]/feed.xml",
		"https://[2606:4700::300:5efe:a00:1]/feed.xml",
		"https://[2606:4700::200:5eff:a00:1]/feed.xml",
		"https://[2606:4700::200:5dfe:a00:1]/feed.xml",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{}); err != nil {
				t.Fatalf("Validate() error = %v, want public address accepted", err)
			}
		})
	}
}

func TestResolveAllowedIPsBlocksTranslatedPrefixes(t *testing.T) {
	resolver := fakeResolver{
		"nat64.example":     {{IP: net.ParseIP("64:ff9b::a9fe:a9fe")}},
		"sixtofour.example": {{IP: net.ParseIP("2002:a9fe:a9fe::")}},
		"compat.example":    {{IP: net.ParseIP("::a9fe:a9fe")}},
		"teredo.example":    {{IP: net.ParseIP("2001:0:a9fe:a9fe::")}},
		"reserved.example":  {{IP: net.ParseIP("240.0.0.1")}},
	}

	for host := range resolver {
		t.Run(host, func(t *testing.T) {
			_, err := ResolveAllowedIPs(context.Background(), host, resolver, Options{})
			if err == nil {
				t.Fatal("ResolveAllowedIPs() succeeded, want blocked-range rejection")
			}
			if !strings.Contains(err.Error(), "blocked address range") {
				t.Fatalf("ResolveAllowedIPs() error = %v, want blocked-address context", err)
			}
		})
	}
}

func TestGuardedDialerBlocksTranslatedPrefixResolution(t *testing.T) {
	dialer := GuardedDialer{
		Resolver: fakeResolver{
			"nat64.invalid": {{IP: net.ParseIP("64:ff9b::a9fe:a9fe")}},
		},
	}

	// Bounded: on an unfixed guard DialContext attempts a real outbound connect.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := dialer.DialContext(ctx, "tcp", "nat64.invalid:443")
	if err == nil {
		t.Fatal("DialContext() succeeded, want blocked-range rejection")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("DialContext() error = %v, want blocked-address context", err)
	}
}

func TestValidateAllowsTranslatedPrefixesWhenPrivateNetworksAllowed(t *testing.T) {
	// AllowPrivateNetworks bypasses the whole address check, not just the private
	// ranges: translated prefixes, wholesale-blocked tunnel prefixes and reserved
	// IPv4 space are all accepted with it. No production call site sets it.
	for _, rawURL := range []string{
		"https://[64:ff9b::a9fe:a9fe]/feed.xml", // NAT64 of link-local
		"https://[2002:a00:1::]/feed.xml",       // 6to4 of RFC 1918
		"https://[2001:0:a9fe:a9fe::]/feed.xml", // Teredo, blocked wholesale
		"https://[64:ff9b:1::a9fe:a9fe]/feed.xml",
		"https://[::7f00:1]/feed.xml",
		"https://[fe80::5efe:5db8:d822]/feed.xml",
		"https://240.0.0.1/feed.xml",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL, Options{AllowPrivateNetworks: true}); err != nil {
				t.Fatalf("Validate() error = %v, want address check bypassed", err)
			}
		})
	}
}

func TestEmbeddedIPv4(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		wantOK      bool
		wantEmbed   string
		wantBlocked bool
	}{
		{"NAT64 well-known", "64:ff9b::a9fe:a9fe", true, "169.254.169.254", true},
		{"NAT64 of public IPv4", "64:ff9b::5db8:d822", true, "93.184.216.34", false},
		{"6to4", "2002:5db8:d822::1", true, "93.184.216.34", false},
		{"6to4 of private IPv4", "2002:a00:1::", true, "10.0.0.1", true},
		{"ISATAP 0000:5efe", "2606:4700::5efe:a9fe:a9fe", true, "169.254.169.254", true},
		{"ISATAP 0200:5efe", "2606:4700::200:5efe:a00:1", true, "10.0.0.1", true},
		{"ISATAP of public IPv4", "2606:4700::200:5efe:5db8:d822", true, "93.184.216.34", false},
		{"ISATAP link-local", "fe80::5efe:a00:1", true, "10.0.0.1", true},
		// Translation yields a PUBLIC IPv4 while the ORIGINAL address is blocked:
		// these rows pin that isBlockedAddress still checks the original after the
		// embedded one. Without the fall-through they would be accepted.
		{"ISATAP link-local of public IPv4", "fe80::5efe:5db8:d822", true, "93.184.216.34", true},
		{"ISATAP ULA of public IPv4", "fc00::200:5efe:5db8:d822", true, "93.184.216.34", true},
		{"ISATAP multicast of public IPv4", "ff02::200:5efe:5db8:d822", true, "93.184.216.34", true},
		{"IPv4-mapped raw", "::ffff:1.2.3.4", false, "", false},
		{"plain IPv4", "1.2.3.4", false, "", false},
		{"public IPv4", "93.184.216.34", false, "", false},
		{"public IPv6", "2606:4700:4700::1111", false, "", false},
		{"RFC 8215 local-use NAT64", "64:ff9b:1::1", false, "", true},
		{"ISATAP near-miss byte 8", "2606:4700::300:5efe:a00:1", false, "", false},
		{"ISATAP near-miss byte 11", "2606:4700::200:5eff:a00:1", false, "", false},
		{"ISATAP near-miss byte 10", "2606:4700::200:5dfe:a00:1", false, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)

			embedded, ok := embeddedIPv4(addr)
			if ok != tt.wantOK {
				t.Fatalf("embeddedIPv4(%s) ok = %v, want %v", tt.addr, ok, tt.wantOK)
			}
			if tt.wantOK && embedded != netip.MustParseAddr(tt.wantEmbed) {
				t.Fatalf("embeddedIPv4(%s) = %v, want %v", tt.addr, embedded, tt.wantEmbed)
			}
			if got := isBlockedAddress(addr); got != tt.wantBlocked {
				t.Fatalf("isBlockedAddress(%s) = %v, want %v", tt.addr, got, tt.wantBlocked)
			}
		})
	}
}
