package status

import (
	"os"
	"path/filepath"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// newLazyAPITrackerWithZeroLengthAPIStatus writes a 0-byte api_status.json
// (the corruption shape finding 4 targets: present but unparseable, not
// missing) and constructs the production lazy-API path
// (scheduler.go's NewTrackerWithRetentionLazyAPI), which must succeed even
// though the file cannot be loaded.
func newLazyAPITrackerWithZeroLengthAPIStatus(t *testing.T) (*Tracker, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, statusAPIFileName), nil, 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}
	tracker, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	return tracker, dir
}

// TestZeroLengthAPIStatusMutationFailureLogsErrorLevel pins finding 4's
// promotion: the recurring "Failed to load API status before API state
// mutation" line -- which fires on every API mutation, forever, while a
// zero-length api_status.json persists -- must log at ERROR, not WARN.
func TestZeroLengthAPIStatusMutationFailureLogsErrorLevel(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	tracker, _ := newLazyAPITrackerWithZeroLengthAPIStatus(t)

	tracker.MarkAPIItemSentToDestination("item-1", "Item 1", "discord.ransomware")
	tracker.MarkAPIItemSentToDestination("item-2", "Item 2", "discord.ransomware")

	const wantMessage = "Failed to load API status before API state mutation"
	var errorCount, warnCount int
	for _, entry := range hook.AllEntries() {
		if entry.Message != wantMessage {
			continue
		}
		switch entry.Level {
		case log.ErrorLevel:
			errorCount++
		case log.WarnLevel:
			warnCount++
		}
	}
	if errorCount != 2 {
		t.Fatalf("ErrorLevel entries for %q = %d, want 2 (one per mutation)", wantMessage, errorCount)
	}
	if warnCount != 0 {
		t.Fatalf("WarnLevel entries for %q = %d, want 0 (promoted to Error)", wantMessage, warnCount)
	}
}

// TestZeroLengthAPIStatusNeverPersistsAndNeverStopsReDelivering pins the
// EXISTING, deliberate behaviour finding 4 does NOT change: a corrupted
// (zero-length) api_status.json is never rewritten by SavePendingChanges
// (the save-skip guard refuses to overwrite what it could not parse), so a
// dedup mark made against a lazily constructed tracker does not survive a
// process restart -- modeled here as a second, independent
// NewTrackerWithRetentionLazyAPI against the same, still-corrupted data_dir
// -- and the same item is seen as never sent again. This is a guard against
// a future accidental "fix" that starts silently discarding the corrupted
// file's bytes -- not a RED/GREEN regression test for this plan.
func TestZeroLengthAPIStatusNeverPersistsAndNeverStopsReDelivering(t *testing.T) {
	dir := t.TempDir()
	apiStatusPath := filepath.Join(dir, statusAPIFileName)
	if err := os.WriteFile(apiStatusPath, nil, 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	assertZeroLength := func(step string) {
		t.Helper()
		info, err := os.Stat(apiStatusPath)
		if err != nil {
			t.Fatalf("%s: Stat(api_status.json) error = %v", step, err)
		}
		if info.Size() != 0 {
			t.Fatalf("%s: api_status.json size = %d, want 0 (still corrupted, not rewritten)", step, info.Size())
		}
	}

	for cycle := 1; cycle <= 2; cycle++ {
		tracker, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
		if err != nil {
			t.Fatalf("cycle %d: NewTrackerWithRetentionLazyAPI() error = %v", cycle, err)
		}
		if tracker.IsAPIItemSentToDestination("item-1", "discord.ransomware") {
			t.Fatalf("cycle %d: item-1 already marked sent even though api_status.json was never "+
				"persisted -- the previous cycle's mark should not have survived", cycle)
		}
		tracker.MarkAPIItemSentToDestination("item-1", "Item 1", "discord.ransomware")
		if err := tracker.SavePendingChanges(); err != nil {
			t.Fatalf("cycle %d: SavePendingChanges() error = %v", cycle, err)
		}
		assertZeroLength("after SavePendingChanges")
	}
}
