package status

import "testing"

// The alias write must precede the previous owner's refresh for retention,
// while LastUpdated must include that final refresh. Repeated owner changes
// cross the host clock's tick even on development machines with coarse time.
func TestAPISharedAliasRefreshKeepsLastUpdatedAtOrAfterSentAt(t *testing.T) {
	tracker := newTestTracker(t)
	const itemKey = "last-updated-api"
	const ownerA = "discord.ransomware"
	const ownerB = "discord.ransomware.2"
	const alias = "https://example.test/shared-api"

	tracker.MarkAPIItemSentToDestination(itemKey, "Item", ownerA)
	tracker.MarkAPIItemSentToDestination(itemKey, "Item", ownerB)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Item", alias, ownerA)

	for i := 0; i < 25000; i++ {
		newOwner, oldOwner := ownerB, ownerA
		if i%2 != 0 {
			newOwner, oldOwner = ownerA, ownerB
		}
		tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Item", alias, newOwner)
		aliasSent := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, alias)].SentAt
		refreshedSent := tracker.apiStatus.SentItems[makeCompositeKey(itemKey, oldOwner)].SentAt
		if refreshedSent.Before(aliasSent) {
			t.Fatalf("iteration %d: refreshed API primary %s is older than alias %s", i, refreshedSent, aliasSent)
		}
		if tracker.apiStatus.LastUpdated.Before(refreshedSent) {
			t.Fatalf("iteration %d: API LastUpdated %s is older than refreshed primary %s", i, tracker.apiStatus.LastUpdated, refreshedSent)
		}
	}
}

func TestRSSSharedAliasRefreshKeepsLastUpdatedAtOrAfterSentAt(t *testing.T) {
	tracker := newTestTracker(t)
	const itemKey = "last-updated-rss"
	const ownerA = "discord.rss.general"
	const ownerB = "discord.rss.general.2"
	const alias = "https://example.test/shared-rss"

	tracker.MarkRSSItemSentToDestination(itemKey, "Item", "Feed", ownerA)
	tracker.MarkRSSItemSentToDestination(itemKey, "Item", "Feed", ownerB)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Item", "Feed", alias, ownerA)

	for i := 0; i < 25000; i++ {
		newOwner, oldOwner := ownerB, ownerA
		if i%2 != 0 {
			newOwner, oldOwner = ownerA, ownerB
		}
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Item", "Feed", alias, newOwner)
		aliasSent := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, alias)].SentAt
		refreshedSent := tracker.rssStatus.SentItems[makeCompositeKey(itemKey, oldOwner)].SentAt
		if refreshedSent.Before(aliasSent) {
			t.Fatalf("iteration %d: refreshed RSS primary %s is older than alias %s", i, refreshedSent, aliasSent)
		}
		if tracker.rssStatus.LastUpdated.Before(refreshedSent) {
			t.Fatalf("iteration %d: RSS LastUpdated %s is older than refreshed primary %s", i, tracker.rssStatus.LastUpdated, refreshedSent)
		}
	}
}
