package status

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

func newTestAuditTracker(t *testing.T, retention RetentionPolicy) (*Tracker, string) {
	t.Helper()
	dataDir := t.TempDir()
	tracker, err := NewTrackerWithRetention(dataDir, retention)
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}
	return tracker, dataDir
}

// recordAuditEvents writes n realistic-shaped delivery audit events straight
// through the tracker's real recording path (RecordDeliveryAuditEvent), which
// is what exercises rotateAuditFileIfNeeded.
func recordAuditEvents(t *testing.T, tracker *Tracker, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
			EventType:     DeliveryAuditEventDeliveryState,
			Source:        "rss",
			ItemType:      "rss",
			ItemKey:       fmt.Sprintf("rss:v2:guid:%064x", i),
			Title:         strings.Repeat("t", 74),
			Messenger:     "slack",
			DestinationID: "slack.rss",
			FeedType:      "rss",
			FeedURL:       "https://example.invalid/feed/" + strings.Repeat("f", 40),
			Outcome:       DeliveryAuditOutcomeDelivered,
		})
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func TestAuditFileRotatesAtMaxSize(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogMaxBackups = 10
	retention.AuditLogMaxAgeDays = 7
	retention.AuditLogCompress = &compress

	tracker, dataDir := newTestAuditTracker(t, retention)
	auditPath := filepath.Join(dataDir, statusAuditFileName)

	// 457 B/line measured for this event shape; 2500 lines (~1.09 MB) crosses
	// the 1 MB cap once and leaves the active file comfortably under it
	// afterwards, so exactly one rotation occurs.
	const total = 2500
	recordAuditEvents(t, tracker, total)

	backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want exactly 1: %v", len(backups), backups)
	}

	activeLines := countLines(t, auditPath)
	backupLines := countLines(t, backups[0])
	if got := activeLines + backupLines; got != total {
		t.Fatalf("total lines across active+backup = %d, want %d (active=%d backup=%d)", got, total, activeLines, backupLines)
	}

	info, err := os.Stat(auditPath)
	if err != nil {
		t.Fatalf("Stat(active) error = %v", err)
	}
	maxBytes := int64(retention.AuditLogMaxSizeMB) * 1024 * 1024
	if info.Size() > maxBytes {
		t.Fatalf("active file size = %d, want <= %d (must not still be growing past the cap)", info.Size(), maxBytes)
	}
}

func TestAuditFileRotationPrunesOldBackupsByCount(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogMaxBackups = 1
	retention.AuditLogMaxAgeDays = 7
	retention.AuditLogCompress = &compress

	tracker, dataDir := newTestAuditTracker(t, retention)

	// 457 B/line measured for this event shape: ~2300 lines fills the first
	// 1 MB backup, ~2300 more forces a second rotation, which must prune the
	// first backup down to AuditLogMaxBackups.
	recordAuditEvents(t, tracker, 5000)

	backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1 (pruned to AuditLogMaxBackups); backups=%v", len(backups), backups)
	}
}

func TestAuditFileRotationPrunesOldBackupsByAge(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogMaxBackups = 10
	retention.AuditLogMaxAgeDays = 1
	retention.AuditLogCompress = &compress

	tracker, dataDir := newTestAuditTracker(t, retention)

	oldBackup := filepath.Join(dataDir, "delivery_audit-20200101T000000.000000000Z.jsonl")
	if err := os.WriteFile(oldBackup, []byte(`{"event_type":"delivered"}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(oldBackup, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	// Force one rotation so cleanup actually runs.
	recordAuditEvents(t, tracker, 2500)

	if _, err := os.Stat(oldBackup); !os.IsNotExist(err) {
		t.Fatalf("expired backup should have been removed by the next rotation, stat err = %v", err)
	}
}

func TestAuditFileRotationCompressesBackupInBackground(t *testing.T) {
	compress := true
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogMaxBackups = 5
	retention.AuditLogMaxAgeDays = 7
	retention.AuditLogCompress = &compress

	tracker, dataDir := newTestAuditTracker(t, retention)
	recordAuditEvents(t, tracker, 2500)

	deadline := time.Now().Add(5 * time.Second)
	var gzPath string
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl.gz"))
		if err != nil {
			t.Fatalf("Glob() error = %v", err)
		}
		if len(matches) == 1 {
			gzPath = matches[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gzPath == "" {
		t.Fatal("timed out waiting for background compression to publish a .gz backup")
	}

	uncompressed, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(uncompressed) != 0 {
		t.Fatalf("uncompressed backup left behind: %v", uncompressed)
	}

	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("Open(.gz) error = %v", err)
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan gzip content: %v", err)
	}
	if lines == 0 {
		t.Fatal("decompressed backup has no lines")
	}
}

func TestAuditFileRotationGroupsInFlightCompressionAsOneBackup(t *testing.T) {
	dataDir := t.TempDir()
	auditPath := filepath.Join(dataDir, statusAuditFileName)

	stem1 := filepath.Join(dataDir, "delivery_audit-20260101T000000.000000000Z.jsonl")
	stem2 := filepath.Join(dataDir, "delivery_audit-20260102T000000.000000000Z.jsonl")
	stem3 := filepath.Join(dataDir, "delivery_audit-20260103T000000.000000000Z.jsonl")

	write := func(p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", p, err)
		}
	}
	// Group 1: finished bare + gz.
	write(stem1)
	write(stem1 + ".gz")
	// Group 2: finished gz only.
	write(stem2 + ".gz")
	// Group 3: mid-compression (bare + gz.tmp), the newest.
	write(stem3)
	write(stem3 + ".gz.tmp")

	now := time.Now()
	chtimes := map[string]time.Time{
		stem1:             now.Add(-3 * time.Hour),
		stem1 + ".gz":     now.Add(-3 * time.Hour),
		stem2 + ".gz":     now.Add(-2 * time.Hour),
		stem3:             now.Add(-1 * time.Hour),
		stem3 + ".gz.tmp": now.Add(-1 * time.Hour),
	}
	for p, mt := range chtimes {
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", p, err)
		}
	}

	if err := log.CleanupBackups(auditPath, 3, 0, now); err != nil {
		t.Fatalf("CleanupBackups() error = %v", err)
	}

	for _, p := range []string{stem1, stem1 + ".gz", stem2 + ".gz", stem3, stem3 + ".gz.tmp"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %q to survive (3 logical backups, cap 3), stat err = %v", p, err)
		}
	}
}

func TestAuditFileRotationKeepsIntactBackupWhenCompressionCrashed(t *testing.T) {
	dataDir := t.TempDir()
	auditPath := filepath.Join(dataDir, statusAuditFileName)

	intact := filepath.Join(dataDir, "delivery_audit-20260101T000000.000000000Z.jsonl")
	truncatedGz := intact + ".gz"
	other := filepath.Join(dataDir, "delivery_audit-20260102T000000.000000000Z.jsonl")
	oldest := filepath.Join(dataDir, "delivery_audit-20251201T000000.000000000Z.jsonl")

	for _, p := range []string{intact, truncatedGz, other, oldest} {
		if err := os.WriteFile(p, []byte("data\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", p, err)
		}
	}

	now := time.Now()
	// intact predates its own truncated .gz (a crashed compression leaves the
	// .gz with the newer mtime), so the group's overall modTime is dominated
	// by the .gz -- that is what must save intact from count pruning.
	if err := os.Chtimes(intact, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Chtimes(intact) error = %v", err)
	}
	if err := os.Chtimes(truncatedGz, now, now); err != nil {
		t.Fatalf("Chtimes(gz) error = %v", err)
	}
	if err := os.Chtimes(other, now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("Chtimes(other) error = %v", err)
	}
	if err := os.Chtimes(oldest, now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("Chtimes(oldest) error = %v", err)
	}

	if err := log.CleanupBackups(auditPath, 2, 0, now.Add(time.Hour)); err != nil {
		t.Fatalf("CleanupBackups() error = %v", err)
	}

	if _, err := os.Stat(intact); err != nil {
		t.Fatalf("intact backup must survive: stat err = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other backup must survive: stat err = %v", err)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("genuinely oldest backup should have been pruned, stat err = %v", err)
	}
}

// TestUpdateRetentionAppliesNewAuditRotationBoundsWithoutTouchingTMutex has
// two jobs. First, it is a race probe: it hammers UpdateRetention and
// RecordDeliveryAuditEvent concurrently under -race to prove the audit write
// path (t.auditMu only) never touches t.mutex-protected state, which is what
// its name promises -- this is the role that already caught the mutation
// where rotateAuditFileIfNeeded read t.retention directly. Second, and this
// is what a 2026-09-04 mutation review found missing, it must actually assert
// that a hot-reloaded bound takes effect: deleting the
// t.setAuditRotationSnapshot(t.retention) call in UpdateRetention (the real
// hot-reload path -- an operator edits max_size_mb and reloads) survived the
// whole suite because nothing here checked the loaded snapshot. After the
// concurrent stress settles, one final deterministic UpdateRetention pins
// that the new bound is the one actually stored.
func TestUpdateRetentionAppliesNewAuditRotationBoundsWithoutTouchingTMutex(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy() // AuditLogMaxSizeMB starts at the 60 MiB default.
	retention.AuditLogCompress = &compress
	tracker, _ := newTestAuditTracker(t, retention)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r := retention
			r.AuditLogMaxSizeMB = 1 + i%5
			tracker.UpdateRetention(r)
		}
	}()

	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
					EventType: DeliveryAuditEventDeliveryState,
					ItemType:  "rss",
					ItemKey:   fmt.Sprintf("k-%d-%d", id, i),
					Outcome:   DeliveryAuditOutcomeDelivered,
				})
			}
		}(w)
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Deterministic settle: apply one more explicit reload and check the
	// bound it produced actually reached the snapshot the audit write path
	// reads. Without setAuditRotationSnapshot in UpdateRetention, this stays
	// at whatever the concurrent goroutine last happened to leave behind
	// instead of tracking the final call.
	final := retention
	final.AuditLogMaxSizeMB = 1
	tracker.UpdateRetention(final)

	snap := tracker.auditRotation.Load()
	if snap == nil {
		t.Fatal("auditRotation snapshot is nil after UpdateRetention")
	}
	wantBytes := int64(1) * 1024 * 1024
	if snap.maxSizeBytes != wantBytes {
		t.Fatalf("auditRotation snapshot maxSizeBytes = %d, want %d (AuditLogMaxSizeMB=1 not applied by UpdateRetention)", snap.maxSizeBytes, wantBytes)
	}
}

func TestRotateAuditFileIfNeededIsNoOpWhenFileMissing(t *testing.T) {
	tracker, dataDir := newTestAuditTracker(t, DefaultRetentionPolicy())
	missing := filepath.Join(dataDir, "does-not-exist.jsonl")
	if err := tracker.rotateAuditFileIfNeeded(missing, 10); err != nil {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v, want nil for a missing file", err)
	}
}

func TestRotateAuditFileIfNeededIsNoOpWhenUnderCap(t *testing.T) {
	tracker, dataDir := newTestAuditTracker(t, DefaultRetentionPolicy())
	path := filepath.Join(dataDir, statusAuditFileName)
	if err := os.WriteFile(path, []byte("small\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := tracker.rotateAuditFileIfNeeded(path, 10); err != nil {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("unexpected rotation under cap: %v", backups)
	}
}

func TestRotateAuditFileIfNeededIsNoOpWhenSnapshotNil(t *testing.T) {
	tracker := NewMemoryTrackerWithRetention(DefaultRetentionPolicy())
	dataDir := t.TempDir()
	tracker.dataDir = dataDir
	path := filepath.Join(dataDir, statusAuditFileName)
	big := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := tracker.rotateAuditFileIfNeeded(path, 10); err != nil {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("in-memory tracker's nil snapshot must never rotate: %v", backups)
	}
}

func TestRotateAuditFileIfNeededIsNoOpWhenFileEmpty(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	tracker, dataDir := newTestAuditTracker(t, retention)
	path := filepath.Join(dataDir, statusAuditFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// incomingBytes alone exceeds the 1 MiB cap, so only the explicit
	// info.Size() == 0 guard -- not the size comparison -- can be what stops
	// rotateAuditFileIfNeeded from rotating an empty file. Without that
	// guard, 0+incomingBytes <= maxSizeBytes is false and rotation proceeds.
	const incoming = 2 * 1024 * 1024
	if err := tracker.rotateAuditFileIfNeeded(path, incoming); err != nil {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("unexpected rotation of an empty file: %v", backups)
	}
}

// TestRotateAuditFileIfNeededBoundaryAtExactCap pins the exact rotation
// boundary: a file that would land AT the cap (size+incoming == maxSizeBytes)
// must not rotate, and one byte over must. TESTING.md documents the identical
// "<=" vs "<" gap for the log writer's own rotation check as a pinned known
// gap rather than a test; here the boundary is asserted directly instead,
// since the two cases are cheap to drive precisely through
// rotateAuditFileIfNeeded's incomingBytes parameter.
func TestRotateAuditFileIfNeededBoundaryAtExactCap(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	const maxSizeBytes = int64(1) * 1024 * 1024

	t.Run("size+incoming equal to cap does not rotate", func(t *testing.T) {
		tracker, dataDir := newTestAuditTracker(t, retention)
		path := filepath.Join(dataDir, statusAuditFileName)
		const incoming = 100
		existing := strings.Repeat("x", int(maxSizeBytes-incoming))
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := tracker.rotateAuditFileIfNeeded(path, incoming); err != nil {
			t.Fatalf("rotateAuditFileIfNeeded() error = %v", err)
		}
		backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
		if err != nil {
			t.Fatalf("Glob() error = %v", err)
		}
		if len(backups) != 0 {
			t.Fatalf("rotated exactly at the cap, want no rotation: %v", backups)
		}
	})

	t.Run("size+incoming one byte over cap rotates", func(t *testing.T) {
		tracker, dataDir := newTestAuditTracker(t, retention)
		path := filepath.Join(dataDir, statusAuditFileName)
		const incoming = 101
		existing := strings.Repeat("x", int(maxSizeBytes-incoming+1))
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := tracker.rotateAuditFileIfNeeded(path, incoming); err != nil {
			t.Fatalf("rotateAuditFileIfNeeded() error = %v", err)
		}
		backups, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl*"))
		if err != nil {
			t.Fatalf("Glob() error = %v", err)
		}
		if len(backups) != 1 {
			t.Fatalf("did not rotate one byte over the cap: %v", backups)
		}
	})
}

func TestRotateAuditFileIfNeededReportsStatError(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	tracker, dataDir := newTestAuditTracker(t, retention)

	badPath := filepath.Join(dataDir, "bad\x00name.jsonl")
	err := tracker.rotateAuditFileIfNeeded(badPath, 10)
	if err == nil || !strings.Contains(err.Error(), "stat delivery audit file") {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v, want a stat failure", err)
	}
}

func TestRotateAuditFileIfNeededReportsRenameError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the backup rename")
	}

	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	tracker, dataDir := newTestAuditTracker(t, retention)

	path := filepath.Join(dataDir, statusAuditFileName)
	big := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	blocker, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer blocker.Close()

	err = tracker.rotateAuditFileIfNeeded(path, 10)
	if err == nil || !strings.Contains(err.Error(), "rotate delivery audit file") {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v, want a rotate failure", err)
	}
}

func TestRotateAuditFileIfNeededReportsCleanupGlobError(t *testing.T) {
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	tracker, _ := newTestAuditTracker(t, retention)

	badDir := filepath.Join(t.TempDir(), "bad[dir")
	if err := os.MkdirAll(badDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(badDir, statusAuditFileName)
	big := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := tracker.rotateAuditFileIfNeeded(path, 10)
	if err == nil || !strings.Contains(err.Error(), "prune delivery audit backups") {
		t.Fatalf("rotateAuditFileIfNeeded() error = %v, want a prune failure", err)
	}
}

func TestRotateAuditFileIfNeededLaunchesCompressionWhenEnabled(t *testing.T) {
	compress := true
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogMaxBackups = 5
	retention.AuditLogCompress = &compress
	tracker, dataDir := newTestAuditTracker(t, retention)

	// Waiting for the published .gz is what makes this test assert its own
	// name: a bare rotated delivery_audit-*.jsonl exists whether or not the
	// compression goroutine was ever launched, so globbing for it proves
	// nothing about compression. The .gz can only appear because
	// rotateAuditFileIfNeeded launched it.
	//
	// The wait also closes a real flake. Returning right after the rotation
	// rename let t.TempDir's RemoveAll race the still-running goroutine's own
	// os.OpenFile(".gz.tmp", O_CREATE, ...), which could create a directory
	// entry between RemoveAll's last (empty) directory read and its rmdir --
	// "The directory is not empty" on Windows under -race. Waiting is not
	// merely a smaller window: CompressBackupFile closes both file handles
	// and renames tempPath -> compressedPath before its only remaining step,
	// removing the bare source. A deletion cannot make rmdir see a non-empty
	// directory, and no handle is left open to block one, so once the .gz is
	// observed the race is structurally over rather than less likely.
	recordAuditEvents(t, tracker, 2500)

	deadline := time.Now().Add(5 * time.Second)
	var gzPath string
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dataDir, "delivery_audit-*.jsonl.gz"))
		if err != nil {
			t.Fatalf("Glob() error = %v", err)
		}
		if len(matches) == 1 {
			gzPath = matches[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gzPath == "" {
		t.Fatal("timed out waiting for background compression to publish a .gz backup")
	}
}

func TestRecordDeliveryAuditEventWarnsButStillAppendsWhenRotationFails(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "bad[dir")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	compress := false
	retention := DefaultRetentionPolicy()
	retention.AuditLogMaxSizeMB = 1
	retention.AuditLogCompress = &compress
	tracker, err := NewTrackerWithRetention(dataDir, retention)
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}

	auditPath := filepath.Join(dataDir, statusAuditFileName)
	big := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(auditPath, []byte(big), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
		EventType: DeliveryAuditEventDeliveryState,
		ItemType:  "rss",
		ItemKey:   "rotation-warn-item",
		Outcome:   DeliveryAuditOutcomeDelivered,
	})

	lines := countLines(t, auditPath)
	if lines != 1 {
		t.Fatalf("active audit file line count = %d, want 1 (event still appended despite rotation/cleanup failure)", lines)
	}
}
