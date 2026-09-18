//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// configureShutdownProcAttr places the subprocess in its own process group so
// a CTRL_BREAK event can be delivered without affecting the test process.
func configureShutdownProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// sendShutdownSignal delivers a CTRL_BREAK event, which the Go runtime maps to
// os.Interrupt for signal.Notify listeners.
func sendShutdownSignal(cmd *exec.Cmd) error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GenerateConsoleCtrlEvent")
	const ctrlBreakEvent = 1
	ret, _, err := proc.Call(uintptr(ctrlBreakEvent), uintptr(cmd.Process.Pid))
	if ret == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", err)
	}
	return nil
}

// blockReadinessMarkerDirectory is the Windows half of
// TestRuntimeExitsWhenReadinessMarkerCannotBeWrittenSubprocess
// (main_readiness_marker_test.go; main_shutdown_unix_test.go carries the
// other half). It places a regular file where the marker's parent directory
// needs to be: writeReadinessMarker's MkdirAll then fails immediately because
// os.Stat sees a non-directory, and the earlier stale-marker removal in
// runConfiguredScheduler no-ops on the way there because Windows reports a
// missing parent directory component as "not found" (os.IsNotExist true for
// ERROR_PATH_NOT_FOUND).
func blockReadinessMarkerDirectory(t *testing.T, tmpDir string) string {
	t.Helper()
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	return filepath.Join(blocker, "ransomware-bot.ready")
}
