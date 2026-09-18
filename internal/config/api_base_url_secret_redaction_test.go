package config

import (
	"strings"
	"testing"
)

// F2, P2 (found 2026-09-04, config.go:550, via api.NormalizeBaseURL at
// internal/api/client.go): validateAPIConfig used to return
// api.NormalizeBaseURL's raw *url.Error, wrapped twice with %w, with nothing
// redacting it -- leaking MORE than the sibling feed-URL defect (L381),
// because it echoed the FULL raw api_base_url rather than only an authority
// prefix. Fixed at the source in api.NormalizeBaseURL (see
// internal/api/base_url_secret_redaction_test.go), which this end-to-end
// test proves reaches validateAPIConfig's own wrapping unchanged.

func TestValidateAPIConfigRedactsUserinfoOnParseFailure(t *testing.T) {
	const password = "APIB64TOKEN"
	const secretTail = "APISLASHSECRET777"
	raw := "https://svc:" + password + "/" + secretTail + "@api.example.com"

	cfg := DefaultConfig()
	cfg.APIBaseURL = raw

	err := validateAPIConfig(cfg)
	if err == nil {
		t.Fatal("expected an error for a malformed api_base_url")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)

	if strings.Contains(msg, password) {
		t.Fatalf("LEAK: userinfo password %q survives: %s", password, msg)
	}
	if strings.Contains(msg, secretTail) {
		t.Fatalf("LEAK: userinfo password tail %q survives: %s", secretTail, msg)
	}
	if strings.Contains(msg, raw) {
		t.Fatalf("LEAK: full raw api_base_url survives: %s", msg)
	}
	if !strings.Contains(msg, "api_base_url is invalid") {
		t.Fatalf("error = %v, want api_base_url context", err)
	}
}
