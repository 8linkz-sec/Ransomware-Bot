package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	testAPIAuthWarnMessage      = "Ransomware API polling suspended after authentication failure"
	testAPIAuthSkipMessage      = "API polling suspended after authentication failure"
	testAPIAuthReprobeMessage   = "Re-probing ransomware API after authentication suspension"
	testAPIAuthRecoveredMessage = "Ransomware API authentication recovered; polling resumed"
	testAPIAuthLiftsOn          = "api_key/api_base_url change, restart, or automatic re-probe"
)

// freezeAPIAuthClock replaces the package clock seam with a frozen clock and
// returns an advance function. The seam is restored by t.Cleanup.
func freezeAPIAuthClock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()

	var mu sync.Mutex
	current := start
	previous := apiAuthNow
	apiAuthNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	t.Cleanup(func() { apiAuthNow = previous })

	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(d)
	}
}

func setAPIAuthTestLogLevel(t *testing.T) {
	t.Helper()

	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previous) })
}

// countAPIAuthLogEntries counts hook entries by level and message; the shared
// findTestLogEntry helper returns only the first match and cannot count.
func countAPIAuthLogEntries(hook *logtest.Hook, level log.Level, message string) int {
	count := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == level && entry.Message == message {
			count++
		}
	}
	return count
}

func newAPIAuthTestScheduler(t *testing.T, baseURL string) *Scheduler {
	t.Helper()

	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T000/B000/test",
	}

	client, err := api.NewClientWithBaseURLAndPolicy("test-api-key", baseURL, api.HTTPPolicy{
		RequestTimeout: 5 * time.Second,
		MaxAttempts:    1,
		RetryBaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithBaseURLAndPolicy() error = %v", err)
	}
	s.apiClient = client
	return s
}

type apiAuthTestStatusFile struct {
	LastSuccess *time.Time `json:"last_success"`
	LastError   *string    `json:"last_error"`
}

func readAPIAuthTestStatusFile(t *testing.T, dataDir string) apiAuthTestStatusFile {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	var parsed apiAuthTestStatusFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal(api_status.json) error = %v; content=%s", err, data)
	}
	return parsed
}

func TestCheckAPIOnceWarnsOnceWhenAPIAuthSuspended(t *testing.T) {
	setAPIAuthTestLogLevel(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	frozen := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	freezeAPIAuthClock(t, frozen)

	apiRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiRequests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	for i := 0; i < 4; i++ {
		s.checkAPIOnce(t.Context())
	}

	if apiRequests != 1 {
		t.Fatalf("API request count = %d, want 1 while the auth suspension holds", apiRequests)
	}
	if got := countAPIAuthLogEntries(hook, log.WarnLevel, testAPIAuthWarnMessage); got != 1 {
		t.Fatalf("auth suspension WARN count = %d, want exactly 1 per arming", got)
	}

	entry := findTestLogEntry(hook, testAPIAuthWarnMessage)
	if entry == nil {
		t.Fatalf("missing WARN %q; entries=%d", testAPIAuthWarnMessage, len(hook.AllEntries()))
	}
	if got := entry.Data["status_code"]; got != http.StatusForbidden {
		t.Fatalf("status_code = %#v, want 403; fields=%#v", got, entry.Data)
	}
	if got := entry.Data["consecutive_auth_failures"]; got != 1 {
		t.Fatalf("consecutive_auth_failures = %#v, want 1; fields=%#v", got, entry.Data)
	}
	if got, want := entry.Data["suspended_since"], frozen.Format(time.RFC3339); got != want {
		t.Fatalf("suspended_since = %#v, want %q; fields=%#v", got, want, entry.Data)
	}
	if got, want := entry.Data["next_probe_at"], frozen.Add(apiAuthSuspensionBase).Format(time.RFC3339); got != want {
		t.Fatalf("next_probe_at = %#v, want %q; fields=%#v", got, want, entry.Data)
	}
	if got := entry.Data["lifts_on"]; got != testAPIAuthLiftsOn {
		t.Fatalf("lifts_on = %#v, want %q; fields=%#v", got, testAPIAuthLiftsOn, entry.Data)
	}
}

// TestCheckAPIOnceLogsDebugPerSkippedCycleWithoutLeakingKey pins the level of
// the skipped-cycle line (one DEBUG per skipped cycle, never WARN or INFO) and
// that neither it nor any other line of the suspended path carries the api_key.
func TestCheckAPIOnceLogsDebugPerSkippedCycleWithoutLeakingKey(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previous) })

	hook := logtest.NewGlobal()
	defer hook.Reset()

	frozen := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	freezeAPIAuthClock(t, frozen)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	for i := 0; i < 3; i++ {
		s.checkAPIOnce(t.Context())
	}

	skipped := 0
	for _, entry := range hook.AllEntries() {
		rendered := fmt.Sprintf("%s %v", entry.Message, entry.Data)
		if strings.Contains(rendered, "test-api-key") {
			t.Errorf("log entry leaks the api_key: [%v] %s", entry.Level, rendered)
		}
		if entry.Message != testAPIAuthSkipMessage {
			continue
		}
		skipped++
		if entry.Level != log.DebugLevel {
			t.Errorf("skipped-cycle entry level = %v, want debug; fields=%#v", entry.Level, entry.Data)
		}
		if got := entry.Data["status_code"]; got != http.StatusUnauthorized {
			t.Errorf("skipped-cycle status_code = %#v, want 401; fields=%#v", got, entry.Data)
		}
	}
	if skipped != 2 {
		t.Fatalf("skipped-cycle DEBUG count = %d, want 2 (one per skipped cycle)", skipped)
	}
}

// TestCheckAPIOnceDoesNotLogRecoveryWithoutSuspension pins that the recovery
// INFO is bound to a lifted suspension and is not emitted on every ordinary
// successful poll.
func TestCheckAPIOnceDoesNotLogRecoveryWithoutSuspension(t *testing.T) {
	setAPIAuthTestLogLevel(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	freezeAPIAuthClock(t, time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"victims":[]}`))
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	for i := 0; i < 2; i++ {
		s.checkAPIOnce(t.Context())
	}

	if got := countAPIAuthLogEntries(hook, log.InfoLevel, testAPIAuthRecoveredMessage); got != 0 {
		t.Fatalf("recovery INFO count = %d, want 0 when no suspension was ever armed", got)
	}
}

func TestCheckAPIOnceReprobesAfterAuthSuspensionInterval(t *testing.T) {
	setAPIAuthTestLogLevel(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	frozen := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	advance := freezeAPIAuthClock(t, frozen)

	apiRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiRequests++
		if apiRequests == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"victims":[]}`))
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	s.checkAPIOnce(t.Context())
	advance(16 * time.Minute)
	s.checkAPIOnce(t.Context())

	if apiRequests != 2 {
		t.Fatalf("API request count = %d, want 2 after the re-probe interval elapsed", apiRequests)
	}
	if entry := findTestLogEntry(hook, testAPIAuthRecoveredMessage); entry == nil {
		t.Fatalf("missing INFO %q after successful re-probe", testAPIAuthRecoveredMessage)
	} else if entry.Level != log.InfoLevel {
		t.Fatalf("recovery entry level = %v, want info", entry.Level)
	}
	if s.apiAuthSuspension != (apiAuthSuspension{}) {
		t.Fatalf("apiAuthSuspension = %#v, want zero value after recovery", s.apiAuthSuspension)
	}

	parsed := readAPIAuthTestStatusFile(t, s.config.DataDir)
	if parsed.LastError != nil {
		t.Fatalf("api_status.json last_error = %q, want null after recovery", *parsed.LastError)
	}
	if parsed.LastSuccess == nil {
		t.Fatal("api_status.json last_success = null, want a timestamp after recovery")
	}
}

func TestCheckAPIOnceRearmsAuthSuspensionAfterFailedReprobe(t *testing.T) {
	setAPIAuthTestLogLevel(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	frozen := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	advance := freezeAPIAuthClock(t, frozen)

	apiRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiRequests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	s.checkAPIOnce(t.Context())
	advance(16 * time.Minute)
	s.checkAPIOnce(t.Context())

	if apiRequests != 2 {
		t.Fatalf("API request count = %d, want 2 after the re-probe interval elapsed", apiRequests)
	}
	if got := s.apiAuthSuspension.failures; got != 2 {
		t.Fatalf("consecutive failures = %d, want 2", got)
	}
	wantNextProbe := frozen.Add(16 * time.Minute).Add(30 * time.Minute)
	if got := s.apiAuthSuspension.nextProbe; !got.Equal(wantNextProbe) {
		t.Fatalf("nextProbe = %s, want %s", got.Format(time.RFC3339), wantNextProbe.Format(time.RFC3339))
	}
	if got := s.apiAuthSuspension.since; !got.Equal(frozen) {
		t.Fatalf("since = %s, want unchanged %s", got.Format(time.RFC3339), frozen.Format(time.RFC3339))
	}
	if got := countAPIAuthLogEntries(hook, log.WarnLevel, testAPIAuthWarnMessage); got != 2 {
		t.Fatalf("auth suspension WARN count = %d, want 2 (one per arming)", got)
	}
}

func TestCheckAPIOnceClearsAuthSuspensionOnNonAuthReprobeFailure(t *testing.T) {
	setAPIAuthTestLogLevel(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	frozen := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	advance := freezeAPIAuthClock(t, frozen)

	apiRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiRequests++
		if apiRequests == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	s := newAPIAuthTestScheduler(t, server.URL)

	s.checkAPIOnce(t.Context())
	advance(16 * time.Minute)
	for i := 0; i < 3; i++ {
		s.checkAPIOnce(t.Context())
	}

	if s.apiAuthSuspension != (apiAuthSuspension{}) {
		t.Fatalf("apiAuthSuspension = %#v, want zero value after a non-auth re-probe failure", s.apiAuthSuspension)
	}
	if got := countAPIAuthLogEntries(hook, log.InfoLevel, testAPIAuthReprobeMessage); got != 1 {
		t.Fatalf("re-probe INFO count = %d, want exactly 1", got)
	}
}

func TestNextAPIAuthProbeAfterBackoff(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{name: "first failure", failures: 1, want: 15 * time.Minute},
		{name: "second failure", failures: 2, want: 30 * time.Minute},
		{name: "third failure", failures: 3, want: time.Hour},
		{name: "fourth failure", failures: 4, want: 2 * time.Hour},
		{name: "fifth failure", failures: 5, want: 4 * time.Hour},
		{name: "sixth failure caps", failures: 6, want: 6 * time.Hour},
		{name: "seventh failure stays capped", failures: 7, want: 6 * time.Hour},
		{name: "far beyond the cap", failures: 99, want: 6 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextAPIAuthProbeAfter(now, tt.failures)
			if want := now.Add(tt.want); !got.Equal(want) {
				t.Fatalf("nextAPIAuthProbeAfter(now, %d) = %s, want %s",
					tt.failures, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestAPIAuthStatusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil error", err: nil, want: 0},
		{name: "plain error", err: errors.New("connection reset"), want: 0},
		{
			name: "http status error",
			err:  &api.HTTPStatusError{StatusCode: http.StatusForbidden, Status: "403 Forbidden"},
			want: http.StatusForbidden,
		},
		{
			name: "wrapped http status error",
			err:  fmt.Errorf("fetch failed: %w", &api.HTTPStatusError{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}),
			want: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiAuthStatusCode(tt.err); got != tt.want {
				t.Fatalf("apiAuthStatusCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestReloadConfigClearsAPIAuthSuspensionOnAPIKeyChange(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantClear bool
	}{
		{
			name:      "api_key change",
			mutate:    func(cfg *config.Config) { cfg.APIKey = "new-key" },
			wantClear: true,
		},
		{
			name:      "api_base_url change only",
			mutate:    func(cfg *config.Config) { cfg.APIBaseURL = "https://api.ransomware.test" },
			wantClear: true,
		},
		{
			// The API client is rebuilt for this change too, but the credentials
			// and the endpoint are unchanged, so the latch must survive it.
			name:      "api_request_timeout change keeps the latch",
			mutate:    func(cfg *config.Config) { cfg.APIRequestTimeout = 42 * time.Second },
			wantClear: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.APIKey = "old-key"
			cfg.DataDir = t.TempDir()

			newCfg := config.DefaultConfig()
			newCfg.APIKey = "old-key"
			tt.mutate(newCfg)

			deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
			s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
			if err != nil {
				t.Fatalf("NewWithDependencies() error = %v", err)
			}
			t.Cleanup(func() {
				if err := s.Stop(); err != nil {
					t.Errorf("Stop() error = %v", err)
				}
			})

			suspendedSince := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
			armed := apiAuthSuspension{
				key:        "old-key",
				since:      suspendedSince,
				nextProbe:  suspendedSince.Add(30 * time.Minute),
				failures:   2,
				statusCode: http.StatusUnauthorized,
			}
			s.apiAuthSuspension = armed

			changed, err := s.ReloadConfig()
			if err != nil {
				t.Fatalf("ReloadConfig() error = %v", err)
			}
			if !changed {
				t.Fatal("ReloadConfig() changed = false, want true")
			}
			if tt.wantClear {
				if s.apiAuthSuspension != (apiAuthSuspension{}) {
					t.Fatalf("apiAuthSuspension = %#v, want zero value after an api_key/api_base_url change", s.apiAuthSuspension)
				}
				return
			}
			if s.apiAuthSuspension != armed {
				t.Fatalf("apiAuthSuspension = %#v, want it unchanged (%#v) when neither api_key nor api_base_url changed",
					s.apiAuthSuspension, armed)
			}
		})
	}
}
