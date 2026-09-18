package retrypolicy

import (
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/httpstatus"
)

const (
	MaxAttempts        = 3
	BaseDelay          = time.Second
	MaxBackoffDelay    = 30 * time.Second
	MaxRetryAfterDelay = 60 * time.Second
	jitterPercent      = 20

	// maxRetryAfterSeconds is MaxRetryAfterDelay expressed in whole seconds.
	// Retry-After second values are clamped to it BEFORE the multiplication so
	// the conversion to time.Duration can never overflow int64 nanoseconds.
	maxRetryAfterSeconds = int(MaxRetryAfterDelay / time.Second)
)

var (
	jitterRandMu sync.Mutex
	jitterRand   = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // G404: jitter for retry backoff, not security-sensitive
)

func Delay(attempt int) time.Duration {
	return DelayWithBase(attempt, BaseDelay)
}

func DelayWithBase(attempt int, baseDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return 0
	}
	minDelay, maxDelay := delayBounds(attempt, baseDelay)
	if minDelay <= 0 || maxDelay <= minDelay {
		return minDelay
	}
	spread := maxDelay - minDelay
	jitterRandMu.Lock()
	offset := time.Duration(jitterRand.Int63n(int64(spread) + 1))
	jitterRandMu.Unlock()
	return CapBackoffDelay(minDelay + offset)
}

func delayBounds(attempt int, baseDelay time.Duration) (time.Duration, time.Duration) {
	delay := exponentialDelay(attempt, baseDelay)
	if delay <= 0 {
		return 0, 0
	}
	jitter := delay * jitterPercent / 100
	return delay - jitter, delay + jitter
}

func exponentialDelay(attempt int, baseDelay time.Duration) time.Duration {
	if attempt <= 0 || baseDelay <= 0 {
		return 0
	}
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		if delay >= MaxBackoffDelay/2 {
			return MaxBackoffDelay
		}
		delay *= 2
	}
	if delay > MaxBackoffDelay {
		return MaxBackoffDelay
	}
	return delay
}

func CapBackoffDelay(delay time.Duration) time.Duration {
	if delay > MaxBackoffDelay {
		return MaxBackoffDelay
	}
	return delay
}

func IsRetryableStatus(statusCode int) bool {
	return httpstatus.IsRetryable(statusCode)
}

func RetryAfterDelay(header string, now time.Time) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds < 0 {
			return 0, false
		}
		if seconds > maxRetryAfterSeconds {
			seconds = maxRetryAfterSeconds
		}
		return CapDelay(time.Duration(seconds) * time.Second), true
	}

	retryAt, err := http.ParseTime(header)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return CapDelay(delay), true
}

func CapDelay(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	if delay > MaxRetryAfterDelay {
		return MaxRetryAfterDelay
	}
	return delay
}

func IsRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || isTransientDNSError(err) || isTransientSyscallError(err)
	}

	errStr := strings.ToLower(err.Error())
	// A refused connection is a definite answer from the peer, not a transient
	// condition: isTransientSyscallError deliberately omits ECONNREFUSED, and the
	// string fallback must not contradict it. Checked before the "connection"
	// substring, which every refused-dial message also contains. The substring is
	// deliberately broad (a proxy refusal counts too); the only theoretical loss
	// is an untyped error that says both "refused" and something transient.
	if strings.Contains(errStr, "refused") {
		return false
	}
	return strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "reset") ||
		strings.Contains(errStr, "unavailable")
}

// isTransientDNSError reports resolver failures the resolver itself marks as
// temporary (for example SERVFAIL), which the deprecated net.Error.Temporary
// used to cover. Permanent lookup failures (NXDOMAIN) stay non-retryable.
func isTransientDNSError(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsTemporary
}

// isTransientSyscallError covers the socket-level conditions that used to be
// reported through net.Error.Temporary: interrupted calls, descriptor
// exhaustion, and connections reset or aborted by the peer.
func isTransientSyscallError(err error) bool {
	for _, errno := range []syscall.Errno{syscall.EINTR, syscall.EMFILE, syscall.ENFILE, syscall.ECONNRESET, syscall.ECONNABORTED} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
