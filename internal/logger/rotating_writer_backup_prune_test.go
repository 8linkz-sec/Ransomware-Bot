package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupBackupsIgnoresUnrelatedFiles pins finding 6: cleanupBackups must
// only ever prune files that match the writer's own rotation naming scheme,
// not merely files that start with the log stem and contain the log
// extension somewhere later in the name (the coarse filepath.Glob pattern
// backupPattern() uses). Before the fix, an unrelated operator file such as
// "bot-important-notes.log.bak" sitting next to bot.log matched the glob and
// was deleted purely by age.
func TestCleanupBackupsIgnoresUnrelatedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	unrelated := filepath.Join(tmpDir, "bot-important-notes.log.bak")
	if err := os.WriteFile(unrelated, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", unrelated, err)
	}
	old := time.Now().UTC().AddDate(0, 0, -30)
	if err := os.Chtimes(unrelated, old, old); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", unrelated, err)
	}

	if err := writer.cleanupBackups(time.Now().UTC()); err != nil {
		t.Fatalf("cleanupBackups() error = %v", err)
	}

	if !fileExists(unrelated) {
		t.Fatal("cleanupBackups() deleted an unrelated operator file that is not a rotation backup")
	}
}

// TestCleanupBackupsStillPrunesGenuineBackupsByAge is the companion check:
// tightening the filter in TestCleanupBackupsIgnoresUnrelatedFiles must not
// stop genuine rotation backups from being pruned by age. Covers every shape
// nextBackupName can produce: plain, the ".N" collision-avoidance suffix, and
// the "-<unixnano>" fallback used once all 1000 ".N" slots for one timestamp
// are taken (TestNextBackupNameFallsBackWhenAllSlotsTaken pins the shape of
// that name but never drives it through cleanupBackups/isRotatedBackupName,
// so a regression narrowing backupNamePattern's suffix alternation down to
// only the ".N" case, or only the plain/no-suffix case, would otherwise slip
// past every other test in this package) -- each plus a .gz counterpart.
func TestCleanupBackupsStillPrunesGenuineBackupsByAge(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	backups := []string{
		filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log"),         // plain
		filepath.Join(tmpDir, "bot-20260102T000000.000000000Z.log.gz"),      // plain, compressed
		filepath.Join(tmpDir, "bot-20260103T000000.000000000Z.1.log"),       // ".N" collision suffix
		filepath.Join(tmpDir, "bot-20260104T000000.000000000Z.1.log.gz"),    // ".N" suffix, compressed
		filepath.Join(tmpDir, "bot-20260105T000000.000000000Z-9999.log"),    // "-<unixnano>" fallback
		filepath.Join(tmpDir, "bot-20260106T000000.000000000Z-9999.log.gz"), // fallback, compressed
	}
	for _, path := range backups {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	old := time.Now().UTC().AddDate(0, 0, -30)
	for _, path := range backups {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, err)
		}
	}

	if err := writer.cleanupBackups(time.Now().UTC()); err != nil {
		t.Fatalf("cleanupBackups() error = %v", err)
	}

	for _, path := range backups {
		if fileExists(path) {
			t.Fatalf("genuine backup %q should have been pruned by age", path)
		}
	}
}

// TestCleanupBackupsCountPruneRemovesAllFormsOfPrunedGroup pins removeBackup
// (2026-09-04 audit-retention mutation review, finding F4): it must delete
// every on-disk form of a pruned rotation, not just the first path in its
// group. A group still carrying both its bare file and a finished ".gz" (or
// an in-progress ".gz.tmp") must not leave the compressed form behind past
// its count bound -- self-healing on the next pass, but unpinned until now.
func TestCleanupBackupsCountPruneRemovesAllFormsOfPrunedGroup(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 1,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	oldBare := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	oldGz := oldBare + ".gz"
	newBare := filepath.Join(tmpDir, "bot-20260102T000000.000000000Z.log")
	newGz := newBare + ".gz"
	for _, path := range []string{oldBare, oldGz, newBare, newGz} {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -2)
	newer := now.AddDate(0, 0, -1)
	for _, path := range []string{oldBare, oldGz} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, err)
		}
	}
	for _, path := range []string{newBare, newGz} {
		if err := os.Chtimes(path, newer, newer); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, err)
		}
	}

	if err := writer.cleanupBackups(now); err != nil {
		t.Fatalf("cleanupBackups() error = %v", err)
	}

	if fileExists(oldBare) || fileExists(oldGz) {
		t.Fatalf("pruned group left a form behind: bare exists=%v gz exists=%v", fileExists(oldBare), fileExists(oldGz))
	}
	if !fileExists(newBare) || !fileExists(newGz) {
		t.Fatal("kept group must survive intact")
	}
}
