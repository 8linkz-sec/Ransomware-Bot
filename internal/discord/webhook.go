package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/discordurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

const maxDiscordWebhookResponseBytes = 8 << 10

// WebhookSender handles sending messages to Discord webhooks
type WebhookSender struct {
	client *http.Client
	policy webhookhttp.Policy
}

type WebhookHTTPError = webhookhttp.HTTPStatusError

// NewWebhookSender creates a new webhook sender instance
func NewWebhookSender(policies ...webhookhttp.Policy) (*WebhookSender, error) {
	policy := webhookPolicyFromOptional(policies)
	return newWebhookSenderWithClient(webhookhttp.NewClient(policy), policy), nil
}

func newWebhookSenderWithClient(client *http.Client, policies ...webhookhttp.Policy) *WebhookSender {
	return &WebhookSender{client: client, policy: webhookPolicyFromOptional(policies)}
}

func webhookPolicyFromOptional(policies []webhookhttp.Policy) webhookhttp.Policy {
	if len(policies) == 0 {
		return webhookhttp.DefaultPolicy()
	}
	return webhookhttp.NormalizePolicy(policies[0])
}

// Close closes the HTTP client and releases resources.
func (w *WebhookSender) Close() error {
	if w.client != nil {
		w.client.CloseIdleConnections()
	}
	return nil
}

// SendRansomwareEntry sends a ransomware entry to a Discord webhook
func (w *WebhookSender) SendRansomwareEntry(
	ctx context.Context,
	webhookURL string,
	entry model.RansomwareEntry,
	formatConfig *notifyfmt.FormatOptions,
) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Discord embed
	embed := formatRansomwareEmbed(entry, formatConfig)

	// Create webhook parameters
	params := &WebhookParams{
		Embeds: []*MessageEmbed{embed},
	}

	// Extract webhook ID and token from URL
	parts, err := discordurl.Parse(webhookURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", webhookhttp.NewPermanentError(err))
	}

	// Send the webhook
	if err := w.executeWebhook(ctx, parts.ID, parts.Token, params); err != nil {
		return fmt.Errorf("failed to send ransomware entry: %w", err)
	}

	log.WithFields(log.Fields{
		"entry_id":   entry.ID,
		"group":      entry.Group,
		"has_victim": entry.Victim != "",
	}).Info("Sent ransomware entry to Discord")

	return nil
}

// SendRSSEntry sends an RSS entry to a Discord webhook.
func (w *WebhookSender) SendRSSEntry(
	ctx context.Context,
	webhookURL string,
	entry model.RSSEntry,
	feedType string,
	formatConfig *notifyfmt.FormatOptions,
) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Discord embed
	embed := formatRSSEmbed(entry, feedType, formatConfig)

	// Create webhook parameters
	params := &WebhookParams{
		Embeds: []*MessageEmbed{embed},
	}

	// Extract webhook ID and token from URL
	parts, err := discordurl.Parse(webhookURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", webhookhttp.NewPermanentError(err))
	}

	// Send the webhook
	if err := w.executeWebhook(ctx, parts.ID, parts.Token, params); err != nil {
		return fmt.Errorf("failed to send RSS entry: %w", err)
	}

	log.WithFields(log.Fields{
		"feed_title": entry.FeedTitle,
		"feed_url":   textutil.RedactURLCredentials(entry.FeedURL),
		"has_title":  entry.Title != "",
	}).Info("Sent RSS entry to Discord")

	return nil
}

// executeWebhook sends a webhook message to Discord.
func (w *WebhookSender) executeWebhook(
	ctx context.Context,
	webhookID, webhookToken string,
	params *WebhookParams,
) error {
	if w.client == nil {
		return fmt.Errorf("discord HTTP client not available")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	webhookURL := discordWebhookEndpoint(webhookID, webhookToken)
	jsonData, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", webhookhttp.NewPermanentError(err))
	}

	policy := webhookhttp.NormalizePolicy(w.policy)
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf(
				"failed to create request: %w",
				webhookhttp.NewPermanentError(textutil.RedactWebhookErrorForURL(err, webhookURL)),
			)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.client.Do(req)
		if err != nil {
			redactedErr := textutil.RedactWebhookErrorForURL(err, webhookURL)
			return fmt.Errorf(
				"failed to send request: %w",
				webhookhttp.NewTransportError("discord", err, redactedErr.Error()),
			)
		}

		retry, err := handleDiscordWebhookResponse(resp, len(jsonData), attempt, policy)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
		if err := sleepWithContext(ctx, discordRateLimitRetryDelay(resp, policy)); err != nil {
			return err
		}
	}

	return fmt.Errorf("discord webhook rate limited after %d attempts", policy.MaxAttempts)
}

func handleDiscordWebhookResponse(
	resp *http.Response,
	payloadSize int,
	attempt int,
	policy webhookhttp.Policy,
) (bool, error) {
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}

	bodyStr, readErr := readDiscordWebhookResponseBody(resp.Body)
	if readErr != nil {
		log.WithError(readErr).Warn("Failed to read Discord webhook response body")
	}
	if resp.StatusCode == http.StatusTooManyRequests && attempt < policy.MaxAttempts-1 {
		delay := discordRateLimitRetryDelay(resp, policy)
		log.WithFields(log.Fields{
			"retry_after_ms": delay.Milliseconds(),
			"attempt":        attempt + 1,
		}).Warn("Discord rate limited, retrying webhook delivery")
		return true, nil
	}

	log.WithFields(log.Fields{
		"status_code":   resp.StatusCode,
		"response_body": bodyStr,
		"payload_size":  payloadSize,
	}).Error("Discord webhook request failed")
	return false, webhookhttp.NewHTTPStatusError("discord", resp.StatusCode, resp.Status, "", nil)
}

func discordRateLimitRetryDelay(resp *http.Response, policy webhookhttp.Policy) time.Duration {
	// Discord has no other backoff in its retry loop, so a Retry-After of 0 or a
	// date already in the past must fall back to the policy base delay instead of
	// producing a zero-delay retry.
	if delay, ok := retrypolicyDelayFromHeader(resp); ok && delay > 0 {
		return delay
	}
	return policy.RetryBaseDelay
}

func retrypolicyDelayFromHeader(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	return retrypolicy.RetryAfterDelay(resp.Header.Get("Retry-After"), time.Now())
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

func discordWebhookEndpoint(webhookID, webhookToken string) string {
	return "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken
}

func readDiscordWebhookResponseBody(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxDiscordWebhookResponseBytes+1))
	truncated := len(data) > maxDiscordWebhookResponseBytes
	if truncated {
		data = data[:maxDiscordWebhookResponseBytes]
	}

	bodyStr := textutil.RedactWebhookSecrets(string(data))
	if truncated {
		bodyStr += "...[truncated]"
	}
	return bodyStr, err
}
