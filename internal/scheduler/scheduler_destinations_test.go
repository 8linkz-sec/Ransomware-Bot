package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	destinationURLA = "https://discord.com/api/webhooks/111111111111111111/tokenAAAAAAAAAAAAAAAA"
	destinationURLB = "https://discord.com/api/webhooks/222222222222222222/tokenBBBBBBBBBBBBBBBB"
	destinationURLC = "https://discord.com/api/webhooks/333333333333333333/tokenCCCCCCCCCCCCCCCC"
)

// destinationReloadConfig builds a config whose discord ransomware block owns
// the given endpoints in order, which is the block the remap scenarios edit.
func destinationReloadConfig(dataDir string, urls ...string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.APIKey = "destination-key"
	cfg.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URLs: urls}
	return cfg
}

// TestDestinationEndpointsMatchDeliveryDestinationIDs is the guard that the
// manifest and the delivery path cannot drift: the IDs config enumerates must
// be exactly the IDs api_delivery.go and webhookTargetsForRoute produce.
func TestDestinationEndpointsMatchDeliveryDestinationIDs(t *testing.T) {
	cfg := config.DefaultConfig()
	block := func(urls ...string) config.WebhookConfig {
		return config.WebhookConfig{Enabled: true, URLs: urls}
	}
	cfg.DiscordWebhooks.Ransomware = block(destinationURLA, destinationURLB, destinationURLC)
	cfg.DiscordWebhooks.RSS = block(destinationURLA, destinationURLB)
	cfg.DiscordWebhooks.Government = block(destinationURLA, destinationURLB, destinationURLC)
	cfg.SlackWebhooks.Ransomware = block("https://hooks.slack.com/services/T1/B1/x1", "https://hooks.slack.com/services/T1/B2/x2")
	cfg.SlackWebhooks.RSS = block("https://hooks.slack.com/services/T1/B3/x3", "https://hooks.slack.com/services/T1/B4/x4")
	cfg.SlackWebhooks.Government = block("https://hooks.slack.com/services/T1/B5/x5")
	cfg.SlackCompatibleWebhooks.Ransomware = block("https://compat.example/c1", "https://compat.example/c2")
	cfg.SlackCompatibleWebhooks.RSS = block("https://compat.example/c3")
	cfg.SlackCompatibleWebhooks.Government = block("https://compat.example/c4", "https://compat.example/c5")

	delivered := map[string]bool{}
	// The API delivery path, exactly as api_delivery.go builds it.
	for _, target := range config.WebhookTargetsForType(cfg, config.WebhookTypeRansomware) {
		messenger, ok := messengerForWebhookPlatform(target.Platform)
		if !ok {
			continue
		}
		delivered[apiDestinationIDForTarget(messenger, target.DestinationSuffix)] = true
	}
	// The RSS delivery path, exactly as scheduler.go builds it per route.
	for _, route := range rssFeedRoutes(cfg) {
		for _, target := range webhookTargetsForRoute(cfg, route) {
			delivered[targetDestinationID(target)] = true
		}
	}

	enumerated := map[string]bool{}
	for _, endpoint := range config.DestinationEndpoints(cfg) {
		enumerated[endpoint.ID] = true
	}

	if !reflect.DeepEqual(sortedKeys(delivered), sortedKeys(enumerated)) {
		t.Fatalf("delivery destination IDs =\n%v\nmanifest destination IDs =\n%v",
			sortedKeys(delivered), sortedKeys(enumerated))
	}
	if len(enumerated) == 0 {
		t.Fatal("no destination IDs enumerated; the guard would be vacuous")
	}
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestApplyReloadedConfigRejectsDestinationRemap(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	dataDir := t.TempDir()
	cfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB)
	cfg.LogLevel = "INFO"
	if err := status.WriteDestinationManifest(dataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
	seeded, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}

	newCfg := destinationReloadConfig(dataDir, destinationURLB, destinationURLA)
	newCfg.LogLevel = "DEBUG"

	reloader := &recordingConfigReloader{cfg: newCfg, changed: true}
	deps, _, _ := newFakeDependenciesForTest(cfg, reloader)
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if !errors.Is(err, errDestinationRemapRejected) {
		t.Fatalf("ReloadConfig() error = %v, want errDestinationRemapRejected", err)
	}
	if changed {
		t.Fatal("ReloadConfig() changed = true, want false")
	}
	if got := s.getConfig().LogLevel; got != "INFO" {
		t.Fatalf("LogLevel after a rejected reload = %q, want the old config value INFO", got)
	}
	if !reloader.applied {
		t.Fatal("MarkApplied was not called, so the rejected version would be re-warned every tick")
	}

	entry := findTestLogEntry(hook, "Config reload rejected: webhook endpoints were remapped onto existing destination IDs")
	if entry == nil {
		t.Fatalf("missing reload rejection WARN; entries=%v", allTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("rejection level = %v, want warn", entry.Level)
	}
	if got := entry.Data["change"]; got != "reorder" {
		t.Fatalf("change = %#v, want reorder", got)
	}
	if got, _ := entry.Data["movements"].(string); got == "" {
		t.Fatalf("movements = %#v, want a non-empty movement list", entry.Data["movements"])
	}
	for _, e := range hook.AllEntries() {
		if e.Level == log.ErrorLevel {
			t.Fatalf("unexpected ERROR entry %q; a rejected reload is a WARN", e.Message)
		}
	}

	after, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	if string(after) != string(seeded) {
		t.Fatal("the manifest was rewritten by a rejected reload")
	}
}

func TestApplyReloadedConfigAppliesAppendAndRewritesManifest(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	dataDir := t.TempDir()
	cfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB)
	if err := status.WriteDestinationManifest(dataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}

	newCfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB, destinationURLC)
	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.applyReloadedConfig(newCfg)
	if err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil)", changed, err)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel {
			t.Fatalf("unexpected WARN %q for an append-only reload", entry.Message)
		}
	}

	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if _, ok := manifest.Destinations["discord.ransomware.3"]; !ok {
		t.Fatalf("manifest = %v, want the appended discord.ransomware.3", manifest.Destinations)
	}
	if !reflect.DeepEqual(manifest.Destinations, config.DestinationURLHashes(newCfg)) {
		t.Fatalf("manifest = %v, want the new config hashes", manifest.Destinations)
	}
}

func TestApplyReloadedConfigSelfHealsMissingManifest(t *testing.T) {
	dataDir := t.TempDir()
	cfg := destinationReloadConfig(dataDir, destinationURLA)
	newCfg := destinationReloadConfig(dataDir, destinationURLB)

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	// No manifest exists, so even a URL rotation applies and is recorded.
	changed, err := s.applyReloadedConfig(newCfg)
	if err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil)", changed, err)
	}
	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(manifest.Destinations, config.DestinationURLHashes(newCfg)) {
		t.Fatalf("manifest = %v, want the new config hashes", manifest.Destinations)
	}
}

func TestApplyReloadedConfigSkipsManifestWhenDataDirEmpty(t *testing.T) {
	cfg := destinationReloadConfig("", destinationURLA, destinationURLB)
	newCfg := destinationReloadConfig("", destinationURLB, destinationURLA)

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.applyReloadedConfig(newCfg)
	if err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil) without persistence", changed, err)
	}
}

func TestApplyReloadedConfigWarnsWhenManifestWriteFails(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	// A regular file cannot hold a manifest, so ensurePrivateDataDir fails fast.
	dataDir := filepath.Join(t.TempDir(), "data-is-a-file")
	if err := os.WriteFile(dataDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataDir) error = %v", err)
	}
	cfg := destinationReloadConfig(dataDir, destinationURLA)
	newCfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB)

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.applyReloadedConfig(newCfg)
	if err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil); a manifest write must never stop a reload", changed, err)
	}
	entry := findTestLogEntry(hook, "Failed to write the destination manifest after config reload")
	if entry == nil {
		t.Fatalf("missing manifest write WARN; entries=%v", allTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("manifest write failure level = %v, want warn", entry.Level)
	}
}

func TestApplyReloadedConfigDryRunDoesNotWriteManifest(t *testing.T) {
	dataDir := t.TempDir()
	cfg := destinationReloadConfig(dataDir, destinationURLA)
	newCfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB)

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	if changed, err := s.applyReloadedConfig(newCfg); err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil)", changed, err)
	}
	if _, err := os.Stat(status.DestinationManifestPath(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote %s (stat err=%v)", status.DestinationManifestPath(dataDir), err)
	}
}

// TestRunConfigWatcherConsumesEveryRejectedDestinationRemapVersion pins the
// scheduler half of invariant 7: every version the reloader hands over is
// consumed with MarkApplied and warned about exactly once. "One WARN per
// version rather than per tick" is a property of the real config.Reloader
// signature comparison, covered by config's TestReloaderCheckLoadsChangedConfig
// AndWaitsForMarkApplied.
func TestRunConfigWatcherConsumesEveryRejectedDestinationRemapVersion(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	dataDir := t.TempDir()
	cfg := destinationReloadConfig(dataDir, destinationURLA, destinationURLB)
	if err := status.WriteDestinationManifest(dataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
	remapCfg := destinationReloadConfig(dataDir, destinationURLB, destinationURLA)

	reloader := &scriptedConfigReloader{
		interval:  5 * time.Millisecond,
		exhausted: make(chan struct{}),
		steps: []func() (*config.Config, bool, error){
			func() (*config.Config, bool, error) { return remapCfg, true, nil },
			func() (*config.Config, bool, error) { return remapCfg, true, nil },
		},
	}

	deps, _, _ := newFakeDependenciesForTest(cfg, reloader)
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	s.wg.Add(1)
	go s.runConfigWatcher(t.Context())

	waitForTestSignalWithTimeout(t, reloader.exhausted, 10*time.Second, "config watcher script completion")
	s.signalStop()

	stopped := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(stopped)
	}()
	waitForTestSignalWithTimeout(t, stopped, 10*time.Second, "config watcher shutdown")

	if got := reloader.appliedCount(); got != 2 {
		t.Fatalf("MarkApplied count = %d, want 2 (one per rejected version)", got)
	}
	rejections := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Config reload rejected: webhook endpoints were remapped onto existing destination IDs" {
			if entry.Level != log.WarnLevel {
				t.Fatalf("rejection level = %v, want warn", entry.Level)
			}
			rejections++
		}
		if entry.Level == log.ErrorLevel {
			t.Fatalf("unexpected ERROR entry %q for a rejected reload", entry.Message)
		}
	}
	if rejections != 2 {
		t.Fatalf("rejection WARN count = %d, want 2 (one per version handed over)", rejections)
	}
	if got := s.getConfig().DiscordWebhooks.Ransomware.URLs[0]; got != destinationURLA {
		t.Fatalf("first endpoint after rejected reloads = %q, want the old config value", got)
	}
}

func allTestLogMessages(hook *logtest.Hook) []string {
	messages := make([]string, 0, len(hook.AllEntries()))
	for _, entry := range hook.AllEntries() {
		messages = append(messages, entry.Message)
	}
	return messages
}

func TestApplyReloadedConfigSelfHealsUnknownManifestVersion(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	dataDir := t.TempDir()
	if err := os.WriteFile(status.DestinationManifestPath(dataDir),
		[]byte(`{"version":99,"destinations":{"discord.ransomware":"stale"}}`), 0600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	cfg := destinationReloadConfig(dataDir, destinationURLA)
	// A rotation that would normally be rejected: with an unusable manifest the
	// check is skipped for this reload and the file is rewritten.
	newCfg := destinationReloadConfig(dataDir, destinationURLB)

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.applyReloadedConfig(newCfg)
	if err != nil || !changed {
		t.Fatalf("applyReloadedConfig() = (%v, %v), want (true, nil)", changed, err)
	}

	entry := findTestLogEntry(hook, "Destination manifest unreadable; rewriting it")
	if entry == nil {
		t.Fatalf("missing self-heal WARN; entries=%v", allTestLogMessages(hook))
	}
	if entry.Level != log.WarnLevel {
		t.Fatalf("self-heal level = %v, want warn", entry.Level)
	}
	if got := entry.Data["manifest_version"]; got != 99 {
		t.Fatalf("manifest_version = %#v, want 99", got)
	}

	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v; the manifest must be rewritten", found, err)
	}
	if !reflect.DeepEqual(manifest.Destinations, config.DestinationURLHashes(newCfg)) {
		t.Fatalf("manifest = %v, want the new config hashes", manifest.Destinations)
	}
}

// writeDestinationConfigDirForTest renders a real, loadable config directory
// whose discord ransomware block owns the given endpoints in order. The real
// config.Reloader keys off mtime and size, so callers must bump the mtime when
// a rewrite keeps the same byte count (a swap does).
// The real config validator rejects any webhook segment containing "TOKEN",
// so the loadable-config test needs its own endpoint URLs.
const (
	loadableURLA = "https://discord.com/api/webhooks/111111111111111111/aaaaAAAAbbbbBBBBccccCCCC01"
	loadableURLB = "https://discord.com/api/webhooks/222222222222222222/ddddDDDDeeeeEEEEffffFFFF02"
	loadableURLC = "https://discord.com/api/webhooks/333333333333333333/gggeGGGGhhhhHHHHiiiiIIII03"
)

func writeDestinationConfigDirForTest(t *testing.T, configDir, dataDir string, urls ...string) {
	t.Helper()

	quoted := make([]string, 0, len(urls))
	for _, endpointURL := range urls {
		quoted = append(quoted, fmt.Sprintf("%q", endpointURL))
	}
	general := fmt.Sprintf(`{
  "log_level": "INFO",
  "data_dir": %q,
  "api_key": "destination-reload-key",
  "api_base_url": "http://api.destination.test",
  "discord_webhooks": {
    "ransomware": {"enabled": true, "urls": [%s]}
  }
}`, dataDir, strings.Join(quoted, ", "))
	generalPath := filepath.Join(configDir, "config_general.json")
	if err := os.WriteFile(generalPath, []byte(general), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
	// The reloader signature is mtime+size, and a pure reorder keeps the size.
	stamp := time.Now().Add(time.Duration(len(urls)) * time.Second)
	if err := os.Chtimes(generalPath, stamp, stamp); err != nil {
		t.Fatalf("Chtimes(config_general.json) error = %v", err)
	}
	// The feeds and format files never change between calls. Rewriting them
	// anyway would move their mtime and hand the 5 ms reloader two or three
	// signature changes for one logical edit, so a remap could be rejected
	// twice (observed flake). Only touch them when the content differs.
	writeConfigFileIfChangedForTest(t, filepath.Join(configDir, "config_feeds.json"), `{
  "general_feeds": [],
  "government_feeds": [],
  "ransomware_feeds": []
}`)
	writeConfigFileIfChangedForTest(t, filepath.Join(configDir, "config_format.json"), `{}`)
}

// writeConfigFileIfChangedForTest writes content to path only when the file
// is missing or differs, so an unchanged file keeps its reloader signature.
func writeConfigFileIfChangedForTest(t *testing.T, path, content string) {
	t.Helper()
	if current, err := os.ReadFile(path); err == nil && string(current) == content { //nolint:gosec // temp dir under test control
		return
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", filepath.Base(path), err)
	}
}

// apiDeliveryMappingForTest is the mapping the delivery path would actually
// use: destination ID to webhook URL, built by api_delivery.go itself.
func apiDeliveryMappingForTest(s *Scheduler) map[string]string {
	mapping := map[string]string{}
	for _, target := range s.apiDeliveryTargetsForConfig(s.getConfig()) {
		mapping[target.destinationID] = target.url
	}
	return mapping
}

func countTestLogMessages(hook *logtest.Hook, message string) int {
	count := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			count++
		}
	}
	return count
}

func waitForTestLogMessage(t *testing.T, hook *logtest.Hook, message string, want int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countTestLogMessages(hook, message) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d x %q; entries=%v", want, message, allTestLogMessages(hook))
}

// TestRunConfigWatcherWithRealReloaderRejectsRemapThenAppliesTheFix drives the
// watcher with the production config.Reloader over real files. It pins three
// things the fake reloader cannot: the rejected config version is consumed, so
// the same file is warned about exactly once and not on every tick; the OLD
// endpoint mapping keeps delivering while the reload is rejected; and a later
// edit of the same file is still detected and applied, rewriting the manifest.
func TestRunConfigWatcherWithRealReloaderRejectsRemapThenAppliesTheFix(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	configDir := t.TempDir()
	dataDir := t.TempDir()
	writeDestinationConfigDirForTest(t, configDir, dataDir, loadableURLA, loadableURLB)

	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := status.WriteDestinationManifest(dataDir, config.DestinationURLHashes(cfg)); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
	seeded, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}

	reloader, err := config.NewReloaderWithInterval(configDir, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewReloaderWithInterval() error = %v", err)
	}
	deps, _, _ := newFakeDependenciesForTest(cfg, reloader)
	s, err := NewWithDependencies(cfg, configDir, false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	s.wg.Add(1)
	go s.runConfigWatcher(t.Context())
	defer func() {
		s.signalStop()
		stopped := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(stopped)
		}()
		waitForTestSignalWithTimeout(t, stopped, 10*time.Second, "config watcher shutdown")
	}()

	const rejection = "Config reload rejected: webhook endpoints were remapped onto existing destination IDs"

	// Swap the two endpoints: the reload must be rejected.
	writeDestinationConfigDirForTest(t, configDir, dataDir, loadableURLB, loadableURLA)
	waitForTestLogMessage(t, hook, rejection, 1)

	// The old configuration must still be the one that delivers.
	mapping := apiDeliveryMappingForTest(s)
	want := map[string]string{
		"discord.ransomware":   loadableURLA,
		"discord.ransomware.2": loadableURLB,
	}
	if !reflect.DeepEqual(mapping, want) {
		t.Fatalf("delivery mapping after a rejected reload = %v, want the old mapping %v", mapping, want)
	}
	after, err := os.ReadFile(status.DestinationManifestPath(dataDir)) //nolint:gosec // temp dir under test control
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	if string(after) != string(seeded) {
		t.Fatal("a rejected reload rewrote the manifest")
	}

	// Several more ticks must not re-warn: MarkApplied consumed the version.
	time.Sleep(60 * time.Millisecond)
	if got := countTestLogMessages(hook, rejection); got != 1 {
		t.Fatalf("rejection WARN count = %d, want exactly 1 per rejected config version", got)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.ErrorLevel {
			t.Fatalf("unexpected ERROR entry %q; a rejected reload is a WARN", entry.Message)
		}
	}

	// Fixing the file (restore the order, append a third endpoint) must be
	// picked up normally and must rewrite the manifest.
	writeDestinationConfigDirForTest(t, configDir, dataDir,
		loadableURLA, loadableURLB, loadableURLC)
	waitForTestLogMessage(t, hook, "Config reloaded successfully", 1)

	if got := countTestLogMessages(hook, rejection); got != 1 {
		t.Fatalf("rejection WARN count after the fix = %d, want 1", got)
	}
	mapping = apiDeliveryMappingForTest(s)
	want["discord.ransomware.3"] = loadableURLC
	if !reflect.DeepEqual(mapping, want) {
		t.Fatalf("delivery mapping after the fix = %v, want %v", mapping, want)
	}
	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(manifest.Destinations, config.DestinationURLHashes(s.getConfig())) {
		t.Fatalf("manifest = %v, want the applied config hashes", manifest.Destinations)
	}
	if _, ok := manifest.Destinations["discord.ransomware.3"]; !ok {
		t.Fatalf("manifest = %v, want the appended discord.ransomware.3", manifest.Destinations)
	}
}
