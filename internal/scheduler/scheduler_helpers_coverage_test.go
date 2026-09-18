package scheduler

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

// fakeNetError implements net.Error for classification tests.
type fakeNetError struct {
	msg     string
	timeout bool
}

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return false }

func TestAppendDestinationSuffixVariants(t *testing.T) {
	if got := appendDestinationSuffix("discord.ransomware", ""); got != "discord.ransomware" {
		t.Fatalf("appendDestinationSuffix(empty) = %q, want base unchanged", got)
	}
	if got := appendDestinationSuffix("discord.ransomware", "  "); got != "discord.ransomware" {
		t.Fatalf("appendDestinationSuffix(blank) = %q, want base unchanged", got)
	}
	if got := appendDestinationSuffix("discord.ransomware", " 2 "); got != "discord.ransomware.2" {
		t.Fatalf("appendDestinationSuffix(suffix) = %q, want discord.ransomware.2", got)
	}
}

func TestMessengerForWebhookPlatformUnknown(t *testing.T) {
	messenger, ok := messengerForWebhookPlatform("matrix")
	if ok {
		t.Fatalf("messengerForWebhookPlatform(matrix) ok = true, want false")
	}
	if messenger.String() != "" {
		t.Fatalf("messengerForWebhookPlatform(matrix) messenger = %q, want empty", messenger.String())
	}
}

func TestStatusRetentionPolicyForConfigNilUsesDefaults(t *testing.T) {
	got := statusRetentionPolicyForConfig(nil)
	want := status.DefaultRetentionPolicy()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statusRetentionPolicyForConfig(nil) = %#v, want defaults %#v", got, want)
	}
}

// TestStatusRetentionPolicyForConfigMapsAuditLogRotation pins the full
// config-to-runtime mapping in statusRetentionPolicyForConfig: it is
// straight-line code with no branches on the non-nil path, so line/statement
// coverage alone (100% via the nil-cfg test above) proves nothing about
// whether each field actually reached its matching output field. Non-default
// values are used deliberately so a mutation that substitutes a zero or a
// literal true/false is visible instead of accidentally matching the
// package defaults (60/20/365/true).
func TestStatusRetentionPolicyForConfigMapsAuditLogRotation(t *testing.T) {
	tests := []struct {
		name     string
		compress bool
	}{
		{name: "compress true", compress: true},
		{name: "compress false", compress: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				StatusRetention: config.StatusRetentionConfig{
					AuditLogRotation: config.AuditLogRotationConfig{
						MaxSizeMB:  5,
						MaxBackups: 3,
						MaxAgeDays: 7,
						Compress:   tt.compress,
					},
				},
			}

			got := statusRetentionPolicyForConfig(cfg)

			if got.AuditLogMaxSizeMB != 5 {
				t.Fatalf("AuditLogMaxSizeMB = %d, want 5", got.AuditLogMaxSizeMB)
			}
			if got.AuditLogMaxBackups != 3 {
				t.Fatalf("AuditLogMaxBackups = %d, want 3", got.AuditLogMaxBackups)
			}
			if got.AuditLogMaxAgeDays != 7 {
				t.Fatalf("AuditLogMaxAgeDays = %d, want 7", got.AuditLogMaxAgeDays)
			}
			if got.AuditLogCompress == nil {
				t.Fatal("AuditLogCompress = nil, want a non-nil *bool")
			}
			if *got.AuditLogCompress != tt.compress {
				t.Fatalf("*AuditLogCompress = %v, want %v", *got.AuditLogCompress, tt.compress)
			}
		})
	}
}

func TestActiveRSSFeedURLsNilConfig(t *testing.T) {
	if got := activeRSSFeedURLs(nil); got != nil {
		t.Fatalf("activeRSSFeedURLs(nil) = %#v, want nil", got)
	}
}

func TestRSSFeedRoutesNilConfigYieldsEmptyRoutes(t *testing.T) {
	routes := rssFeedRoutes(nil)
	if len(routes) != len(rssFeedRouteDefinitions) {
		t.Fatalf("rssFeedRoutes(nil) route count = %d, want %d", len(routes), len(rssFeedRouteDefinitions))
	}
	for _, route := range routes {
		if route.feedURLs != nil {
			t.Fatalf("rssFeedRoutes(nil) route %q feedURLs = %#v, want nil", route.feedType, route.feedURLs)
		}
	}
}

func TestConfigReloadAuditEventsNilConfigs(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := configReloadAuditEvents(nil, cfg); got != nil {
		t.Fatalf("configReloadAuditEvents(nil, cfg) = %#v, want nil", got)
	}
	if got := configReloadAuditEvents(cfg, nil); got != nil {
		t.Fatalf("configReloadAuditEvents(cfg, nil) = %#v, want nil", got)
	}
}

func TestSafeHashPrefixFallsBackOnMarshalError(t *testing.T) {
	got := safeHashPrefix(make(chan int))
	if len(got) != 12 {
		t.Fatalf("safeHashPrefix(chan) length = %d, want 12", len(got))
	}
	if other := safeHashPrefix(make(chan struct{})); other != got {
		t.Fatalf("safeHashPrefix marshal-error hashes differ: %q vs %q", got, other)
	}
	if plain := safeHashPrefix("value"); plain == got {
		t.Fatal("safeHashPrefix marshal-error hash equals hash of plain value")
	}
}

func TestIsAPIAuthErrorNonHTTPStatusError(t *testing.T) {
	if isAPIAuthError(errors.New("plain failure")) {
		t.Fatal("isAPIAuthError(plain error) = true, want false")
	}
	if !isAPIAuthError(&api.HTTPStatusError{StatusCode: http.StatusForbidden}) {
		t.Fatal("isAPIAuthError(403) = false, want true")
	}
}

func TestAPISourceErrorInfoClassifications(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCategory  string
		wantStatus    int
		wantRetryable *bool
		wantTimeout   *bool
	}{
		{name: "nil error", err: nil, wantCategory: "", wantRetryable: boolPtr(true), wantTimeout: boolPtr(false)},
		{name: "canceled", err: context.Canceled, wantCategory: "canceled", wantRetryable: boolPtr(false)},
		{name: "deadline", err: context.DeadlineExceeded, wantCategory: "timeout", wantTimeout: boolPtr(true)},
		{
			name:         "rate limit cooldown",
			err:          &api.RateLimitCooldownError{Until: time.Now().Add(time.Minute)},
			wantCategory: "rate_limited",
			wantStatus:   http.StatusTooManyRequests,
		},
		{
			name:          "server error status",
			err:           &api.HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Status: "503"},
			wantCategory:  "provider_unavailable",
			wantStatus:    http.StatusServiceUnavailable,
			wantRetryable: boolPtr(true),
		},
		{
			name:          "not found status",
			err:           &api.HTTPStatusError{StatusCode: http.StatusNotFound, Status: "404"},
			wantCategory:  "not_found",
			wantStatus:    http.StatusNotFound,
			wantRetryable: boolPtr(false),
		},
		{name: "net timeout", err: &fakeNetError{msg: "i/o timeout", timeout: true}, wantCategory: "timeout", wantTimeout: boolPtr(true)},
		{name: "decode failure", err: errors.New("failed to decode response body"), wantCategory: "decode_error"},
		{name: "generic network", err: errors.New("connection reset"), wantCategory: "network"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := apiSourceErrorInfo(tt.err)
			if info.ErrorCategory != tt.wantCategory {
				t.Fatalf("ErrorCategory = %q, want %q", info.ErrorCategory, tt.wantCategory)
			}
			if info.StatusCode != tt.wantStatus {
				t.Fatalf("StatusCode = %d, want %d", info.StatusCode, tt.wantStatus)
			}
			if tt.wantRetryable != nil {
				if info.Retryable == nil || *info.Retryable != *tt.wantRetryable {
					t.Fatalf("Retryable = %v, want %v", info.Retryable, *tt.wantRetryable)
				}
			}
			if tt.wantTimeout != nil {
				if info.Timeout == nil || *info.Timeout != *tt.wantTimeout {
					t.Fatalf("Timeout = %v, want %v", info.Timeout, *tt.wantTimeout)
				}
			}
		})
	}
}

func TestAPIHTTPErrorCategoryTable(t *testing.T) {
	tests := map[int]string{
		http.StatusTooManyRequests:     "rate_limited",
		http.StatusUnauthorized:        "authentication",
		http.StatusForbidden:           "authentication",
		http.StatusNotFound:            "not_found",
		http.StatusTeapot:              "request_rejected",
		http.StatusInternalServerError: "provider_unavailable",
		http.StatusFound:               "http_status",
	}
	for statusCode, want := range tests {
		if got := apiHTTPErrorCategory(statusCode); got != want {
			t.Fatalf("apiHTTPErrorCategory(%d) = %q, want %q", statusCode, got, want)
		}
	}
}

func TestRSSSourceErrorInfoNilError(t *testing.T) {
	if got := rssSourceErrorInfo(nil); !reflect.DeepEqual(got, status.SourceErrorInfo{}) {
		t.Fatalf("rssSourceErrorInfo(nil) = %#v, want zero value", got)
	}
}

func TestRSSSourceErrorInfoFromStringClassifications(t *testing.T) {
	tests := []struct {
		name          string
		message       string
		wantCategory  string
		wantStatus    int
		wantRetryable *bool
	}{
		{name: "timeout", message: "request timeout while fetching", wantCategory: "timeout"},
		{name: "rate limit", message: "provider rate limit exceeded", wantCategory: "rate_limited", wantStatus: http.StatusTooManyRequests},
		{name: "client status", message: "server returned status 403", wantCategory: "request_rejected", wantRetryable: boolPtr(false)},
		{name: "server status", message: "server returned status 502", wantCategory: "provider_unavailable", wantRetryable: boolPtr(true)},
		{name: "parse failure", message: "malformed feed could not parse", wantCategory: "parse_error", wantRetryable: boolPtr(false)},
		{name: "validation", message: "feed url blocked by policy", wantCategory: "validation", wantRetryable: boolPtr(false)},
		{name: "generic", message: "mysterious failure", wantCategory: "fetch", wantRetryable: boolPtr(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := rssSourceErrorInfoFromString(tt.message)
			if info.ErrorCategory != tt.wantCategory {
				t.Fatalf("ErrorCategory = %q, want %q", info.ErrorCategory, tt.wantCategory)
			}
			if info.StatusCode != tt.wantStatus {
				t.Fatalf("StatusCode = %d, want %d", info.StatusCode, tt.wantStatus)
			}
			if tt.wantRetryable != nil {
				if info.Retryable == nil || *info.Retryable != *tt.wantRetryable {
					t.Fatalf("Retryable = %v, want %v", info.Retryable, *tt.wantRetryable)
				}
			}
		})
	}
}

func TestDeliveryStatusMessageContextAndNetworkErrors(t *testing.T) {
	if got := deliveryStatusMessage("discord", nil); got != "" {
		t.Fatalf("deliveryStatusMessage(nil error) = %q, want empty", got)
	}
	if got := deliveryStatusMessage("", errors.New("some failure")); got != "webhook webhook delivery failed; check webhook configuration and provider availability" {
		t.Fatalf("deliveryStatusMessage(empty messenger) = %q, want generic webhook fallback", got)
	}
	if got := deliveryStatusMessage("discord", context.Canceled); got != "Webhook delivery canceled" {
		t.Fatalf("deliveryStatusMessage(canceled) = %q", got)
	}
	wantTimeout := "Webhook delivery timed out; provider or network did not respond before the deadline"
	if got := deliveryStatusMessage("discord", context.DeadlineExceeded); got != wantTimeout {
		t.Fatalf("deliveryStatusMessage(deadline) = %q", got)
	}
	if got := deliveryStatusMessage("slack", &fakeNetError{msg: "i/o timeout", timeout: true}); got != wantTimeout {
		t.Fatalf("deliveryStatusMessage(net timeout) = %q", got)
	}
	if got := deliveryStatusMessage("slack", &fakeNetError{msg: "connection refused"}); got != "slack webhook provider unavailable" {
		t.Fatalf("deliveryStatusMessage(net non-timeout) = %q", got)
	}
}

func TestDeliveryStatusMessageForHTTPStatusTable(t *testing.T) {
	tests := map[int]string{
		http.StatusTooManyRequests:     "slack webhook rate limited delivery",
		http.StatusUnauthorized:        "slack webhook rejected delivery; verify or rotate the webhook URL",
		http.StatusPaymentRequired:     "slack webhook rejected the message payload; check formatting configuration",
		http.StatusInternalServerError: "slack webhook provider unavailable",
		http.StatusFound:               "slack webhook delivery failed; check webhook configuration and provider availability",
	}
	for statusCode, want := range tests {
		if got := deliveryStatusMessageForHTTPStatus("slack", statusCode); got != want {
			t.Fatalf("deliveryStatusMessageForHTTPStatus(%d) = %q, want %q", statusCode, got, want)
		}
	}
}

func TestWebhookRetryErrorInfoContextErrors(t *testing.T) {
	info := webhookRetryErrorInfo(nil)
	if info.ErrorCategory != "" {
		t.Fatalf("webhookRetryErrorInfo(nil) category = %q, want empty", info.ErrorCategory)
	}
	if info.Retryable == nil || *info.Retryable {
		t.Fatalf("webhookRetryErrorInfo(nil) retryable = %v, want false", info.Retryable)
	}

	if got := webhookRetryErrorInfo(context.Canceled); got.ErrorCategory != "canceled" {
		t.Fatalf("webhookRetryErrorInfo(canceled) category = %q, want canceled", got.ErrorCategory)
	}
	if got := webhookRetryErrorInfo(context.DeadlineExceeded); got.ErrorCategory != "timeout" {
		t.Fatalf("webhookRetryErrorInfo(deadline) category = %q, want timeout", got.ErrorCategory)
	}
}

func TestWebhookHTTPErrorCategoryTable(t *testing.T) {
	tests := map[int]string{
		http.StatusTooManyRequests:     "rate_limited",
		http.StatusUnauthorized:        "invalid_webhook",
		http.StatusNotFound:            "invalid_webhook",
		http.StatusPaymentRequired:     "payload_rejected",
		http.StatusInternalServerError: "provider_unavailable",
		http.StatusFound:               "http_status",
	}
	for statusCode, want := range tests {
		if got := webhookHTTPErrorCategory(statusCode); got != want {
			t.Fatalf("webhookHTTPErrorCategory(%d) = %q, want %q", statusCode, got, want)
		}
	}
}

func TestWebhookFailureFieldsCreatesFieldsWhenNil(t *testing.T) {
	fields := webhookFailureFields("slack", &webhookhttp.HTTPStatusError{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"}, nil)
	if fields == nil {
		t.Fatal("webhookFailureFields(nil fields) returned nil map")
	}
	if fields["error"] == "" {
		t.Fatalf("webhookFailureFields missing error message: %#v", fields)
	}
	if got := fields["status_code"]; got != http.StatusBadGateway {
		t.Fatalf("webhookFailureFields status_code = %#v, want 502", got)
	}
}

func TestWebhookProgressFieldsClampsBounds(t *testing.T) {
	fields := webhookProgressFields(-1, 0, "discord.ransomware")
	if got := fields["current"]; got != 1 {
		t.Fatalf("current = %#v, want 1", got)
	}
	if got := fields["total"]; got != 1 {
		t.Fatalf("total = %#v, want 1", got)
	}
	if got := fields["remaining"]; got != 0 {
		t.Fatalf("remaining = %#v, want 0", got)
	}
	if got := fields["percent_complete"]; got != 100 {
		t.Fatalf("percent_complete = %#v, want 100", got)
	}
}

func TestQuietHoursAuditDetailsNilPolicy(t *testing.T) {
	if got := quietHoursAuditDetails(nil); got != nil {
		t.Fatalf("quietHoursAuditDetails(nil) = %#v, want nil", got)
	}
}

func TestAddDryRunPreviewHelpers(t *testing.T) {
	cfg := config.DefaultConfig()
	opts := config.NotificationFormatOptions(&cfg.Format)
	apiEntry := api.RansomwareEntry{
		ID:     "preview-1",
		Group:  "lockbit",
		Victim: "Preview Corp",
	}
	rssEntry := rss.Entry{
		Title:     "Preview RSS",
		Link:      "https://example.test/preview",
		FeedTitle: "Preview Feed",
		FeedURL:   "https://example.test/feed.xml",
		Published: time.Now().UTC(),
	}

	unknownFields := map[string]any{}
	addDryRunRansomwarePreview(unknownFields, "matrix", apiEntry, opts)
	addDryRunRSSPreview(unknownFields, "matrix", rssEntry, opts, config.FeedTypeGeneral)
	if len(unknownFields) != 0 {
		t.Fatalf("unknown messenger previews mutated fields: %#v", unknownFields)
	}

	for _, messenger := range []string{
		status.MessengerSlack.String(),
		status.MessengerSlackCompatible.String(),
	} {
		fields := map[string]any{}
		addDryRunRSSPreview(fields, messenger, rssEntry, opts, config.FeedTypeGeneral)
		if fields["rendered_preview"] == nil {
			t.Fatalf("addDryRunRSSPreview(%s) did not attach rendered_preview: %#v", messenger, fields)
		}
	}

	emptyPreviewFields := map[string]any{}
	addDryRunPreviewFields(emptyPreviewFields, map[string]any{})
	if len(emptyPreviewFields) != 0 {
		t.Fatalf("addDryRunPreviewFields(empty preview) mutated fields: %#v", emptyPreviewFields)
	}
}

func TestFirstPreviewBlockTextAndPreviewText(t *testing.T) {
	if got := firstPreviewBlockText(nil); got != "" {
		t.Fatalf("firstPreviewBlockText(nil) = %q, want empty", got)
	}

	fieldBlocks := []map[string]any{
		{"type": "divider"},
		{"fields": []map[string]any{
			{"type": "mrkdwn"},
			{"text": "field text"},
		}},
	}
	if got := firstPreviewBlockText(fieldBlocks); got != "field text" {
		t.Fatalf("firstPreviewBlockText(field blocks) = %q, want %q", got, "field text")
	}

	textBlocks := []map[string]any{
		{"text": map[string]any{"text": "block text"}},
	}
	if got := firstPreviewBlockText(textBlocks); got != "block text" {
		t.Fatalf("firstPreviewBlockText(text blocks) = %q, want %q", got, "block text")
	}

	if got := previewText("not a map"); got != "" {
		t.Fatalf("previewText(non-map) = %q, want empty", got)
	}
	if got := previewText(map[string]any{"text": 42}); got != "" {
		t.Fatalf("previewText(non-string text) = %q, want empty", got)
	}
	if got := previewText(map[string]any{"text": "ok"}); got != "ok" {
		t.Fatalf("previewText(valid) = %q, want ok", got)
	}
}

func TestWaitForSendDelayCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitForSendDelay(ctx, time.Hour) {
		t.Fatal("waitForSendDelay(cancelled ctx) = true, want false")
	}
	if !waitForSendDelay(ctx, 0) {
		t.Fatal("waitForSendDelay(zero delay) = false, want true")
	}
}

func TestWaitForSendDelayCompletesElapsedDelay(t *testing.T) {
	if !waitForSendDelay(t.Context(), time.Millisecond) {
		t.Fatal("waitForSendDelay(elapsed delay) = false, want true")
	}
}

func TestWebhookURLsFromTargetsSkipsEmptyURLs(t *testing.T) {
	targets := []webhookTarget{
		{url: "", destinationID: "discord.rss.general"},
		{url: "https://hooks.example.test/a", destinationID: "slack.rss.general"},
	}
	got := webhookURLsFromTargets(targets)
	if !reflect.DeepEqual(got, []string{"https://hooks.example.test/a"}) {
		t.Fatalf("webhookURLsFromTargets = %#v, want single non-empty URL", got)
	}
}

func TestFeedResultsForURLsHandlesNilSourceAndErrorOnlyFeeds(t *testing.T) {
	nilResults := feedResultsForURLs(nil, []string{"https://feed.test/a"})
	if nilResults == nil || len(nilResults.Entries) != 0 || len(nilResults.FeedErrors) != 0 {
		t.Fatalf("feedResultsForURLs(nil source) = %#v, want empty results", nilResults)
	}

	source := &rss.FeedResults{
		Entries:         map[string][]rss.Entry{},
		FeedErrors:      map[string]string{"https://feed.test/broken": "fetch failed"},
		FeedValidators:  map[string]rss.FeedHTTPValidators{},
		FeedNotModified: map[string]bool{},
	}
	results := feedResultsForURLs(source, []string{"https://feed.test/broken"})
	if got := results.FeedErrors["https://feed.test/broken"]; got != "fetch failed" {
		t.Fatalf("FeedErrors = %q, want fetch failed", got)
	}
	entries, exists := results.Entries["https://feed.test/broken"]
	if !exists || len(entries) != 0 {
		t.Fatalf("Entries for failed feed = %#v (exists=%v), want empty slice", entries, exists)
	}
}

func TestStringSetFromSliceSkipsEmptyValues(t *testing.T) {
	if got := stringSetFromSlice(nil); got != nil {
		t.Fatalf("stringSetFromSlice(nil) = %#v, want nil", got)
	}
	got := stringSetFromSlice([]string{"", "https://feed.test/a"})
	if len(got) != 1 {
		t.Fatalf("stringSetFromSlice length = %d, want 1", len(got))
	}
	if _, ok := got["https://feed.test/a"]; !ok {
		t.Fatalf("stringSetFromSlice missing URL: %#v", got)
	}
}

func TestMarkRSSDeliveryAttemptsForEntriesNilSetIsNoOp(t *testing.T) {
	entries := []rss.Entry{{
		Title:   "Attempt",
		GUID:    "attempt-guid",
		FeedURL: "https://feed.test/a",
	}}
	markRSSDeliveryAttemptsForEntries(entries, "discord.rss.general", nil)

	attempts := rssDeliveryAttemptSet{}
	markRSSDeliveryAttemptsForEntries(nil, "discord.rss.general", attempts)
	if len(attempts) != 0 {
		t.Fatalf("markRSSDeliveryAttemptsForEntries(no entries) mutated set: %#v", attempts)
	}
}

func TestRSSDeliveryModeMessages(t *testing.T) {
	tests := []struct {
		name         string
		fn           func(rssDeliveryMode) string
		wantFresh    string
		wantRecovery string
	}{
		{
			name:         "cancel",
			fn:           rssDeliveryCancelMessage,
			wantFresh:    "Context cancelled, stopping RSS entry send",
			wantRecovery: "Context cancelled, stopping RSS send",
		},
		{
			name:         "delay cancel",
			fn:           rssDeliveryDelayCancelMessage,
			wantFresh:    "Context cancelled during RSS send delay",
			wantRecovery: "Context cancelled during RSS recovery send delay",
		},
		{
			name:         "filtered",
			fn:           rssDeliveryFilteredMessage,
			wantFresh:    "RSS entry filtered out by webhook rules",
			wantRecovery: "RSS recovery entry filtered out by webhook rules",
		},
		{
			name:         "missing sender",
			fn:           rssDeliveryMissingSenderMessage,
			wantFresh:    "Missing RSS webhook sender",
			wantRecovery: "Missing RSS recovery webhook sender",
		},
		{
			name:         "progress",
			fn:           rssDeliveryProgressMessage,
			wantFresh:    "Sending RSS item to webhook",
			wantRecovery: "Sending RSS recovery item to webhook",
		},
		{
			name:         "stale",
			fn:           rssDeliveryStaleMessage,
			wantFresh:    "Skipping stale RSS entry without marking it sent",
			wantRecovery: "Skipping stale RSS entry in recovery without marking it sent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn(rssDeliveryModeFresh); got != tt.wantFresh {
				t.Fatalf("%s fresh message = %q, want %q", tt.name, got, tt.wantFresh)
			}
			if got := tt.fn(rssDeliveryModeRecovery); got != tt.wantRecovery {
				t.Fatalf("%s recovery message = %q, want %q", tt.name, got, tt.wantRecovery)
			}
		})
	}
}

func TestAPITargetLogIDFallsBackToURL(t *testing.T) {
	if got := apiTargetLogID(apiDeliveryTarget{destinationID: "discord.ransomware", url: "https://x"}); got != "discord.ransomware" {
		t.Fatalf("apiTargetLogID(destination set) = %q, want destination ID", got)
	}
	if got := apiTargetLogID(apiDeliveryTarget{url: "https://hooks.example.test/a"}); got != "https://hooks.example.test/a" {
		t.Fatalf("apiTargetLogID(no destination) = %q, want URL", got)
	}
}

func TestRetryEntryFromPayloadVariants(t *testing.T) {
	if _, reason, err := retryEntryFromPayload(status.RetryRecord{}); err == nil || reason != "missing API retry payload" {
		t.Fatalf("retryEntryFromPayload(no payload) reason = %q, err = %v", reason, err)
	}

	_, reason, err := retryEntryFromPayload(status.RetryRecord{
		Payload:        []byte(`{}`),
		PayloadVersion: "future_version_v9",
	})
	if err == nil || reason != "unsupported API retry payload version" {
		t.Fatalf("retryEntryFromPayload(unsupported version) reason = %q, err = %v", reason, err)
	}

	if _, reason, err := retryEntryFromPayload(status.RetryRecord{Payload: []byte(`{invalid`)}); err == nil || reason != "malformed API retry payload" {
		t.Fatalf("retryEntryFromPayload(malformed) reason = %q, err = %v", reason, err)
	}

	entry, reason, err := retryEntryFromPayload(status.RetryRecord{
		Payload:        []byte(`{"id":"replay-1","group":"lockbit"}`),
		PayloadVersion: status.RetryPayloadVersionRansomwareEntryV1,
	})
	if err != nil || reason != "" {
		t.Fatalf("retryEntryFromPayload(valid) reason = %q, err = %v", reason, err)
	}
	if entry.ID != "replay-1" || entry.Group != "lockbit" {
		t.Fatalf("retryEntryFromPayload(valid) entry = %#v", entry)
	}
}
