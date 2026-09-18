package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runShutdownHelperIfRequested runs main() in helper mode when the subprocess
// helper environment variable is set. It returns true in that case.
func runShutdownHelperIfRequested(helperEnv string) bool {
	if os.Getenv(helperEnv) != "1" {
		return false
	}
	os.Args = []string{
		"ransomware-bot",
		"--config-dir",
		os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
		"--data-dir",
		os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
	}
	main()
	return true
}

// driveGracefulShutdown starts the bot in a subprocess with a readiness marker
// configured, waits for the marker, runs beforeSignal, delivers a shutdown
// signal (SIGTERM on Unix, CTRL_BREAK on Windows), and expects a clean exit.
// It returns the combined subprocess output.
func driveGracefulShutdown(t *testing.T, testName, helperEnv string, beforeSignal func(t *testing.T, readyFile string)) string {
	t.Helper()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	logFilePath := filepath.Join(tmpDir, "logs", "bot.log")
	readyFile := filepath.Join(tmpDir, "run", "ransomware-bot.ready")
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{
		"data_dir": %q,
		"log_file_path": %q
	}`, dataDir, logFilePath))

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$") //nolint:gosec // G204: re-execs this test binary; testName is a fixed literal passed by the calling test, not external input
	cmd.Env = append(os.Environ(),
		"DATA_DIR=",
		helperEnv+"=1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR="+configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR="+dataDir,
		readinessFileEnv+"="+readyFile,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	configureShutdownProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting bot subprocess: %v", err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		select {
		case err := <-waitErr:
			t.Fatalf("bot exited before readiness marker appeared: %v; output:\n%s", err, output.String())
		default:
		}
		if time.Now().After(readyDeadline) {
			_ = cmd.Process.Kill()
			<-waitErr
			t.Fatalf("bot did not write readiness marker in time; output:\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if beforeSignal != nil {
		beforeSignal(t, readyFile)
	}

	select {
	case err := <-waitErr:
		t.Fatalf("bot exited before shutdown signal was sent: %v; output:\n%s", err, output.String())
	default:
	}
	if err := sendShutdownSignal(cmd); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			<-waitErr
			t.Fatalf("bot process was already gone when the shutdown signal was sent: %v; output:\n%s", err, output.String())
		}
		_ = cmd.Process.Kill()
		<-waitErr
		t.Skipf("cannot deliver shutdown signal in this environment: %v", err)
	}

	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("bot exited with error after shutdown signal: %v; output:\n%s", err, output.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-waitErr
		t.Fatalf("bot did not exit after shutdown signal; output:\n%s", output.String())
	}

	for _, want := range []string{"Bot started successfully", "Bot stopped successfully"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("shutdown output missing %q:\n%s", want, output.String())
		}
	}
	return output.String()
}

// TestGracefulShutdownOnSignalSubprocess verifies the full runtime lifecycle:
// startup, readiness marker creation, signal-triggered shutdown with exit code
// 0, and readiness marker removal.
func TestGracefulShutdownOnSignalSubprocess(t *testing.T) {
	if runShutdownHelperIfRequested("RANSOMWARE_BOT_SHUTDOWN_HELPER") {
		return
	}

	var readyFile string
	driveGracefulShutdown(t, "TestGracefulShutdownOnSignalSubprocess", "RANSOMWARE_BOT_SHUTDOWN_HELPER",
		func(_ *testing.T, marker string) { readyFile = marker })

	if _, err := os.Stat(readyFile); !os.IsNotExist(err) {
		t.Fatalf("readiness marker still present after shutdown, stat err = %v", err)
	}
}

// TestGracefulShutdownRemovesProgressMarkersSubprocess is the F2 regression
// pin: the existing suite only ever asserted that a fake ProgressRecorder saw
// a call, and that readPollerProgress round-trips hand-written content -- so a
// mutation that dropped stale_after from the content progressMarkers.write
// actually produces (making every real marker fail the staleAfter <= 0
// invariant and every wedge check fail open forever) or that emptied Clear()
// survived the whole suite with green coverage. This drives a real subprocess
// so both mutations are exercised against the real writer and the real
// Stop(): while the bot is running, both progress markers must exist, contain
// a real "stale_after=" line, and round-trip through readPollerProgress; after
// a clean shutdown, neither marker file may still exist.
func TestGracefulShutdownRemovesProgressMarkersSubprocess(t *testing.T) {
	if runShutdownHelperIfRequested("RANSOMWARE_BOT_PROGRESS_MARKER_SHUTDOWN_HELPER") {
		return
	}

	var readyFile string
	driveGracefulShutdown(t, "TestGracefulShutdownRemovesProgressMarkersSubprocess", "RANSOMWARE_BOT_PROGRESS_MARKER_SHUTDOWN_HELPER",
		func(t *testing.T, marker string) {
			readyFile = marker
			for _, suffix := range []string{progressMarkerSuffixAPI, progressMarkerSuffixRSS} {
				data, err := os.ReadFile(marker + suffix)
				if err != nil {
					t.Fatalf("progress marker %s missing while running: %v", suffix, err)
				}
				if !strings.Contains(string(data), "stale_after=") {
					t.Fatalf("marker %s content = %q, want a stale_after line", suffix, data)
				}
				if _, ok := readPollerProgress(marker + suffix); !ok {
					t.Fatalf("readPollerProgress rejected what the writer produced for %s: %q", suffix, data)
				}
			}
		})

	for _, suffix := range []string{progressMarkerSuffixAPI, progressMarkerSuffixRSS} {
		if _, err := os.Stat(readyFile + suffix); !os.IsNotExist(err) {
			t.Fatalf("progress marker %s survived a clean shutdown (stat err = %v)", suffix, err)
		}
	}
}

// TestGracefulShutdownWarnsWhenReadinessMarkerCannotBeRemovedSubprocess pins
// that a failing readiness marker removal during shutdown is only a warning:
// the bot still exits cleanly with code 0.
func TestGracefulShutdownWarnsWhenReadinessMarkerCannotBeRemovedSubprocess(t *testing.T) {
	if runShutdownHelperIfRequested("RANSOMWARE_BOT_SHUTDOWN_WARN_HELPER") {
		return
	}

	output := driveGracefulShutdown(t, "TestGracefulShutdownWarnsWhenReadinessMarkerCannotBeRemovedSubprocess",
		"RANSOMWARE_BOT_SHUTDOWN_WARN_HELPER",
		func(t *testing.T, readyFile string) {
			// Replace the marker file with a non-empty directory so os.Remove
			// fails with an error that is not "not exist".
			if err := os.Remove(readyFile); err != nil {
				t.Fatalf("Remove(readyFile) error = %v", err)
			}
			if err := os.Mkdir(readyFile, 0700); err != nil {
				t.Fatalf("Mkdir(readyFile) error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(readyFile, "blocker"), []byte("x"), 0600); err != nil {
				t.Fatalf("WriteFile(blocker) error = %v", err)
			}
		})

	if !strings.Contains(output, "Failed to remove readiness marker") {
		t.Fatalf("shutdown output missing readiness marker removal warning:\n%s", output)
	}
}
