package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireDataDirLockRejectsFileDataDir(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "data-dir-is-a-file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := AcquireDataDirLock(filePath)
	if err == nil || !strings.Contains(err.Error(), "prepare data_dir lock directory") {
		t.Fatalf("AcquireDataDirLock() error = %v, want directory preparation failure", err)
	}
}

func TestAcquireDataDirLockFailsWhenLockPathIsDirectory(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, dataDirLockFileName), 0700); err != nil {
		t.Fatalf("Mkdir(lock blocker) error = %v", err)
	}

	_, err := AcquireDataDirLock(dataDir)
	if err == nil || !strings.Contains(err.Error(), "open data_dir lock") {
		t.Fatalf("AcquireDataDirLock() error = %v, want lock open failure", err)
	}
}

func TestDataDirLockReleaseNilAndIdempotent(t *testing.T) {
	var nilLock *DataDirLock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("Release() on nil lock error = %v, want nil", err)
	}

	lock, err := AcquireDataDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("AcquireDataDirLock() error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release() error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release() error = %v, want nil for released lock", err)
	}
}

func TestDataDirLockReleaseReportsUnlockAndCloseErrors(t *testing.T) {
	lock, err := AcquireDataDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("AcquireDataDirLock() error = %v", err)
	}
	// Sabotage the handle so unlock and close both fail during Release.
	if err := lock.file.Close(); err != nil {
		t.Fatalf("Close(lock file) error = %v", err)
	}

	releaseErr := lock.Release()
	if releaseErr == nil {
		t.Fatal("Release() on closed lock file returned nil error")
	}
	if !strings.Contains(releaseErr.Error(), "unlock data_dir lock") {
		t.Fatalf("Release() error = %v, want unlock failure context", releaseErr)
	}
	if !strings.Contains(releaseErr.Error(), "close data_dir lock") {
		t.Fatalf("Release() error = %v, want close failure context", releaseErr)
	}
}
