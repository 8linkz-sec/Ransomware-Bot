package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

const (
	maxSlackWebhookResponseBytes = 8 << 10
	contentTypeHeader            = "Content-Type"
	jsonContentType              = "application/json"
)

var slackRetrySleep = sleepWithContext

// WebhookSender handles sending messages to Slack webhooks
type WebhookSender struct {
	client *http.Client
	policy webhookhttp.Policy
}

// NewWebhookSender creates a new Slack webhook sender instance
func NewWebhookSender(policies ...webhookhttp.Policy) *WebhookSender {
	policy := webhookPolicyFromOptional(policies)
	return newWebhookSenderWithClient(webhookhttp.NewClient(policy), policy)
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

// Close closes the HTTP client and releases resources
func (w *WebhookSender) Close() error {
	if w.client != nil {
		w.client.CloseIdleConnections()
	}
	return nil
}

// SendPayload sends a preformatted Slack webhook payload.
func (w *WebhookSender) SendPayload(ctx context.Context, webhookURL string, payload any) error {
	if err := ensureSlackContextActive(ctx); err != nil {
		return err
	}
	if err := w.executeWebhook(ctx, webhookURL, payload); err != nil {
		return fmt.Errorf("failed to send Slack payload: %w", err)
	}
	return nil
}

// SendRansomwareEntry sends a ransomware entry to a Slack webhook
func (w *WebhookSender) SendRansomwareEntry(
	ctx context.Context,
	webhookURL string,
	entry model.RansomwareEntry,
	formatConfig *notifyfmt.FormatOptions,
) error {
	// Format the entry as a Slack Block Kit message
	payload := formatRansomwareMessage(entry, formatConfig)

	if err := w.SendPayload(ctx, webhookURL, payload); err != nil {
		return fmt.Errorf("failed to send ransomware entry: %w", err)
	}

	log.WithFields(log.Fields{
		"entry_id":   entry.ID,
		"group":      entry.Group,
		"has_victim": entry.Victim != "",
	}).Info("Sent ransomware entry to Slack")

	return nil
}

// SendRSSEntry sends an RSS entry to a Slack webhook
func (w *WebhookSender) SendRSSEntry(
	ctx context.Context,
	webhookURL string,
	entry model.RSSEntry,
	feedType string,
	formatConfig *notifyfmt.FormatOptions,
) error {
	// Format the entry as a Slack Block Kit message
	payload := formatRSSMessage(entry, formatConfig, feedType)

	if err := w.SendPayload(ctx, webhookURL, payload); err != nil {
		return fmt.Errorf("failed to send RSS entry: %w", err)
	}

	log.WithFields(log.Fields{
		"feed_title": entry.FeedTitle,
		"feed_url":   textutil.RedactURLCredentials(entry.FeedURL),
		"has_title":  entry.Title != "",
	}).Info("Sent RSS entry to Slack")

	return nil
}

func ensureSlackContextActive(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// executeWebhook sends a webhook message to Slack with retry logic
func (w *WebhookSender) executeWebhook(ctx context.Context, webhookURL string, payload any) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", webhookhttp.NewPermanentError(err))
	}

	log.WithField("payload_size", len(jsonData)).Debug("Sending Slack webhook request")
	log.WithField("payload", string(jsonData)).Trace("Slack webhook payload")

	policy := webhookhttp.NormalizePolicy(w.policy)
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		outcome, err := w.executeSlackWebhookAttempt(ctx, webhookURL, jsonData, attempt, policy)
		if err != nil {
			return err
		}
		retry, err := waitForSlackWebhookRetry(ctx, outcome)
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		return outcome.err
	}

	return fmt.Errorf("slack webhook rate limited after %d attempts", policy.MaxAttempts)
}

func (w *WebhookSender) executeSlackWebhookAttempt(
	ctx context.Context,
	webhookURL string,
	jsonData []byte,
	attempt int,
	policy webhookhttp.Policy,
) (slackWebhookOutcome, error) {
	if err := waitBeforeSlackRetry(ctx, attempt, policy); err != nil {
		return slackWebhookOutcome{}, err
	}

	resp, err := w.doSlackWebhookRequest(ctx, webhookURL, jsonData)
	if err != nil {
		return slackWebhookOutcome{}, err
	}

	return handleSlackWebhookResponse(resp, webhookURL, len(jsonData), attempt, policy), nil
}

func waitForSlackWebhookRetry(ctx context.Context, outcome slackWebhookOutcome) (bool, error) {
	if outcome.err == nil || !outcome.retry {
		return false, nil
	}
	if err := waitForSlackRetryAfter(ctx, outcome.retryDelay); err != nil {
		return false, err
	}
	return true, nil
}

func waitBeforeSlackRetry(ctx context.Context, attempt int, policy webhookhttp.Policy) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if attempt == 0 {
		return nil
	}

	retryDelay := retrypolicy.DelayWithBase(attempt, policy.RetryBaseDelay)
	log.WithFields(log.Fields{
		"attempt": attempt + 1,
		"delay":   retryDelay,
	}).Debug("Retrying Slack webhook")

	return slackRetrySleep(ctx, retryDelay)
}

func (w *WebhookSender) doSlackWebhookRequest(
	ctx context.Context,
	webhookURL string,
	jsonData []byte,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf(
			"failed to create request: %w",
			webhookhttp.NewPermanentError(textutil.RedactWebhookErrorForURL(err, webhookURL)),
		)
	}
	req.Header.Set(contentTypeHeader, jsonContentType)

	resp, err := w.client.Do(req)
	if err != nil {
		redactedErr := textutil.RedactWebhookErrorForURL(err, webhookURL)
		return nil, fmt.Errorf(
			"failed to send request: %w",
			webhookhttp.NewTransportError("slack", err, redactedErr.Error()),
		)
	}
	return resp, nil
}

type slackWebhookOutcome struct {
	retry      bool
	retryDelay time.Duration
	err        error
}

func handleSlackWebhookResponse(resp *http.Response, webhookURL string, payloadSize int, attempt int, policy webhookhttp.Policy) slackWebhookOutcome {
	bodyStr, readErr := readSlackWebhookResponseBody(resp.Body, webhookURL)
	if readErr != nil {
		log.WithError(readErr).Warn("Failed to read Slack webhook response body")
	}
	_ = resp.Body.Close()

	if isSlackWebhookSuccessStatus(resp.StatusCode) {
		log.WithField("response", bodyStr).Debug("Slack webhook response")
		if resp.StatusCode != http.StatusOK {
			// Real Slack always answers exactly 200, so a
			// 201/204 only ever comes from an operator-configured
			// Slack-compatible host. Logged at INFO (not just Debug) so an
			// operator can see when their host is using a non-standard
			// success status. 202 stays excluded -- see
			// isSlackWebhookSuccessStatus.
			log.WithFields(log.Fields{
				"status_code": resp.StatusCode,
				"host":        slackWebhookHost(webhookURL),
			}).Info("Slack-compatible webhook returned a non-200 success status")
		}
		return slackWebhookOutcome{}
	}

	webhookErr := webhookhttp.NewHTTPStatusError("slack", resp.StatusCode, resp.Status, bodyStr, nil)
	if resp.StatusCode == http.StatusTooManyRequests && attempt < policy.MaxAttempts-1 {
		if delay, ok := retryAfterDelay(resp, policy); ok {
			log.WithFields(log.Fields{
				"retry_after": int(delay / time.Second),
				"attempt":     attempt + 1,
			}).Warn("Slack rate limited, waiting Retry-After duration")
			return slackWebhookOutcome{retry: true, retryDelay: delay, err: webhookErr}
		}
	}

	log.WithFields(log.Fields{
		"status_code":   resp.StatusCode,
		"response_body": bodyStr,
		"payload_size":  payloadSize,
	}).Error("Slack webhook request failed")
	return slackWebhookOutcome{err: webhookErr}
}

// isSlackWebhookSuccessStatus reports whether statusCode counts as a
// successful Slack-compatible delivery. Real Slack incoming webhooks answer
// exactly 200; 201 and 204 are additionally accepted because
// slack_compatible_webhook_hosts is an arbitrary, operator-populated hostname
// allow-list and some self-hosted receivers answer one of those for an
// accepted delivery. 202 is deliberately excluded: "accepted, processing
// follows" can still fail after this response, so treating it as success
// would let a proxy that never actually forwards the message be recorded as
// delivered, with no retry and no dead letter -- unlike the Discord sender,
// which accepts the full 2xx range because Discord answers 204 and never 202.
func isSlackWebhookSuccessStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return true
	default:
		return false
	}
}

// slackWebhookHost extracts the hostname from a Slack-compatible webhook URL
// for logging, never the full URL (which carries the webhook secret).
func slackWebhookHost(webhookURL string) string {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func retryAfterDelay(resp *http.Response, policy webhookhttp.Policy) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	delay, ok := retrypolicy.RetryAfterDelay(resp.Header.Get("Retry-After"), time.Now())
	if ok {
		return delay, true
	}
	if policy.RetryBaseDelay <= 0 {
		return 0, false
	}
	return policy.RetryBaseDelay, true
}

func waitForSlackRetryAfter(ctx context.Context, delay time.Duration) error {
	return slackRetrySleep(ctx, delay)
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

func readSlackWebhookResponseBody(body io.Reader, webhookURL string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxSlackWebhookResponseBytes+1))
	truncated := len(data) > maxSlackWebhookResponseBytes
	if truncated {
		data = data[:maxSlackWebhookResponseBytes]
	}

	bodyStr := textutil.RedactWebhookSecretsForURL(string(data), webhookURL)
	if truncated {
		bodyStr += "...[truncated]"
	}
	return bodyStr, err
}
