package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestSlackCompatibleWebhookResponseDoesNotLogAnchoredURLTail(t *testing.T) {
	const webhookURL = "https://hooks.example.test/services/T0"
	const tail = "SYNTHETICSECRET35"
	responseBody := "delivery to " + webhookURL + "/B0/" + tail + " failed"
	hook := logtest.NewGlobal()
	defer hook.Reset()

	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != webhookURL {
			t.Errorf("request URL = %q, want %q", got, webhookURL)
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}, nil
	}))
	err := sender.SendPayload(context.Background(), webhookURL, map[string]string{"text": "hello"})
	if err == nil {
		t.Fatal("SendPayload() error = nil, want a provider rejection")
	}
	if strings.Contains(err.Error(), tail) {
		t.Fatalf("returned webhook error leaked URL tail: %v", err)
	}
	foundBody := false
	for _, entry := range hook.AllEntries() {
		if body, _ := entry.Data["response_body"].(string); strings.Contains(body, tail) {
			t.Fatalf("response_body log leaked URL tail: %q", body)
		} else if body != "" {
			foundBody = true
		}
	}
	if !foundBody {
		t.Fatal("expected a response_body log entry")
	}
}
