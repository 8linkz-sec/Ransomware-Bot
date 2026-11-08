package slack

import (
	"context"
	"io"
	"net/http"
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
