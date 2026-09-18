// Client supports:
// - Hardened TLS 1.2/1.3 configuration with secure cipher suites
// - API key authentication via X-API-KEY header
// - Request timeouts and connection pooling
// - JSON response parsing with memory limits
// - Comprehensive request logging
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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

const (
	maxAPIResponseBytes      = 10 << 20
	apiErrorBodyPreviewBytes = 1024

	apiHTTPTimeout            = 30 * time.Second
	apiMaxIdleConns           = 10
	apiMaxIdleConnsPerHost    = 5
	apiIdleConnTimeout        = 90 * time.Second
	apiTLSHandshakeTimeout    = 10 * time.Second
	apiExpectContinueTimeout  = time.Second
	apiMaxCredentialRedirects = 10

	apiKeyHeader    = "X-API-KEY"
	apiUserAgent    = "Wget/1.21.3"
	acceptHeader    = "Accept"
	userAgentHeader = "User-Agent"
	jsonContentType = "application/json"
)

var apiRetrySleep = sleepWithContext

// HTTPStatusError represents a non-success response from the ransomware API.
type HTTPStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.Body == "" {
		return fmt.Sprintf("API returned status %d: %s", e.StatusCode, e.Status)
	}
	return fmt.Sprintf("API returned status %d: %s - %s", e.StatusCode, e.Status, e.Body)
}

type RateLimitCooldownError struct {
	Until time.Time
}

func (e *RateLimitCooldownError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("API rate limited until %s", e.Until.UTC().Format(time.RFC3339))
}

// OperatorErrorMessage returns a concise status message suitable for persisted
// operator-facing state. It intentionally omits raw provider response bodies.
func OperatorErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "API request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "API request timed out; provider did not respond before the polling deadline"
	}

	var cooldownErr *RateLimitCooldownError
	if errors.As(err, &cooldownErr) {
		return "API rate limited; retry after " + cooldownErr.Until.UTC().Format(time.RFC3339)
	}

	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusBadRequest:
			return "API request rejected by provider; check API configuration"
		case http.StatusUnauthorized, http.StatusForbidden:
			return "API authentication failed; check api_key and provider access"
		case http.StatusNotFound:
			return "API endpoint not found; check provider API compatibility"
		case http.StatusTooManyRequests:
			return "API rate limited; retry on next poll"
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusInternalServerError:
			return "API provider unavailable; retry on next poll"
		default:
			if statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 {
				return "API request rejected by provider; check API configuration"
			}
			if statusErr.StatusCode >= 500 {
				return "API provider unavailable; retry on next poll"
			}
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "API request timed out; check network connectivity and provider availability"
	}
	if strings.Contains(err.Error(), "failed to decode response") {
		return "API response could not be decoded; provider response format may have changed"
	}
	return "API request failed; check network connectivity and provider availability"
}

// Client represents the HTTP client for external APIs
type Client struct {
	httpClient        *http.Client
	apiKey            string
	baseURL           string
	maxAttempts       int
	retryBaseDelay    time.Duration
	rateLimitMu       sync.Mutex
	rateLimitCooldown time.Time
}

type HTTPPolicy struct {
	RequestTimeout time.Duration
	MaxAttempts    int
	RetryBaseDelay time.Duration
}

func DefaultHTTPPolicy() HTTPPolicy {
	return HTTPPolicy{
		RequestTimeout: apiHTTPTimeout,
		MaxAttempts:    retrypolicy.MaxAttempts,
		RetryBaseDelay: retrypolicy.BaseDelay,
	}
}

func (p HTTPPolicy) withDefaults() HTTPPolicy {
	defaults := DefaultHTTPPolicy()
	if p.RequestTimeout <= 0 {
		p.RequestTimeout = defaults.RequestTimeout
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaults.MaxAttempts
	}
	if p.RetryBaseDelay < 0 {
		p.RetryBaseDelay = defaults.RetryBaseDelay
	}
	return p
}

func (c *Client) httpPolicy() HTTPPolicy {
	return HTTPPolicy{
		RequestTimeout: c.httpClient.Timeout,
		MaxAttempts:    c.maxAttempts,
		RetryBaseDelay: c.retryBaseDelay,
	}.withDefaults()
}

// HTTPPolicy returns the effective HTTP request and retry policy for the client.
func (c *Client) HTTPPolicy() HTTPPolicy {
	return c.httpPolicy()
}

// HTTPTimeout returns the configured HTTP request timeout.
func (c *Client) HTTPTimeout() time.Duration {
	return c.HTTPPolicy().RequestTimeout
}

// HTTPTransport returns the configured HTTP round tripper.
func (c *Client) HTTPTransport() http.RoundTripper {
	return c.httpClient.Transport
}

// NewClient creates a new API client instance with hardened TLS configuration
//
// Security features:
// - Enforces TLS 1.2+ with secure cipher suites (AEAD preferred)
// - Certificate verification always enabled
// - Connection pooling with limits to prevent resource exhaustion
// - Session resumption for performance
//
// The client is configured for production use with 30-second timeouts
// and appropriate connection limits for concurrent requests.
func NewClient(apiKey string) (*Client, error) {
	return NewClientWithBaseURL(apiKey, ransomwareLiveAPI)
}

// NewClientWithBaseURL creates a new API client for a caller-selected API base URL.
func NewClientWithBaseURL(apiKey, baseURL string) (*Client, error) {
	return NewClientWithBaseURLAndPolicy(apiKey, baseURL, DefaultHTTPPolicy())
}

func NewClientWithBaseURLAndPolicy(apiKey, baseURL string, policy HTTPPolicy) (*Client, error) {
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	policy = policy.withDefaults()

	// Create hardened TLS configuration
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12, // Minimum TLS 1.2
		MaxVersion: tls.VersionTLS13, // Prefer TLS 1.3

		// Secure cipher suites for TLS 1.2 (TLS 1.3 manages its own)
		CipherSuites: []uint16{
			// CHACHA20-POLY1305 (AEAD, performant without AES-NI)
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			// AES-GCM (AEAD, hardware-accelerated)
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},

		// Security hardening
		InsecureSkipVerify: false,                           // Always verify certificates
		ServerName:         "",                              // Let Go handle SNI automatically
		ClientSessionCache: tls.NewLRUClientSessionCache(0), // Session resumption
	}

	// Create HTTP transport with hardened TLS
	//
	// Security rationale:
	// - TLS 1.2+ encryption protects API keys and sensitive data in transit
	// - Secure cipher suites prevent downgrade attacks
	// - Certificate verification prevents man-in-the-middle attacks
	// - Essential for secure API key transmission to external services
	//
	// Performance optimizations:
	// - Keep-alive connections enabled for efficiency
	// - Connection pooling: max 10 idle, 5 per host
	// - Reasonable timeouts for production environments
	// - Session resumption reduces TLS handshake overhead
	transport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,  // Enable HTTP/2 for custom transports
		DisableCompression:    false, // Keep compression for performance
		DisableKeepAlives:     false, // Keep alive for performance
		MaxIdleConns:          apiMaxIdleConns,
		MaxIdleConnsPerHost:   apiMaxIdleConnsPerHost,
		IdleConnTimeout:       apiIdleConnTimeout,
		TLSHandshakeTimeout:   apiTLSHandshakeTimeout,
		ExpectContinueTimeout: apiExpectContinueTimeout,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:       policy.RequestTimeout,
			Transport:     transport,
			CheckRedirect: apiRedirectPolicy(normalizedBaseURL),
		},
		apiKey:         apiKey,
		baseURL:        normalizedBaseURL,
		maxAttempts:    policy.MaxAttempts,
		retryBaseDelay: policy.RetryBaseDelay,
	}, nil
}

// NewClientWithHTTPClient creates an API client around a caller-provided HTTP
// client. It is intended for integration tests and controlled embeddings that
// need the public retry/authentication behavior with custom transports.
func NewClientWithHTTPClient(apiKey, baseURL string, httpClient *http.Client, policy HTTPPolicy) (*Client, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("http client is nil")
	}
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	policy = policy.withDefaults()

	client := *httpClient
	if client.Timeout <= 0 {
		client.Timeout = policy.RequestTimeout
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = apiRedirectPolicy(normalizedBaseURL)
	}

	return &Client{
		httpClient:     &client,
		apiKey:         apiKey,
		baseURL:        normalizedBaseURL,
		maxAttempts:    policy.MaxAttempts,
		retryBaseDelay: policy.RetryBaseDelay,
	}, nil
}

func apiRedirectPolicy(baseURL string) func(*http.Request, []*http.Request) error {
	base, _ := url.Parse(baseURL)
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= apiMaxCredentialRedirects {
			return fmt.Errorf("API redirect blocked after %d hops", len(via))
		}
		if req == nil || req.URL == nil {
			return fmt.Errorf("API redirect blocked: missing redirect URL")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("API redirect blocked: refusing credential-bearing redirect to %q", req.URL.Scheme)
		}
		if !sameAPIHost(req.URL, base) {
			return fmt.Errorf("API redirect blocked: host changed from %q to %q", base.Host, req.URL.Host)
		}
		return nil
	}
}

func sameAPIHost(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && normalizedURLPort(a) == normalizedURLPort(b)
}

func normalizedURLPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// NormalizeBaseURL validates and canonicalizes a caller-supplied API base
// URL.
//
// F2, P2 (found 2026-09-04): a url.Parse failure here used to be wrapped with
// %w and returned as-is. This function has three callers -- NewClientWithBase
// URLAndPolicy and NewClientWithHTTPClient in this package (both of which
// simply `return nil, err`, unwrapped, on failure) and
// internal/config's validateAPIConfig, which wraps it once more with %w and
// nothing else in the chain redacts. Reproduced end to end with a real
// binary: an api_base_url containing userinfo credentials put the FULL raw
// URL -- not just a prefix, unlike the feed-URL case -- on --check-config's
// stdout, in --dry-run's JSON log line (main.go:122), and in bot.log on a
// failed hot reload (scheduler.go's "Config reload failed, keeping current
// config"). Fixed here, at the source, rather than at each of the three call
// sites: the redaction now protects every current AND future caller, mirrors
// how validateFeedURL/canonicalFeedURL already redact feedurl.Parse's raw
// error for the same reason, and needs no import config does not already
// have (this package already imports internal/textutil for
// RedactWebhookErrorForURL elsewhere). RedactWebhookErrorForURL is generic
// text/URL redaction with no webhook-specific assumption (see its doc
// comment; internal/config uses it the same way for the API-URL case) and
// produces a correct, non-webhook-shaped marker for a plain host like
// api.example.com.
func NormalizeBaseURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("api base URL is empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid api base URL: %w", textutil.RedactWebhookErrorForURL(err, rawURL))
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("api base URL must include a host")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("api base URL scheme must be http or https")
	}
	return strings.TrimRight(rawURL, "/"), nil
}

// GetJSON performs an authenticated GET request and decodes the JSON response
// into target. It is the public low-level request seam used by integration
// tests and advanced callers that need client retry behavior without a
// ransomware.live endpoint wrapper.
func (c *Client) GetJSON(ctx context.Context, requestURL string, target interface{}) error {
	return c.makeRequest(ctx, requestURL, target)
}

// makeRequest performs requests to caller-provided external API URLs with retry logic and rate-limit handling.
//
// Security measures:
// - Uses the client's TLS 1.2+ transport settings when the caller supplies an HTTPS URL
// - API key is sent via X-API-KEY only when configured
// - 10MB response body limit to prevent memory exhaustion
// - Context-aware cancellation support
// - Certificate verification remains enabled for HTTPS requests
//
// URL policy:
// - This helper does not reject non-HTTPS URLs by itself.
// - Callers are responsible for passing trusted HTTPS API endpoints.
//
// Retry behavior:
// - Retries on rate-limit (429), server errors (5xx), and network errors
// - Exponential backoff with jitter between retries
// - Respects context cancellation
//
// Authentication:
// - Uses X-API-KEY header when API key is provided (encrypted via TLS)
// - User-Agent mimics wget for compatibility
func (c *Client) makeRequest(ctx context.Context, url string, target interface{}) error {
	if err := c.rateLimitCooldownError(time.Now()); err != nil {
		return err
	}

	var lastErr error
	var lastStatusCode int
	var nextRetryDelay time.Duration
	var useNextRetryDelay bool
	policy := c.httpPolicy()

	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := waitBeforeAPIRetry(ctx, attempt, url, policy.RetryBaseDelay, nextRetryDelay, useNextRetryDelay); err != nil {
			return err
		}
		if useNextRetryDelay {
			useNextRetryDelay = false
		}

		req, err := c.newAuthenticatedGETRequest(ctx, url)
		if err != nil {
			return err
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if isRetryableNetworkError(err) {
				if hasRemainingAPIAttempts(attempt, policy.MaxAttempts) {
					log.WithError(err).WithField("attempt", attempt+1).Warn("API request failed, retrying...")
				}
				continue
			}
			return lastErr
		}

		result := handleAPIResponse(resp, target, attempt, policy.MaxAttempts)
		lastStatusCode = result.statusCode
		if result.retry {
			lastErr = result.err
			nextRetryDelay = result.retryDelay
			useNextRetryDelay = result.useRetryDelay
			if result.statusCode == http.StatusTooManyRequests && result.useRetryDelay && result.retryDelay > 0 {
				c.setRateLimitCooldown(time.Now().Add(result.retryDelay))
			}
			continue
		}
		if result.err == nil {
			c.clearRateLimitCooldown()
		}
		return result.err
	}

	if lastStatusCode == 0 {
		return fmt.Errorf("failed after %d attempts: %w", policy.MaxAttempts, lastErr)
	}
	return fmt.Errorf("failed after %d attempts (last status: %d): %w", policy.MaxAttempts, lastStatusCode, lastErr)
}

func hasRemainingAPIAttempts(attempt, maxAttempts int) bool {
	return attempt+1 < maxAttempts
}

func (c *Client) rateLimitCooldownError(now time.Time) error {
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()

	if c.rateLimitCooldown.IsZero() {
		return nil
	}
	if !now.Before(c.rateLimitCooldown) {
		c.rateLimitCooldown = time.Time{}
		return nil
	}
	return &RateLimitCooldownError{Until: c.rateLimitCooldown}
}

func (c *Client) setRateLimitCooldown(until time.Time) {
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()

	if until.After(c.rateLimitCooldown) {
		c.rateLimitCooldown = until
	}
}

func (c *Client) clearRateLimitCooldown() {
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()
	c.rateLimitCooldown = time.Time{}
}

func waitBeforeAPIRetry(
	ctx context.Context,
	attempt int,
	url string,
	retryBaseDelay time.Duration,
	nextRetryDelay time.Duration,
	useNextRetryDelay bool,
) error {
	if attempt == 0 {
		return nil
	}

	retryDelay := retrypolicy.DelayWithBase(attempt, retryBaseDelay)
	if useNextRetryDelay {
		retryDelay = nextRetryDelay
	}
	log.WithFields(log.Fields{
		"attempt":  attempt + 1,
		"delay_ms": retryDelay.Milliseconds(),
		"url":      url,
	}).Debug("Retrying API request")

	if retryDelay <= 0 {
		return nil
	}
	return apiRetrySleep(ctx, retryDelay)
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (c *Client) newAuthenticatedGETRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set(apiKeyHeader, c.apiKey)
	}
	req.Header.Set(userAgentHeader, apiUserAgent)
	req.Header.Set(acceptHeader, jsonContentType)
	return req, nil
}

type apiResponseResult struct {
	retry         bool
	statusCode    int
	retryDelay    time.Duration
	useRetryDelay bool
	err           error
}

func handleAPIResponse(resp *http.Response, target interface{}, attempt, maxAttempts int) apiResponseResult {
	result := apiResponseResult{statusCode: resp.StatusCode}
	if retrypolicy.IsRetryableStatus(resp.StatusCode) {
		drainAndCloseAPIResponse(resp.Body)

		result.retry = true
		result.err = &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			// A Retry-After of "0" or a date already in the past parses to a
			// zero delay; only override the exponential backoff for a
			// genuinely positive delay, matching the floor the Discord
			// sender got on 2026-09-03 (webhook.go's
			// discordRateLimitRetryDelay). Without this, a degenerate header
			// retried faster than a header-less 429 and armed no cooldown.
			if delay, ok := retryAfterDelay(resp.Header.Get("Retry-After"), time.Now()); ok && delay > 0 {
				result.retryDelay = delay
				result.useRetryDelay = true
			}
		}
		if hasRemainingAPIAttempts(attempt, maxAttempts) {
			log.WithFields(log.Fields{
				"status_code": resp.StatusCode,
				"attempt":     attempt + 1,
			}).Warn("API request failed with retryable status, retrying...")
		}
		return result
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, apiErrorBodyPreviewBytes))
		result.err = &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       sanitizeAPIErrorBody(body),
		}
		return result
	}

	result.err = decodeBoundedAPIResponse(resp.Body, target)
	return result
}

func sanitizeAPIErrorBody(body []byte) string {
	bodyText := strings.Join(strings.Fields(textutil.RedactWebhookSecrets(string(body))), " ")
	return textutil.TruncateText(bodyText, apiErrorBodyPreviewBytes)
}

func decodeBoundedAPIResponse(body io.Reader, target interface{}) error {
	limitedBody := &io.LimitedReader{
		R: body,
		N: maxAPIResponseBytes + 1,
	}
	decoder := json.NewDecoder(limitedBody)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	if limitedBody.N <= 0 {
		return fmt.Errorf("API response exceeds %d byte limit", maxAPIResponseBytes)
	}

	var extra any
	err := decoder.Decode(&extra)
	if err == nil {
		return fmt.Errorf("API response contains trailing JSON data")
	}
	if limitedBody.N <= 0 {
		return fmt.Errorf("API response exceeds %d byte limit", maxAPIResponseBytes)
	}
	if err != io.EOF {
		return fmt.Errorf("API response contains trailing data: %w", err)
	}
	return nil
}

func drainAndCloseAPIResponse(body io.ReadCloser) {
	_, _ = io.CopyN(io.Discard, body, maxAPIResponseBytes)
	_ = body.Close()
}

func isRetryableNetworkError(err error) bool {
	return retrypolicy.IsRetryableNetworkError(err)
}

// RetryAfterDelay parses a Retry-After header relative to now.
func RetryAfterDelay(header string, now time.Time) (time.Duration, bool) {
	return retrypolicy.RetryAfterDelay(header, now)
}

func retryAfterDelay(header string, now time.Time) (time.Duration, bool) {
	return RetryAfterDelay(header, now)
}

// logRequest logs API request details for monitoring and debugging
//
// Logs include:
// - Request URL as supplied by the caller
// - Response time for performance monitoring
// - Error details for troubleshooting
// - Uses different log levels based on success/failure
func (c *Client) logRequest(url string, duration time.Duration, err error) {
	fields := log.Fields{
		"url":         url,
		"duration_ms": duration.Milliseconds(),
	}

	if err != nil {
		fields["error"] = OperatorErrorMessage(err)
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) {
			fields["status_code"] = statusErr.StatusCode
			fields["status"] = statusErr.Status
		}
		log.WithFields(fields).Debug("API request failed")
	} else {
		log.WithFields(fields).Debug("API request successful")
	}
}

// Close closes the HTTP client and cleans up idle connections
// Should be called during application shutdown
func (c *Client) Close() error {
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}
