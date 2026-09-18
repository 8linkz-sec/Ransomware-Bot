package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- Error type formatting ---

func TestHTTPStatusErrorError(t *testing.T) {
	var nilErr *HTTPStatusError
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil HTTPStatusError.Error() = %q, want empty string", got)
	}

	withoutBody := &HTTPStatusError{StatusCode: http.StatusNotFound, Status: "404 Not Found"}
	if got := withoutBody.Error(); got != "API returned status 404: 404 Not Found" {
		t.Fatalf("HTTPStatusError.Error() without body = %q", got)
	}

	withBody := &HTTPStatusError{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Body: "denied"}
	if got := withBody.Error(); got != "API returned status 403: 403 Forbidden - denied" {
		t.Fatalf("HTTPStatusError.Error() with body = %q", got)
	}
}

func TestRateLimitCooldownErrorError(t *testing.T) {
	var nilErr *RateLimitCooldownError
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil RateLimitCooldownError.Error() = %q, want empty string", got)
	}

	until := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	err := &RateLimitCooldownError{Until: until}
	if got := err.Error(); got != "API rate limited until 2026-02-03T04:05:06Z" {
		t.Fatalf("RateLimitCooldownError.Error() = %q", got)
	}
}

func TestOperatorErrorMessage(t *testing.T) {
	cooldownUntil := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil error", nil, ""},
		{"context canceled", fmt.Errorf("request failed: %w", context.Canceled), "API request canceled"},
		{"context deadline", fmt.Errorf("request failed: %w", context.DeadlineExceeded), "API request timed out; provider did not respond before the polling deadline"},
		{"rate limit cooldown", &RateLimitCooldownError{Until: cooldownUntil}, "API rate limited; retry after 2026-02-03T04:05:06Z"},
		{"status 400", &HTTPStatusError{StatusCode: http.StatusBadRequest}, "API request rejected by provider; check API configuration"},
		{"status 401", &HTTPStatusError{StatusCode: http.StatusUnauthorized}, "API authentication failed; check api_key and provider access"},
		{"status 403", &HTTPStatusError{StatusCode: http.StatusForbidden}, "API authentication failed; check api_key and provider access"},
		{"status 404", &HTTPStatusError{StatusCode: http.StatusNotFound}, "API endpoint not found; check provider API compatibility"},
		{"status 429", &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, "API rate limited; retry on next poll"},
		{"status 503", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, "API provider unavailable; retry on next poll"},
		{"status 418 uses 4xx default", &HTTPStatusError{StatusCode: http.StatusTeapot}, "API request rejected by provider; check API configuration"},
		{"status 599 uses 5xx default", &HTTPStatusError{StatusCode: 599}, "API provider unavailable; retry on next poll"},
		{"status 302 falls through to generic", &HTTPStatusError{StatusCode: http.StatusFound}, "API request failed; check network connectivity and provider availability"},
		{"network timeout", testNetError{msg: "i/o timeout", timeout: true}, "API request timed out; check network connectivity and provider availability"},
		{"decode failure", errors.New("failed to decode response: unexpected EOF"), "API response could not be decoded; provider response format may have changed"},
		{"generic failure", errors.New("something unexpected"), "API request failed; check network connectivity and provider availability"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OperatorErrorMessage(tt.err); got != tt.want {
				t.Fatalf("OperatorErrorMessage(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// --- Policy defaults and constructor validation ---

func TestHTTPPolicyWithDefaults(t *testing.T) {
	defaults := DefaultHTTPPolicy()

	zero := HTTPPolicy{}.withDefaults()
	if zero.RequestTimeout != defaults.RequestTimeout {
		t.Fatalf("zero RequestTimeout = %v, want default %v", zero.RequestTimeout, defaults.RequestTimeout)
	}
	if zero.MaxAttempts != defaults.MaxAttempts {
		t.Fatalf("zero MaxAttempts = %d, want default %d", zero.MaxAttempts, defaults.MaxAttempts)
	}
	// Zero is an explicit "no delay" value and is preserved as-is.
	if zero.RetryBaseDelay != 0 {
		t.Fatalf("zero RetryBaseDelay = %v, want 0 preserved", zero.RetryBaseDelay)
	}

	negative := HTTPPolicy{RetryBaseDelay: -time.Second}.withDefaults()
	if negative.RetryBaseDelay != defaults.RetryBaseDelay {
		t.Fatalf("negative RetryBaseDelay = %v, want default %v", negative.RetryBaseDelay, defaults.RetryBaseDelay)
	}
}

func TestNewClientWithBaseURLAndPolicyRejectsInvalidBaseURL(t *testing.T) {
	client, err := NewClientWithBaseURLAndPolicy("test-key", "", DefaultHTTPPolicy())
	if err == nil {
		t.Fatalf("NewClientWithBaseURLAndPolicy() = %v, want invalid base URL error", client)
	}
	if !strings.Contains(err.Error(), "api base URL") {
		t.Fatalf("error = %v, want base URL context", err)
	}
}

func TestNewClientWithHTTPClientRejectsNilClient(t *testing.T) {
	client, err := NewClientWithHTTPClient("test-key", "https://api.example.test", nil, HTTPPolicy{})
	if err == nil {
		t.Fatalf("NewClientWithHTTPClient(nil) = %v, want error", client)
	}
	if !strings.Contains(err.Error(), "http client is nil") {
		t.Fatalf("error = %v, want nil client context", err)
	}
}

func TestNewClientWithHTTPClientRejectsInvalidBaseURL(t *testing.T) {
	client, err := NewClientWithHTTPClient("test-key", "ftp://api.example.test", &http.Client{}, HTTPPolicy{})
	if err == nil {
		t.Fatalf("NewClientWithHTTPClient(ftp URL) = %v, want error", client)
	}
	if !strings.Contains(err.Error(), "scheme must be http or https") {
		t.Fatalf("error = %v, want scheme context", err)
	}
}

// --- Redirect policy helpers ---

func TestAPIRedirectPolicyBlocksExcessiveHopsAndMissingURL(t *testing.T) {
	policy := apiRedirectPolicy("https://api.example.test")

	via := make([]*http.Request, apiMaxCredentialRedirects)
	req, err := http.NewRequest(http.MethodGet, "https://api.example.test/next", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := policy(req, via); err == nil || !strings.Contains(err.Error(), "redirect blocked after") {
		t.Fatalf("hop-limit error = %v, want redirect blocked after context", err)
	}

	if err := policy(nil, nil); err == nil || !strings.Contains(err.Error(), "missing redirect URL") {
		t.Fatalf("nil request error = %v, want missing redirect URL context", err)
	}
	if err := policy(&http.Request{}, nil); err == nil || !strings.Contains(err.Error(), "missing redirect URL") {
		t.Fatalf("nil URL error = %v, want missing redirect URL context", err)
	}
}

func TestSameAPIHostNilURLs(t *testing.T) {
	parsed, err := url.Parse("https://api.example.test")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	if sameAPIHost(nil, parsed) {
		t.Error("sameAPIHost(nil, url) = true, want false")
	}
	if sameAPIHost(parsed, nil) {
		t.Error("sameAPIHost(url, nil) = true, want false")
	}
	if !sameAPIHost(parsed, parsed) {
		t.Error("sameAPIHost(url, url) = false, want true")
	}
}

func TestNormalizedURLPort(t *testing.T) {
	tests := []struct {
		rawURL string
		want   string
	}{
		{"https://api.example.test", "443"},
		{"http://api.example.test", "80"},
		{"https://api.example.test:8443", "8443"},
		{"ftp://api.example.test", ""},
	}

	for _, tt := range tests {
		t.Run(tt.rawURL, func(t *testing.T) {
			parsed, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", tt.rawURL, err)
			}
			if got := normalizedURLPort(parsed); got != tt.want {
				t.Fatalf("normalizedURLPort(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr string
	}{
		{name: "valid https", rawURL: "https://api.example.test", want: "https://api.example.test"},
		{name: "trailing slashes trimmed", rawURL: "https://api.example.test///", want: "https://api.example.test"},
		{name: "empty", rawURL: "", wantErr: "api base URL is empty"},
		{name: "whitespace only", rawURL: "   ", wantErr: "api base URL is empty"},
		{name: "unparseable", rawURL: "http://[::1", wantErr: "invalid api base URL"},
		{name: "missing host", rawURL: "http://", wantErr: "api base URL must include a host"},
		{name: "unsupported scheme", rawURL: "ftp://api.example.test", wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.rawURL)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NormalizeBaseURL(%q) error = %v, want %q", tt.rawURL, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBaseURL(%q) error = %v", tt.rawURL, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

// --- Rate limit cooldown state ---

func TestRateLimitCooldownExpiresAndClears(t *testing.T) {
	client, err := NewClientWithBaseURL("test-key", "https://api.example.test")
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	if err := client.rateLimitCooldownError(now); err != nil {
		t.Fatalf("cooldown error without cooldown = %v, want nil", err)
	}

	client.setRateLimitCooldown(now.Add(time.Minute))
	err = client.rateLimitCooldownError(now)
	var cooldownErr *RateLimitCooldownError
	if !errors.As(err, &cooldownErr) {
		t.Fatalf("active cooldown error = %T %v, want RateLimitCooldownError", err, err)
	}
	if !cooldownErr.Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("cooldown until = %v, want %v", cooldownErr.Until, now.Add(time.Minute))
	}

	if err := client.rateLimitCooldownError(now.Add(2 * time.Minute)); err != nil {
		t.Fatalf("expired cooldown error = %v, want nil", err)
	}
	// Expired cooldown must be cleared so an earlier timestamp no longer trips it.
	if err := client.rateLimitCooldownError(now); err != nil {
		t.Fatalf("cooldown error after clear = %v, want nil", err)
	}
}

// --- Context-aware retry sleep ---

func TestSleepWithContext(t *testing.T) {
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepWithContext(0) error = %v, want nil", err)
	}
	if err := sleepWithContext(context.Background(), -time.Second); err != nil {
		t.Fatalf("sleepWithContext(negative) error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepWithContext(canceled) error = %v, want context.Canceled", err)
	}

	if err := sleepWithContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepWithContext(1ms) error = %v, want nil", err)
	}
}

// --- Bounded response decoding ---

func TestDecodeBoundedAPIResponseRejectsOversizedDocument(t *testing.T) {
	// A single JSON string value that consumes exactly the read budget
	// (maxAPIResponseBytes + 1 bytes) decodes successfully but must still be
	// rejected as oversized.
	reader := io.MultiReader(
		strings.NewReader(`"`),
		io.LimitReader(repeatingByteReader('a'), maxAPIResponseBytes-1),
		strings.NewReader(`"`),
	)

	var target string
	err := decodeBoundedAPIResponse(reader, &target)
	if err == nil {
		t.Fatal("expected oversized document error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want byte-limit context", err)
	}
}

func TestDecodeBoundedAPIResponseRejectsTrailingGarbage(t *testing.T) {
	reader := strings.NewReader(`{"client":"test","count":0,"order":"desc","victims":[]} @@@`)

	var response ransomwareResponse
	err := decodeBoundedAPIResponse(reader, &response)
	if err == nil {
		t.Fatal("expected trailing garbage error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("error = %v, want trailing data context", err)
	}
}

// --- ransomwareAPITime edge cases ---

func TestRansomwareAPITimeUnmarshalJSONBareNull(t *testing.T) {
	var ct ransomwareAPITime
	if err := json.Unmarshal([]byte(`null`), &ct); err != nil {
		t.Fatalf("Unmarshal(null) error = %v", err)
	}
	if !ct.IsZero() {
		t.Fatalf("Unmarshal(null) time = %v, want zero", ct.Time)
	}
}

func TestRansomwareAPITimeUnmarshalJSONRejectsNonString(t *testing.T) {
	var ct ransomwareAPITime
	err := json.Unmarshal([]byte(`12345`), &ct)
	if err == nil {
		t.Fatal("expected error for numeric timestamp, got nil")
	}
	if !strings.Contains(err.Error(), "expected JSON string or null") {
		t.Fatalf("error = %v, want JSON string or null context", err)
	}
}

// --- GetLatestEntries edge cases ---

func TestGetLatestEntriesDefaultsToRansomwareLiveAPI(t *testing.T) {
	var requestedURL string
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedURL = req.URL.String()
		body := `{"client":"test","count":1,"order":"desc","victims":[{"id":"abc-123","group":"LockBit","victim":"TestCorp"}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}}

	entries, err := client.GetLatestEntries(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	if want := ransomwareLiveAPI + "/victims/recent"; requestedURL != want {
		t.Fatalf("requested URL = %q, want default %q", requestedURL, want)
	}
	if len(entries) != 1 || entries[0].ID != "abc-123" {
		t.Fatalf("entries = %+v, want single decoded victim", entries)
	}
}

func TestGetLatestEntriesRejectsNullVictimsField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":null}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatalf("GetLatestEntries() returned entries %+v, want null victims error", entries)
	}
	if !strings.Contains(err.Error(), "missing required victims field") {
		t.Fatalf("GetLatestEntries() error = %v, want missing victims context", err)
	}
}

func TestGetLatestEntriesRejectsNonArrayVictimsField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":{"unexpected":"object"}}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatalf("GetLatestEntries() returned entries %+v, want decode error", entries)
	}
	if !strings.Contains(err.Error(), "failed to decode victims field") {
		t.Fatalf("GetLatestEntries() error = %v, want victims decode context", err)
	}
}

// Finding 7 (internal/api/ransomware.go): normalizeRansomwareEntry used to
// blindly TruncateText the raw country to 2 runes before validation ever ran,
// so a lowercase or 3-letter code either got wrongly rejected (lowercase
// "us" doesn't match isISO2CountryCode's uppercase-only shape check) or
// silently mapped to the wrong real country ("AUT" truncated to "AU" is
// Australia, not Austria). The fix validates the raw value against ISO
// 3166-1 (via golang.org/x/text/language, which accepts alpha-2 and alpha-3)
// before truncation ever happens, canonicalizing a recognised code (any
// case, alpha-2 or alpha-3) and clearing an unrecognised one to "" instead of
// truncating it into something that looks valid but is wrong -- matching
// boundedAPIURLField's existing reject-to-empty idiom for the same class of
// problem.

func TestGetLatestEntriesCanonicalizesLowercaseCountryCode(t *testing.T) {
	// Before the fix, a no-id entry with a lowercase country was truncated to
	// "us" unchanged and then rejected outright by isISO2CountryCode's
	// uppercase-only shape check -- an entry with an otherwise perfectly
	// identifiable country was silently dropped in full. The fix
	// canonicalizes any recognised code (case-insensitive) before
	// validation, so the entry is delivered with the correct "US".
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":2,"order":"desc","victims":[{"group":"LockBit","victim":"LowercaseCountryCorp","country":"us","attackdate":"2025-01-01"},{"id":"abc-123","group":"LockBit","victim":"TestCorp"}]}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want both victims delivered", entries)
	}
	var lowercaseCountryEntry *RansomwareEntry
	for i := range entries {
		if entries[i].Victim == "LowercaseCountryCorp" {
			lowercaseCountryEntry = &entries[i]
		}
	}
	if lowercaseCountryEntry == nil {
		t.Fatal("expected the lowercase-country victim to be present, not skipped")
	}
	if lowercaseCountryEntry.Country != "US" {
		t.Fatalf("Country = %q, want canonicalized %q", lowercaseCountryEntry.Country, "US")
	}
}

func TestGetLatestEntriesClearsUnrecognizedCountryCodeInsteadOfDropping(t *testing.T) {
	// "XY" is in ISO 3166-1's private-use range -- not a recognised country
	// (language.Region.IsCountry() == false). The fix clears Country to ""
	// (Option B) rather than rejecting the whole entry, so the alert is
	// still delivered with the country simply omitted.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{"group":"LockBit","victim":"UnknownCountryCorp","country":"XY","attackdate":"2025-01-01"}]}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the entry delivered with country cleared, not dropped", entries)
	}
	if entries[0].Country != "" {
		t.Fatalf("Country = %q, want cleared to empty for an unrecognised code", entries[0].Country)
	}
}

func TestGetLatestEntriesMapsAlpha3CountryCodeToCorrectAlpha2(t *testing.T) {
	// The demonstrated wrong-country bug: blind truncate-to-2 turned "AUT"
	// (Austria) into "AU" (Australia's real ISO2 code) -- and reached this
	// ordinary, id-present entry exactly the same way, since
	// normalizeRansomwareEntry truncated unconditionally regardless of id.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{"id":"some-real-id-12345","group":"LockBit","victim":"AustriaCorp","country":"AUT"}]}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	if entries[0].Country != "AT" {
		t.Fatalf("Country = %q, want %q (Austria) -- not the old truncated %q (Australia's real code)", entries[0].Country, "AT", "AU")
	}
}

func TestNormalizedAPICountryCode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain iso2 unchanged", "DE", "DE"},
		{"lowercase iso2 canonicalized", "de", "DE"},
		{"alpha3 mapped to correct alpha2", "AUT", "AT"},
		{"alpha3 that happens to share its alpha2 prefix", "USA", "US"},
		{"unrecognized 2-letter private-use code cleared", "XY", ""},
		{"unrecognized 3-letter code cleared", "ZZZ", ""},
		{"empty stays empty", "", ""},
		{"whitespace trimmed then validated", "  at  ", "AT"},
		// XY/ZZZ above are shape-valid ISO codes that ParseRegion parses
		// without error and then rejects via IsCountry() == false. This case
		// instead drives ParseRegion's own err != nil branch (a full country
		// name is not a well-formed BCP-47 region subtag), so a mutation that
		// falls back to truncating the raw value on a parse error (instead of
		// clearing it) is not silently indistinguishable from correct
		// behaviour for every case in this table.
		{"non-code text cleared, not truncated into a fake code", "Austria", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedAPICountryCode(tt.raw); got != tt.want {
				t.Fatalf("normalizedAPICountryCode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// --- Entry validation ---

func TestValidateRansomwareEntry(t *testing.T) {
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name    string
		entry   RansomwareEntry
		wantErr string
	}{
		{
			name:  "id present is always valid",
			entry: RansomwareEntry{ID: "abc-123"},
		},
		{
			name:    "missing id requires group and victim",
			entry:   RansomwareEntry{Group: "LockBit"},
			wantErr: "must include group and victim",
		},
		{
			name:    "lowercase country code rejected",
			entry:   RansomwareEntry{Group: "LockBit", Victim: "Corp", Country: "us", AttackDate: "2025-01-01"},
			wantErr: `invalid country code "us"`,
		},
		{
			name:    "missing fallback discriminator rejected",
			entry:   RansomwareEntry{Group: "LockBit", Victim: "Corp", Country: "US"},
			wantErr: "stable fallback discriminator",
		},
		{
			name:  "attack date is a sufficient discriminator",
			entry: RansomwareEntry{Group: "LockBit", Victim: "Corp", Country: "US", AttackDate: "2025-01-01"},
		},
		{
			name:  "published timestamp is a sufficient discriminator",
			entry: RansomwareEntry{Group: "LockBit", Victim: "Corp", Published: published},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRansomwareEntry(tt.entry)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRansomwareEntry() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRansomwareEntry() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestIsISO2CountryCode(t *testing.T) {
	tests := []struct {
		country string
		want    bool
	}{
		{"US", true},
		{"DE", true},
		{"us", false},
		{"Us", false},
		{"USA", false},
		{"U", false},
		{"", false},
		{"U1", false},
		{"1S", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("country %q", tt.country), func(t *testing.T) {
			if got := isISO2CountryCode(tt.country); got != tt.want {
				t.Fatalf("isISO2CountryCode(%q) = %v, want %v", tt.country, got, tt.want)
			}
		})
	}
}
