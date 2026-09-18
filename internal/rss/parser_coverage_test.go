package rss

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "request timed out" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("stream failure") }

func TestNewParserNormalizesInvalidArguments(t *testing.T) {
	parser := NewParser(-3, time.Millisecond, 0)
	if parser.retryCount != 0 {
		t.Fatalf("retryCount = %d, want 0 for negative input", parser.retryCount)
	}
	if parser.workerTimeout != 30*time.Second {
		t.Fatalf("workerTimeout = %v, want 30s default for non-positive input", parser.workerTimeout)
	}
}

func TestParseFeedRetryWaitHonorsContextDeadline(t *testing.T) {
	parser := newLocalFeedTestParser(3, 10*time.Second, time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := parser.ParseFeed(ctx, server.URL+"/feed.xml")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ParseFeed() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("ParseFeed() waited %v, want early exit before retry delay", elapsed)
	}
}

func TestShouldRetryRSSParseErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "url error", err: &neturl.Error{}, want: true},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "net error", err: timeoutNetError{}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryRSSParseError(tt.err); got != tt.want {
				t.Fatalf("shouldRetryRSSParseError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestOperatorErrorMessageClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "canceled", err: context.Canceled, want: "RSS feed parsing was cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "RSS feed parsing timed out"},
		{
			name: "url timeout",
			err:  &neturl.Error{Op: "Get", URL: "https://feeds.example/rss", Err: timeoutNetError{}},
			want: "RSS feed network request timed out",
		},
		{
			name: "url dns",
			err:  &neturl.Error{Op: "Get", URL: "https://feeds.example/rss", Err: &net.DNSError{Name: "feeds.example"}},
			want: "RSS feed DNS lookup failed",
		},
		{
			name: "url other",
			err:  &neturl.Error{Op: "Get", URL: "https://feeds.example/rss", Err: errors.New("connection refused")},
			want: "RSS feed network request failed",
		},
		{
			name: "non-xml content type",
			err:  errors.New(`RSS feed returned non-XML content type "text/html"`),
			want: "RSS feed returned non-XML content instead of RSS or Atom XML",
		},
		{
			name: "response too large",
			err:  errors.New("RSS feed response too large: exceeds 10485760 bytes"),
			want: "RSS feed response is too large",
		},
		{
			name: "worker pool timeout",
			err:  errors.New("worker pool timeout after processing 0 of 2 feeds"),
			want: "RSS feed worker pool timed out",
		},
		{
			name: "results channel closed",
			err:  errors.New("results channel closed before feed completed"),
			want: "RSS feed worker stopped before completing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OperatorErrorMessage(tt.err); got != tt.want {
				t.Fatalf("OperatorErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := OperatorErrorMessageFromString("   "); got != "" {
		t.Fatalf("OperatorErrorMessageFromString(blank) = %q, want empty", got)
	}
}

func TestFetchAndParseFeedWrapperReturnsParsedFeed(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedFixture(`<item>
      <title>Wrapper Item</title>
      <link>https://example.test/wrapper</link>
      <guid>wrapper-guid</guid>
    </item>`)))
	}))
	defer server.Close()

	feed, err := parser.fetchAndParseFeed(context.Background(), server.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("fetchAndParseFeed() error = %v", err)
	}
	if feed == nil || feed.Title != "Example Feed" || len(feed.Items) != 1 {
		t.Fatalf("fetchAndParseFeed() feed = %+v, want one-item Example Feed", feed)
	}
}

func TestFetchAndParseFeedRejectsInvalidFeedURL(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)

	_, _, err := parser.fetchAndParseFeedWithValidators(context.Background(), "ftp://feeds.example/rss", FeedHTTPValidators{})
	if err == nil {
		t.Fatal("fetchAndParseFeedWithValidators() accepted ftp URL")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("error = %v, want scheme validation context", err)
	}
}

func TestFetchAndParseFeedPropagatesRequestBuildErrors(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)

	var missingCtx context.Context
	_, _, err := parser.fetchAndParseFeedWithValidators(missingCtx, "https://feeds.example/rss", FeedHTTPValidators{})
	if err == nil {
		t.Fatal("fetchAndParseFeedWithValidators() succeeded without request context")
	}
	if !strings.Contains(err.Error(), "nil Context") {
		t.Fatalf("error = %v, want nil Context request build error", err)
	}
}

func TestFetchAndParseFeedBuildsFallbackClientWhenUnset(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	parser.httpClient = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedFixture(`<item>
      <title>Fallback Client Item</title>
      <link>https://example.test/fallback</link>
      <guid>fallback-guid</guid>
    </item>`)))
	}))
	defer server.Close()

	feed, _, err := parser.fetchAndParseFeedWithValidators(context.Background(), server.URL+"/feed.xml", FeedHTTPValidators{})
	if err != nil {
		t.Fatalf("fetchAndParseFeedWithValidators() error = %v", err)
	}
	if feed == nil || len(feed.Items) != 1 {
		t.Fatalf("feed = %+v, want one item via fallback client", feed)
	}
}

func TestFetchAndParseFeedReturnsTransportErrors(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	feedURL := server.URL + "/feed.xml"
	server.Close()

	_, _, err := parser.fetchAndParseFeedWithValidators(context.Background(), feedURL, FeedHTTPValidators{})
	if err == nil {
		t.Fatal("fetchAndParseFeedWithValidators() succeeded against closed server")
	}
	var urlErr *neturl.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error = %T %v, want *url.Error transport failure", err, err)
	}
}

func TestFetchAndParseFeedRejectsDeclaredOversizedResponse(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	parser.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"application/rss+xml"}},
			ContentLength: maxRSSResponseBytes + 1,
			Body:          io.NopCloser(strings.NewReader("")),
			Request:       req,
		}, nil
	})}

	_, _, err := parser.fetchAndParseFeedWithValidators(context.Background(), "https://feeds.example/rss", FeedHTTPValidators{})
	if err == nil {
		t.Fatal("fetchAndParseFeedWithValidators() accepted oversized declared response")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want too large context", err)
	}
}

func TestFetchAndParseFeedPropagatesBodyReadErrors(t *testing.T) {
	parser := NewParser(0, time.Millisecond, time.Second)
	parser.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"application/rss+xml"}},
			ContentLength: -1,
			Body:          io.NopCloser(failingReader{}),
			Request:       req,
		}, nil
	})}

	_, _, err := parser.fetchAndParseFeedWithValidators(context.Background(), "https://feeds.example/rss", FeedHTTPValidators{})
	if err == nil {
		t.Fatal("fetchAndParseFeedWithValidators() succeeded despite body read error")
	}
	if !strings.Contains(err.Error(), "stream failure") {
		t.Fatalf("error = %v, want stream failure context", err)
	}
}

func TestRejectNonFeedContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantErr     bool
	}{
		{name: "empty", contentType: "", wantErr: false},
		{name: "whitespace", contentType: "   ", wantErr: false},
		{name: "xml", contentType: "application/rss+xml; charset=utf-8", wantErr: false},
		{name: "html", contentType: "text/html; charset=utf-8", wantErr: true},
		{name: "html with unparseable parameters", contentType: "text/html; charset", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectNonFeedContentType(tt.contentType)
			if tt.wantErr && err == nil {
				t.Fatalf("rejectNonFeedContentType(%q) = nil, want error", tt.contentType)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("rejectNonFeedContentType(%q) error = %v", tt.contentType, err)
			}
		})
	}
}

func TestFeedHTTPErrorMessages(t *testing.T) {
	var nilErr *feedHTTPError
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil feedHTTPError.Error() = %q, want empty", got)
	}
	if got := (&feedHTTPError{StatusCode: 503, Status: "503 Service Unavailable"}).Error(); got != "503 Service Unavailable" {
		t.Fatalf("Error() = %q, want status text", got)
	}
	if got := (&feedHTTPError{StatusCode: 503}).Error(); got != "HTTP 503" {
		t.Fatalf("Error() = %q, want HTTP 503 fallback", got)
	}
}

func TestParseFeedXMLRejectsMalformedXML(t *testing.T) {
	if _, err := parseFeedXML([]byte("<unclosed")); err == nil {
		t.Fatal("parseFeedXML() accepted malformed XML")
	}
}

func TestParseRSSXMLVariants(t *testing.T) {
	if _, err := parseRSSXML([]byte("<rss>")); err == nil {
		t.Fatal("parseRSSXML() accepted truncated XML")
	}

	if _, err := parseRSSXML([]byte("<rss><channel></channel></rss>")); err == nil ||
		!strings.Contains(err.Error(), "failed to detect feed type") {
		t.Fatal("parseRSSXML() accepted feed without title or items")
	}

	feed, err := parseFeedXML([]byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <item>
    <title>RDF Item</title>
    <link>https://example.test/rdf-item</link>
  </item>
</rdf:RDF>`))
	if err != nil {
		t.Fatalf("parseFeedXML(rdf) error = %v", err)
	}
	if len(feed.Items) != 1 || feed.Items[0].Title != "RDF Item" {
		t.Fatalf("parseFeedXML(rdf) items = %+v, want root-level RDF item", feed.Items)
	}
}

func TestParseAtomXMLErrorPaths(t *testing.T) {
	if _, err := parseAtomXML([]byte("<feed>")); err == nil {
		t.Fatal("parseAtomXML() accepted truncated XML")
	}

	if _, err := parseAtomXML([]byte("<feed></feed>")); err == nil ||
		!strings.Contains(err.Error(), "failed to detect feed type") {
		t.Fatal("parseAtomXML() accepted feed without title or entries")
	}
}

func TestAtomEntryLinkSelection(t *testing.T) {
	links := []atomLink{
		{Href: "https://example.test/other", Rel: "enclosure"},
		{Href: "https://example.test/canonical", Rel: "alternate"},
	}
	if got := atomEntryLink(links); got != "https://example.test/canonical" {
		t.Fatalf("atomEntryLink() = %q, want alternate link", got)
	}

	if got := atomEntryLink([]atomLink{{Href: "   ", Rel: "alternate"}}); got != "" {
		t.Fatalf("atomEntryLink(blank hrefs) = %q, want empty", got)
	}
}

func TestAtomCategoriesUseFirstNonEmptyValue(t *testing.T) {
	got := atomCategories([]atomCategory{
		{Term: "malware"},
		{Label: "Security"},
		{Value: "chardata"},
	})
	want := []string{"malware", "Security", "chardata"}
	if len(got) != len(want) {
		t.Fatalf("atomCategories() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("atomCategories()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := firstNonEmpty("  ", ""); got != "" {
		t.Fatalf("firstNonEmpty(all blank) = %q, want empty", got)
	}
}

func TestFeedClientStopsAfterTenRedirects(t *testing.T) {
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/feed.xml", http.StatusFound)
	}))
	defer server.Close()

	_, err := parser.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatal("ParseFeed() succeeded despite redirect loop")
	}
	if !strings.Contains(err.Error(), "stopped after 10 RSS feed redirects") {
		t.Fatalf("error = %v, want redirect limit context", err)
	}
}

func TestGenerateEntryLookupKeysForEntryMatchesFieldBasedKeys(t *testing.T) {
	entry := Entry{
		Title:     "Lookup Item",
		Link:      "https://example.test/lookup",
		GUID:      "lookup-guid",
		FeedURL:   "https://feeds.example/rss",
		Published: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	got := GenerateEntryLookupKeysForEntry(entry)
	want := GenerateEntryLookupKeys(entry.FeedURL, entry.GUID, entry.Link, entry.Title, entry.Published)
	if len(got) == 0 {
		t.Fatal("GenerateEntryLookupKeysForEntry() returned no keys")
	}
	if len(got) != len(want) {
		t.Fatalf("lookup keys = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lookup key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
