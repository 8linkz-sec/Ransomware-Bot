package scheduler

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

func TestSlackCancelledSendIsRecordedAsCanceledAndStaysQueued(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cancel()
		<-release
	}))
	// LIFO: release the blocked handler before Close waits for it.
	defer server.Close()
	defer close(release)

	entry := api.RansomwareEntry{ID: "cancel-1", Group: "lockbit", Victim: "Example Cancel", Country: "DE"}
	s.sendAPIEntriesIndividuallyToSlack(ctx, []api.RansomwareEntry{entry}, server.URL)

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	retryItems := reloaded.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("persisted retry queue length = %d, want 1", len(retryItems))
	}
	if got := retryItems[0].ErrorCategory; got != "canceled" {
		t.Fatalf("persisted error_category = %q, want %q", got, "canceled")
	}
	if got := retryItems[0].LastError; got != "Webhook delivery canceled" {
		t.Fatalf("persisted last_error = %q, want %q", got, "Webhook delivery canceled")
	}
	// The cancelled attempt is still consumed: retry_count is 1 before and after
	// this change. Only the category, the message and the queue-vs-dead-letter
	// decision changed, never the attempt budget.
	if got := retryItems[0].RetryCount; got != 1 {
		t.Fatalf("persisted retry_count = %d, want 1", got)
	}
	key := api.GenerateEntryKey(entry)
	if s.statusTracker.IsRetryDeadLettered(key, "slack", "api") {
		t.Fatal("cancelled Slack send was dead-lettered instead of staying queued")
	}
}

func TestDiscordCancelledSendStaysQueued(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	// A Discord delivery cannot be driven against a local httptest server from
	// the scheduler because discordWebhookEndpoint hardcodes discord.com, so the
	// queue-vs-dead-letter decision is exercised with the exact error shape
	// internal/discord/webhook.go builds for a mid-request cancel.
	const token = "cancel-token-abcdefghijklmnop"
	raw := &url.Error{
		Op:  "Post",
		URL: "https://discord.com/api/webhooks/123456789/" + token,
		Err: context.Canceled,
	}
	err := fmt.Errorf("failed to send request: %w",
		webhookhttp.NewTransportError("discord", raw,
			`Post "https://discord.com/api/webhooks/[redacted]": context canceled`))

	const itemKey = "id:discord-cancel-1"
	const destinationID = "discord.ransomware"
	s.recordWebhookDeliveryFailure(
		itemKey,
		destinationID,
		"discord",
		"api",
		"Example Discord Cancel",
		err,
		s.config.RetryMaxAttempts,
		s.config.RetryWindow,
		[]byte(`{"content":"example"}`),
	)
	// The direct call bypasses the send loop, which is what normally flushes.
	s.persistStatus("discord_cancel_test")

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	retryItems := reloaded.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("persisted retry queue length = %d, want 1", len(retryItems))
	}
	if got := retryItems[0].ErrorCategory; got != "canceled" {
		t.Fatalf("persisted error_category = %q, want %q", got, "canceled")
	}
	if got := retryItems[0].Messenger; got != "discord" {
		t.Fatalf("persisted messenger = %q, want %q", got, "discord")
	}
	if s.statusTracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("cancelled Discord send was dead-lettered instead of staying queued")
	}
}

func TestWebhookFailureIsRetryableTreatsCancelAsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "raw context canceled", err: context.Canceled, want: true},
		{
			name: "transport error wrapping canceled",
			err:  webhookhttp.NewTransportError("slack", &url.Error{Err: context.Canceled}, "redacted"),
			want: true,
		},
		{
			name: "transport error wrapping deadline exceeded",
			err:  webhookhttp.NewTransportError("slack", &url.Error{Err: context.DeadlineExceeded}, "redacted"),
			want: true,
		},
		{
			name: "transport error wrapping x509 unknown authority",
			err:  webhookhttp.NewTransportError("slack", &url.Error{Err: &x509.UnknownAuthorityError{}}, "redacted"),
			want: false,
		},
		{
			name: "http status 404",
			err:  webhookhttp.NewHTTPStatusError("slack", http.StatusNotFound, "404 Not Found", "", nil),
			want: false,
		},
		{
			name: "http status 500",
			err:  webhookhttp.NewHTTPStatusError("slack", http.StatusInternalServerError, "500 Internal Server Error", "", nil),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := webhookFailureIsRetryable(tc.err); got != tc.want {
				t.Fatalf("webhookFailureIsRetryable(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// TestSlackCancelledRetryReplayStaysQueued guards the SECOND call site of
// webhookFailureIsRetryable. recordRetryQueueDeliveryFailure runs on the
// retry-replay path, which no other test drives with a cancelled context: with
// the guard removed there and left in place on the fresh-send path, the whole
// suite stays green. A shutdown that lands mid-replay must leave the item in the
// queue, not turn it into a dead letter.
func TestSlackCancelledRetryReplayStaysQueued(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
		cancel()
		<-release
	}))
	// LIFO: release the blocked handler before Close waits for it.
	defer server.Close()
	defer close(release)

	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: server.URL}

	entry := api.RansomwareEntry{ID: "replay-cancel-1", Group: "lockbit", Victim: "Example Replay", Country: "DE"}
	key := api.GenerateEntryKey(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(t, s.statusTracker, key, server.URL, "slack", "api", "Example Replay", "first failure", 5, time.Hour, payload) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	s.processAPIRetryQueue(ctx, nil)

	if requests != 1 {
		t.Fatalf("slack request count = %d, want 1", requests)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	retryItems := reloaded.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("persisted retry queue length = %d, want 1 (cancelled replay must stay queued)", len(retryItems))
	}
	if got := retryItems[0].ErrorCategory; got != "canceled" {
		t.Fatalf("persisted error_category = %q, want %q", got, "canceled")
	}
	if got := retryItems[0].LastError; got != "Webhook delivery canceled" {
		t.Fatalf("persisted last_error = %q, want %q", got, "Webhook delivery canceled")
	}
	// The replay attempt is consumed like any other: 1 from the seed enqueue,
	// 1 from the cancelled replay.
	if got := retryItems[0].RetryCount; got != 2 {
		t.Fatalf("persisted retry_count = %d, want 2", got)
	}
	if reloaded.IsRetryDeadLettered(key, "slack", "api") {
		t.Fatal("cancelled Slack retry replay was dead-lettered instead of staying queued")
	}
}

// TestSlackTLSFailureDeadLettersOnFirstAttempt pins the one behaviour change the
// operator accepted as a trade-off: an untrusted certificate on the webhook host
// is a permanent transport failure, so the item is dead-lettered immediately
// (terminal_failure, retry_count 0) instead of after retry_max_attempts+1 sends.
// It also pins the corrected last_error text:
// deliveryStatusMessage now checks *webhookhttp.TransportError before
// the generic net.Error arm, so a non-retryable transport failure like this one
// is named as terminal instead of claiming a retry that will never happen.
func TestSlackTLSFailureDeadLettersOnFirstAttempt(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: server.URL}

	entry := api.RansomwareEntry{ID: "tls-1", Group: "lockbit", Victim: "Example TLS", Country: "DE"}
	key := api.GenerateEntryKey(entry)
	s.sendAPIEntriesIndividuallyToSlack(context.Background(), []api.RansomwareEntry{entry}, server.URL)

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if items := reloaded.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("persisted retry queue length = %d, want 0 (an x509 failure is terminal)", len(items))
	}
	dead := reloaded.GetDeadLetterItems()
	if len(dead) != 1 {
		t.Fatalf("persisted dead-letter count = %d, want 1", len(dead))
	}
	if got := dead[0].ItemKey; got != key {
		t.Fatalf("dead-letter item_key = %q, want %q", got, key)
	}
	if got := dead[0].ErrorCategory; got != "permanent" {
		t.Fatalf("dead-letter error_category = %q, want %q", got, "permanent")
	}
	if got := dead[0].TerminalReason; got != status.TerminalReasonTerminalFailure {
		t.Fatalf("dead-letter terminal_reason = %q, want %q", got, status.TerminalReasonTerminalFailure)
	}
	if got := dead[0].RetryCount; got != 0 {
		t.Fatalf("dead-letter retry_count = %d, want 0 (dead-lettered before it was ever queued)", got)
	}
	// Terminal, accurate text: no longer claims a retry that will never happen.
	const wantLastError = "slack webhook connection failed permanently; verify the certificate and network path"
	if got := dead[0].LastError; got != wantLastError {
		t.Fatalf("dead-letter last_error = %q, want %q", got, wantLastError)
	}
	if strings.Contains(dead[0].LastError, server.URL) {
		t.Fatalf("dead-letter last_error leaked the webhook URL: %q", dead[0].LastError)
	}
}
