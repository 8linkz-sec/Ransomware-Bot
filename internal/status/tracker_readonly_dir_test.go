package status

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The read-only constructors back --healthcheck and --list-dead-letter. A
// read-only surface that created data_dir would turn an unmounted volume into
// an empty, healthy-looking state, and the chmod to 0700 would tighten a
// directory the bot's UID shares with the host.

func TestNewReadOnlyTrackerDoesNotCreateMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-mounted")

	tracker, err := NewReadOnlyTracker(dataDir)
	if err == nil {
		t.Fatalf("NewReadOnlyTracker() error = nil (tracker != nil: %t), want missing data directory failure", tracker != nil)
	}
	if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), fmt.Sprintf("%q", dataDir)) {
		t.Fatalf("NewReadOnlyTracker() error = %v, want a missing-directory message naming %q", err, dataDir)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("data dir %q exists after the read-only constructor (stat err = %v), want it untouched", dataDir, statErr)
	}
}

func TestNewReadOnlyTrackerLazyAPIDoesNotCreateMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-mounted-lazy")

	tracker, err := NewReadOnlyTrackerLazyAPI(dataDir)
	if err == nil {
		t.Fatalf("NewReadOnlyTrackerLazyAPI() error = nil (tracker != nil: %t), want missing data directory failure", tracker != nil)
	}
	if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), fmt.Sprintf("%q", dataDir)) {
		t.Fatalf("NewReadOnlyTrackerLazyAPI() error = %v, want a missing-directory message naming %q", err, dataDir)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("data dir %q exists after the lazy read-only constructor (stat err = %v), want it untouched", dataDir, statErr)
	}
}

func TestNewReadOnlyTrackerKeepsDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows; ensurePrivateDataDir guards the chmod with runtime.GOOS != \"windows\"")
	}

	dataDir := filepath.Join(t.TempDir(), "shared-data")
	const sharedMode os.FileMode = 0750
	if err := os.Mkdir(dataDir, sharedMode); err != nil {
		t.Fatalf("Mkdir(dataDir) error = %v", err)
	}
	if err := os.Chmod(dataDir, sharedMode); err != nil {
		t.Fatalf("Chmod(dataDir) error = %v", err)
	}

	if _, err := NewReadOnlyTracker(dataDir); err != nil {
		t.Fatalf("NewReadOnlyTracker() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != sharedMode {
		t.Fatalf("data dir mode = %v after the read-only constructor, want %v (unchanged)", got, sharedMode)
	}
}

func TestNewReadOnlyTrackerRejectsFileDataDir(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := NewReadOnlyTracker(filePath)
	if err == nil {
		t.Fatal("NewReadOnlyTracker() error = nil, want not-a-directory failure")
	}
	if !strings.Contains(err.Error(), "is not a directory") || !strings.Contains(err.Error(), fmt.Sprintf("%q", filePath)) {
		t.Fatalf("NewReadOnlyTracker() error = %v, want a not-a-directory message naming %q", err, filePath)
	}
	if strings.Contains(err.Error(), "failed to create data directory") {
		t.Fatalf("NewReadOnlyTracker() error = %v, want no creation wording on a read-only surface", err)
	}
}

// TestRequireExistingDataDirReportsStatFailure reaches the non-ENOENT stat
// branch. A NUL byte makes os.Stat fail with something other than not-exist on
// Windows and Linux alike; same trick as TestEnsurePrivateDataDirReportsStatFailure.
func TestRequireExistingDataDirReportsStatFailure(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "bad\x00name")

	err := requireExistingDataDir(badPath)
	if err == nil {
		t.Fatal("requireExistingDataDir() error = nil, want a stat failure")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Fatalf("requireExistingDataDir() error = %v, want a stat failure", err)
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("requireExistingDataDir() error = %v, want it not reported as a missing directory", err)
	}
}

func TestNewTrackerStillCreatesMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "writer-data")

	if _, err := NewTracker(dataDir); err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v, want the writer path to have created it", err)
	}
	if !info.IsDir() {
		t.Fatalf("data dir %q is not a directory after NewTracker()", dataDir)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := info.Mode().Perm(); got != os.FileMode(privateDataDirMode) {
		t.Fatalf("data dir mode = %v, want %v", got, os.FileMode(privateDataDirMode))
	}
}
