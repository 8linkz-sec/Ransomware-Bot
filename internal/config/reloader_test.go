package config

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestReloaderCheckLoadsChangedConfigAndWaitsForMarkApplied(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "old-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Second)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}
	if got := reloader.Interval(); got != time.Second {
		t.Fatalf("Interval() = %v, want 1s", got)
	}

	cfg, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() unchanged error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() unchanged = cfg %v changed %v, want no change", cfg, changed)
	}

	writeGeneralConfig(t, dir, map[string]any{"api_key": "newer-key"})

	cfg, changed, err = reloader.Check()
	if err != nil {
		t.Fatalf("Check() changed error = %v", err)
	}
	if !changed {
		t.Fatal("Check() changed = false, want true")
	}
	if cfg.APIKey != "newer-key" {
		t.Fatalf("loaded APIKey = %q, want newer-key", cfg.APIKey)
	}

	cfg, changed, err = reloader.Check()
	if err != nil {
		t.Fatalf("Check() before MarkApplied error = %v", err)
	}
	if !changed || cfg.APIKey != "newer-key" {
		t.Fatalf("Check() before MarkApplied = cfg APIKey %q changed %v, want pending change", cfg.APIKey, changed)
	}

	reloader.MarkApplied()
	cfg, changed, err = reloader.Check()
	if err != nil {
		t.Fatalf("Check() after MarkApplied error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() after MarkApplied = cfg %v changed %v, want no change", cfg, changed)
	}
}

func TestReloaderCheckWarnsOncePerLoadForSharedWebhookURL(t *testing.T) {
	dir := t.TempDir()
	sharedWebhookOverrides := func(apiKey string) map[string]any {
		return map[string]any{
			"api_key": apiKey,
			"discord_webhooks": map[string]any{
				"ransomware": map[string]any{
					"enabled": true,
					"url":     sharedDiscordURL,
					"targets": []any{map[string]any{"url": sharedDiscordURL}},
				},
			},
		}
	}
	writeGeneralConfig(t, dir, sharedWebhookOverrides(realAPIKey))

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	countLoadInfos := func(entries []*logrus.Entry) int {
		loads := 0
		for _, entry := range entries {
			if entry.Level == logrus.InfoLevel && strings.Contains(entry.Message, "Configuration loaded successfully") {
				loads++
			}
		}
		return loads
	}

	hook := logtest.NewGlobal()
	defer hook.Reset()

	// Phase 1: a real file change loads the config and warns once. The rewrite
	// changes the file size, which the signature (name:present:mtime:size) sees.
	hook.Reset()
	writeGeneralConfig(t, dir, sharedWebhookOverrides(realAPIKey+"-rotated-for-the-signature"))
	cfg, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() after rewrite error = %v", err)
	}
	if !changed {
		t.Fatal("Check() after rewrite changed = false, want true (the signature did not move)")
	}
	if cfg == nil {
		t.Fatal("Check() after rewrite cfg = nil, want a loaded config")
	}
	if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 1 {
		t.Fatalf("phase 1 shared-URL WARN count = %d, want 1", len(warns))
	}

	// Phase 2: an unchanged file is not loaded at all, so it neither logs the
	// load nor warns. This is what makes the WARN per load, not per tick.
	hook.Reset()
	reloader.MarkApplied()
	cfg, changed, err = reloader.Check()
	if err != nil {
		t.Fatalf("Check() unchanged error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() unchanged = cfg %v changed %v, want no change", cfg, changed)
	}
	if loads := countLoadInfos(hook.AllEntries()); loads != 0 {
		t.Fatalf("phase 2 config load count = %d, want 0", loads)
	}
	if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 0 {
		t.Fatalf("phase 2 shared-URL WARN count = %d, want 0", len(warns))
	}

	// Phase 3: an explicit Reload always loads, so it warns again.
	hook.Reset()
	if _, err := reloader.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if warns := countSharedURLWarnings(hook.AllEntries()); len(warns) != 1 {
		t.Fatalf("phase 3 shared-URL WARN count = %d, want 1", len(warns))
	}
}

// TestReloaderCheckWarnsOncePerLoadForTargetInheritance pins the rule that the
// target-inheritance WARN fires once per real load or real reload, never once
// per tick, the same cadence already proven for the shared-webhook-URL WARN
// above.
func TestReloaderCheckWarnsOncePerLoadForTargetInheritance(t *testing.T) {
	dir := t.TempDir()
	targetInheritanceOverrides := func(apiKey string) map[string]any {
		return map[string]any{
			"api_key": apiKey,
			"discord_webhooks": map[string]any{
				"ransomware": map[string]any{
					"enabled": true,
					"url":     sharedDiscordURL,
					"filters": map[string]any{"include_groups": []any{"lockbit"}},
					"targets": []any{map[string]any{"url": otherDiscordURL}},
				},
			},
		}
	}
	writeGeneralConfig(t, dir, targetInheritanceOverrides(realAPIKey))

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	hook := logtest.NewGlobal()
	defer hook.Reset()

	// Phase 1: a real file change loads the config and warns once.
	hook.Reset()
	writeGeneralConfig(t, dir, targetInheritanceOverrides(realAPIKey+"-rotated-for-the-signature"))
	cfg, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() after rewrite error = %v", err)
	}
	if !changed {
		t.Fatal("Check() after rewrite changed = false, want true (the signature did not move)")
	}
	if cfg == nil {
		t.Fatal("Check() after rewrite cfg = nil, want a loaded config")
	}
	if warns := countTargetInheritanceWarnings(hook.AllEntries()); len(warns) != 1 {
		t.Fatalf("phase 1 target-inheritance WARN count = %d, want 1", len(warns))
	}

	// Phase 2: an unchanged file is not loaded at all, so it does not warn.
	// This is what makes the WARN per load, not per tick.
	hook.Reset()
	reloader.MarkApplied()
	cfg, changed, err = reloader.Check()
	if err != nil {
		t.Fatalf("Check() unchanged error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() unchanged = cfg %v changed %v, want no change", cfg, changed)
	}
	if warns := countTargetInheritanceWarnings(hook.AllEntries()); len(warns) != 0 {
		t.Fatalf("phase 2 target-inheritance WARN count = %d, want 0", len(warns))
	}

	// Phase 3: an explicit Reload always loads, so it warns again.
	hook.Reset()
	if _, err := reloader.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if warns := countTargetInheritanceWarnings(hook.AllEntries()); len(warns) != 1 {
		t.Fatalf("phase 3 target-inheritance WARN count = %d, want 1", len(warns))
	}
}

// TestReloadRaceConcurrentEditDoesNotDesyncSignature pins the fix: a config
// file rewritten between Reload()'s signature read
// and its config load must not let the reloader believe it applied a config
// version it never actually read. reloadSignatureHook fires once per attempt,
// after the "before" signature read and before LoadConfig -- exactly the
// race window -- so the single concurrent edit lands deterministically
// instead of depending on real goroutine timing.
func TestReloadRaceConcurrentEditDoesNotDesyncSignature(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	raced := false
	reloader.reloadSignatureHook = func() {
		if raced {
			return
		}
		raced = true
		// Land a concurrent edit inside the window between the "before"
		// signature read and LoadConfig -- a longer value changes both the
		// file size and the mtime, guaranteeing the signature moves.
		writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key-updated-by-race"})
	}

	cfg, err := reloader.Reload()
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if cfg.APIKey != "initial-key-updated-by-race" {
		t.Fatalf("Reload() loaded APIKey = %q, want the raced value", cfg.APIKey)
	}
	reloader.MarkApplied()

	// Check() after MarkApplied() must report no pending change: the
	// signature Reload() recorded must be the one that was actually stable
	// when the config was loaded, not the stale "before" signature.
	again, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() after MarkApplied error = %v", err)
	}
	if changed {
		t.Fatalf("Check() after MarkApplied = cfg %+v changed %v, want no pending change "+
			"(disk state is unchanged since Reload() loaded and MarkApplied() recorded it)", again, changed)
	}
}

// TestReloadUnderSustainedRaceNeverMarksAppliedForUnverifiedSignature drives
// reloadSignatureHook on every retry attempt with an ever-different config,
// so the directory never stabilizes within the attempt budget. Reload() must
// still return the last-loaded config (never nil), the hook must fire
// exactly maxReloadSignatureAttempts times (bounded, no infinite loop), and
// MarkApplied() afterwards must be a true no-op: lastSignature stays
// byte-identical to what it was before Reload() ran.
func TestReloadUnderSustainedRaceNeverMarksAppliedForUnverifiedSignature(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}
	signatureBefore, err := ConfigFileSignature(dir)
	if err != nil {
		t.Fatalf("ConfigFileSignature() error = %v", err)
	}
	lastSignatureBefore := reloader.lastSignature

	hookCalls := 0
	reloader.reloadSignatureHook = func() {
		hookCalls++
		// A different length every time guarantees the file size (part of
		// the signature) changes on every single attempt, so the directory
		// never stabilizes within the retry budget.
		writeGeneralConfig(t, dir, map[string]any{
			"api_key": strings.Repeat("x", hookCalls) + "-sustained-race",
		})
	}

	cfg, err := reloader.Reload()
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("Reload() cfg = nil, want the last-loaded config even under a sustained race")
	}
	if hookCalls != maxReloadSignatureAttempts {
		t.Fatalf("hook fired %d times, want exactly maxReloadSignatureAttempts = %d", hookCalls, maxReloadSignatureAttempts)
	}

	reloader.MarkApplied()
	if reloader.lastSignature != lastSignatureBefore {
		t.Fatalf("lastSignature = %q after MarkApplied(), want unchanged %q "+
			"(nextSignature must stay unset when no attempt verified a stable signature)",
			reloader.lastSignature, lastSignatureBefore)
	}
	if reloader.lastSignature == signatureBefore && reloader.nextSignature != "" {
		t.Fatalf("nextSignature = %q, want empty (MarkApplied must be a no-op)", reloader.nextSignature)
	}
}

// TestReloadAloneDoesNotCommitSignatureUntilCallerCallsMarkApplied guards the
// Reload()/MarkApplied() split itself: Reload() must only compute a
// verified-stable signature and stage it in nextSignature, never commit it to
// lastSignature on its own. Production relies on this -- Scheduler.ReloadConfig
// (internal/scheduler/scheduler.go) calls MarkApplied() conditionally, only
// after applyReloadedConfig succeeds, so a reload whose apply step fails is
// retried on the next cycle instead of being silently treated as applied. The
// two race tests above call Reload() then MarkApplied() together and so
// cannot tell "Reload() alone commits" apart from "the explicit MarkApplied()
// call commits" -- this test calls only Reload() and checks Check() still
// reports the change as pending.
func TestReloadAloneDoesNotCommitSignatureUntilCallerCallsMarkApplied(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	writeGeneralConfig(t, dir, map[string]any{"api_key": "reloaded-key"})

	if _, err := reloader.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	// No MarkApplied() call here on purpose.
	_, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() after Reload() alone error = %v", err)
	}
	if !changed {
		t.Fatal("Check() after Reload() alone reported no pending change; " +
			"Reload() must never commit the signature itself -- only MarkApplied() may")
	}
}

// TestReloadReturnsTheVerifiedStableConfigNotTheFirstAttempt guards the other
// half of the read-verify retry: when the directory changes between attempts,
// Reload() must return the config from the attempt whose signature it
// actually verified as stable, not the config loaded on the very first
// attempt. The hook writes a different value on each of the first two
// attempts and then stops, so attempt 0's config (the discarded, unstable
// read) and the final stable attempt's config are provably different values
// -- unlike TestReloadRaceConcurrentEditDoesNotDesyncSignature, whose single
// mid-window edit makes the first and the stable read identical by
// construction and so cannot distinguish "returned the first read" from
// "returned the verified read".
func TestReloadReturnsTheVerifiedStableConfigNotTheFirstAttempt(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Minute)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	hookCalls := 0
	reloader.reloadSignatureHook = func() {
		hookCalls++
		switch hookCalls {
		case 1:
			writeGeneralConfig(t, dir, map[string]any{"api_key": "first-attempt-value"})
		case 2:
			writeGeneralConfig(t, dir, map[string]any{"api_key": "final-stable-value"})
		}
		// From the third call on, stop writing so the directory stabilizes.
	}

	cfg, err := reloader.Reload()
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if cfg.APIKey != "final-stable-value" {
		t.Fatalf("Reload() APIKey = %q, want the verified-stable value %q, not the discarded first attempt",
			cfg.APIKey, "final-stable-value")
	}
}
