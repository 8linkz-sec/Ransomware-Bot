package feedurl_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
)

// twoStepResolver returns a public, allowed address on its first call and a
// blocked loopback address on every call after that -- simulating a DNS
// answer that changes between two independent resolutions of the same host
// (a rebinding attack).
type twoStepResolver struct{ calls int32 }

func (r *twoStepResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	if atomic.AddInt32(&r.calls, 1) == 1 {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil // public, passes
	}
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil // rebound to loopback
}

// TestRedirectHookTOCTOUIsClosedByGuardedDialer pins the finding-1 verdict:
// CheckRedirect's own resolution is discarded (a real TOCTOU window in
// isolation), but GuardedDialer.DialContext performs its OWN, independent
// resolution and dials the literal resolved IP on every hop, so a stale or
// rebound CheckRedirect answer can never reach the network. A future change
// that made the dialer trust a cached/passed-through resolution instead of
// re-resolving for itself would be caught by this test.
func TestRedirectHookTOCTOUIsClosedByGuardedDialer(t *testing.T) {
	orig := feedurl.DefaultResolver
	r := &twoStepResolver{}
	feedurl.DefaultResolver = r
	defer func() { feedurl.DefaultResolver = orig }()

	ctx := t.Context()

	// Step 1: exactly what CheckRedirect does -- validate with a nil resolver,
	// which falls back to DefaultResolver. This is the first (safe) call.
	if err := feedurl.ValidateForFetch(ctx, "https://example.com/feed", nil, feedurl.Options{}); err != nil {
		t.Fatalf("redirect-hook validation unexpectedly failed on the first (safe) resolution: %v", err)
	}

	// Step 2: what actually happens next -- the transport dials the approved
	// URL. GuardedDialer resolves independently (its own Resolver field is
	// also nil -> DefaultResolver), so this is the SECOND resolver call and
	// must observe the rebound address.
	dialer := feedurl.GuardedDialer{Options: feedurl.Options{}}
	_, err := dialer.DialContext(ctx, "tcp", "example.com:443")
	if err == nil {
		t.Fatal("expected the dial-time re-resolution to independently block the rebound address, got nil error")
	}
}
