package discord

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"Ransomware-Bot/internal/api"
	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/rss"

	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
)

// Retry configuration
const (
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second
)

// WebhookSender handles sending messages to Discord webhooks
type WebhookSender struct {
	session *discordgo.Session
}

// NewWebhookSender creates a new webhook sender instance
func NewWebhookSender() (*WebhookSender, error) {
	// For webhook-only operations, we create a minimal Discord session
	// The empty token is fine since we're only using webhook functionality
	session, err := discordgo.New("")
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	// Configure HTTP client with timeout to prevent hanging requests
	session.Client = &http.Client{
		Timeout: 10 * time.Second,
	}

	return &WebhookSender{
		session: session,
	}, nil
}

// Close closes the Discord session and releases resources
func (w *WebhookSender) Close() error {
	if w.session != nil {
		return w.session.Close()
	}
	return nil
}

// SendRansomwareEntry sends a ransomware entry to a Discord webhook
func (w *WebhookSender) SendRansomwareEntry(ctx context.Context, webhookURL string, entry api.RansomwareEntry, formatConfig *config.FormatConfig) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Discord embed
	embed := w.formatRansomwareEmbed(entry, formatConfig)

	// Create webhook parameters
	params := &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	}

	// Extract webhook ID and token from URL
	webhookID, webhookToken, err := parseWebhookURL(webhookURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	// Send the webhook
	if err := w.executeWebhook(ctx, webhookID, webhookToken, params); err != nil {
		return fmt.Errorf("failed to send ransomware entry: %w", err)
	}

	log.WithFields(log.Fields{
		"group":  entry.Group,
		"victim": entry.Victim,
	}).Info("Sent ransomware entry to Discord")

	return nil
}

// SendRSSEntry sends an RSS entry to a Discord webhook
func (w *WebhookSender) SendRSSEntry(ctx context.Context, webhookURL string, entry rss.Entry) error {
	// Check if context is cancelled before proceeding
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Format the entry as a Discord embed
	embed := w.formatRSSEmbed(entry)

	// Create webhook parameters
	params := &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	}

	// Extract webhook ID and token from URL
	webhookID, webhookToken, err := parseWebhookURL(webhookURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	// Send the webhook
	if err := w.executeWebhook(ctx, webhookID, webhookToken, params); err != nil {
		return fmt.Errorf("failed to send RSS entry: %w", err)
	}

	log.WithFields(log.Fields{
		"title":      entry.Title,
		"feed_title": entry.FeedTitle,
	}).Info("Sent RSS entry to Discord")

	return nil
}

// executeWebhook sends a webhook message to Discord with retry logic
func (w *WebhookSender) executeWebhook(ctx context.Context, webhookID, webhookToken string, params *discordgo.WebhookParams) error {
	if w.session == nil {
		return fmt.Errorf("discord session not available")
	}

	var lastErr error
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
			}).Debug("Retrying Discord webhook")

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		// Execute webhook
		_, err := w.session.WebhookExecute(webhookID, webhookToken, false, params)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			return err
		}

		log.WithError(err).WithField("attempt", attempt+1).Warn("Discord webhook failed, retrying...")
	}

	return fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

// isRetryableError checks if an error is temporary and worth retrying
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// Discord rate limit (HTTP 429)
	if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
		return true
	}

	// Temporary network errors
	if strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "temporary") ||
		strings.Contains(errStr, "unavailable") {
		return true
	}

	// Server errors (5xx)
	if strings.Contains(errStr, "500") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") {
		return true
	}

	return false
}

// parseWebhookURL extracts the webhook ID and token from a Discord webhook URL
func parseWebhookURL(webhookURL string) (string, string, error) {
	// Discord webhook URLs have the format:
	// https://discord.com/api/webhooks/{webhook.id}/{webhook.token}

	const prefix = "https://discord.com/api/webhooks/"
	const legacyPrefix = "https://discordapp.com/api/webhooks/"

	var remainder string
	switch {
	case strings.HasPrefix(webhookURL, prefix):
		remainder = webhookURL[len(prefix):]
	case strings.HasPrefix(webhookURL, legacyPrefix):
		remainder = webhookURL[len(legacyPrefix):]
	default:
		return "", "", fmt.Errorf("invalid webhook URL format")
	}

	// Split by '/' to get ID and token
	parts := strings.Split(remainder, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("webhook URL missing ID or token")
	}

	webhookID := parts[0]
	// Strip query parameters or fragments from token (e.g. ?wait=true)
	webhookToken := strings.SplitN(parts[1], "?", 2)[0]
	webhookToken = strings.SplitN(webhookToken, "#", 2)[0]

	if webhookID == "" || webhookToken == "" {
		return "", "", fmt.Errorf("webhook ID or token is empty")
	}

	return webhookID, webhookToken, nil
}
