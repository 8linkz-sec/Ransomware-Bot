package rss

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

// feedResult holds the result of parsing a single RSS feed.
type feedResult struct {
	url      string
	entries  []Entry
	metadata FeedFetchMetadata
	err      error
}

// workerPool manages concurrent RSS feed processing.
type workerPool struct {
	maxWorkers int
	jobs       chan string
	results    chan feedResult
	parser     *Parser
	validators map[string]FeedHTTPValidators
	wg         sync.WaitGroup
}

// newWorkerPool creates a new worker pool for RSS feed processing.
func newWorkerPool(maxWorkers int, parser *Parser) *workerPool {
	return newWorkerPoolWithValidators(maxWorkers, parser, nil)
}

func newWorkerPoolWithValidators(
	maxWorkers int,
	parser *Parser,
	validators map[string]FeedHTTPValidators,
) *workerPool {
	return &workerPool{
		maxWorkers: maxWorkers,
		jobs:       make(chan string, maxWorkers*2),
		results:    make(chan feedResult, maxWorkers*2),
		parser:     parser,
		validators: validators,
	}
}

// worker processes RSS feeds from the jobs channel.
func (wp *workerPool) worker(ctx context.Context) {
	defer wp.wg.Done()
	defer recoverRSSGoroutinePanic("rss worker")

	for {
		select {
		case url, ok := <-wp.jobs:
			if !ok {
				return
			}

			result := wp.parseFeedJob(ctx, url)
			select {
			case wp.results <- result:
			case <-ctx.Done():
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

func (wp *workerPool) parseFeedJob(ctx context.Context, feedURL string) (result feedResult) {
	result.url = feedURL
	defer func() {
		if recovered := recover(); recovered != nil {
			result.entries = nil
			result.err = fmt.Errorf("panic while parsing RSS feed: %v", recovered)
			log.WithFields(log.Fields{
				"feed_url": textutil.RedactURLCredentials(feedURL),
				"panic":    fmt.Sprint(recovered),
				"stack":    string(debug.Stack()),
			}).Error("Recovered panic in RSS feed worker")
		}
	}()

	entries, metadata, err := wp.parser.ParseFeedWithValidators(ctx, feedURL, wp.validators[feedURL])
	result.entries = entries
	result.metadata = metadata
	result.err = err
	return result
}

func recoverRSSGoroutinePanic(component string) {
	if recovered := recover(); recovered != nil {
		log.WithFields(log.Fields{
			"component": component,
			"panic":     fmt.Sprint(recovered),
			"stack":     string(debug.Stack()),
		}).Error("Recovered panic in RSS goroutine")
	}
}

// FeedResults holds per-feed entries and per-feed errors from a multi-feed parse.
// A feed URL can appear with an empty entry slice and a FeedErrors value when a
// worker fails or times out; callers should use both maps for status reporting.
type FeedResults struct {
	Entries         map[string][]Entry            // feedURL -> parsed entries
	FeedErrors      map[string]string             // feedURL -> error message (only for failed feeds)
	FeedValidators  map[string]FeedHTTPValidators // feedURL -> response cache validators
	FeedNotModified map[string]bool               // feedURL -> true when upstream returned 304
}

// processFeeds starts workers and processes all feeds with controlled concurrency.
func (wp *workerPool) processFeeds(ctx context.Context, feedURLs []string) (*FeedResults, error) {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	wp.startWorkers(workerCtx)
	wp.submitFeedJobs(workerCtx, feedURLs)
	return wp.collectFeedResults(ctx, cancel, feedURLs)
}

func (wp *workerPool) startWorkers(workerCtx context.Context) {
	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(workerCtx)
	}

	go func() {
		defer recoverRSSGoroutinePanic("rss results closer")
		wp.wg.Wait()
		close(wp.results)
	}()
}

func (wp *workerPool) submitFeedJobs(workerCtx context.Context, feedURLs []string) {
	go func() {
		defer recoverRSSGoroutinePanic("rss job sender")
		defer close(wp.jobs)
		for _, url := range feedURLs {
			select {
			case wp.jobs <- url:
			case <-workerCtx.Done():
				return
			}
		}
	}()
}

func newFeedResults() *FeedResults {
	return &FeedResults{
		Entries:         make(map[string][]Entry),
		FeedErrors:      make(map[string]string),
		FeedValidators:  make(map[string]FeedHTTPValidators),
		FeedNotModified: make(map[string]bool),
	}
}

func (wp *workerPool) collectFeedResults(
	ctx context.Context,
	cancel context.CancelFunc,
	feedURLs []string,
) (*FeedResults, error) {
	fr := newFeedResults()

	batchTimer := time.NewTimer(wp.parser.workerTimeout)
	defer batchTimer.Stop()

	for i := 0; i < len(feedURLs); i++ {
		select {
		case result, ok := <-wp.results:
			if !ok {
				if err := ctx.Err(); err != nil {
					return resultsOnContextError(fr, err)
				}
				return handleResultsChannelClosed(fr, feedURLs, i)
			}
			applyFeedResult(fr, result)
		case <-ctx.Done():
			err := ctx.Err()
			if errors.Is(err, context.DeadlineExceeded) {
				wp.drainBufferedResults(fr)
			}
			return resultsOnContextError(fr, err)
		case <-batchTimer.C:
			return wp.handleWorkerPoolTimeout(cancel, fr, feedURLs, i)
		}
	}

	logFeedErrorSummary(fr.FeedErrors)
	return fr, nil
}

// resultsOnContextError decides what a context error hands back to the caller.
// A deadline is the caller's poll budget running out: the results collected so
// far are returned with the error, and feeds that never answered are simply
// absent from them. A cancellation is a shutdown and keeps returning nil, so
// nothing is applied on the way out.
func resultsOnContextError(fr *FeedResults, err error) (*FeedResults, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return fr, err
	}
	return nil, err
}

func logFeedErrorSummary(feedErrors map[string]string) {
	if len(feedErrors) == 0 {
		return
	}

	log.WithField("failed_feed_count", len(feedErrors)).Error("Some RSS feeds failed to parse")

	feedURLs := make([]string, 0, len(feedErrors))
	for feedURL := range feedErrors {
		feedURLs = append(feedURLs, feedURL)
	}
	sort.Strings(feedURLs)

	for _, feedURL := range feedURLs {
		log.WithFields(log.Fields{
			"feed_url": textutil.RedactURLCredentials(feedURL),
			"error":    textutil.RedactWebhookSecretsForURL(feedErrors[feedURL], feedURL),
		}).Error("RSS feed failed to parse")
	}
}

func applyFeedResult(fr *FeedResults, result feedResult) {
	if result.err != nil {
		fr.FeedErrors[result.url] = OperatorErrorMessage(result.err)
		fr.Entries[result.url] = []Entry{}
		return
	}
	fr.Entries[result.url] = result.entries
	fr.FeedValidators[result.url] = result.metadata.Validators
	if result.metadata.NotModified {
		fr.FeedNotModified[result.url] = true
	}
}

func handleResultsChannelClosed(fr *FeedResults, feedURLs []string, collected int) (*FeedResults, error) {
	log.WithField("collected", collected).Warn("Results channel closed before collecting all results")
	markMissingFeedErrors(fr, feedURLs, "results channel closed before feed completed")
	return fr, fmt.Errorf("collected %d of %d feeds before workers finished", collected, len(feedURLs))
}

func (wp *workerPool) handleWorkerPoolTimeout(
	cancel context.CancelFunc,
	fr *FeedResults,
	feedURLs []string,
	collected int,
) (*FeedResults, error) {
	collected += wp.drainBufferedResults(fr)

	pendingFeeds := missingFeedURLs(fr, feedURLs)
	log.WithFields(log.Fields{
		"remaining_feeds": len(feedURLs) - collected,
		"pending_feeds":   boundedFeedURLList(pendingFeeds),
		"pending_count":   len(pendingFeeds),
		"timeout_ms":      wp.parser.workerTimeout.Milliseconds(),
	}).Error("Worker pool timeout waiting for feed results")

	cancel()
	wp.shutdownWorkersAfterTimeout()

	markMissingFeedErrors(fr, feedURLs, fmt.Sprintf("worker pool timeout after %s", wp.parser.workerTimeout))
	return fr, fmt.Errorf(
		"worker pool timeout after processing %d of %d feeds; pending feeds: %s",
		collected,
		len(feedURLs),
		summarizeFeedURLs(pendingFeeds),
	)
}

// drainBufferedResults consumes results that workers already delivered before the
// batch deadline without blocking, so they are honoured instead of being reported
// as timeouts. It returns the number of results applied.
func (wp *workerPool) drainBufferedResults(fr *FeedResults) int {
	drained := 0
	for {
		select {
		case result, ok := <-wp.results:
			if !ok {
				return drained
			}
			applyFeedResult(fr, result)
			drained++
		default:
			return drained
		}
	}
}

func (wp *workerPool) shutdownWorkersAfterTimeout() {
	waitDone := make(chan struct{})
	go func() {
		defer recoverRSSGoroutinePanic("rss timeout waiter")
		wp.wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		log.Info("All workers shut down cleanly after timeout")
	case <-time.After(5 * time.Second):
		log.Warn("Some workers did not shut down within 5 seconds after timeout")
	}
}

func missingFeedURLs(fr *FeedResults, feedURLs []string) []string {
	pending := make([]string, 0)
	for _, feedURL := range feedURLs {
		if _, exists := fr.Entries[feedURL]; exists {
			continue
		}
		if _, exists := fr.FeedErrors[feedURL]; exists {
			continue
		}
		pending = append(pending, feedURL)
	}
	sort.Strings(pending)
	return pending
}

func boundedFeedURLList(feedURLs []string) []string {
	const maxPendingFeedsInError = 5
	redacted := redactFeedURLList(feedURLs)
	if len(feedURLs) <= maxPendingFeedsInError {
		return redacted
	}
	return redacted[:maxPendingFeedsInError]
}

func summarizeFeedURLs(feedURLs []string) string {
	const maxPendingFeedsInError = 5
	if len(feedURLs) == 0 {
		return "none"
	}
	redacted := redactFeedURLList(feedURLs)
	if len(feedURLs) <= maxPendingFeedsInError {
		return strings.Join(redacted, ", ")
	}
	return fmt.Sprintf(
		"%s (+%d more)",
		strings.Join(redacted[:maxPendingFeedsInError], ", "),
		len(feedURLs)-maxPendingFeedsInError,
	)
}

func redactFeedURLList(feedURLs []string) []string {
	redacted := make([]string, len(feedURLs))
	for i, feedURL := range feedURLs {
		redacted[i] = textutil.RedactURLCredentials(feedURL)
	}
	return redacted
}

func markMissingFeedErrors(fr *FeedResults, feedURLs []string, errMsg string) {
	for _, feedURL := range feedURLs {
		if _, exists := fr.Entries[feedURL]; exists {
			continue
		}
		if _, exists := fr.FeedErrors[feedURL]; exists {
			continue
		}
		fr.FeedErrors[feedURL] = OperatorErrorMessageFromString(errMsg)
		fr.Entries[feedURL] = []Entry{}
	}
}

// ParseMultipleFeeds parses multiple RSS feeds concurrently with worker limits.
// It returns partial FeedResults when some feeds fail, the worker pool times
// out, or the caller's context deadline expires; feeds that had not answered
// are then absent from the result rather than reported as failed. In the
// worker-pool-timeout case, uncollected feed URLs are included as failed
// empty-entry feeds so callers can replace stale feed health status.
func (p *Parser) ParseMultipleFeeds(ctx context.Context, feedURLs []string, maxWorkers int) (*FeedResults, error) {
	return p.ParseMultipleFeedsWithValidators(ctx, feedURLs, maxWorkers, nil)
}

// ParseMultipleFeedsWithValidators parses multiple RSS feeds concurrently with
// optional per-feed HTTP cache validators.
func (p *Parser) ParseMultipleFeedsWithValidators(
	ctx context.Context,
	feedURLs []string,
	maxWorkers int,
	validators map[string]FeedHTTPValidators,
) (*FeedResults, error) {
	if len(feedURLs) == 0 {
		return newFeedResults(), nil
	}

	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	if maxWorkers > len(feedURLs) {
		maxWorkers = len(feedURLs)
	}

	pool := newWorkerPoolWithValidators(maxWorkers, p, validators)
	return pool.processFeeds(ctx, feedURLs)
}
