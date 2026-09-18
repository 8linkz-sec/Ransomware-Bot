package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewReloaderUsesDefaultInterval(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, nil)

	reloader, err := NewReloader(dir)
	if err != nil {
		t.Fatalf("NewReloader() error = %v", err)
	}
	if got := reloader.Interval(); got != DefaultReloadInterval {
		t.Fatalf("Interval() = %v, want %v", got, DefaultReloadInterval)
	}
}

func TestNewReloaderWithIntervalNormalizesNonPositiveInterval(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, nil)

	reloader, err := NewReloaderWithInterval(dir, 0)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}
	if got := reloader.Interval(); got != DefaultReloadInterval {
		t.Fatalf("Interval() = %v, want %v", got, DefaultReloadInterval)
	}
}

func TestNewReloaderReturnsErrorForMissingRequiredConfig(t *testing.T) {
	reloader, err := NewReloaderWithInterval(t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("NewReloaderWithInterval() succeeded without config_general.json")
	}
	if reloader == nil {
		t.Fatal("NewReloaderWithInterval() returned nil reloader alongside error")
	}
	if !strings.Contains(err.Error(), "config_general.json") {
		t.Fatalf("error = %v, want config_general.json context", err)
	}
}

func TestReloaderIntervalNilReceiver(t *testing.T) {
	var reloader *Reloader
	if got := reloader.Interval(); got != DefaultReloadInterval {
		t.Fatalf("nil Interval() = %v, want %v", got, DefaultReloadInterval)
	}
}

func TestReloaderCheckPropagatesSignatureErrors(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, nil)

	reloader, err := NewReloaderWithInterval(dir, time.Second)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "config_general.json")); err != nil {
		t.Fatalf("Remove(config_general.json) error = %v", err)
	}

	cfg, changed, err := reloader.Check()
	if err == nil {
		t.Fatal("Check() succeeded after required config was deleted")
	}
	if cfg != nil || changed {
		t.Fatalf("Check() = cfg %v changed %v, want no pending change on signature error", cfg, changed)
	}
}

func TestReloaderCheckReportsLoadErrorForChangedInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, nil)

	reloader, err := NewReloaderWithInterval(dir, time.Second)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	writeFile(t, dir, "config_general.json", `{"log_level":"NOT-A-LEVEL"}`)

	cfg, changed, err := reloader.Check()
	if err == nil {
		t.Fatal("Check() succeeded with invalid changed config")
	}
	if !changed {
		t.Fatal("Check() changed = false, want true for changed invalid config")
	}
	if cfg != nil {
		t.Fatalf("Check() cfg = %v, want nil on load error", cfg)
	}
}

func TestReloaderReloadLoadsConfigAndTracksSignature(t *testing.T) {
	dir := t.TempDir()
	writeGeneralConfig(t, dir, map[string]any{"api_key": "initial-key"})

	reloader, err := NewReloaderWithInterval(dir, time.Second)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	writeGeneralConfig(t, dir, map[string]any{"api_key": "reloaded-key-value"})

	cfg, err := reloader.Reload()
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if cfg.APIKey != "reloaded-key-value" {
		t.Fatalf("Reload() APIKey = %q, want reloaded-key-value", cfg.APIKey)
	}

	reloader.MarkApplied()
	cfg, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() after Reload+MarkApplied error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() = cfg %v changed %v, want reloaded signature applied", cfg, changed)
	}
}

func TestReloaderReloadReturnsLoadErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config_general.json", `{"log_level":"NOT-A-LEVEL"}`)

	reloader := &Reloader{configDir: dir, interval: time.Second}
	cfg, err := reloader.Reload()
	if err == nil {
		t.Fatal("Reload() succeeded with invalid config")
	}
	if cfg != nil {
		t.Fatalf("Reload() cfg = %v, want nil on error", cfg)
	}
	if !strings.Contains(err.Error(), "log level") {
		t.Fatalf("Reload() error = %v, want log level context", err)
	}
}

func TestMarkAppliedIsNoopWithoutPendingSignature(t *testing.T) {
	var nilReloader *Reloader
	nilReloader.MarkApplied() // must not panic on nil receiver

	dir := t.TempDir()
	writeGeneralConfig(t, dir, nil)
	reloader, err := NewReloaderWithInterval(dir, time.Second)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}

	reloader.MarkApplied()
	cfg, changed, err := reloader.Check()
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if changed || cfg != nil {
		t.Fatalf("Check() = cfg %v changed %v, want unchanged after no-op MarkApplied", cfg, changed)
	}
}
