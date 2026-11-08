package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestAPIRetryWarningRedactsConfiguredQuerySecret(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	transport := &failingRoundTripper{
		err: &net.DNSError{Err: "server misbehaving", Name: "api.example.test", IsTemporary: true},
	}
	policy := DefaultHTTPPolicy()
	policy.MaxAttempts = 2
	policy.RetryBaseDelay = 0
	client, err := NewClientWithHTTPClient("", "https://api.example.test/base?token=SEC)RET", &http.Client{Transport: transport}, policy)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	_, err = client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatal("GetLatestEntries() succeeded, want DNS failure")
	}
	if got := transport.count.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Message != "API request failed, retrying..." {
			continue
		}
		if entry.Level != log.WarnLevel {
			t.Fatalf("retry log level = %s, want WARN", entry.Level)
		}
		if got, want := fmt.Sprint(entry.Data[log.ErrorKey]), "API request failed; check network connectivity and provider availability"; got != want {
			t.Fatalf("retry warning error = %q, want safe status %q", got, want)
		}
		return
	}
	t.Fatal("retry warning was not logged")
}

func TestAPIDebugURLFieldsRedactConfiguredQuerySecret(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	policy := DefaultHTTPPolicy()
	policy.MaxAttempts = 1
	client, err := NewClientWithHTTPClient("", "https://api.example.test/base?token=SEC)RET", &http.Client{
		Transport: &failingRoundTripper{err: &net.DNSError{Err: "no such host", Name: "api.example.test", IsNotFound: true}},
	}, policy)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	_, err = client.GetLatestEntries(context.Background())
	if err == nil {
		t.Fatal("GetLatestEntries() succeeded, want DNS failure")
	}
	wantMessages := map[string]bool{
		"Fetching latest ransomware entries": false,
		"API request failed":                 false,
	}
	for _, entry := range hook.AllEntries() {
		if _, ok := wantMessages[entry.Message]; !ok {
			continue
		}
		wantMessages[entry.Message] = true
		got := fmt.Sprint(entry.Data["url"])
		if !strings.Contains(got, "api.example.test") {
			t.Fatalf("%s lost safe host context: %s", entry.Message, got)
		}
		if strings.Contains(got, "SEC") || strings.Contains(got, "RET") {
			t.Fatalf("%s leaked configured query value in URL field: %s", entry.Message, got)
		}
	}
	for message, found := range wantMessages {
		if !found {
			t.Fatalf("debug log %q not found", message)
		}
	}
}

func TestAPIRetryDebugURLFieldRedactsUserinfoAndUnknownQueryValue(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	transport := &failingRoundTripper{
		err: &net.DNSError{Err: "server misbehaving", Name: "api.example.test", IsTemporary: true},
	}
	policy := DefaultHTTPPolicy()
	policy.MaxAttempts = 2
	policy.RetryBaseDelay = 0
	client, err := NewClientWithHTTPClient("", "https://api.example.test", &http.Client{Transport: transport}, policy)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	var response any
	err = client.GetJSON(context.Background(), "https://user:PASS@api.example.test/path?ref=ALTSECRET", &response)
	if err == nil {
		t.Fatal("GetJSON() succeeded, want DNS failure")
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsTemporary {
		t.Fatalf("GetJSON() error = %T (%v), want retryable DNS cause", err, err)
	}
	warnFound := false
	retryFound := false
	for _, entry := range hook.AllEntries() {
		switch entry.Message {
		case "API request failed, retrying...":
			warnFound = true
			if got, want := fmt.Sprint(entry.Data[log.ErrorKey]), "API request failed; check network connectivity and provider availability"; got != want {
				t.Fatalf("retry warning error = %q, want safe status %q", got, want)
			}
		case "Retrying API request":
			retryFound = true
			got := fmt.Sprint(entry.Data["url"])
			if !strings.Contains(got, "api.example.test") {
				t.Fatalf("retry debug URL field lost safe host context: %s", got)
			}
			if strings.Contains(got, "PASS") || strings.Contains(got, "ALTSECRET") {
				t.Fatalf("retry debug URL field leaked credentials: %s", got)
			}
		}
	}
	if !warnFound || !retryFound {
		t.Fatalf("expected warning and retry debug entries, got warning=%t retry=%t", warnFound, retryFound)
	}
}

func TestAPISuccessDebugURLFieldRedactsConfiguredQuerySecret(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	policy := DefaultHTTPPolicy()
	policy.MaxAttempts = 1
	client, err := NewClientWithHTTPClient("", "https://api.example.test/base?token=SEC)RET", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"victims":[]}`)),
			}, nil
		}),
	}, policy)
	if err != nil {
		t.Fatalf("NewClientWithHTTPClient() error = %v", err)
	}

	if _, err := client.GetLatestEntries(context.Background()); err != nil {
		t.Fatalf("GetLatestEntries() error = %v", err)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Message != "API request successful" {
			continue
		}
		got := fmt.Sprint(entry.Data["url"])
		if !strings.Contains(got, "api.example.test") {
			t.Fatalf("successful request debug URL field lost safe host context: %s", got)
		}
		if strings.Contains(got, "SEC") || strings.Contains(got, "RET") {
			t.Fatalf("successful request debug URL field leaked query value: %s", got)
		}
		return
	}
	t.Fatal("successful request debug entry was not logged")
}
