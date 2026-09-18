package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/i18n"
)

// --- Part (a): per-poller progress markers ---

// TestNewProgressMarkersReturnsNilWithoutTheReadinessMarkerMechanism pins the
// typed-nil guard: runSchedulerUntilSignal only registers a recorder when
// this returns non-nil, precisely because a typed nil satisfying the
// ProgressRecorder interface would panic on first use.
func TestNewProgressMarkersReturnsNilWithoutTheReadinessMarkerMechanism(t *testing.T) {
	t.Setenv(readinessFileEnv, "")
	if got := newProgressMarkers(); got != nil {
		t.Fatalf("newProgressMarkers() = %v, want nil when the readiness marker mechanism is unused", got)
	}
}

// TestNewProgressMarkersDerivesPathsFromTheReadinessMarker pins the naming
// convention: the two progress markers sit beside the readiness marker with
// fixed suffixes.
func TestNewProgressMarkersDerivesPathsFromTheReadinessMarker(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	t.Setenv(readinessFileEnv, base)

	got := newProgressMarkers()
	if got == nil {
		t.Fatal("newProgressMarkers() = nil, want a non-nil recorder")
	}
	if got.apiPath != base+progressMarkerSuffixAPI {
		t.Fatalf("apiPath = %q, want %q", got.apiPath, base+progressMarkerSuffixAPI)
	}
	if got.rssPath != base+progressMarkerSuffixRSS {
		t.Fatalf("rssPath = %q, want %q", got.rssPath, base+progressMarkerSuffixRSS)
	}
}

// TestReadRSSProgressMarkerFailsSafeWithoutTheReadinessMarkerMechanism and
// TestReadRSSProgressMarkerFailsSafeWhenTheMarkerIsMissing are compile pins:
// readRSSProgressMarker's two fail-safe branches (an unconfigured marker
// mechanism, and a marker that has not been written yet) must both come back
// "no verdict", the same fail-open contract every other marker read in this
// file has.
func TestReadRSSProgressMarkerFailsSafeWithoutTheReadinessMarkerMechanism(t *testing.T) {
	t.Setenv(readinessFileEnv, "")
	if _, ok := readRSSProgressMarker(); ok {
		t.Fatal("readRSSProgressMarker() ok = true without the readiness marker mechanism, want false")
	}
}

func TestReadRSSProgressMarkerFailsSafeWhenTheMarkerIsMissing(t *testing.T) {
	t.Setenv(readinessFileEnv, filepath.Join(t.TempDir(), "never-written.ready"))
	if _, ok := readRSSProgressMarker(); ok {
		t.Fatal("readRSSProgressMarker() ok = true with a missing marker, want false")
	}
}

// TestReadRSSProgressMarkerReturnsTheRecordedOverrunFlag is the behavioural
// pin for F1: the flag readRSSProgressMarker hands back must be exactly what
// RecordRSSPass wrote, in both directions, since evaluateRSSHealth's R0b gate
// now branches on it directly instead of waiting out a fixed grace period.
func TestReadRSSProgressMarkerReturnsTheRecordedOverrunFlag(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "overrun", content: "at=2026-01-01T00:00:00Z\nstale_after=1m\nbudget_overrun=true\n", want: true},
		{name: "completed", content: "at=2026-01-01T00:00:00Z\nstale_after=1m\nbudget_overrun=false\n", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
			t.Setenv(readinessFileEnv, base)
			if err := os.WriteFile(base+progressMarkerSuffixRSS, []byte(tc.content), 0600); err != nil {
				t.Fatalf("WriteFile(rss marker) error = %v", err)
			}
			progress, ok := readRSSProgressMarker()
			if !ok {
				t.Fatal("readRSSProgressMarker() ok = false, want true")
			}
			if progress.budgetOverrun != tc.want {
				t.Fatalf("budgetOverrun = %v, want %v", progress.budgetOverrun, tc.want)
			}
		})
	}
}

// TestWriteProgressMarkerFileReplacesAtomically pins that a second write fully
// replaces the file content (no partial/mixed content) and leaves no leftover
// temp file, mirroring internal/status/json_store.go's own pattern.
func TestWriteProgressMarkerFileReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.api")
	if err := writeProgressMarkerFile(path, "at=old\nstale_after=1m\n"); err != nil {
		t.Fatalf("writeProgressMarkerFile() first write error = %v", err)
	}
	if err := writeProgressMarkerFile(path, "at=new\nstale_after=2m\n"); err != nil {
		t.Fatalf("writeProgressMarkerFile() second write error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	if string(data) != "at=new\nstale_after=2m\n" {
		t.Fatalf("marker content = %q, want the fully-replaced content", data)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("leftover temp file %q after a successful write", entry.Name())
		}
	}
}

// TestProgressMarkersClearRemovesBothFiles is a direct unit test of Clear():
// the shutdown subprocess tests exercise it too, but only through a real
// process, so its own package's coverage tooling does not see it (F8/coverage
// re-measurement, 2026-09-04).
func TestProgressMarkersClearRemovesBothFiles(t *testing.T) {
	dir := t.TempDir()
	apiPath := filepath.Join(dir, "ready.api")
	rssPath := filepath.Join(dir, "ready.rss")
	for _, path := range []string{apiPath, rssPath} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	(&progressMarkers{apiPath: apiPath, rssPath: rssPath}).Clear()

	for _, path := range []string{apiPath, rssPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("marker %q still exists after Clear(), stat err = %v", path, err)
		}
	}
}

// TestProgressMarkersClearWarnsOnARemoveFailureWithoutPanicking covers Clear's
// one remaining branch: a target os.Remove cannot delete (a non-empty
// directory, the same fault-free technique writeProgressMarkerFile's own
// os.Rename test above uses) must log a WARN naming the marker and must not
// stop Clear from still attempting the other marker.
func TestProgressMarkersClearWarnsOnARemoveFailureWithoutPanicking(t *testing.T) {
	_, hook := captureDestinationLog(t)

	dir := t.TempDir()
	apiPath := filepath.Join(dir, "ready.api")
	if err := os.Mkdir(apiPath, 0700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(apiPath, "blocker"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	rssPath := filepath.Join(dir, "ready.rss")
	if err := os.WriteFile(rssPath, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile(rss marker) error = %v", err)
	}

	(&progressMarkers{apiPath: apiPath, rssPath: rssPath}).Clear()

	if findRootTestLogEntry(hook, "Failed to remove poller progress marker") == nil {
		t.Fatalf("missing remove-failure WARN; entries=%v", rootTestLogMessages(hook))
	}
	if _, err := os.Stat(rssPath); !os.IsNotExist(err) {
		t.Fatalf("rss marker still exists after Clear(), stat err = %v (the api failure must not stop the rss removal)", err)
	}
}

// TestWriteProgressMarkerFileErrorBranchesAreTestable is the F6 regression
// pin: TESTING.md's exception list claimed all five of
// writeProgressMarkerFile's error branches need ACL manipulation or a
// full/read-only filesystem, but two do not -- the same techniques
// TestApplyDestinationManifestWarnsWhenTheManifestCannotBeWritten already uses
// elsewhere in this package. A regular file where the parent directory should
// be reaches os.MkdirAll; a non-empty directory already at the target path
// reaches os.Rename (and leaves no leftover temp file behind).
func TestWriteProgressMarkerFileErrorBranchesAreTestable(t *testing.T) {
	t.Run("MkdirAll", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "dir-is-a-file")
		if err := os.WriteFile(base, []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err := writeProgressMarkerFile(filepath.Join(base, "ready.api"), "at=x\n")
		if err == nil {
			t.Fatal("writeProgressMarkerFile() error = nil, want the MkdirAll failure")
		}
		if !strings.Contains(err.Error(), "create progress marker directory") {
			t.Fatalf("error = %v, want the MkdirAll branch", err)
		}
	})

	t.Run("Rename", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "ready.api")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "blocker"), []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile(blocker) error = %v", err)
		}
		err := writeProgressMarkerFile(target, "at=x\n")
		if err == nil {
			t.Fatal("writeProgressMarkerFile() error = nil, want the Rename failure")
		}
		if !strings.Contains(err.Error(), "replace progress marker") {
			t.Fatalf("error = %v, want the Rename branch", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir() error = %v", err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".tmp") {
				t.Fatalf("temp file %q left behind after a failed rename", entry.Name())
			}
		}
	})
}

// TestWriteReadinessMarkerRoutesThroughAtomicMarkerWrite pins the fix:
// writeReadinessMarker used to write the marker directly with
// os.MkdirAll + os.WriteFile, so a process that died between those two
// steps -- disk full, an I/O error partway through the write -- could leave
// a truncated marker behind with no cleanup on the error branch. It now
// delegates to writeProgressMarkerFile, the same temp-file-plus-rename
// helper the per-poller progress markers already use, so a failed write can
// only ever leave (or fail to remove) a *temp* file beside the marker, never
// a partially-written marker at the real path. The two sub-tests below
// mirror TestWriteProgressMarkerFileErrorBranchesAreTestable's own MkdirAll
// and Rename failure techniques exactly, applied to writeReadinessMarker
// instead of writeProgressMarkerFile directly, and assert on the error text
// that only appears when the call actually routes through the shared
// helper -- a regression to a bespoke, non-atomic implementation would
// produce a different (or absent) error here even though it also fails.
//
// Literally reproducing "the process dies mid os.WriteFile, leaving
// truncated bytes at the real path" needs OS-level fault injection (a full
// disk, a killed process) that is not portably reproducible in this test
// harness; TestWriteProgressMarkerFileReplacesAtomically and this test's own
// "no leftover temp file" assertions are the closest portable stand-in, and
// TESTING.md records this as the same kind of accepted testing exception
// writeProgressMarkerFile's own error branches already carry.
func TestWriteReadinessMarkerRoutesThroughAtomicMarkerWrite(t *testing.T) {
	t.Run("MkdirAll failure wraps writeProgressMarkerFile's own error", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "dir-is-a-file")
		if err := os.WriteFile(base, []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		t.Setenv(readinessFileEnv, filepath.Join(base, "ransomware-bot.ready"))

		err := writeReadinessMarker()
		if err == nil {
			t.Fatal("writeReadinessMarker() error = nil, want the MkdirAll failure")
		}
		if !strings.Contains(err.Error(), "create progress marker directory") {
			t.Fatalf("error = %v, want it to route through writeProgressMarkerFile's MkdirAll branch", err)
		}
	})

	t.Run("Rename failure leaves no leftover temp file", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "ransomware-bot.ready")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "blocker"), []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile(blocker) error = %v", err)
		}
		t.Setenv(readinessFileEnv, target)

		err := writeReadinessMarker()
		if err == nil {
			t.Fatal("writeReadinessMarker() error = nil, want the Rename failure")
		}
		if !strings.Contains(err.Error(), "replace progress marker") {
			t.Fatalf("error = %v, want it to route through writeProgressMarkerFile's Rename branch", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir() error = %v", err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".tmp") {
				t.Fatalf("temp file %q left behind after a failed readiness marker write", entry.Name())
			}
		}
	})
}

// TestCheckPollerProgressFailsWhenAPollerIsWedged writes a readiness marker
// and a stale .rss progress marker, and asserts the error names the wedged
// poller, in both locales.
func TestCheckPollerProgressFailsWhenAPollerIsWedged(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "en", want: "the RSS poller has completed no poll pass"},
		{locale: "de", want: "der RSS-Poller hat seit"},
	}

	for _, tc := range tests {
		t.Run(tc.locale, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
			t.Setenv(readinessFileEnv, base)

			rssMarker := base + progressMarkerSuffixRSS
			if err := os.WriteFile(rssMarker, []byte("at=2020-01-01T00:00:00Z\nstale_after=1m\n"), 0600); err != nil {
				t.Fatalf("WriteFile(rss marker) error = %v", err)
			}

			cfg := config.DefaultConfig()
			err := checkPollerProgress(i18n.ForLocale(tc.locale), cfg, time.Now())
			if err == nil {
				t.Fatal("checkPollerProgress() error = nil, want the wedged RSS poller failure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkPollerProgress() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestCheckPollerProgressToleratesALoweredPollInterval is the regression
// guard for the outage v1 would have caused: a marker recorded under a large
// old allowance must stay green even though the live config now implies a
// much smaller one, until the poller has had a chance to rewrite the marker.
func TestCheckPollerProgressToleratesALoweredPollInterval(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	t.Setenv(readinessFileEnv, base)

	now := time.Now()
	recordedAt := now.Add(-5 * time.Hour)
	content := fmt.Sprintf("at=%s\nstale_after=21h0m0s\n", recordedAt.UTC().Format(time.RFC3339))
	for _, suffix := range []string{progressMarkerSuffixAPI, progressMarkerSuffixRSS} {
		if err := os.WriteFile(base+suffix, []byte(content), 0600); err != nil {
			t.Fatalf("WriteFile(marker) error = %v", err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.APIPollInterval = 10 * time.Minute
	cfg.RSSPollInterval = 10 * time.Minute

	if err := checkPollerProgress(i18n.Default(), cfg, now); err != nil {
		t.Fatalf("checkPollerProgress() error = %v, want nil (the recorded allowance must still cover a freshly lowered poll interval)", err)
	}
}

// TestCheckPollerProgressToleratesARaisedPollInterval is the mirror case: a
// marker recorded under a small allowance must stay green once the live
// config now implies a much larger one.
func TestCheckPollerProgressToleratesARaisedPollInterval(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	t.Setenv(readinessFileEnv, base)

	now := time.Now()
	recordedAt := now.Add(-5 * time.Minute)
	content := fmt.Sprintf("at=%s\nstale_after=9m0s\n", recordedAt.UTC().Format(time.RFC3339))
	for _, suffix := range []string{progressMarkerSuffixAPI, progressMarkerSuffixRSS} {
		if err := os.WriteFile(base+suffix, []byte(content), 0600); err != nil {
			t.Fatalf("WriteFile(marker) error = %v", err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.APIPollInterval = 6 * time.Hour
	cfg.RSSPollInterval = 6 * time.Hour

	if err := checkPollerProgress(i18n.Default(), cfg, now); err != nil {
		t.Fatalf("checkPollerProgress() error = %v, want nil (the config-derived allowance must cover a freshly raised poll interval)", err)
	}
}

// TestCheckPollerProgressFailsOpenOnAMalformedMarker covers every malformed
// shape: a missing file, garbage content, a missing stale_after, an
// unparseable at, an unparseable stale_after, and an unparseable
// budget_overrun. All must yield no verdict at all.
func TestCheckPollerProgressFailsOpenOnAMalformedMarker(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	t.Setenv(readinessFileEnv, base)
	cfg := config.DefaultConfig()

	tests := []struct {
		name    string
		content string
		write   bool
	}{
		{name: "missing file", write: false},
		{name: "garbage content", content: "garbage", write: true},
		{name: "missing stale_after", content: "at=2026-01-01T00:00:00Z\n", write: true},
		{name: "unparseable stale_after", content: "at=2026-01-01T00:00:00Z\nstale_after=not-a-duration\n", write: true},
		{name: "unparseable at", content: "at=not-a-time\nstale_after=1m\n", write: true},
		{name: "unparseable budget_overrun", content: "at=2026-01-01T00:00:00Z\nstale_after=1m\nbudget_overrun=not-a-bool\n", write: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apiMarker := base + progressMarkerSuffixAPI
			rssMarker := base + progressMarkerSuffixRSS
			_ = os.Remove(apiMarker)
			_ = os.Remove(rssMarker)
			if tc.write {
				if err := os.WriteFile(apiMarker, []byte(tc.content), 0600); err != nil {
					t.Fatalf("WriteFile(marker) error = %v", err)
				}
			}
			if err := checkPollerProgress(i18n.Default(), cfg, time.Now()); err != nil {
				t.Fatalf("checkPollerProgress() error = %v, want nil (a malformed marker must fail open)", err)
			}
		})
	}
}

// TestRunHealthcheckFailsOnAWedgedPoller is the behavioural pin for Part (a)
// (F8: every other Part (a) test is a compile pin against a struct/interface
// that did not exist before this feature, so this is the one that reaches the
// wedge detection through the real, pre-existing --healthcheck entrypoint),
// mirroring TestRunHealthcheckFailsOnDeadLetterItems: exit 1 through the CLI,
// and no path, URL or host reaches stdout.
func TestRunHealthcheckFailsOnAWedgedPoller(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "en", want: "API poller has completed no poll pass"},
		{locale: "de", want: "API-Poller hat seit"},
	}

	for _, tc := range tests {
		t.Run(tc.locale, func(t *testing.T) {
			configDir := writeMainTestConfig(t, `{}`)
			dataDir := filepath.Join(t.TempDir(), "data")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatalf("MkdirAll(dataDir) error = %v", err)
			}
			markerPath := filepath.Join(t.TempDir(), "ransomware-bot.ready")
			if err := os.WriteFile(markerPath, []byte("marker"), 0600); err != nil {
				t.Fatalf("WriteFile(marker) error = %v", err)
			}
			apiMarker := markerPath + progressMarkerSuffixAPI
			if err := os.WriteFile(apiMarker, []byte("at=2020-01-01T00:00:00Z\nstale_after=1m\n"), 0600); err != nil {
				t.Fatalf("WriteFile(api marker) error = %v", err)
			}
			t.Setenv(readinessFileEnv, markerPath)

			var out bytes.Buffer
			code := runCLI([]string{"--healthcheck", "--config-dir", configDir, "--data-dir", dataDir, "--locale", tc.locale}, &out, io.Discard)
			if code != 1 {
				t.Fatalf("runCLI(--healthcheck) = %d, want 1; out=%q", code, out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("out = %q, want it to contain %q", out.String(), tc.want)
			}
			for _, leak := range []string{dataDir, configDir, "://"} {
				if strings.Contains(out.String(), leak) {
					t.Fatalf("out = %q, wants no path, URL or host (found %q)", out.String(), leak)
				}
			}
		})
	}
}

// --- Part (b): the empty-data-directory case ---

// TestRunHealthcheckFailsWhenRSSNeverPolledAfterStartup is the literal
// reproduction: RSS enabled, no rss_status.json, and the RSS progress marker
// itself shows the last completed pass did not overrun its budget -- so the
// permanently empty state can only be a data_dir the bot cannot write to.
func TestRunHealthcheckFailsWhenRSSNeverPolledAfterStartup(t *testing.T) {
	configDir := writeRSSHealthcheckConfig(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}

	markerPath := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	if err := os.WriteFile(markerPath, []byte("marker"), 0600); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	rssMarker := markerPath + progressMarkerSuffixRSS
	// "at" must be recent: checkPollerProgress reads this same marker for
	// wedge detection and runs before evaluateRSSHealth, so a stale "at"
	// would report the poller wedged instead of exercising R0b.
	content := fmt.Sprintf("at=%s\nstale_after=40m0s\nbudget_overrun=false\n", time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(rssMarker, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(rss marker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, markerPath)

	err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default())
	if err == nil {
		t.Fatal("runHealthcheckWithMessages() error = nil, want the RSS-never-polled failure")
	}
	if !strings.Contains(err.Error(), "no poll attempt has been recorded") {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the RSS-never-polled message", err)
	}
	if !strings.Contains(err.Error(), "2 feed(s) configured") {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the configured feed count", err)
	}
}

// TestRunHealthcheckReportsRSSBudgetOverrunDistinctlyFromDataDirUnwritable is
// F1's regression pin: when the RSS progress marker shows the last completed
// pass ended in a cycle-budget overrun, the report must name rss_check_timeout
// and rss_worker_timeout, never the data_dir wording -- and, unlike the old
// design, this holds on every cycle, not only the one right after startup.
func TestRunHealthcheckReportsRSSBudgetOverrunDistinctlyFromDataDirUnwritable(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "en", want: "raise rss_check_timeout above rss_worker_timeout"},
		{locale: "de", want: "rss_check_timeout ueber rss_worker_timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.locale, func(t *testing.T) {
			configDir := writeRSSHealthcheckConfig(t)
			dataDir := filepath.Join(t.TempDir(), "data")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatalf("MkdirAll(dataDir) error = %v", err)
			}

			markerPath := filepath.Join(t.TempDir(), "ransomware-bot.ready")
			if err := os.WriteFile(markerPath, []byte("marker"), 0600); err != nil {
				t.Fatalf("WriteFile(marker) error = %v", err)
			}
			rssMarker := markerPath + progressMarkerSuffixRSS
			// "at" must be recent for the same reason as the sibling test
			// above: checkPollerProgress runs first and reads this marker.
			content := fmt.Sprintf("at=%s\nstale_after=40m0s\nbudget_overrun=true\n", time.Now().UTC().Format(time.RFC3339))
			if err := os.WriteFile(rssMarker, []byte(content), 0600); err != nil {
				t.Fatalf("WriteFile(rss marker) error = %v", err)
			}
			t.Setenv(readinessFileEnv, markerPath)

			err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.ForLocale(tc.locale))
			if err == nil {
				t.Fatal("runHealthcheckWithMessages() error = nil, want the RSS budget-overrun failure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runHealthcheckWithMessages() error = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "data_dir volume mount") || strings.Contains(err.Error(), "Mount und Schreibrechte") {
				t.Fatalf("runHealthcheckWithMessages() error = %v, want the data_dir wording absent", err)
			}
		})
	}
}

// TestRunHealthcheckStaysHealthyOnAFreshStart is the first-start guard: same
// setup, but the marker is fresh (just written), so the empty rss_status.json
// must not be reported as unhealthy yet.
func TestRunHealthcheckStaysHealthyOnAFreshStart(t *testing.T) {
	configDir := writeRSSHealthcheckConfig(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	markerPath := filepath.Join(t.TempDir(), "ransomware-bot.ready")
	if err := os.WriteFile(markerPath, []byte("marker"), 0600); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	t.Setenv(readinessFileEnv, markerPath)

	if err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default()); err != nil {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want nil on a fresh start", err)
	}
}

// TestRunHealthcheckStaysHealthyWithoutTheReadinessMarkerMechanism asserts
// that Part (b)'s new gate never fires when the marker mechanism is unused
// (RANSOMWARE_BOT_READY_FILE unset), which is the state every non-container
// local run is in.
func TestRunHealthcheckStaysHealthyWithoutTheReadinessMarkerMechanism(t *testing.T) {
	configDir := writeRSSHealthcheckConfig(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	t.Setenv(readinessFileEnv, "")

	if err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default()); err != nil {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want nil when the readiness marker mechanism is unused", err)
	}
}

// TestRunHealthcheckReportsAMissingMarkerBeforeTheRSSVerdict is the
// precedence pin: a missing marker must report as missing, never mask itself
// behind the new RSS-never-polled verdict.
func TestRunHealthcheckReportsAMissingMarkerBeforeTheRSSVerdict(t *testing.T) {
	configDir := writeRSSHealthcheckConfig(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir) error = %v", err)
	}
	markerPath := filepath.Join(t.TempDir(), "never-written.ready")
	t.Setenv(readinessFileEnv, markerPath)

	err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default())
	if err == nil {
		t.Fatal("runHealthcheckWithMessages() error = nil, want the missing readiness marker failure")
	}
	if !strings.Contains(err.Error(), "readiness marker") {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the readiness-marker message", err)
	}
	if strings.Contains(err.Error(), "no poll attempt has been recorded") {
		t.Fatalf("runHealthcheckWithMessages() error = %v, want the RSS-never-polled message absent", err)
	}
}

// --- Part (c): --dry-run exit code ---

// TestDryRunSubprocessExitsOneOnAPIFetchFailure is the behavioural pin,
// mirroring TestDryRunSubprocessExitsZeroWithoutPersistingTrackers: a genuine
// API fetch failure with a ransomware webhook enabled must exit 1.
func TestDryRunSubprocessExitsOneOnAPIFetchFailure(t *testing.T) {
	if os.Getenv("RANSOMWARE_BOT_DRY_RUN_API_FAILURE_HELPER") == "1" {
		os.Args = []string{
			"ransomware-bot",
			"--dry-run",
			"--config-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_CONFIG_DIR"),
			"--data-dir",
			os.Getenv("RANSOMWARE_BOT_TEST_DATA_DIR"),
		}
		main()
		return
	}

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	logFilePath := filepath.Join(tmpDir, "logs", "bot.log")
	configDir := writeMainTestConfig(t, fmt.Sprintf(`{
		"data_dir": %q,
		"log_file_path": %q,
		"api_key": "test-key",
		"api_base_url": %q,
		"discord_webhooks": {
			"ransomware": {
				"enabled": true,
				"url": "https://discord.com/api/webhooks/123/abc"
			}
		}
	}`, dataDir, logFilePath, apiServer.URL))

	output, err := runMainSubprocess(t, "TestDryRunSubprocessExitsOneOnAPIFetchFailure", map[string]string{
		"RANSOMWARE_BOT_DRY_RUN_API_FAILURE_HELPER": "1",
		"RANSOMWARE_BOT_TEST_CONFIG_DIR":            configDir,
		"RANSOMWARE_BOT_TEST_DATA_DIR":              dataDir,
	})
	if err == nil {
		t.Fatalf("dry-run helper exited 0, want 1; output:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("dry-run helper error = %T %v; output:\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("dry-run helper exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "Dry-run cycle reported a failure") {
		t.Fatalf("output = %s\nwant the dry-run failure log line", output)
	}
}
