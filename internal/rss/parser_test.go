package rss

import (
	"context"
	"errors"
	"fmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newLocalFeedTestParser(retryCount int, retryDelay, workerTimeout time.Duration, _ ...any) *Parser {
	p := NewParser(retryCount, retryDelay, workerTimeout)
	opts := feedurl.Options{AllowPlainHTTP: true, AllowPrivateNetworks: true}
	p.feedURLOpts = opts
	p.httpClient = newFeedHTTPClient(opts)
	return p
}

func parseLocalFeedFixture(t *testing.T, parser *Parser, body string) ([]Entry, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	feedURL := server.URL + "/feed.xml"
	entries, err := parser.ParseFeed(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	return entries, feedURL
}

func TestParseFeedWithValidatorsSendsConditionalHeadersAndHandlesNotModified(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	lastModified := "Wed, 21 Oct 2015 07:28:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"old-etag"` {
			t.Fatalf("If-None-Match = %q, want old etag", got)
		}
		if got := r.Header.Get("If-Modified-Since"); got != lastModified {
			t.Fatalf("If-Modified-Since = %q, want %q", got, lastModified)
		}
		w.Header().Set("ETag", `"current-etag"`)
		w.Header().Set("Last-Modified", lastModified)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	entries, metadata, err := parser.ParseFeedWithValidators(
		context.Background(),
		server.URL+"/feed.xml",
		FeedHTTPValidators{ETag: `"old-etag"`, LastModified: lastModified},
	)
	if err != nil {
		t.Fatalf("ParseFeedWithValidators() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want none for 304", len(entries))
	}
	if !metadata.NotModified {
		t.Fatal("NotModified = false, want true")
	}
	if metadata.Validators.ETag != `"current-etag"` || metadata.Validators.LastModified != lastModified {
		t.Fatalf("validators = %+v, want response validators", metadata.Validators)
	}
}

func TestParseFeedWithValidatorsCapturesResponseValidators(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	lastModified := "Thu, 22 Oct 2015 07:28:00 GMT"
	body := rssFeedFixture(`<item>
      <title>Cached Feed Item</title>
      <link>https://example.test/cached</link>
      <guid>cached-guid</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
    </item>`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"etag-v1"`)
		w.Header().Set("Last-Modified", lastModified)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	entries, metadata, err := parser.ParseFeedWithValidators(context.Background(), server.URL+"/feed.xml", FeedHTTPValidators{})
	if err != nil {
		t.Fatalf("ParseFeedWithValidators() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if metadata.NotModified {
		t.Fatal("NotModified = true, want false")
	}
	if metadata.Validators.ETag != `"etag-v1"` || metadata.Validators.LastModified != lastModified {
		t.Fatalf("validators = %+v, want response validators", metadata.Validators)
	}
}

func TestParseFeedSendsFeedFriendlyRequestHeaders(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	body := rssFeedFixture(`<item>
      <title>Header Check</title>
      <link>https://example.test/header-check</link>
      <guid>header-check</guid>
    </item>`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent := r.Header.Get("User-Agent")
		if !strings.Contains(userAgent, "Ransomware-Bot") {
			t.Fatalf("User-Agent = %q, want Ransomware-Bot identifier", userAgent)
		}
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "application/rss+xml") || !strings.Contains(accept, "application/atom+xml") {
			t.Fatalf("Accept = %q, want RSS and Atom media types", accept)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	entries, err := parser.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestParseMultipleFeedsWithValidatorsKeepsPerFeedHeaders(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	newServer := func(expectedETag string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("If-None-Match"); got != expectedETag {
				t.Fatalf("If-None-Match = %q, want %q", got, expectedETag)
			}
			w.Header().Set("ETag", expectedETag+"-next")
			w.WriteHeader(http.StatusNotModified)
		}))
	}
	first := newServer(`"feed-one"`)
	defer first.Close()
	second := newServer(`"feed-two"`)
	defer second.Close()

	validators := map[string]FeedHTTPValidators{
		first.URL + "/feed.xml":  {ETag: `"feed-one"`},
		second.URL + "/feed.xml": {ETag: `"feed-two"`},
	}
	results, err := parser.ParseMultipleFeedsWithValidators(
		context.Background(),
		[]string{first.URL + "/feed.xml", second.URL + "/feed.xml"},
		2,
		validators,
	)
	if err != nil {
		t.Fatalf("ParseMultipleFeedsWithValidators() error = %v", err)
	}
	for feedURL, validator := range validators {
		if !results.FeedNotModified[feedURL] {
			t.Fatalf("FeedNotModified[%q] = false, want true", feedURL)
		}
		if got := results.FeedValidators[feedURL].ETag; got != validator.ETag+"-next" {
			t.Fatalf("FeedValidators[%q].ETag = %q, want %q", feedURL, got, validator.ETag+"-next")
		}
	}
}

func TestParseFeedNormalizesNestedHTMLEntitiesInTextFields(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	raw := "YARA-X&&#x23&#x3b;x26&#x3b;&#x23&#x3b;39&#x3b;s 1.18.0 release brings 3 improvements and 2 bugfixes.&#xd;"
	escaped := strings.ReplaceAll(raw, "&", "&amp;")
	want := "YARA-X's 1.18.0 release brings 3 improvements and 2 bugfixes."
	body := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>` + escaped + `</title>
    <item>
      <title>` + escaped + `</title>
      <link>https://example.test/yara-x-1-18-0</link>
      <guid>private-sector-yara-x-1-18-0</guid>
      <description><![CDATA[<p>` + raw + `</p>]]></description>
      <author>` + escaped + `</author>
      <category>` + escaped + `</category>
    </item>
  </channel>
</rss>`

	entries, _ := parseLocalFeedFixture(t, parser, body)

	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Title != want {
		t.Fatalf("entry title = %q, want %q", entry.Title, want)
	}
	if entry.Description != want {
		t.Fatalf("entry description = %q, want %q", entry.Description, want)
	}
	if entry.Author != want {
		t.Fatalf("entry author = %q, want %q", entry.Author, want)
	}
	if entry.FeedTitle != want {
		t.Fatalf("feed title = %q, want %q", entry.FeedTitle, want)
	}
	if len(entry.Categories) != 1 || entry.Categories[0] != want {
		t.Fatalf("categories = %#v, want [%q]", entry.Categories, want)
	}
}

func rssFeedFixture(items string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example Feed</title>
` + items + `
  </channel>
</rss>`
}

func TestLogFeedErrorSummaryUsesStructuredRedactedFields(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	logFeedErrorSummary(map[string]string{ //nolint:gosec // G101: test fixture, not a real credential
		"https://user:secret@example.com/rss?token=secret": "failed https://user:secret@example.com/rss?token=secret",
	})

	entries := hook.AllEntries()
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2", len(entries))
	}
	if entries[0].Level.String() != "error" {
		t.Fatalf("summary level = %s, want error", entries[0].Level)
	}
	if entries[1].Level.String() != "error" {
		t.Fatalf("feed error level = %s, want error", entries[1].Level)
	}
	if got := entries[0].Data["failed_feed_count"]; got != 1 {
		t.Fatalf("failed_feed_count = %v, want 1", got)
	}

	feedURL, ok := entries[1].Data["feed_url"].(string)
	if !ok {
		t.Fatalf("feed_url field missing or non-string: %#v", entries[1].Data["feed_url"])
	}
	if strings.Contains(feedURL, "secret") {
		t.Fatalf("feed_url field leaked credential: %q", feedURL)
	}

	errorText, ok := entries[1].Data["error"].(string)
	if !ok {
		t.Fatalf("error field missing or non-string: %#v", entries[1].Data["error"])
	}
	if strings.Contains(errorText, "secret") {
		t.Fatalf("error field leaked credential: %q", errorText)
	}
}

func TestGenerateEntryKey(t *testing.T) {
	feedURL := "https://example.com/feed"

	tests := []struct {
		name       string
		guid       string
		link       string
		title      string
		wantPrefix string
	}{
		{
			"prefers GUID",
			"unique-123", "https://link.com", "Title",
			"rss:v2:guid:",
		},
		{
			"falls back to link",
			"", "https://example.com/article", "Title",
			"rss:v2:link:",
		},
		{
			"falls back to normalized title",
			"", "", "  Hello World  ",
			"rss:v2:title:",
		},
		{
			"empty fields",
			"", "", "",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateEntryKey(feedURL, tt.guid, tt.link, tt.title)
			if tt.wantPrefix == "" {
				if result != "" {
					t.Errorf("GenerateEntryKey() = %q, want empty key", result)
				}
				return
			}
			if !strings.HasPrefix(result, tt.wantPrefix) {
				t.Errorf("GenerateEntryKey() = %q, want prefix %q", result, tt.wantPrefix)
			}
			for _, forbidden := range []string{tt.guid, "example.com/article", "hello world"} {
				if forbidden != "" && strings.Contains(result, forbidden) {
					t.Errorf("GenerateEntryKey() leaked source identity %q in opaque key: %q", forbidden, result)
				}
			}
		})
	}
}

func TestGenerateEntryLookupKeysIncludesLegacyKey(t *testing.T) {
	feedURL := "https://example.com/feed"

	keys := GenerateEntryLookupKeys(feedURL, "", "https://example.com/article/", "Ignored")
	if len(keys) != 2 {
		t.Fatalf("GenerateEntryLookupKeys() returned %d keys, want current + legacy", len(keys))
	}
	if !strings.HasPrefix(keys[0], "rss:v2:link:") {
		t.Fatalf("primary key = %q, want opaque link key", keys[0])
	}
	if keys[1] != feedURL+":https://example.com/article" {
		t.Fatalf("legacy key = %q, want canonical legacy link key", keys[1])
	}
}

func TestParseFeedRejectsItemsWithoutStableIdentity(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, _ := parseLocalFeedFixture(t, parser, rssFeedFixture(`
    <item>
      <description>missing guid, link, and title</description>
    </item>
    <item>
      <title>  Valid Item  </title>
      <description>valid title fallback</description>
    </item>`))

	if len(entries) != 1 {
		t.Fatalf("ParseFeed() returned %d entries, want 1", len(entries))
	}
	if entries[0].Title != "Valid Item" {
		t.Fatalf("entry title = %q, want Valid Item", entries[0].Title)
	}
}

func TestParseFeedReturnsParsedItemsWithoutPersistentFiltering(t *testing.T) {
	feedBody := rssFeedFixture(`
    <item>
      <title>Already Parsed</title>
      <link>https://example.test/already</link>
      <guid>already-guid</guid>
      <pubDate>Thu, 15 Jan 2026 10:00:00 GMT</pubDate>
    </item>
    <item>
      <title>New Article</title>
      <link>https://example.test/new</link>
      <guid>new-guid</guid>
      <pubDate>Thu, 15 Jan 2026 11:00:00 GMT</pubDate>
    </item>`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedBody))
	}))
	defer server.Close()
	feedURL := server.URL + "/feed.xml"

	parser := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, err := parser.ParseFeed(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("ParseFeed() returned %d entries, want both stable feed items", len(entries))
	}
	if entries[0].Title != "Already Parsed" || entries[1].Title != "New Article" {
		t.Fatalf("returned titles = %q, %q", entries[0].Title, entries[1].Title)
	}
}

func TestGenerateContentSignature(t *testing.T) {
	feedURL := "https://example.com/feed"
	published := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		title     string
		published time.Time
		forbidden string
	}{
		{
			"normalized title + date",
			"  My Article  ",
			published,
			"my article",
		},
		{
			"same title different case produces same signature",
			"MY ARTICLE",
			published,
			"my article",
		},
		{
			"zero time omits date",
			"My Article",
			time.Time{},
			"my article",
		},
		{
			"empty title",
			"",
			published,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateContentSignature(feedURL, tt.title, tt.published)
			if !strings.HasPrefix(result, "content-sig:v3:") {
				t.Errorf("GenerateContentSignature() = %q, want opaque v3 prefix", result)
			}
			if tt.forbidden != "" && strings.Contains(result, tt.forbidden) {
				t.Errorf("GenerateContentSignature() leaked source title in opaque key: %q", result)
			}
		})
	}

	// Verify that link variants produce the same content signature
	sig1 := GenerateContentSignature(feedURL, "Ransomware Attack on ACME", published)
	sig2 := GenerateContentSignature(feedURL, "Ransomware Attack on ACME", published)
	if sig1 != sig2 {
		t.Errorf("identical content should produce identical signatures: %q != %q", sig1, sig2)
	}
}

func TestGenerateEntryContentSignaturePrecision(t *testing.T) {
	feedURL := "https://example.com/feed"
	published := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	base := Entry{
		FeedURL:     feedURL,
		Title:       "Daily Advisory",
		Description: "Same incident details",
		Published:   published,
	}
	linkVariant := base
	linkVariant.Link = "https://example.com/article-variant"
	if GenerateEntryContentSignature(base) != GenerateEntryContentSignature(linkVariant) {
		t.Fatal("link variants with identical content should share a content signature")
	}

	laterSameDay := base
	laterSameDay.Published = published.Add(2 * time.Hour)
	if GenerateEntryContentSignature(base) == GenerateEntryContentSignature(laterSameDay) {
		t.Fatal("same-title articles with different full timestamps should not share a content signature")
	}

	differentDescription := base
	differentDescription.Description = "Different incident details"
	if GenerateEntryContentSignature(base) == GenerateEntryContentSignature(differentDescription) {
		t.Fatal("same-title articles with different descriptions should not share a content signature")
	}
}

func TestGenerateEntryContentSignatureMatchesAcrossFeedsForSameDatedArticle(t *testing.T) {
	published := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	firstFeed := Entry{
		FeedURL:     "https://vendor-a.example/feed.xml",
		Title:       "Shared Advisory",
		Link:        "https://example.com/shared-advisory",
		Description: "Same incident details",
		Published:   published,
	}
	secondFeed := firstFeed
	secondFeed.FeedURL = "https://vendor-b.example/feed.xml"

	if GenerateEntryContentSignature(firstFeed) != GenerateEntryContentSignature(secondFeed) {
		t.Fatal("same dated RSS article from different feeds should share a content signature")
	}
}

func TestGenerateEntryContentSignatureUsesIdentityForUndatedEntries(t *testing.T) {
	feedURL := "https://example.com/feed"
	base := Entry{
		FeedURL: feedURL,
		Title:   "Security Advisory",
		Link:    "https://example.com/advisory-one",
	}
	otherLink := base
	otherLink.Link = "https://example.com/advisory-two"

	if GenerateEntryContentSignature(base) == GenerateEntryContentSignature(otherLink) {
		t.Fatal("undated same-title entries with different links should not share a content signature")
	}
	if got := GenerateEntryContentSignature(base); !strings.HasPrefix(got, "content-sig:v4:") {
		t.Fatalf("undated entry content signature = %q, want opaque v4 signature", got)
	}
	if keys := GenerateEntryContentSignatureLookupKeys(base); len(keys) != 1 {
		t.Fatalf("undated content lookup keys = %d, want only identity-aware current key: %#v", len(keys), keys)
	}
}

func TestGenerateEntryKeyUsesDateForTitleFallback(t *testing.T) {
	feedURL := "https://example.com/feed"
	first := GenerateEntryKey(feedURL, "", "", "Daily Briefing", time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC))
	second := GenerateEntryKey(feedURL, "", "", "Daily Briefing", time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC))

	if first == "" || second == "" {
		t.Fatalf("title-date fallback keys must not be empty: first=%q second=%q", first, second)
	}
	if first == second {
		t.Fatal("title-only RSS entries with different dates should not share a key")
	}
	if !strings.HasPrefix(first, "rss:v2:title-date:") {
		t.Fatalf("dated title fallback key = %q, want title-date prefix", first)
	}
}

func TestGenerateContentSignatureLookupKeysIncludesLegacySignature(t *testing.T) {
	feedURL := "https://example.com/feed"
	published := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	keys := GenerateContentSignatureLookupKeys(feedURL, "  My Article  ", published)
	if len(keys) != 4 {
		t.Fatalf("GenerateContentSignatureLookupKeys() returned %d keys, want current + legacy v3 + v2 + legacy", len(keys))
	}
	if !strings.HasPrefix(keys[0], "content-sig:v3:") {
		t.Fatalf("primary content signature = %q, want opaque v3 signature", keys[0])
	}
	if !strings.HasPrefix(keys[1], "content-sig:v3:") {
		t.Fatalf("legacy v3 content signature = %q, want opaque v3 signature", keys[1])
	}
	if !strings.HasPrefix(keys[2], "content-sig:v2:") {
		t.Fatalf("v2 content signature = %q, want opaque v2 signature", keys[2])
	}
	if keys[3] != "content-sig:https://example.com/feed:my article:2025-01-15" {
		t.Fatalf("legacy content signature = %q, want legacy title-bearing signature", keys[3])
	}
}

func TestParseFeedMapsAuthorDateAndCategories(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, _ := parseLocalFeedFixture(t, parser, rssFeedFixture(`
    <item>
      <title>Observable Item</title>
      <guid>observable-guid</guid>
      <link>https://example.test/observable</link>
      <author>Jane Doe</author>
      <pubDate>Thu, 15 Jan 2026 10:00:00 GMT</pubDate>
      <category> security </category>
      <category></category>
      <category>ransomware</category>
    </item>`))

	if len(entries) != 1 {
		t.Fatalf("ParseFeed() returned %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Author != "Jane Doe" {
		t.Fatalf("Author = %q, want Jane Doe", entry.Author)
	}
	wantPublished := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	if !entry.Published.Equal(wantPublished) {
		t.Fatalf("Published = %v, want %v", entry.Published, wantPublished)
	}
	if fmt.Sprint(entry.Categories) != "[security ransomware]" {
		t.Fatalf("Categories = %#v, want trimmed non-empty categories", entry.Categories)
	}
}

func TestParseFeedUsesUpdatedDateWhenPublishedIsMissing(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, _ := parseLocalFeedFixture(t, parser, `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example Atom Feed</title>
  <entry>
    <title>Updated Only</title>
    <id>updated-only</id>
    <link href="https://example.test/updated-only"/>
    <updated>2026-01-16T12:30:00Z</updated>
  </entry>
</feed>`)

	if len(entries) != 1 {
		t.Fatalf("ParseFeed() returned %d entries, want 1", len(entries))
	}
	wantPublished := time.Date(2026, 1, 16, 12, 30, 0, 0, time.UTC)
	if !entries[0].Published.Equal(wantPublished) {
		t.Fatalf("Published = %v, want updated fallback %v", entries[0].Published, wantPublished)
	}
}

func TestNewParserSupportsPublicOperations(t *testing.T) {
	p := NewParser(0, time.Second, 30*time.Second)

	if p == nil {
		t.Fatal("NewParser() returned nil")
	}
	results, err := p.ParseMultipleFeeds(context.Background(), nil, 5)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds(empty) error = %v", err)
	}
	if results == nil {
		t.Fatal("ParseMultipleFeeds(empty) returned nil results")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ParseFeed(ctx, "https://example.com/feed.xml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseFeed(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestParseMultipleFeeds_EmptyList(t *testing.T) {
	p := NewParser(0, time.Second, 30*time.Second)

	ctx := context.Background()
	results, err := p.ParseMultipleFeeds(ctx, []string{}, 5)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds() with empty list returned error: %v", err)
	}
	if results == nil {
		t.Fatal("expected non-nil FeedResults")
	}
	if len(results.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(results.Entries))
	}
	if len(results.FeedErrors) != 0 {
		t.Errorf("expected 0 feed errors, got %d", len(results.FeedErrors))
	}
}

func TestParseMultipleFeedsRecoversWorkerPanicAsFeedError(t *testing.T) {
	p := newLocalFeedTestParser(0, time.Millisecond, time.Second, nil)
	p.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("transport panic")
	})}

	feedURL := "http://example.test/feed.xml"
	results, err := p.ParseMultipleFeeds(context.Background(), []string{feedURL}, 1)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds() error = %v, want per-feed error result", err)
	}
	if results == nil {
		t.Fatal("ParseMultipleFeeds() returned nil results")
	}
	got := results.FeedErrors[feedURL]
	if !strings.Contains(got, "panic") || !strings.Contains(got, "transport panic") {
		t.Fatalf("feed error = %q, want recovered panic details", got)
	}
	if entries := results.Entries[feedURL]; len(entries) != 0 {
		t.Fatalf("entries = %d, want none for recovered panic", len(entries))
	}
}

func TestParseMultipleFeeds_WorkerLimiting(t *testing.T) {
	// retryCount=0 so failures return immediately without retries
	p := newLocalFeedTestParser(0, time.Second, 10*time.Second)

	serverOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverOne.Close()
	serverTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverTwo.Close()

	ctx := context.Background()
	urls := []string{
		serverOne.URL + "/rss",
		serverTwo.URL + "/rss",
	}

	// maxWorkers=1 should cap workers to 1 even though we have 2 URLs.
	// The feeds return deterministic HTTP failures, but the function should handle the errors
	// and return per-feed errors rather than a fatal error.
	results, err := p.ParseMultipleFeeds(ctx, urls, 1)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds() returned unexpected fatal error: %v", err)
	}

	if results == nil {
		t.Fatal("expected non-nil FeedResults")
	}

	// Both feeds should have errors because the local servers return deterministic failures.
	if len(results.FeedErrors) != 2 {
		t.Errorf("expected 2 feed errors, got %d", len(results.FeedErrors))
	}

	// Each feed should have an entry in results (even if empty due to error)
	for _, url := range urls {
		if _, exists := results.Entries[url]; !exists {
			t.Errorf("expected Entries map to contain key for %q", url)
		}
		if _, exists := results.FeedErrors[url]; !exists {
			t.Errorf("expected FeedErrors map to contain key for %q", url)
		}
	}
}

func TestParseFeed_ContextCancel(t *testing.T) {
	p := NewParser(0, time.Second, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := p.ParseFeed(ctx, "https://example.com/feed.xml")
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	if ctx.Err() == nil {
		t.Fatal("expected context error to be set")
	}
}

func TestParseFeed_RetryOnFailure(t *testing.T) {
	// retryCount=0: no retries, should fail immediately
	p := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := p.ParseFeed(ctx, server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("expected error for failing feed URL")
	}

	// Error should indicate 1 attempt (retryCount=0 means 1 attempt total)
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestParseMultipleFeeds_ContextCancel(t *testing.T) {
	p := NewParser(0, time.Second, 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before calling

	urls := []string{
		"https://example1.com/feed.xml",
		"https://example2.com/feed.xml",
		"https://example3.com/feed.xml",
	}

	results, err := p.ParseMultipleFeeds(ctx, urls, 2)

	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ParseMultipleFeeds() error = %v, want context.Canceled", err)
		}
		return
	}

	if results == nil {
		t.Fatal("expected non-nil results when err is nil")
	}
	for _, url := range urls {
		errMsg, ok := results.FeedErrors[url]
		if !ok {
			t.Fatalf("missing cancellation FeedErrors entry for %q: %#v", url, results.FeedErrors)
		}
		if !strings.Contains(strings.ToLower(errMsg), "cancel") {
			t.Fatalf("FeedErrors[%q] = %q, want cancellation error", url, errMsg)
		}
	}
}

// TestParseMultipleFeedsReturnsPartialResultsWhenCycleBudgetExpires pins the
// package-boundary contract for an expired caller context (the RSS cycle
// budget): a feed that answered in time keeps its entry, a feed that never
// answered is absent from both FeedResults maps rather than reported failed,
// and the error is context.DeadlineExceeded so the caller can tell the two
// apart from a shutdown cancellation.
func TestParseMultipleFeedsReturnsPartialResultsWhenCycleBudgetExpires(t *testing.T) {
	fastURL := "http://feeds.example/fast.xml"
	hungURL := "http://feeds.example/hung.xml"

	p := newLocalFeedTestParser(0, time.Millisecond, 10*time.Second)
	p.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == hungURL {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(validRSSXML("Fast Article"))),
		}, nil
	})}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results, err := p.ParseMultipleFeeds(ctx, []string{fastURL, hungURL}, 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ParseMultipleFeeds() error = %v, want context.DeadlineExceeded", err)
	}
	if results == nil {
		t.Fatal("ParseMultipleFeeds() = <nil>, want partial results")
	}
	if got := len(results.Entries[fastURL]); got != 1 {
		t.Fatalf("Entries[%q] = %d items, want 1", fastURL, got)
	}
	if _, exists := results.Entries[hungURL]; exists {
		t.Fatalf("Entries[%q] exists, want the not-answered feed absent", hungURL)
	}
	if _, exists := results.FeedErrors[hungURL]; exists {
		t.Fatalf("FeedErrors[%q] exists, want the not-answered feed absent", hungURL)
	}
}

// --- httptest-based tests for retry loop, worker timeout, and partial results ---

// validRSSXML returns a minimal valid RSS 2.0 feed with one item.
func validRSSXML(title string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>https://example.com</link>
    <item>
      <title>%s</title>
      <link>https://example.com/article</link>
      <guid>guid-1</guid>
    </item>
  </channel>
</rss>`, title)
}

func writeHTTPTestResponse(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := fmt.Fprint(w, body); err != nil {
		t.Errorf("write test HTTP response: %v", err)
	}
}

func writeHTTPTestResponsef(t *testing.T, w http.ResponseWriter, format string, args ...any) {
	t.Helper()
	if _, err := fmt.Fprintf(w, format, args...); err != nil { //nolint:gosec // G705: local httptest fixture body, not a real HTTP response to a browser
		t.Errorf("write test HTTP response: %v", err)
	}
}

func TestParseFeedReturnsStableParsedItemWithoutStatusLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, validRSSXML("Already Parsed Article"))
	}))
	defer server.Close()

	feedURL := server.URL + "/feed.xml"
	wantKey := GenerateEntryKey(feedURL, "guid-1", "https://example.com/article", "Already Parsed Article")
	p := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, err := p.ParseFeed(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed entries = %d, want 1", len(entries))
	}
	if got := GenerateEntryKeyForEntry(entries[0]); got != wantKey {
		t.Fatalf("entry key = %q, want %q", got, wantKey)
	}
}

func TestParseFeedReturnsNewItemWithStableKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, validRSSXML("New Article"))
	}))
	defer server.Close()

	feedURL := server.URL + "/feed.xml"
	wantKey := GenerateEntryKey(feedURL, "guid-1", "https://example.com/article", "New Article")
	p := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, err := p.ParseFeed(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed entries = %d, want 1", len(entries))
	}
	if got := GenerateEntryKeyForEntry(entries[0]); got != wantKey {
		t.Fatalf("entry key = %q, want %q", got, wantKey)
	}
}

func TestParseFeedKeepsRecurringTitleOnlyItemsWithDifferentDates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <item>
      <title>Daily Briefing</title>
      <pubDate>Mon, 05 Jan 2026 08:00:00 +0000</pubDate>
    </item>
    <item>
      <title>Daily Briefing</title>
      <pubDate>Tue, 06 Jan 2026 08:00:00 +0000</pubDate>
    </item>
  </channel>
</rss>`)
	}))
	defer server.Close()

	p := newLocalFeedTestParser(0, time.Second, 30*time.Second)

	entries, err := p.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed entries = %d, want 2", len(entries))
	}
	firstKey := GenerateEntryKeyForEntry(entries[0])
	secondKey := GenerateEntryKeyForEntry(entries[1])
	if firstKey == "" || secondKey == "" {
		t.Fatalf("entry keys = %q, %q; want stable keys", firstKey, secondKey)
	}
	if firstKey == secondKey {
		t.Fatalf("recurring title-only items share key %q", firstKey)
	}
}

// TestParseFeed_RetryThenSuccess uses retryCount=2 with an httptest server
// that fails once (500) then returns valid RSS on the second attempt.
func TestParseFeed_RetryThenSuccess(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= 1 {
			w.WriteHeader(http.StatusInternalServerError)
			writeHTTPTestResponse(t, w, "server error\n")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		writeHTTPTestResponse(t, w, validRSSXML("Retry Article"))
	}))
	defer server.Close()

	// retryCount=2, retryDelay=0 so retry behavior is immediate and deterministic.
	p := newLocalFeedTestParser(2, 0, 30*time.Second)

	entries, err := p.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	finalCount := requestCount.Load()
	if finalCount != 2 {
		t.Errorf("expected 2 requests (1 fail + 1 success), got %d", finalCount)
	}

	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].Title != "Retry Article" {
		t.Errorf("expected title 'Retry Article', got %q", entries[0].Title)
	}
}

// TestParseFeed_RetryExhausted uses retryCount=1 with a server that always fails.
// Verifies exactly retryCount+1 attempts are made.
func TestParseFeed_RetryExhausted(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		writeHTTPTestResponse(t, w, "always failing\n")
	}))
	defer server.Close()

	p := newLocalFeedTestParser(1, 0, 30*time.Second)

	_, err := p.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}

	// retryCount=1 means 2 total attempts (initial + 1 retry)
	finalCount := requestCount.Load()
	if finalCount != 2 {
		t.Errorf("expected 2 requests (retryCount=1 → 2 attempts), got %d", finalCount)
	}

	if !strings.Contains(err.Error(), "2 attempts") {
		t.Errorf("expected error to mention '2 attempts', got: %v", err)
	}
}

func TestParseFeedDoesNotRetryPermanentHTTPStatus(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNotFound)
		writeHTTPTestResponse(t, w, "feed removed\n")
	}))
	defer server.Close()

	p := newLocalFeedTestParser(3, 0, 30*time.Second)

	_, err := p.ParseFeed(context.Background(), server.URL+"/missing.xml")
	if err == nil {
		t.Fatal("expected permanent HTTP error, got nil")
	}

	if finalCount := requestCount.Load(); finalCount != 1 {
		t.Fatalf("request count = %d, want 1 for permanent HTTP status", finalCount)
	}
	if !strings.Contains(err.Error(), "1 attempts") {
		t.Fatalf("error = %v, want actual attempt count", err)
	}
}

func TestParseFeedDoesNotRetryMalformedFeed(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, "<html>not a feed</html>")
	}))
	defer server.Close()

	p := newLocalFeedTestParser(3, 0, 30*time.Second)

	_, err := p.ParseFeed(context.Background(), server.URL+"/not-feed.xml")
	if err == nil {
		t.Fatal("expected malformed feed error, got nil")
	}

	if finalCount := requestCount.Load(); finalCount != 1 {
		t.Fatalf("request count = %d, want 1 for malformed feed", finalCount)
	}
	if !strings.Contains(err.Error(), "1 attempts") {
		t.Fatalf("error = %v, want actual attempt count", err)
	}
}

func TestParseFeedRejectsHTMLContentTypeBeforeXMLDetection(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeHTTPTestResponse(t, w, "<!doctype html><title>Checking your browser</title>")
	}))
	defer server.Close()

	p := newLocalFeedTestParser(3, 0, 30*time.Second)

	_, err := p.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("expected HTML content type error, got nil")
	}

	if finalCount := requestCount.Load(); finalCount != 1 {
		t.Fatalf("request count = %d, want 1 for non-feed HTML response", finalCount)
	}
	if !strings.Contains(err.Error(), "RSS feed returned non-XML content type") {
		t.Fatalf("error = %v, want non-XML content type detail", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "failed to detect feed type") {
		t.Fatalf("error = %v, want content-type error before XML detection", err)
	}
}

func TestParseFeedTerminalFailureLogsError(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeHTTPTestResponse(t, w, "<!doctype html><title>Checking your browser</title>")
	}))
	defer server.Close()

	p := newLocalFeedTestParser(0, 0, 30*time.Second)

	_, err := p.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("expected terminal parse error, got nil")
	}

	for _, entry := range hook.AllEntries() {
		if entry.Message != "RSS feed parsing failed with terminal error, not retrying" {
			continue
		}
		if entry.Level.String() != "error" {
			t.Fatalf("terminal failure level = %s, want error", entry.Level)
		}
		return
	}
	t.Fatal("expected terminal RSS parse failure log entry")
}

func TestOperatorErrorMessageNormalizesMalformedFeed(t *testing.T) {
	err := fmt.Errorf("failed to parse RSS feed after 1 attempts: failed to detect feed type\n<html>debug body</html>")

	got := OperatorErrorMessage(err)

	if got != "RSS feed is not valid RSS or Atom XML" {
		t.Fatalf("OperatorErrorMessage() = %q", got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "debug body") {
		t.Fatalf("OperatorErrorMessage() leaked raw parser detail: %q", got)
	}
}

func TestOperatorErrorMessageRedactsURLCredentials(t *testing.T) {
	err := fmt.Errorf("request failed for https://user:pass@example.test/feed.xml?token=secret&category=news")

	got := OperatorErrorMessage(err)

	for _, leaked := range []string{"user:pass", "token=secret"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("OperatorErrorMessage() leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "token=%5Bredacted%5D") {
		t.Fatalf("OperatorErrorMessage() did not redact token query: %s", got)
	}
}

// TestParseMultipleFeeds_WorkerTimeout creates a parser with a very short
// workerTimeout and a feed that blocks indefinitely. maxWorkers=1 makes the
// fast feed complete before the slow feed starts and then times out.
func TestParseMultipleFeeds_WorkerTimeout(t *testing.T) {
	// Fast server: responds immediately with valid RSS
	fastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, validRSSXML("Fast Article"))
	}))
	defer fastServer.Close()

	// Slow server: blocks until context is cancelled (guaranteed to exceed timeout)
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until client gives up
	}))
	defer slowServer.Close()

	// workerTimeout=200ms - the slow feed will never respond within this
	p := newLocalFeedTestParser(0, time.Second, 200*time.Millisecond)

	urls := []string{
		fastServer.URL + "/feed.xml",
		slowServer.URL + "/feed.xml",
	}

	results, err := p.ParseMultipleFeeds(context.Background(), urls, 1)

	// Must get a timeout error
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	slowURL := slowServer.URL + "/feed.xml"
	if !strings.Contains(err.Error(), slowURL) {
		t.Fatalf("timeout error = %v, want pending feed URL %q", err, slowURL)
	}

	// Partial results should contain the fast feed
	if results == nil {
		t.Fatal("expected non-nil partial results on timeout")
	}

	fastEntries, ok := results.Entries[fastServer.URL+"/feed.xml"]
	if !ok {
		t.Error("expected fast feed to be in partial results")
	} else if len(fastEntries) != 1 {
		t.Errorf("expected 1 entry from fast feed, got %d", len(fastEntries))
	}

	if slowEntries, ok := results.Entries[slowURL]; !ok {
		t.Error("expected slow feed to be represented in partial results")
	} else if len(slowEntries) != 0 {
		t.Errorf("expected 0 entries from timed-out slow feed, got %d", len(slowEntries))
	}
	slowErr, ok := results.FeedErrors[slowURL]
	if !ok {
		t.Fatal("expected slow feed timeout to be recorded in FeedErrors")
	}
	if !strings.Contains(slowErr, "timed out") {
		t.Fatalf("slow feed error = %q, want timeout context", slowErr)
	}
}

func TestParseMultipleFeedsWorkerTimeoutIsBatchBudget(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, validRSSXML("Slow Article"))
	}))
	defer server.Close()

	p := newLocalFeedTestParser(0, time.Millisecond, 120*time.Millisecond)
	urls := []string{
		server.URL + "/feed-1.xml",
		server.URL + "/feed-2.xml",
		server.URL + "/feed-3.xml",
	}

	start := time.Now()
	results, err := p.ParseMultipleFeeds(context.Background(), urls, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected batch timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "worker pool timeout") {
		t.Fatalf("ParseMultipleFeeds() error = %v, want worker pool timeout", err)
	}
	if elapsed >= 240*time.Millisecond {
		t.Fatalf("worker timeout elapsed = %s, want one batch budget instead of per-result reset", elapsed)
	}
	if results == nil {
		t.Fatal("expected partial results on timeout")
	}
	if got := len(results.FeedErrors); got == 0 {
		t.Fatal("expected pending feeds to be marked with timeout errors")
	}
	if got := requestCount.Load(); got > 2 {
		t.Fatalf("request count = %d, want timeout before processing all feeds", got)
	}
}

// TestParseMultipleFeeds_MixedResults tests a mix of successful and failing feeds.
// Verifies per-feed errors are reported while successful feeds return entries.
func TestParseMultipleFeeds_MixedResults(t *testing.T) {
	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponse(t, w, validRSSXML("Good Article"))
	}))
	defer goodServer.Close()

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTTPTestResponse(t, w, "broken\n")
	}))
	defer badServer.Close()

	p := newLocalFeedTestParser(0, time.Second, 10*time.Second)

	urls := []string{
		goodServer.URL + "/feed.xml",
		badServer.URL + "/feed.xml",
	}

	results, err := p.ParseMultipleFeeds(context.Background(), urls, 2)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}

	// Good feed should have entries
	goodEntries := results.Entries[goodServer.URL+"/feed.xml"]
	if len(goodEntries) != 1 {
		t.Errorf("expected 1 entry from good feed, got %d", len(goodEntries))
	}

	// Bad feed should have an error
	badErr, hasErr := results.FeedErrors[badServer.URL+"/feed.xml"]
	if !hasErr {
		t.Error("expected bad feed to have an error")
	} else if badErr == "" {
		t.Error("expected non-empty error message for bad feed")
	}

	// Bad feed entries should be empty
	badEntries := results.Entries[badServer.URL+"/feed.xml"]
	if len(badEntries) != 0 {
		t.Errorf("expected 0 entries from bad feed, got %d", len(badEntries))
	}
}

func TestParseFeedMapsNamedZonePubDate(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	body := rssFeedFixture(`<item>
      <title>Named Zone Item</title>
      <link>https://example.test/named-zone</link>
      <guid>named-zone-guid</guid>
      <pubDate>Fri, 02 Jan 2026 15:04:05 EST</pubDate>
    </item>`)

	entries, _ := parseLocalFeedFixture(t, parser, body)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	want := time.Date(2026, 1, 2, 20, 4, 5, 0, time.UTC)
	if !entries[0].Published.Equal(want) {
		t.Fatalf("Published = %s, want %s", entries[0].Published.UTC(), want)
	}
}

func TestParseFeedTreatsUnknownZonePubDateAsUndated(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	body := rssFeedFixture(`<item>
      <title>Unknown Zone Item</title>
      <link>https://example.test/unknown-zone</link>
      <guid>unknown-zone-guid</guid>
      <pubDate>Fri, 02 Jan 2026 15:04:05 XYZ</pubDate>
    </item>`)

	entries, _ := parseLocalFeedFixture(t, parser, body)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !entries[0].Published.IsZero() {
		t.Fatalf("Published = %s, want zero time", entries[0].Published.UTC())
	}
}

func serveLocalFeedBytes(t *testing.T, body []byte) ([]Entry, error) {
	t.Helper()
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return parser.ParseFeed(context.Background(), server.URL+"/feed.xml")
}

func encodedRSSFixture(declared, title string) string {
	return `<?xml version="1.0" encoding="` + declared + `"?>
<rss version="2.0">
  <channel>
    <title>Example Feed</title>
    <item>
      <title>` + title + `</title>
      <link>https://example.test/a</link>
      <guid>charset-a</guid>
      <description>` + title + `</description>
    </item>
  </channel>
</rss>`
}

func encodedAtomFixture(declared, title string) string {
	return `<?xml version="1.0" encoding="` + declared + `"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example Atom Feed</title>
  <entry>
    <title>` + title + `</title>
    <link href="https://example.test/a"/>
    <id>charset-a</id>
    <summary>` + title + `</summary>
  </entry>
</feed>`
}

func TestParseFeedDecodesDeclaredCharsets(t *testing.T) {
	identityEncode := func(b []byte) ([]byte, error) { return b, nil }
	utf8BOMEncode := func(b []byte) ([]byte, error) {
		return append([]byte{0xEF, 0xBB, 0xBF}, b...), nil
	}

	tests := []struct {
		name     string
		declared string
		title    string
		encode   func([]byte) ([]byte, error)
	}{
		{
			name:     "iso-8859-1",
			declared: "ISO-8859-1",
			title:    "Attaque cibl\u00e9e",
			encode:   charmap.ISO8859_1.NewEncoder().Bytes,
		},
		{
			name:     "windows-1252",
			declared: "windows-1252",
			title:    "Acme\u2019s breach",
			encode:   charmap.Windows1252.NewEncoder().Bytes,
		},
		{
			name:     "utf-16le-bom",
			declared: "UTF-16",
			title:    "Ransomware \u00e9v\u00e8nement",
			encode:   unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes,
		},
		{
			name:     "utf-16be-bom",
			declared: "UTF-16",
			title:    "Ransomware \u00e9v\u00e8nement",
			encode:   unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().Bytes,
		},
		{
			name:     "utf-8-bom",
			declared: "UTF-8",
			title:    "caf\u00e9 leak",
			encode:   utf8BOMEncode,
		},
		{
			name:     "utf8-no-hyphen",
			declared: "utf8",
			title:    "caf\u00e9 direct",
			encode:   identityEncode,
		},
		{
			// XML 1.0 4.3.3: the byte-order mark outranks the declaration. The
			// body is UTF-8 even though it claims ISO-8859-1, so the declared
			// charset must not be applied on top of it (that would yield
			// "caf\u00c3\u00a9 wins").
			name:     "utf-8-bom-outranks-declaration",
			declared: "ISO-8859-1",
			title:    "caf\u00e9 wins",
			encode:   utf8BOMEncode,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tc.encode([]byte(encodedRSSFixture(tc.declared, tc.title)))
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}

			entries, err := serveLocalFeedBytes(t, body)
			if err != nil {
				t.Fatalf("ParseFeed() error = %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(entries))
			}
			if entries[0].Title != tc.title {
				t.Fatalf("entry title = %q, want %q", entries[0].Title, tc.title)
			}
			if entries[0].Description != tc.title {
				t.Fatalf("entry description = %q, want %q", entries[0].Description, tc.title)
			}
		})
	}
}

func TestParseFeedDecodesHTMLNamedEntitiesInRSS(t *testing.T) {
	const want = "Acme Corp\u2014hit\u2019s"

	entries, err := serveLocalFeedBytes(t, []byte(encodedRSSFixture("UTF-8", "Acme&nbsp;Corp&mdash;hit&rsquo;s")))
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Title != want {
		t.Fatalf("entry title = %q, want %q", entries[0].Title, want)
	}
	if entries[0].Description != want {
		t.Fatalf("entry description = %q, want %q", entries[0].Description, want)
	}
}

func TestParseFeedDecodesHTMLNamedEntitiesInAtom(t *testing.T) {
	const want = "Acme Corp\u2014hit\u2019s"

	entries, err := serveLocalFeedBytes(t, []byte(encodedAtomFixture("UTF-8", "Acme&nbsp;Corp&mdash;hit&rsquo;s")))
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Title != want {
		t.Fatalf("entry title = %q, want %q", entries[0].Title, want)
	}
	if entries[0].Description != want {
		t.Fatalf("entry description = %q, want %q", entries[0].Description, want)
	}
}

// TestUnknownCharsetLabelLogsAreTruncated pins Finding 3: a feed can declare
// an XML encoding label of attacker-controlled size (bounded only by the
// 10 MiB response cap), and both the terminal-error and the retry log lines
// wrote textutil.RedactURLCredentials(err.Error()) with no length bound. The
// first attempt is forced to fail with a huge, retryable *url.Error (via a
// RoundTripper that returns an oversized error once) so the retry-log branch
// fires with an oversized message; the second, real attempt hits the huge
// charset label itself, which is non-retryable and fires the terminal-error
// branch with an oversized message. Both logged "error" fields must stay
// bounded to 240 runes.
func TestUnknownCharsetLabelLogsAreTruncated(t *testing.T) {
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	hugeLabel := strings.Repeat("A", 100000)
	body := []byte(encodedRSSFixture(hugeLabel, "irrelevant"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	parser := newLocalFeedTestParser(1, time.Millisecond, time.Second)
	realTransport := parser.httpClient.Transport
	var calls int32
	parser.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Wrapped by http.Client.Do into *url.Error, which
			// shouldRetryRSSParseError always treats as retryable,
			// regardless of the wrapped message's content.
			return nil, errors.New(strings.Repeat("B", 100000))
		}
		return realTransport.RoundTrip(req)
	})

	_, err := parser.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("expected terminal charset error, got nil")
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want 2 (one retryable failure, one real attempt)", calls)
	}

	const maxLoggedErrorRunes = 240
	assertLoggedErrorFieldBounded := func(message string) {
		t.Helper()
		for _, entry := range hook.AllEntries() {
			if entry.Message != message {
				continue
			}
			raw, ok := entry.Data["error"]
			if !ok {
				t.Fatalf("log entry %q has no \"error\" field", message)
			}
			text, ok := raw.(string)
			if !ok {
				t.Fatalf("log entry %q \"error\" field = %#v, want string", message, raw)
			}
			if got := utf8.RuneCountInString(text); got > maxLoggedErrorRunes {
				t.Fatalf("log entry %q \"error\" field = %d runes, want <= %d", message, got, maxLoggedErrorRunes)
			}
			return
		}
		t.Fatalf("no log entry found with message %q", message)
	}

	assertLoggedErrorFieldBounded("RSS feed parsing failed, retrying...")
	assertLoggedErrorFieldBounded("RSS feed parsing failed with terminal error, not retrying")
}

// TestRSSRetryLogRedactsFeedURLErrorViaAnchor pins a fix: the retry-log
// branch's "error" field is redacted with
// textutil.RedactWebhookSecretsForURL, anchored on the feed URL the request
// was actually made to, instead of textutil.RedactURLCredentials's
// regex/url.Parse heuristic, which has to find a URL inside freeform text
// with no known-good anchor to check its own answer against.
//
// This does NOT pin a credential leak: none could be constructed against
// either redactor for this shape. feedurl.Validate rejects any feed URL with
// userinfo before a request is ever built, so the *url.Error this code
// handles can never carry one through the real fetch path, and net/http's
// own stripPassword additionally scrubs a password from a *url.Error's URL
// field before this code ever sees it. Directly constructing adversarial
// *url.Error values that bypass both of those (every shape
// internal/textutil's own doc comments record as having broken
// RedactURLCredentials across six prior hardening rounds -- authority
// truncation, fragment smuggling, a raw control byte, a bare backslash)
// produced byte-identical output from RedactURLCredentials and
// RedactWebhookSecretsForURL in every case tried, consistent with a prior
// 225-million-execution fuzz campaign against RedactURLCredentials that
// found no divergence either.
//
// What the switch DOES observably change, on every ordinary (credential-free)
// fetch failure -- the overwhelming majority of real ones, since a feed URL
// can never carry a credential past validation -- is that the anchor
// unconditionally blanks the feed URL's own text in the "error" field to a
// generic "scheme://host/[redacted]" marker, where the old heuristic left it
// untouched because nothing about a clean URL is "sensitive" to it. This
// test pins that the switch took effect (the distinctive feed path is no
// longer readable in "error") and that nothing an operator needs is lost:
// the same log entry's "feed_url" field, unaffected by this change, still
// carries the real, credential-safe path.
func TestRSSRetryLogRedactsFeedURLErrorViaAnchor(t *testing.T) {
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	parser := newLocalFeedTestParser(1, time.Millisecond, time.Second)
	parser.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	const feedURL = "https://feeds.example.test/very-distinctive-path/rss.xml"
	_, err := parser.ParseFeed(context.Background(), feedURL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var urlErr *neturl.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error = %T (%v), want a *url.Error somewhere in the chain -- test no longer exercises Finding C's shape", err, err)
	}

	for _, entry := range hook.AllEntries() {
		if entry.Message != "RSS feed parsing failed, retrying..." {
			continue
		}
		errField, ok := entry.Data["error"].(string)
		if !ok {
			t.Fatalf("log entry %q has no string \"error\" field", entry.Message)
		}
		feedURLField, ok := entry.Data["feed_url"].(string)
		if !ok {
			t.Fatalf("log entry %q has no string \"feed_url\" field", entry.Message)
		}
		if strings.Contains(errField, "very-distinctive-path") {
			t.Fatalf(`"error" field = %q, still contains the feed path -- anchored redaction did not fire`, errField)
		}
		if !strings.Contains(errField, "feeds.example.test") {
			t.Fatalf(`"error" field = %q, want the anchored marker to still name the host for diagnosis`, errField)
		}
		if !strings.Contains(feedURLField, "very-distinctive-path") {
			t.Fatalf(`"feed_url" field = %q, want the real path preserved on the same log line so nothing is lost`, feedURLField)
		}
		return
	}
	t.Fatal(`no log entry found with message "RSS feed parsing failed, retrying..."`)
}

func TestParseFeedRejectsUnknownDeclaredEncoding(t *testing.T) {
	entries, err := serveLocalFeedBytes(t, []byte(encodedRSSFixture("x-nonsense-1", "unknown label")))
	if err == nil {
		t.Fatalf("ParseFeed() succeeded with unknown encoding, entries = %d", len(entries))
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
	if !strings.Contains(err.Error(), "unsupported feed encoding") {
		t.Fatalf("ParseFeed() error = %v, want unsupported feed encoding", err)
	}
	if shouldRetryRSSParseError(err) {
		t.Fatalf("shouldRetryRSSParseError(%v) = true, want false", err)
	}
	if got := OperatorErrorMessage(err); got != "RSS feed declares an encoding the bot cannot decode" {
		t.Fatalf("OperatorErrorMessage() = %q, want encoding message", got)
	}
}

func TestParseFeedRejectsUTF32ByteOrderMark(t *testing.T) {
	tests := []struct {
		name string
		bom  []byte
	}{
		{name: "utf-32le", bom: []byte{0xFF, 0xFE, 0x00, 0x00}},
		{name: "utf-32be", bom: []byte{0x00, 0x00, 0xFE, 0xFF}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := append(append([]byte{}, tc.bom...), []byte(encodedRSSFixture("UTF-32", "utf-32 body"))...)

			entries, err := serveLocalFeedBytes(t, body)
			if err == nil {
				t.Fatalf("ParseFeed() succeeded with UTF-32 BOM, entries = %d", len(entries))
			}
			if len(entries) != 0 {
				t.Fatalf("entries = %d, want 0", len(entries))
			}
			if !strings.Contains(err.Error(), `unsupported feed encoding "utf-32"`) {
				t.Fatalf("ParseFeed() error = %v, want unsupported feed encoding \"utf-32\"", err)
			}
			if shouldRetryRSSParseError(err) {
				t.Fatalf("shouldRetryRSSParseError(%v) = true, want false", err)
			}
			if got := OperatorErrorMessage(err); got != "RSS feed declares an encoding the bot cannot decode" {
				t.Fatalf("OperatorErrorMessage() = %q, want encoding message", got)
			}
		})
	}
}

// TestParseFeedWithValidatorsKeepsStoredValidatorsMissingOn304 pins the parser
// contract: FeedFetchMetadata mirrors the response, including the absence of a
// validator on a 304 (RFC 7232 only recommends repeating them). Keeping the
// stored value is the tracker's job, not the parser's, so this test must stay
// green both before and after that guard is added.
func TestParseFeedWithValidatorsKeepsStoredValidatorsMissingOn304(t *testing.T) {
	storedETag := `"v1"`
	storedLastModified := "Wed, 21 Oct 2015 07:28:00 GMT"

	tests := []struct {
		name             string
		respond          func(w http.ResponseWriter)
		wantNotModified  bool
		wantETag         string
		wantLastModified string
	}{
		{
			name: "304 with etag only",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("ETag", `"v1"`)
				w.WriteHeader(http.StatusNotModified)
			},
			wantNotModified: true,
			wantETag:        `"v1"`,
		},
		{
			name: "304 without validators",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotModified)
			},
			wantNotModified: true,
		},
		{
			name: "200 replaces both validators",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/rss+xml")
				w.Header().Set("ETag", `"v2"`)
				w.Header().Set("Last-Modified", "Thu, 22 Oct 2015 07:28:00 GMT")
				_, _ = w.Write([]byte(rssFeedFixture(`<item>
      <title>Refreshed</title>
      <link>https://example.test/refreshed</link>
      <guid>refreshed-guid</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
    </item>`)))
			},
			wantETag:         `"v2"`,
			wantLastModified: "Thu, 22 Oct 2015 07:28:00 GMT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tt.respond(w)
			}))
			defer server.Close()

			_, metadata, err := parser.ParseFeedWithValidators(
				context.Background(),
				server.URL+"/feed.xml",
				FeedHTTPValidators{ETag: storedETag, LastModified: storedLastModified},
			)
			if err != nil {
				t.Fatalf("ParseFeedWithValidators() error = %v", err)
			}
			if metadata.NotModified != tt.wantNotModified {
				t.Fatalf("NotModified = %v, want %v", metadata.NotModified, tt.wantNotModified)
			}
			if metadata.Validators.ETag != tt.wantETag {
				t.Fatalf("metadata ETag = %q, want %q (mirror of the response)", metadata.Validators.ETag, tt.wantETag)
			}
			if metadata.Validators.LastModified != tt.wantLastModified {
				t.Fatalf("metadata Last-Modified = %q, want %q (mirror of the response)",
					metadata.Validators.LastModified, tt.wantLastModified)
			}
		})
	}
}

// TestParseFeedWarnsOnceForUnparseableTimestamp: an item whose timestamp cannot
// be parsed is delivered as undated instead of being dropped, so the operator
// needs one signal that the feed emits an unsupported format. One WARN per feed
// URL per parser instance - not per item and not per poll, which would flood the
// log for a 1000-item feed polled every few minutes.
func TestParseFeedWarnsOnceForUnparseableTimestamp(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	body := rssFeedFixture(`<item>
      <title>Broken One</title>
      <link>https://example.test/broken-1</link>
      <guid>broken-guid-1</guid>
      <pubDate>2026-01-15T10:00:00+bogus</pubDate>
    </item>
    <item>
      <title>Broken Two</title>
      <link>https://example.test/broken-2</link>
      <guid>broken-guid-2</guid>
      <pubDate>2026-01-15T10:00:00+bogus</pubDate>
    </item>
    <item>
      <title>Broken Three</title>
      <link>https://example.test/broken-3</link>
      <guid>broken-guid-3</guid>
      <pubDate>2026-01-15T10:00:00+bogus</pubDate>
    </item>`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(body))
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()

	firstURL := first.URL + "/feed.xml"
	for poll := 0; poll < 2; poll++ {
		entries, err := parser.ParseFeed(context.Background(), firstURL)
		if err != nil {
			t.Fatalf("ParseFeed(poll %d) error = %v", poll, err)
		}
		if len(entries) != 3 {
			t.Fatalf("entries on poll %d = %d, want 3 (undated items must not be dropped)", poll, len(entries))
		}
		for _, entry := range entries {
			if !entry.Published.IsZero() {
				t.Fatalf("entry %q Published = %s, want zero for an unparseable timestamp", entry.Title, entry.Published)
			}
		}
	}

	warnings := timestampWarningsForTest(hook)
	if len(warnings) != 1 {
		t.Fatalf("timestamp warnings after two polls of one feed = %d, want 1", len(warnings))
	}
	feedURLField, ok := warnings[0].Data["feed_url"].(string)
	if !ok {
		t.Fatalf("feed_url field missing or non-string: %#v", warnings[0].Data["feed_url"])
	}
	if feedURLField != firstURL {
		t.Fatalf("feed_url field = %q, want %q", feedURLField, firstURL)
	}
	rawValue, ok := warnings[0].Data["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp field missing or non-string: %#v", warnings[0].Data["timestamp"])
	}
	if rawValue != "2026-01-15T10:00:00+bogus" {
		t.Fatalf("timestamp field = %q, want the raw feed value", rawValue)
	}

	if _, err := parser.ParseFeed(context.Background(), second.URL+"/feed.xml"); err != nil {
		t.Fatalf("ParseFeed(second feed) error = %v", err)
	}
	if got := len(timestampWarningsForTest(hook)); got != 2 {
		t.Fatalf("timestamp warnings after a second broken feed = %d, want 2 (per feed, not global)", got)
	}

	// A feed that simply carries no date is undated too, but nothing failed to
	// parse: warning about an unparseable timestamp there would be wrong and
	// would send the operator hunting a parser bug that does not exist.
	dateless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedFixture(`<item>
      <title>No Date At All</title>
      <link>https://example.test/dateless</link>
      <guid>dateless-guid</guid>
    </item>`)))
	}))
	defer dateless.Close()

	datelessEntries, err := parser.ParseFeed(context.Background(), dateless.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("ParseFeed(dateless feed) error = %v", err)
	}
	if len(datelessEntries) != 1 {
		t.Fatalf("dateless entries = %d, want 1 (a dateless item must still be delivered)", len(datelessEntries))
	}
	if !datelessEntries[0].Published.IsZero() {
		t.Fatalf("dateless entry Published = %s, want zero", datelessEntries[0].Published)
	}
	if got := len(timestampWarningsForTest(hook)); got != 2 {
		t.Fatalf("timestamp warnings after a feed with no timestamps at all = %d, want 2 (nothing failed to parse)", got)
	}
}

func timestampWarningsForTest(hook *logtest.Hook) []*logrus.Entry {
	var warnings []*logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel && entry.Message == "RSS feed timestamp could not be parsed" {
			warnings = append(warnings, entry)
		}
	}
	return warnings
}

// TestParseFeedAcceptsZonelessISOAndLongWeekdayPubDate covers the formats
// real feeds emit most often outside the layouts the parser used to accept: a
// zone-less ISO 8601 timestamp (WordPress/Drupal with no timezone configured),
// a spelled-out weekday in the RFC 1123 form, and (added 2026-09-04, verbatim
// <item> blocks copied from the real captured bsi_csw.xml and f3.xml) BSI's
// Government Site Builder CMS unpadded day and CISA's all.xml two-digit year
// combined with seconds. All of these used to yield a zero Published, which
// made the item undated - and undated items were dropped whenever
// rss_max_item_age was set (measured against the real captures: 16/50 BSI
// items and 30/30 CISA items).
func TestParseFeedAcceptsZonelessISOAndLongWeekdayPubDate(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	entries, _ := parseLocalFeedFixture(t, parser, rssFeedFixture(`<item>
      <title>Zoneless ISO</title>
      <link>https://example.test/zoneless-iso</link>
      <guid>zoneless-iso-guid</guid>
      <pubDate>2026-01-15T10:00:00</pubDate>
    </item>
    <item>
      <title>Long Weekday</title>
      <link>https://example.test/long-weekday</link>
      <guid>long-weekday-guid</guid>
      <pubDate>Thursday, 15 Jan 2026 10:00:00 GMT</pubDate>
    </item>
    <item>
<guid isPermaLink="true">https://www.bsi.bund.de/SharedDocs/Cybersicherheitswarnungen/DE/2026/2026-287419-1032_bits.html</guid>
<title>Version 1.0: Deutsche Institutionen &#252;ber TerminalFix-Kampagne kompromittiert</title>
<link>https://www.bsi.bund.de/SharedDocs/Cybersicherheitswarnungen/DE/2026/2026-287419-1032_bits.html</link>
<pubDate>Fri, 4 Sep 2026 09:55:00 +0200</pubDate>
</item>
<item>
  <title>Rockwell Automation 1756-ENBT Module</title>
  <link>https://www.cisa.gov/news-events/ics-advisories/icsa-26-246-05</link>
  <pubDate>Thu, 03 Sep 26 12:00:00 +0000</pubDate>
  <guid isPermaLink="false">/node/25407</guid>
</item>`))

	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	want := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	wantBSI := time.Date(2026, 9, 4, 7, 55, 0, 0, time.UTC)
	wantCISA := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		switch entry.Title {
		case "Version 1.0: Deutsche Institutionen über TerminalFix-Kampagne kompromittiert":
			if !entry.Published.Equal(wantBSI) {
				t.Fatalf("BSI entry Published = %s, want %s", entry.Published, wantBSI)
			}
		case "Rockwell Automation 1756-ENBT Module":
			if !entry.Published.Equal(wantCISA) {
				t.Fatalf("CISA entry Published = %s, want %s", entry.Published, wantCISA)
			}
		default:
			if !entry.Published.Equal(want) {
				t.Fatalf("entry %q Published = %s, want %s", entry.Title, entry.Published, want)
			}
		}
	}
	if got := len(timestampWarningsForTest(hook)); got != 0 {
		t.Fatalf("timestamp warnings for parseable timestamps = %d, want 0", got)
	}
}
