package rss

import (
	"Ransomware-Bot/internal/status"
	"Ransomware-Bot/internal/textutil"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	log "github.com/sirupsen/logrus"
)

// Parser handles RSS feed parsing with retry logic and deduplication
type Parser struct {
	parser        *gofeed.Parser
	retryCount    int
	retryDelay    time.Duration
	workerTimeout time.Duration // Timeout for worker pool results
	statusTracker StatusTracker // Interface for persistent deduplication
}

// StatusTracker interface for deduplication
type StatusTracker interface {
	IsRSSItemParsed(feedURL, itemKey string) bool
	MarkRSSItemParsed(feedURL, itemKey string, entry status.StoredRSSEntry)
}

// Entry represents a processed RSS feed entry
type Entry struct {
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

// feedResult holds the result of parsing a single RSS feed
type feedResult struct {
	url     string
	entries []Entry
	err     error
}

// workerPool manages concurrent RSS feed processing
type workerPool struct {
	maxWorkers int
	jobs       chan string
	results    chan feedResult
	parser     *Parser
	wg         sync.WaitGroup
}

// NewParser creates a new RSS parser with retry configuration
func NewParser(retryCount int, retryDelay, workerTimeout time.Duration, statusTracker StatusTracker) *Parser {
	if retryCount < 0 {
		retryCount = 0
	}
	if workerTimeout <= 0 {
		workerTimeout = 30 * time.Second
	}
	return &Parser{
		parser:        gofeed.NewParser(),
		retryCount:    retryCount,
		retryDelay:    retryDelay,
		workerTimeout: workerTimeout,
		statusTracker: statusTracker,
	}
}

// ParseFeed parses an RSS feed and returns new entries
func (p *Parser) ParseFeed(ctx context.Context, feedURL string) ([]Entry, error) {
	var feed *gofeed.Feed
	var err error

	// Retry logic for feed parsing
	for attempt := 0; attempt <= p.retryCount; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		log.WithFields(log.Fields{
			"feed_url":     feedURL,
			"attempt":      attempt + 1,
			"max_attempts": p.retryCount + 1,
		}).Debug("Attempting to parse RSS feed")

		feed, err = p.parser.ParseURLWithContext(feedURL, ctx)
		if err == nil {
			break
		}

		// Log the error and wait before retrying (unless it's the last attempt)
		if attempt < p.retryCount {
			log.WithFields(log.Fields{
				"feed_url": feedURL,
				"attempt":  attempt + 1,
				"error":    err.Error(),
			}).Warn("RSS feed parsing failed, retrying...")

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(p.retryDelay):
				// Continue to next attempt
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse RSS feed after %d attempts: %w", p.retryCount+1, err)
	}

	// Convert feed items to our Entry format and filter for new items
	entries := p.processFeedItems(feed, feedURL)

	log.WithFields(log.Fields{
		"feed_url":    feedURL,
		"feed_title":  feed.Title,
		"total_items": len(feed.Items),
		"new_entries": len(entries),
	}).Info("Successfully parsed RSS feed")

	return entries, nil
}

// processFeedItems converts gofeed items to our Entry format and filters for new items
func (p *Parser) processFeedItems(feed *gofeed.Feed, feedURL string) []Entry {
	var newEntries []Entry

	for _, item := range feed.Items {
		// Skip nil items (safety check)
		if item == nil {
			continue
		}

		// Generate unique key for this item
		key := GenerateEntryKey(feedURL, safeString(item.GUID), safeString(item.Link), safeString(item.Title))

		// Skip if we've already parsed this item (using persistent storage)
		if p.statusTracker != nil && p.statusTracker.IsRSSItemParsed(feedURL, key) {
			continue
		}

		// Convert to our Entry format with safe nil checks
		entry := Entry{
			Title:       safeString(item.Title),
			Link:        safeString(item.Link),
			Description: textutil.StripHTML(safeString(item.Description)),
			Author:      getAuthorName(item),
			Categories:  safeCategories(item.Categories),
			GUID:        safeString(item.GUID),
			FeedTitle:   safeString(feed.Title),
			FeedURL:     feedURL,
		}

		// Parse publication date safely
		entry.Published = getPublishedDate(item)

		// Mark as processed in persistent storage
		if p.statusTracker != nil {
			storedEntry := convertToStoredRSSEntry(entry, key)
			p.statusTracker.MarkRSSItemParsed(feedURL, key, storedEntry)
		}

		newEntries = append(newEntries, entry)
	}

	return newEntries
}

// GenerateContentSignature creates a content-based signature for an RSS item.
// This is used as a secondary dedup check to catch link variants
// (e.g. ".../article" vs ".../article-0") that have the same content.
func GenerateContentSignature(feedURL, title string, published time.Time) string {
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	dateStr := ""
	if !published.IsZero() {
		dateStr = published.Format("2006-01-02")
	}
	return "content-sig:" + feedURL + ":" + normalizedTitle + ":" + dateStr
}

// GenerateEntryKey creates a unique key for an RSS item.
// This is the single source of truth for RSS item key generation,
// used by both the parser and the scheduler to ensure consistent deduplication.
func GenerateEntryKey(feedURL, guid, link, title string) string {
	// Prefer GUID if available (most stable)
	if guid != "" {
		return feedURL + ":" + strings.TrimSpace(guid)
	}

	// Fallback to link if available (very stable)
	if link != "" {
		return feedURL + ":" + strings.TrimRight(strings.TrimSpace(link), "/")
	}

	// Last resort: use normalized title (lowercase, trimmed)
	if title != "" {
		normalizedTitle := strings.ToLower(strings.TrimSpace(title))
		return feedURL + ":" + normalizedTitle
	}

	// Ultimate fallback
	return feedURL + ":unknown-item"
}

// getAuthorName safely extracts author name from RSS item
func getAuthorName(item *gofeed.Item) string {
	if item == nil {
		return ""
	}

	// Try primary author field
	if item.Author != nil && item.Author.Name != "" {
		return item.Author.Name
	}

	// Try authors array
	if len(item.Authors) > 0 && item.Authors[0] != nil && item.Authors[0].Name != "" {
		return item.Authors[0].Name
	}

	// Return empty string if no author found
	return ""
}

// getPublishedDate safely extracts publication date from RSS item
func getPublishedDate(item *gofeed.Item) time.Time {
	if item == nil {
		log.Warn("Nil RSS item, using zero time for published date")
		return time.Time{} // Zero time instead of time.Now()
	}

	// Prefer parsed published date
	if item.PublishedParsed != nil {
		return *item.PublishedParsed
	}

	// Fallback to updated date
	if item.UpdatedParsed != nil {
		return *item.UpdatedParsed
	}

	// Fallback to zero time if no date is available
	// This prevents false chronological ordering
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

// safeCategories safely extracts categories from RSS item
func safeCategories(categories []string) []string {
	if categories == nil {
		return []string{}
	}
	return categories
}

// convertToStoredRSSEntry converts rss.Entry to status.StoredRSSEntry for persistence
func convertToStoredRSSEntry(entry Entry, key string) status.StoredRSSEntry {
	published := ""
	if !entry.Published.IsZero() {
		published = entry.Published.Format("2006-01-02 15:04:05.999999")
	}

	return status.StoredRSSEntry{
		Key:         key,
		FeedURL:     entry.FeedURL,
		Title:       entry.Title,
		Link:        entry.Link,
		Description: entry.Description,
		Published:   published,
		Author:      entry.Author,
		Categories:  entry.Categories,
		GUID:        entry.GUID,
		FeedTitle:   entry.FeedTitle,
		ParsedAt:    time.Now().Format("2006-01-02 15:04:05.999999"),
	}
}

// newWorkerPool creates a new worker pool for RSS feed processing
func newWorkerPool(maxWorkers int, parser *Parser) *workerPool {
	return &workerPool{
		maxWorkers: maxWorkers,
		jobs:       make(chan string, maxWorkers*2), // Buffer for smooth operation
		results:    make(chan feedResult, maxWorkers*2),
		parser:     parser,
	}
}

// worker processes RSS feeds from the jobs channel
func (wp *workerPool) worker(ctx context.Context) {
	defer wp.wg.Done() // Signal completion when worker exits

	for {
		select {
		case url, ok := <-wp.jobs:
			if !ok {
				return // Jobs channel closed, worker should exit
			}

			// Process the RSS feed
			entries, err := wp.parser.ParseFeed(ctx, url)

			// Send result back (channel is buffered; block until space or context cancel)
			select {
			case wp.results <- feedResult{url: url, entries: entries, err: err}:
			case <-ctx.Done():
				return
			}

		case <-ctx.Done():
			return // Context cancelled, worker should exit
		}
	}
}

// FeedResults holds the results and per-feed errors from parsing multiple feeds.
type FeedResults struct {
	Entries    map[string][]Entry  // feedURL → parsed entries
	FeedErrors map[string]string   // feedURL → error message (only for failed feeds)
}

// processFeeds starts workers and processes all feeds with controlled concurrency
func (wp *workerPool) processFeeds(ctx context.Context, feedURLs []string) (*FeedResults, error) {
	// Create a child context that we can cancel to stop all workers
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Always cancel workers when returning

	// Start workers with WaitGroup tracking
	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(workerCtx)
	}

	// Goroutine to close results channel after all workers finish
	go func() {
		wp.wg.Wait()
		close(wp.results)
	}()

	// Send all jobs
	go func() {
		defer close(wp.jobs)
		for _, url := range feedURLs {
			select {
			case wp.jobs <- url:
			case <-workerCtx.Done():
				return
			}
		}
	}()

	// Collect results
	fr := &FeedResults{
		Entries:    make(map[string][]Entry),
		FeedErrors: make(map[string]string),
	}

	for i := 0; i < len(feedURLs); i++ {
		select {
		case result, ok := <-wp.results:
			if !ok {
				// Channel closed - all workers finished
				log.WithField("collected", i).Warn("Results channel closed before collecting all results")
				return fr, fmt.Errorf("collected %d of %d feeds before workers finished", i, len(feedURLs))
			}
			if result.err != nil {
				fr.FeedErrors[result.url] = result.err.Error()
				fr.Entries[result.url] = []Entry{}
			} else {
				fr.Entries[result.url] = result.entries
			}
		case <-ctx.Done():
			// Parent context cancelled - cancel workers and return
			return nil, ctx.Err()
		case <-time.After(wp.parser.workerTimeout):
			log.WithFields(log.Fields{
				"remaining_feeds": len(feedURLs) - i,
				"timeout":         wp.parser.workerTimeout,
			}).Error("Worker pool timeout waiting for feed results")
			// Cancel workers before returning to prevent goroutine leak
			cancel()

			// Wait for workers to finish with a timeout
			waitDone := make(chan struct{})
			go func() {
				wp.wg.Wait()
				close(waitDone)
			}()

			select {
			case <-waitDone:
				log.Info("All workers shut down cleanly after timeout")
			case <-time.After(5 * time.Second):
				log.Warn("Some workers did not shut down within 5 seconds after timeout")
			}

			// Return partial results instead of failing completely
			return fr, fmt.Errorf("worker pool timeout after processing %d of %d feeds", i, len(feedURLs))
		}
	}

	// Log any per-feed errors
	if len(fr.FeedErrors) > 0 {
		var errorMsg strings.Builder
		fmt.Fprintf(&errorMsg, "Some RSS feeds failed to parse (%d errors):\n", len(fr.FeedErrors))
		i := 1
		for url, errMsg := range fr.FeedErrors {
			fmt.Fprintf(&errorMsg, "  %d. %s: %s\n", i, url, errMsg)
			i++
		}
		log.Warn(errorMsg.String())
	}

	return fr, nil
}

// ParseMultipleFeeds parses multiple RSS feeds concurrently with worker pool limiting.
// Returns FeedResults containing both per-feed entries and per-feed errors.
func (p *Parser) ParseMultipleFeeds(ctx context.Context, feedURLs []string, maxWorkers int) (*FeedResults, error) {
	if len(feedURLs) == 0 {
		return &FeedResults{
			Entries:    make(map[string][]Entry),
			FeedErrors: make(map[string]string),
		}, nil
	}

	// Limit workers to available feeds if fewer feeds than workers
	if maxWorkers > len(feedURLs) {
		maxWorkers = len(feedURLs)
	}

	// Create and use worker pool for controlled concurrency
	pool := newWorkerPool(maxWorkers, p)
	return pool.processFeeds(ctx, feedURLs)
}


