package model

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

const (
	rssEntryGUIDKeyPrefix       = "rss:v2:guid:"
	rssEntryLinkKeyPrefix       = "rss:v2:link:"
	rssEntryTitleDateKeyPrefix  = "rss:v2:title-date:"
	rssEntryTitleKeyPrefix      = "rss:v2:title:"
	rssContentSignatureV4Prefix = "content-sig:v4:"
	rssContentSignatureV3Prefix = "content-sig:v3:"
	rssContentSignatureV2Prefix = "content-sig:v2:"
	rssLegacyContentSigPrefix   = "content-sig:"
	rssHashFieldSeparator       = byte(0)
	rssLegacyKeySeparator       = ":"
)

// RSSEntry represents one normalized feed entry independent from a parser implementation.
type RSSEntry struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Author      string    `json:"author"`
	Categories  []string  `json:"categories"`
	GUID        string    `json:"guid"`
	FeedTitle   string    `json:"feed_title"`
	FeedURL     string    `json:"feed_url"`
}

// GenerateRSSEntryKey creates a stable best-effort deduplication key for an RSS item.
func GenerateRSSEntryKey(feedURL, guid, link, title string, published ...time.Time) string {
	if normalizedGUID := strings.TrimSpace(guid); normalizedGUID != "" {
		return rssEntryGUIDKeyPrefix + hashRSSKey("guid", feedURL, normalizedGUID)
	}

	if stableLink := canonicalRSSLink(link); stableLink != "" {
		return rssEntryLinkKeyPrefix + hashRSSKey("link", feedURL, stableLink)
	}

	if normalizedTitle := normalizeRSSTitle(title); normalizedTitle != "" {
		if timestamp := optionalRSSSignatureTimestamp(published); timestamp != "" {
			return rssEntryTitleDateKeyPrefix + hashRSSKey("title-date", feedURL, normalizedTitle, timestamp)
		}
		return rssEntryTitleKeyPrefix + hashRSSKey("title", feedURL, normalizedTitle)
	}

	return ""
}

func GenerateRSSEntryKeyForEntry(entry RSSEntry) string {
	return GenerateRSSEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title, entry.Published)
}

// GenerateRSSEntryLookupKeys returns the current opaque key plus legacy natural keys.
func GenerateRSSEntryLookupKeys(feedURL, guid, link, title string, published ...time.Time) []string {
	currentKey := GenerateRSSEntryKey(feedURL, guid, link, title, published...)
	if usesDatedTitleFallback(guid, link, title, published) {
		return uniqueRSSKeys(currentKey)
	}
	keys := []string{
		currentKey,
		GenerateRSSEntryKey(feedURL, guid, link, title),
		legacyRSSEntryKey(feedURL, guid, link, title),
	}
	keys = append(keys, legacyCaseSensitiveRSSEntryKeys(feedURL, guid, link)...)
	return uniqueRSSKeys(keys...)
}

func GenerateRSSEntryLookupKeysForEntry(entry RSSEntry) []string {
	return GenerateRSSEntryLookupKeys(entry.FeedURL, entry.GUID, entry.Link, entry.Title, entry.Published)
}

// GenerateRSSContentSignature creates a content-based signature for an RSS item.
func GenerateRSSContentSignature(feedURL, title string, published time.Time) string {
	return generateRSSContentSignature(title, published, "")
}

// GenerateRSSEntryContentSignature creates a precise content-based signature for an RSS entry.
func GenerateRSSEntryContentSignature(entry RSSEntry) string {
	if entry.Published.IsZero() {
		if discriminator := GenerateRSSEntryKeyForEntry(entry); discriminator != "" {
			return generateRSSUndatedContentSignature(entry.FeedURL, entry.Title, entry.Description, discriminator)
		}
	}
	return generateRSSContentSignature(entry.Title, entry.Published, entry.Description)
}

// GenerateRSSContentSignatureLookupKeys returns the current opaque signature plus legacy signatures.
func GenerateRSSContentSignatureLookupKeys(feedURL, title string, published time.Time) []string {
	return uniqueRSSKeys(
		GenerateRSSContentSignature(feedURL, title, published),
		legacyRSSV3ContentSignature(feedURL, title, published, ""),
		legacyRSSV2ContentSignature(feedURL, title, published),
		legacyRSSContentSignature(feedURL, title, published),
	)
}

// GenerateRSSEntryContentSignatureLookupKeys returns the current entry signature plus legacy signatures.
func GenerateRSSEntryContentSignatureLookupKeys(entry RSSEntry) []string {
	if entry.Published.IsZero() {
		return uniqueRSSKeys(GenerateRSSEntryContentSignature(entry))
	}
	return uniqueRSSKeys(
		GenerateRSSEntryContentSignature(entry),
		GenerateRSSContentSignature(entry.FeedURL, entry.Title, entry.Published),
		legacyRSSV3ContentSignature(entry.FeedURL, entry.Title, entry.Published, entry.Description),
		legacyRSSV3ContentSignature(entry.FeedURL, entry.Title, entry.Published, ""),
		legacyRSSV2ContentSignature(entry.FeedURL, entry.Title, entry.Published),
		legacyRSSContentSignature(entry.FeedURL, entry.Title, entry.Published),
	)
}

func generateRSSUndatedContentSignature(feedURL, title, description, discriminator string) string {
	return rssContentSignatureV4Prefix + hashRSSKey(
		"content-v4",
		feedURL,
		normalizeRSSTitle(title),
		normalizeRSSDescription(description),
		discriminator,
	)
}

func generateRSSContentSignature(title string, published time.Time, description string) string {
	return rssContentSignatureV3Prefix + hashRSSKey(
		"content-v3",
		normalizeRSSTitle(title),
		rssSignatureTimestamp(published),
		normalizeRSSDescription(description),
	)
}

func legacyRSSV3ContentSignature(feedURL, title string, published time.Time, description string) string {
	return rssContentSignatureV3Prefix + hashRSSKey(
		"content-v3",
		feedURL,
		normalizeRSSTitle(title),
		rssSignatureTimestamp(published),
		normalizeRSSDescription(description),
	)
}

func legacyRSSV2ContentSignature(feedURL, title string, published time.Time) string {
	return rssContentSignatureV2Prefix + hashRSSKey("content", feedURL, normalizeRSSTitle(title), rssSignatureDate(published))
}

func legacyRSSContentSignature(feedURL, title string, published time.Time) string {
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	return strings.Join([]string{
		strings.TrimSuffix(rssLegacyContentSigPrefix, rssLegacyKeySeparator),
		feedURL,
		normalizedTitle,
		rssSignatureDate(published),
	}, rssLegacyKeySeparator)
}

// legacyRSSEntryKey joins feedURL and the entry's GUID/link/title with a bare
// rssLegacyKeySeparator (":") and no escaping. This is a READ-ONLY backward
// compatibility key: it is never written by current code (every write uses
// the escaping-safe opaque key from hashRSSKey), only looked up so markers
// from a pre-opaque-key build still resolve. A feedURL containing ":" (a
// port) and a differently-shaped (feedURL, guid) pair can theoretically
// produce the identical joined string -- left as-is: any change to how this
// string is COMPUTED cannot retroactively fix a collision among markers
// already written under the old, un-escaped format, so there is no additive
// fix that closes the gap; only "document, do not change" applies. See
// TestLegacyRSSEntryKeyDocumentedJoinCollision.
func legacyRSSEntryKey(feedURL, guid, link, title string) string {
	if normalizedGUID := strings.TrimSpace(guid); normalizedGUID != "" {
		return strings.Join([]string{feedURL, normalizedGUID}, rssLegacyKeySeparator)
	}
	if stableLink := canonicalRSSLink(link); stableLink != "" {
		return strings.Join([]string{feedURL, stableLink}, rssLegacyKeySeparator)
	}
	if normalizedTitle := normalizeRSSTitle(title); normalizedTitle != "" {
		return strings.Join([]string{feedURL, normalizedTitle}, rssLegacyKeySeparator)
	}
	return ""
}

func normalizeRSSTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

func normalizeRSSDescription(description string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
}

func optionalRSSSignatureTimestamp(published []time.Time) string {
	if len(published) == 0 || published[0].IsZero() {
		return ""
	}
	return published[0].UTC().Format(time.RFC3339Nano)
}

func usesDatedTitleFallback(guid, link, title string, published []time.Time) bool {
	return strings.TrimSpace(guid) == "" &&
		canonicalRSSLink(link) == "" &&
		normalizeRSSTitle(title) != "" &&
		optionalRSSSignatureTimestamp(published) != ""
}

func rssSignatureTimestamp(published time.Time) string {
	if published.IsZero() {
		return ""
	}
	return published.UTC().Format(time.RFC3339Nano)
}

func rssSignatureDate(published time.Time) string {
	if published.IsZero() {
		return ""
	}
	return timeutil.FormatDateOnly(published)
}

func hashRSSKey(namespace string, values ...string) string {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{rssHashFieldSeparator})
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{rssHashFieldSeparator})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func uniqueRSSKeys(keys ...string) []string {
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

// canonicalRSSLink returns the deduplication form of an entry link: validated,
// with a trailing slash removed and the scheme and host lowercased. It is empty
// for links that cannot identify an entry.
func canonicalRSSLink(link string) string {
	validated := validatedRSSLink(link)
	if validated == "" {
		return ""
	}
	return lowercaseURLSchemeAndHost(validated)
}

// validatedRSSLink accepts only absolute http(s) links with a host and returns
// them without a trailing slash, in their original spelling.
func validatedRSSLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return strings.TrimRight(link, "/")
	default:
		return ""
	}
}

// lowercaseURLSchemeAndHost lowercases the scheme and the host of an absolute
// URL. Userinfo, path, query and fragment stay byte-identical; no percent-,
// port- or IDNA normalization is applied.
func lowercaseURLSchemeAndHost(link string) string {
	separator := strings.Index(link, "://")
	if separator < 0 {
		return link
	}
	authorityStart := separator + len("://")
	authorityEnd := len(link)
	if offset := strings.IndexAny(link[authorityStart:], "/?#"); offset >= 0 {
		authorityEnd = authorityStart + offset
	}
	hostStart := authorityStart
	if at := strings.LastIndex(link[authorityStart:authorityEnd], "@"); at >= 0 {
		hostStart = authorityStart + at + 1
	}
	return strings.ToLower(link[:separator]) + "://" +
		link[authorityStart:hostStart] +
		strings.ToLower(link[hostStart:authorityEnd]) +
		link[authorityEnd:]
}

// legacyCaseSensitiveRSSEntryKeys returns the keys an entry had before the
// scheme/host lowercasing, so markers written by an older build still match.
func legacyCaseSensitiveRSSEntryKeys(feedURL, guid, link string) []string {
	if strings.TrimSpace(guid) != "" {
		return nil
	}
	rawLink := validatedRSSLink(link)
	if rawLink == "" || rawLink == lowercaseURLSchemeAndHost(rawLink) {
		return nil
	}
	return []string{
		rssEntryLinkKeyPrefix + hashRSSKey("link", feedURL, rawLink),
		strings.Join([]string{feedURL, rawLink}, rssLegacyKeySeparator),
	}
}
