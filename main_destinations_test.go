package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/i18n"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	destinationWebhookA = "https://discord.com/api/webhooks/123456789012345678/secretAAAABBBBCCCC012345"
	destinationWebhookB = "https://discord.com/api/webhooks/876543210987654321/secretZZZZYYYYXXXX543210"
	destinationWebhookC = "https://discord.com/api/webhooks/555555555555555555/secretMMMMNNNNOOOO555555"
)

// destinationManifestConfig returns a config whose discord ransomware block owns
// the given endpoints in order.
func destinationManifestConfig(dataDir string, urls ...string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.APIKey = "live-api-key-1234567890"
	cfg.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URLs: urls}
	return cfg
}

func seedDestinationManifest(t *testing.T, cfg *config.Config) []byte {
	t.Helper()

	if err := status.WriteDestinationManifest(cfg.DataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
	seeded, err := os.ReadFile(status.DestinationManifestPath(cfg.DataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	return seeded
}

// captureDestinationLog routes the global logger into a buffer for the duration
// of the test and returns the buffer plus the hook that recorded the entries.
func captureDestinationLog(t *testing.T) (*bytes.Buffer, *logtest.Hook) {
	t.Helper()

	preserveGlobalLoggerState(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.DebugLevel)
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)
	return &logged, hook
}

func TestApplyDestinationManifestRefusesRemap(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		want   string
	}{
		{name: "english", locale: "en", want: "Destination remap detected (endpoints reordered)"},
		{name: "german", locale: "de", want: "Ziel-Neuzuordnung erkannt (Endpunkte umsortiert)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logged, hook := captureDestinationLog(t)

			dataDir := t.TempDir()
			seeded := seedDestinationManifest(t, destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB))
			cfg := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)

			var out bytes.Buffer
			code := applyDestinationManifest(cfg, cliOptions{locale: tc.locale}, &out, i18n.ForLocale(tc.locale))
			if code != 1 {
				t.Fatalf("applyDestinationManifest() = %d, want 1; out=%q", code, out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("stdout = %q, want it to contain %q", out.String(), tc.want)
			}
			if !strings.Contains(out.String(), "discord.ransomware<-discord.ransomware.2") {
				t.Fatalf("stdout = %q, want the movement list", out.String())
			}
			if !strings.Contains(out.String(), status.DestinationManifestPath(dataDir)) {
				t.Fatalf("stdout = %q, want the manifest path", out.String())
			}

			entry := findRootTestLogEntry(hook, "Refusing to start: webhook endpoints were remapped onto existing destination IDs")
			if entry == nil {
				t.Fatalf("missing refusal entry; entries=%v", rootTestLogMessages(hook))
			}
			if entry.Level != log.ErrorLevel {
				t.Fatalf("refusal level = %v, want error", entry.Level)
			}
			for _, field := range []string{
				"change", "affected_destination_ids", "movements", "new_destination_ids",
				"removed_destination_ids", "manifest", "consequence", "remedy",
			} {
				if _, ok := entry.Data[field]; !ok {
					t.Fatalf("refusal fields = %v, want %q", entry.Data, field)
				}
			}
			if got := entry.Data["change"]; got != "reorder" {
				t.Fatalf("change = %#v, want reorder", got)
			}

			combined := out.String() + logged.String()
			for _, secret := range []string{
				"/api/webhooks/", "secretAAAABBBBCCCC012345", "secretZZZZYYYYXXXX543210", "discord.com",
			} {
				if strings.Contains(combined, secret) {
					t.Fatalf("output leaks %q:\n%s", secret, combined)
				}
			}

			after, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
			if err != nil {
				t.Fatalf("ReadFile(manifest) error = %v", err)
			}
			if string(after) != string(seeded) {
				t.Fatal("a refused start rewrote the manifest")
			}
		})
	}
}

func TestApplyDestinationManifestAcceptsRemapWithFlagAndRewrites(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	seedDestinationManifest(t, destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB))
	cfg := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)

	var out bytes.Buffer
	code := applyDestinationManifest(cfg, cliOptions{acceptDestinationRemap: true}, &out, i18n.Default())
	if code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0 with --accept-destination-remap", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing when the remap is accepted", out.String())
	}

	entry := findRootTestLogEntry(hook, "Accepting a changed destination mapping on operator request")
	if entry == nil {
		t.Fatalf("missing acceptance entry; entries=%v", rootTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("acceptance level = %v, want warn", entry.Level)
	}
	if got := entry.Data["change"]; got != "reorder" {
		t.Fatalf("change = %#v, want reorder", got)
	}

	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	want := config.DestinationURLHashes(cfg)
	for id, hash := range want {
		if manifest.Destinations[id] != hash {
			t.Fatalf("manifest[%q] = %q, want %q", id, manifest.Destinations[id], hash)
		}
	}
}

func TestApplyDestinationManifestWritesSilentlyOnFirstRun(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)

	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0 on a first run", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing on a first run", out.String())
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level <= log.InfoLevel {
			t.Fatalf("first run logged %v %q, want the manifest to appear silently", entry.Level, entry.Message)
		}
	}

	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if len(manifest.Destinations) != len(config.DestinationURLHashes(cfg)) {
		t.Fatalf("manifest = %v, want one row per endpoint", manifest.Destinations)
	}
}

func TestApplyDestinationManifestDryRunDoesNotWrite(t *testing.T) {
	t.Run("clean data dir writes nothing", func(t *testing.T) {
		captureDestinationLog(t)

		dataDir := t.TempDir()
		cfg := destinationManifestConfig(dataDir, destinationWebhookA)

		var out bytes.Buffer
		if code := applyDestinationManifest(cfg, cliOptions{dryRun: true}, &out, i18n.Default()); code != 0 {
			t.Fatalf("applyDestinationManifest() = %d, want 0", code)
		}
		if _, err := os.Stat(status.DestinationManifestPath(dataDir)); !os.IsNotExist(err) {
			t.Fatalf("dry-run created the manifest (stat err=%v)", err)
		}
	})

	t.Run("remap still refuses", func(t *testing.T) {
		captureDestinationLog(t)

		dataDir := t.TempDir()
		seedDestinationManifest(t, destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB))
		cfg := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)

		var out bytes.Buffer
		if code := applyDestinationManifest(cfg, cliOptions{dryRun: true}, &out, i18n.Default()); code != 1 {
			t.Fatalf("applyDestinationManifest() = %d, want 1; --dry-run must show the same refusal", code)
		}
	})
}

func TestApplyDestinationManifestReportsRemovedDestinationsWithOrphanCounts(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	seedDestinationManifest(t, destinationManifestConfig(
		dataDir, destinationWebhookA, destinationWebhookB, destinationWebhookC))

	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	if !tracker.EnqueueRetryForDestination(status.RetryRequest{
		ItemKey:       "queued-item",
		DestinationID: "discord.ransomware.3",
		Messenger:     status.MessengerDiscord,
		ItemType:      status.RetryItemTypeAPI,
		Title:         "queued",
		LastError:     "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination() = false, want true")
	}
	tracker.MarkRetryDeadLetterForDestination(
		"dead-item", "discord.ransomware.3",
		status.MessengerDiscord.String(), status.RetryItemTypeAPI.String(), "dead", "boom",
	)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0; deleting from the end needs no flag", code)
	}

	entry := findRootTestLogEntry(hook, "Destination IDs removed from the configuration")
	if entry == nil {
		t.Fatalf("missing removal entry; entries=%v", rootTestLogMessages(hook))
	}
	if entry.Level != log.InfoLevel {
		t.Fatalf("removal level = %v, want info", entry.Level)
	}
	ids, _ := entry.Data["destination_ids"].(string)
	if !strings.Contains(ids, "discord.ransomware.3") || !strings.Contains(ids, "discord.rss.ransomware.3") {
		t.Fatalf("destination_ids = %q, want both removed families", ids)
	}
	if got := entry.Data["orphaned_retry_rows"]; got != 1 {
		t.Fatalf("orphaned_retry_rows = %#v, want 1", got)
	}
	if got := entry.Data["orphaned_dead_letter_rows"]; got != 1 {
		t.Fatalf("orphaned_dead_letter_rows = %#v, want 1", got)
	}
}

// TestApplyDestinationManifestCountsOrphansWithoutReadingAPIStatus pins the lazy
// API load for the orphan count. Counting a handful of retry and dead-letter
// rows must never read api_status.json, which holds up to max_api_sent_items
// (100 000) rows; the scheduler loads it lazily on purpose. An unreadable
// api_status.json is the cheapest observable proof: an eager tracker fails to
// construct and the counts silently vanish from the INFO, while the lazy one
// never opens the file and still reports them.
func TestApplyDestinationManifestCountsOrphansWithoutReadingAPIStatus(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	seedDestinationManifest(t, destinationManifestConfig(
		dataDir, destinationWebhookA, destinationWebhookB, destinationWebhookC))

	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	if !tracker.EnqueueRetryForDestination(status.RetryRequest{
		ItemKey:       "queued-item",
		DestinationID: "discord.ransomware.3",
		Messenger:     status.MessengerDiscord,
		ItemType:      status.RetryItemTypeAPI,
		Title:         "queued",
		LastError:     "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination() = false, want true")
	}
	tracker.MarkRetryDeadLetterForDestination(
		"dead-item", "discord.ransomware.3",
		status.MessengerDiscord.String(), status.RetryItemTypeAPI.String(), "dead", "boom",
	)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	// Any reader of api_status.json fails from here on; rss_status.json and
	// retry_status.json stay intact, so only an eager API load can break.
	apiStatusPath := filepath.Join(dataDir, "api_status.json")
	if err := os.WriteFile(apiStatusPath, []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0", code)
	}

	entry := findRootTestLogEntry(hook, "Destination IDs removed from the configuration")
	if entry == nil {
		t.Fatalf("missing removal entry; entries=%v", rootTestLogMessages(hook))
	}
	if got, ok := entry.Data["error"]; ok {
		t.Fatalf("removal entry carries error = %v; the orphan count must not open api_status.json", got)
	}
	if got := entry.Data["orphaned_retry_rows"]; got != 1 {
		t.Fatalf("orphaned_retry_rows = %#v, want 1; the count must not depend on api_status.json", got)
	}
	if got := entry.Data["orphaned_dead_letter_rows"]; got != 1 {
		t.Fatalf("orphaned_dead_letter_rows = %#v, want 1; the count must not depend on api_status.json", got)
	}
}

func TestApplyDestinationManifestTreatsCorruptManifestAsAbsent(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)

	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0; a corrupt manifest must self-heal", code)
	}

	entry := findRootTestLogEntry(hook, "Destination manifest unreadable; rewriting it")
	if entry == nil {
		t.Fatalf("missing self-heal WARN; entries=%v", rootTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("self-heal level = %v, want warn", entry.Level)
	}
	reason, _ := entry.Data["reason"].(string)
	if !strings.Contains(reason, "parse destination manifest") {
		t.Fatalf("reason = %q, want the parse failure named", reason)
	}
	consequence, _ := entry.Data["consequence"].(string)
	if !strings.Contains(consequence, "endpoint-order check is skipped") {
		t.Fatalf("consequence = %q, want the cost of the self-heal named", consequence)
	}

	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v; the manifest must be rewritten", found, err)
	}
	if manifest.Version != status.DestinationManifestVersion {
		t.Fatalf("Version = %d, want %d", manifest.Version, status.DestinationManifestVersion)
	}
}

func TestApplyDestinationManifestSkipsWhenDataDirEmpty(t *testing.T) {
	captureDestinationLog(t)

	cfg := destinationManifestConfig("", destinationWebhookA, destinationWebhookB)
	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0 without persistence", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing", out.String())
	}
}

// writeDestinationSubprocessConfig writes config_general.json for the given
// endpoint order into configDir, creating it when needed. apiBaseURL, when
// non-empty, points api_base_url at a local stub instead of the real
// ransomware.live default -- required by any caller whose subprocess reaches
// a live API cycle (e.g. an accepted --dry-run), so the test does not make a
// real HTTPS call to the vendor. Callers that never reach a fetch (a refused
// start, --check-config) pass "".
func writeDestinationSubprocessConfig(t *testing.T, configDir, dataDir, apiBaseURL string, urls ...string) {
	t.Helper()

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(configDir) error = %v", err)
	}
	quoted := make([]string, 0, len(urls))
	for _, webhookURL := range urls {
		quoted = append(quoted, fmt.Sprintf("%q", webhookURL))
	}
	apiBaseURLField := ""
	if apiBaseURL != "" {
		apiBaseURLField = fmt.Sprintf(",\n  \"api_base_url\": %q", apiBaseURL)
	}
	generalConfig := fmt.Sprintf(`{
  "api_key": "live-api-key-1234567890"%s,
  "data_dir": %q,
  "log_file_path": %q,
  "discord_webhooks": {
    "ransomware": {
      "enabled": true,
      "urls": [%s]
    }
  }
}`, apiBaseURLField, dataDir, filepath.Join(filepath.Dir(configDir), "logs", "bot.log"), strings.Join(quoted, ", "))
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(generalConfig), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
}

// seedDestinationManifestFromConfigDir writes the manifest that the current
// content of configDir would produce, which is how a previous run left it.
func seedDestinationManifestFromConfigDir(t *testing.T, configDir, dataDir string) {
	t.Helper()

	cfg, err := config.LoadConfigStrict(configDir)
	if err != nil {
		t.Fatalf("LoadConfigStrict() error = %v", err)
	}
	if err := status.WriteDestinationManifest(dataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
}

func TestRunCheckConfigRefusesDestinationRemapSubprocess(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_REMAP_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
			"--data-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
			"--locale",
			os.Getenv("RANSOMWARE_BOT_TEST_LOCALE"),
		}
		main()
		return
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")

	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
	seedDestinationManifestFromConfigDir(t, configDir, dataDir)
	// The operator swaps the two endpoints.
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)

	tests := []struct {
		name       string
		locale     string
		wantText   string
		wantAbsent string
	}{
		{
			name:       "english",
			locale:     "en",
			wantText:   "Destination remap detected (endpoints reordered): discord.ransomware<-discord.ransomware.2",
			wantAbsent: "Configuration valid",
		},
		{
			name:       "german",
			locale:     "de",
			wantText:   "Ziel-Neuzuordnung erkannt (Endpunkte umsortiert): discord.ransomware<-discord.ransomware.2",
			wantAbsent: "Konfiguration gueltig",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRunCheckConfigRefusesDestinationRemapSubprocess$") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
			cmd.Env = append(os.Environ(),
				"DATA_DIR=",
				"RANSOMWARE_BOT_CHECK_CONFIG_REMAP_HELPER=1",
				"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
				"RANSOMWARE_BOT_TEST_DATA_DIR="+dataDir,
				"RANSOMWARE_BOT_TEST_LOCALE="+tc.locale,
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			if err == nil {
				t.Fatalf("check-config exited 0, want 1; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("check-config error = %T %v; stdout:\n%s", err, err, stdout.String())
			}
			if exitErr.ExitCode() != 1 {
				t.Fatalf("check-config exit code = %d, want 1; stdout:\n%s", exitErr.ExitCode(), stdout.String())
			}

			out := stdout.String()
			combined := out + stderr.String()
			if !strings.Contains(out, tc.wantText) {
				t.Fatalf("stdout = %q, want %q on stdout", out, tc.wantText)
			}
			if strings.Contains(out, tc.wantAbsent) {
				t.Fatalf("stdout = %q, want %q absent", out, tc.wantAbsent)
			}
			for _, secret := range []string{"/api/webhooks/", "secretAAAABBBBCCCC012345", "secretZZZZYYYYXXXX543210"} {
				if strings.Contains(combined, secret) {
					t.Fatalf("output leaks %q:\n%s", secret, combined)
				}
			}
		})
	}
}

// TestRunHealthcheckReportsDestinationRemap pins that the remap check runs
// before the readiness-marker check. Both marker states matter: a missing
// marker would otherwise mask the real cause, and a stale marker left by a
// killed process would otherwise report GREEN while the bot refuses to start.
func TestRunHealthcheckReportsDestinationRemap(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		want   string
	}{
		{name: "english", locale: "en", want: "destination remap pending (endpoints reordered)"},
		{name: "german", locale: "de", want: "Ziel-Neuzuordnung ausstehend (Endpunkte umsortiert)"},
	}

	markers := []struct {
		name  string
		stale bool
	}{
		{name: "missing readiness marker"},
		{name: "stale readiness marker", stale: true},
	}

	for _, tc := range tests {
		for _, marker := range markers {
			t.Run(tc.name+" with a "+marker.name, func(t *testing.T) {
				tmpDir := t.TempDir()
				configDir := filepath.Join(tmpDir, "configs")
				dataDir := filepath.Join(tmpDir, "data")
				writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
				seedDestinationManifestFromConfigDir(t, configDir, dataDir)
				writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)

				markerPath := filepath.Join(tmpDir, "ransomware-bot.ready")
				if marker.stale {
					if err := os.WriteFile(markerPath, []byte("stale"), 0600); err != nil {
						t.Fatalf("WriteFile(marker) error = %v", err)
					}
				}
				t.Setenv(readinessFileEnv, markerPath)

				err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.ForLocale(tc.locale))
				if err == nil {
					t.Fatal("runHealthcheckWithMessages() error = nil, want the pending remap")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.want)
				}
				if !strings.Contains(err.Error(), "discord.ransomware<-discord.ransomware.2") {
					t.Fatalf("error = %v, want the movement list", err)
				}
				if strings.Contains(err.Error(), "readiness marker") {
					t.Fatalf("error = %v, want the remap reported before the readiness marker", err)
				}
				if strings.Contains(err.Error(), "/api/webhooks/") {
					t.Fatalf("error = %v, want no webhook URL", err)
				}
			})
		}
	}
}

func TestRunCLIHealthcheckModeExitsOneOnDestinationRemap(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
	seedDestinationManifestFromConfigDir(t, configDir, dataDir)
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)
	t.Setenv(readinessFileEnv, "")

	var out bytes.Buffer
	code := runCLI([]string{"--healthcheck", "--config-dir", configDir, "--data-dir", dataDir}, &out, io.Discard)
	if code != 1 {
		t.Fatalf("runCLI(--healthcheck) = %d, want 1; out=%q", code, out.String())
	}
	msg := i18n.Default()
	wantPrefix := strings.TrimSpace(strings.TrimSuffix(fmt.Sprintf(msg.T("cli.healthcheck_failed"), ""), "\n"))
	if !strings.Contains(out.String(), wantPrefix) {
		t.Fatalf("out = %q, want the cli.healthcheck_failed wrapper", out.String())
	}
	if !strings.Contains(out.String(), "destination remap pending") {
		t.Fatalf("out = %q, want the pending remap", out.String())
	}
}

func TestRuntimeStartRefusesDestinationRemapSubprocess(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_RUNTIME_REMAP_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
			"--data-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
		}
		main()
		return
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
	seedDestinationManifestFromConfigDir(t, configDir, dataDir)
	seeded, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)

	output, err := runMainSubprocess(t, "TestRuntimeStartRefusesDestinationRemapSubprocess", map[string]string{
		"RANSOMWARE_BOT_RUNTIME_REMAP_HELPER": "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR":      configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":        dataDir,
	})
	if err == nil {
		t.Fatalf("runtime start exited 0, want 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("runtime start error = %T %v; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("runtime start exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `"change":"reorder"`) {
		t.Fatalf("output = %s\nwant the structured ERROR with change=reorder", output)
	}
	if !strings.Contains(string(output), "Refusing to start: webhook endpoints were remapped onto existing destination IDs") {
		t.Fatalf("output = %s\nwant the refusal message", output)
	}
	// The gate runs before scheduler.New, so the data-dir lock is never taken.
	if _, err := os.Stat(filepath.Join(dataDir, ".ransomware-bot.lock")); !os.IsNotExist(err) {
		t.Fatalf("data dir lock exists (stat err=%v); the gate ran too late", err)
	}
	after, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	if string(after) != string(seeded) {
		t.Fatal("a refused start rewrote the manifest")
	}
}

func TestRuntimeStartAcceptsDestinationRemapSubprocess(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_RUNTIME_ACCEPT_REMAP_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--dry-run",
			"--accept-destination-remap",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
			"--data-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
		}
		main()
		return
	}

	// A real httptest.Server stubs api_base_url so the accepted dry-run's
	// live API cycle succeeds locally instead of making a real HTTPS call to
	// the vendor's default api_base_url (which 403s on the fake key and used
	// to make this test depend on --dry-run's exit code always being 0).
	// Started before writeDestinationSubprocessConfig:
	// the config is written before the subprocess launches, and the
	// subprocess reaches this listener over loopback.
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"victims": []}`)); err != nil {
			t.Errorf("write stub API response: %v", err)
		}
	}))
	defer apiServer.Close()

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")
	writeDestinationSubprocessConfig(t, configDir, dataDir, apiServer.URL, destinationWebhookA, destinationWebhookB)
	seedDestinationManifestFromConfigDir(t, configDir, dataDir)
	seeded, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	writeDestinationSubprocessConfig(t, configDir, dataDir, apiServer.URL, destinationWebhookB, destinationWebhookA)

	output, err := runMainSubprocess(t, "TestRuntimeStartAcceptsDestinationRemapSubprocess", map[string]string{
		"RANSOMWARE_BOT_RUNTIME_ACCEPT_REMAP_HELPER": "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR":             configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":               dataDir,
	})
	if err != nil {
		t.Fatalf("accepted dry-run failed: %v; output:\n%s", err, output)
	}
	if !strings.Contains(string(output), "Accepting a changed destination mapping on operator request") {
		t.Fatalf("output = %s\nwant the acceptance WARN", output)
	}
	after, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	if string(after) != string(seeded) {
		t.Fatal("a dry run rewrote the manifest; --dry-run must never write persistent state")
	}
}

func findRootTestLogEntry(hook *logtest.Hook, message string) *log.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	return nil
}

func rootTestLogMessages(hook *logtest.Hook) []string {
	messages := make([]string, 0, len(hook.AllEntries()))
	for _, entry := range hook.AllEntries() {
		messages = append(messages, entry.Message)
	}
	return messages
}

func TestApplyDestinationManifestWarnsWhenTheManifestCannotBeWritten(t *testing.T) {
	_, hook := captureDestinationLog(t)

	// A regular file cannot hold a manifest, so the write fails immediately.
	dataDir := filepath.Join(t.TempDir(), "data-is-a-file")
	if err := os.WriteFile(dataDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataDir) error = %v", err)
	}
	cfg := destinationManifestConfig(dataDir, destinationWebhookA)

	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0; a manifest write must never stop the bot", code)
	}
	entry := findRootTestLogEntry(hook, "Failed to write the destination manifest")
	if entry == nil {
		t.Fatalf("missing write-failure WARN; entries=%v", rootTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("write-failure level = %v, want warn", entry.Level)
	}
}

func TestApplyDestinationManifestReportsRemovedDestinationsWithoutCountsWhenTheTrackerFails(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dataDir := t.TempDir()
	seedDestinationManifest(t, destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB))
	// A directory in retry_status.json's place makes the read-only tracker fail.
	if err := os.Mkdir(filepath.Join(dataDir, "retry_status.json"), 0700); err != nil {
		t.Fatalf("Mkdir(retry_status.json) error = %v", err)
	}

	cfg := destinationManifestConfig(dataDir, destinationWebhookA)
	var out bytes.Buffer
	if code := applyDestinationManifest(cfg, cliOptions{}, &out, i18n.Default()); code != 0 {
		t.Fatalf("applyDestinationManifest() = %d, want 0", code)
	}

	entry := findRootTestLogEntry(hook, "Destination IDs removed from the configuration")
	if entry == nil {
		t.Fatalf("missing removal entry; entries=%v", rootTestLogMessages(hook))
	}
	if _, ok := entry.Data["orphaned_retry_rows"]; ok {
		t.Fatalf("fields = %v, want no counts when the tracker could not be opened", entry.Data)
	}
	if _, ok := entry.Data[log.ErrorKey]; !ok {
		t.Fatalf("fields = %v, want the tracker error attached", entry.Data)
	}
}

func TestPendingDestinationRemapIsQuietWithoutStateOrChange(t *testing.T) {
	dataDir := t.TempDir()
	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)

	if _, pending, skipped := pendingDestinationRemap(cfg, ""); pending || skipped != nil {
		t.Fatalf("pendingDestinationRemap(no data dir) = pending %v skipped %v, want false, nil", pending, skipped)
	}
	if _, pending, skipped := pendingDestinationRemap(nil, dataDir); pending || skipped != nil {
		t.Fatalf("pendingDestinationRemap(nil config) = pending %v skipped %v, want false, nil", pending, skipped)
	}
	// A missing manifest (normal first run) is not a "skip" -- only a
	// manifest that exists but fails to load is (see
	// TestPendingDestinationRemapReturnsSkippedErrorOnCorruptManifest). This
	// distinction is exactly what pendingDestinationRemap's skipped-error
	// return was fixed to preserve: it must not collapse into the same
	// signal as a corrupt manifest.
	if _, pending, skipped := pendingDestinationRemap(cfg, dataDir); pending || skipped != nil {
		t.Fatalf("pendingDestinationRemap(no manifest) = pending %v skipped %v, want false, nil", pending, skipped)
	}

	seedDestinationManifest(t, cfg)
	if _, pending, skipped := pendingDestinationRemap(cfg, dataDir); pending || skipped != nil {
		t.Fatalf("pendingDestinationRemap(unchanged config) = pending %v skipped %v, want false, nil", pending, skipped)
	}
	// Appending an endpoint is always safe and must stay quiet.
	appended := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB, destinationWebhookC)
	if _, pending, skipped := pendingDestinationRemap(appended, dataDir); pending || skipped != nil {
		t.Fatalf("pendingDestinationRemap(appended endpoint) = pending %v skipped %v, want false, nil", pending, skipped)
	}
}

// TestPendingDestinationRemapReturnsSkippedErrorOnCorruptManifest pins the
// fix: a corrupt (unparsable) destinations.json must
// surface as a distinct "skipped" signal, not silently collapse into
// pending=false the same way a genuinely absent manifest does.
func TestPendingDestinationRemapReturnsSkippedErrorOnCorruptManifest(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	cfg := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)

	diff, pending, skipped := pendingDestinationRemap(cfg, dataDir)
	if pending {
		t.Fatalf("pendingDestinationRemap(corrupt manifest) pending = true, want false; diff = %+v", diff)
	}
	if skipped == nil {
		t.Fatal("pendingDestinationRemap(corrupt manifest) skipped = nil, want the load error")
	}
	if !strings.Contains(skipped.Error(), "parse destination manifest") {
		t.Fatalf("skipped error = %v, want it to name the parse failure", skipped)
	}
}

// TestRunCheckConfigWarnsWhenDestinationManifestUnreadable pins the matching
// --check-config surface: a corrupt destinations.json must not
// be silently indistinguishable from "no remap"; --check-config prints an
// explicit warning naming the skip while its exit code stays exactly as
// today (the endpoint-order guard being unreadable is not itself refused).
func TestRunCheckConfigWarnsWhenDestinationManifestUnreadable(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}

	opts := cliOptions{configDir: configDir, dataDir: dataDir, locale: "en"}
	var out bytes.Buffer
	code := runCheckConfig(opts, &out, i18n.ForLocale("en"))
	if code != 0 {
		t.Fatalf("runCheckConfig() = %d, want 0 (a corrupt manifest is a warning, not a refusal): %s", code, out.String())
	}
	if !strings.Contains(out.String(), "destinations.json could not be read") {
		t.Fatalf("output missing the manifest-unreadable warning:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "parse destination manifest") {
		t.Fatalf("output missing the underlying parse-failure reason:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Configuration valid") {
		t.Fatalf("output missing the usual success line:\n%s", out.String())
	}
}

// TestRunHealthcheckWarnsWhenDestinationManifestUnreadable pins the matching
// --healthcheck surface: the same warning, still returning nil
// (exit 0). This warning is best-effort: it is reached only when the
// healthcheck gets all the way to its normal success summary, i.e. only if
// the dead-letter, API-auth-suspension and RSS-health checks all pass first.
// This fixture has no RSS webhook enabled and no dead letters, so it does
// reach the summary.
func TestRunHealthcheckWarnsWhenDestinationManifestUnreadable(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	dataDir := filepath.Join(tmpDir, "data")
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookA, destinationWebhookB)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	t.Setenv(readinessFileEnv, "")

	var out bytes.Buffer
	err := runHealthcheckWithMessages(configDir, dataDir, &out, i18n.ForLocale("en"))
	if err != nil {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Destination manifest unreadable") {
		t.Fatalf("output missing the manifest-unreadable warning:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "parse destination manifest") {
		t.Fatalf("output missing the underlying parse-failure reason:\n%s", out.String())
	}
}
