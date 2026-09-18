package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	"github.com/sirupsen/logrus"
)

// preserveGlobalLoggerState restores the process-global logrus configuration
// after tests that run runCLI runtime-mode paths or logger setup helpers.
func preserveGlobalLoggerState(t *testing.T) {
	t.Helper()

	originalOut := logrus.StandardLogger().Out
	originalFormatter := logrus.StandardLogger().Formatter
	originalLevel := logrus.StandardLogger().Level
	t.Cleanup(func() {
		_ = logger.Close()
		logrus.SetOutput(originalOut)
		logrus.SetFormatter(originalFormatter)
		logrus.SetLevel(originalLevel)
	})
}

func TestRunCLIRejectsUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer

	code := runCLI([]string{"--no-such-flag"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("runCLI() = %d, want 2 for unknown flag", code)
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined") {
		t.Fatalf("flag error output missing parse failure:\n%s", errOut.String())
	}
}

// TestRunCLIHelpFlagExitsZero pins the rule that -h/--help is
// an intentional, successful request for usage text, not a malformed
// invocation, so it must exit 0 like every other successful CLI request --
// distinct from an unknown flag or a bad value, which must still exit 2. The
// usage text still prints to errOut (stderr in production), not out: main.go
// uses a single flags.SetOutput(errOut) for both the help case and the error
// case, and this fix changes only the exit code, not the stream. Full GNU
// parity (git --help, docker --help also print to stdout) is out of scope.
func TestRunCLIHelpFlagExitsZero(t *testing.T) {
	for _, flagSpelling := range []string{"--help", "-h"} {
		t.Run(flagSpelling, func(t *testing.T) {
			var out, errOut bytes.Buffer

			code := runCLI([]string{flagSpelling}, &out, &errOut)
			if code != 0 {
				t.Fatalf("runCLI(%q) = %d, want 0", flagSpelling, code)
			}
			if out.String() != "" {
				t.Fatalf("stdout = %q, want nothing (usage text stays on stderr)", out.String())
			}
			if !strings.Contains(errOut.String(), "Usage of ransomware-news-bot") {
				t.Fatalf("stderr missing usage text:\n%s", errOut.String())
			}
		})
	}
}

func TestRunCLIHealthcheckModeSucceeds(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(readinessFileEnv, "")
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	// --healthcheck no longer creates data_dir.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	var out bytes.Buffer
	code := runCLI([]string{"--healthcheck", "--config-dir", configDir, "--data-dir", dataDir}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("runCLI(--healthcheck) = %d, want 0; output:\n%s", code, out.String())
	}
	for _, want := range []string{"Healthcheck valid", "Data dir: " + dataDir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("healthcheck output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunCLIHealthcheckModeFailsWhenConfigDirMissing(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(readinessFileEnv, "")
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	var out bytes.Buffer
	code := runCLI([]string{"--healthcheck", "--config-dir", missing}, &out, io.Discard)
	if code != 1 {
		t.Fatalf("runCLI(--healthcheck) = %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "Healthcheck failed") {
		t.Fatalf("healthcheck output missing failure message:\n%s", out.String())
	}
}

func TestRunCLIListDeadLetterModeSucceedsWithEmptyQueue(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	// --list-dead-letter no longer creates data_dir.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	var out bytes.Buffer
	code := runCLI([]string{"--list-dead-letter", "--config-dir", configDir, "--data-dir", dataDir}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("runCLI(--list-dead-letter) = %d, want 0; output:\n%s", code, out.String())
	}
	for _, want := range []string{"Dead-letter items: 0", "Data dir: " + dataDir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dead-letter output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunCLIListDeadLetterModeFailsWhenDataDirIsFile(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{}`)
	dataPath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	var out bytes.Buffer
	code := runCLI([]string{"--list-dead-letter", "--config-dir", configDir, "--data-dir", dataPath}, &out, io.Discard)
	if code != 1 {
		t.Fatalf("runCLI(--list-dead-letter) = %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "Error listing dead-letter items") {
		t.Fatalf("dead-letter output missing failure message:\n%s", out.String())
	}
}

func TestRunCLIRuntimeModeFailsWhenConfigDirMissing(t *testing.T) {
	preserveGlobalLoggerState(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	var out bytes.Buffer
	code := runCLI([]string{"--config-dir", missing}, &out, io.Discard)
	if code != 1 {
		t.Fatalf("runCLI() = %d, want 1 for missing config dir; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "does not exist") {
		t.Fatalf("runtime output missing config dir error:\n%s", out.String())
	}
}

func TestRunCLIRuntimeModeFailsWhenConfigJSONInvalid(t *testing.T) {
	preserveGlobalLoggerState(t)
	configDir := writeMainTestConfig(t, `{"log_level":`)

	var out bytes.Buffer
	code := runCLI([]string{"--config-dir", configDir}, &out, io.Discard)
	if code != 1 {
		t.Fatalf("runCLI() = %d, want 1 for invalid config JSON; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "loading configuration") {
		t.Fatalf("runtime output missing configuration load error:\n%s", out.String())
	}
}

func TestRequireConfigDirRejectsMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	err := requireConfigDir(missing)
	if err == nil {
		t.Fatal("requireConfigDir() error = nil, want missing directory failure")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("requireConfigDir() error = %v, want does-not-exist context", err)
	}

	if err := requireConfigDir(t.TempDir()); err != nil {
		t.Fatalf("requireConfigDir(existing) error = %v", err)
	}
}

func TestRunConfiguredSchedulerFailsWhenStaleReadinessMarkerCannotBeRemoved(t *testing.T) {
	preserveGlobalLoggerState(t)

	marker := filepath.Join(t.TempDir(), "ready-marker")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatalf("Mkdir(marker) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(marker, "blocker"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, marker)

	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")

	code := runConfiguredScheduler(cfg, cliOptions{configDir: t.TempDir()})
	if code != 1 {
		t.Fatalf("runConfiguredScheduler() = %d, want 1 when stale readiness marker cannot be removed", code)
	}
}

func TestRunConfiguredSchedulerFailsWhenDataDirAlreadyLocked(t *testing.T) {
	preserveGlobalLoggerState(t)
	t.Setenv(readinessFileEnv, "")

	dataDir := t.TempDir()
	lock, err := status.AcquireDataDirLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireDataDirLock() error = %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir

	code := runConfiguredScheduler(cfg, cliOptions{configDir: writeMainTestConfig(t, `{}`)})
	if code != 1 {
		t.Fatalf("runConfiguredScheduler() = %d, want 1 when data_dir is already locked", code)
	}
}

func TestSetupApplicationLoggerFailsWhenStdoutFallbackRejectsLevelAfterLogDirError(t *testing.T) {
	preserveGlobalLoggerState(t)

	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(logDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(logDir placeholder) error = %v", err)
	}

	err := setupApplicationLogger("NOT_A_LEVEL", filepath.Join(logDir, "bot.log"), logger.LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})
	if err == nil {
		t.Fatal("setupApplicationLogger() error = nil, want stdout fallback failure")
	}
	if !strings.Contains(err.Error(), "stdout logger fallback failed after logs directory error") {
		t.Fatalf("setupApplicationLogger() error = %v, want logs directory fallback context", err)
	}
}

func TestSetupApplicationLoggerFailsWhenStdoutFallbackRejectsLevelAfterFileLoggerError(t *testing.T) {
	preserveGlobalLoggerState(t)

	logFilePath := filepath.Join(t.TempDir(), "logs", "bot.log")
	err := setupApplicationLogger("NOT_A_LEVEL", logFilePath, logger.LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})
	if err == nil {
		t.Fatal("setupApplicationLogger() error = nil, want stdout fallback failure")
	}
	if !strings.Contains(err.Error(), "stdout logger fallback failed after file logger error") {
		t.Fatalf("setupApplicationLogger() error = %v, want file logger fallback context", err)
	}
}

func TestSetupApplicationLoggerFallsBackToStdoutWhenLogFilePathIsDirectory(t *testing.T) {
	preserveGlobalLoggerState(t)

	logFilePath := filepath.Join(t.TempDir(), "bot.log")
	if err := os.Mkdir(logFilePath, 0700); err != nil {
		t.Fatalf("Mkdir(logFilePath) error = %v", err)
	}

	err := setupApplicationLogger("INFO", logFilePath, logger.LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("setupApplicationLogger() error = %v, want stdout fallback without error", err)
	}
}

func TestWriteReadinessMarkerNoopWithoutEnv(t *testing.T) {
	t.Setenv(readinessFileEnv, "")

	if err := writeReadinessMarker(); err != nil {
		t.Fatalf("writeReadinessMarker() error = %v, want nil without %s", err, readinessFileEnv)
	}
}

func TestWriteReadinessMarkerFailsWhenParentPathIsFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, filepath.Join(blocker, "ransomware-bot.ready"))

	err := writeReadinessMarker()
	if err == nil {
		t.Fatal("writeReadinessMarker() error = nil, want marker directory failure")
	}
	// writeReadinessMarker now delegates to writeProgressMarkerFile, so the
	// wrapped cause names that helper's own MkdirAll branch instead of a
	// bespoke phrase.
	if !strings.Contains(err.Error(), "write readiness marker") || !strings.Contains(err.Error(), "create progress marker directory") {
		t.Fatalf("writeReadinessMarker() error = %v, want marker directory context", err)
	}
}

func TestWriteReadinessMarkerFailsWhenPathIsDirectory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready-marker")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatalf("Mkdir(marker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, marker)

	err := writeReadinessMarker()
	if err == nil {
		t.Fatal("writeReadinessMarker() error = nil, want write failure for directory path")
	}
	if !strings.Contains(err.Error(), "write readiness marker") {
		t.Fatalf("writeReadinessMarker() error = %v, want write marker context", err)
	}
}

func TestRemoveReadinessMarkerNoopWithoutEnv(t *testing.T) {
	t.Setenv(readinessFileEnv, "")

	if err := removeReadinessMarker(); err != nil {
		t.Fatalf("removeReadinessMarker() error = %v, want nil without %s", err, readinessFileEnv)
	}
}

func TestRemoveReadinessMarkerFailsOnNonEmptyDirectory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready-marker")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatalf("Mkdir(marker) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(marker, "blocker"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, marker)

	err := removeReadinessMarker()
	if err == nil {
		t.Fatal("removeReadinessMarker() error = nil, want removal failure for non-empty directory")
	}
	if !strings.Contains(err.Error(), "remove readiness marker") {
		t.Fatalf("removeReadinessMarker() error = %v, want remove marker context", err)
	}
}

func TestCheckReadinessMarkerNoopWithoutEnv(t *testing.T) {
	t.Setenv(readinessFileEnv, "")

	var out bytes.Buffer
	if err := checkReadinessMarker(&out); err != nil {
		t.Fatalf("checkReadinessMarker() error = %v, want nil without %s", err, readinessFileEnv)
	}
	if out.Len() != 0 {
		t.Fatalf("checkReadinessMarker() output = %q, want empty without configured marker", out.String())
	}
}

func TestCheckReadinessMarkerRejectsDirectory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready-marker")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatalf("Mkdir(marker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, marker)

	err := checkReadinessMarker(io.Discard)
	if err == nil {
		t.Fatal("checkReadinessMarker() error = nil, want directory rejection")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("checkReadinessMarker() error = %v, want is-a-directory context", err)
	}
}

func TestEnsurePrivateRuntimeDirFailsWhenParentIsFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}

	err := ensurePrivateRuntimeDir(filepath.Join(blocker, "sub"))
	if err == nil {
		t.Fatal("ensurePrivateRuntimeDir() error = nil, want failure for file parent")
	}
	if !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("ensurePrivateRuntimeDir() error = %v, want blocked path context", err)
	}
}

func TestValidateDataDirAllowsEmptyPath(t *testing.T) {
	if err := validateDataDir(""); err != nil {
		t.Fatalf("validateDataDir(\"\") error = %v, want nil for in-memory fallback", err)
	}
}

func TestRunHealthcheckFailsWhenConfigInvalid(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{"log_level":"NOT_A_LEVEL"}`)

	err := runHealthcheck(configDir, filepath.Join(t.TempDir(), "data"), io.Discard)
	if err == nil {
		t.Fatal("runHealthcheck() error = nil, want config validation failure")
	}
	if !strings.Contains(err.Error(), "config:") {
		t.Fatalf("runHealthcheck() error = %v, want config context", err)
	}
}

func TestRunHealthcheckFailsWhenDataDirIsFile(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{}`)
	dataPath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	err := runHealthcheck(configDir, dataPath, io.Discard)
	if err == nil {
		t.Fatal("runHealthcheck() error = nil, want data_dir validation failure")
	}
	if !strings.Contains(err.Error(), "data_dir:") {
		t.Fatalf("runHealthcheck() error = %v, want data_dir context", err)
	}
}

func TestRunListDeadLetterLoadsDataDirFromConfig(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	dataDir := filepath.Join(t.TempDir(), "data")
	// --list-dead-letter no longer creates data_dir.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{"data_dir": %q}`, dataDir))

	var out bytes.Buffer
	if err := runListDeadLetter(configDir, "", &out); err != nil {
		t.Fatalf("runListDeadLetter() error = %v", err)
	}
	for _, want := range []string{"Dead-letter items: 0", "Data dir: " + dataDir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dead-letter output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunListDeadLetterFailsWhenConfigDirMissingWithoutDataDirFlag(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	err := runListDeadLetter(missing, "", io.Discard)
	if err == nil {
		t.Fatal("runListDeadLetter() error = nil, want missing config dir failure")
	}
	if !strings.Contains(err.Error(), "--data-dir was not provided") {
		t.Fatalf("runListDeadLetter() error = %v, want missing data-dir flag context", err)
	}
}

func TestRunListDeadLetterFailsWhenConfigInvalid(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{"log_level":`)

	err := runListDeadLetter(configDir, "", io.Discard)
	if err == nil {
		t.Fatal("runListDeadLetter() error = nil, want config load failure")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Fatalf("runListDeadLetter() error = %v, want load config context", err)
	}
}

func TestRunListDeadLetterFailsWhenDataDirIsFile(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	configDir := writeMainTestConfig(t, `{}`)
	dataPath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	err := runListDeadLetter(configDir, dataPath, io.Discard)
	if err == nil {
		t.Fatal("runListDeadLetter() error = nil, want data_dir validation failure")
	}
	if !strings.Contains(err.Error(), "data_dir:") {
		t.Fatalf("runListDeadLetter() error = %v, want data_dir context", err)
	}
}

func TestWriteDeadLetterListIncludesDestinationID(t *testing.T) {
	var buf bytes.Buffer
	writeDeadLetterList(&buf, "/tmp/ransomware-bot-data", []status.DeadLetterEntry{
		{
			ItemKey:        "api-item-1",
			ItemType:       "api",
			Messenger:      "discord",
			Title:          "Example Corp",
			DestinationID:  "webhook-2",
			RetryCount:     3,
			LastError:      "provider unavailable",
			TerminalReason: status.TerminalReasonMaxAttempts,
			DeadAt:         "2026-06-27 10:30:00",
		},
	})

	if !strings.Contains(buf.String(), "destination_id: webhook-2") {
		t.Fatalf("dead-letter output missing destination_id:\n%s", buf.String())
	}
}
