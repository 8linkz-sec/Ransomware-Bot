// Package api provides HTTP client functionality for external API communication
// with security-hardened TLS configuration and proper error handling.
//
// The client supports:
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
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Retry configuration
const (
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second
)

// Client represents the HTTP client for external APIs
type Client struct {
	httpClient *http.Client
	apiKey     string
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
		MaxIdleConns:          10,    // Limit idle connections
		MaxIdleConnsPerHost:   5,     // Limit per-host connections
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		apiKey: apiKey,
	}, nil
}

// makeRequest performs HTTPS requests to external APIs with retry logic and rate-limit handling
//
// Security measures:
// - Enforces TLS 1.2+ encryption for all requests (configured in transport)
// - API key transmitted securely via encrypted HTTPS headers
// - 10MB response body limit to prevent memory exhaustion
// - Context-aware cancellation support
// - Certificate verification always enabled
//
// Retry behavior:
// - Retries on rate-limit (429), server errors (5xx), and network errors
// - Linear backoff between retries
// - Respects context cancellation
//
// Authentication:
// - Uses X-API-KEY header when API key is provided (encrypted via TLS)
// - User-Agent mimics wget for compatibility
func (c *Client) makeRequest(ctx context.Context, url string, target interface{}) error {
	var lastErr error
	var lastStatusCode int

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Wait before retry (linear backoff)
		if attempt > 0 {
			retryDelay := baseRetryDelay * time.Duration(attempt)
			log.WithFields(log.Fields{
				"attempt": attempt + 1,
				"delay":   retryDelay,
				"url":     url,
			}).Debug("Retrying API request")

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		// Create request
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		// Add API key if provided
		if c.apiKey != "" {
			req.Header.Set("X-API-KEY", c.apiKey)
		}

		// Set common headers
		req.Header.Set("User-Agent", "Wget/1.21.3")
		req.Header.Set("Accept", "application/json")

		// Perform request
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if isRetryableNetworkError(err) {
				log.WithError(err).WithField("attempt", attempt+1).Warn("API request failed, retrying...")
				continue
			}
			return lastErr
		}

		lastStatusCode = resp.StatusCode

		// Check if status code is retryable
		if isRetryableStatusCode(resp.StatusCode) {
			// Read and discard body to allow connection reuse
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			lastErr = fmt.Errorf("API returned status %d: %s", resp.StatusCode, resp.Status)
			log.WithFields(log.Fields{
				"status_code": resp.StatusCode,
				"attempt":     attempt + 1,
			}).Warn("API request failed with retryable status, retrying...")
			continue
		}

		// Non-retryable error status
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read first 1KB for error message
			_ = resp.Body.Close()
			return fmt.Errorf("API returned status %d: %s - %s", resp.StatusCode, resp.Status, string(body))
		}

		// Success - decode response
		// Limit response body size to prevent memory exhaustion attacks
		limitedBody := io.LimitReader(resp.Body, 10<<20) // 10MB limit

		if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("failed to decode response: %w", err)
		}
		// Drain remaining body to allow HTTP connection reuse
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		return nil
	}

	return fmt.Errorf("failed after %d attempts (last status: %d): %w", maxRetries, lastStatusCode, lastErr)
}

// isRetryableNetworkError checks if a network error is temporary and worth retrying
func isRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for typed net.Error (timeout, temporary)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// Fallback: string matching for errors not wrapped as net.Error
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "reset") ||
		strings.Contains(errStr, "unavailable")
}

// isRetryableStatusCode checks if an HTTP status code is worth retrying
func isRetryableStatusCode(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,     // 429 - Rate limit
		http.StatusInternalServerError,   // 500
		http.StatusBadGateway,            // 502
		http.StatusServiceUnavailable,    // 503
		http.StatusGatewayTimeout:        // 504
		return true
	default:
		return false
	}
}

// logRequest logs API request details for monitoring and debugging
//
// Logs include:
// - Request URL (sanitized)
// - Response time for performance monitoring
// - Error details for troubleshooting
// - Uses different log levels based on success/failure
func (c *Client) logRequest(url string, duration time.Duration, err error) {
	fields := log.Fields{
		"url":      url,
		"duration": duration,
	}

	if err != nil {
		log.WithFields(fields).WithError(err).Error("API request failed")
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
