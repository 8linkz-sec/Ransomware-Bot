package status

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteAtomicJSONFileSleepsOnlyBetweenFailedRenameAttempts pins the
// finding-1 fix: writeAtomicJSONFile must sleep between failed rename
// attempts, never after the last one, since no retry follows it. The test
// swaps renameFunc to always fail and sleepFunc to a fast counting stub so
// the assertion is deterministic (call count and argument), not wall-clock.
func TestWriteAtomicJSONFileSleepsOnlyBetweenFailedRenameAttempts(t *testing.T) {
	restoreDurabilitySeams(t)
	origSleep := sleepFunc
	t.Cleanup(func() { sleepFunc = origSleep })

	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")

	renameFunc = func(oldPath, newPath string) error {
		return errors.New("simulated rename failure")
	}

	var sleepCalls []time.Duration
	sleepFunc = func(d time.Duration) {
		sleepCalls = append(sleepCalls, d)
	}

	err := writeAtomicJSONFile(target, "sample", map[string]string{"key": "value"})
	if err == nil {
		t.Fatal("writeAtomicJSONFile() error = nil, want the wrapped rename error")
	}

	if len(sleepCalls) != statusRenameAttempts-1 {
		t.Fatalf("sleepFunc call count = %d, want %d (no sleep after the final failed attempt)",
			len(sleepCalls), statusRenameAttempts-1)
	}
	for i, d := range sleepCalls {
		if d != statusRenameRetryDelay {
			t.Fatalf("sleepFunc call %d duration = %v, want %v", i, d, statusRenameRetryDelay)
		}
	}
}
