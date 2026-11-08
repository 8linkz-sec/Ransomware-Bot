package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExecuteWebhookRedactsDecodedSlackCompatibleURLFromResponseBody(t *testing.T) {
	const webhookURL = "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN"
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.RawPath == "" || req.URL.Path != "/services/a/b/FAKEPROVIDERTOKEN" {
			t.Fatalf("request URL path was not decoded by net/url: %#v", req.URL)
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
	if strings.Contains(err.Error(), "FAKEPROVIDERTOKEN") {
		t.Fatalf("server error leaked decoded webhook token: %v", err)
	}
}
