package webhookhttp

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
)

const (
	RequestTimeout                 = 10 * time.Second
	transportMaxIdleConns          = 20
	transportMaxIdleConnsPerHost   = 4
	transportIdleConnTimeout       = 90 * time.Second
	transportTLSHandshakeTimeout   = 10 * time.Second
	transportResponseHeaderTimeout = 10 * time.Second
	transportExpectContinueTimeout = time.Second
	maxSameHostRedirects           = 10
)

type Policy struct {
	RequestTimeout time.Duration
	MaxAttempts    int
	RetryBaseDelay time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		RequestTimeout: RequestTimeout,
		MaxAttempts:    retrypolicy.MaxAttempts,
		RetryBaseDelay: retrypolicy.BaseDelay,
	}
}

func NormalizePolicy(policy Policy) Policy {
	defaults := DefaultPolicy()
	if policy.RequestTimeout <= 0 {
		policy.RequestTimeout = defaults.RequestTimeout
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}
	if policy.RetryBaseDelay < 0 {
		policy.RetryBaseDelay = defaults.RetryBaseDelay
	}
	return policy
}

func NewClient(policies ...Policy) *http.Client {
	policy := DefaultPolicy()
	if len(policies) > 0 {
		policy = NormalizePolicy(policies[0])
	}
	return &http.Client{
		Timeout:       policy.RequestTimeout,
		Transport:     NewTransport(),
		CheckRedirect: SameHostHTTPSRedirectPolicy,
	}
}

func NewTransport() http.RoundTripper {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// DefaultTransport was replaced by a custom RoundTripper; start from the stdlib defaults.
		baseTransport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport := baseTransport.Clone()
	transport.MaxIdleConns = transportMaxIdleConns
	transport.MaxIdleConnsPerHost = transportMaxIdleConnsPerHost
	transport.IdleConnTimeout = transportIdleConnTimeout
	transport.TLSHandshakeTimeout = transportTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = transportResponseHeaderTimeout
	transport.ExpectContinueTimeout = transportExpectContinueTimeout
	return transport
}

func SameHostHTTPSRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxSameHostRedirects {
		return fmt.Errorf("webhook redirect blocked after %d hops", len(via))
	}
	if req == nil || req.URL == nil {
		return fmt.Errorf("webhook redirect blocked: missing redirect URL")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("webhook redirect blocked: refusing redirect to %q", req.URL.Scheme)
	}
	if len(via) == 0 || via[0] == nil || via[0].URL == nil {
		return fmt.Errorf("webhook redirect blocked: missing original URL")
	}
	if !sameWebhookHost(req.URL, via[0].URL) {
		return fmt.Errorf("webhook redirect blocked: host changed")
	}
	return nil
}

func sameWebhookHost(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && normalizedWebhookPort(a) == normalizedWebhookPort(b)
}

func normalizedWebhookPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
