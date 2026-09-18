package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestRunHealthcheckReportsDataDirLockUnsupportedAsInformationNeverFailure is
// the core proof of the operator's decision: a data_dir whose filesystem does
// not support advisory locking (ENOLCK/EINVAL) must be reported by
// --healthcheck as information printed alongside the normal success summary,
// and must never fail the check -- a failing healthcheck restart-loops the
// container, which is exactly the outage the "refuse to start" remedy was
// rejected for.
func TestRunHealthcheckReportsDataDirLockUnsupportedAsInformationNeverFailure(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(readinessFileEnv, "")
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	original := probeLockAcquireFunc
	probeLockAcquireFunc = func(*os.File) error { return syscall.ENOLCK }
	t.Cleanup(func() { probeLockAcquireFunc = original })

	var out bytes.Buffer
	code := runCLI([]string{"--healthcheck", "--config-dir", configDir, "--data-dir", dataDir}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("runCLI(--healthcheck) = %d, want 0 (information, never a failure); output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "Healthcheck valid") {
		t.Fatalf("healthcheck output missing success line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "does not support file locking") {
		t.Fatalf("healthcheck output missing the lock-unsupported information line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/mnt/user") {
		t.Fatalf("healthcheck output missing the Unraid remedy detail:\n%s", out.String())
	}
}

// TestRunHealthcheckOmitsLockUnsupportedLineWhenLockingWorks is the regression
// guard: on a normal filesystem (the unmocked default, real flock/LockFileEx),
// the informational line must not appear.
func TestRunHealthcheckOmitsLockUnsupportedLineWhenLockingWorks(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(readinessFileEnv, "")
	configDir := writeMainTestConfig(t, `{}`)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	var out bytes.Buffer
	code := runCLI([]string{"--healthcheck", "--config-dir", configDir, "--data-dir", dataDir}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("runCLI(--healthcheck) = %d, want 0; output:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "does not support file locking") {
		t.Fatalf("healthcheck output unexpectedly contains the lock-unsupported information line:\n%s", out.String())
	}
}

// TestProbeDataDirLockingUnsupportedClassifiesENOLCKAndEINVAL is a direct,
// behavioural unit test of the probe helper.
func TestProbeDataDirLockingUnsupportedClassifiesENOLCKAndEINVAL(t *testing.T) {
	dataDir := t.TempDir()

	for _, errno := range []syscall.Errno{syscall.ENOLCK, syscall.EINVAL} {
		original := probeLockAcquireFunc
		probeLockAcquireFunc = func(*os.File) error { return errno }
		if !probeDataDirLockingUnsupported(dataDir) {
			probeLockAcquireFunc = original
			t.Fatalf("probeDataDirLockingUnsupported() = false for %v, want true", errno)
		}
		probeLockAcquireFunc = original
	}
}

// TestProbeDataDirLockingUnsupportedOnRealFilesystemReturnsFalse exercises the
// unmocked path: this repository's dev/CI filesystems support advisory
// locking, so the probe must report false without any injection.
func TestProbeDataDirLockingUnsupportedOnRealFilesystemReturnsFalse(t *testing.T) {
	if probeDataDirLockingUnsupported(t.TempDir()) {
		t.Fatal("probeDataDirLockingUnsupported() = true on a normal filesystem, want false")
	}
}

// TestProbeDataDirLockingUnsupportedReturnsFalseWhenTempFileCannotBeCreated
// pins the documented best-effort fallback: if the probe cannot even create
// its throwaway temp file (for example a directory that no longer exists),
// it reports "supported" rather than adding a second, redundant failure mode
// -- the data_dir validation this runs after already covers that.
func TestProbeDataDirLockingUnsupportedReturnsFalseWhenTempFileCannotBeCreated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if probeDataDirLockingUnsupported(missing) {
		t.Fatal("probeDataDirLockingUnsupported() = true when the temp file could not be created, want false")
	}
}
