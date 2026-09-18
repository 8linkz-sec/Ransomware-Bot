package model

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRSSEntryKeyUsesStableDomainModel(t *testing.T) {
	key := GenerateRSSEntryKey("https://example.test/feed.xml", "guid-1", "https://example.test/article", "Article")

	if !strings.HasPrefix(key, "rss:v2:guid:") {
		t.Fatalf("GenerateRSSEntryKey() = %q, want guid prefix", key)
	}
	if strings.Contains(key, "guid-1") || strings.Contains(key, "article") {
		t.Fatalf("GenerateRSSEntryKey() leaked source identity in %q", key)
	}
}

func TestRSSTitleFallbackUsesTimestampWhenAvailable(t *testing.T) {
	first := GenerateRSSEntryKey(
		"https://example.test/feed.xml",
		"",
		"",
		"Daily Briefing",
		time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	)
	second := GenerateRSSEntryKey(
		"https://example.test/feed.xml",
		"",
		"",
		"Daily Briefing",
		time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC),
	)

	if first == "" || second == "" {
		t.Fatalf("title-date keys must not be empty: first=%q second=%q", first, second)
	}
	if first == second {
		t.Fatalf("title-date keys should differ for different timestamps: %q", first)
	}
	if !strings.HasPrefix(first, "rss:v2:title-date:") {
		t.Fatalf("title-date key = %q, want rss:v2:title-date prefix", first)
	}
}

func TestRSSEntryKeyLinkFallbackNormalizesTrailingSlash(t *testing.T) {
	feedURL := "https://example.test/feed.xml"

	withSlash := GenerateRSSEntryKey(feedURL, "", "https://example.test/article/", "Title A")
	withoutSlash := GenerateRSSEntryKey(feedURL, "", "https://example.test/article", "Title B")

	if !strings.HasPrefix(withSlash, "rss:v2:link:") {
		t.Fatalf("link key = %q, want rss:v2:link prefix", withSlash)
	}
	if withSlash != withoutSlash {
		t.Fatalf("link keys should ignore trailing slash: %q vs %q", withSlash, withoutSlash)
	}
}

func TestRSSEntryKeyTitleFallbackWithoutTimestamp(t *testing.T) {
	feedURL := "https://example.test/feed.xml"

	key := GenerateRSSEntryKey(feedURL, "", "ftp://example.test/item", "  Daily Briefing  ")
	if !strings.HasPrefix(key, "rss:v2:title:") {
		t.Fatalf("title key = %q, want rss:v2:title prefix", key)
	}

	zeroTimestamp := GenerateRSSEntryKey(feedURL, "", "", "daily briefing", time.Time{})
	if key != zeroTimestamp {
		t.Fatalf("title keys should normalize case and ignore zero timestamps: %q vs %q", key, zeroTimestamp)
	}
}

func TestRSSEntryKeyWithoutIdentityIsEmpty(t *testing.T) {
	if key := GenerateRSSEntryKey("https://example.test/feed.xml", " ", "/relative/only", "   "); key != "" {
		t.Fatalf("GenerateRSSEntryKey() = %q, want empty key without usable identity", key)
	}
}

func TestGenerateRSSEntryKeyForEntryMatchesFieldVariant(t *testing.T) {
	entry := RSSEntry{
		FeedURL:   "https://example.test/feed.xml",
		GUID:      "guid-1",
		Link:      "https://example.test/article",
		Title:     "Article",
		Published: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	}

	got := GenerateRSSEntryKeyForEntry(entry)
	want := GenerateRSSEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title, entry.Published)
	if got == "" || got != want {
		t.Fatalf("GenerateRSSEntryKeyForEntry() = %q, want %q", got, want)
	}
}

func TestRSSEntryLookupKeysWithGUIDIncludeLegacyNaturalKey(t *testing.T) {
	feedURL := "https://example.test/feed.xml"

	keys := GenerateRSSEntryLookupKeys(
		feedURL,
		" guid-1 ",
		"https://example.test/article",
		"Article",
		time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	)

	if len(keys) != 2 {
		t.Fatalf("GenerateRSSEntryLookupKeys() returned %d keys, want current and legacy", len(keys))
	}
	if !strings.HasPrefix(keys[0], "rss:v2:guid:") {
		t.Fatalf("primary key = %q, want rss:v2:guid prefix", keys[0])
	}
	if keys[1] != feedURL+":guid-1" {
		t.Fatalf("legacy key = %q, want natural feed:guid key", keys[1])
	}
}

func TestRSSEntryLookupKeysDatedTitleFallbackOmitLegacyKeys(t *testing.T) {
	keys := GenerateRSSEntryLookupKeys(
		"https://example.test/feed.xml",
		"",
		"",
		"Daily Briefing",
		time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	)

	if len(keys) != 1 {
		t.Fatalf("dated title fallback returned %d keys, want only the title-date key: %#v", len(keys), keys)
	}
	if !strings.HasPrefix(keys[0], "rss:v2:title-date:") {
		t.Fatalf("primary key = %q, want rss:v2:title-date prefix", keys[0])
	}
}

func TestRSSEntryLookupKeysTitleWithoutDateIncludeLegacyKey(t *testing.T) {
	feedURL := "https://example.test/feed.xml"

	keys := GenerateRSSEntryLookupKeys(feedURL, "", "", "Daily Briefing")

	if len(keys) != 2 {
		t.Fatalf("GenerateRSSEntryLookupKeys() returned %d keys, want current and legacy: %#v", len(keys), keys)
	}
	if !strings.HasPrefix(keys[0], "rss:v2:title:") {
		t.Fatalf("primary key = %q, want rss:v2:title prefix", keys[0])
	}
	if keys[1] != feedURL+":daily briefing" {
		t.Fatalf("legacy key = %q, want natural feed:title key", keys[1])
	}
}

func TestRSSEntryLookupKeysWithoutIdentityAreEmpty(t *testing.T) {
	keys := GenerateRSSEntryLookupKeys("https://example.test/feed.xml", "", "", "")
	if len(keys) != 0 {
		t.Fatalf("GenerateRSSEntryLookupKeys() = %#v, want no keys without usable identity", keys)
	}
}

func TestGenerateRSSEntryLookupKeysForEntryMatchesFieldVariant(t *testing.T) {
	entry := RSSEntry{
		FeedURL:   "https://example.test/feed.xml",
		Link:      "https://example.test/article",
		Title:     "Article",
		Published: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	}

	got := GenerateRSSEntryLookupKeysForEntry(entry)
	want := GenerateRSSEntryLookupKeys(entry.FeedURL, entry.GUID, entry.Link, entry.Title, entry.Published)
	if len(got) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("GenerateRSSEntryLookupKeysForEntry() = %#v, want %#v", got, want)
	}
}

func TestGenerateRSSContentSignatureIgnoresFeedURL(t *testing.T) {
	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	first := GenerateRSSContentSignature("https://a.test/feed.xml", "Alert Title", published)
	second := GenerateRSSContentSignature("https://b.test/feed.xml", "Alert Title", published)
	other := GenerateRSSContentSignature("https://a.test/feed.xml", "Alert Title", published.Add(time.Second))

	if !strings.HasPrefix(first, "content-sig:v3:") {
		t.Fatalf("signature = %q, want content-sig:v3 prefix", first)
	}
	if first != second {
		t.Fatalf("v3 signature should not depend on feed URL: %q vs %q", first, second)
	}
	if first == other {
		t.Fatalf("v3 signature should depend on the published timestamp: %q", first)
	}
}

func TestRSSEntryContentSignatureUndatedUsesEntryKeyDiscriminator(t *testing.T) {
	base := RSSEntry{
		FeedURL:     "https://example.test/feed.xml",
		GUID:        "guid-1",
		Title:       "Report",
		Description: "Body   text\nhere",
	}
	otherGUID := base
	otherGUID.GUID = "guid-2"
	normalizedDescription := base
	normalizedDescription.Description = "Body text here"

	first := GenerateRSSEntryContentSignature(base)
	if !strings.HasPrefix(first, "content-sig:v4:") {
		t.Fatalf("undated signature = %q, want content-sig:v4 prefix", first)
	}
	if first == GenerateRSSEntryContentSignature(otherGUID) {
		t.Fatalf("undated signatures should differ per entry key: %q", first)
	}
	if first != GenerateRSSEntryContentSignature(normalizedDescription) {
		t.Fatal("undated signatures should normalize description whitespace")
	}
}

func TestRSSEntryContentSignatureUndatedWithoutIdentityFallsBackToV3(t *testing.T) {
	first := GenerateRSSEntryContentSignature(RSSEntry{Description: "  spaced   out  "})
	second := GenerateRSSEntryContentSignature(RSSEntry{Description: "spaced out"})

	if !strings.HasPrefix(first, "content-sig:v3:") {
		t.Fatalf("signature = %q, want content-sig:v3 fallback prefix", first)
	}
	if first != second {
		t.Fatalf("fallback signatures should normalize description whitespace: %q vs %q", first, second)
	}
}

func TestRSSEntryContentSignatureDatedMatchesFeedLevelSignature(t *testing.T) {
	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	entry := RSSEntry{
		FeedURL:   "https://example.test/feed.xml",
		Title:     "Report",
		Published: published,
	}

	got := GenerateRSSEntryContentSignature(entry)
	want := GenerateRSSContentSignature(entry.FeedURL, entry.Title, published)
	if got != want {
		t.Fatalf("dated entry signature = %q, want feed-level signature %q", got, want)
	}
}

func TestGenerateRSSContentSignatureLookupKeys(t *testing.T) {
	feedURL := "https://example.test/feed.xml"
	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	keys := GenerateRSSContentSignatureLookupKeys(feedURL, "Alert Title", published)

	if len(keys) != 4 {
		t.Fatalf("GenerateRSSContentSignatureLookupKeys() returned %d keys, want 4: %#v", len(keys), keys)
	}
	if !strings.HasPrefix(keys[0], "content-sig:v3:") || !strings.HasPrefix(keys[1], "content-sig:v3:") {
		t.Fatalf("keys[0..1] = %q, %q, want content-sig:v3 prefixes", keys[0], keys[1])
	}
	if keys[0] == keys[1] {
		t.Fatal("current and legacy v3 signatures should differ because the legacy variant hashes the feed URL")
	}
	if !strings.HasPrefix(keys[2], "content-sig:v2:") {
		t.Fatalf("keys[2] = %q, want content-sig:v2 prefix", keys[2])
	}
	if keys[3] != "content-sig:"+feedURL+":alert title:2026-05-06" {
		t.Fatalf("legacy key = %q, want natural content-sig key", keys[3])
	}
}

func TestGenerateRSSContentSignatureLookupKeysWithZeroPublishedUseEmptyDate(t *testing.T) {
	feedURL := "https://example.test/feed.xml"

	keys := GenerateRSSContentSignatureLookupKeys(feedURL, "Alert Title", time.Time{})

	if len(keys) != 4 {
		t.Fatalf("GenerateRSSContentSignatureLookupKeys() returned %d keys, want 4: %#v", len(keys), keys)
	}
	if keys[3] != "content-sig:"+feedURL+":alert title:" {
		t.Fatalf("legacy key = %q, want empty date suffix for zero published time", keys[3])
	}
}

func TestGenerateRSSEntryContentSignatureLookupKeys(t *testing.T) {
	feedURL := "https://example.test/feed.xml"
	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	dated := RSSEntry{
		FeedURL:     feedURL,
		Title:       "Alert Title",
		Description: "Body text",
		Published:   published,
	}
	datedKeys := GenerateRSSEntryContentSignatureLookupKeys(dated)
	if len(datedKeys) != 6 {
		t.Fatalf("dated entry returned %d keys, want 6: %#v", len(datedKeys), datedKeys)
	}
	if datedKeys[0] != GenerateRSSEntryContentSignature(dated) {
		t.Fatalf("keys[0] = %q, want current entry signature", datedKeys[0])
	}
	if datedKeys[5] != "content-sig:"+feedURL+":alert title:2026-05-06" {
		t.Fatalf("legacy key = %q, want natural content-sig key", datedKeys[5])
	}

	undated := RSSEntry{FeedURL: feedURL, GUID: "guid-1", Title: "Alert Title"}
	undatedKeys := GenerateRSSEntryContentSignatureLookupKeys(undated)
	if len(undatedKeys) != 1 {
		t.Fatalf("undated entry returned %d keys, want only the v4 signature: %#v", len(undatedKeys), undatedKeys)
	}
	if !strings.HasPrefix(undatedKeys[0], "content-sig:v4:") {
		t.Fatalf("undated key = %q, want content-sig:v4 prefix", undatedKeys[0])
	}
}

func TestUniqueRSSKeysSkipsEmptyAndDuplicateKeys(t *testing.T) {
	got := uniqueRSSKeys("a", "", "a", "b", "")
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("uniqueRSSKeys() = %#v, want [a b]", got)
	}
}

func TestCanonicalRSSLink(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{name: "empty", link: "", want: ""},
		{name: "whitespace only", link: "   ", want: ""},
		{name: "https trims trailing slash", link: "https://example.test/a/", want: "https://example.test/a"},
		{name: "http accepted", link: "http://example.test/a", want: "http://example.test/a"},
		{name: "relative link rejected", link: "/only/path", want: ""},
		{name: "non-http scheme rejected", link: "ftp://example.test/a", want: ""},
		{name: "unparsable link rejected", link: "https://exa mple.test/a", want: ""},
		{name: "host case lowered, path case kept", link: "https://Example.test/Story", want: "https://example.test/Story"},
		{name: "scheme and host case lowered with trailing slash", link: "HTTPS://EXAMPLE.TEST/a/", want: "https://example.test/a"},
		{ //nolint:gosec // G101: test fixture, not a real credential
			name: "userinfo case preserved",
			link: "https://User:Pa%73S@Example.TEST:8443/p",
			want: "https://User:Pa%73S@example.test:8443/p",
		},
		{name: "port preserved", link: "https://Example.test:443/a", want: "https://example.test:443/a"},
		{name: "bare host without path", link: "https://Example.test", want: "https://example.test"},
		{name: "query directly after host", link: "https://Example.test?Q=A", want: "https://example.test?Q=A"},
		{name: "ipv6 host lowered", link: "https://[2001:DB8::1]:8080/a", want: "https://[2001:db8::1]:8080/a"},
		{
			name: "query and fragment case preserved",
			link: "https://Example.test/Story?Q=Ab#Frag",
			want: "https://example.test/Story?Q=Ab#Frag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalRSSLink(tt.link); got != tt.want {
				t.Fatalf("canonicalRSSLink(%q) = %q, want %q", tt.link, got, tt.want)
			}
		})
	}
}

func TestGenerateRSSEntryKeyIgnoresSchemeAndHostCase(t *testing.T) {
	const (
		feedURL = "https://feeds.test/case.xml"
		title   = "Story"
	)

	want := GenerateRSSEntryKey(feedURL, "", "https://example.test/story", title)
	if want == "" {
		t.Fatal("GenerateRSSEntryKey(lowercase link) = \"\", want a link key")
	}

	for _, link := range []string{
		"https://example.test/story",
		"https://Example.test/story",
		"HTTPS://EXAMPLE.TEST/story",
	} {
		if got := GenerateRSSEntryKey(feedURL, "", link, title); got != want {
			t.Fatalf("GenerateRSSEntryKey(link=%q) = %q, want %q", link, got, want)
		}
	}

	if got := GenerateRSSEntryKey(feedURL, "", "https://example.test/Story", title); got == want {
		t.Fatalf("GenerateRSSEntryKey(path case variant) = %q, want a different key than %q", got, want)
	}
}

func TestGenerateRSSEntryLookupKeysIncludeLegacyCaseSensitiveKey(t *testing.T) {
	const (
		feedURL = "https://feeds.test/case.xml"
		title   = "Story"
	)

	const (
		preNormalizationKey       = "rss:v2:link:988ce11c31c4db42866ad71ca917335b2ece6454f3cdc370494cd38b11b5d400"
		preNormalizationLegacyKey = "https://feeds.test/case.xml:https://Example.test/story"
	)

	mixedCaseKeys := GenerateRSSEntryLookupKeys(feedURL, "", "https://Example.test/story", title)
	lowercaseKey := GenerateRSSEntryKey(feedURL, "", "https://example.test/story", title)

	for _, want := range []string{lowercaseKey, preNormalizationKey, preNormalizationLegacyKey} {
		if !containsRSSKey(mixedCaseKeys, want) {
			t.Fatalf("GenerateRSSEntryLookupKeys(mixed-case link) = %#v, want it to contain %q", mixedCaseKeys, want)
		}
	}

	lowercaseKeys := GenerateRSSEntryLookupKeys(feedURL, "", "https://example.test/story", title)
	wantLowercaseKeys := []string{
		lowercaseKey,
		"https://feeds.test/case.xml:https://example.test/story",
	}
	if !reflect.DeepEqual(lowercaseKeys, wantLowercaseKeys) {
		t.Fatalf("GenerateRSSEntryLookupKeys(lowercase link) = %#v, want %#v", lowercaseKeys, wantLowercaseKeys)
	}

	guidKeys := GenerateRSSEntryLookupKeys(feedURL, "guid-1", "https://Example.test/story", title)
	wantGUIDKeys := []string{
		GenerateRSSEntryKey(feedURL, "guid-1", "https://Example.test/story", title),
		"https://feeds.test/case.xml:guid-1",
	}
	if !reflect.DeepEqual(guidKeys, wantGUIDKeys) {
		t.Fatalf("GenerateRSSEntryLookupKeys(guid present) = %#v, want %#v", guidKeys, wantGUIDKeys)
	}
}

func containsRSSKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func TestRSSKeyGoldenDigests(t *testing.T) {
	const feedURL = "https://feeds.test/golden.xml"
	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "guid key",
			got:  GenerateRSSEntryKey(feedURL, "golden-guid", "https://example.test/Story/", "Golden Title", published),
			want: "rss:v2:guid:d332456d378de93e9932b938ef23ff5a05efe86dbedad19007441e4047a482d3",
		},
		{
			name: "link key",
			got:  GenerateRSSEntryKey(feedURL, "", "https://example.test/Story/", "Golden Title", published),
			want: "rss:v2:link:bb53aa3c60da63118ccf0c33d7a13698f640c6e2d5f359c3d88e83e035afd65f",
		},
		{
			name: "title and published key",
			got:  GenerateRSSEntryKey(feedURL, "", "", "Golden Title", published),
			want: "rss:v2:title-date:4a285ca784e4365d96e9b6857bb95bfd8c1790529a3506408b09fc9b976c5abc",
		},
		{
			name: "title only key",
			got:  GenerateRSSEntryKey(feedURL, "", "", "Golden Title"),
			want: "rss:v2:title:c39df435d0ff8752ffbb200155edb086ffcb23e66082fbeeab7e85911a90574c",
		},
		{
			name: "content signature",
			got:  GenerateRSSContentSignature(feedURL, "Golden Title", published),
			want: "content-sig:v3:be47707308030c1f2fc984f154eff1d093db2c31dbbefbb921901186d443957f",
		},
		{
			name: "undated content signature",
			got: GenerateRSSEntryContentSignature(RSSEntry{
				FeedURL:     feedURL,
				GUID:        "golden-guid",
				Title:       "Golden Title",
				Description: "Golden body",
			}),
			want: "content-sig:v4:48bed76ba1718f16e72e6a01b7df36b8f41dd972518e8f923d8be479446f1a21",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestRSSEntryContentSignatureUndatedFollowsLinkCaseNormalization pins the one
// signature the link canonicalization moves: the undated v4 signature embeds the
// entry key, so host-case variants of the same story now share it. The dated v3
// signature never sees the link and is unaffected.
func TestRSSEntryContentSignatureUndatedFollowsLinkCaseNormalization(t *testing.T) {
	base := RSSEntry{
		FeedURL:     "https://feeds.test/case.xml",
		Title:       "Story",
		Description: "Body text",
	}

	lowercase := base
	lowercase.Link = "https://example.test/story"
	mixedCase := base
	mixedCase.Link = "https://Example.test/story"
	pathCase := base
	pathCase.Link = "https://example.test/Story"

	got := GenerateRSSEntryContentSignature(mixedCase)
	if !strings.HasPrefix(got, "content-sig:v4:") {
		t.Fatalf("undated signature = %q, want content-sig:v4 prefix", got)
	}
	if want := GenerateRSSEntryContentSignature(lowercase); got != want {
		t.Fatalf("undated signature for host-case variants = %q, want %q", got, want)
	}
	if other := GenerateRSSEntryContentSignature(pathCase); got == other {
		t.Fatalf("undated signature ignored the path case: %q", got)
	}

	published := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	datedMixed := mixedCase
	datedMixed.Published = published
	datedLower := lowercase
	datedLower.Published = published
	if got, want := GenerateRSSEntryContentSignature(datedMixed), GenerateRSSEntryContentSignature(datedLower); got != want {
		t.Fatalf("dated v3 signature must not depend on the link at all: %q vs %q", got, want)
	}
}

// Documents a known, accepted limitation: the bare ":" join in
// legacyRSSEntryKey can collide for a feedURL containing a port and a guid
// shaped like the remainder of that URL. This is a read-only backward-compat
// key (see the comment on legacyRSSEntryKey); the collision is accepted, not
// fixed -- do not "fix" this test without re-reading that comment: changing
// the join format cannot retroactively fix markers already written under the
// old, un-escaped format.
func TestLegacyRSSEntryKeyDocumentedJoinCollision(t *testing.T) {
	a := legacyRSSEntryKey("http://example.com:8080", "foo", "", "")
	b := legacyRSSEntryKey("http://example.com", "8080:foo", "", "")
	if a != b {
		t.Fatalf("expected the documented collision, got distinct keys %q vs %q", a, b)
	}
}
