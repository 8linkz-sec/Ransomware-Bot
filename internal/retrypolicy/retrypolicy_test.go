package retrypolicy

import (
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestDelay(t *testing.T) {
	if got := Delay(0); got != 0 {
		t.Fatalf("Delay(0) = %v, want 0", got)
	}
	minDelay, maxDelay := delayBounds(2, BaseDelay)
	for i := 0; i < 20; i++ {
		if got := Delay(2); got < minDelay || got > maxDelay {
			t.Fatalf("Delay(2) = %v, want within %v-%v", got, minDelay, maxDelay)
		}
	}
}

func TestExponentialDelay(t *testing.T) {
	if got := exponentialDelay(1, BaseDelay); got != BaseDelay {
		t.Fatalf("exponentialDelay(1) = %v, want %v", got, BaseDelay)
	}
	if got := exponentialDelay(3, BaseDelay); got != 4*BaseDelay {
		t.Fatalf("exponentialDelay(3) = %v, want %v", got, 4*BaseDelay)
	}
	if got := exponentialDelay(100, BaseDelay); got != MaxBackoffDelay {
		t.Fatalf("exponentialDelay(100) = %v, want cap %v", got, MaxBackoffDelay)
	}
}

func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	retryAt := now.Add(2 * time.Second).Format(http.TimeFormat)

	tests := []struct {
		name   string
		header string
		want   time.Duration
		ok     bool
	}{
		{name: "seconds", header: "5", want: 5 * time.Second, ok: true},
		{name: "seconds capped", header: "120", want: MaxRetryAfterDelay, ok: true},
		{name: "seconds at overflow boundary", header: "9223372036", want: MaxRetryAfterDelay, ok: true},
		{name: "seconds overflowing int64 ns", header: "9223372037", want: MaxRetryAfterDelay, ok: true},
		{name: "seconds wrapping to a positive value", header: "18446744074", want: MaxRetryAfterDelay, ok: true},
		{name: "seconds max int64", header: "9223372036854775807", want: MaxRetryAfterDelay, ok: true},
		{name: "seconds beyond int64", header: "9223372036854775808", want: 0, ok: false},
		{name: "zero seconds", header: "0", want: 0, ok: true},
		{name: "far future http date", header: now.AddDate(999, 0, 0).Format(http.TimeFormat), want: MaxRetryAfterDelay, ok: true},
		{name: "http date", header: retryAt, want: 2 * time.Second, ok: true},
		{name: "past date", header: now.Add(-time.Second).Format(http.TimeFormat), want: 0, ok: true},
		{name: "negative seconds", header: "-1", ok: false},
		{name: "invalid", header: "soon", ok: false},
		{name: "empty", header: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RetryAfterDelay(tt.header, now)
			if ok != tt.ok {
				t.Fatalf("RetryAfterDelay() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("RetryAfterDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRetryAfterDelayIsAlwaysWithinCap pins the property the overflow bug broke:
// a parsed Retry-After is never negative and never above the documented cap, and
// every representable all-digit header of at least MaxRetryAfterDelay seconds
// yields exactly the cap. Digit strings outside int64 are not parseable at all
// and only have to satisfy the weaker !ok clause.
func TestRetryAfterDelayIsAlwaysWithinCap(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	capSeconds := int64(MaxRetryAfterDelay / time.Second)

	headers := []string{
		"0",
		"1",
		"60",
		"61",
		"9223372036",
		"9223372037",
		"18446744074",
		"19223372037",
		"9223372036854775807",
		now.Add(-time.Hour).Format(http.TimeFormat),
		now.AddDate(999, 0, 0).Format(http.TimeFormat),
	}

	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			delay, ok := RetryAfterDelay(header, now)
			if !ok {
				if delay != 0 {
					t.Fatalf("RetryAfterDelay(%q) = %v with ok=false, want 0", header, delay)
				}
				return
			}
			if delay < 0 || delay > MaxRetryAfterDelay {
				t.Fatalf("RetryAfterDelay(%q) = %v, want within 0-%v", header, delay, MaxRetryAfterDelay)
			}

			seconds, err := strconv.ParseInt(header, 10, 64)
			if err != nil {
				return
			}
			if seconds >= capSeconds && delay != MaxRetryAfterDelay {
				t.Fatalf("RetryAfterDelay(%q) = %v, want the cap %v", header, delay, MaxRetryAfterDelay)
			}
		})
	}
}

func TestIsRetryableNetworkError(t *testing.T) {
	if !IsRetryableNetworkError(errors.New("connection reset by peer")) {
		t.Fatal("connection reset should be retryable")
	}
	if IsRetryableNetworkError(errors.New("invalid API key")) {
		t.Fatal("invalid API key should not be retryable")
	}
}

func TestIsRetryableNetworkErrorNil(t *testing.T) {
	if IsRetryableNetworkError(nil) {
		t.Fatal("IsRetryableNetworkError(nil) = true, want false")
	}
}

type fakeNetError struct {
	timeout   bool
	temporary bool
}

func (e fakeNetError) Error() string   { return "fake net error" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return e.temporary }

func TestIsRetryableNetworkErrorNetError(t *testing.T) {
	var _ net.Error = fakeNetError{}

	if !IsRetryableNetworkError(fakeNetError{timeout: true}) {
		t.Fatal("net.Error with Timeout() = true should be retryable")
	}
	if IsRetryableNetworkError(fakeNetError{temporary: true}) {
		t.Fatal("deprecated net.Error.Temporary() alone must not make an error retryable")
	}
	if !IsRetryableNetworkError(&net.DNSError{Err: "server misbehaving", Name: "example.invalid", IsTemporary: true}) {
		t.Fatal("temporary DNS failure should be retryable")
	}
	if IsRetryableNetworkError(&net.DNSError{Err: "no such host", Name: "example.invalid", IsNotFound: true}) {
		t.Fatal("permanent DNS failure should not be retryable")
	}
	if !IsRetryableNetworkError(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}) {
		t.Fatal("connection reset by peer should be retryable")
	}
	if IsRetryableNetworkError(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}) {
		t.Fatal("connection refused should not be retryable")
	}
	if IsRetryableNetworkError(fakeNetError{}) {
		t.Fatal("net.Error without timeout/temporary should not be retryable")
	}
}

func TestIsRetryableNetworkErrorStringFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "refused", err: errors.New("connection refused"), want: false},
		{name: "refused dial", err: errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), want: false},
		{name: "refused windows wording", err: errors.New("No connection could be made because the target machine actively refused it."), want: false},
		// Pins that the exclusion is matched against the lower-cased message, like
		// the substrings below it; without the fold this row falls through to the
		// "connection" substring and comes back retryable.
		{name: "refused mixed case", err: errors.New("Connection REFUSED by remote host"), want: false},
		{name: "reset by peer", err: errors.New("connection reset by peer"), want: true},
		{name: "service unavailable", err: errors.New("service unavailable"), want: true},
		{name: "invalid API key", err: errors.New("invalid API key"), want: false},
		{name: "no such host", err: errors.New("dial tcp: lookup x: no such host"), want: false},
		{name: "certificate", err: errors.New("x509: certificate signed by unknown authority"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableNetworkError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableNetworkError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsRetryableNetworkErrorStructuredWinsOverString documents why the string
// fallback is unreachable from the only production call site: http.Client.Do
// always returns a *url.Error, which satisfies net.Error, so the structured
// branch decides before any text is inspected.
func TestIsRetryableNetworkErrorStructuredWinsOverString(t *testing.T) {
	urlErr := &url.Error{Op: "Get", URL: "https://x", Err: errors.New("connection refused")}
	var _ net.Error = urlErr

	if IsRetryableNetworkError(urlErr) {
		t.Fatal("*url.Error wrapping a refused connection should not be retryable")
	}
	if IsRetryableNetworkError(errors.New("connection refused")) {
		t.Fatal("untyped refused connection should agree with the structured path")
	}
	if IsRetryableNetworkError(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}) {
		t.Fatal("ECONNREFUSED should agree with the structured path")
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
	for _, statusCode := range retryable {
		if !IsRetryableStatus(statusCode) {
			t.Fatalf("IsRetryableStatus(%d) = false, want true", statusCode)
		}
	}

	nonRetryable := []int{http.StatusOK, http.StatusBadRequest, http.StatusNotFound}
	for _, statusCode := range nonRetryable {
		if IsRetryableStatus(statusCode) {
			t.Fatalf("IsRetryableStatus(%d) = true, want false", statusCode)
		}
	}
}

func TestDelayWithBaseEdgeCases(t *testing.T) {
	if got := DelayWithBase(0, BaseDelay); got != 0 {
		t.Fatalf("DelayWithBase(0, base) = %v, want 0", got)
	}
	// Non-positive base delay yields zero bounds and therefore no delay.
	if got := DelayWithBase(1, 0); got != 0 {
		t.Fatalf("DelayWithBase(1, 0) = %v, want 0", got)
	}
	// A base delay too small to produce jitter returns the lower bound as-is.
	if got := DelayWithBase(1, time.Nanosecond); got != time.Nanosecond {
		t.Fatalf("DelayWithBase(1, 1ns) = %v, want 1ns", got)
	}
	// An oversized base delay is capped, and jittered results stay within bounds.
	minDelay, maxDelay := delayBounds(1, 2*MaxBackoffDelay)
	for i := 0; i < 20; i++ {
		got := DelayWithBase(1, 2*MaxBackoffDelay)
		if got < minDelay || got > maxDelay || got > MaxBackoffDelay {
			t.Fatalf("DelayWithBase(1, 2*cap) = %v, want within %v-%v and <= %v",
				got, minDelay, maxDelay, MaxBackoffDelay)
		}
	}
}

func TestExponentialDelayEdgeCases(t *testing.T) {
	if got := exponentialDelay(0, BaseDelay); got != 0 {
		t.Fatalf("exponentialDelay(0, base) = %v, want 0", got)
	}
	if got := exponentialDelay(1, 0); got != 0 {
		t.Fatalf("exponentialDelay(1, 0) = %v, want 0", got)
	}
	// A base above the cap is clamped even for the first attempt.
	if got := exponentialDelay(1, MaxBackoffDelay+time.Second); got != MaxBackoffDelay {
		t.Fatalf("exponentialDelay(1, cap+1s) = %v, want %v", got, MaxBackoffDelay)
	}
}

func TestCapBackoffDelay(t *testing.T) {
	if got := CapBackoffDelay(MaxBackoffDelay + time.Second); got != MaxBackoffDelay {
		t.Fatalf("CapBackoffDelay(cap+1s) = %v, want %v", got, MaxBackoffDelay)
	}
	if got := CapBackoffDelay(time.Second); got != time.Second {
		t.Fatalf("CapBackoffDelay(1s) = %v, want 1s", got)
	}
}

func TestCapDelay(t *testing.T) {
	tests := []struct {
		name  string
		delay time.Duration
		want  time.Duration
	}{
		// Keep this row: after the seconds clamp it is one of only two places in
		// the suite that still reaches CapDelay's upper bound.
		{name: "above cap", delay: MaxRetryAfterDelay + time.Second, want: MaxRetryAfterDelay},
		{name: "below cap", delay: time.Second, want: time.Second},
		{name: "zero", delay: 0, want: 0},
		{name: "negative", delay: -time.Nanosecond, want: 0},
		{name: "min int64", delay: time.Duration(math.MinInt64), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CapDelay(tt.delay); got != tt.want {
				t.Fatalf("CapDelay(%v) = %v, want %v", tt.delay, got, tt.want)
			}
		})
	}
}
