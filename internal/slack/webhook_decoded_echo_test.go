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
			hook := logtest.NewGlobal()
			defer hook.Reset()
			webhookURL := "https://hooks.eu.example/services/a" + tt.escaped + "b/FAKEPROVIDERTOKEN"
			sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				wantPath := "/services/a" + tt.decoded + "b/FAKEPROVIDERTOKEN"
				if req.URL.Path != wantPath {
					t.Fatalf("request URL path = %q, want %q", req.URL.Path, wantPath)
				}
				decodedURL := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 Internal Server Error",
					Body:       io.NopCloser(strings.NewReader("failed for " + decodedURL)),
					Header:     make(http.Header),
				}, nil
			}))

			err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
			if err == nil {
				t.Fatal("expected server error")
			}
			foundBody := false
			for _, entry := range hook.AllEntries() {
				body, _ := entry.Data["response_body"].(string)
				if strings.Contains(body, "FAKEPROVIDERTOKEN") {
					t.Fatalf("response_body log leaked decoded webhook token: %q", body)
				}
				if body != "" {
					if body != "failed for https://hooks.eu.example/services/[redacted]" {
						t.Fatalf("response_body log = %q", body)
					}
					foundBody = true
				}
			}
			if !foundBody {
				t.Fatal("expected a response_body log entry")
			}
			if strings.Contains(err.Error(), "FAKEPROVIDERTOKEN") {
				t.Fatalf("server error leaked decoded webhook token: %v", err)
			}
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
		want         string
	}{
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
			hook := logtest.NewGlobal()
			defer hook.Reset()
			sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				path := req.URL.EscapedPath()
				if tt.decodedPath {
					path = req.URL.Path
				}
				query := req.URL.RawQuery
				if tt.decodedQuery {
					var err error
					query, err = url.QueryUnescape(query)
					if err != nil {
						t.Fatalf("decode request query: %v", err)
					}
				}
				echoed := path
				if !tt.pathOnly {
					echoed = req.URL.Scheme + "://" + req.URL.Host + echoed
				}
				if req.URL.RawQuery != "" {
					echoed += "?" + query
				}
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 Internal Server Error",
					Body:       io.NopCloser(strings.NewReader("failed for " + echoed)),
					Header:     make(http.Header),
				}, nil
			}))

			err := sendTestSlackRansomware(context.Background(), sender, tt.webhookURL)
			if err == nil {
				t.Fatal("expected server error")
			}
			for _, token := range []string{"FAKEPROVIDERTOKEN", "EXTRA", "SECRET"} {
				if strings.Contains(err.Error(), token) {
					t.Fatalf("server error leaked %q: %v", token, err)
				}
			}
			foundBody := false
			for _, entry := range hook.AllEntries() {
				body, _ := entry.Data["response_body"].(string)
				if body == "" {
					continue
				}
				for _, token := range []string{"FAKEPROVIDERTOKEN", "EXTRA", "SECRET"} {
					if strings.Contains(body, token) {
						t.Fatalf("response_body log leaked %q: %q", token, body)
					}
				}
				if body != tt.want {
					t.Fatalf("response_body log = %q, want %q", body, tt.want)
				}
				foundBody = true
			}
			if !foundBody {
				t.Fatal("expected a response_body log entry")
			}
		})
	}
}
