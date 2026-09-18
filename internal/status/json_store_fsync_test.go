package status

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// restoreDurabilitySeams swaps the durability seams back after a test and
// resets the once-per-process directory warning flag, so the WARN-once
// assertions start from a known state even under -count=2.
func restoreDurabilitySeams(t *testing.T) {
	t.Helper()
	origFile, origRename, origDir, origWarned := syncFileFunc, renameFunc, syncDirFunc, dirSyncWarned.Load()
	t.Cleanup(func() {
		syncFileFunc, renameFunc, syncDirFunc = origFile, origRename, origDir
		dirSyncWarned.Store(origWarned)
	})
	dirSyncWarned.Store(false)
}

func TestWriteAtomicJSONFileSyncsFileBeforeRenameAndDirAfter(t *testing.T) {
	restoreDurabilitySeams(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")

	var calls []string
	origFile, origRename, origDir := syncFileFunc, renameFunc, syncDirFunc
	syncFileFunc = func(f *os.File) error {
		calls = append(calls, "file-sync")
		return origFile(f)
	}
	renameFunc = func(oldPath, newPath string) error {
		calls = append(calls, "rename")
		return origRename(oldPath, newPath)
	}
	syncDirFunc = func(directory string) error {
		calls = append(calls, "dir-sync:"+filepath.Base(directory))
		return origDir(directory)
	}

	if err := writeAtomicJSONFile(target, "sample", map[string]string{"key": "value"}); err != nil {
		t.Fatalf("writeAtomicJSONFile() error = %v", err)
	}

	want := []string{"file-sync", "rename", "dir-sync:" + filepath.Base(dir)}
	if strings.Join(calls, " ") != strings.Join(want, " ") {
		t.Fatalf("call order = %v, want %v", calls, want)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", content, err)
	}
	if decoded["key"] != "value" {
		t.Fatalf("target content = %v, want key=value", decoded)
	}
}

func TestWriteAtomicJSONFileFileSyncFailureKeepsPreviousFile(t *testing.T) {
	restoreDurabilitySeams(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")
	previous := []byte("{\n  \"old\": true\n}\n")
	if err := os.WriteFile(target, previous, statusFileMode); err != nil {
		t.Fatalf("WriteFile(previous) error = %v", err)
	}

	syncFailure := errors.New("no space left on device")
	syncFileFunc = func(*os.File) error { return syncFailure }
	renameCalls := 0
	origRename := renameFunc
	renameFunc = func(oldPath, newPath string) error {
		renameCalls++
		return origRename(oldPath, newPath)
	}

	err := writeAtomicJSONFile(target, "sample", map[string]string{"key": "value"})
	if !errors.Is(err, syncFailure) {
		t.Fatalf("writeAtomicJSONFile() error = %v, want %v", err, syncFailure)
	}
	if !strings.Contains(err.Error(), "failed to write temporary sample file") {
		t.Fatalf("writeAtomicJSONFile() error = %v, want temporary write wrapping", err)
	}
	if renameCalls != 0 {
		t.Fatalf("rename attempts = %d, want the rename to be skipped entirely", renameCalls)
	}

	current, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(target) error = %v", readErr)
	}
	if string(current) != string(previous) {
		t.Fatalf("target content = %q, want the previous file untouched (%q)", current, previous)
	}

	// Documented behaviour: like a failed write, a failed file sync leaves the
	// temp file behind; it is truncated by the next save and removed at the
	// next start by cleanupStatusTempFiles.
	tempPath := target + statusTempFileSuffix
	orphan, statErr := os.ReadFile(tempPath)
	if statErr != nil {
		t.Fatalf("ReadFile(temp file) error = %v, want the temp file left in place", statErr)
	}
	if !strings.Contains(string(orphan), `"key": "value"`) {
		t.Fatalf("temp file content = %q, want the payload that could not be flushed", orphan)
	}

	// The other half of that sentence, asserted rather than only claimed: the
	// next successful save must OVERWRITE the orphaned temp file, not append to
	// it. Opening the temp file with O_APPEND instead of O_TRUNC would produce
	// two concatenated JSON documents and rename them over the live status
	// file -- exactly the corruption this change exists to prevent.
	syncFileFunc = func(f *os.File) error { return f.Sync() }
	if err := writeAtomicJSONFile(target, "sample", map[string]string{"next": "save"}); err != nil {
		t.Fatalf("writeAtomicJSONFile() after the failed sync error = %v", err)
	}
	recovered, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(target after recovery) error = %v", readErr)
	}
	var decoded map[string]string
	if err := json.Unmarshal(recovered, &decoded); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v, want a single valid JSON document", recovered, err)
	}
	if len(decoded) != 1 || decoded["next"] != "save" {
		t.Fatalf("target content = %v, want exactly the second payload", decoded)
	}
	if _, tempErr := os.Stat(tempPath); !os.IsNotExist(tempErr) {
		t.Fatalf("temp file still present after the recovering save, stat err = %v", tempErr)
	}
}

func TestWriteAtomicJSONFileDirSyncFailureWarnsOnceAndSucceeds(t *testing.T) {
	degradations := map[string]error{
		"ENOTSUP": syscall.ENOTSUP,
		"EINVAL":  syscall.EINVAL,
	}

	for name, syncErr := range degradations {
		t.Run(name, func(t *testing.T) {
			restoreDurabilitySeams(t)
			hook := logtest.NewGlobal()
			defer hook.Reset()

			dir := t.TempDir()
			target := filepath.Join(dir, "status.json")
			syncDirFunc = func(string) error { return syncErr }

			for round := 0; round < 3; round++ {
				payload := map[string]int{"round": round}
				if err := writeAtomicJSONFile(target, "sample", payload); err != nil {
					t.Fatalf("writeAtomicJSONFile() round %d error = %v, want the save to succeed", round, err)
				}
			}

			content, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			var decoded map[string]int
			if err := json.Unmarshal(content, &decoded); err != nil {
				t.Fatalf("Unmarshal(%q) error = %v", content, err)
			}
			if decoded["round"] != 2 {
				t.Fatalf("target content = %v, want the last write (round 2)", decoded)
			}

			warnings := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, "Status directory fsync failed") {
					continue
				}
				warnings++
				if got := entry.Data["directory"]; got != dir {
					t.Fatalf("warning directory = %v, want %s", got, dir)
				}
				if got := entry.Data["label"]; got != "sample" {
					t.Fatalf("warning label = %v, want sample", got)
				}
				loggedErr, ok := entry.Data["error"].(error)
				if !ok || !errors.Is(loggedErr, syncErr) {
					t.Fatalf("warning error = %v, want %v", entry.Data["error"], syncErr)
				}
			}
			if warnings != 1 {
				t.Fatalf("directory fsync warnings = %d, want exactly 1", warnings)
			}
		})
	}
}

func TestSyncDirPlatformBehaviour(t *testing.T) {
	dir := t.TempDir()

	if err := syncDir(dir); err != nil {
		t.Fatalf("syncDir(%s) error = %v, want nil", dir, err)
	}

	if runtime.GOOS == "windows" {
		// The no-op is justified, not assumed: a real directory Sync fails here.
		handle, err := os.Open(dir)
		if err != nil {
			t.Fatalf("os.Open(dir) error = %v", err)
		}
		syncErr := handle.Sync()
		if closeErr := handle.Close(); closeErr != nil {
			t.Fatalf("Close(dir handle) error = %v", closeErr)
		}
		if syncErr == nil {
			t.Fatal("os.Open(dir).Sync() = nil on Windows, so the compile-time no-op would no longer be justified")
		}
		return
	}

	if err := syncDir(filepath.Join(dir, "missing-directory")); err == nil {
		t.Fatal("syncDir(missing directory) = nil, want an error")
	}
}

func TestWriteAtomicJSONFilePreservesModeAndTempCleanup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")

	if err := writeAtomicJSONFile(target, "sample", map[string]string{"key": "value"}); err != nil {
		t.Fatalf("writeAtomicJSONFile() error = %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat(target) error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != statusFileMode {
			t.Fatalf("target mode = %o, want %o", got, statusFileMode)
		}
	}

	if _, statErr := os.Stat(target + statusTempFileSuffix); !os.IsNotExist(statErr) {
		t.Fatalf("temp file still present after a successful write, stat err = %v", statErr)
	}
}
