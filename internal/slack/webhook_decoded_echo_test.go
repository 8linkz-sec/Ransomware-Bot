package slack

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestExecuteWebhookRedactsDecodedSlackCompatibleURLFromResponseBody(t *testing.T) {
	tests := []struct {
		name    string
		escaped string
		decoded string
	}{
		{"slash", "%2F", "/"},
		{"space", "%20", " "},
		{"quote", "%22", "\""},
		{"hash", "%23", "#"},
		{"question mark", "%3F", "?"},
		{"apostrophe", "%27", "'"},
		{"backslash", "%5C", "\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhookURL := "https://hooks.eu.example/services/a" + tt.escaped + "b/FAKEPROVIDERTOKEN"
			assertWebhookEchoRedacted(t, webhookURL, "failed for https://hooks.eu.example/services/[redacted]", func(req *http.Request) string {
				wantPath := "/services/a" + tt.decoded + "b/FAKEPROVIDERTOKEN"
				if req.URL.Path != wantPath {
					t.Fatalf("request URL path = %q, want %q", req.URL.Path, wantPath)
				}
				return req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
			})
		})
	}
}

func TestExecuteWebhookRedactsPathAndQueryEchoFromResponseBody(t *testing.T) {
	tests := []struct {
		name         string
		webhookURL   string
		decodedPath  bool
		decodedQuery bool
		pathOnly     bool
		queryOnly    bool
		want         string
	}{
		{
			name:       "query only raw",
			webhookURL: "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			queryOnly:  true,
			want:       "failed for [redacted]",
		},
		{
			name:         "query only decoded",
			webhookURL:   "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			decodedQuery: true,
			queryOnly:    true,
			want:         "failed for [redacted]",
		},
		{
			name:        "path only decoded",
			webhookURL:  "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN",
			decodedPath: true,
			pathOnly:    true,
			want:        "failed for /services/[redacted]",
		},
		{
			name:       "path only escaped",
			webhookURL: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN",
			pathOnly:   true,
			want:       "failed for /services/[redacted]",
		},
		{
			name:       "request URI with raw query",
			webhookURL: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			pathOnly:   true,
			want:       "failed for /services/[redacted]",
		},
		{
			name:         "path only decoded path and query",
			webhookURL:   "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			decodedPath:  true,
			decodedQuery: true,
			pathOnly:     true,
			want:         "failed for /services/[redacted]",
		},
		{
			name:         "query only without host",
			webhookURL:   "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			decodedQuery: true,
			pathOnly:     true,
			want:         "failed for /[redacted]",
		},
		{
			name:       "escaped path and raw query",
			webhookURL: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			want:       "failed for https://hooks.eu.example/services/[redacted]",
		},
		{
			name:        "decoded path and raw query",
			webhookURL:  "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			decodedPath: true,
			want:        "failed for https://hooks.eu.example/services/[redacted]",
		},
		{
			name:         "escaped path and percent space query",
			webhookURL:   "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			decodedQuery: true,
			want:         "failed for https://hooks.eu.example/services/[redacted]",
		},
		{
			name:         "decoded path and percent space query",
			webhookURL:   "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			decodedPath:  true,
			decodedQuery: true,
			want:         "failed for https://hooks.eu.example/services/[redacted]",
		},
		{
			name:         "query only percent space",
			webhookURL:   "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			decodedQuery: true,
			want:         "failed for https://hooks.eu.example/[redacted]",
		},
		{
			name:         "decoded path and plus space query",
			webhookURL:   "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA+SECRET",
			decodedPath:  true,
			decodedQuery: true,
			want:         "failed for https://hooks.eu.example/services/[redacted]",
		},
		{
			name:         "query only plus space",
			webhookURL:   "https://hooks.eu.example/public?token=EXTRA+SECRET",
			decodedQuery: true,
			want:         "failed for https://hooks.eu.example/[redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertWebhookEchoRedacted(t, tt.webhookURL, tt.want, func(req *http.Request) string {
				return buildProviderEcho(t, req, tt.decodedPath, tt.decodedQuery, tt.pathOnly, tt.queryOnly)
			})
		})
	}
}

func buildProviderEcho(t *testing.T, req *http.Request, decodedPath, decodedQuery, pathOnly, queryOnly bool) string {
	t.Helper()
	path := req.URL.EscapedPath()
	if decodedPath {
		path = req.URL.Path
	}
	query := req.URL.RawQuery
	if decodedQuery {
		var err error
		query, err = url.QueryUnescape(query)
		if err != nil {
			t.Fatalf("decode request query: %v", err)
		}
	}
	if queryOnly {
		return query
	}
	if !pathOnly {
		path = req.URL.Scheme + "://" + req.URL.Host + path
	}
	if req.URL.RawQuery != "" {
		path += "?" + query
	}
	return path
}

func assertWebhookEchoRedacted(t *testing.T, webhookURL, want string, echo func(*http.Request) string) {
	t.Helper()
	hook := logtest.NewGlobal()
	defer hook.Reset()
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("failed for " + echo(req))),
			Header:     make(http.Header),
		}, nil
	}))
	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected server error")
	}
	assertNoWebhookEchoTokens(t, "server error", err.Error())
	foundBody := false
	for _, entry := range hook.AllEntries() {
		body, _ := entry.Data["response_body"].(string)
		if body == "" {
			continue
		}
		assertNoWebhookEchoTokens(t, "response_body log", body)
		if body != want {
			t.Fatalf("response_body log = %q, want %q", body, want)
		}
		foundBody = true
	}
	if !foundBody {
		t.Fatal("expected a response_body log entry")
	}
}

func assertNoWebhookEchoTokens(t *testing.T, source, value string) {
	t.Helper()
	for _, token := range []string{"FAKEPROVIDERTOKEN", "EXTRA", "SECRET"} {
		if strings.Contains(value, token) {
			t.Fatalf("%s leaked %q: %q", source, token, value)
		}
	}
}
