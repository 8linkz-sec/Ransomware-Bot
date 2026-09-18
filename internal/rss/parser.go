package rss

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/httpstatus"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

const (
	maxRSSResponseBytes = 10 * 1024 * 1024
	maxRSSItemsPerFeed  = 1000
	rssAcceptHeader     = "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5"
	rssUserAgent        = "Ransomware-Bot/1.1 (+https://github.com/8linkz-sec/Ransomware-Bot)"
)

// Parser handles RSS feed parsing with retry logic.
type Parser struct {
	httpClient    *http.Client
	feedURLOpts   feedurl.Options
	retryCount    int
	retryDelay    time.Duration
	workerTimeout time.Duration // Timeout for worker pool results

	// timestampWarnedFeeds remembers which feed URLs already produced the
	// "timestamp could not be parsed" WARN. It is a sync.Map because the worker
	// pool parses feeds concurrently through the same *Parser. The state is per
	// feed URL and per parser instance, so a broken feed is reported once per
	// process and survives an ordinary hot reload (the scheduler rebuilds the
	// parser only when the retry/timeout settings change). Growth is bounded by
	// the number of distinct feed URLs polled during the process lifetime.
	timestampWarnedFeeds sync.Map
}

// Entry is kept here as a compatibility alias; the stable feed model is owned
// by internal/model, not the concrete RSS parser adapter.
type Entry = model.RSSEntry

// FeedHTTPValidators are HTTP cache validators for one RSS feed.
type FeedHTTPValidators struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// FeedFetchMetadata describes cache-related metadata observed while fetching a feed.
type FeedFetchMetadata struct {
	Validators  FeedHTTPValidators
	NotModified bool
}

// NewParser creates a new RSS parser with retry configuration.
//
// Negative retry counts are normalized to zero. Non-positive worker timeouts
// are normalized to 30 seconds.
func NewParser(retryCount int, retryDelay, workerTimeout time.Duration) *Parser {
	if retryCount < 0 {
		retryCount = 0
	}
	if workerTimeout <= 0 {
		workerTimeout = 30 * time.Second
	}
	return &Parser{
		httpClient:    newFeedHTTPClient(feedurl.Options{}),
		feedURLOpts:   feedurl.Options{},
		retryCount:    retryCount,
		retryDelay:    retryDelay,
		workerTimeout: workerTimeout,
	}
}

// ParseFeed parses one RSS feed. It retries retryable fetch/parse failures,
// enforces response and item limits, and normalizes entry fields. Persistent
// deduplication and parsed-state updates are owned by the caller.
func (p *Parser) ParseFeed(ctx context.Context, feedURL string) ([]Entry, error) {
	entries, _, err := p.ParseFeedWithValidators(ctx, feedURL, FeedHTTPValidators{})
	return entries, err
}

// ParseFeedWithValidators parses one RSS feed using optional HTTP cache validators.
// A 304 Not Modified response is treated as a successful empty result.
func (p *Parser) ParseFeedWithValidators(
	ctx context.Context,
	feedURL string,
	validators FeedHTTPValidators,
) ([]Entry, FeedFetchMetadata, error) {
	var feed *parsedFeed
	var metadata FeedFetchMetadata
	var err error
	attempts := 0

	// Retry logic for feed parsing. The "error" field logged below redacts
	// err.Error() with RedactWebhookSecretsForURL, anchored on feedURL, rather
	// than RedactURLCredentials's regex/url.Parse heuristic: err can be a
	// *url.Error wrapping this exact feed URL, and an anchor that only has to
	// recognize known-good bytes is structurally safer than one that has to
	// reparse and interpret whatever a freeform error string happens to contain.
	for attempt := 0; attempt <= p.retryCount; attempt++ {
		select {
		case <-ctx.Done():
			return nil, metadata, ctx.Err()
		default:
		}

		log.WithFields(log.Fields{
			"feed_url":     textutil.RedactURLCredentials(feedURL),
			"attempt":      attempt + 1,
			"max_attempts": p.retryCount + 1,
		}).Debug("Attempting to parse RSS feed")

		feed, metadata, err = p.fetchAndParseFeedWithValidators(ctx, feedURL, validators)
		attempts = attempt + 1
		if err == nil {
			break
		}

		if !shouldRetryRSSParseError(err) {
			log.WithFields(log.Fields{
				"feed_url": textutil.RedactURLCredentials(feedURL),
				"attempt":  attempts,
				"error":    textutil.TruncateText(textutil.RedactWebhookSecretsForURL(err.Error(), feedURL), 240),
			}).Error("RSS feed parsing failed with terminal error, not retrying")
			break
		}

		// Log the error and wait before retrying (unless it's the last attempt)
		if attempt < p.retryCount {
			log.WithFields(log.Fields{
				"feed_url": textutil.RedactURLCredentials(feedURL),
				"attempt":  attempt + 1,
				"error":    textutil.TruncateText(textutil.RedactWebhookSecretsForURL(err.Error(), feedURL), 240),
			}).Error("RSS feed parsing failed, retrying...")

			select {
			case <-ctx.Done():
				return nil, metadata, ctx.Err()
			case <-time.After(p.retryDelay):
				// Continue to next attempt
			}
		}
	}

	if err != nil {
		return nil, metadata, fmt.Errorf("failed to parse RSS feed after %d attempts: %w", attempts, err)
	}

	if metadata.NotModified {
		log.WithField("feed_url", textutil.RedactURLCredentials(feedURL)).Debug("RSS feed not modified")
		return []Entry{}, metadata, nil
	}

	// Convert feed items to our Entry format and filter for new items
	entries := p.processFeedItems(feed, feedURL)

	log.WithFields(log.Fields{
		"feed_url":    textutil.RedactURLCredentials(feedURL),
		"feed_title":  feed.Title,
		"total_items": len(feed.Items),
		"new_entries": len(entries),
	}).Info("Successfully parsed RSS feed")

	return entries, metadata, nil
}

func shouldRetryRSSParseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var httpErr *feedHTTPError
	if errors.As(err, &httpErr) {
		return httpstatus.IsRetryable(httpErr.StatusCode)
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr)
}

func OperatorErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "RSS feed parsing was cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "RSS feed parsing timed out"
	}

	var httpErr *feedHTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("RSS feed returned HTTP %d", httpErr.StatusCode)
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "RSS feed network request timed out"
		}
		var dnsErr *net.DNSError
		if errors.As(urlErr.Err, &dnsErr) {
			return "RSS feed DNS lookup failed"
		}
		return "RSS feed network request failed"
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unsupported feed encoding"):
		return "RSS feed declares an encoding the bot cannot decode"
	case strings.Contains(lower, "non-xml content type"):
		return "RSS feed returned non-XML content instead of RSS or Atom XML"
	case strings.Contains(lower, "failed to detect feed type"),
		strings.Contains(lower, "expected element type"),
		strings.Contains(lower, "xml syntax error"):
		return "RSS feed is not valid RSS or Atom XML"
	case strings.Contains(lower, "response too large"):
		return "RSS feed response is too large"
	case strings.Contains(lower, "worker pool timeout"):
		return "RSS feed worker pool timed out"
	case strings.Contains(lower, "results channel closed"):
		return "RSS feed worker stopped before completing"
	}

	msg = textutil.RedactURLCredentials(strings.Join(strings.Fields(msg), " "))
	return textutil.TruncateText(msg, 240)
}

func OperatorErrorMessageFromString(errMsg string) string {
	if strings.TrimSpace(errMsg) == "" {
		return ""
	}
	return OperatorErrorMessage(errors.New(errMsg))
}

func (p *Parser) fetchAndParseFeed(ctx context.Context, feedURL string) (*parsedFeed, error) {
	feed, _, err := p.fetchAndParseFeedWithValidators(ctx, feedURL, FeedHTTPValidators{})
	return feed, err
}

func (p *Parser) fetchAndParseFeedWithValidators(
	ctx context.Context,
	feedURL string,
	validators FeedHTTPValidators,
) (*parsedFeed, FeedFetchMetadata, error) {
	var metadata FeedFetchMetadata
	if err := feedurl.Validate(feedURL, p.feedURLOpts); err != nil {
		return nil, metadata, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, metadata, err
	}
	if validators.ETag != "" {
		req.Header.Set("If-None-Match", validators.ETag)
	}
	if validators.LastModified != "" {
		req.Header.Set("If-Modified-Since", validators.LastModified)
	}
	req.Header.Set("Accept", rssAcceptHeader)
	req.Header.Set("User-Agent", rssUserAgent)

	client := p.httpClient
	if client == nil {
		client = newFeedHTTPClient(p.feedURLOpts)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, metadata, err
	}
	defer resp.Body.Close()

	metadata.Validators = FeedHTTPValidators{
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
	}

	if resp.StatusCode == http.StatusNotModified {
		metadata.NotModified = true
		return nil, metadata, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, metadata, &feedHTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
		}
	}

	if err := rejectNonFeedContentType(resp.Header.Get("Content-Type")); err != nil {
		return nil, metadata, err
	}

	if resp.ContentLength > maxRSSResponseBytes {
		return nil, metadata, fmt.Errorf(
			"RSS feed response too large: %d bytes exceeds %d bytes",
			resp.ContentLength,
			maxRSSResponseBytes,
		)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRSSResponseBytes+1))
	if err != nil {
		return nil, metadata, err
	}
	if len(data) > maxRSSResponseBytes {
		return nil, metadata, fmt.Errorf("RSS feed response too large: exceeds %d bytes", maxRSSResponseBytes)
	}

	feed, err := parseFeedXML(data)
	return feed, metadata, err
}

func rejectNonFeedContentType(contentType string) error {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	}

	if strings.EqualFold(mediaType, "text/html") {
		return fmt.Errorf("RSS feed returned non-XML content type %q", contentType)
	}
	return nil
}

type feedHTTPError struct {
	StatusCode int
	Status     string
}

func (e *feedHTTPError) Error() string {
	if e == nil {
		return ""
	}
	if e.Status != "" {
		return e.Status
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

type parsedFeed struct {
	Title string
	Items []parsedFeedItem
}

type parsedFeedItem struct {
	Title       string
	Link        string
	Description string
	Author      string
	Categories  []string
	GUID        string
	Published   time.Time
	Updated     time.Time
	// RawDate carries the first non-empty feed timestamp that failed to parse,
	// so processFeedItems can report an undated item. In-memory only.
	RawDate string
}

type rssEnvelope struct {
	XMLName   xml.Name
	Channel   rssChannel `xml:"channel"`
	RootItems []rssItem  `xml:"item"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	Author      string   `xml:"author"`
	Categories  []string `xml:"category"`
	GUID        string   `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	DCDate      string   `xml:"date"`
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title      string         `xml:"title"`
	ID         string         `xml:"id"`
	Links      []atomLink     `xml:"link"`
	Summary    string         `xml:"summary"`
	Content    string         `xml:"content"`
	Author     atomAuthor     `xml:"author"`
	Categories []atomCategory `xml:"category"`
	Published  string         `xml:"published"`
	Updated    string         `xml:"updated"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
	Value string `xml:",chardata"`
}

// decodeFeedXML unmarshals feed XML into v. Unlike xml.Unmarshal it accepts a
// declared non-UTF-8 encoding and HTML named entities. Strict stays true, so
// undeclared entities (&xxe;, billion-laughs) and syntax errors still fail.
func decodeFeedXML(data []byte, v any) error {
	reader, transcoded, err := normalizeFeedReader(data)
	if err != nil {
		return err
	}
	decoder := xml.NewDecoder(reader)
	decoder.Entity = xml.HTMLEntity
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if transcoded {
			// The BOM already decided the encoding; the stream is UTF-8 now.
			return input, nil
		}
		return feedCharsetReader(label, input)
	}
	return decoder.Decode(v)
}

// normalizeFeedReader converts a byte-order-marked body to UTF-8, as an
// io.Reader instead of a fully materialized []byte. XML 1.0 4.3.3 makes the
// BOM authoritative over the declaration, and Go's tokenizer rejects UTF-16
// bytes as invalid UTF-8 before it ever consults CharsetReader. The UTF-16
// cases are streamed through transform.NewReader instead of buffered via
// Decoder.Bytes(), so peak memory no longer scales with the decoded body
// size -- xml.Decoder pulls small chunks as it tokenizes.
func normalizeFeedReader(data []byte) (io.Reader, bool, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return bytes.NewReader(data[3:]), true, nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE, 0x00, 0x00}),
		bytes.HasPrefix(data, []byte{0x00, 0x00, 0xFE, 0xFF}):
		// A UTF-32 BOM shares its first two bytes with UTF-16LE, but a genuine
		// UTF-16LE document cannot start with U+0000, so this cannot misfire.
		return nil, false, fmt.Errorf("unsupported feed encoding %q", "utf-32")
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return streamBOMUnicode(data, unicode.LittleEndian), true, nil
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return streamBOMUnicode(data, unicode.BigEndian), true, nil
	default:
		return bytes.NewReader(data), false, nil
	}
}

// streamBOMUnicode lazily decodes a UTF-16 body via the encoding/unicode
// decoder wrapped in transform.NewReader. The decoder never returns an error
// for input reaching this point (it only errors on a missing BOM, and the
// caller only reaches here after one has already matched); a malformed body
// (odd byte count, unpaired surrogate) substitutes U+FFFD, exactly as the
// former buffered decodeBOMUnicode did.
func streamBOMUnicode(data []byte, endianness unicode.Endianness) io.Reader {
	return transform.NewReader(bytes.NewReader(data), unicode.UTF16(endianness, unicode.ExpectBOM).NewDecoder())
}

// feedCharsetReader resolves a declared encoding label via the WHATWG index
// (iso-8859-1 -> windows-1252, utf-16 -> utf-16le), as the old gofeed path did.
func feedCharsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8":
		return input, nil
	}
	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, fmt.Errorf("unsupported feed encoding %q", label)
	}
	return enc.NewDecoder().Reader(input), nil
}

func parseFeedXML(data []byte) (*parsedFeed, error) {
	var probe struct {
		XMLName xml.Name
	}
	if err := decodeFeedXML(data, &probe); err != nil {
		return nil, err
	}

	switch strings.ToLower(probe.XMLName.Local) {
	case "rss", "rdf":
		return parseRSSXML(data)
	case "feed":
		return parseAtomXML(data)
	default:
		return nil, fmt.Errorf("failed to detect feed type")
	}
}

func parseRSSXML(data []byte) (*parsedFeed, error) {
	var envelope rssEnvelope
	if err := decodeFeedXML(data, &envelope); err != nil {
		return nil, err
	}

	items := envelope.Channel.Items
	if len(items) == 0 {
		items = envelope.RootItems
	}
	if strings.TrimSpace(envelope.Channel.Title) == "" && len(items) == 0 {
		return nil, fmt.Errorf("failed to detect feed type")
	}

	feed := &parsedFeed{
		Title: strings.TrimSpace(envelope.Channel.Title),
		Items: make([]parsedFeedItem, 0, len(items)),
	}
	for _, item := range items {
		feed.Items = append(feed.Items, parsedFeedItem{
			Title:       item.Title,
			Link:        item.Link,
			Description: item.Description,
			Author:      item.Author,
			Categories:  item.Categories,
			GUID:        item.GUID,
			Published:   parseFeedTimestamp(item.PubDate),
			Updated:     parseFeedTimestamp(item.DCDate),
			RawDate:     unparsedFeedTimestamp(item.PubDate, item.DCDate),
		})
	}
	return feed, nil
}

func parseAtomXML(data []byte) (*parsedFeed, error) {
	var atom atomFeed
	if err := decodeFeedXML(data, &atom); err != nil {
		return nil, err
	}
	if strings.TrimSpace(atom.Title) == "" && len(atom.Entries) == 0 {
		return nil, fmt.Errorf("failed to detect feed type")
	}

	feed := &parsedFeed{
		Title: strings.TrimSpace(atom.Title),
		Items: make([]parsedFeedItem, 0, len(atom.Entries)),
	}
	for _, entry := range atom.Entries {
		feed.Items = append(feed.Items, parsedFeedItem{
			Title:       entry.Title,
			Link:        atomEntryLink(entry.Links),
			Description: firstNonEmpty(entry.Summary, entry.Content),
			Author:      entry.Author.Name,
			Categories:  atomCategories(entry.Categories),
			GUID:        entry.ID,
			Published:   parseFeedTimestamp(entry.Published),
			Updated:     parseFeedTimestamp(entry.Updated),
			RawDate:     unparsedFeedTimestamp(entry.Published, entry.Updated),
		})
	}
	return feed, nil
}

func atomEntryLink(links []atomLink) string {
	for _, link := range links {
		if strings.EqualFold(strings.TrimSpace(link.Rel), "alternate") && strings.TrimSpace(link.Href) != "" {
			return link.Href
		}
	}
	for _, link := range links {
		if strings.TrimSpace(link.Href) != "" {
			return link.Href
		}
	}
	return ""
}

func atomCategories(categories []atomCategory) []string {
	values := make([]string, 0, len(categories))
	for _, category := range categories {
		values = append(values, firstNonEmpty(category.Term, category.Label, category.Value))
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseFeedTimestamp(value string) time.Time {
	parsed, err := timeutil.ParseFlexibleTimestamp(value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// unparsedFeedTimestamp returns the first non-empty timestamp that failed to
// parse, so the caller can report an undated item instead of dropping it
// silently. The order matters: when one of the two values does parse, the item
// ends up dated and no warning is due.
func unparsedFeedTimestamp(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, err := timeutil.ParseFlexibleTimestamp(value); err != nil {
			return value
		}
	}
	return ""
}

func newFeedHTTPClient(opts feedurl.Options) *http.Client {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// DefaultTransport was replaced by a custom RoundTripper; start from the stdlib defaults.
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	// Feed fetches never go through an HTTP proxy: a proxy resolves and connects
	// on our behalf, so feedurl.GuardedDialer would only ever validate the proxy
	// address. Without a proxy the dialer sees the real feed host.
	transport.Proxy = nil
	transport.DialContext = feedurl.GuardedDialer{Options: opts}.DialContext
	transport.MaxIdleConns = 20
	transport.MaxIdleConnsPerHost = 5
	transport.MaxConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.ExpectContinueTimeout = 1 * time.Second

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 RSS feed redirects")
			}
			return feedurl.ValidateForFetch(req.Context(), req.URL.String(), nil, opts)
		},
	}
}

// processFeedItems converts parsed feed items to our Entry format.
func (p *Parser) processFeedItems(feed *parsedFeed, feedURL string) []Entry {
	var newEntries []Entry

	items := feed.Items
	if len(items) > maxRSSItemsPerFeed {
		log.WithFields(log.Fields{
			"feed_url":    textutil.RedactURLCredentials(feedURL),
			"total_items": len(items),
			"kept_items":  maxRSSItemsPerFeed,
		}).Warn("RSS feed item count exceeds processing cap")
		items = items[:maxRSSItemsPerFeed]
	}

	for _, item := range items {
		guid := safeString(item.GUID)
		link := safeString(item.Link)
		title := safeFeedText(item.Title)

		published := getPublishedDate(item)
		if published.IsZero() && item.RawDate != "" {
			// Once per feed URL per parser instance: the item is still delivered
			// as undated, but the operator needs one signal that this feed emits
			// a timestamp format the parser does not accept.
			if _, seen := p.timestampWarnedFeeds.LoadOrStore(feedURL, struct{}{}); !seen {
				log.WithFields(log.Fields{
					"feed_url":  textutil.RedactURLCredentials(feedURL),
					"timestamp": textutil.RedactURLCredentials(item.RawDate),
				}).Warn("RSS feed timestamp could not be parsed")
			}
		}

		// Generate a stable deduplication key for this item.
		key := GenerateEntryKey(feedURL, guid, link, title, published)
		if key == "" {
			log.WithField("feed_url", textutil.RedactURLCredentials(feedURL)).Warn("Skipping RSS item without stable identity")
			continue
		}

		// Convert to our Entry format with safe nil checks
		entry := Entry{
			Title:       title,
			Link:        link,
			Description: safeFeedText(item.Description),
			Author:      getAuthorName(item),
			Categories:  safeCategories(item.Categories),
			GUID:        guid,
			FeedTitle:   safeFeedText(feed.Title),
			FeedURL:     feedURL,
		}

		entry.Published = published

		newEntries = append(newEntries, entry)
	}

	return newEntries
}

// GenerateContentSignature creates a content-based signature for an RSS item.
// This is used as a secondary dedup check to catch link variants
// (e.g. ".../article" vs ".../article-0") that have the same content.
func GenerateContentSignature(feedURL, title string, published time.Time) string {
	return model.GenerateRSSContentSignature(feedURL, title, published)
}

// GenerateEntryContentSignature creates a precise content-based signature for
// an RSS entry. Link variants with the same dated content still dedupe, while
// same-title articles with different timestamps, descriptions, or undated
// entry identities no longer collapse.
func GenerateEntryContentSignature(entry Entry) string {
	return model.GenerateRSSEntryContentSignature(entry)
}

// GenerateContentSignatureLookupKeys returns the current opaque content
// signature plus the legacy title-bearing signature for status-file
// compatibility.
func GenerateContentSignatureLookupKeys(feedURL, title string, published time.Time) []string {
	return model.GenerateRSSContentSignatureLookupKeys(feedURL, title, published)
}

// GenerateEntryContentSignatureLookupKeys returns the current entry-content
// signature plus older no-description and legacy signatures.
func GenerateEntryContentSignatureLookupKeys(entry Entry) []string {
	return model.GenerateRSSEntryContentSignatureLookupKeys(entry)
}

// GenerateEntryKey creates a stable best-effort deduplication key for an RSS item.
// This is the single source of truth for RSS item key generation,
// used by both the parser and the scheduler to ensure consistent deduplication.
func GenerateEntryKey(feedURL, guid, link, title string, published ...time.Time) string {
	return model.GenerateRSSEntryKey(feedURL, guid, link, title, published...)
}

func GenerateEntryKeyForEntry(entry Entry) string {
	return model.GenerateRSSEntryKeyForEntry(entry)
}

// GenerateEntryLookupKeys returns the current opaque key plus legacy natural
// keys so existing status files continue to deduplicate after upgrades.
func GenerateEntryLookupKeys(feedURL, guid, link, title string, published ...time.Time) []string {
	return model.GenerateRSSEntryLookupKeys(feedURL, guid, link, title, published...)
}

func GenerateEntryLookupKeysForEntry(entry Entry) []string {
	return model.GenerateRSSEntryLookupKeysForEntry(entry)
}

// getAuthorName safely extracts author name from RSS item.
func getAuthorName(item parsedFeedItem) string {
	return safeFeedText(item.Author)
}

// getPublishedDate safely extracts publication date from RSS item.
func getPublishedDate(item parsedFeedItem) time.Time {
	if !item.Published.IsZero() {
		return item.Published
	}

	if !item.Updated.IsZero() {
		return item.Updated
	}

	log.WithFields(log.Fields{
		"title": item.Title,
		"link":  item.Link,
	}).Debug("RSS item has no published or updated date, using zero time")
	return time.Time{}
}

// safeString trims whitespace from RSS field values
func safeString(s string) string {
	return strings.TrimSpace(s)
}

func safeFeedText(s string) string {
	return textutil.StripHTML(safeString(s))
}

// safeCategories safely extracts categories from RSS item
func safeCategories(categories []string) []string {
	if categories == nil {
		return []string{}
	}
	normalized := make([]string, 0, len(categories))
	for _, category := range categories {
		category = safeFeedText(category)
		if category == "" {
			continue
		}
		normalized = append(normalized, category)
	}
	return normalized
}
