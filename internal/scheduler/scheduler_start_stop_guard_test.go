package scheduler

import (
	"context"
	"testing"
)

// TestSchedulerStartAfterStopFailsLoudly pins the fix to scheduler.go's
// Start/Stop: a second Start() call after Stop() used to
// return nil while doing nothing functional. s.started is set true by the
// first Start() and Stop() never resets it, so a caller who (mistakenly)
// tries to restart a stopped scheduler on the same instance got no
// indication anything was wrong: no error, no poller goroutines doing real
// work (they exit immediately on the already-closed stopChan), and two new
// tickers created with nothing left to stop them (Stop() runs stopTickers()
// exactly once per process today, so a second Start() call's tickers are
// never reached by an already-completed Stop()).
//
// Reachability: main.go's only call sites (runSchedulerUntilSignal,
// TestRunSchedulerUntilSignal* subprocess paths) call Start() and Stop()
// each exactly once per process, so this is a defensive guard for a path
// with no production caller today -- confirmed by grepping every Start(
// call in the repository outside _test.go files. This test drives Start()
// directly a second time, which is not itself a production entry point;
// see the report for why that is the right way to cover a guard against an
// unreachable input.
func TestSchedulerStartAfterStopFailsLoudly(t *testing.T) {
	s := newTestScheduler(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() first call error = %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if err := s.Start(ctx); err == nil {
		t.Fatal("Start() after Stop() = nil error, want a loud failure instead of a silent no-op restart")
	}
}
