package rss

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// errOnlyContext reports a non-nil Err() while its Done() channel never fires,
// so select statements deterministically take non-context cases first.
type errOnlyContext struct {
	context.Context
}

func (errOnlyContext) Done() <-chan struct{} { return nil }

func (errOnlyContext) Err() error { return context.Canceled }

// deadlineOnlyContext reports context.DeadlineExceeded while its Done() channel
// never fires, so a select deterministically takes the non-context case first.
type deadlineOnlyContext struct{ context.Context }

func (deadlineOnlyContext) Done() <-chan struct{} { return nil }

func (deadlineOnlyContext) Err() error { return context.DeadlineExceeded }

// TestParseMultipleFeedsClampsNonPositiveMaxWorkersToOne pins Finding 4: a
// negative maxWorkers must not panic building the jobs/results channels, and
// a zero maxWorkers must not start zero workers (which fails every feed fast
// with "results channel closed" instead of processing it). Both are clamped
// to 1 before the pool is built.
func TestParseMultipleFeedsClampsNonPositiveMaxWorkersToOne(t *testing.T) {
	tests := []struct {
		name       string
		maxWorkers int
	}{
		{name: "negative", maxWorkers: -1},
		{name: "zero", maxWorkers: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
			body := rssFeedFixture(`<item>
      <title>Clamp Check</title>
      <link>https://example.test/clamp-check</link>
      <guid>clamp-check</guid>
    </item>`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/rss+xml")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			feedURL := server.URL + "/feed.xml"
			results, err := parser.ParseMultipleFeedsWithValidators(context.Background(), []string{feedURL}, tc.maxWorkers, nil)
			if err != nil {
				t.Fatalf("ParseMultipleFeedsWithValidators() error = %v, want nil", err)
			}
			if got := len(results.Entries[feedURL]); got != 1 {
				t.Fatalf("Entries[%q] = %d, want 1", feedURL, got)
			}
			if got := len(results.FeedErrors); got != 0 {
				t.Fatalf("FeedErrors = %#v, want none", results.FeedErrors)
			}
		})
	}
}

// TestSubmitFeedJobsReturnsOnContextCancelWhileStillQueuing pins Finding 5:
// submitFeedJobs' "case <-workerCtx.Done(): return" branch, previously only
// reached sometimes (timing-dependent on a batch-timeout firing while the
// sender still had URLs queued). This drives it deterministically: one
// worker (cap(jobs) == 2) is provably mid-request when the sender tries to
// enqueue a fifth URL and blocks on the full buffered channel; the pool
// context is then cancelled from outside, which must unblock the sender via
// this exact select case instead of leaking the goroutine.
func TestSubmitFeedJobsReturnsOnContextCancelWhileStillQueuing(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPoolWithValidators(1, parser, nil)
	if cap(pool.jobs) != 2 {
		t.Fatalf("cap(jobs) = %d, want 2", cap(pool.jobs))
	}

	started := make(chan struct{})
	var startOnce sync.Once
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedFixture("")))
	}))
	defer server.Close()
	defer close(release)

	feedURLs := []string{
		server.URL + "/feed1.xml",
		server.URL + "/feed2.xml",
		server.URL + "/feed3.xml",
		server.URL + "/feed4.xml",
		server.URL + "/feed5.xml",
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	pool.startWorkers(workerCtx)
	pool.submitFeedJobs(workerCtx, feedURLs)

	<-started // the single worker is now mid-request, blocked on <-release
	// Let the sender fill cap(jobs)==2 and then block trying to enqueue the
	// remaining URLs -- this is setup (letting the sender reach its blocking
	// send), not a correctness-critical timing assumption.
	time.Sleep(100 * time.Millisecond)

	cancel() // must hit submitFeedJobs' <-workerCtx.Done() case, not hang

	done := make(chan struct{})
	go func() {
		for range pool.results {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submitFeedJobs did not return after context cancellation while still queuing")
	}
}

func TestCollectFeedResultsKeepsResultsWhenCycleBudgetExpires(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPool(2, parser)

	urls := []string{
		"https://feeds.example/f1",
		"https://feeds.example/f2",
		"https://feeds.example/f3",
	}
	pushed := urls[:2]
	for _, feedURL := range pushed {
		pool.results <- feedResult{
			url:     feedURL,
			entries: []Entry{{Title: "Item for " + feedURL, Link: feedURL}},
			metadata: FeedFetchMetadata{
				Validators: FeedHTTPValidators{ETag: `"etag-` + feedURL + `"`},
			},
		}
	}

	// Already expired: no wall clock involved, ctx.Done() is closed from the start.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()

	fr, err := pool.collectFeedResults(ctx, func() {}, urls)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collectFeedResults() error = %v, want context.DeadlineExceeded", err)
	}
	if fr == nil {
		t.Fatal("collectFeedResults() = <nil>, want partial results")
	}
	for _, feedURL := range pushed {
		if got := len(fr.Entries[feedURL]); got != 1 {
			t.Fatalf("Entries[%q] = %d items, want 1", feedURL, got)
		}
		if want := `"etag-` + feedURL + `"`; fr.FeedValidators[feedURL].ETag != want {
			t.Fatalf("FeedValidators[%q].ETag = %q, want %q", feedURL, fr.FeedValidators[feedURL].ETag, want)
		}
	}
	notAnswered := urls[2]
	if _, exists := fr.Entries[notAnswered]; exists {
		t.Fatalf("Entries[%q] exists, want the not-answered feed absent", notAnswered)
	}
	if _, exists := fr.FeedErrors[notAnswered]; exists {
		t.Fatalf("FeedErrors[%q] exists, want the not-answered feed absent", notAnswered)
	}
}

func TestCollectFeedResultsKeepsPartialResultsWhenClosedChannelMeetsDeadline(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPool(2, parser)

	answeredURL := "https://feeds.example/answered"
	notAnsweredURL := "https://feeds.example/not-answered"
	pool.results <- feedResult{
		url:     answeredURL,
		entries: []Entry{{Title: "Item", Link: answeredURL}},
	}
	close(pool.results)

	ctx := deadlineOnlyContext{context.Background()}
	fr, err := pool.collectFeedResults(ctx, func() {}, []string{answeredURL, notAnsweredURL})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collectFeedResults() error = %v, want context.DeadlineExceeded", err)
	}
	if fr == nil {
		t.Fatal("collectFeedResults() = <nil>, want partial results")
	}
	if got := len(fr.Entries[answeredURL]); got != 1 {
		t.Fatalf("Entries[%q] = %d items, want 1", answeredURL, got)
	}
	if _, exists := fr.Entries[notAnsweredURL]; exists {
		t.Fatalf("Entries[%q] exists, want the not-answered feed absent", notAnsweredURL)
	}
	if _, exists := fr.FeedErrors[notAnsweredURL]; exists {
		t.Fatalf("FeedErrors[%q] exists, want the not-answered feed absent", notAnsweredURL)
	}
}

// TestCollectFeedResultsDropsResultsOnShutdownCancel is an invariant guard: a
// shutdown cancellation must keep discarding whatever was collected, unlike an
// expired cycle deadline. It stays green before and after this change.
func TestCollectFeedResultsDropsResultsOnShutdownCancel(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPool(2, parser)

	feedURL := "https://feeds.example/buffered"
	pool.results <- feedResult{
		url:     feedURL,
		entries: []Entry{{Title: "Item", Link: feedURL}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A second URL with no buffered result forces at least one iteration to be
	// decided by ctx.Done() rather than wp.results, regardless of select order.
	fr, err := pool.collectFeedResults(ctx, func() {}, []string{feedURL, "https://feeds.example/other"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectFeedResults() error = %v, want context.Canceled", err)
	}
	if fr != nil {
		t.Fatalf("collectFeedResults() = %#v, want nil results on shutdown cancel", fr)
	}
}

func TestNewWorkerPoolInitializesChannelsAndParser(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(2, parser)

	if pool.maxWorkers != 2 {
		t.Fatalf("maxWorkers = %d, want 2", pool.maxWorkers)
	}
	if pool.parser != parser {
		t.Fatal("pool parser not wired to provided parser")
	}
	if cap(pool.jobs) != 4 || cap(pool.results) != 4 {
		t.Fatalf("channel capacities = %d/%d, want 4/4", cap(pool.jobs), cap(pool.results))
	}
	if pool.validators != nil {
		t.Fatalf("validators = %#v, want nil for plain pool", pool.validators)
	}
}

func TestWorkerStopsWhenContextCancelledDuringResultDelivery(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	parser := NewParser(0, time.Millisecond, time.Second)
	parser.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case requestSeen <- struct{}{}:
		default:
		}
		return nil, errors.New("transport rejected")
	})}

	pool := &workerPool{
		maxWorkers: 1,
		jobs:       make(chan string, 1),
		results:    make(chan feedResult), // unbuffered and never read
		parser:     parser,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.wg.Add(1)
	go pool.worker(ctx)
	pool.jobs <- "https://feeds.example/rss"

	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not process submitted job")
	}
	cancel()

	done := make(chan struct{})
	go func() {
		pool.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after context cancellation during result delivery")
	}
}

func TestRecoverRSSGoroutinePanicLogsRecoveredPanic(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	func() {
		defer recoverRSSGoroutinePanic("test component")
		panic("boom")
	}()

	found := false
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "Recovered panic in RSS goroutine") {
			found = true
			if entry.Data["component"] != "test component" {
				t.Fatalf("component = %v, want test component", entry.Data["component"])
			}
		}
	}
	if !found {
		t.Fatal("recoverRSSGoroutinePanic() did not log the recovered panic")
	}
}

func TestCollectFeedResultsHandlesPrematurelyClosedResultsChannel(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPool(1, parser)
	close(pool.results)

	feedURL := "https://feeds.example/rss"
	fr, err := pool.collectFeedResults(context.Background(), func() {}, []string{feedURL})
	if err == nil {
		t.Fatal("collectFeedResults() succeeded with closed results channel")
	}
	if !strings.Contains(err.Error(), "collected 0 of 1 feeds") {
		t.Fatalf("error = %v, want collected count context", err)
	}
	if fr == nil {
		t.Fatal("collectFeedResults() returned nil results, want partial results")
	}
	if got := fr.FeedErrors[feedURL]; got != "RSS feed worker stopped before completing" {
		t.Fatalf("FeedErrors[%q] = %q, want worker stopped message", feedURL, got)
	}
	if entries, ok := fr.Entries[feedURL]; !ok || len(entries) != 0 {
		t.Fatalf("Entries[%q] = %#v, want empty slice placeholder", feedURL, entries)
	}
}

func TestCollectFeedResultsPropagatesContextErrorOnClosedChannel(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Minute)
	pool := newWorkerPool(1, parser)
	close(pool.results)

	ctx := errOnlyContext{context.Background()}
	fr, err := pool.collectFeedResults(ctx, func() {}, []string{"https://feeds.example/rss"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectFeedResults() error = %v, want context.Canceled", err)
	}
	if fr != nil {
		t.Fatalf("collectFeedResults() = %#v, want nil results on context error", fr)
	}
}

func TestShutdownWorkersAfterTimeoutWarnsWhenWorkersHang(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 5s worker shutdown wait in short mode")
	}

	hook := logtest.NewGlobal()
	defer hook.Reset()

	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(1, parser)
	pool.wg.Add(1) // simulate a hung worker that never finishes
	defer pool.wg.Done()

	start := time.Now()
	pool.shutdownWorkersAfterTimeout()
	if elapsed := time.Since(start); elapsed < 4*time.Second {
		t.Fatalf("shutdownWorkersAfterTimeout() returned after %v, want ~5s wait", elapsed)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "did not shut down") {
			found = true
		}
	}
	if !found {
		t.Fatal("shutdownWorkersAfterTimeout() did not warn about hung workers")
	}
}

func TestMissingFeedURLsSkipsCollectedAndFailedFeeds(t *testing.T) {
	fr := newFeedResults()
	fr.Entries["https://feeds.example/done"] = []Entry{}
	fr.FeedErrors["https://feeds.example/failed"] = "boom"

	pending := missingFeedURLs(fr, []string{
		"https://feeds.example/done",
		"https://feeds.example/failed",
		"https://feeds.example/pending",
	})
	if len(pending) != 1 || pending[0] != "https://feeds.example/pending" {
		t.Fatalf("missingFeedURLs() = %#v, want only the pending feed", pending)
	}
}

func TestBoundedFeedURLListTruncatesLongLists(t *testing.T) {
	urls := []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"}
	got := boundedFeedURLList(urls)
	if len(got) != 5 {
		t.Fatalf("boundedFeedURLList() len = %d, want 5", len(got))
	}

	short := boundedFeedURLList([]string{"u1", "u2"})
	if len(short) != 2 {
		t.Fatalf("boundedFeedURLList(short) len = %d, want 2", len(short))
	}
}

func TestSummarizeFeedURLs(t *testing.T) {
	if got := summarizeFeedURLs(nil); got != "none" {
		t.Fatalf("summarizeFeedURLs(nil) = %q, want none", got)
	}
	if got := summarizeFeedURLs([]string{"u1", "u2"}); got != "u1, u2" {
		t.Fatalf("summarizeFeedURLs(short) = %q, want joined list", got)
	}
	got := summarizeFeedURLs([]string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"})
	if !strings.Contains(got, "(+2 more)") {
		t.Fatalf("summarizeFeedURLs(long) = %q, want +2 more suffix", got)
	}
}

func TestMarkMissingFeedErrorsSkipsFeedsWithExistingState(t *testing.T) {
	fr := newFeedResults()
	fr.Entries["https://feeds.example/done"] = []Entry{}
	fr.FeedErrors["https://feeds.example/failed"] = "original error"

	markMissingFeedErrors(fr, []string{
		"https://feeds.example/done",
		"https://feeds.example/failed",
		"https://feeds.example/pending",
	}, "worker pool timeout after 1s")

	if got := fr.FeedErrors["https://feeds.example/failed"]; got != "original error" {
		t.Fatalf("existing error overwritten: %q", got)
	}
	if _, exists := fr.FeedErrors["https://feeds.example/done"]; exists {
		t.Fatal("collected feed marked as failed")
	}
	if got := fr.FeedErrors["https://feeds.example/pending"]; got != "RSS feed worker pool timed out" {
		t.Fatalf("pending feed error = %q, want worker pool timeout message", got)
	}
}

func TestParseMultipleFeedsClampsWorkerCountToFeedCount(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, 5*time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedFixture(`<item>
      <title>Clamp Item</title>
      <link>https://example.test/clamp</link>
      <guid>clamp-guid</guid>
    </item>`)))
	}))
	defer server.Close()

	feedURL := server.URL + "/feed.xml"
	results, err := parser.ParseMultipleFeeds(context.Background(), []string{feedURL}, 8)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds() error = %v", err)
	}
	if len(results.Entries[feedURL]) != 1 {
		t.Fatalf("Entries[%q] = %d items, want 1", feedURL, len(results.Entries[feedURL]))
	}
}

func TestHandleWorkerPoolTimeoutHonoursBufferedResults(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(2, parser)

	urls := []string{
		"https://feeds.example/f1",
		"https://feeds.example/f2",
		"https://feeds.example/f3",
		"https://feeds.example/f4",
	}
	if cap(pool.results) != len(urls) {
		t.Fatalf("cap(results) = %d, want %d", cap(pool.results), len(urls))
	}
	for _, feedURL := range urls {
		pool.results <- feedResult{
			url:     feedURL,
			entries: []Entry{{Title: "Item for " + feedURL, Link: feedURL}},
			metadata: FeedFetchMetadata{
				Validators: FeedHTTPValidators{ETag: `"etag-` + feedURL + `"`},
			},
		}
	}

	hook := logtest.NewGlobal()
	defer hook.Reset()

	fr := newFeedResults()
	type timeoutOutcome struct {
		fr  *FeedResults
		err error
	}
	// Watchdog, not a timing assertion: the drain must never block, and a blocking
	// one would otherwise only surface as the package-level test timeout.
	returned := make(chan timeoutOutcome, 1)
	go func() {
		gotFR, gotErr := pool.handleWorkerPoolTimeout(func() {}, fr, urls, 0)
		returned <- timeoutOutcome{gotFR, gotErr}
	}()
	var got *FeedResults
	var err error
	select {
	case outcome := <-returned:
		got, err = outcome.fr, outcome.err
	case <-time.After(10 * time.Second):
		t.Fatal("handleWorkerPoolTimeout() did not return: the buffered-results drain blocked")
	}
	if err == nil {
		t.Fatal("handleWorkerPoolTimeout() returned no error, want the batch timeout error")
	}
	if got != fr {
		t.Fatal("handleWorkerPoolTimeout() did not return the collected FeedResults")
	}
	if len(fr.FeedErrors) != 0 {
		t.Fatalf("FeedErrors = %#v, want none for feeds whose results were already delivered", fr.FeedErrors)
	}
	for _, feedURL := range urls {
		if len(fr.Entries[feedURL]) != 1 {
			t.Fatalf("Entries[%q] = %#v, want 1 entry", feedURL, fr.Entries[feedURL])
		}
		if want := `"etag-` + feedURL + `"`; fr.FeedValidators[feedURL].ETag != want {
			t.Fatalf("FeedValidators[%q].ETag = %q, want %q", feedURL, fr.FeedValidators[feedURL].ETag, want)
		}
	}
	if !strings.Contains(err.Error(), "processing 4 of 4 feeds") {
		t.Fatalf("error = %v, want a collected count of 4 of 4", err)
	}
	if !strings.Contains(err.Error(), "pending feeds: none") {
		t.Fatalf("error = %v, want no pending feeds", err)
	}

	var timeoutEntry *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "Worker pool timeout waiting for feed results") {
			timeoutEntry = entry
		}
	}
	if timeoutEntry == nil {
		t.Fatal("handleWorkerPoolTimeout() did not log the worker pool timeout")
	}
	if got := timeoutEntry.Data["remaining_feeds"]; got != 0 {
		t.Fatalf("log remaining_feeds = %v, want 0 after every result was drained", got)
	}
	if got := timeoutEntry.Data["pending_count"]; got != 0 {
		t.Fatalf("log pending_count = %v, want 0 after every result was drained", got)
	}
}

func TestHandleWorkerPoolTimeoutKeepsRealErrorsAndTimesOutPendingFeeds(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(2, parser)

	okURL := "https://feeds.example/ok"
	badURL := "https://feeds.example/bad"
	hungURL := "https://feeds.example/hung"
	urls := []string{okURL, badURL, hungURL}

	pool.results <- feedResult{
		url:     okURL,
		entries: []Entry{{Title: "Delivered before the deadline", Link: okURL}},
	}
	pool.results <- feedResult{url: badURL, err: errors.New("connection refused")}

	fr := newFeedResults()
	// Watchdog, not a timing assertion: see the buffered-results test above.
	timedOut := make(chan error, 1)
	go func() {
		_, gotErr := pool.handleWorkerPoolTimeout(func() {}, fr, urls, 0)
		timedOut <- gotErr
	}()
	var err error
	select {
	case err = <-timedOut:
	case <-time.After(10 * time.Second):
		t.Fatal("handleWorkerPoolTimeout() did not return: the buffered-results drain blocked")
	}
	if err == nil {
		t.Fatal("handleWorkerPoolTimeout() returned no error, want the batch timeout error")
	}
	if len(fr.Entries[okURL]) != 1 {
		t.Fatalf("Entries[%q] = %#v, want 1 entry", okURL, fr.Entries[okURL])
	}
	if got, exists := fr.FeedErrors[okURL]; exists {
		t.Fatalf("FeedErrors[%q] = %q, want no error for a delivered feed", okURL, got)
	}
	if got := fr.FeedErrors[badURL]; got != "connection refused" {
		t.Fatalf("FeedErrors[%q] = %q, want the classified worker error", badURL, got)
	}
	if got := fr.FeedErrors[hungURL]; got != "RSS feed worker pool timed out" {
		t.Fatalf("FeedErrors[%q] = %q, want the worker pool timeout message", hungURL, got)
	}
	if entries, exists := fr.Entries[hungURL]; !exists || len(entries) != 0 {
		t.Fatalf("Entries[%q] = %#v, want empty slice placeholder", hungURL, entries)
	}
	if !strings.Contains(err.Error(), "pending feeds: "+hungURL) {
		t.Fatalf("error = %v, want only %q named as pending", err, hungURL)
	}
	if strings.Contains(err.Error(), okURL) || strings.Contains(err.Error(), badURL) {
		t.Fatalf("error = %v, want no delivered feed named as pending", err)
	}
	if !strings.Contains(err.Error(), "processing 2 of 3 feeds") {
		t.Fatalf("error = %v, want the drained results counted as collected", err)
	}
}

func TestDrainBufferedResultsStopsOnClosedChannel(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(1, parser)

	feedURL := "https://feeds.example/closed"
	pool.results <- feedResult{
		url:     feedURL,
		entries: []Entry{{Title: "Buffered before close", Link: feedURL}},
	}
	close(pool.results)

	fr := newFeedResults()
	drained := make(chan int, 1)
	go func() {
		drained <- pool.drainBufferedResults(fr)
	}()

	select {
	case got := <-drained:
		if got != 1 {
			t.Fatalf("drainBufferedResults() = %d, want 1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drainBufferedResults() did not return on a closed results channel")
	}

	if len(fr.Entries[feedURL]) != 1 {
		t.Fatalf("Entries[%q] = %#v, want 1 entry", feedURL, fr.Entries[feedURL])
	}
}

// TestDrainBufferedResultsTakesMoreResultsThanChannelCapacity pins the "loop until
// default" contract: cap(results) is maxWorkers*2, but workers parked on a full
// channel can hand over further results while the drain runs, so a drain bounded by
// cap(wp.results) would silently drop them back into the timeout path. The settle
// below is setup (letting the third sender park), not a timing assertion.
func TestDrainBufferedResultsTakesMoreResultsThanChannelCapacity(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	pool := newWorkerPool(1, parser)
	if cap(pool.results) != 2 {
		t.Fatalf("cap(results) = %d, want 2", cap(pool.results))
	}

	pool.results <- feedResult{url: "https://feeds.example/a", entries: []Entry{{Title: "a"}}}
	pool.results <- feedResult{url: "https://feeds.example/b", entries: []Entry{{Title: "b"}}}

	sending := make(chan struct{})
	go func() {
		close(sending)
		// Blocks: the buffer is full until the drain takes its first result.
		pool.results <- feedResult{url: "https://feeds.example/c", entries: []Entry{{Title: "c"}}}
	}()
	<-sending
	time.Sleep(500 * time.Millisecond)

	fr := newFeedResults()
	if got := pool.drainBufferedResults(fr); got != 3 {
		t.Fatalf("drainBufferedResults() = %d, want 3 with a worker parked on the full channel", got)
	}
	for _, feedURL := range []string{"https://feeds.example/a", "https://feeds.example/b", "https://feeds.example/c"} {
		if len(fr.Entries[feedURL]) != 1 {
			t.Fatalf("Entries[%q] = %#v, want 1 entry", feedURL, fr.Entries[feedURL])
		}
	}
}
