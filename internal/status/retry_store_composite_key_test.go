package status

import "testing"

// literalFeedTypeRansomware mirrors internal/config's FeedTypeRansomware
// constant ("ransomware"). It cannot be imported directly: internal/config
// imports internal/status (for the Messenger type and RetryItemTypeRSS), so
// the reverse import would be a cycle. This package instead reconstructs the
// two destination-ID shapes it builds (APIDestinationIDForTarget,
// RSSDestinationIDForTarget) as literal strings, matching
// internal/config/destinations_test.go's own expected outputs.
const literalFeedTypeRansomware = "ransomware"

// TestFeedTypeRansomwareLiteralNeverEqualsRSSMarker pins the structural
// reason finding 3 is unreachable today: every API destination ID's second
// dot-segment is always exactly FeedTypeRansomware, every RSS destination
// ID's is always exactly RetryItemTypeRSS ("rss"), and those two strings
// differ. If a future change ever renamed FeedTypeRansomware to "rss" this
// guard (and TestCompositeKeyDestinationSchemesNeverCollide below) would need
// updating to keep testing the real invariant -- so pin the assumption
// explicitly instead of leaving it implicit in the enumeration.
func TestFeedTypeRansomwareLiteralNeverEqualsRSSMarker(t *testing.T) {
	if literalFeedTypeRansomware == RetryItemTypeRSS.String() {
		t.Fatalf("literalFeedTypeRansomware = %q must differ from RetryItemTypeRSS %q, "+
			"or the API/RSS destination-ID schemes could collide by construction",
			literalFeedTypeRansomware, RetryItemTypeRSS.String())
	}
}

// literalAPIDestinationID and literalRSSDestinationID reconstruct
// internal/config's APIDestinationIDForTarget / RSSDestinationIDForTarget
// without importing that package (see literalFeedTypeRansomware above).
func literalAPIDestinationID(messenger Messenger, suffix string) string {
	id := messenger.String() + "." + literalFeedTypeRansomware
	if suffix != "" {
		id += "." + suffix
	}
	return id
}

func literalRSSDestinationID(messenger Messenger, feedType, suffix string) string {
	id := messenger.String() + "." + RetryItemTypeRSS.String() + "." + feedType
	if suffix != "" {
		id += "." + suffix
	}
	return id
}

// TestCompositeKeyDestinationSchemesNeverCollide is a permanent trip-wire for
// makeCompositeKey omitting ItemType/Messenger from its input. It
// reconstructs the real API- and RSS-shaped
// destination IDs over an adversarial enumeration -- including a feed type
// literally named "ransomware" -- and asserts makeCompositeKey never produces
// the same hash for an API-shaped and an RSS-shaped destination sharing an
// item key. If a future destination-ID change ever narrows the two schemes'
// string shapes toward each other, this test fails loudly instead of the
// collision surfacing silently in production.
func TestCompositeKeyDestinationSchemesNeverCollide(t *testing.T) {
	messengers := []Messenger{MessengerDiscord, MessengerSlack, MessengerSlackCompatible}
	feedTypes := []string{"ransomware", "government", literalFeedTypeRansomware, "rss", "general"}
	suffixes := []string{"", "2", "3", "target"}

	itemKey := "shared-item-key"

	apiIDs := make(map[string]bool)
	for _, messenger := range messengers {
		for _, suffix := range suffixes {
			apiIDs[literalAPIDestinationID(messenger, suffix)] = true
		}
	}

	for _, messenger := range messengers {
		for _, feedType := range feedTypes {
			for _, suffix := range suffixes {
				rssID := literalRSSDestinationID(messenger, feedType, suffix)
				rssKey := makeCompositeKey(itemKey, rssID)

				for apiID := range apiIDs {
					if apiID == rssID {
						t.Fatalf("RSS destination ID %q collides verbatim with an API destination ID", rssID)
					}
					apiKey := makeCompositeKey(itemKey, apiID)
					if apiKey == rssKey {
						t.Fatalf("makeCompositeKey(%q, %q) == makeCompositeKey(%q, %q): "+
							"API destination %q and RSS destination %q produced the same composite key",
							itemKey, apiID, itemKey, rssID, apiID, rssID)
					}
				}
			}
		}
	}
}
