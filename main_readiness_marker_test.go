package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeExitsWhenReadinessMarkerCannotBeWrittenSubprocess verifies that a
// started scheduler shuts down again with exit code 1 when the configured
// readiness marker cannot be written. This used to exist only as a Windows
// subprocess test (main_shutdown_windows_test.go): the bot never runs on
// Windows, so the behaviour it pins -- "the bot exits 1 when the readiness
// marker cannot be written" -- was verified only on the platform it never
// deploys to. blockReadinessMarkerDirectory now supplies a
// platform-appropriate obstruction (its Windows half stays in
// main_shutdown_windows_test.go, its Unix half -- which also covers Linux,
// the deployment platform -- is in main_shutdown_unix_test.go) so this one
// test runs, and asserts the same outcome, on both.
func TestRuntimeExitsWhenReadinessMarkerCannotBeWrittenSubprocess(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_READY_FAIL_HELPER") == "1" {
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
	dataDir := filepath.Join(tmpDir, "data")
	logFilePath := filepath.Join(tmpDir, "logs", "bot.log")
	readyFile := blockReadinessMarkerDirectory(t, tmpDir)
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{
		"data_dir": %q,
		"log_file_path": %q
	}`, dataDir, logFilePath))

	output, err := runMainSubprocess(t, "TestRuntimeExitsWhenReadinessMarkerCannotBeWrittenSubprocess", map[string]string{
		"RANSOMWARE_BOT_READY_FAIL_HELPER": "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR":   configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":     dataDir,
		readinessFileEnv:                   readyFile,
	})
	if err == nil {
		t.Fatalf("readiness-failure helper exited successfully, want status 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("readiness-failure helper error = %T %v, want ExitError; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("readiness-failure exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "Failed to write readiness marker") {
		t.Fatalf("output missing readiness marker failure:\n%s", output)
	}
}
