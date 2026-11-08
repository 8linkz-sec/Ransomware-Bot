package status

import (
	"testing"
	"time"
)

func TestAPIAliasRetentionWithThreeSharedDestinationsKeepsDeliveries(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 3
	tracker := NewMemoryTrackerWithRetention(policy)
	itemKey := "shared-api-item"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-three"
	destinations := []string{"discord.ransomware", "discord.ransomware.2", "discord.ransomware.3"}

	for _, destination := range destinations {
		tracker.MarkAPIItemSentToDestination(itemKey, "Title", destination)
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destination)
		time.Sleep(aliasOwnershipSleepMargin)
	}
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, destinations[2])
	tracker.CleanupOldEntries()

	if got := len(tracker.apiStatus.SentItems); got != policy.MaxAPISentItems {
		t.Fatalf("API sent markers after retention = %d, want %d", got, policy.MaxAPISentItems)
	}
	for _, destination := range destinations {
		if !tracker.IsAPIItemSentToDestination(itemKey, destination, sharedURL) {
			t.Fatalf("destination %q can replay after its shared alias passed through three owners", destination)
		}
	}
	if _, exists := tracker.APISentItemSnapshot(itemKey, sharedURL); exists {
		t.Fatal("legacy API alias survived retention ahead of a genuine primary marker")
	}
}

func TestRSSAliasRetentionWithThreeSharedDestinationsKeepsDeliveries(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSSentItems = 3
	tracker := NewMemoryTrackerWithRetention(policy)
	itemKey := "shared-rss-item"
	sharedURL := "https://discord.com/api/webhooks/shared-alias-three"
	destinations := []string{"discord.rss.general", "discord.rss.general.2", "discord.rss.general.3"}

	for _, destination := range destinations {
		tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destination)
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destination)
		time.Sleep(aliasOwnershipSleepMargin)
	}
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, destinations[2])
	tracker.CleanupOldEntries()

	if got := len(tracker.rssStatus.SentItems); got != policy.MaxRSSSentItems {
		t.Fatalf("RSS sent markers after retention = %d, want %d", got, policy.MaxRSSSentItems)
	}
	for _, destination := range destinations {
		if !tracker.IsRSSItemSentToDestination(itemKey, destination, sharedURL) {
			t.Fatalf("destination %q can replay after its shared alias passed through three owners", destination)
		}
	}
	if _, exists := tracker.RSSSentItemSnapshot(itemKey, sharedURL); exists {
		t.Fatal("legacy RSS alias survived retention ahead of a genuine primary marker")
	}
}

func TestOwnerlessLegacyAliasRewriteUsesFreshTimestamp(t *testing.T) {
	t.Run("API", func(t *testing.T) {
		tracker := NewMemoryTracker()
		itemKey := "ownerless-api-item"
		sharedURL := "https://discord.com/api/webhooks/ownerless-alias"
		tracker.MarkAPIItemSentToWebhook(itemKey, "Title", sharedURL)
		before, ok := tracker.APISentItemSnapshot(itemKey, sharedURL)
		if !ok {
			t.Fatal("ownerless API alias missing")
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Title", sharedURL, "discord.ransomware")
		after, ok := tracker.APISentItemSnapshot(itemKey, sharedURL)
		if !ok || !after.SentAt.After(before.SentAt) {
			t.Fatalf("ownerless API alias rewrite time = %v, want after %v", after.SentAt, before.SentAt)
		}
	})

	t.Run("RSS", func(t *testing.T) {
		tracker := NewMemoryTracker()
		itemKey := "ownerless-rss-item"
		sharedURL := "https://discord.com/api/webhooks/ownerless-alias"
		tracker.MarkRSSItemSentToWebhook(itemKey, "Title", "Feed", sharedURL)
		before, ok := tracker.RSSSentItemSnapshot(itemKey, sharedURL)
		if !ok {
			t.Fatal("ownerless RSS alias missing")
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", sharedURL, "discord.rss.general")
		after, ok := tracker.RSSSentItemSnapshot(itemKey, sharedURL)
		if !ok || !after.SentAt.After(before.SentAt) {
			t.Fatalf("ownerless RSS alias rewrite time = %v, want after %v", after.SentAt, before.SentAt)
		}
	})
}

func TestPrimaryMarkerRewriteUsesFreshTimestamp(t *testing.T) {
	t.Run("API", func(t *testing.T) {
		tracker := NewMemoryTracker()
		itemKey := "primary-api-item"
		destination := "discord.ransomware"
		tracker.MarkAPIItemSentToDestination(itemKey, "Title", destination)
		before, ok := tracker.APISentItemSnapshot(itemKey, destination)
		if !ok {
			t.Fatal("API primary marker missing")
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkAPIItemSentToDestination(itemKey, "Title", destination)
		after, ok := tracker.APISentItemSnapshot(itemKey, destination)
		if !ok || !after.SentAt.After(before.SentAt) {
			t.Fatalf("API primary rewrite time = %v, want after %v", after.SentAt, before.SentAt)
		}
	})

	t.Run("RSS", func(t *testing.T) {
		tracker := NewMemoryTracker()
		itemKey := "primary-rss-item"
		destination := "discord.rss.general"
		tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destination)
		before, ok := tracker.RSSSentItemSnapshot(itemKey, destination)
		if !ok {
			t.Fatal("RSS primary marker missing")
		}
		time.Sleep(aliasOwnershipSleepMargin)
		tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", destination)
		after, ok := tracker.RSSSentItemSnapshot(itemKey, destination)
		if !ok || !after.SentAt.After(before.SentAt) {
			t.Fatalf("RSS primary rewrite time = %v, want after %v", after.SentAt, before.SentAt)
		}
	})
}
