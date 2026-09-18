package textutil

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// This file pins the performance fix on redactURLErrorSubstringFragments
// (found 2026-09-04, performance review): the loop was O(len(secret) *
// len(detail)^2), cubic in the combined input size whenever detail scales
// with secret, as net/url's colonPort detail does for the shape below (see
// F1 in userinfo_ambiguous_authority_test.go). Measured before this fix: a
// secret of 264/514/1014/2014 bytes took roughly 14ms/100ms/790ms/7.5s.
// Fixed with a work-budget guard on redactURLErrorSubstringFragments -- see
// its own doc comment in textutil.go for the exact bound and why a
// length-only cap was rejected (it broke
// TestRedactURLErrorSubstringFragmentsHandlesLongSecretShortDetail, an
// existing case that is long but cheap and must stay byte-for-byte
// unmodified).

// TestRedactURLErrorSubstringFragmentsComplexityBounded pins the fix itself:
// this must never again take more than a couple of seconds, regardless of
// input length. n=5000 (secret length ~5015) is chosen because the
// pre-fix algorithm would need on the order of two minutes there (cubic
// growth from the measured 7.5s at length 2014), so a regression trips the
// 2-second bound quickly rather than the test hanging for minutes; the fixed
// algorithm's actual cost at this size is low single-digit milliseconds
// (measured 2026-09-04), so 2 seconds is generous by roughly three orders of
// magnitude for the fix and undershoots the pre-fix cost by roughly the
// same margin -- a bound that cannot be met by accident either way. The
// call runs in a goroutine with a select/timeout rather than a plain
// time.Since assertion so a regression fails in ~2s instead of only being
// reported after the full slow call completes.
func TestRedactURLErrorSubstringFragmentsComplexityBounded(t *testing.T) {
	const n = 5000
	const bound = 2 * time.Second
	raw := "http://0:S@0" + strings.Repeat(":", n) + "00"

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = RedactWebhookSecretsForURL("x"+raw+"y", raw)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > bound {
			t.Fatalf("RedactWebhookSecretsForURL(len(secret)=%d) took %v, want under %v -- cubic blowup regression in redactURLErrorSubstringFragments?", len(raw), elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("RedactWebhookSecretsForURL(len(secret)=%d) did not complete within %v -- cubic blowup regression in redactURLErrorSubstringFragments?", len(raw), bound)
	}
}

// TestRedactURLErrorSubstringFragmentsReportedReproIsNowFast is the exact
// reported reproduction (2026-09-04 performance finding), pinned directly:
// used to take ~7.5s on this hardware; must now return near-instantly.
func TestRedactURLErrorSubstringFragmentsReportedReproIsNowFast(t *testing.T) {
	const bound = 2 * time.Second
	raw := "http://0:S@0" + strings.Repeat(":", 2000) + "00"

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = RedactWebhookSecretsForURL("x"+raw+"y", raw)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > bound {
			t.Fatalf("reported repro took %v, want under %v", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("reported repro did not complete within %v", bound)
	}
}

// TestRedactURLErrorSubstringFragmentsBudgetFallbackDoesNotLeak proves the
// work-budget guard degrades safely, not silently: past the budget this
// function no longer runs the exact per-fragment search, but it must never
// fall back to "leave text alone" either, since detail can still carry a raw
// slice of secret (this is F1's own leak channel -- see
// userinfo_ambiguous_authority_test.go). An earlier version of this fix used
// a plain length-only cap that DID leak here (secret verbatim in the
// output) before this test caught it; the shipped fix instead redacts the
// whole detail substring wholesale once the budget is exceeded. Uses a real
// url.Parse failure (not a fabricated *url.Error), the same
// unescaped-'/'-in-userinfo shape F1's other tests use, scaled well past the
// work budget.
func TestRedactURLErrorSubstringFragmentsBudgetFallbackDoesNotLeak(t *testing.T) {
	for _, passwordLen := range []int{490, 512, 600, 2000, 20000} {
		password := strings.Repeat("P", passwordLen)
		raw := "https://svc:" + password + "/tail@host.example.test/rss"

		_, parseErr := url.Parse(raw)
		if parseErr == nil {
			t.Fatalf("precondition: url.Parse(%d-byte password) should fail", passwordLen)
		}

		got := RedactWebhookSecretsForURL(parseErr.Error(), raw)

		if strings.Contains(got, password) {
			t.Fatalf("LEAK at password length %d: %s", passwordLen, got)
		}
	}
}

// TestRedactURLErrorSubstringFragmentsBudgetFallbackStillRedactsRealisticFragment
// checks the fallback path's own redaction on a small, realistic example:
// once secret is long enough to exceed the work budget, the previously
// exact per-candidate match is replaced by a wholesale redaction of the
// entire inner detail -- confirm that still happens (not just "no leak",
// but "detail's dynamic content is gone from the output").
func TestRedactURLErrorSubstringFragmentsBudgetFallbackStillRedactsRealisticFragment(t *testing.T) {
	err := &url.Error{Op: "parse", URL: "x", Err: errStr(`invalid port ":` + strings.Repeat("A", 600) + `" after host`)}
	text := `parse "x": invalid port ":` + strings.Repeat("A", 600) + `" after host`
	secret := "https://svc:" + strings.Repeat("A", 600) + "@host.example.test/rss"

	got := redactURLErrorSubstringFragments(text, err, secret)

	if strings.Contains(got, strings.Repeat("A", 600)) {
		t.Fatalf("fallback did not redact the fragment: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("fallback produced no redaction marker at all: %s", got)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

// TestRedactURLErrorSubstringFragmentsVeryLongDetailShortCircuitsBeforeSquaring
// exercises the maxFragmentDetailLenForBudgetCheck guard directly: detail
// long enough on its own to make len(detail)^2 already exceed the work
// budget (regardless of secret's length) must take the fallback without
// ever computing secret*detail*detail, so this also guards against an
// int64 overflow in that product for a sufficiently large detail.
func TestRedactURLErrorSubstringFragmentsVeryLongDetailShortCircuitsBeforeSquaring(t *testing.T) {
	const bound = 2 * time.Second
	raw := "http://0:S@0" + strings.Repeat(":", 70000) + "00"

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = RedactWebhookSecretsForURL("x"+raw+"y", raw)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > bound {
			t.Fatalf("took %v, want under %v", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("did not complete within %v", bound)
	}
}
