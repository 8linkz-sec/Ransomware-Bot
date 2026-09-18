package rss

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
)

func TestParseFeedRejectsOversizedResponses(t *testing.T) {
	oversizedDescription := strings.Repeat("x", 11*1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponsef(t, w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Large Feed</title>
    <item>
      <title>Large Item</title>
      <guid>large-item</guid>
      <description>%s</description>
    </item>
  </channel>
</rss>`, oversizedDescription)
	}))
	defer server.Close()

	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second, nil)
	entries, err := parser.ParseFeed(context.Background(), server.URL+"/feed.xml")
	if err == nil {
		t.Fatalf("ParseFeed() succeeded with oversized response, entries=%d", len(entries))
	}
	if !strings.Contains(strings.ToLower(err.Error()), "too large") {
		t.Fatalf("ParseFeed() error = %v, want response size error", err)
	}
}

func TestParseFeedCapsItemsPerFeed(t *testing.T) {
	var items strings.Builder
	for i := 0; i < maxRSSItemsPerFeed+1; i++ {
		_, _ = fmt.Fprintf(&items, `
    <item>
      <title>Item %04d</title>
      <guid>item-%04d</guid>
    </item>`, i, i)
	}
	parser := newLocalFeedTestParser(0, time.Millisecond, time.Second, nil)

	entries, _ := parseLocalFeedFixture(t, parser, rssFeedFixture(items.String()))
	if len(entries) != maxRSSItemsPerFeed {
		t.Fatalf("ParseFeed() returned %d entries, want %d", len(entries), maxRSSItemsPerFeed)
	}
}

func TestParseMultipleFeedsConcurrentFeedsNoSharedParserRace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/xml")
		writeHTTPTestResponsef(t, w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Concurrent Feed</title>
    <item>
      <title>Concurrent Item %s</title>
      <guid>concurrent-%s</guid>
    </item>
  </channel>
</rss>`, id, id)
	}))
	defer server.Close()

	urls := make([]string, 40)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/feed.xml?id=%d", server.URL, i)
	}

	parser := newLocalFeedTestParser(0, time.Millisecond, 5*time.Second, nil)
	results, err := parser.ParseMultipleFeeds(context.Background(), urls, 8)
	if err != nil {
		t.Fatalf("ParseMultipleFeeds() error = %v", err)
	}
	if len(results.FeedErrors) != 0 {
		t.Fatalf("ParseMultipleFeeds() feed errors = %v", results.FeedErrors)
	}
	for _, url := range urls {
		if got := len(results.Entries[url]); got != 1 {
			t.Fatalf("Entries[%s] length = %d, want 1", url, got)
		}
	}
}

func TestParseFeedRejectsExternalEntityDeclaration(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "external entity",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE rss [ <!ENTITY xxe SYSTEM "file:///c:/windows/win.ini"> ]>
<rss version="2.0">
  <channel>
    <title>XXE Feed</title>
    <item>
      <title>&xxe;</title>
      <link>https://example.test/xxe</link>
      <guid>xxe-item</guid>
    </item>
  </channel>
</rss>`,
			wantErr: "invalid character entity &xxe;",
		},
		{
			name: "entity expansion",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE rss [
  <!ENTITY lol1 "lololololololololololololololol">
  <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
]>
<rss version="2.0">
  <channel>
    <title>Expansion Feed</title>
    <item>
      <title>&lol2;</title>
      <link>https://example.test/lol</link>
      <guid>lol-item</guid>
    </item>
  </channel>
</rss>`,
			wantErr: "invalid character entity &lol2;",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := serveLocalFeedBytes(t, []byte(tc.body))
			if err == nil {
				t.Fatalf("ParseFeed() accepted %s, entries = %d", tc.name, len(entries))
			}
			if len(entries) != 0 {
				t.Fatalf("entries = %d, want 0", len(entries))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseFeed() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestParseFeedNeverDereferencesSystemIdentifiers pins the other half of the
// XXE guarantee: a SYSTEM identifier in the DOCTYPE (external DTD subset) or in
// an entity declaration must never be fetched, so a hostile feed cannot use the
// bot as an SSRF probe or read a local file. The decoder ignores the doctype
// directive entirely, so the external DTD document parses -- but nothing on the
// referenced host may be contacted, and no local file content may surface.
func TestParseFeedNeverDereferencesSystemIdentifiers(t *testing.T) {
	var externalHits int64
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&externalHits, 1)
		w.Header().Set("Content-Type", "application/xml-dtd")
		_, _ = w.Write([]byte(`<!ENTITY xxe "LEAKED-VIA-EXTERNAL-DTD">`))
	}))
	defer external.Close()

	localFile := filepath.Join(t.TempDir(), "sentinel.txt")
	const sentinel = "LEAKED-VIA-FILE-URI"
	if err := os.WriteFile(localFile, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	fileURI := "file:///" + filepath.ToSlash(localFile)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "external dtd subset",
			body: fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE rss SYSTEM %q>
<rss version="2.0"><channel><title>t</title><item><title>plain</title><guid>g</guid></item></channel></rss>`, external.URL+"/evil.dtd"),
		},
		{
			name: "parameter entity",
			body: fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE rss [ <!ENTITY %% pe SYSTEM %q> %%pe; ]>
<rss version="2.0"><channel><title>t</title><item><title>plain</title><guid>g</guid></item></channel></rss>`, external.URL+"/evil.dtd"),
		},
		{
			name: "file uri entity declared but unused",
			body: fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE rss [ <!ENTITY xxe SYSTEM %q> ]>
<rss version="2.0"><channel><title>t</title><item><title>plain</title><guid>g</guid></item></channel></rss>`, fileURI),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := serveLocalFeedBytes(t, []byte(tc.body))
			if err != nil {
				t.Fatalf("ParseFeed() error = %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(entries))
			}
			if entries[0].Title != "plain" {
				t.Fatalf("entry title = %q, want %q", entries[0].Title, "plain")
			}
			for _, field := range []string{entries[0].Title, entries[0].Description, entries[0].Link, entries[0].FeedTitle} {
				if strings.Contains(field, sentinel) || strings.Contains(field, "LEAKED-VIA-EXTERNAL-DTD") {
					t.Fatalf("external content leaked into entry field %q", field)
				}
			}
		})
	}

	if got := atomic.LoadInt64(&externalHits); got != 0 {
		t.Fatalf("external SYSTEM identifier was fetched %d times, want 0", got)
	}
}

func TestFeedHTTPClientIgnoresProxyEnvironment(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newFeedHTTPClient(feedurl.Options{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("newFeedHTTPClient() transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("feed transport has a Proxy function, want nil so no proxy can connect on the guard's behalf")
	}
}

func TestFeedFetchDoesNotReachProxyForPrivateTarget(t *testing.T) {
	var proxyRequests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newFeedHTTPClient(feedurl.Options{})

	// A blocked IPv4 literal takes the no-DNS branch of ResolveAllowedIPs, so the
	// guarded dialer decides instantly and no resolver timeout is involved.
	resp, err := client.Get("https://10.1.2.3/feed.xml")
	if err == nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("resp.Body.Close() error = %v", closeErr)
		}
		t.Fatal("Get(blocked literal) succeeded, want blocked-range rejection")
	}
	if !strings.Contains(err.Error(), "feed host resolves to blocked address range") {
		t.Fatalf("Get() error = %v, want blocked-address context", err)
	}
	if strings.Contains(err.Error(), "proxyconnect") {
		t.Fatalf("Get() error = %v, want the guard to refuse the feed host, not a proxy connection", err)
	}
	if got := proxyRequests.Load(); got != 0 {
		t.Fatalf("proxy received %d requests, want 0", got)
	}
}
