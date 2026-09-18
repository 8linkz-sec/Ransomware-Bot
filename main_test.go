package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/i18n"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	log "github.com/sirupsen/logrus"
)

func TestResolveDataDir(t *testing.T) {
	tests := []struct {
		name      string
		flagVal   string
		envVal    string
		configVal string
		wantAbsOf string
		wantEmpty bool
	}{
		{
			name:      "flag overrides env and config",
			flagVal:   "flag/path",
			envVal:    "/env/path",
			configVal: "/config/path",
			wantAbsOf: "flag/path",
		},
		{
			name:      "env overrides config",
			envVal:    "env/path",
			configVal: "/config/path",
			wantAbsOf: "env/path",
		},
		{
			name:      "config used without flag or env",
			configVal: "/config/data",
			wantAbsOf: "/config/data",
		},
		{
			name:      "default empty when all sources empty",
			wantEmpty: true,
		},
		{
			name:      "flag resolves to absolute",
			flagVal:   "relative/data",
			wantAbsOf: "relative/data",
		},
		{
			name:      "env resolves to absolute",
			envVal:    "relative/env",
			wantAbsOf: "relative/env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", tt.envVal)

			result, err := resolveDataDir(tt.flagVal, tt.configVal)
			if err != nil {
				t.Fatalf("resolveDataDir() error = %v", err)
			}

			if tt.wantEmpty {
				if result != "" {
					t.Fatalf("resolveDataDir() = %q, want empty default", result)
				}
				return
			}

			want, err := filepath.Abs(tt.wantAbsOf)
			if err != nil {
				t.Fatalf("Abs(%q) error = %v", tt.wantAbsOf, err)
			}
			if result != want {
				t.Fatalf("resolveDataDir() = %q, want %q", result, want)
			}
			if !filepath.IsAbs(result) {
				t.Fatalf("resolveDataDir() = %q, want absolute path", result)
			}
		})
	}
}

func TestVersionInfoIncludesBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	Version, Commit, BuildDate = "test-version", "abc123", "2026-06-27T12:00:00Z"
	t.Cleanup(func() {
		Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	got := versionInfo()
	if !strings.HasPrefix(got, "Ransomware News Bot v") {
		t.Fatalf("versionInfo() = %q, want Ransomware News Bot product name", got)
	}
	for _, want := range []string{"test-version", "abc123", "2026-06-27T12:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("versionInfo() = %q, want substring %q", got, want)
		}
	}
}

func TestParseCLIOptionsLocalizesHelpFromLocaleFlag(t *testing.T) {
	var errOut bytes.Buffer

	_, err := parseCLIOptions([]string{"--locale", "de", "--help"}, &errOut)
	if err == nil {
		t.Fatal("parseCLIOptions() error = nil, want help error")
	}
	for _, want := range []string{
		"Verzeichnis mit Konfigurationsdateien",
		"Konfiguration pruefen und beenden",
		"Locale fuer CLI-Hilfe",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("localized help missing %q:\n%s", want, errOut.String())
		}
	}
}

func TestParseCLIOptionsLocalizesHelpFromBotLocaleEnv(t *testing.T) {
	t.Setenv(i18n.LocaleEnv, "de")
	var errOut bytes.Buffer

	_, err := parseCLIOptions([]string{"--help"}, &errOut)
	if err == nil {
		t.Fatal("parseCLIOptions() error = nil, want help error")
	}
	if !strings.Contains(errOut.String(), "Laufzeitstatus pruefen und beenden") {
		t.Fatalf("localized help missing healthcheck text:\n%s", errOut.String())
	}
}

func TestVersionFlagSubprocessExitsZero(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_VERSION_HELPER") == "1" {
		os.Args = []string{"ransomware-bot", "--version"}
		main()
		return
	}

	output, err := runMainSubprocess(t, "TestVersionFlagSubprocessExitsZero", map[string]string{
		"RANSOMWARE_BOT_VERSION_HELPER": "1",
	})
	if err != nil {
		t.Fatalf("version helper failed: %v; output:\n%s", err, output)
	}
	if !strings.HasPrefix(string(output), "Ransomware News Bot v") {
		t.Fatalf("version output = %q, want Ransomware News Bot version prefix", output)
	}
}

func TestDryRunSubprocessExitsZeroWithoutPersistingTrackers(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_DRY_RUN_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--dry-run",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
			"--data-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
		}
		main()
		return
	}

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	logFilePath := filepath.Join(tmpDir, "logs", "bot.log")
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{
		"data_dir": %q,
		"log_file_path": %q
	}`, dataDir, logFilePath))

	output, err := runMainSubprocess(t, "TestDryRunSubprocessExitsZeroWithoutPersistingTrackers", map[string]string{
		"RANSOMWARE_BOT_DRY_RUN_HELPER":  "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR": configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":   dataDir,
	})
	if err != nil {
		t.Fatalf("dry-run helper failed: %v; output:\n%s", err, output)
	}
	for _, name := range []string{
		"api_status.json", "rss_status.json", "retry_status.json",
		"destinations.json", ".ransomware-bot.lock",
	} {
		path := filepath.Join(dataDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run created persistent tracker file %s (stat err=%v); output:\n%s", path, err, output)
		}
	}
}

func TestRuntimeConfigLoadUsesBootstrapStructuredLogger(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	dataPath := filepath.Join(tmpDir, "data-is-a-file")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(fmt.Sprintf(`{
		"data_dir": %q
	}`, dataPath)), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	originalOut := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.StandardLogger().Level
	t.Cleanup(func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	})
	if err := setupBootstrapLogger(); err != nil {
		t.Fatalf("setupBootstrapLogger() error = %v", err)
	}
	var output bytes.Buffer
	log.SetOutput(&output)

	cfg, err := loadRuntimeConfig(cliOptions{configDir: configDir})
	if err != nil {
		t.Fatalf("loadRuntimeConfig() error = %v, want scheduler memory fallback", err)
	}
	if cfg.DataDir != dataPath {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, dataPath)
	}
	text := output.String()
	for _, want := range []string{
		"Data directory validation failed",
		`"service":"ransomware-news-bot"`,
		`"level":"warning"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestWriteDeadLetterListIncludesOperatorRecoveryDetails(t *testing.T) {
	var buf bytes.Buffer
	writeDeadLetterList(&buf, "/tmp/ransomware-bot-data", []status.DeadLetterEntry{
		{
			ItemKey:        "api-item-1",
			ItemType:       "api",
			Messenger:      "slack",
			Title:          "Example Corp",
			RetryCount:     5,
			LastError:      "Slack webhook provider unavailable",
			TerminalReason: status.TerminalReasonMaxAttempts,
			DeadAt:         "2026-06-27 10:30:00",
			Payload:        []byte(`{"id":"victim-1"}`),
		},
	})

	got := buf.String()
	for _, want := range []string{
		"Dead-letter items: 1",
		"Data dir: /tmp/ransomware-bot-data",
		"[slack/api] Example Corp",
		"key: api-item-1",
		"reason: max_attempts_exhausted",
		"retry_count: 5",
		"last_error: Slack webhook provider unavailable",
		"replay_payload: yes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dead-letter output missing %q:\n%s", want, got)
		}
	}
}

func TestWriteDeadLetterListUsesConfiguredLocale(t *testing.T) {
	var buf bytes.Buffer
	writeDeadLetterListWithMessages(&buf, "/tmp/ransomware-bot-data", []status.DeadLetterEntry{
		{
			ItemKey:        "api-item-1",
			ItemType:       "api",
			Messenger:      "slack",
			RetryCount:     5,
			LastError:      "provider unavailable",
			TerminalReason: status.TerminalReasonMaxAttempts,
			DeadAt:         "2026-06-27 10:30:00",
			Payload:        []byte(`{"id":"victim-1"}`),
		},
	}, i18n.ForLocale("de"))

	got := buf.String()
	for _, want := range []string{
		"Dead-Letter-Eintraege: 1",
		"Datenverzeichnis: /tmp/ransomware-bot-data",
		"(ohne Titel)",
		"Schluessel: api-item-1",
		"Retry-Anzahl: 5",
		"letzter_fehler: provider unavailable",
		"replay_payload: ja",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("localized dead-letter output missing %q:\n%s", want, got)
		}
	}
}

func TestValidateDataDirCreatesWritableDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "new-data")
	if err := validateDataDir(dataDir); err != nil {
		t.Fatalf("validateDataDir() error = %v", err)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("dataDir exists but is not a directory")
	}
}

func TestValidateDataDirCreatesPrivateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}

	dataDir := filepath.Join(t.TempDir(), "new-data")
	if err := validateDataDir(dataDir); err != nil {
		t.Fatalf("validateDataDir() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != privateRuntimeDirMode {
		t.Fatalf("data dir mode = %v, want %v", got, privateRuntimeDirMode)
	}
}

func TestEnsurePrivateRuntimeDirTightensExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}

	dataDir := filepath.Join(t.TempDir(), "existing-data")
	if err := os.Mkdir(dataDir, 0755); err != nil { //nolint:gosec // G301: deliberately loose starting mode, this test asserts ensurePrivateRuntimeDir tightens it
		t.Fatalf("Mkdir(dataDir) error = %v", err)
	}

	if err := ensurePrivateRuntimeDir(dataDir); err != nil {
		t.Fatalf("ensurePrivateRuntimeDir() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != privateRuntimeDirMode {
		t.Fatalf("data dir mode = %v, want %v", got, privateRuntimeDirMode)
	}
}

func TestValidateDataDirRejectsFile(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	err := validateDataDir(dataPath)
	if err == nil {
		t.Fatal("expected validateDataDir to reject a file path")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want not a directory context", err)
	}
}

func TestLoadRuntimeConfigAllowsInvalidDataDirForSchedulerFallback(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{"data_dir": %q}`, dataPath))

	cfg, err := loadRuntimeConfig(cliOptions{configDir: configDir})
	if err != nil {
		t.Fatalf("loadRuntimeConfig() error = %v, want scheduler memory fallback", err)
	}
	if cfg.DataDir != dataPath {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, dataPath)
	}
}

func runMainSubprocess(t *testing.T, testName string, env map[string]string) ([]byte, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$") //nolint:gosec // G204: re-execs this test binary; testName is a fixed literal passed by the calling test, not external input
	cmd.Env = append(os.Environ(), "DATA_DIR=")
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd.CombinedOutput()
}

func TestCheckConfigInvalidExitsWithStatusOne(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
		}
		main()
		return
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config_general.json")
	if err := os.WriteFile(configPath, []byte(`{"log_level":"NOT_A_LEVEL"}`), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCheckConfigInvalidExitsWithStatusOne") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
	cmd.Env = append(os.Environ(),
		"RANSOMWARE_BOT_CHECK_CONFIG_HELPER=1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("check-config helper exited successfully, want status 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("check-config helper error = %T %v, want ExitError; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("check-config exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "Configuration invalid") {
		t.Fatalf("output = %q, want Configuration invalid", output)
	}
}

func TestCheckConfigBundledExampleConfigsSmoke(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_BUNDLED_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
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
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	copyBundledConfigForTest(t, filepath.Join("configs", "config_general.example.json"), filepath.Join(configDir, "config_general.json"))
	copyBundledConfigForTest(t, filepath.Join("configs", "config_feeds.json"), filepath.Join(configDir, "config_feeds.json"))
	copyBundledConfigForTest(t, filepath.Join("configs", "config_format.json"), filepath.Join(configDir, "config_format.json"))

	output, err := runMainSubprocess(t, "TestCheckConfigBundledExampleConfigsSmoke", map[string]string{
		"RANSOMWARE_BOT_CHECK_CONFIG_BUNDLED_HELPER": "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR":             configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":               dataDir,
	})
	if err != nil {
		t.Fatalf("bundled check-config helper failed: %v; output:\n%s", err, output)
	}
	if !strings.Contains(string(output), "Configuration valid") {
		t.Fatalf("output = %q, want Configuration valid", output)
	}
}

func TestCheckConfigRejectsDataDirFile(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_DATADIR_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
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
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	configPath := filepath.Join(configDir, "config_general.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
	dataPath := filepath.Join(tmpDir, "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCheckConfigRejectsDataDirFile") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
	cmd.Env = append(os.Environ(),
		"RANSOMWARE_BOT_CHECK_CONFIG_DATADIR_HELPER=1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR="+dataPath,
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("check-config helper exited successfully, want status 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("check-config helper error = %T %v, want ExitError; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("check-config exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "data_dir") || !strings.Contains(string(output), "not a directory") {
		t.Fatalf("output = %q, want data_dir not-a-directory validation context", output)
	}
}

func copyBundledConfigForTest(t *testing.T, src, dst string) {
	t.Helper()
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0600); err != nil { //nolint:gosec // G703: dst is filepath.Clean'd above and built from t.TempDir()/copyBundledConfigForTest's fixed test-config filenames, not external input
		t.Fatalf("WriteFile(%s) error = %v", dst, err)
	}
}

func TestCheckConfigWarnsWhenNoDeliveryTargetsEnabled(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_NO_TARGETS_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
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
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCheckConfigWarnsWhenNoDeliveryTargetsEnabled") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
	cmd.Env = append(os.Environ(),
		"RANSOMWARE_BOT_CHECK_CONFIG_NO_TARGETS_HELPER=1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR="+dataDir,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-config helper failed unexpectedly: %v; output:\n%s", err, output)
	}
	if !strings.Contains(string(output), "Configuration warning: no delivery channels enabled") {
		t.Fatalf("output = %q, want no-delivery warning", output)
	}
	if !strings.Contains(string(output), "Configuration valid") {
		t.Fatalf("output = %q, want Configuration valid", output)
	}
}

func TestCheckConfigWarnsWhenWebhookEndpointsShareURL(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_SHARED_URL_HELPER") == "1" {
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
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	// Two enabled discord ransomware endpoints on one URL. api_key must be a
	// real-looking value or validateAPIConfig rejects the config first.
	const sharedURL = "https://discord.com/api/webhooks/123456789012345678/secretAAAABBBBCCCC012345"
	generalConfig := `{
  "api_key": "live-api-key-1234567890",
  "discord_webhooks": {
    "ransomware": {
      "enabled": true,
      "url": "` + sharedURL + `",
      "targets": [{"url": "` + sharedURL + `"}]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(generalConfig), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	tests := []struct {
		name        string
		locale      string
		wantWarning string
		wantValid   string
	}{
		{
			name:        "english",
			locale:      "en",
			wantWarning: "Configuration warning: webhook endpoints discord.ransomware, discord.ransomware.2 share one webhook URL",
			wantValid:   "Configuration valid",
		},
		{
			name:        "german",
			locale:      "de",
			wantWarning: "Konfigurationswarnung: die Webhook-Ziele discord.ransomware, discord.ransomware.2 verwenden dieselbe Webhook-URL",
			wantValid:   "Konfiguration gueltig",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestCheckConfigWarnsWhenWebhookEndpointsShareURL") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
			cmd.Env = append(os.Environ(),
				"RANSOMWARE_BOT_CHECK_CONFIG_SHARED_URL_HELPER=1",
				"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
				"RANSOMWARE_BOT_TEST_DATA_DIR="+dataDir,
				"RANSOMWARE_BOT_TEST_LOCALE="+tc.locale,
			)

			// stdout and stderr are captured separately on purpose: the
			// localized warning is an operator-surface line and must be on
			// STDOUT. The logrus WARN reaches stderr from the same load; a
			// combined capture would pass even if runCheckConfig printed to
			// stderr only.
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("check-config helper failed unexpectedly: %v; stdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			out := stdout.String()
			combined := out + stderr.String()

			if !strings.Contains(out, tc.wantWarning) {
				t.Fatalf("stdout = %q, want shared webhook URL warning %q on stdout", out, tc.wantWarning)
			}
			if !strings.Contains(out, tc.wantValid) {
				t.Fatalf("stdout = %q, want %q", out, tc.wantValid)
			}
			// The warning must precede "Configuration valid" so a script that
			// stops reading at the valid line still sees it.
			if strings.Index(out, tc.wantWarning) > strings.Index(out, tc.wantValid) {
				t.Fatalf("stdout = %q, want the shared-URL warning before %q", out, tc.wantValid)
			}
			// Neither stream may carry the URL or its token.
			if strings.Contains(combined, "/api/webhooks/") || strings.Contains(combined, "secretAAAABBBBCCCC012345") {
				t.Fatalf("output = %q, want no webhook URL or token", combined)
			}
			// --check-config runs with runtimeMode false, so no logger is
			// configured and the logrus service field is never attached.
			if strings.Contains(combined, "service=") {
				t.Fatalf("output = %q, want no service field in check-config mode", combined)
			}
		})
	}
}

func TestEnabledDeliveryTargetsCountsSlackCompatibleTargets(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SlackCompatibleWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.eu.example/services/T12345678/B12345678/custom-secret-token",
	}

	if got := enabledDeliveryTargets(cfg); got != 1 {
		t.Fatalf("enabledDeliveryTargets() = %d, want 1 for Slack-compatible target", got)
	}
	if !ransomwareDeliveryEnabled(cfg) {
		t.Fatal("ransomwareDeliveryEnabled() = false for Slack-compatible ransomware target")
	}
}

func TestRunHealthcheckOK(t *testing.T) {
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	// --healthcheck no longer creates data_dir, so the operator's mounted
	// directory has to be here before the check runs.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	var buf bytes.Buffer
	if err := runHealthcheck(configDir, dataDir, &buf); err != nil {
		t.Fatalf("runHealthcheck() error = %v", err)
	}
	for _, want := range []string{
		"Data dir: " + dataDir,
		"Retry queue items: 0",
		"Dead-letter items: 0",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("healthcheck output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunHealthcheckRequiresReadinessMarkerWhenConfigured(t *testing.T) {
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	marker := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	t.Setenv(readinessFileEnv, marker)

	err := runHealthcheck(configDir, dataDir, io.Discard)
	if err == nil {
		t.Fatal("runHealthcheck() error = nil, want missing readiness marker failure")
	}
	if !strings.Contains(err.Error(), "readiness marker") {
		t.Fatalf("runHealthcheck() error = %v, want readiness marker context", err)
	}

	if err := writeReadinessMarker(); err != nil {
		t.Fatalf("writeReadinessMarker() error = %v", err)
	}
	var buf bytes.Buffer
	if err := runHealthcheck(configDir, dataDir, &buf); err != nil {
		t.Fatalf("runHealthcheck() with readiness marker error = %v", err)
	}
	if !strings.Contains(buf.String(), "Readiness marker: "+marker) {
		t.Fatalf("healthcheck output missing readiness marker %q:\n%s", marker, buf.String())
	}

	if err := removeReadinessMarker(); err != nil {
		t.Fatalf("removeReadinessMarker() error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("readiness marker still exists after removal, stat err = %v", err)
	}
}

func TestRunHealthcheckFailsOnDeadLetterItems(t *testing.T) {
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")

	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	tracker.MarkRetryDeadLetter("api-item", "discord", "api", "Example Corp", "terminal failure")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	err = runHealthcheck(configDir, dataDir, io.Discard)
	if err == nil {
		t.Fatal("runHealthcheck() error = nil, want dead-letter failure")
	}
	if !strings.Contains(err.Error(), "dead-letter queue has 1") {
		t.Fatalf("runHealthcheck() error = %v, want dead-letter context", err)
	}
}

func TestRunHealthcheckFailsOnAPIErrorForRansomwareDelivery(t *testing.T) {
	configDir := writeMainTestConfig(t, `{
		"api_key": "real-api-key",
		"discord_webhooks": {
			"ransomware": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`)
	dataDir := filepath.Join(t.TempDir(), "data")

	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	tracker.UpdateAPIStatus(false, 0, "provider unavailable")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	err = runHealthcheck(configDir, dataDir, io.Discard)
	if err == nil {
		t.Fatal("runHealthcheck() error = nil, want API failure")
	}
	if !strings.Contains(err.Error(), "ransomware API last check failed") {
		t.Fatalf("runHealthcheck() error = %v, want API failure context", err)
	}
}

func writeMainTestConfig(t *testing.T, configJSON string) string {
	t.Helper()

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(configJSON), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
	return configDir
}

func TestCheckConfigRejectsPlaceholderAPIKeyForRansomwareAlerts(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_CHECK_CONFIG_PLACEHOLDER_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--check-config",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
		}
		main()
		return
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config_general.json")
	configJSON := `{
		"api_key": "YOUR_RANSOMWARE_LIVE_API_KEY",
		"discord_webhooks": {
			"ransomware": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCheckConfigRejectsPlaceholderAPIKeyForRansomwareAlerts") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
	cmd.Env = append(os.Environ(),
		"RANSOMWARE_BOT_CHECK_CONFIG_PLACEHOLDER_HELPER=1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("check-config helper exited successfully, want status 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("check-config helper error = %T %v, want ExitError; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("check-config exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "api_key") {
		t.Fatalf("output = %q, want api_key validation context", output)
	}
}

func TestSetupApplicationLoggerFallsBackToStdoutWhenLogDirIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	logDir := filepath.Join(tmpDir, "logs")
	if err := os.WriteFile(logDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(logDir placeholder) error = %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	// Redirect the real os.Stdout through a pipe: NewStdoutLogger's fallback
	// (internal/logger/logger.go's resetOutputAndCloseCurrentLocked) points
	// logrus at whatever os.Stdout is at call time, so this lets the test
	// both capture the fallback's log lines and, further down, compare the
	// resulting logrus writer against that same os.Stdout by identity.
	originalStdout := os.Stdout
	originalOut := log.StandardLogger().Out
	reader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = pipeWriter
	log.SetOutput(pipeWriter)
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		captured <- string(data)
	}()

	logFilePath := filepath.Join(logDir, "bot.log")
	setupErr := setupApplicationLogger("INFO", logFilePath, logger.LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})

	// Capture the writer identity before restoring the real os.Stdout: the
	// stdout-only fallback must actually be the active logger.
	// internal/logger.NewLogger installs io.MultiWriter(os.Stdout,
	// logFileWriter) (internal/logger/logger.go:71), so a bare os.Stdout here
	// proves NewStdoutLogger really ran -- this catches both a dropped
	// fallback call (Out would stay wherever it was left, not this pipe) and
	// a fallback that quietly installed a file logger elsewhere (Out would be
	// a MultiWriter, never comparable-equal to a bare os.Stdout).
	gotOut := log.StandardLogger().Out
	wantOut := os.Stdout

	os.Stdout = originalStdout
	log.SetOutput(originalOut)
	_ = pipeWriter.Close()
	output := <-captured
	_ = reader.Close()

	if setupErr != nil {
		t.Fatalf("setupApplicationLogger() error = %v", setupErr)
	}
	if gotOut != wantOut {
		t.Fatalf("logrus output = %v (%T), want bare os.Stdout", gotOut, gotOut)
	}
	for _, want := range []string{
		"Could not create logs directory; falling back to stdout-only logging",
		"Logger initialized with stdout output only",
		"File logging disabled",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout output missing %q:\n%s", want, output)
		}
	}
	// logDir is itself a regular file, so bot.log can never have been created
	// under it: Stat on that path either reports the path outright missing
	// (os.IsNotExist, what Windows returns here) or rejects a non-directory
	// path component (syscall.ENOTDIR, what Linux returns for the same
	// blocked path). Both confirm no log file exists; os.IsNotExist alone
	// does not recognise ENOTDIR (it is not ENOENT), which is why this
	// assertion previously failed deterministically on Linux while the
	// stdout fallback above it was already firing correctly.
	if _, err := os.Stat(filepath.Join(logDir, "bot.log")); !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("bot.log stat error = %v, want not-exist (or ENOTDIR from the blocked path) because stdout fallback is active", err)
	}
}

func TestSetupApplicationLoggerCreatesPrivateLogDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}

	logDir := filepath.Join(t.TempDir(), "logs")
	logFilePath := filepath.Join(logDir, "bot.log")
	t.Cleanup(func() { _ = logger.Close() })

	err := setupApplicationLogger("INFO", logFilePath, logger.LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("setupApplicationLogger() error = %v", err)
	}

	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("Stat(logDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != privateRuntimeDirMode {
		t.Fatalf("log dir mode = %v, want %v", got, privateRuntimeDirMode)
	}
}

func writeAPIAuthSuspendedStatus(t *testing.T, statusCode int, errorMsg string) (string, string) {
	t.Helper()

	dataDir := filepath.Join(t.TempDir(), "data")
	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	tracker.UpdateAPIStatusWithErrorInfo(false, 0, errorMsg, status.SourceErrorInfo{
		ErrorCategory: "authentication",
		StatusCode:    statusCode,
	})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	lastCheck := tracker.APIStatusSnapshot().LastCheck.UTC().Format(time.RFC3339)
	return dataDir, lastCheck
}

func writeRansomwareHealthcheckConfig(t *testing.T) string {
	t.Helper()

	return writeMainTestConfig(t, `{
		"api_key": "real-api-key",
		"discord_webhooks": {
			"ransomware": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`)
}

func TestRunHealthcheckReportsAPIAuthSuspension(t *testing.T) {
	const authErrorMsg = "API authentication failed; check api_key and provider access"

	tests := []struct {
		name       string
		locale     string
		statusCode int
		errorMsg   string
		want       string
	}{
		{
			name:       "english names the suspension",
			locale:     "en",
			statusCode: http.StatusForbidden,
			errorMsg:   authErrorMsg,
			want:       "ransomware API polling suspended after HTTP 403",
		},
		{
			name:       "german names the suspension",
			locale:     "de",
			statusCode: http.StatusUnauthorized,
			errorMsg:   authErrorMsg,
			want:       "Ransomware-API-Abfrage nach HTTP 401",
		},
		{
			name:       "non-auth status keeps the generic message",
			locale:     "en",
			statusCode: http.StatusInternalServerError,
			errorMsg:   "provider unavailable",
			want:       "ransomware API last check failed: provider unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := writeRansomwareHealthcheckConfig(t)
			dataDir, lastCheck := writeAPIAuthSuspendedStatus(t, tt.statusCode, tt.errorMsg)

			err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.ForLocale(tt.locale))
			if err == nil {
				t.Fatal("runHealthcheckWithMessages() error = nil, want API failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runHealthcheckWithMessages() error = %v, want it to contain %q", err, tt.want)
			}
			if tt.statusCode == http.StatusInternalServerError {
				if strings.Contains(err.Error(), lastCheck) {
					t.Fatalf("runHealthcheckWithMessages() error = %v, want the generic message without the check timestamp", err)
				}
				return
			}
			if !strings.Contains(err.Error(), tt.errorMsg) {
				t.Fatalf("runHealthcheckWithMessages() error = %v, want it to carry the operator error %q", err, tt.errorMsg)
			}
			// The timestamp is the operator's only way to tell a live suspension
			// from a stale api_status.json read right after a restart.
			if !strings.Contains(err.Error(), lastCheck) {
				t.Fatalf("runHealthcheckWithMessages() error = %v, want it to carry the persisted last check %q", err, lastCheck)
			}
		})
	}
}

func TestRunHealthcheckModeExitsOneWhenAPIAuthSuspended(t *testing.T) {
	configDir := writeRansomwareHealthcheckConfig(t)
	dataDir, lastCheck := writeAPIAuthSuspendedStatus(t, http.StatusForbidden,
		"API authentication failed; check api_key and provider access")

	var out bytes.Buffer
	msg := i18n.ForLocale("en")
	opts := cliOptions{configDir: configDir, dataDir: dataDir, healthcheck: true, locale: "en"}

	if code := runHealthcheckMode(opts, &out, msg); code != 1 {
		t.Fatalf("runHealthcheckMode() = %d, want 1; output=%s", code, out.String())
	}
	wantPrefix := fmt.Sprintf(msg.T("cli.healthcheck_failed"), "")
	wantPrefix = strings.TrimSpace(strings.TrimSuffix(wantPrefix, "\n"))
	if !strings.Contains(out.String(), strings.TrimSpace(wantPrefix)) {
		t.Fatalf("output = %q, want the cli.healthcheck_failed wrapper", out.String())
	}
	if !strings.Contains(out.String(), "ransomware API polling suspended after HTTP 403") {
		t.Fatalf("output = %q, want the auth suspension message", out.String())
	}
	if !strings.Contains(out.String(), lastCheck) {
		t.Fatalf("output = %q, want the persisted last check %q", out.String(), lastCheck)
	}
}

// --- read-only data_dir validation and the RSS healthcheck verdict ---------

const (
	rssHealthFeedA = "https://feed-a.example.com/feed.xml"
	rssHealthFeedB = "https://feed-b.example.com/feed.xml"
	rssHealthFeedC = "https://feed-c.example.com/feed.xml"
	rssHealthFeedD = "https://feed-d.example.com/feed.xml"
)

// feedSeed is one hand-written rss_status.json feed record. Writing the file
// directly is the only way to pin LastSuccess to a chosen instant; the tracker
// API always stamps statusNow().
type feedSeed struct {
	lastSuccess *time.Time
	lastError   string
}

// boolPtrForTest returns a pointer to value, for table rows that need to
// distinguish "not set" (nil) from an explicit false.
func boolPtrForTest(value bool) *bool {
	return &value
}

// seedRSSFeedStatus creates a data dir and writes rss_status.json with exactly
// the given feed records. It returns the data dir.
func seedRSSFeedStatus(t *testing.T, feeds map[string]feedSeed) string {
	t.Helper()

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	writeRSSFeedStatusInto(t, dataDir, feeds)
	return dataDir
}

// writeRSSFeedStatusInto writes rss_status.json into an existing data dir,
// replacing whatever a tracker may have written there before.
func writeRSSFeedStatusInto(t *testing.T, dataDir string, feeds map[string]feedSeed) {
	t.Helper()

	records := make(map[string]status.FeedInfo, len(feeds))
	for feedURL, seed := range feeds {
		info := status.FeedInfo{
			LastCheck:   time.Now().UTC(),
			LastSuccess: seed.lastSuccess,
			SuccessRate: 1.0,
		}
		if seed.lastError != "" {
			lastError := seed.lastError
			info.LastError = &lastError
			info.ConsecutiveFailures = 1
			info.SuccessRate = 0
		}
		records[feedURL] = info
	}

	payload := struct {
		LastUpdated time.Time                  `json:"last_updated"`
		Feeds       map[string]status.FeedInfo `json:"feeds"`
		ParsedItems []struct{}                 `json:"parsed_items"`
		SentItems   map[string]struct{}        `json:"sent_items"`
	}{
		LastUpdated: time.Now().UTC(),
		Feeds:       records,
		ParsedItems: []struct{}{},
		SentItems:   map[string]struct{}{},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(rss_status) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "rss_status.json"), encoded, 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}
}

// writeRSSHealthcheckConfigWithGeneral writes config_general.json plus a
// config_feeds.json holding two general feeds.
func writeRSSHealthcheckConfigWithGeneral(t *testing.T, generalJSON string) string {
	t.Helper()

	configDir := writeMainTestConfig(t, generalJSON)
	feedsJSON := fmt.Sprintf(`{"general_feeds": [%q, %q]}`, rssHealthFeedA, rssHealthFeedB)
	if err := os.WriteFile(filepath.Join(configDir, "config_feeds.json"), []byte(feedsJSON), 0600); err != nil {
		t.Fatalf("WriteFile(config_feeds.json) error = %v", err)
	}
	return configDir
}

// writeRSSHealthcheckConfig enables discord_webhooks.rss and configures the two
// general feeds the RSS healthcheck tests seed status for.
func writeRSSHealthcheckConfig(t *testing.T) string {
	t.Helper()

	return writeRSSHealthcheckConfigWithGeneral(t, `{
		"discord_webhooks": {
			"rss": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`)
}

func rssHealthTestOptions(configDir, dataDir, locale string) cliOptions {
	return cliOptions{configDir: configDir, dataDir: dataDir, locale: locale, healthcheck: true}
}

func TestRunHealthcheckFailsWhenDataDirMissing(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		want   []string
	}{
		{
			name:   "english names the missing volume",
			locale: "en",
			want:   []string{"does not exist", "this check never creates it", "possibly an unmounted volume"},
		},
		{
			name:   "german names the missing volume",
			locale: "de",
			want:   []string{"existiert nicht", "diese Pruefung legt es nicht an", "nicht eingehaengtes Volume"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeMainTestConfig(t, `{}`)
			dataDir := filepath.Join(t.TempDir(), "unmounted", "data")

			err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.ForLocale(tt.locale))
			if err == nil {
				t.Fatal("runHealthcheckWithMessages() error = nil, want missing data_dir failure")
			}
			for _, want := range append(tt.want, dataDir) {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("runHealthcheckWithMessages() error = %v, want it to contain %q", err, want)
				}
			}
			if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
				t.Fatalf("data dir %q exists after the healthcheck (stat err = %v), want it untouched", dataDir, statErr)
			}
		})
	}
}

func TestValidateReadOnlyDataDirRejectsUnstatableAndAcceptsEmptyPath(t *testing.T) {
	// The empty path is the "no data_dir configured" case; it must stay a
	// silent success exactly as validateDataDir treats it.
	if err := validateReadOnlyDataDir(""); err != nil {
		t.Fatalf("validateReadOnlyDataDir(\"\") error = %v, want nil", err)
	}

	// A NUL byte makes os.Stat fail with something other than not-exist, which
	// is the branch an unreadable mount point would hit. Same trick as
	// internal/status TestEnsurePrivateDataDirReportsStatFailure.
	badPath := filepath.Join(t.TempDir(), "bad\x00name")
	err := validateReadOnlyDataDir(badPath)
	if err == nil {
		t.Fatal("validateReadOnlyDataDir(NUL path) error = nil, want a stat failure")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Fatalf("validateReadOnlyDataDir(NUL path) error = %v, want a stat failure", err)
	}
	if errors.Is(err, errDataDirMissing) {
		t.Fatalf("validateReadOnlyDataDir(NUL path) error = %v, want it not classified as a missing directory", err)
	}
}

func TestRunHealthcheckModeExitsOneWhenDataDirMissing(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		want   []string
	}{
		{
			name:   "english",
			locale: "en",
			want:   []string{"Healthcheck failed:", "does not exist", "possibly an unmounted volume"},
		},
		{
			name:   "german",
			locale: "de",
			want:   []string{"Healthcheck fehlgeschlagen:", "existiert nicht", "nicht eingehaengtes Volume"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeMainTestConfig(t, `{}`)
			dataDir := filepath.Join(t.TempDir(), "unmounted", "data")

			var out bytes.Buffer
			msg := i18n.ForLocale(tt.locale)
			if code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, tt.locale), &out, msg); code != 1 {
				t.Fatalf("runHealthcheckMode() = %d, want 1; output:\n%s", code, out.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("healthcheck output missing %q:\n%s", want, out.String())
				}
			}
			if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
				t.Fatalf("data dir %q exists after the healthcheck (stat err = %v), want it untouched", dataDir, statErr)
			}
		})
	}
}

// TestRunHealthcheckChecksDataDirBeforeReadinessMarker pins the check order of
// runHealthcheckWithMessages: the data_dir check runs before
// checkReadinessMarkerWithMessages. Both fail here, and the data_dir message
// must win. Moving the data_dir block below the marker check survived the whole
// suite before this test existed, because every other data_dir test leaves
// RANSOMWARE_BOT_READY_FILE unset — and in the container the marker IS set, so
// an unmounted volume would then be reported as "readiness marker is missing",
// hiding the real cause.
func TestRunHealthcheckChecksDataDirBeforeReadinessMarker(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	marker := filepath.Join(t.TempDir(), "never-written.ready")
	t.Setenv(readinessFileEnv, marker)
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "unmounted", "data")

	// Precondition: the marker check on its own does fail for this path.
	if err := checkReadinessMarkerWithMessages(io.Discard, i18n.Default()); err == nil {
		t.Fatalf("checkReadinessMarkerWithMessages() error = nil for %q, want the marker to be missing", marker)
	}

	err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default())
	if err == nil {
		t.Fatal("runHealthcheckWithMessages() error = nil, want missing data_dir failure")
	}
	if !strings.Contains(err.Error(), "possibly an unmounted volume") || !strings.Contains(err.Error(), dataDir) {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the data_dir message naming %q", err, dataDir)
	}
	if strings.Contains(err.Error(), "readiness marker") {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the data_dir failure to win over the readiness marker", err)
	}
}

func TestRunListDeadLetterFailsWhenDataDirMissing(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "unmounted", "data")

	var out bytes.Buffer
	err := runListDeadLetter(configDir, dataDir, &out)
	if err == nil {
		t.Fatalf("runListDeadLetter() error = nil, want missing data_dir failure; output:\n%s", out.String())
	}
	for _, want := range []string{"data_dir:", "data directory does not exist", dataDir} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("runListDeadLetter() error = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(out.String(), "Dead-letter items:") {
		t.Fatalf("runListDeadLetter() printed a dead-letter listing for a missing data_dir:\n%s", out.String())
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("data dir %q exists after --list-dead-letter (stat err = %v), want it untouched", dataDir, statErr)
	}
}

func TestEvaluateRSSHealth(t *testing.T) {
	base := time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC)
	allowed := rssStaleSuccessIntervals * config.DefaultConfig().RSSPollInterval
	fresh := base
	stale := base.Add(-allowed - time.Second)

	rssEnabledConfig := func(feeds ...string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Feeds.GeneralFeeds = feeds
		cfg.DiscordWebhooks.RSS = config.WebhookConfig{
			Enabled: true,
			URL:     "https://discord.com/api/webhooks/123/abc",
		}
		return cfg
	}

	tests := []struct {
		name       string
		cfg        func() *config.Config
		feeds      map[string]feedSeed
		nilTracker bool
		now        time.Time
		// readyConfirmed and rssMarkerOverrun are Part (b) additions.
		// readyConfirmed defaults to false on every existing row (Go zero
		// value), which keeps them exercising the pre-Part-(b) "first-start
		// grace" path unchanged. rssMarkerOverrun only matters when
		// readyConfirmed is true: nil means no .rss progress marker is
		// written at all (the fail-open "no completed pass recorded yet"
		// case, F1), and a non-nil value writes one with that budget_overrun
		// flag for readRSSProgressMarker to read.
		readyConfirmed   bool
		rssMarkerOverrun *bool
		wantErr          map[string]string // locale -> substring
		wantWarn         map[string]string // locale -> substring
	}{
		{
			name:  "nil config is a zero verdict",
			cfg:   func() *config.Config { return nil },
			feeds: map[string]feedSeed{},
			now:   base,
		},
		{
			name:       "nil tracker is a zero verdict",
			cfg:        func() *config.Config { return rssEnabledConfig(rssHealthFeedA) },
			feeds:      map[string]feedSeed{},
			nilTracker: true,
			now:        base,
		},
		{
			name: "R0 no enabled rss webhook",
			cfg: func() *config.Config {
				cfg := rssEnabledConfig(rssHealthFeedA, rssHealthFeedB)
				cfg.DiscordWebhooks.RSS.Enabled = false
				return cfg
			},
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastError: "RSS feed returned HTTP 503"},
			},
			now: base,
		},
		{
			name:  "R0b enabled feeds have no record yet",
			cfg:   func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB) },
			feeds: map[string]feedSeed{},
			now:   base,
		},
		{
			name: "R4 all feeds ok and fresh",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &fresh},
				rssHealthFeedB: {lastSuccess: &fresh},
			},
			now: base,
		},
		{
			name: "R3 one of three failed while newest is fresh",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB, rssHealthFeedC) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &fresh, lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastSuccess: &fresh},
				rssHealthFeedC: {lastSuccess: &fresh},
			},
			now: base,
			wantWarn: map[string]string{
				"en": "RSS feeds degraded: 1 of 3 enabled feeds failed their last poll",
				"de": "RSS-Feeds beeintraechtigt: 1 von 3 aktivierten Feeds",
			},
		},
		{
			// A transient all-feeds blip while the newest success
			// is still inside the window is a warning, never exit 1.
			name: "R3 three of three failed while newest is fresh",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB, rssHealthFeedC) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &fresh, lastError: "dial tcp: i/o timeout"},
				rssHealthFeedB: {lastSuccess: &fresh, lastError: "dial tcp: i/o timeout"},
				rssHealthFeedC: {lastSuccess: &fresh, lastError: "dial tcp: i/o timeout"},
			},
			now: base,
			wantWarn: map[string]string{
				"en": "RSS feeds degraded: 3 of 3 enabled feeds failed their last poll",
				"de": "RSS-Feeds beeintraechtigt: 3 von 3 aktivierten Feeds",
			},
		},
		{
			name: "R1 three of three failed and newest is stale",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB, rssHealthFeedC) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &stale, lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastSuccess: &stale, lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedC: {lastSuccess: &stale, lastError: "RSS feed returned HTTP 503"},
			},
			now: base,
			wantErr: map[string]string{
				"en": "all 3 enabled RSS feed(s) failed their last poll; last error: RSS feed returned HTTP 503",
				"de": "alle 3 aktivierten RSS-Feeds sind beim letzten Abruf fehlgeschlagen",
			},
		},
		{
			// known == 3 but enabled == 4, so R1 cannot fire; R2 does.
			name: "R2 two of three failed with a fourth feed without a record and no success ever",
			cfg: func() *config.Config {
				return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB, rssHealthFeedC, rssHealthFeedD)
			},
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedC: {},
			},
			now: base,
			wantErr: map[string]string{
				"en": "no enabled RSS feed has succeeded since never (allowed: 1h30m0s = 3 x rss_poll_interval); 2 of 3 feeds report an error",
				"de": "kein aktivierter RSS-Feed war seit nie erfolgreich (erlaubt: 1h30m0s = 3 x rss_poll_interval); 2 von 3 Feeds melden einen Fehler",
			},
		},
		{
			name: "R4 newest exactly allowed old is still fresh",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &fresh},
			},
			now: fresh.Add(allowed),
		},
		{
			name: "R2 newest one second past allowed is stale",
			cfg:  func() *config.Config { return rssEnabledConfig(rssHealthFeedA) },
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &fresh},
			},
			now: fresh.Add(allowed + time.Second),
			wantErr: map[string]string{
				"en": "no enabled RSS feed has succeeded since 2026-09-03T11:00:00Z (allowed: 1h30m0s = 3 x rss_poll_interval); 0 of 1 feeds report an error",
				"de": "kein aktivierter RSS-Feed war seit 2026-09-03T11:00:00Z erfolgreich",
			},
		},
		{
			// RSSPollInterval <= 0 is unreachable after config validation but
			// reachable here: neither R1 nor R2 may fire, R3 decides.
			name: "zero poll interval disables staleness entirely",
			cfg: func() *config.Config {
				cfg := rssEnabledConfig(rssHealthFeedA, rssHealthFeedB)
				cfg.RSSPollInterval = 0
				return cfg
			},
			feeds: map[string]feedSeed{
				rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastError: "RSS feed returned HTTP 503"},
			},
			now: base,
			wantWarn: map[string]string{
				"en": "RSS feeds degraded: 2 of 2 enabled feeds failed their last poll; newest successful poll never",
				"de": "RSS-Feeds beeintraechtigt: 2 von 2 aktivierten Feeds",
			},
		},
		{
			// Part (b): startup finished (readyConfirmed) and the RSS
			// progress marker shows the last completed pass did NOT overrun
			// its budget, so the empty rss_status.json can only be explained
			// by a data_dir the bot cannot write to (F1: this is no longer
			// "postponed by one budget" -- it is read directly off the
			// marker's own recorded outcome).
			name:             "R0b confirmed, last pass completed within budget",
			cfg:              func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB) },
			feeds:            map[string]feedSeed{},
			now:              base,
			readyConfirmed:   true,
			rssMarkerOverrun: boolPtrForTest(false),
			wantErr: map[string]string{
				"en": "RSS is enabled (2 feed(s) configured) but no poll attempt has been recorded since the scheduler finished starting",
				"de": "RSS ist aktiviert (2 Feed(s) konfiguriert), aber seit dem Startabschluss des Schedulers wurde kein Abrufversuch aufgezeichnet",
			},
		},
		{
			// F1: the last completed pass ended in a cycle-budget overrun
			// (rss_check_timeout expired before any feed answered), which is
			// not "genuinely found nothing" and must be reported distinctly,
			// on every cycle -- not just postponed once and then blamed on
			// data_dir forever.
			name:             "R0b confirmed, last pass overran its budget",
			cfg:              func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB) },
			feeds:            map[string]feedSeed{},
			now:              base,
			readyConfirmed:   true,
			rssMarkerOverrun: boolPtrForTest(true),
			wantErr: map[string]string{
				"en": "RSS is enabled (2 feed(s) configured) but the last completed poll pass exhausted its rss_check_timeout",
				"de": "RSS ist aktiviert (2 Feed(s) konfiguriert), aber der letzte abgeschlossene Abrufdurchlauf hat sein rss_check_timeout",
			},
		},
		{
			// Mirror: startup finished but no RSS progress marker has been
			// written yet (the narrow startup race between the RSS marker
			// and the readiness marker), so this must stay the same
			// first-start grace as R0b's unconfirmed rows above -- fail open,
			// not guess.
			name:             "R0b confirmed but no RSS pass recorded yet",
			cfg:              func() *config.Config { return rssEnabledConfig(rssHealthFeedA, rssHealthFeedB) },
			feeds:            map[string]feedSeed{},
			now:              base,
			readyConfirmed:   true,
			rssMarkerOverrun: nil,
		},
	}

	for _, tt := range tests {
		for _, locale := range []string{"en", "de"} {
			t.Run(tt.name+"/"+locale, func(t *testing.T) {
				var tracker *status.Tracker
				if !tt.nilTracker {
					dataDir := seedRSSFeedStatus(t, tt.feeds)
					seeded, err := status.NewReadOnlyTracker(dataDir)
					if err != nil {
						t.Fatalf("NewReadOnlyTracker() error = %v", err)
					}
					tracker = seeded
				}

				if tt.readyConfirmed {
					markerPath := filepath.Join(t.TempDir(), "ransomware-bot.ready")
					if err := os.WriteFile(markerPath, []byte("marker"), 0600); err != nil {
						t.Fatalf("WriteFile(marker) error = %v", err)
					}
					t.Setenv(readinessFileEnv, markerPath)
					if tt.rssMarkerOverrun != nil {
						content := fmt.Sprintf("at=%s\nstale_after=1m\nbudget_overrun=%t\n",
							tt.now.Add(-time.Minute).UTC().Format(time.RFC3339), *tt.rssMarkerOverrun)
						if err := os.WriteFile(markerPath+progressMarkerSuffixRSS, []byte(content), 0600); err != nil {
							t.Fatalf("WriteFile(rss marker) error = %v", err)
						}
					}
				}

				verdict := evaluateRSSHealth(tt.cfg(), tracker, tt.now, i18n.ForLocale(locale), tt.readyConfirmed)

				wantErr := tt.wantErr[locale]
				if wantErr == "" {
					if verdict.err != nil {
						t.Fatalf("evaluateRSSHealth() err = %v, want nil", verdict.err)
					}
				} else {
					if verdict.err == nil {
						t.Fatalf("evaluateRSSHealth() err = nil, want %q", wantErr)
					}
					if !strings.Contains(verdict.err.Error(), wantErr) {
						t.Fatalf("evaluateRSSHealth() err = %v, want it to contain %q", verdict.err, wantErr)
					}
				}

				wantWarn := tt.wantWarn[locale]
				if wantWarn == "" {
					if verdict.warn != "" {
						t.Fatalf("evaluateRSSHealth() warn = %q, want empty", verdict.warn)
					}
				} else {
					if !strings.Contains(verdict.warn, wantWarn) {
						t.Fatalf("evaluateRSSHealth() warn = %q, want it to contain %q", verdict.warn, wantWarn)
					}
					if !strings.HasSuffix(verdict.warn, "\n") {
						t.Fatalf("evaluateRSSHealth() warn = %q, want a trailing newline", verdict.warn)
					}
				}
			})
		}
	}
}

// TestEvaluateRSSHealthPicksTheSortedFirstErrorDeterministically pins that the
// reported error is the one of the lexicographically first failing feed, and
// that it does not move between runs. evaluateRSSHealth must walk the sorted
// slice from config.EnabledRSSFeedURLs, never a map: StatusSummary.LastRSSError
// is picked by Go map iteration order and would make the operator-visible
// message change from probe to probe.
//
// Four distinct errors and 200 repetitions: a map-iteration implementation
// survives one call with probability 1/4, so it cannot survive this loop.
func TestEvaluateRSSHealthPicksTheSortedFirstErrorDeterministically(t *testing.T) {
	feeds := map[string]feedSeed{
		rssHealthFeedA: {lastError: "error-for-feed-a"},
		rssHealthFeedB: {lastError: "error-for-feed-b"},
		rssHealthFeedC: {lastError: "error-for-feed-c"},
		rssHealthFeedD: {lastError: "error-for-feed-d"},
	}
	dataDir := seedRSSFeedStatus(t, feeds)
	tracker, err := status.NewReadOnlyTracker(dataDir)
	if err != nil {
		t.Fatalf("NewReadOnlyTracker() error = %v", err)
	}

	cfg := config.DefaultConfig()
	// Deliberately unsorted, so the function cannot rely on the config order.
	cfg.Feeds.GeneralFeeds = []string{rssHealthFeedC, rssHealthFeedA, rssHealthFeedD, rssHealthFeedB}
	cfg.DiscordWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
	}

	sorted := config.EnabledRSSFeedURLs(cfg)
	if len(sorted) != 4 || sorted[0] != rssHealthFeedA {
		t.Fatalf("EnabledRSSFeedURLs() = %v, want four URLs with %q first", sorted, rssHealthFeedA)
	}
	want := feeds[sorted[0]].lastError

	now := time.Now()
	for i := 0; i < 200; i++ {
		verdict := evaluateRSSHealth(cfg, tracker, now, i18n.Default(), false)
		if verdict.err == nil {
			t.Fatalf("evaluateRSSHealth() err = nil on iteration %d, want the all-feeds-failed error", i)
		}
		if !strings.Contains(verdict.err.Error(), "last error: "+want) {
			t.Fatalf("evaluateRSSHealth() err = %v on iteration %d, want the error of the sorted-first feed %q (%q)",
				verdict.err, i, sorted[0], want)
		}
	}
}

func TestRunHealthcheckFailsWhenAllRSSFeedsFailed(t *testing.T) {
	const seededError = "RSS feed returned HTTP 503"

	tests := []struct {
		name   string
		locale string
		want   []string
	}{
		{
			name:   "english",
			locale: "en",
			want: []string{
				"Healthcheck failed:",
				"all 2 enabled RSS feed(s) failed their last poll; last error: " + seededError,
			},
		},
		{
			name:   "german",
			locale: "de",
			want: []string{
				"Healthcheck fehlgeschlagen:",
				"alle 2 aktivierten RSS-Feeds sind beim letzten Abruf fehlgeschlagen; letzter Fehler: " + seededError,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeRSSHealthcheckConfig(t)
			// No LastSuccess anywhere: newest is zero, so the outage is stale.
			dataDir := seedRSSFeedStatus(t, map[string]feedSeed{
				rssHealthFeedA: {lastError: seededError},
				rssHealthFeedB: {lastError: "dial tcp: i/o timeout"},
			})

			var out bytes.Buffer
			msg := i18n.ForLocale(tt.locale)
			if code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, tt.locale), &out, msg); code != 1 {
				t.Fatalf("runHealthcheckMode() = %d, want 1; output:\n%s", code, out.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("healthcheck output missing %q:\n%s", want, out.String())
				}
			}
			for _, forbidden := range []string{rssHealthFeedA, rssHealthFeedB, "feed-a.example.com", "feed-b.example.com"} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("healthcheck output leaked %q:\n%s", forbidden, out.String())
				}
			}

			// The printed error is byte-equal to the stored FeedInfo.LastError:
			// the healthcheck forwards the operator message the log already
			// emits and never re-formats it.
			tracker, err := status.NewReadOnlyTracker(dataDir)
			if err != nil {
				t.Fatalf("NewReadOnlyTracker() error = %v", err)
			}
			info, ok := tracker.GetRSSFeedInfo(rssHealthFeedA)
			if !ok || info.LastError == nil {
				t.Fatalf("GetRSSFeedInfo(%q) = (%+v, %t), want a stored LastError", rssHealthFeedA, info, ok)
			}
			if *info.LastError != seededError {
				t.Fatalf("stored LastError = %q, want %q", *info.LastError, seededError)
			}
			if !strings.Contains(out.String(), *info.LastError) {
				t.Fatalf("healthcheck output does not carry the stored LastError %q:\n%s", *info.LastError, out.String())
			}
		})
	}
}

func TestRunHealthcheckFailsWhenNoRecentRSSSuccess(t *testing.T) {
	newest := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	newestText := newest.Format(time.RFC3339)

	tests := []struct {
		name   string
		locale string
		want   []string
	}{
		{
			name:   "english",
			locale: "en",
			want: []string{
				"Healthcheck failed:",
				"no enabled RSS feed has succeeded since " + newestText +
					" (allowed: 1h30m0s = 3 x rss_poll_interval); 0 of 2 feeds report an error",
			},
		},
		{
			name:   "german",
			locale: "de",
			want: []string{
				"Healthcheck fehlgeschlagen:",
				"kein aktivierter RSS-Feed war seit " + newestText +
					" erfolgreich (erlaubt: 1h30m0s = 3 x rss_poll_interval); 0 von 2 Feeds melden einen Fehler",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeRSSHealthcheckConfig(t)
			older := newest.Add(-time.Hour)
			dataDir := seedRSSFeedStatus(t, map[string]feedSeed{
				rssHealthFeedA: {lastSuccess: &newest},
				rssHealthFeedB: {lastSuccess: &older},
			})

			var out bytes.Buffer
			msg := i18n.ForLocale(tt.locale)
			if code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, tt.locale), &out, msg); code != 1 {
				t.Fatalf("runHealthcheckMode() = %d, want 1; output:\n%s", code, out.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("healthcheck output missing %q:\n%s", want, out.String())
				}
			}
			for _, forbidden := range []string{rssHealthFeedA, rssHealthFeedB} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("healthcheck output leaked %q:\n%s", forbidden, out.String())
				}
			}
		})
	}
}

func TestRunHealthcheckWarnsWhenSingleRSSFeedFailed(t *testing.T) {
	newest := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	newestText := newest.Format(time.RFC3339)

	tests := []struct {
		name      string
		locale    string
		validText string
		itemsLine string
		wantWarn  string
	}{
		{
			name:      "english",
			locale:    "en",
			validText: "Healthcheck valid",
			itemsLine: "Dead-letter items:",
			wantWarn: "RSS feeds degraded: 1 of 2 enabled feeds failed their last poll; newest successful poll " +
				newestText,
		},
		{
			name:      "german",
			locale:    "de",
			validText: "Healthcheck gueltig",
			itemsLine: "Dead-Letter-Eintraege:",
			wantWarn: "RSS-Feeds beeintraechtigt: 1 von 2 aktivierten Feeds sind beim letzten Abruf fehlgeschlagen; " +
				"letzter erfolgreicher Abruf " + newestText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeRSSHealthcheckConfig(t)
			dataDir := seedRSSFeedStatus(t, map[string]feedSeed{
				rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
				rssHealthFeedB: {lastSuccess: &newest},
			})

			var out bytes.Buffer
			msg := i18n.ForLocale(tt.locale)
			if code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, tt.locale), &out, msg); code != 0 {
				t.Fatalf("runHealthcheckMode() = %d, want 0; output:\n%s", code, out.String())
			}
			output := out.String()
			for _, want := range []string{tt.validText, tt.wantWarn} {
				if !strings.Contains(output, want) {
					t.Fatalf("healthcheck output missing %q:\n%s", want, output)
				}
			}
			if strings.Index(output, tt.wantWarn) < strings.Index(output, tt.itemsLine) {
				t.Fatalf("degraded line must follow %q:\n%s", tt.itemsLine, output)
			}
			for _, forbidden := range []string{rssHealthFeedA, rssHealthFeedB} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("healthcheck output leaked %q:\n%s", forbidden, output)
				}
			}
		})
	}
}

func TestRunHealthcheckIgnoresRSSWhenNoRSSTargetEnabled(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(readinessFileEnv, "")
	configDir := writeRSSHealthcheckConfigWithGeneral(t, `{
		"discord_webhooks": {
			"rss": {
				"enabled": false,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`)
	dataDir := seedRSSFeedStatus(t, map[string]feedSeed{
		rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
		rssHealthFeedB: {lastError: "RSS feed returned HTTP 503"},
	})

	var out bytes.Buffer
	if code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, "en"), &out, i18n.Default()); code != 0 {
		t.Fatalf("runHealthcheckMode() = %d, want 0; output:\n%s", code, out.String())
	}
	for _, forbidden := range []string{"RSS feeds degraded", "enabled RSS feed"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("healthcheck output carries an RSS verdict for a deployment without an enabled RSS webhook:\n%s", out.String())
		}
	}
	if !strings.Contains(out.String(), "Healthcheck valid") {
		t.Fatalf("healthcheck output missing the valid line:\n%s", out.String())
	}
}

func TestRunHealthcheckReportsAPIErrorBeforeRSSVerdict(t *testing.T) {
	totalOutage := map[string]feedSeed{
		rssHealthFeedA: {lastError: "RSS feed returned HTTP 503"},
		rssHealthFeedB: {lastError: "RSS feed returned HTTP 503"},
	}

	tests := []struct {
		name     string
		seed     func(t *testing.T, dataDir string)
		want     string
		notWant  string
		wantCode int
	}{
		{
			name: "generic api error wins over the rss verdict",
			seed: func(t *testing.T, dataDir string) {
				t.Helper()
				tracker, err := status.NewTracker(dataDir)
				if err != nil {
					t.Fatalf("NewTracker() error = %v", err)
				}
				tracker.UpdateAPIStatus(false, 0, "ransomware.live unreachable")
				if err := tracker.SavePendingChanges(); err != nil {
					t.Fatalf("SavePendingChanges() error = %v", err)
				}
			},
			want:     "ransomware API last check failed: ransomware.live unreachable",
			notWant:  "enabled RSS feed(s) failed",
			wantCode: 1,
		},
		{
			name: "401 auth suspension wins over the rss verdict",
			seed: func(t *testing.T, dataDir string) {
				t.Helper()
				tracker, err := status.NewTracker(dataDir)
				if err != nil {
					t.Fatalf("NewTracker() error = %v", err)
				}
				tracker.UpdateAPIStatusWithErrorInfo(false, 0,
					"API authentication failed; check api_key and provider access",
					status.SourceErrorInfo{ErrorCategory: "authentication", StatusCode: http.StatusUnauthorized})
				if err := tracker.SavePendingChanges(); err != nil {
					t.Fatalf("SavePendingChanges() error = %v", err)
				}
			},
			want:     "ransomware API polling suspended after HTTP 401",
			notWant:  "enabled RSS feed(s) failed",
			wantCode: 1,
		},
		{
			name: "dead-letter queue wins over the rss verdict",
			seed: func(t *testing.T, dataDir string) {
				t.Helper()
				tracker, err := status.NewTracker(dataDir)
				if err != nil {
					t.Fatalf("NewTracker() error = %v", err)
				}
				tracker.MarkRetryDeadLetter("api-item", "discord", "api", "Example Corp", "terminal failure")
				if err := tracker.SavePendingChanges(); err != nil {
					t.Fatalf("SavePendingChanges() error = %v", err)
				}
			},
			want:     "dead-letter queue has 1 terminal delivery failure(s)",
			notWant:  "enabled RSS feed(s) failed",
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", "")
			t.Setenv(readinessFileEnv, "")
			configDir := writeRSSHealthcheckConfigWithGeneral(t, `{
				"api_key": "real-api-key",
				"discord_webhooks": {
					"ransomware": {
						"enabled": true,
						"url": "https://discord.com/api/webhooks/123/abc"
					},
					"rss": {
						"enabled": true,
						"url": "https://discord.com/api/webhooks/456/def"
					}
				}
			}`)
			dataDir := filepath.Join(t.TempDir(), "data")
			tt.seed(t, dataDir)
			// Written last so the tracker's own save cannot clobber the seed.
			writeRSSFeedStatusInto(t, dataDir, totalOutage)

			var out bytes.Buffer
			code := runHealthcheckMode(rssHealthTestOptions(configDir, dataDir, "en"), &out, i18n.Default())
			if code != tt.wantCode {
				t.Fatalf("runHealthcheckMode() = %d, want %d; output:\n%s", code, tt.wantCode, out.String())
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("healthcheck output missing %q:\n%s", tt.want, out.String())
			}
			if strings.Contains(out.String(), tt.notWant) {
				t.Fatalf("healthcheck output reports the RSS verdict before %q:\n%s", tt.want, out.String())
			}
		})
	}
}
