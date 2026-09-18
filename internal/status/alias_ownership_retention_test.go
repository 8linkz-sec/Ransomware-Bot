package status

import (
	"testing"
	"time"
)

// aliasOwnershipSleepMargin separates writes in time so retention's
// oldest-first eviction (both the API count-only heap and the RSS
// age/count-bounded sort) has an unambiguous answer instead of a coin flip.
// statusNow() (time.Now().UTC()) has been measured at ~555us granularity on
// this host, with three consecutive calls sometimes returning byte-identical
// values; 2ms is the smallest margin with real headroom. Every test in this
// file that depends on SentAt ordering sleeps this long between the writes
// whose relative order the assertions rely on.
const aliasOwnershipSleepMargin = 2 * time.Millisecond

// apiSentTo replicates apiItemSentToDestinationLocked's read decision --
// "would this destination be treated as already sent, via its own primary or
// a shared legacy alias it still owns" -- without that function's read-time
// migration side effect (it writes an adopted copy of the alias into the
// destination's own primary key). Tests that need to probe sent-state
// repeatedly without perturbing which composite keys exist use this instead
// of calling Tracker.IsAPIItemSentToDestination directly.
func apiSentTo(tracker *Tracker, itemKey, destinationID, sharedURL string) bool {
	if _, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, destinationID)]; exists {
		return true
	}
	aliasInfo, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		return false
	}
	return legacySentMarkerAppliesTo(aliasInfo.DestinationID, destinationID)
}

// rssSentTo is the RSS twin of apiSentTo: the same non-mutating read decision
// over t.rssStatus.SentItems, for tests that need to probe RSS sent-state
// repeatedly without IsRSSItemSentToDestination's read-time legacy-adoption
// migration perturbing which composite keys exist between probes.
func rssSentTo(tracker *Tracker, itemKey, destinationID, sharedURL string) bool {
	if _, exists := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, destinationID)]; exists {
		return true
	}
	aliasInfo, exists := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		return false
	}
	return legacySentMarkerAppliesTo(aliasInfo.DestinationID, destinationID)
}

// TestAPIAliasOwnerHandoffSurvivesSupersededOwnerPruning pins the API-side
// defect end to end. The sleeps
// are load-bearing, not cosmetic -- without them all four SentAt values tie
// and eviction order becomes a coin flip. Sequence: destA delivers (primary +
// alias), then destB delivers and supersedes the shared alias's ownership.
// Under a retention cap of 2, this leaves 3 composite keys, forcing one
// eviction.
//
// Fails today (verified RED, unmodified tree):
// IsAPIItemSentToDestination(itemKey, destA, sharedURL) returns false --
// destA's own primary marker was pruned as the oldest entry while the
// rewritten alias claims destB as owner, so destA's own dedup check falls
// through and it would receive this item a second time even though it
// already delivered it.
func TestAPIAliasOwnerHandoffSurvivesSupersededOwnerPruning(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)

	tracker.CleanupOldEntries()

	if got := len(tracker.apiStatus.SentItems); got != 2 {
		t.Fatalf("len(apiStatus.SentItems) after CleanupOldEntries() = %d, want 2 (retention cap)", got)
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, destA, sharedURL) {
		t.Fatalf("IsAPIItemSentToDestination(%s) = false after destB superseded the shared alias and "+
			"destA's own primary marker was pruned by retention; destA would receive this item a second "+
			"time even though it already delivered it", destA)
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, destB, sharedURL) {
		t.Fatalf("IsAPIItemSentToDestination(%s) = false", destB)
	}
}

// TestRSSAliasOwnerHandoffSurvivesSupersededOwnerPruning is the RSS twin of
// the test above (plan sec2.2), pinning markRSSItemSentUnderKeyLocked. No
// ParsedItems are seeded, so retention's age branch never engages and nothing
// is "protected" -- matching the plan's reproduction exactly.
//
// Fails today (verified RED, unmodified tree), same reason as the API twin.
func TestRSSAliasOwnerHandoffSurvivesSupersededOwnerPruning(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSSentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)

	tracker.CleanupOldEntries()

	if got := len(tracker.rssStatus.SentItems); got != 2 {
		t.Fatalf("len(rssStatus.SentItems) after CleanupOldEntries() = %d, want 2 (retention cap)", got)
	}
	if !tracker.IsRSSItemSentToDestination(itemKey, destA, sharedURL) {
		t.Fatalf("IsRSSItemSentToDestination(%s) = false after destB superseded the shared alias and "+
			"destA's own primary marker was pruned by retention; destA would receive this item a second "+
			"time even though it already delivered it", destA)
	}
	if !tracker.IsRSSItemSentToDestination(itemKey, destB, sharedURL) {
		t.Fatalf("IsRSSItemSentToDestination(%s) = false", destB)
	}
}

// TestMarkAPIItemSentUnderKeyRefreshesSupersededOwnerPrimarySentAt is a
// narrower, write-path-only pin: no retention involved, direct proof the
// refresh fires when a second, distinct destination supersedes the shared
// alias's ownership. The whole safety argument
// (refreshAPISentItemSentAtLocked, tracker.go) rests on the refresh touching
// ONLY SentAt, so this also pins DestinationID byte-identical before and
// after -- a mutant that blanks it on refresh survives the full suite
// otherwise.
func TestMarkAPIItemSentUnderKeyRefreshesSupersededOwnerPrimarySentAt(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)

	before, ok := tracker.APISentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing before handoff")
	}

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)

	after, ok := tracker.APISentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after handoff")
	}
	if !after.SentAt.After(before.SentAt) {
		t.Fatalf("primary(A) SentAt after handoff = %v, want strictly after %v (refresh did not fire)",
			after.SentAt, before.SentAt)
	}
	if after.DestinationID != before.DestinationID {
		t.Fatalf("primary(A) DestinationID changed from %q to %q; the refresh must touch only SentAt",
			before.DestinationID, after.DestinationID)
	}
}

// TestMarkRSSItemSentUnderKeyLockedRefreshesSupersededOwnerPrimarySentAt is
// the RSS twin. refreshRSSSentItemSentAtLocked
// (tracker.go) must touch ONLY SentAt -- the whole safety argument for the
// alias-ownership fix rests on that property, and RSS carries the row with
// teeth: a mutant that clears Skipped would let a filter-mismatch skip
// marker be expanded into content-signature markers at
// backfillContentSignatureAliasesForSentItem (tracker.go:3241), which is
// exactly the missed-delivery outcome this fix exists to prevent, and that
// mutant was previously caught only incidentally by an unrelated scheduler
// test. Assert DestinationID, Skipped, Derived, ItemKey, Title and FeedTitle
// are byte-identical before and after the refresh so every field-mutating
// regression fails here, in the package that owns the property.
func TestMarkRSSItemSentUnderKeyLockedRefreshesSupersededOwnerPrimarySentAt(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)

	before, ok := tracker.RSSSentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing before handoff")
	}

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)

	after, ok := tracker.RSSSentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after handoff")
	}
	if !after.SentAt.After(before.SentAt) {
		t.Fatalf("primary(A) SentAt after handoff = %v, want strictly after %v (refresh did not fire)",
			after.SentAt, before.SentAt)
	}
	if after.DestinationID != before.DestinationID {
		t.Fatalf("primary(A) DestinationID changed from %q to %q; the refresh must touch only SentAt",
			before.DestinationID, after.DestinationID)
	}
	if after.Skipped != before.Skipped {
		t.Fatalf("primary(A) Skipped changed from %v to %v; the refresh must touch only SentAt "+
			"(clearing Skipped would let this row be expanded into content-signature markers at "+
			"backfillContentSignatureAliasesForSentItem, tracker.go:3241 -- a missed delivery)",
			before.Skipped, after.Skipped)
	}
	if after.Derived != before.Derived {
		t.Fatalf("primary(A) Derived changed from %v to %v; the refresh must touch only SentAt",
			before.Derived, after.Derived)
	}
	if after.ItemKey != before.ItemKey {
		t.Fatalf("primary(A) ItemKey changed from %q to %q; the refresh must touch only SentAt",
			before.ItemKey, after.ItemKey)
	}
	if after.Title != before.Title {
		t.Fatalf("primary(A) Title changed from %q to %q; the refresh must touch only SentAt",
			before.Title, after.Title)
	}
	if after.FeedTitle != before.FeedTitle {
		t.Fatalf("primary(A) FeedTitle changed from %q to %q; the refresh must touch only SentAt",
			before.FeedTitle, after.FeedTitle)
	}
}

// TestMarkAPIItemSentUnderKeyDoesNotRefreshOnOwnerlessLegacyMigration guards
// the one-time migration-adoption path (plan sec4.1): a legacy alias written
// before destination IDs existed (or via MarkAPIItemSentToWebhook, whose
// owner is always blanked) carries no recorded owner. Adopting it must not
// touch any other composite key.
func TestMarkAPIItemSentUnderKeyDoesNotRefreshOnOwnerlessLegacyMigration(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"

	// MarkAPIItemSentToWebhook passes the webhook URL as both marker key and
	// owner; deliveryDestinationIDForPersistence blanks any http(s) owner, so
	// this writes an owner-less alias -- the pre-destination-ID legacy shape.
	tracker.MarkAPIItemSentToWebhook(itemKey, "Title", sharedURL)

	if got := len(tracker.apiStatus.SentItems); got != 1 {
		t.Fatalf("setup: len(SentItems) = %d, want 1", got)
	}

	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)

	if got := len(tracker.apiStatus.SentItems); got != 1 {
		t.Fatalf("len(SentItems) after owner-less adoption = %d, want 1 (no other row created by a refresh)", got)
	}
	info, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		t.Fatal("alias row missing after adoption")
	}
	if info.DestinationID != destA {
		t.Fatalf("alias DestinationID = %q, want %q", info.DestinationID, destA)
	}
}

// TestMarkRSSItemSentUnderKeyLockedDoesNotRefreshOnOwnerlessLegacyMigration
// is the RSS twin.
func TestMarkRSSItemSentUnderKeyLockedDoesNotRefreshOnOwnerlessLegacyMigration(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"

	tracker.MarkRSSItemSentToWebhook(itemKey, "Title", "Feed", sharedURL)

	if got := len(tracker.rssStatus.SentItems); got != 1 {
		t.Fatalf("setup: len(SentItems) = %d, want 1", got)
	}

	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)

	if got := len(tracker.rssStatus.SentItems); got != 1 {
		t.Fatalf("len(SentItems) after owner-less adoption = %d, want 1 (no other row created by a refresh)", got)
	}
	info, exists := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		t.Fatal("alias row missing after adoption")
	}
	if info.DestinationID != destA {
		t.Fatalf("alias DestinationID = %q, want %q", info.DestinationID, destA)
	}
}

// TestMarkAPIItemSentUnderKeyRefreshIsNoOpWhenSupersededPrimaryAlreadyPruned
// simulates destA's own primary marker having already been pruned by an
// earlier retention pass before destB supersedes the shared alias. The
// refresh helper must find nothing at destA's key and do nothing (no panic,
// no fabricated row).
func TestMarkAPIItemSentUnderKeyRefreshIsNoOpWhenSupersededPrimaryAlreadyPruned(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)

	delete(tracker.apiStatus.SentItems, makeCompositeKey(itemKey, destA))

	if got := len(tracker.apiStatus.SentItems); got != 1 {
		t.Fatalf("setup: len(SentItems) = %d, want 1 (alias only, primary already pruned)", got)
	}

	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)

	if got := len(tracker.apiStatus.SentItems); got != 1 {
		t.Fatalf("len(SentItems) after handoff onto an already-pruned primary = %d, want 1 "+
			"(alias overwrite only, no fabricated row)", got)
	}
	if _, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, destA)]; exists {
		t.Fatal("refresh fabricated a row at the already-pruned primary key")
	}
}

// TestMarkRSSItemSentUnderKeyLockedRefreshIsNoOpWhenSupersededPrimaryAlreadyPruned
// is the RSS twin.
func TestMarkRSSItemSentUnderKeyLockedRefreshIsNoOpWhenSupersededPrimaryAlreadyPruned(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)

	delete(tracker.rssStatus.SentItems, makeCompositeKey(itemKey, destA))

	if got := len(tracker.rssStatus.SentItems); got != 1 {
		t.Fatalf("setup: len(SentItems) = %d, want 1 (alias only, primary already pruned)", got)
	}

	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)

	if got := len(tracker.rssStatus.SentItems); got != 1 {
		t.Fatalf("len(SentItems) after handoff onto an already-pruned primary = %d, want 1 "+
			"(alias overwrite only, no fabricated row)", got)
	}
	if _, exists := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, destA)]; exists {
		t.Fatal("refresh fabricated a row at the already-pruned primary key")
	}
}

// TestMarkAPIItemSentUnderKeyDoesNotRefreshOnSameOwnerRewrite guards the
// no-handoff case: a destination re-marking its OWN primary
// (markerDestination == ownerDestinationID == destA in both writes, so
// oldOwner == newOwner) is a direct overwrite of itself, not a supersession,
// and must not touch any OTHER composite key. This
// pins both len(SentItems) unchanged and an unrelated marker's SentAt staying
// byte-identical, not just "no side effect" in the abstract.
func TestMarkAPIItemSentUnderKeyDoesNotRefreshOnSameOwnerRewrite(t *testing.T) {
	tracker := NewMemoryTracker()

	unrelatedKey := "other-item"
	unrelatedDest := "discord.other"
	itemKey := "item-1"
	destA := "discord.ransomware"

	tracker.MarkAPIItemSentToDestination(unrelatedKey, "Title", unrelatedDest)
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)

	unrelatedBefore, ok := tracker.APISentItemSnapshot(unrelatedKey, unrelatedDest)
	if !ok {
		t.Fatal("unrelated marker missing before rewrite")
	}
	lenBefore := len(tracker.apiStatus.SentItems)

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)

	if got := len(tracker.apiStatus.SentItems); got != lenBefore {
		t.Fatalf("len(SentItems) after same-owner rewrite = %d, want %d (no key fabricated)", got, lenBefore)
	}
	unrelatedAfter, ok := tracker.APISentItemSnapshot(unrelatedKey, unrelatedDest)
	if !ok {
		t.Fatal("unrelated marker missing after rewrite")
	}
	if !unrelatedAfter.SentAt.Equal(unrelatedBefore.SentAt) {
		t.Fatalf("unrelated marker SentAt changed from %v to %v; a same-owner rewrite of a different "+
			"item must not touch it", unrelatedBefore.SentAt, unrelatedAfter.SentAt)
	}
}

// TestMarkRSSItemSentUnderKeyLockedDoesNotRefreshOnSameOwnerRewrite is the
// RSS twin, using MarkRSSItemDedupedForDestination after
// MarkRSSItemSentToDestination for the same destination (a derived-flag
// overwrite of one's own primary) to prove the guard holds for the richer RSS
// call surface too.
func TestMarkRSSItemSentUnderKeyLockedDoesNotRefreshOnSameOwnerRewrite(t *testing.T) {
	tracker := NewMemoryTracker()

	unrelatedKey := "other-item"
	unrelatedDest := "discord.other"
	itemKey := "item-1"
	destA := "discord.rss.general"

	tracker.MarkRSSItemSentToDestination(unrelatedKey, "Title", "Feed", unrelatedDest)
	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)

	unrelatedBefore, ok := tracker.RSSSentItemSnapshot(unrelatedKey, unrelatedDest)
	if !ok {
		t.Fatal("unrelated marker missing before rewrite")
	}
	lenBefore := len(tracker.rssStatus.SentItems)

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemDedupedForDestination(itemKey, "Title", "Feed", destA)

	if got := len(tracker.rssStatus.SentItems); got != lenBefore {
		t.Fatalf("len(SentItems) after same-owner rewrite = %d, want %d (no key fabricated)", got, lenBefore)
	}
	unrelatedAfter, ok := tracker.RSSSentItemSnapshot(unrelatedKey, unrelatedDest)
	if !ok {
		t.Fatal("unrelated marker missing after rewrite")
	}
	if !unrelatedAfter.SentAt.Equal(unrelatedBefore.SentAt) {
		t.Fatalf("unrelated marker SentAt changed from %v to %v; a same-owner rewrite of a different "+
			"item must not touch it", unrelatedBefore.SentAt, unrelatedAfter.SentAt)
	}
}

// TestMarkAPIItemSentUnderKeyRefreshesOnBlankNewOwner covers a mirror
// branch that was previously missing: a write that changes the alias's
// owner FROM a real destination TO blank (MarkAPIItemSentToWebhook /
// MarkRSSItemSentToWebhook, whose owner is always blanked by
// deliveryDestinationIDForPersistence) is still a genuine owner change --
// oldOwner != "" and oldOwner != newOwner ("") -- and must still refresh the
// superseded owner's own primary.
func TestMarkAPIItemSentUnderKeyRefreshesOnBlankNewOwner(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)

	before, ok := tracker.APISentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing before blank-owner write")
	}
	lenBefore := len(tracker.apiStatus.SentItems)

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToWebhook(itemKey, "Title", sharedURL)

	if got := len(tracker.apiStatus.SentItems); got != lenBefore {
		t.Fatalf("len(SentItems) after blank-owner write = %d, want %d (no row created)", got, lenBefore)
	}
	after, ok := tracker.APISentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after blank-owner write")
	}
	if !after.SentAt.After(before.SentAt) {
		t.Fatalf("primary(A) SentAt after blank-owner write = %v, want strictly after %v "+
			"(refresh did not fire for the blank-new-owner branch)", after.SentAt, before.SentAt)
	}
	aliasInfo, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		t.Fatal("alias row missing after blank-owner write")
	}
	if aliasInfo.DestinationID != "" {
		t.Fatalf("alias DestinationID = %q after blank-owner write, want empty "+
			"(unchanged owner-blanking behaviour)", aliasInfo.DestinationID)
	}
}

// TestMarkRSSItemSentUnderKeyLockedRefreshesOnBlankNewOwner is the RSS twin.
func TestMarkRSSItemSentUnderKeyLockedRefreshesOnBlankNewOwner(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)

	before, ok := tracker.RSSSentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing before blank-owner write")
	}
	lenBefore := len(tracker.rssStatus.SentItems)

	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToWebhook(itemKey, "Title", "Feed", sharedURL)

	if got := len(tracker.rssStatus.SentItems); got != lenBefore {
		t.Fatalf("len(SentItems) after blank-owner write = %d, want %d (no row created)", got, lenBefore)
	}
	after, ok := tracker.RSSSentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after blank-owner write")
	}
	if !after.SentAt.After(before.SentAt) {
		t.Fatalf("primary(A) SentAt after blank-owner write = %v, want strictly after %v "+
			"(refresh did not fire for the blank-new-owner branch)", after.SentAt, before.SentAt)
	}
	aliasInfo, exists := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, sharedURL)]
	if !exists {
		t.Fatal("alias row missing after blank-owner write")
	}
	if aliasInfo.DestinationID != "" {
		t.Fatalf("alias DestinationID = %q after blank-owner write, want empty "+
			"(unchanged owner-blanking behaviour)", aliasInfo.DestinationID)
	}
}

// TestAPIAliasOwnerHandoffPingPongResolvesBothDestinationsAfterFix pins the
// fact that the defect is symmetric and self-restarting, not capped at
// one duplicate. Once destA's primary is pruned and destA "re-delivers" (a
// fresh primary + alias write, exactly what a real redelivery produces), that
// write retakes the alias and strands destB's primary for the next pressure
// cycle, and so on. apiSentTo (not IsAPIItemSentToDestination) is used to
// probe sent-state so the read-time legacy-adoption migration -- a real but
// unrelated Tracker behaviour -- never perturbs which composite keys exist
// between cycles.
//
// Verified RED (8 duplicates across 8 pressure cycles) against the tree that
// had no superseded-owner refresh at all. It is NOT red against the tree that
// merely had the refresh in the wrong place: apiSentTo skips the read-time
// mint, so this test cannot see the refresh-ordering defect (that is what the
// real-accessor test below is for) -- measured green on Linux both before and
// after the 2026-09-04 reordering fix.
func TestAPIAliasOwnerHandoffPingPongResolvesBothDestinationsAfterFix(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.CleanupOldEntries()

	const pressureCycles = 8
	duplicates := 0
	for cycle := 0; cycle < pressureCycles; cycle++ {
		switch {
		case !apiSentTo(tracker, itemKey, destA, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
		case !apiSentTo(tracker, itemKey, destB, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.CleanupOldEntries()
	}

	if duplicates != 0 {
		t.Fatalf("duplicates across %d pressure cycles = %d, want 0; the alias-owner handoff must not "+
			"strand the other destination's primary marker on each successive supersession "+
			"(the defect is symmetric and self-restarting, not capped at one duplicate)",
			pressureCycles, duplicates)
	}
}

// TestAPIAliasOwnerHandoffPingPongViaRealAccessorZeroDuplicatesAfterFix
// covers a gap the earlier refresh-ordering left open: the test above proves
// the fix's own duplicate bound using apiSentTo, a local read helper that
// deliberately does not carry IsAPIItemSentToDestination's read-time
// legacy-adoption migration (adopting a matching legacy alias mints a copy of
// it under the asking destination's own primary key). That migration is
// real, unrelated Tracker behaviour, and driving this same pressure loop
// through the real accessor used to change the answer, not just its
// stability: on a coarse-clock host (Windows, ~510us statusNow()
// granularity, ties on two back-to-back calls ~100% of the time for this
// exact refresh-then-write gap) it measured 0 duplicates across 8 pressure
// cycles, 10/10 identical runs; on real Linux (no ties, 30ns resolution) the
// identical scenario measured 4/8, deterministically, every run, on both
// golang:1.27-alpine and golang:1.27 -- not "at most 1, self-correcting" but
// a sustained ~50% oscillation, because refreshAPISentItemSentAtLocked used
// to run BEFORE the marker write it was protecting against, leaving the
// refreshed superseded-owner primary one clock tick older than the alias
// write a few lines later in the same function; the next read-time mint then
// inherited that alias's newer SentAt unchanged, making the once-refreshed
// primary the unique oldest of the three surviving keys and evicting it.
// Moving the refresh to run AFTER the marker write closes that gap: the
// refreshed SentAt is now always >= the write it follows, so it is never the
// unique oldest.
//
// This test is meaningless run only on Windows: it reads 0/8 there both
// before and after the fix, by accident of clock granularity, not because of
// anything the fix does. It MUST be run inside a real Linux container (see
// TESTING.md / the plan's reproduction section) for its result to mean
// anything about the actual defect.
//
// Renamed 2026-09-04 (implementation review) from
// ...ViaRealAccessorAtMostOneDuplicateAfterFix, whose name still claimed the
// disproved "at most 1" bound this test no longer asserts. The plan
// (.claude/working/plans/2026-09-04-alias-duplicate-bound-linux.md) and
// TESTING.md's platform-findings history record the old name.
//
// Before the ordering fix, real Linux measured 4/8, deterministically, every
// run, both distros. After the fix: 0/8 (and 0/2000 over a longer run).
func TestAPIAliasOwnerHandoffPingPongViaRealAccessorZeroDuplicatesAfterFix(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.CleanupOldEntries()

	const pressureCycles = 8
	duplicates := 0
	for cycle := 0; cycle < pressureCycles; cycle++ {
		switch {
		case !tracker.IsAPIItemSentToDestination(itemKey, destA, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
		case !tracker.IsAPIItemSentToDestination(itemKey, destB, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.CleanupOldEntries()
	}

	if duplicates != 0 {
		t.Fatalf("duplicates via the real IsAPIItemSentToDestination accessor across %d pressure "+
			"cycles = %d, want 0 (pre-fix on real Linux, the identical scenario produces 4/%d, "+
			"deterministically, every run -- see the doc comment)",
			pressureCycles, duplicates, pressureCycles)
	}
}

// TestRSSAliasOwnerHandoffPingPongResolvesBothDestinationsAfterFix is the RSS
// twin of TestAPIAliasOwnerHandoffPingPongResolvesBothDestinationsAfterFix:
// the RSS side is exactly as constructible and exactly as
// reachable at tracker level as the API side, so it gets the same recurrence
// pin using rssSentTo (the RSS non-mutating read helper) rather than
// IsRSSItemSentToDestination, for the same reason apiSentTo is used above.
//
// Verified RED (8 duplicates across 8 pressure cycles) against the tree that
// had no superseded-owner refresh at all, the RSS mirror of the API probe
// above. Like that probe it is blind to the refresh-ORDERING defect, because
// rssSentTo skips the read-time mint; measured green on Linux both before and
// after the 2026-09-04 reordering fix.
func TestRSSAliasOwnerHandoffPingPongResolvesBothDestinationsAfterFix(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSSentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.CleanupOldEntries()

	const pressureCycles = 8
	duplicates := 0
	for cycle := 0; cycle < pressureCycles; cycle++ {
		switch {
		case !rssSentTo(tracker, itemKey, destA, sharedURL):
			duplicates++
			tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)
		case !rssSentTo(tracker, itemKey, destB, sharedURL):
			duplicates++
			tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destB)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.CleanupOldEntries()
	}

	if duplicates != 0 {
		t.Fatalf("duplicates across %d pressure cycles = %d, want 0; the alias-owner handoff must not "+
			"strand the other destination's primary marker on each successive supersession "+
			"(RSS twin of the API case: the defect is symmetric and self-restarting, "+
			"not capped at one duplicate)",
			pressureCycles, duplicates)
	}
}

// TestRSSAliasOwnerHandoffPingPongViaRealAccessorZeroDuplicatesAfterFix is the
// RSS twin of TestAPIAliasOwnerHandoffPingPongViaRealAccessorZeroDuplicatesAfterFix
// -- through the PRODUCTION IsRSSItemSentToDestination accessor, not the
// non-mutating rssSentTo helper used by the ping-pong test above. This
// distinction is load-bearing, not a symmetry nicety: rssSentTo was written
// specifically to avoid rssItemSentToDestinationLocked's own read-time
// legacy-adoption mint, so it cannot see the defect this test exists to
// catch. Before the refresh-ordering fix, driving this same pressure loop
// through IsRSSItemSentToDestination measured 300/600 on real Linux (both
// golang:1.27-alpine and golang:1.27) -- the RSS marker path oscillates at
// the same ~50% rate as the API side, for the exact same reason (§1c/§3d of
// .claude/working/plans/2026-09-04-alias-duplicate-bound-linux.md): the
// superseded owner's primary refresh used to run before the marker write
// that competes with it for retention's eviction slot.
//
// This test is meaningless run only on Windows for the same clock-resolution
// reason as its API twin -- it MUST be run inside a real Linux container.
// Unlike its API twin (whose count-bounded heap breaks ties deterministically
// by composite-key string), RSS's retention sorts with sort.Slice, which is
// NOT stable, over a slice built by ranging a Go map, whose iteration order
// is runtime-randomized -- so on a host where statusNow() ties (Windows), the
// outcome of this pressure loop is not just "meaningless", it is genuinely
// non-deterministic run to run. Skip explicitly rather than report a flaky
// pass or fail.
func TestRSSAliasOwnerHandoffPingPongViaRealAccessorZeroDuplicatesAfterFix(t *testing.T) {
	if canaryA, canaryB := statusNow(), statusNow(); canaryA.Equal(canaryB) {
		t.Skip("statusNow() ties on two back-to-back calls on this host (coarse clock granularity, " +
			"e.g. Windows) -- RSS retention's sort.Slice is not stable over ties and this pressure " +
			"loop cannot produce a meaningful, deterministic result here; run it inside a real Linux " +
			"container instead")
	}

	policy := DefaultRetentionPolicy()
	policy.MaxRSSSentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.CleanupOldEntries()

	const pressureCycles = 8
	duplicates := 0
	for cycle := 0; cycle < pressureCycles; cycle++ {
		switch {
		case !tracker.IsRSSItemSentToDestination(itemKey, destA, sharedURL):
			duplicates++
			tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)
		case !tracker.IsRSSItemSentToDestination(itemKey, destB, sharedURL):
			duplicates++
			tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destB)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.CleanupOldEntries()
	}

	if duplicates != 0 {
		t.Fatalf("duplicates via the real IsRSSItemSentToDestination accessor across %d pressure "+
			"cycles = %d, want 0 (pre-fix on real Linux, the identical scenario produces 300/600 "+
			"over a longer run, deterministically -- see the doc comment)",
			pressureCycles, duplicates)
	}
}

// TestAPIAliasOwnerHandoffMintOriginPrimarySurvivesGenuineSupersession is the
// mint-origin-primary regression case: destA's own primary marker is never
// written by an explicit MarkAPIItemSentToDestination /
// MarkAPIItemSentToLegacyDestination call -- it exists only because
// IsAPIItemSentToDestination's read-time legacy-adoption migration
// (apiItemSentToDestinationLocked) minted a copy of the shared alias under
// destA's own key. destA then genuinely becomes the superseded owner in a
// real handoff to a third destination, destB, and retention runs under
// pressure. This is exactly the case the superseded §3c fix direction
// (tagging the mint "synthetic" and evicting synthetic rows first,
// regardless of SentAt) would have made WORSE: a mint-origin primary that
// later becomes a genuinely-needed row via the refresh would have kept a
// stale "synthetic" tag forever and been evicted ahead of a real row,
// producing duplicateForA=true where today's code (and this reorder) produce
// false. The reorder fix carries no such flag and treats a mint-origin
// primary exactly like any other primary once it has been refreshed -- which
// is correct, since after the mint runs once, the row IS a real, load-bearing
// primary marker regardless of how it was created.
//
// Measured on real Linux: before the reorder, driving a pressure loop from
// this starting state through the real accessor produces 300/600 duplicates
// over a longer run; after the reorder, 0/600. This test checks the
// immediate case plus a short pressure loop through the real accessor.
//
// Like both real-accessor ping-pong tests above, this one is meaningless run
// only on Windows: measured 2026-09-04, it passes 20/20 there with the
// refresh ordering reverted as well as with it applied, because statusNow()
// ties on ~99.998% of back-to-back calls on that host. It MUST be run inside
// a real Linux container for its result to mean anything.
func TestAPIAliasOwnerHandoffMintOriginPrimarySurvivesGenuineSupersession(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	// Only the alias is written for destA -- no explicit primary(A) call.
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
	time.Sleep(aliasOwnershipSleepMargin)

	// A dedup CHECK, not a mark, is what produces destA's primary here: the
	// read-time legacy-adoption migration mints a copy of the alias under
	// destA's own key.
	if !tracker.IsAPIItemSentToDestination(itemKey, destA, sharedURL) {
		t.Fatal("setup: destA should already be considered sent via the shared alias")
	}
	if _, exists := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, destA)]; !exists {
		t.Fatal("setup: the dedup check should have minted a primary(A) row from the alias")
	}
	time.Sleep(aliasOwnershipSleepMargin)

	// destB genuinely delivers and supersedes the shared alias's ownership.
	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
	time.Sleep(aliasOwnershipSleepMargin)
	tracker.CleanupOldEntries()

	if !tracker.IsAPIItemSentToDestination(itemKey, destA, sharedURL) {
		t.Fatal("duplicateForA = true immediately after the genuine handoff away from a mint-origin " +
			"primary; want false (this is the case the rejected §3c comparator fix would have made " +
			"worse -- a mint-origin row must not be evicted ahead of a genuinely-needed one)")
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, destB, sharedURL) {
		t.Fatal("destB not considered sent after its own genuine delivery")
	}

	const pressureCycles = 8
	duplicates := 0
	for cycle := 0; cycle < pressureCycles; cycle++ {
		switch {
		case !tracker.IsAPIItemSentToDestination(itemKey, destA, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)
		case !tracker.IsAPIItemSentToDestination(itemKey, destB, sharedURL):
			duplicates++
			tracker.MarkAPIItemSentToDestination(itemKey, "Title", destB)
			time.Sleep(aliasOwnershipSleepMargin)
			tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.CleanupOldEntries()
	}

	if duplicates != 0 {
		t.Fatalf("duplicates across %d pressure cycles starting from a mint-origin primary = %d, "+
			"want 0 (pre-fix on real Linux, the identical scenario produces roughly 50%% of cycles as "+
			"duplicates, e.g. 300/600 over a longer run)", pressureCycles, duplicates)
	}
}

// TestMarkAPIItemSentUnderKeyRefreshedSupersededPrimaryIsNotOlderThanTheWrite
// is a direct unit test on the reordered call site itself, isolating the
// ordering guarantee from the full pressure scenario: given a handoff where
// the superseded owner's primary already exists, its SentAt after the
// handoff must be >= the new alias/primary write's own SentAt, not merely
// .After() some earlier snapshot (TestMarkAPIItemSentUnderKeyRefreshesSupersededOwnerPrimarySentAt
// already pins that weaker property and continues to pass unmodified).
//
// This test is a coin flip on a coarse clock: on Windows the superseded
// primary and the alias write tie at delta=0 both before and after this fix
// (measured), so it self-documents that condition with t.Skip rather than
// reporting a false pass or a false fail. On real Linux it discriminates
// cleanly (measured: delta<0 before the reorder, delta>0 after). Run inside a
// real Linux container for a meaningful result.
func TestMarkAPIItemSentUnderKeyRefreshedSupersededPrimaryIsNotOlderThanTheWrite(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.ransomware"
	destB := "discord.ransomware.2"

	tracker.MarkAPIItemSentToDestination(itemKey, "Title", destA)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destA)

	// The handoff under test: destB supersedes destA's ownership of the
	// shared alias. markAPIItemSentUnderKey writes the alias/primary marker
	// for this call and then refreshes destA's superseded primary.
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destB)

	refreshedPrimary, ok := tracker.APISentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after handoff")
	}
	aliasWrite, ok := tracker.APISentItemSnapshot(itemKey, sharedURL)
	if !ok {
		t.Fatal("alias marker missing after handoff")
	}

	delta := refreshedPrimary.SentAt.Sub(aliasWrite.SentAt)
	if delta == 0 {
		t.Skip("statusNow() ties between the marker write and the refresh on this host " +
			"(coarse clock granularity, e.g. Windows) -- this test cannot discriminate the ordering " +
			"fix here; run it inside a real Linux container instead")
	}
	if delta < 0 {
		t.Fatalf("refreshed primary(A) SentAt is %v BEFORE the alias write that superseded it "+
			"(delta=%v); the refresh must run after the marker write so the superseded owner's "+
			"primary is never the unique oldest of the surviving composite keys",
			refreshedPrimary.SentAt, delta)
	}
}

// TestMarkRSSItemSentUnderKeyLockedRefreshedSupersededPrimaryIsNotOlderThanTheWrite
// is the RSS twin -- required because an API-only reorder (leaving
// markRSSItemSentUnderKeyLocked's refresh running before its write) passes
// the entire existing internal/status suite AND the tightened API test above;
// only an RSS-side assertion like this one (or the RSS real-accessor
// ping-pong test) catches a half-applied fix.
func TestMarkRSSItemSentUnderKeyLockedRefreshedSupersededPrimaryIsNotOlderThanTheWrite(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-test"
	destA := "discord.rss.general"
	destB := "discord.rss.general.2"

	tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destA)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destA)

	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destB)

	refreshedPrimary, ok := tracker.RSSSentItemSnapshot(itemKey, destA)
	if !ok {
		t.Fatal("primary(A) marker missing after handoff")
	}
	aliasWrite, ok := tracker.RSSSentItemSnapshot(itemKey, sharedURL)
	if !ok {
		t.Fatal("alias marker missing after handoff")
	}

	delta := refreshedPrimary.SentAt.Sub(aliasWrite.SentAt)
	if delta == 0 {
		t.Skip("statusNow() ties between the marker write and the refresh on this host " +
			"(coarse clock granularity, e.g. Windows) -- this test cannot discriminate the ordering " +
			"fix here; run it inside a real Linux container instead")
	}
	if delta < 0 {
		t.Fatalf("refreshed primary(A) SentAt is %v BEFORE the alias write that superseded it "+
			"(delta=%v); the refresh must run after the marker write so the superseded owner's "+
			"primary is never the unique oldest of the surviving composite keys",
			refreshedPrimary.SentAt, delta)
	}
}
