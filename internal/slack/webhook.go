package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/rss"

	log "github.com/sirupsen/logrus"
)

// Retry configuration
const (
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second
)

// WebhookSender handles sending messages to Slack webhooks
type WebhookSender struct {
	client *http.Client
}

// NewWebhookSender creates a new Slack webhook sender instance
func NewWebhookSender() *WebhookSender {
	return &WebhookSender{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Close closes the HTTP client and releases resources
func (w *WebhookSender) Close() error {
	if w.client != nil {
		w.client.CloseIdleConnections()
	}
	return nil
}

// SendRansomwareEntry sends a ransomware entry to a Slack webhook
func (w *WebhookSender) SendRansomwareEntry(ctx context.Context, webhookURL string, entry api.RansomwareEntry, formatConfig *config.FormatConfig) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Slack Block Kit message
	payload := formatRansomwareMessage(entry, formatConfig)

	// Send the webhook
	if err := w.executeWebhook(ctx, webhookURL, payload); err != nil {
		return fmt.Errorf("failed to send ransomware entry: %w", err)
	}

	log.WithFields(log.Fields{
		"group":  entry.Group,
		"victim": entry.Victim,
	}).Info("Sent ransomware entry to Slack")

	return nil
}

// SendRSSEntry sends an RSS entry to a Slack webhook
func (w *WebhookSender) SendRSSEntry(ctx context.Context, webhookURL string, entry rss.Entry, formatConfig *config.FormatConfig) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Slack Block Kit message
	payload := formatRSSMessage(entry, formatConfig)

	// Send the webhook
	if err := w.executeWebhook(ctx, webhookURL, payload); err != nil {
		return fmt.Errorf("failed to send RSS entry: %w", err)
	}

	log.WithFields(log.Fields{
		"title":      entry.Title,
		"feed_title": entry.FeedTitle,
	}).Info("Sent RSS entry to Slack")

	return nil
}

// executeWebhook sends a webhook message to Slack with retry logic
func (w *WebhookSender) executeWebhook(ctx context.Context, webhookURL string, payload any) error {
	// Marshal payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	log.WithField("payload_size", len(jsonData)).Debug("Sending Slack webhook request")
	log.WithField("payload", string(jsonData)).Trace("Slack webhook payload")

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
			}).Debug("Retrying Slack webhook")

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		// Create HTTP request with context
		req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		// Send request
		resp, err := w.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %w", err)
			if isRetryableError(err) {
				log.WithError(err).WithField("attempt", attempt+1).Warn("Slack webhook request failed, retrying...")
				continue
			}
			return lastErr
		}

		// Read response body
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.WithError(readErr).Warn("Failed to read Slack webhook response body")
		}
		_ = resp.Body.Close()

		lastStatusCode = resp.StatusCode

		// Success
		if resp.StatusCode == http.StatusOK {
			log.WithField("response", string(body)).Debug("Slack webhook response")
			return nil
		}

		// Check if status code is retryable
		bodyStr := string(body)
		lastErr = fmt.Errorf("slack webhook returned status %d: %s", resp.StatusCode, bodyStr)

		if isRetryableStatusCode(resp.StatusCode) {
			// Honor Retry-After header for 429 responses
			if resp.StatusCode == http.StatusTooManyRequests {
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
						delay := time.Duration(seconds) * time.Second
						if delay > 60*time.Second {
							delay = 60 * time.Second // cap at 60s
						}
						log.WithFields(log.Fields{
							"retry_after": seconds,
							"attempt":     attempt + 1,
						}).Warn("Slack rate limited, waiting Retry-After duration")
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(delay):
						}
						continue
					}
				}
			}
			log.WithFields(log.Fields{
				"status_code": resp.StatusCode,
				"attempt":     attempt + 1,
			}).Warn("Slack webhook failed with retryable status, retrying...")
			continue
		}

		// Non-retryable error - log and return immediately
		log.WithFields(log.Fields{
			"status_code":   resp.StatusCode,
			"response_body": bodyStr,
			"payload_size":  len(jsonData),
		}).Error("Slack webhook request failed")

		return lastErr
	}

	return fmt.Errorf("failed after %d attempts (last status: %d): %w", maxRetries, lastStatusCode, lastErr)
}

// isRetryableError checks if a network error is temporary and worth retrying
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "temporary") ||
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

