package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/httpstatus"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func writeAPITestResponse(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := fmt.Fprintln(w, body); err != nil {
		t.Errorf("write API test response: %v", err)
	}
}

func newTestHTTPClient(t *testing.T, apiKey string, customClient *http.Client) *Client {
	return newTestHTTPClientWithBaseURL(t, apiKey, ransomwareLiveAPI, customClient)
}

func newTestHTTPClientWithBaseURL(t *testing.T, apiKey, baseURL string, customClient *http.Client) *Client {
	t.Helper()
	client, err := NewClientWithHTTPClient(apiKey, baseURL, customClient, HTTPPolicy{})
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}
	return client
}

func TestRansomwareAPITimeUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  time.Time
		shouldErr bool
	}{
		{
			"with microseconds",
			`"2025-01-15 10:30:45.123456"`,
			time.Date(2025, 1, 15, 10, 30, 45, 123456000, time.UTC),
			false,
		},
		{
			"without microseconds",
			`"2025-01-15 10:30:45"`,
			time.Date(2025, 1, 15, 10, 30, 45, 0, time.UTC),
			false,
		},
		{
			"null value",
			`"null"`,
			time.Time{},
			false,
		},
		{
			"empty string",
			`""`,
			time.Time{},
			false,
		},
		{
			"invalid format",
			`"not-a-date"`,
			time.Time{},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ct ransomwareAPITime
			err := json.Unmarshal([]byte(tt.input), &ct)
			if tt.shouldErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ct.Equal(tt.expected) {
				t.Errorf("got %v, want %v", ct, tt.expected)
			}
		})
	}
}

func TestRansomwareAPITimeUnmarshalJSONInvalidFormatIsActionable(t *testing.T) {
	var ct ransomwareAPITime
	err := json.Unmarshal([]byte(`"not-a-date"`), &ct)
	if err == nil {
		t.Fatal("expected timestamp parse error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"invalid ransomware API timestamp",
		"not-a-date",
		"expected one of",
		"2006-01-02 15:04:05.999999",
		time.RFC3339,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

func TestRansomwareAPITimeInStruct(t *testing.T) {
	// Test that ransomwareAPITime works embedded in a JSON struct
	jsonData := `{"discovered": "2025-06-15 08:30:00.123456", "published": "2025-06-15 09:00:00"}`

	var entry struct {
		Discovered ransomwareAPITime `json:"discovered"`
		Published  ransomwareAPITime `json:"published"`
	}

	err := json.Unmarshal([]byte(jsonData), &entry)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if entry.Discovered.Year() != 2025 || entry.Discovered.Month() != 6 || entry.Discovered.Day() != 15 {
		t.Errorf("Discovered date wrong: %v", entry.Discovered)
	}
	if entry.Published.Hour() != 9 || entry.Published.Minute() != 0 {
		t.Errorf("Published time wrong: %v", entry.Published)
	}
}

func TestRansomwareEntryExposesPlainTimestamps(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		Discovered: now,
		Published:  now.Add(time.Hour),
	}

	if !entry.Discovered.Equal(now) {
		t.Fatalf("Discovered = %v, want %v", entry.Discovered, now)
	}
	if !entry.Published.Equal(now.Add(time.Hour)) {
		t.Fatalf("Published = %v, want %v", entry.Published, now.Add(time.Hour))
	}
}

func TestGenerateEntryKey(t *testing.T) {
	entryWithID := RansomwareEntry{ID: "abc-123", Group: "LockBit", Victim: "Corp"}
	if got := GenerateEntryKey(entryWithID); got != "id:abc-123" {
		t.Fatalf("GenerateEntryKey(with ID) = %q, want id:abc-123", got)
	}

	fallbackEntries := []RansomwareEntry{
		{Group: "LockBit", Victim: "Corp"},
		{Group: "LockBit", Victim: "Corp", Country: "US"},
		{Group: "LockBit", Victim: "Corp", Country: "US", AttackDate: "2025-01-15"},
		{Group: "LockBit", Victim: "Corp", AttackDate: "2025-01-15"},
		{},
	}

	for _, entry := range fallbackEntries {
		first := GenerateEntryKey(entry)
		second := GenerateEntryKey(entry)
		if !strings.HasPrefix(first, "fallback:v2:") {
			t.Fatalf("GenerateEntryKey(%+v) = %q, want fallback:v2 prefix", entry, first)
		}
		if first != second {
			t.Fatalf("GenerateEntryKey(%+v) is not deterministic: %q != %q", entry, first, second)
		}
	}
}

func TestGenerateEntryKeyFallbackDistinguishesDistinctRecords(t *testing.T) {
	first := RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Corp",
		Country:    "US",
		AttackDate: "2025-01-15",
		ClaimURL:   "https://example.onion/first",
	}
	second := first
	second.ClaimURL = "https://example.onion/second"

	firstKey := GenerateEntryKey(first)
	secondKey := GenerateEntryKey(second)
	legacyKey := "LockBit|Corp|US|2025-01-15"

	if firstKey == secondKey {
		t.Fatalf("fallback keys collide: %q", firstKey)
	}
	if firstKey == legacyKey || secondKey == legacyKey {
		t.Fatalf("fallback key still uses legacy delimiter format: first=%q second=%q", firstKey, secondKey)
	}
}

func TestGenerateEntryKeyFallbackDoesNotCollideOnLegacySeparator(t *testing.T) {
	first := RansomwareEntry{Group: "a|b", Victim: "c", Country: "DE", AttackDate: "2026-01-01"}
	second := RansomwareEntry{Group: "a", Victim: "b|c", Country: "DE", AttackDate: "2026-01-01"}

	legacyFirst := strings.Join([]string{first.Group, first.Victim, first.Country, first.AttackDate}, "|")
	legacySecond := strings.Join([]string{second.Group, second.Victim, second.Country, second.AttackDate}, "|")
	if legacyFirst != legacySecond {
		t.Fatal("test setup must collide under the legacy separator fallback")
	}
	if firstKey, secondKey := GenerateEntryKey(first), GenerateEntryKey(second); firstKey == secondKey {
		t.Fatalf("fallback keys collide when fields contain separators: %q", firstKey)
	}
}

func TestGenerateEntryLookupKeysIncludesLegacyFallback(t *testing.T) {
	entry := RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Corp",
		Country:    "US",
		AttackDate: "2025-01-15",
		ClaimURL:   "https://example.onion/post",
	}

	keys := GenerateEntryLookupKeys(entry)

	if len(keys) != 2 {
		t.Fatalf("lookup keys length = %d, want primary and legacy", len(keys))
	}
	if keys[0] != GenerateEntryKey(entry) {
		t.Fatalf("first lookup key = %q, want primary %q", keys[0], GenerateEntryKey(entry))
	}
	if keys[1] != "LockBit|Corp|US|2025-01-15" {
		t.Fatalf("legacy lookup key = %q, want old fallback key", keys[1])
	}
}

func TestGetLatestEntriesNormalizesFieldLengths(t *testing.T) {
	longText := strings.Repeat("x", maxAPIDescriptionFieldRunes+100)
	longURL := "https://example.test/" + strings.Repeat("a", maxAPIURLFieldRunes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"victims": []map[string]any{
				{
					"id":          " victim-1 ",
					"group":       strings.Repeat("g", maxAPIIdentityFieldRunes+10),
					"victim":      strings.Repeat("v", maxAPIIdentityFieldRunes+10),
					"country":     "DE",
					"description": longText,
					"post_url":    longURL,
					"website":     longURL,
					"screenshot":  longURL,
				},
			},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
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
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ID != "victim-1" {
		t.Fatalf("ID = %q, want trimmed victim-1", entry.ID)
	}
	if len([]rune(entry.Group)) > maxAPIIdentityFieldRunes || len([]rune(entry.Victim)) > maxAPIIdentityFieldRunes {
		t.Fatalf("identity fields were not bounded: group=%d victim=%d", len([]rune(entry.Group)), len([]rune(entry.Victim)))
	}
	if len([]rune(entry.Description)) > maxAPIDescriptionFieldRunes {
		t.Fatalf("description length = %d, want <= %d", len([]rune(entry.Description)), maxAPIDescriptionFieldRunes)
	}
	if entry.ClaimURL != "" || entry.WebsiteURL != "" || entry.Screenshot != "" {
		t.Fatalf("overlong URL fields were not cleared: claim=%q url=%q screenshot=%q", entry.ClaimURL, entry.WebsiteURL, entry.Screenshot)
	}
}

func TestNewClientWithBaseURLAndPolicyUsesConfiguredHTTPPolicy(t *testing.T) {
	client, err := NewClientWithBaseURLAndPolicy("test-key", "https://api.example.test", HTTPPolicy{
		RequestTimeout: 45 * time.Second,
		MaxAttempts:    4,
		RetryBaseDelay: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithBaseURLAndPolicy() error = %v", err)
	}

	policy := client.HTTPPolicy()
	if got := client.HTTPTimeout(); got != 45*time.Second {
		t.Fatalf("HTTP timeout = %v, want 45s", got)
	}
	if policy.RequestTimeout != 45*time.Second {
		t.Fatalf("policy RequestTimeout = %v, want 45s", policy.RequestTimeout)
	}
	if policy.MaxAttempts != 4 {
		t.Fatalf("policy MaxAttempts = %d, want 4", policy.MaxAttempts)
	}
	if policy.RetryBaseDelay != 250*time.Millisecond {
		t.Fatalf("policy RetryBaseDelay = %v, want 250ms", policy.RetryBaseDelay)
	}
}

func TestNewClient_ConfiguresTransport(t *testing.T) {
	client, err := NewClient("test-key")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	if got := client.HTTPTimeout(); got != apiHTTPTimeout {
		t.Fatalf("HTTP timeout = %v, want %v", got, apiHTTPTimeout)
	}

	transport, ok := client.HTTPTransport().(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.HTTPTransport())
	}
	if transport.MaxIdleConns != apiMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, apiMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != apiMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, apiMaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != apiIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, apiIdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != apiTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, apiTLSHandshakeTimeout)
	}
	if transport.ExpectContinueTimeout != apiExpectContinueTimeout {
		t.Fatalf("ExpectContinueTimeout = %v, want %v", transport.ExpectContinueTimeout, apiExpectContinueTimeout)
	}

	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS MinVersion = %x, want TLS 1.2", tlsConfig.MinVersion)
	}
	if tlsConfig.MaxVersion != tls.VersionTLS13 {
		t.Fatalf("TLS MaxVersion = %x, want TLS 1.3", tlsConfig.MaxVersion)
	}
	if tlsConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify = true, want certificate verification enabled")
	}
}

func TestClientCloseReturnsNilWithConfiguredTransport(t *testing.T) {
	client, err := NewClient("test-key")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSharedRetryableStatusCodePolicy(t *testing.T) {
	retryable := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, code := range retryable {
		if !httpstatus.IsRetryable(code) {
			t.Errorf("expected status %d to be retryable", code)
		}
	}

	nonRetryable := []int{
		http.StatusOK,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	}

	for _, code := range nonRetryable {
		if httpstatus.IsRetryable(code) {
			t.Errorf("expected status %d to NOT be retryable", code)
		}
	}
}

type testNetError struct {
	msg     string
	timeout bool
}

func (e testNetError) Error() string   { return e.msg }
func (e testNetError) Timeout() bool   { return e.timeout }
func (e testNetError) Temporary() bool { return false }

func TestIsRetryableNetworkError(t *testing.T) {
	retryableErrors := []error{
		errors.New("service unavailable"),
		errors.New("connection reset by peer"),
		&net.DNSError{Err: "server misbehaving", Name: "api.example.test", IsTemporary: true},
		testNetError{msg: "network timeout", timeout: true},
	}

	for _, err := range retryableErrors {
		if !isRetryableNetworkError(err) {
			t.Errorf("expected %q to be retryable", err)
		}
	}

	nonRetryableErrors := []error{
		// A refused connection is permanent: retrypolicy.isTransientSyscallError
		// deliberately omits ECONNREFUSED, and the string fallback now agrees.
		errors.New("connection refused"),
		errors.New("invalid URL"),
		errors.New("malformed response"),
		nil,
	}

	for _, err := range nonRetryableErrors {
		if isRetryableNetworkError(err) {
			t.Errorf("expected %v to NOT be retryable", err)
		}
	}
}

func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		header string
		want   time.Duration
		wantOK bool
	}{
		{name: "empty", header: "", wantOK: false},
		{name: "seconds", header: "3", want: 3 * time.Second, wantOK: true},
		{name: "zero seconds", header: "0", want: 0, wantOK: true},
		{name: "seconds capped", header: "120", want: retrypolicy.MaxRetryAfterDelay, wantOK: true},
		{name: "seconds overflow", header: "9223372037", want: retrypolicy.MaxRetryAfterDelay, wantOK: true},
		{name: "http date future", header: now.Add(5 * time.Second).UTC().Format(http.TimeFormat), want: 5 * time.Second, wantOK: true},
		{name: "http date past", header: now.Add(-5 * time.Second).UTC().Format(http.TimeFormat), want: 0, wantOK: true},
		{name: "invalid", header: "soon", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RetryAfterDelay(tt.header, now)
			if ok != tt.wantOK {
				t.Fatalf("RetryAfterDelay() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("RetryAfterDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}

type repeatingByteReader byte

func (r repeatingByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

type countingInfiniteBody struct {
	read   int64
	closed bool
}

func (b *countingInfiniteBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += int64(len(p))
	return len(p), nil
}

func (b *countingInfiniteBody) Close() error {
	b.closed = true
	return nil
}

func TestDecodeBoundedAPIResponseRejectsOversizedTrailingData(t *testing.T) {
	reader := io.MultiReader(
		strings.NewReader(`{"client":"test","count":0,"order":"desc","victims":[]}`),
		io.LimitReader(repeatingByteReader(' '), maxAPIResponseBytes+1),
	)

	var response ransomwareResponse
	err := decodeBoundedAPIResponse(reader, &response)
	if err == nil {
		t.Fatal("expected oversized response error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want byte-limit context", err)
	}
}

func TestDecodeBoundedAPIResponseRejectsTrailingJSONData(t *testing.T) {
	reader := strings.NewReader(`{"client":"test","count":0,"order":"desc","victims":[]} {"extra":true}`)

	var response ransomwareResponse
	err := decodeBoundedAPIResponse(reader, &response)
	if err == nil {
		t.Fatal("expected trailing JSON error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("error = %v, want trailing JSON context", err)
	}
}

func TestDrainAndCloseAPIResponseStopsAtByteLimit(t *testing.T) {
	body := &countingInfiniteBody{}

	drainAndCloseAPIResponse(body)

	if body.read > maxAPIResponseBytes {
		t.Fatalf("drained %d bytes, want at most %d", body.read, maxAPIResponseBytes)
	}
	if !body.closed {
		t.Fatal("body was not closed")
	}
}

// --- HTTP request flow tests ---

func TestGetJSON_Success(t *testing.T) {
	var requestCount atomic.Int32

	// Server returns 200 with a valid JSON response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{"id":"abc-123","group":"LockBit","victim":"TestCorp","country":"US"}]}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}

	if len(response.Victims) != 1 {
		t.Fatalf("expected 1 victim, got %d", len(response.Victims))
	}
	if response.Victims[0].ID != "abc-123" {
		t.Errorf("expected victim ID %q, got %q", "abc-123", response.Victims[0].ID)
	}
	if response.Victims[0].Group != "LockBit" {
		t.Errorf("expected group %q, got %q", "LockBit", response.Victims[0].Group)
	}
	if response.Victims[0].Victim != "TestCorp" {
		t.Errorf("expected victim %q, got %q", "TestCorp", response.Victims[0].Victim)
	}
	if response.Victims[0].Country != "US" {
		t.Errorf("expected country %q, got %q", "US", response.Victims[0].Country)
	}
}

func TestGetJSON_InvalidJSON(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{not-json`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err == nil {
		t.Fatal("expected invalid JSON decode error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode response") {
		t.Fatalf("error = %v, want decode context", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestGetJSON_MalformedURL(t *testing.T) {
	client := newTestHTTPClient(t, "", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), "http://example.test/%zz", &response)
	if err == nil {
		t.Fatal("expected malformed URL error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create request") {
		t.Fatalf("error = %v, want request construction context", err)
	}
}

func TestGetJSON_NoAPIKeyHeader(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if got := r.Header.Get(apiKeyHeader); got != "" {
			t.Errorf("%s header = %q, want empty", apiKeyHeader, got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	if err := client.GetJSON(context.Background(), server.URL, &response); err != nil {
		t.Fatalf("GetJSON() error = %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestGetJSON_RetryOn429(t *testing.T) {
	disableAPIRetrySleep(t)
	var requestCount atomic.Int32

	// Server returns 429 twice then 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			writeAPITestResponse(t, w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}

	finalCount := requestCount.Load()
	if finalCount != 3 {
		t.Errorf("expected 3 requests (2 retries + 1 success), got %d", finalCount)
	}
}

func TestGetJSON_RetryOn429HonorsRetryAfterDate(t *testing.T) {
	var requestCount atomic.Int32
	pastRetryAfter := time.Now().Add(-5 * time.Second).UTC().Format(http.TimeFormat)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			w.Header().Set("Retry-After", pastRetryAfter)
			w.WriteHeader(http.StatusTooManyRequests)
			writeAPITestResponse(t, w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer server.Close()

	// A real retry base delay, so the "immediate retry" assertion below actually
	// bites: without the Retry-After override this client would wait ~1 s before
	// the second attempt. newTestHTTPClient passes HTTPPolicy{}, whose zero
	// RetryBaseDelay survives withDefaults (it only replaces negatives), and with
	// it the elapsed-time assertion would pass no matter what the header did.
	client, err := NewClientWithHTTPClient(
		"test-key", server.URL, &http.Client{Timeout: 5 * time.Second},
		HTTPPolicy{RetryBaseDelay: time.Second},
	)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	start := time.Now()
	var response ransomwareResponse
	if err := client.GetJSON(context.Background(), server.URL, &response); err != nil {
		t.Fatalf("expected success after Retry-After retry, got: %v", err)
	}

	if got := requestCount.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	// A Retry-After date already in the past parses to 0, which is floored to
	// the policy base delay -- the same floor the Discord sender got -- so it
	// no longer retries faster than a header-less 429.
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("Retry-After:0/past-date retry took %v, want at least ~%v (the floored base delay, not an instant retry)", elapsed, 500*time.Millisecond)
	}
}

// TestGetJSON_RetryAfterZeroFallsBackToBaseDelay is the fast, sleep-free
// counterpart of TestGetJSON_RetryOn429HonorsRetryAfterDate above: it asserts
// the exact computed delay (via recordAPIRetrySleep) instead of wall-clock
// elapsed time, and additionally asserts that a Retry-After:0 429 arms no
// cooldown -- matching a header-less 429, not a positive-delay one.
func TestGetJSON_RetryAfterZeroFallsBackToBaseDelay(t *testing.T) {
	recorded := recordAPIRetrySleep(t)
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			writeAPITestResponse(t, w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer server.Close()

	client, err := NewClientWithHTTPClient(
		"test-key", server.URL, &http.Client{Timeout: 5 * time.Second},
		HTTPPolicy{RetryBaseDelay: time.Second},
	)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	var response ransomwareResponse
	if err := client.GetJSON(context.Background(), server.URL, &response); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}

	delays := recorded()
	if len(delays) != 1 {
		t.Fatalf("retry sleep count = %d (%v), want 1", len(delays), delays)
	}
	if delays[0] < 500*time.Millisecond {
		t.Fatalf("retry delay = %v, want at least ~%v (the floored base delay, not the unfloored zero)", delays[0], 500*time.Millisecond)
	}

	var cooldownErr *RateLimitCooldownError
	if err := client.rateLimitCooldownError(time.Now()); errors.As(err, &cooldownErr) {
		t.Fatalf("rateLimitCooldownError() = %v, want no cooldown armed for a zero-delay header", err)
	}
}

func TestGetJSON_CachesRetryAfterCooldown(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		writeAPITestResponse(t, w, `{"error":"rate limited"}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var response ransomwareResponse
	err := client.GetJSON(ctx, server.URL, &response)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first GetJSON() error = %v, want context deadline after Retry-After wait", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count after first call = %d, want 1", got)
	}

	err = client.GetJSON(context.Background(), server.URL, &response)
	var cooldownErr *RateLimitCooldownError
	if !errors.As(err, &cooldownErr) {
		t.Fatalf("second GetJSON() error = %T %v, want RateLimitCooldownError", err, err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count after cooldown call = %d, want no additional request", got)
	}
}

// recordAPIRetrySleep replaces the retry sleep with a recorder so a 429 loop
// runs at full speed while the requested delays stay observable.
func recordAPIRetrySleep(t *testing.T) func() []time.Duration {
	t.Helper()

	var mu sync.Mutex
	var recorded []time.Duration

	previous := apiRetrySleep
	apiRetrySleep = func(ctx context.Context, delay time.Duration) error {
		mu.Lock()
		recorded = append(recorded, delay)
		mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t.Cleanup(func() { apiRetrySleep = previous })

	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), recorded...)
	}
}

func TestMakeRequestCapsOverflowRetryAfterAndArmsCooldown(t *testing.T) {
	recorded := recordAPIRetrySleep(t)
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Retry-After", "9223372037")
		w.WriteHeader(http.StatusTooManyRequests)
		writeAPITestResponse(t, w, `{"error":"rate limited"}`)
	}))
	defer server.Close()

	client := newTestHTTPClientWithBaseURL(t, "test-key", server.URL, &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	if err := client.GetJSON(context.Background(), server.URL, &response); err == nil {
		t.Fatal("GetJSON() error = nil, want failure after all attempts")
	}

	if got := requestCount.Load(); got != int32(retrypolicy.MaxAttempts) {
		t.Fatalf("request count = %d, want %d", got, retrypolicy.MaxAttempts)
	}

	delays := recorded()
	if len(delays) != retrypolicy.MaxAttempts-1 {
		t.Fatalf("retry sleep count = %d (%v), want %d", len(delays), delays, retrypolicy.MaxAttempts-1)
	}
	for _, delay := range delays {
		if delay != retrypolicy.MaxRetryAfterDelay {
			t.Fatalf("retry sleep delay = %v, want %v", delay, retrypolicy.MaxRetryAfterDelay)
		}
	}

	var cooldownErr *RateLimitCooldownError
	if err := client.rateLimitCooldownError(time.Now()); !errors.As(err, &cooldownErr) {
		t.Fatalf("rateLimitCooldownError() = %T %v, want RateLimitCooldownError", err, err)
	}
}

func TestGetJSON_RetryOn500(t *testing.T) {
	disableAPIRetrySleep(t)
	var requestCount atomic.Int32

	// Server always returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		writeAPITestResponse(t, w, `{"error":"internal server error"}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}

	finalCount := requestCount.Load()
	if finalCount != int32(retrypolicy.MaxAttempts) {
		t.Errorf("expected %d requests, got %d", retrypolicy.MaxAttempts, finalCount)
	}
}

type failingRoundTripper struct {
	err   error
	count atomic.Int32
}

func (rt *failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.count.Add(1)
	return nil, rt.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGetJSON_RetryExhaustedNetworkErrorOmitsStatusZero(t *testing.T) {
	disableAPIRetrySleep(t)
	transport := &failingRoundTripper{
		err: &net.DNSError{Err: "server misbehaving", Name: "api.example.test", IsTemporary: true},
	}
	client := newTestHTTPClient(t, "test-key", &http.Client{Transport: transport})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), "https://api.example.test/victims/recent", &response)
	if err == nil {
		t.Fatal("expected retry-exhausted network error, got nil")
	}
	if got := transport.count.Load(); got != int32(retrypolicy.MaxAttempts) {
		t.Fatalf("request count = %d, want %d", got, retrypolicy.MaxAttempts)
	}
	if strings.Contains(err.Error(), "last status: 0") {
		t.Fatalf("network retry error included meaningless status zero: %v", err)
	}
}

func TestGetJSON_RetryExhaustedNetworkErrorLogsOnlyActualRetries(t *testing.T) {
	disableAPIRetrySleep(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	transport := &failingRoundTripper{
		err: &net.DNSError{Err: "server misbehaving", Name: "api.example.test", IsTemporary: true},
	}
	client := newTestHTTPClient(t, "test-key", &http.Client{Transport: transport})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), "https://api.example.test/victims/recent", &response)
	if err == nil {
		t.Fatal("expected retry-exhausted network error, got nil")
	}

	retryLogs := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message != "API request failed, retrying..." {
			continue
		}
		retryLogs++
		if got := entry.Data["attempt"]; got == retrypolicy.MaxAttempts {
			t.Fatalf("logged retrying on final attempt %v", got)
		}
	}
	if retryLogs != retrypolicy.MaxAttempts-1 {
		t.Fatalf("retrying log count = %d, want %d", retryLogs, retrypolicy.MaxAttempts-1)
	}
}

func TestGetJSON_RetryExhaustedStatusErrorLogsOnlyActualRetries(t *testing.T) {
	disableAPIRetrySleep(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeAPITestResponse(t, w, `{"error":"internal server error"}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err == nil {
		t.Fatal("expected retry-exhausted status error, got nil")
	}

	retryLogs := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message != "API request failed with retryable status, retrying..." {
			continue
		}
		retryLogs++
		if got := entry.Data["attempt"]; got == retrypolicy.MaxAttempts {
			t.Fatalf("logged retrying on final attempt %v", got)
		}
	}
	if retryLogs != retrypolicy.MaxAttempts-1 {
		t.Fatalf("retrying status log count = %d, want %d", retryLogs, retrypolicy.MaxAttempts-1)
	}
}

func disableAPIRetrySleep(t *testing.T) {
	t.Helper()
	previous := apiRetrySleep
	apiRetrySleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t.Cleanup(func() { apiRetrySleep = previous })
}

func TestGetJSON_Non200NonRetryable(t *testing.T) {
	var requestCount atomic.Int32

	// Server returns 403 Forbidden (non-retryable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusForbidden)
		writeAPITestResponse(t, w, `{"error":"forbidden"}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}

	// Should not retry on non-retryable status codes
	finalCount := requestCount.Load()
	if finalCount != 1 {
		t.Errorf("expected exactly 1 request (no retries for 403), got %d", finalCount)
	}
}

func TestGetJSON_Non200BodyIsSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeAPITestResponse(t, w, "line one\nline two\tsecret https://hooks.slack.com/services/T123/B123/secret-token")
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T, want HTTPStatusError", err)
	}
	if strings.ContainsAny(statusErr.Body, "\r\n\t") {
		t.Fatalf("HTTPStatusError body contains control whitespace: %q", statusErr.Body)
	}
	if strings.Contains(statusErr.Body, "secret-token") {
		t.Fatalf("HTTPStatusError body leaked webhook token: %q", statusErr.Body)
	}
	for _, want := range []string{"line one", "line two"} {
		if !strings.Contains(statusErr.Body, want) {
			t.Fatalf("HTTPStatusError body = %q, want %q", statusErr.Body, want)
		}
	}
}

func TestDisplayRansomwareTitleFillsMissingIdentity(t *testing.T) {
	tests := []struct {
		name  string
		entry RansomwareEntry
		want  string
	}{
		{
			name:  "complete",
			entry: RansomwareEntry{Group: "LockBit", Victim: "Example Corp"},
			want:  "LockBit -> Example Corp",
		},
		{
			name:  "missing group",
			entry: RansomwareEntry{Victim: "Example Corp"},
			want:  "Unknown group -> Example Corp",
		},
		{
			name:  "missing victim",
			entry: RansomwareEntry{Group: "LockBit"},
			want:  "LockBit -> Unknown victim",
		},
		{
			name:  "both missing",
			entry: RansomwareEntry{},
			want:  "Unknown group -> Unknown victim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayRansomwareTitle(tt.entry); got != tt.want {
				t.Fatalf("DisplayRansomwareTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOperatorErrorMessageOmitsRawHTTPBody(t *testing.T) {
	err := &HTTPStatusError{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Body:       `<html>provider body with token-like detail</html>`,
	}

	got := OperatorErrorMessage(err)

	if strings.Contains(got, "provider body") || strings.Contains(got, "<html>") {
		t.Fatalf("OperatorErrorMessage() leaked raw body: %q", got)
	}
	if !strings.Contains(got, "API authentication failed") {
		t.Fatalf("OperatorErrorMessage() = %q, want authentication guidance", got)
	}
}

func TestGetJSON_ContextCancel(t *testing.T) {
	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	// Create an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var response ransomwareResponse
	err := client.GetJSON(ctx, "http://localhost:0/should-not-reach", &response)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestGetJSON_APIKeyHeader(t *testing.T) {
	const testAPIKey = "my-secret-api-key-12345" //nolint:gosec // G101: test fixture, not a real credential

	// Server captures and verifies the request headers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey := r.Header.Get("X-API-KEY")
		if gotKey != testAPIKey {
			t.Errorf("expected X-API-KEY header %q, got %q", testAPIKey, gotKey)
		}

		gotUA := r.Header.Get("User-Agent")
		if gotUA != "Wget/1.21.3" {
			t.Errorf("expected User-Agent %q, got %q", "Wget/1.21.3", gotUA)
		}

		gotAccept := r.Header.Get("Accept")
		if gotAccept != "application/json" {
			t.Errorf("expected Accept %q, got %q", "application/json", gotAccept)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, testAPIKey, &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientBlocksCredentialRedirectDowngrade(t *testing.T) {
	const testAPIKey = "redirect-secret"
	var downgradedRequests atomic.Int32

	plaintextServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downgradedRequests.Add(1)
		if got := r.Header.Get("X-API-KEY"); got != "" {
			t.Errorf("downgraded redirect received API key %q", got)
		}
		writeAPITestResponse(t, w, `{"client":"test","count":0,"order":"desc","victims":[]}`)
	}))
	defer plaintextServer.Close()

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plaintextServer.URL+"/victims/recent", http.StatusFound)
	}))
	defer tlsServer.Close()

	client := newTestHTTPClientWithBaseURL(t, testAPIKey, tlsServer.URL, tlsServer.Client())

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), tlsServer.URL+"/victims/recent", &response)
	if err == nil {
		t.Fatal("expected HTTPS-to-HTTP redirect to be blocked")
	}
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("redirect error = %v, want redirect blocked context", err)
	}
	if got := downgradedRequests.Load(); got != 0 {
		t.Fatalf("plaintext redirect target received %d requests, want 0", got)
	}
}

func TestAPIClientBlocksCredentialRedirectToDifferentHTTPSHost(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.test/victims/recent", http.StatusFound)
	}))
	defer tlsServer.Close()

	client := newTestHTTPClientWithBaseURL(t, "redirect-secret", tlsServer.URL, tlsServer.Client())

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), tlsServer.URL+"/victims/recent", &response)
	if err == nil {
		t.Fatal("expected cross-host redirect to be blocked")
	}
	if !strings.Contains(err.Error(), "host changed") {
		t.Fatalf("redirect error = %v, want host changed context", err)
	}
}

func TestAPIClientAllowsSameHostHTTPSRedirect(t *testing.T) {
	const testAPIKey = "redirect-secret"
	var finalRequests atomic.Int32

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/victims/recent", http.StatusFound)
		case "/victims/recent":
			finalRequests.Add(1)
			if got := r.Header.Get("X-API-KEY"); got != testAPIKey {
				t.Errorf("X-API-KEY header = %q, want %q", got, testAPIKey)
			}
			writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{"id":"abc-123","group":"LockBit","victim":"TestCorp"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()

	client := newTestHTTPClientWithBaseURL(t, testAPIKey, tlsServer.URL, tlsServer.Client())

	var response ransomwareResponse
	if err := client.GetJSON(context.Background(), tlsServer.URL+"/redirect", &response); err != nil {
		t.Fatalf("same-host HTTPS redirect failed: %v", err)
	}
	if got := finalRequests.Load(); got != 1 {
		t.Fatalf("final request count = %d, want 1", got)
	}
	if len(response.Victims) != 1 || response.Victims[0].ID != "abc-123" {
		t.Fatalf("response victims = %+v, want redirected response", response.Victims)
	}
}

func TestGetLatestEntriesUsesConfiguredBaseURL(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.URL.Path != "/victims/recent" {
			t.Fatalf("request path = %q, want /victims/recent", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{"id":"abc-123","group":"LockBit","victim":"TestCorp"}]}`)
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
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if len(entries) != 1 || entries[0].ID != "abc-123" {
		t.Fatalf("entries = %+v, want one configured-base-url entry", entries)
	}
}

func TestGetLatestEntries_UsesRecentVictimsEndpoint(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestHTTPClient(t, "test-key", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount.Add(1)
		if req.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", req.Method)
		}
		if got, want := req.URL.String(), ransomwareLiveAPI+"/victims/recent"; got != want {
			t.Errorf("request URL = %q, want %q", got, want)
		}
		if got := req.Header.Get(apiKeyHeader); got != "test-key" {
			t.Errorf("%s header = %q, want configured API key", apiKeyHeader, got)
		}
		body := `{"client":"test","count":1,"order":"desc","victims":[{"id":"abc-123","group":"LockBit","victim":"TestCorp","country":"US"}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})})

	entries, err := client.GetLatestEntries(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if len(entries) != 1 {
		t.Fatalf("entries length = %d, want 1", len(entries))
	}
	if entries[0].ID != "abc-123" || entries[0].Group != "LockBit" || entries[0].Victim != "TestCorp" {
		t.Fatalf("entry = %+v, want decoded victim from public wrapper", entries[0])
	}
}

func TestGetLatestEntries_PropagatesError(t *testing.T) {
	client := newTestHTTPClient(t, "test-key", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"forbidden"}`)),
			Request:    req,
		}, nil
	})})

	entries, err := client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatalf("GetLatestEntries() returned entries %+v, want error", entries)
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T, want HTTPStatusError", err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", statusErr.StatusCode, http.StatusForbidden)
	}
}

func TestGetLatestEntriesRejectsMissingVictimsField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc"}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatalf("GetLatestEntries() returned entries %+v, want missing victims error", entries)
	}
	if !strings.Contains(err.Error(), "victims") {
		t.Fatalf("GetLatestEntries() error = %v, want victims field error", err)
	}
}

func TestGetLatestEntriesSkipsMalformedEntriesBeforeDedupe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":2,"order":"desc","victims":[{},{"id":"abc-123","group":"LockBit","victim":"TestCorp"}]}`)
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
	if len(entries) != 1 || entries[0].ID != "abc-123" {
		t.Fatalf("entries = %+v, want only valid victim", entries)
	}
}

func TestGetLatestEntriesSkipsEntryWithMalformedTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":2,"order":"desc","victims":[{"id":"bad-time","group":"LockBit","victim":"BadTimeCorp","discovered":"not-a-time"},{"id":"abc-123","group":"LockBit","victim":"TestCorp","discovered":"2026-06-27 12:00:00"}]}`)
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
	if len(entries) != 1 || entries[0].ID != "abc-123" {
		t.Fatalf("entries = %+v, want only valid timestamp victim", entries)
	}
}

func TestGetLatestEntriesRejectsAllMalformedEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, `{"client":"test","count":1,"order":"desc","victims":[{}]}`)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	entries, err := client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatalf("GetLatestEntries() returned entries %+v, want malformed entry error", entries)
	}
	if !strings.Contains(err.Error(), "valid") {
		t.Fatalf("GetLatestEntries() error = %v, want valid victims error", err)
	}
}

func TestGetLatestEntriesCapsVictimsPerResponse(t *testing.T) {
	var victims strings.Builder
	for i := 0; i < maxAPIVictimsPerResponse+2; i++ {
		if i > 0 {
			victims.WriteByte(',')
		}
		fmt.Fprintf(&victims, `{"id":"victim-%04d","country":"US"}`, i)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeAPITestResponse(t, w, fmt.Sprintf(`{"client":"test","count":%d,"order":"desc","victims":[%s]}`, maxAPIVictimsPerResponse+2, victims.String()))
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
	if len(entries) != maxAPIVictimsPerResponse {
		t.Fatalf("entries length = %d, want cap %d", len(entries), maxAPIVictimsPerResponse)
	}
	if entries[0].ID != "victim-0000" {
		t.Fatalf("first entry ID = %q, want victim-0000", entries[0].ID)
	}
	wantLastID := fmt.Sprintf("victim-%04d", maxAPIVictimsPerResponse-1)
	if entries[len(entries)-1].ID != wantLastID {
		t.Fatalf("last entry ID = %q, want %q", entries[len(entries)-1].ID, wantLastID)
	}
}

//nolint:gocyclo // long sequential integration-style test asserting many response fields; not a table split candidate
func TestGetJSON_DecodesRansomwareWireResponse(t *testing.T) {
	// Full ransomwareResponse JSON with multiple entries and all fields
	responseJSON := `{
		"client": "api-pro",
		"count": 2,
		"order": "desc",
		"victims": [
			{
				"id": "entry-001",
				"group": "LockBit",
				"victim": "AcmeCorp",
				"country": "US",
				"activity": "data leak",
				"attackdate": "2025-01-15",
				"discovered": "2025-01-16 12:30:00.123456",
				"post_url": "https://example.onion/post1",
				"website": "https://acme.example.com",
				"description": "Data breach affecting customer records",
				"screenshot": "https://screenshots.example.com/img1.png",
				"published": "2025-01-17 08:00:00"
			},
			{
				"id": "entry-002",
				"group": "BlackCat",
				"victim": "GlobalTech",
				"country": "DE",
				"activity": "encryption",
				"attackdate": "2025-02-01",
				"discovered": "2025-02-02 14:00:00",
				"post_url": "",
				"website": "https://globaltech.example.de",
				"description": "",
				"screenshot": "",
				"published": "2025-02-03 10:15:30.654321"
			}
		]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeAPITestResponse(t, w, responseJSON)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, "test-key", &http.Client{Timeout: 5 * time.Second})

	var response ransomwareResponse
	err := client.GetJSON(context.Background(), server.URL, &response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(response.Victims) != 2 {
		t.Fatalf("expected 2 victims, got %d", len(response.Victims))
	}

	// Verify first entry
	v0 := response.Victims[0]
	if v0.ID != "entry-001" {
		t.Errorf("Victim[0].ID = %q, want %q", v0.ID, "entry-001")
	}
	if v0.Group != "LockBit" {
		t.Errorf("Victim[0].Group = %q, want %q", v0.Group, "LockBit")
	}
	if v0.Victim != "AcmeCorp" {
		t.Errorf("Victim[0].Victim = %q, want %q", v0.Victim, "AcmeCorp")
	}
	if v0.Country != "US" {
		t.Errorf("Victim[0].Country = %q, want %q", v0.Country, "US")
	}
	if v0.Activity != "data leak" {
		t.Errorf("Victim[0].Activity = %q, want %q", v0.Activity, "data leak")
	}
	if v0.AttackDate != "2025-01-15" {
		t.Errorf("Victim[0].AttackDate = %q, want %q", v0.AttackDate, "2025-01-15")
	}
	if v0.ClaimURL != "https://example.onion/post1" {
		t.Errorf("Victim[0].ClaimURL = %q, want %q", v0.ClaimURL, "https://example.onion/post1")
	}
	if v0.URL != "https://acme.example.com" {
		t.Errorf("Victim[0].URL = %q, want %q", v0.URL, "https://acme.example.com")
	}
	if v0.Description != "Data breach affecting customer records" {
		t.Errorf("Victim[0].Description = %q, want %q", v0.Description, "Data breach affecting customer records")
	}
	// Verify wire timestamp fields parsed correctly
	if v0.Discovered.Year() != 2025 || v0.Discovered.Month() != 1 || v0.Discovered.Day() != 16 {
		t.Errorf("Victim[0].Discovered date wrong: %v", v0.Discovered)
	}
	if v0.Published.Year() != 2025 || v0.Published.Month() != 1 || v0.Published.Day() != 17 {
		t.Errorf("Victim[0].Published date wrong: %v", v0.Published)
	}

	// Verify second entry
	v1 := response.Victims[1]
	if v1.ID != "entry-002" {
		t.Errorf("Victim[1].ID = %q, want %q", v1.ID, "entry-002")
	}
	if v1.Group != "BlackCat" {
		t.Errorf("Victim[1].Group = %q, want %q", v1.Group, "BlackCat")
	}
	if v1.Country != "DE" {
		t.Errorf("Victim[1].Country = %q, want %q", v1.Country, "DE")
	}
}
