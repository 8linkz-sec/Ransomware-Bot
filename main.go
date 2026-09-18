// Package main implements a ransomware alert bot that monitors ransomware.live
// API and RSS feeds, delivering cybersecurity alerts to messaging webhooks.
//
// The bot uses a modular architecture with separate components for:
// - API polling and RSS feed parsing
// - Message formatting and Discord/Slack webhook delivery
// - Persistent status tracking and deduplication
// - Configurable logging with rotation
//
// Configuration is managed through JSON files in the config directory.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/i18n"
	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/scheduler"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// Application build metadata. Values can be overridden with -ldflags.
var (
	Version   = "1.2.1"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const privateRuntimeDirMode os.FileMode = 0700

const readinessFileEnv = "RANSOMWARE_BOT_READY_FILE"

// Per-poller progress markers live beside the readiness marker, one file per
// poller, each written by exactly one goroutine. They are deliberately separate
// files rather than extra lines in the readiness marker: the readiness marker
// keeps its existing "written once after Start(), removed at shutdown" meaning,
// which --healthcheck's RSS verdict depends on.
const (
	progressMarkerSuffixAPI = ".api"
	progressMarkerSuffixRSS = ".rss"
)

type cliOptions struct {
	configDir      string
	dataDir        string
	locale         string
	showVersion    bool
	checkConfig    bool
	healthcheck    bool
	listDeadLetter bool
	dryRun         bool
	// acceptDestinationRemap accepts a changed webhook-endpoint-to-destination-ID
	// mapping once and rewrites destinations.json.
	acceptDestinationRemap bool
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, out, errOut io.Writer) int {
	opts, err := parseCLIOptions(args, errOut)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/--help is an intentional, successful request for usage text
			// (already printed by flags.Parse via flags.SetOutput(errOut)),
			// not a malformed invocation. The usage text still goes to
			// errOut, same as a genuine parse error -- this only changes the
			// exit code, not the stream.
			return 0
		}
		return 2
	}
	msg := i18n.ForLocale(opts.locale)

	switch {
	case opts.showVersion:
		fmt.Fprintln(out, versionInfoFor(msg))
		return 0
	case opts.healthcheck:
		return runHealthcheckMode(opts, out, msg)
	case opts.listDeadLetter:
		return runListDeadLetterMode(opts, out, msg)
	}

	runtimeMode := !opts.checkConfig
	if runtimeMode {
		if err := setupBootstrapLogger(); err != nil {
			fmt.Fprintf(out, msg.T("cli.error_bootstrap_logger"), err)
			return 1
		}
	}

	if err := requireConfigDir(opts.configDir); err != nil {
		if runtimeMode {
			log.WithError(err).Error("Config directory validation failed")
		}
		fmt.Fprintf(out, msg.T("cli.error"), err)
		return 1
	}
	if opts.checkConfig {
		return runCheckConfig(opts, out, msg)
	}

	cfg, err := loadRuntimeConfig(opts)
	if err != nil {
		log.WithError(err).Error("Failed to load runtime configuration")
		fmt.Fprintf(out, msg.T("cli.error_plain"), err)
		return 1
	}
	if err := setupRuntimeLogger(cfg); err != nil {
		log.WithError(err).Error("Failed to set up runtime logger")
		fmt.Fprintf(out, msg.T("cli.error_logger"), err)
		return 1
	}
	defer closeApplicationLogger()

	log.WithFields(log.Fields{
		"version":    Version,
		"commit":     Commit,
		"build_date": BuildDate,
	}).Info("Starting Ransomware News Bot")

	// Runs before scheduler.New, so a refusal never takes the data_dir lock
	// and never clears the readiness marker.
	if code := applyDestinationManifest(cfg, opts, out, msg); code != 0 {
		return code
	}

	return runConfiguredScheduler(cfg, opts)
}

func parseCLIOptions(args []string, errOut io.Writer) (cliOptions, error) {
	var opts cliOptions
	msg := i18n.FromArgsAndEnv(args)
	opts.locale = msg.Locale()
	flags := flag.NewFlagSet("ransomware-news-bot", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&opts.configDir, "config-dir", "./configs", msg.T("cli.flag.config_dir"))
	flags.StringVar(&opts.dataDir, "data-dir", "", msg.T("cli.flag.data_dir"))
	flags.StringVar(&opts.locale, "locale", opts.locale, msg.T("cli.flag.locale"))
	flags.BoolVar(&opts.showVersion, "version", false, msg.T("cli.flag.version"))
	flags.BoolVar(&opts.checkConfig, "check-config", false, msg.T("cli.flag.check_config"))
	flags.BoolVar(&opts.healthcheck, "healthcheck", false, msg.T("cli.flag.healthcheck"))
	flags.BoolVar(&opts.listDeadLetter, "list-dead-letter", false, msg.T("cli.flag.list_dead_letter"))
	flags.BoolVar(&opts.dryRun, "dry-run", false, msg.T("cli.flag.dry_run"))
	flags.BoolVar(&opts.acceptDestinationRemap, "accept-destination-remap", false,
		msg.T("cli.flag.accept_destination_remap"))
	err := flags.Parse(args)
	opts.locale = i18n.ForLocale(opts.locale).Locale()
	return opts, err
}

func runHealthcheckMode(opts cliOptions, out io.Writer, msg i18n.Messages) int {
	if err := runHealthcheckWithMessages(opts.configDir, opts.dataDir, out, msg); err != nil {
		fmt.Fprintf(out, msg.T("cli.healthcheck_failed"), err)
		return 1
	}
	fmt.Fprintln(out, msg.T("cli.healthcheck_valid"))
	return 0
}

func runListDeadLetterMode(opts cliOptions, out io.Writer, msg i18n.Messages) int {
	if err := runListDeadLetterWithMessages(opts.configDir, opts.dataDir, out, msg); err != nil {
		fmt.Fprintf(out, msg.T("cli.dead_letter_error"), err)
		return 1
	}
	return 0
}

func requireConfigDir(configDir string) error {
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		return fmt.Errorf("config directory %q does not exist", configDir)
	}
	return nil
}

func runCheckConfig(opts cliOptions, out io.Writer, msg i18n.Messages) int {
	cfg, err := config.LoadConfigStrict(opts.configDir)
	if err != nil {
		fmt.Fprintf(out, msg.T("cli.config_invalid"), err)
		return 1
	}
	resolved, err := resolveDataDir(opts.dataDir, cfg.DataDir)
	if err != nil {
		fmt.Fprintf(out, msg.T("cli.config_invalid_data_dir"), err)
		return 1
	}
	if err := validateDataDir(resolved); err != nil {
		fmt.Fprintf(out, msg.T("cli.config_invalid_data_dir"), err)
		return 1
	}
	// The only --check-config outcome that depends on data_dir state rather
	// than on the config files alone.
	diff, pending, manifestSkipped := pendingDestinationRemap(cfg, resolved)
	if pending {
		printDestinationRemap(out, msg, diff, status.DestinationManifestPath(resolved))
		return 1
	}
	if manifestSkipped != nil {
		fmt.Fprintln(out, msg.Tf("cli.config_warning_manifest_unreadable", manifestSkipped.Error()))
	}
	if enabledDeliveryTargets(cfg) == 0 {
		fmt.Fprintln(out, msg.T("cli.config_warning_no_targets"))
	}
	for _, group := range config.SharedWebhookURLGroups(cfg) {
		fmt.Fprintln(out, msg.Tf("cli.config_warning_shared_webhook_url", strings.Join(group.EndpointIDs, ", ")))
	}
	fmt.Fprintln(out, msg.T("cli.config_valid"))
	return 0
}

func loadRuntimeConfig(opts cliOptions) (*config.Config, error) {
	cfg, err := config.LoadConfig(opts.configDir)
	if err != nil {
		return nil, fmt.Errorf("loading configuration: %w", err)
	}

	// Data directory precedence is flag > DATA_DIR env > config > default.
	resolved, err := resolveDataDir(opts.dataDir, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolving data-dir path: %w", err)
	}
	if err := validateDataDir(resolved); err != nil {
		log.WithError(err).WithField("data_dir", resolved).Warn("Data directory validation failed; scheduler will use in-memory status if persistence remains unavailable")
	}
	cfg.DataDir = resolved
	return cfg, nil
}

func setupRuntimeLogger(cfg *config.Config) error {
	return setupApplicationLogger(cfg.LogLevel, cfg.LogFilePath, log.LogRotationConfig{
		MaxSizeMB:  cfg.LogRotation.MaxSizeMB,
		MaxBackups: cfg.LogRotation.MaxBackups,
		MaxAgeDays: cfg.LogRotation.MaxAgeDays,
		Compress:   cfg.LogRotation.Compress,
	})
}

func setupBootstrapLogger() error {
	return log.NewStdoutLogger("INFO")
}

func runConfiguredScheduler(cfg *config.Config, opts cliOptions) int {
	if opts.dryRun {
		log.Info("Dry-run mode enabled: messages will be logged but not sent")
	} else if err := removeReadinessMarker(); err != nil {
		log.WithError(err).Error("Failed to clear stale readiness marker")
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched, err := scheduler.New(cfg, opts.configDir, opts.dryRun)
	if err != nil {
		log.WithError(err).Error("Failed to initialize scheduler")
		return 1
	}

	if opts.dryRun {
		return runDryRun(ctx, sched)
	}
	return runSchedulerUntilSignal(ctx, cancel, sched)
}

// applyDestinationManifest compares the webhook endpoints of cfg against
// destinations.json in data_dir and refuses the start when an existing
// destination ID now points at a different webhook URL. Returns the process
// exit code: 1 for a refusal, 0 otherwise.
//
// Destination IDs are positional, and dedup markers, retry-queue rows and dead
// letters are keyed by them, so a reorder silently re-points stored state at
// another channel. The manifest is the only record that binds an ID to a URL.
func applyDestinationManifest(cfg *config.Config, opts cliOptions, out io.Writer, msg i18n.Messages) int {
	if cfg == nil || cfg.DataDir == "" {
		return 0
	}
	manifestPath := status.DestinationManifestPath(cfg.DataDir)
	current := config.DestinationURLHashes(cfg)

	manifest, found, err := status.LoadDestinationManifest(cfg.DataDir)
	if err != nil {
		// Self-healing: the bot owns this file and nothing in it is
		// irreplaceable, so a corruption must not become an outage.
		log.WithFields(status.DestinationManifestUnreadableFields(cfg.DataDir, manifest, err)).
			Warn("Destination manifest unreadable; rewriting it")
		found = false
	}

	if found {
		diff := status.DiffDestinations(manifest.Destinations, current)
		if diff.HasRemap() {
			fields := status.DestinationRemapLogFields(diff, manifestPath)
			if !opts.acceptDestinationRemap {
				log.WithFields(fields).
					Error("Refusing to start: webhook endpoints were remapped onto existing destination IDs")
				printDestinationRemap(out, msg, diff, manifestPath)
				return 1
			}
			log.WithFields(fields).Warn("Accepting a changed destination mapping on operator request")
		}
		if len(diff.Removed) > 0 {
			logRemovedDestinations(cfg.DataDir, diff, current)
		}
	}

	if opts.dryRun {
		// --dry-run must not create persistent files.
		return 0
	}
	if err := status.WriteDestinationManifest(cfg.DataDir, current); err != nil {
		log.WithError(err).WithField("manifest", manifestPath).
			Warn("Failed to write the destination manifest")
		return 0
	}
	log.WithFields(log.Fields{
		"manifest":     manifestPath,
		"destinations": len(current),
	}).Debug("Destination manifest written")
	return 0
}

// pendingDestinationRemap reports a remap that destinations.json in dataDir
// proves against cfg. It never writes and never takes the data_dir lock, so it
// is safe to run beside a live bot.
// pendingDestinationRemap reports whether cfg's endpoint order has drifted
// from the recorded destination manifest. skipped is non-nil only when the
// manifest exists but could not be read or parsed (corrupt JSON, unknown
// version): the endpoint-order check did not run at all, as opposed to
// running and finding nothing to report. A missing manifest (first run) is
// not a skip and returns skipped == nil.
func pendingDestinationRemap(cfg *config.Config, dataDir string) (diff status.DestinationDiff, pending bool, skipped error) {
	if cfg == nil || dataDir == "" {
		return status.DestinationDiff{}, false, nil
	}
	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil {
		return status.DestinationDiff{}, false, err
	}
	if !found {
		return status.DestinationDiff{}, false, nil
	}
	diff = status.DiffDestinations(manifest.Destinations, config.DestinationURLHashes(cfg))
	if !diff.HasRemap() {
		return status.DestinationDiff{}, false, nil
	}
	return diff, true, nil
}

// printDestinationRemap writes the localized refusal to the operator stream.
// The movement list is destination IDs only, never a URL or a token.
func printDestinationRemap(out io.Writer, msg i18n.Messages, diff status.DestinationDiff, manifestPath string) {
	fmt.Fprintf(out, msg.T("cli.destination_remap"),
		destinationRemapKindText(msg, diff), diff.Movements(), manifestPath)
}

// destinationRemapKindText localizes the headline of the change. An unknown
// kind falls back to the key itself, which is visible but harmless.
func destinationRemapKindText(msg i18n.Messages, diff status.DestinationDiff) string {
	return msg.T("destination_remap.kind." + diff.Kind())
}

// logRemovedDestinations reports destination IDs that left the configuration.
// Deleting from the end is always safe, so this is an INFO; the counts tell the
// operator how much queued work still carries the removed IDs.
func logRemovedDestinations(dataDir string, diff status.DestinationDiff, known map[string]string) {
	fields := log.Fields{"destination_ids": strings.Join(diff.Removed, ", ")}
	// Lazy API load: counting a handful of retry rows must not read
	// api_status.json, which the scheduler loads lazily on purpose.
	tracker, err := status.NewReadOnlyTrackerLazyAPI(dataDir)
	if err != nil {
		log.WithError(err).WithFields(fields).Info("Destination IDs removed from the configuration")
		return
	}
	retryRows, deadLetterRows := tracker.OrphanedDestinationRows(known)
	fields["orphaned_retry_rows"] = retryRows
	fields["orphaned_dead_letter_rows"] = deadLetterRows
	log.WithFields(fields).Info("Destination IDs removed from the configuration")
}

func runDryRun(ctx context.Context, sched *scheduler.Scheduler) int {
	cycleOK := sched.RunOnce(ctx)
	if err := sched.Stop(); err != nil {
		log.WithError(err).Error("Dry-run shutdown completed with errors")
	}
	if !cycleOK {
		log.Error("Dry-run cycle reported a failure; see the errors above")
		return 1
	}
	log.Info("Dry-run completed")
	return 0
}

func runSchedulerUntilSignal(ctx context.Context, cancel context.CancelFunc, sched *scheduler.Scheduler) int {
	// A typed nil would satisfy the interface but panic on use, so only
	// register a recorder when the marker mechanism is actually configured.
	if markers := newProgressMarkers(); markers != nil {
		sched.SetProgressRecorder(markers)
	}
	if err := sched.Start(ctx); err != nil {
		log.WithError(err).Error("Failed to start scheduler")
		return 1
	}
	if err := writeReadinessMarker(); err != nil {
		cancel()
		if stopErr := sched.Stop(); stopErr != nil {
			log.WithError(stopErr).Error("Scheduler shutdown after readiness failure completed with errors")
		}
		log.WithError(err).Error("Failed to write readiness marker")
		return 1
	}

	log.Info("Bot started successfully")
	waitForShutdownSignal()
	log.Info("Shutdown signal received, stopping bot...")
	if err := removeReadinessMarker(); err != nil {
		log.WithError(err).Warn("Failed to remove readiness marker")
	}

	cancel()
	if err := sched.Stop(); err != nil {
		log.WithError(err).Error("Bot stopped with shutdown errors")
		return 1
	}
	log.Info("Bot stopped successfully")
	return 0
}

func waitForShutdownSignal() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	<-sigChan
}

func closeApplicationLogger() {
	if err := log.Close(); err != nil {
		log.WithError(err).Warn("Failed to close logger")
	}
}

func versionInfo() string {
	return versionInfoFor(i18n.Default())
}

func versionInfoFor(msg i18n.Messages) string {
	return msg.Tf("cli.version", Version, Commit, BuildDate)
}

func setupApplicationLogger(logLevel, logFilePath string, rotationConfig log.LogRotationConfig) error {
	logDir := filepath.Dir(logFilePath)
	if err := ensurePrivateRuntimeDir(logDir); err != nil {
		log.WithError(err).WithField("log_dir", logDir).Warn("Could not create logs directory; falling back to stdout-only logging")
		if logErr := log.NewStdoutLogger(logLevel); logErr != nil {
			return fmt.Errorf("stdout logger fallback failed after logs directory error: %w", logErr)
		}
		log.WithError(err).WithField("log_dir", logDir).Warn("File logging disabled")
		return nil
	}

	if err := log.NewLogger(logLevel, logFilePath, rotationConfig); err != nil {
		log.WithError(err).WithField("log_file", logFilePath).Warn("Could not initialize file logger; falling back to stdout-only logging")
		if logErr := log.NewStdoutLogger(logLevel); logErr != nil {
			return fmt.Errorf("stdout logger fallback failed after file logger error: %w", logErr)
		}
		log.WithError(err).WithField("log_dir", logDir).Warn("File logging disabled")
	}
	return nil
}

// resolveDataDir determines the final DataDir value using the priority:
// flag > DATA_DIR env > config value.
// All non-empty paths are resolved to absolute paths.
func resolveDataDir(flagVal, configVal string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	if envDir := os.Getenv("DATA_DIR"); envDir != "" {
		return filepath.Abs(envDir)
	}
	if configVal != "" {
		return filepath.Abs(configVal)
	}
	return configVal, nil
}

// errDataDirMissing marks a data_dir that does not exist on a read-only CLI
// surface. In the container that means the volume is not mounted.
var errDataDirMissing = errors.New("data directory does not exist")

// validateDataDir is the writer-path check used by the normal start and
// --check-config: it creates data_dir 0700 when missing, tightens its mode and
// then probes that it is writable.
func validateDataDir(dataDir string) error {
	if dataDir == "" {
		return nil
	}

	if err := ensurePrivateRuntimeDir(dataDir); err != nil {
		return err
	}

	return probeDataDirWritable(dataDir)
}

// validateReadOnlyDataDir checks data_dir for the CLI surfaces that must not
// change it (--healthcheck, --list-dead-letter). It never creates and never
// chmods the directory; the write probe is deliberately kept, because the bot
// itself needs data_dir writable and a read-only bind mount must still fail.
func validateReadOnlyDataDir(dataDir string) error {
	if dataDir == "" {
		return nil
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", errDataDirMissing, dataDir)
		}
		return fmt.Errorf("stat %q: %w", dataDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dataDir)
	}

	return probeDataDirWritable(dataDir)
}

// probeDataDirWritable is the create/close/remove write test both validators
// share.
func probeDataDirWritable(dataDir string) error {
	tempFile, err := os.CreateTemp(dataDir, ".ransomware-bot-write-test-*")
	if err != nil {
		return fmt.Errorf("write test in %q: %w", dataDir, err)
	}
	tempName := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("close write test file %q: %w", tempName, err)
	}
	if err := os.Remove(tempName); err != nil {
		return fmt.Errorf("remove write test file %q: %w", tempName, err)
	}
	return nil
}

// probeLockAcquireFunc is a var, not a direct call to filelock.Acquire, so
// tests can inject the ENOLCK/EINVAL classification without a real
// lock-unsupported filesystem.
var probeLockAcquireFunc = filelock.Acquire

// probeDataDirLockingUnsupported reports whether data_dir's filesystem lacks
// advisory locking support (ENOLCK/EINVAL). It locks its own throwaway temp
// file, never the real data_dir lock file a running instance may hold, so it
// can never contend with -- or be mistaken for -- that lock: on a normal
// filesystem this always succeeds instantly, lock or no live instance. Any
// outcome other than the classified condition is treated as "supported"; an
// unrelated I/O problem is already reported by the data_dir validation this
// runs after. Best-effort only: a failure to even create the temp file (for
// example a read-only bind mount, already caught above) reports "supported"
// rather than adding a second, redundant failure mode here.
func probeDataDirLockingUnsupported(dataDir string) bool {
	tempFile, err := os.CreateTemp(dataDir, ".ransomware-bot-lock-test-*")
	if err != nil {
		return false
	}
	tempName := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempName)
	}()

	lockErr := probeLockAcquireFunc(tempFile)
	if lockErr == nil {
		_ = filelock.Release(tempFile)
		return false
	}
	return filelock.IsUnsupported(lockErr)
}

func readinessMarkerPath() string {
	return os.Getenv(readinessFileEnv)
}

// writeReadinessMarker writes the marker via writeProgressMarkerFile's
// temp-file-plus-rename mechanism (the same one the per-poller progress
// markers use) instead of a direct MkdirAll + WriteFile. A process that dies
// between creating the directory and finishing the write -- disk full, an
// I/O error -- can then only ever leave (or fail to remove) a temp file
// beside the marker; the real path is only ever touched by the final atomic
// rename, so it is either the previous good marker or the new one, never a
// partially-written mix of both.
func writeReadinessMarker() error {
	path := readinessMarkerPath()
	if path == "" {
		return nil
	}
	content := fmt.Sprintf("pid=%d\nready_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := writeProgressMarkerFile(path, content); err != nil {
		return fmt.Errorf("write readiness marker %q: %w", path, err)
	}
	return nil
}

// progressMarkers writes one file per poller recording when that poller last
// completed a pass, and the staleness allowance the running configuration
// implied at that moment. Writing the allowance into the file is what makes a
// poll-interval change safe: --healthcheck never has to guess which cadence the
// running process is actually using.
type progressMarkers struct {
	apiPath string
	rssPath string
}

// newProgressMarkers returns nil when the readiness marker mechanism is unused.
func newProgressMarkers() *progressMarkers {
	base := readinessMarkerPath()
	if base == "" {
		return nil
	}
	return &progressMarkers{
		apiPath: base + progressMarkerSuffixAPI,
		rssPath: base + progressMarkerSuffixRSS,
	}
}

func (m *progressMarkers) RecordAPIPass(staleAfter time.Duration) {
	m.write(m.apiPath, staleAfter)
}

// RecordRSSPass additionally records budgetOverrun: whether the pass being
// recorded ended because rss_check_timeout expired before every feed
// answered. --healthcheck reads this back to tell "the data directory cannot
// persist RSS state" apart from "rss_check_timeout is too small for these
// feeds" (F1) -- both otherwise look identical from outside the process: a
// permanently empty rss_status.json.
func (m *progressMarkers) RecordRSSPass(staleAfter time.Duration, budgetOverrun bool) {
	content := fmt.Sprintf("at=%s\nstale_after=%s\nbudget_overrun=%t\n",
		time.Now().UTC().Format(time.RFC3339), staleAfter.String(), budgetOverrun)
	if err := writeProgressMarkerFile(m.rssPath, content); err != nil {
		log.WithError(err).WithField("marker", m.rssPath).Warn("Failed to write poller progress marker")
	}
}

// Clear removes both markers. The scheduler calls it only after both poller
// goroutines have exited, so no late tick can recreate a marker afterwards.
func (m *progressMarkers) Clear() {
	for _, path := range []string{m.apiPath, m.rssPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.WithError(err).WithField("marker", path).Warn("Failed to remove poller progress marker")
		}
	}
}

func (m *progressMarkers) write(path string, staleAfter time.Duration) {
	content := fmt.Sprintf("at=%s\nstale_after=%s\n",
		time.Now().UTC().Format(time.RFC3339), staleAfter.String())
	if err := writeProgressMarkerFile(path, content); err != nil {
		log.WithError(err).WithField("marker", path).Warn("Failed to write poller progress marker")
	}
}

// writeProgressMarkerFile replaces path atomically. --healthcheck parses the
// content from another process, so a truncate-then-write would let it read a
// half-written file; this mirrors internal/status/json_store.go's pattern.
func writeProgressMarkerFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, privateRuntimeDirMode); err != nil {
		return fmt.Errorf("create progress marker directory %q: %w", dir, err)
	}
	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp progress marker in %q: %w", dir, err)
	}
	tempName := tempFile.Name()
	if _, err := tempFile.WriteString(content); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempName)
		return fmt.Errorf("write temp progress marker %q: %w", tempName, err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("close temp progress marker %q: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("replace progress marker %q: %w", path, err)
	}
	return nil
}

// pollerProgress is one parsed progress marker. budgetOverrun is only ever
// set on the RSS marker (RecordRSSPass writes it; RecordAPIPass does not), and
// defaults to false -- exactly the right default for an API marker, which has
// no concept of a cycle-budget overrun.
type pollerProgress struct {
	at            time.Time
	staleAfter    time.Duration
	budgetOverrun bool
}

// readPollerProgress parses a progress marker. It reports false for a missing,
// unreadable or malformed file: that is a fault in this program, not an
// operator problem, and the staleness verdict fails open rather than restarting
// a container over it.
func readPollerProgress(path string) (pollerProgress, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path derives from the operator-set readiness marker env var
	if err != nil {
		return pollerProgress{}, false
	}
	var progress pollerProgress
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "at":
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return pollerProgress{}, false
			}
			progress.at = parsed
		case "stale_after":
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return pollerProgress{}, false
			}
			progress.staleAfter = parsed
		case "budget_overrun":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return pollerProgress{}, false
			}
			progress.budgetOverrun = parsed
		}
	}
	if progress.at.IsZero() || progress.staleAfter <= 0 {
		return pollerProgress{}, false
	}
	return progress, true
}

// checkPollerProgress reports a poller that has completed no pass for longer
// than its allowance. The allowance is the larger of the value the running
// process recorded and the value the configuration on disk implies, so neither
// raising nor lowering a poll interval can red a healthy bot: whichever side of
// the change is stale, the other one still covers it.
func checkPollerProgress(msg i18n.Messages, cfg *config.Config, now time.Time) error {
	base := readinessMarkerPath()
	if base == "" {
		return nil
	}
	pollers := []struct {
		name        string
		path        string
		fromCurrent time.Duration
	}{
		{"API", base + progressMarkerSuffixAPI, scheduler.APIProgressStaleAfter(cfg)},
		{"RSS", base + progressMarkerSuffixRSS, scheduler.RSSProgressStaleAfter(cfg)},
	}
	for _, poller := range pollers {
		progress, ok := readPollerProgress(poller.path)
		if !ok {
			continue
		}
		allowance := max(progress.staleAfter, poller.fromCurrent)
		if allowance <= 0 {
			continue
		}
		if age := now.Sub(progress.at); age > allowance {
			return fmt.Errorf(msg.T("health.poller_wedged"),
				poller.name, age.Round(time.Second),
				progress.at.UTC().Format(time.RFC3339), allowance)
		}
	}
	return nil
}

func removeReadinessMarker() error {
	path := readinessMarkerPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove readiness marker %q: %w", path, err)
	}
	return nil
}

func checkReadinessMarker(out io.Writer) error {
	return checkReadinessMarkerWithMessages(out, i18n.Default())
}

func checkReadinessMarkerWithMessages(out io.Writer, msg i18n.Messages) error {
	path := readinessMarkerPath()
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("scheduler readiness marker %q is missing", path)
		}
		return fmt.Errorf("stat readiness marker %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("readiness marker %q is a directory", path)
	}
	fmt.Fprintf(out, msg.T("health.readiness_marker"), path)
	return nil
}

func ensurePrivateRuntimeDir(path string) error {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		if err := os.MkdirAll(path, privateRuntimeDirMode); err != nil {
			return fmt.Errorf("create %q: %w", path, err)
		}
	} else if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, privateRuntimeDirMode); err != nil {
			return fmt.Errorf("chmod %q: %w", path, err)
		}
	}
	return nil
}

func enabledDeliveryTargets(cfg *config.Config) int {
	targets := 0
	for _, target := range config.WebhookTargets(cfg) {
		if target.Webhook.Enabled {
			targets++
		}
	}
	return targets
}

func runHealthcheck(configDir, dataDirFlag string, out io.Writer) error {
	return runHealthcheckWithMessages(configDir, dataDirFlag, out, i18n.Default())
}

func runHealthcheckWithMessages(configDir, dataDirFlag string, out io.Writer, msg i18n.Messages) error {
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		return fmt.Errorf("config directory %q does not exist", configDir)
	}

	cfg, err := config.LoadConfigStrict(configDir)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	resolved, err := resolveDataDir(dataDirFlag, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data_dir: %w", err)
	}
	if err := validateReadOnlyDataDir(resolved); err != nil {
		if errors.Is(err, errDataDirMissing) {
			return fmt.Errorf(msg.T("health.data_dir_missing"), resolved)
		}
		return fmt.Errorf("data_dir: %w", err)
	}
	// Information, never a failure: a failing healthcheck restart-loops the
	// container, which is exactly the outage a fatal check here was rejected
	// for. Probed now (data_dir is confirmed to exist and be writable) and
	// printed later, alongside the other health.* summary lines, only on the
	// normal success path -- same placement as manifestSkipped below.
	lockingUnsupported := probeDataDirLockingUnsupported(resolved)
	// Before the readiness marker on purpose: a stale marker left by a killed
	// process would otherwise report GREEN while the bot refuses to start, and
	// a missing marker would mask the real cause. manifestSkipped is declared
	// here (not inside the "if pending" block) so it survives in scope down to
	// where it is printed, near the other health.* summary lines below.
	diff, pending, manifestSkipped := pendingDestinationRemap(cfg, resolved)
	if pending {
		return fmt.Errorf(msg.T("health.destination_remap"),
			destinationRemapKindText(msg, diff), diff.Movements())
	}
	if err := checkReadinessMarkerWithMessages(out, msg); err != nil {
		return err
	}
	if err := checkPollerProgress(msg, cfg, time.Now()); err != nil {
		return err
	}
	// Getting here with the marker mechanism in use means the marker exists,
	// which proves Start()'s initial checks -- the first RSS attempt included --
	// already ran and were persisted.
	readinessConfirmed := readinessMarkerPath() != ""

	tracker, err := status.NewReadOnlyTracker(resolved)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	summary := tracker.StatusSummary()
	if summary.DeadLetterItems > 0 {
		return fmt.Errorf(
			"dead-letter queue has %d terminal delivery failure(s); run --list-dead-letter",
			summary.DeadLetterItems,
		)
	}
	if ransomwareDeliveryEnabled(cfg) && summary.LastAPIError != "" {
		snapshot := tracker.APIStatusSnapshot()
		switch snapshot.LastStatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf(msg.T("health.api_auth_suspended"), snapshot.LastStatusCode,
				snapshot.LastCheck.UTC().Format(time.RFC3339), summary.LastAPIError)
		}
		return fmt.Errorf("ransomware API last check failed: %s", summary.LastAPIError)
	}

	verdict := evaluateRSSHealth(cfg, tracker, time.Now(), msg, readinessConfirmed)
	if verdict.err != nil {
		return verdict.err
	}

	fmt.Fprintf(out, msg.T("health.data_dir"), resolved)
	// Best-effort: this warning is only printed here, at the normal success
	// summary, so it is reached only when the dead-letter, API-auth-suspension
	// and RSS-health checks above all pass first. A corrupt manifest combined
	// with one of those other failures still reports that failure (unchanged
	// exit 1) without this warning on top -- the operator already gets an
	// exit-1 result to act on either way.
	if manifestSkipped != nil {
		fmt.Fprintf(out, msg.T("health.destination_manifest_unreadable"), manifestSkipped.Error())
	}
	fmt.Fprintf(out, msg.T("health.retry_queue_items"), summary.RetryQueueItems)
	fmt.Fprintf(out, msg.T("health.dead_letter_items"), summary.DeadLetterItems)
	if verdict.warn != "" {
		fmt.Fprint(out, verdict.warn)
	}
	if lockingUnsupported {
		fmt.Fprint(out, msg.T("health.data_dir_lock_unsupported"))
	}
	return nil
}

// rssStaleSuccessIntervals is how many rss_poll_interval periods may pass
// without a single successful RSS poll before the container is called
// unhealthy. Derived from the existing rss_poll_interval on purpose; there is
// no operator key for it.
const rssStaleSuccessIntervals = 3

// rssHealthVerdict is the RSS part of the healthcheck. err != nil means exit 1;
// warn is a line printed with exit 0.
type rssHealthVerdict struct {
	err  error
	warn string
}

// evaluateRSSHealth turns persisted per-feed health into a verdict. One failing
// feed out of many is a warning, never unhealthy, and neither is a transient
// blip that fails every feed at once: the check only goes red once RSS has
// produced no successful poll for three poll intervals.
//
// It prints counts, a duration, an RFC3339 timestamp and one already-classified
// operator error string that the log emits verbatim today; it never formats a
// feed URL, a webhook URL or a host itself. StatusSummary.LastRSSError is
// deliberately not used: it is picked by Go map iteration order.
// rssNeverPolledVerdict decides the verdict for evaluateRSSHealth's known == 0
// case: every enabled feed still has no record in rss_status.json at all.
// Split out of evaluateRSSHealth to keep that function's cyclomatic complexity
// under the lint threshold (F1 added a third branch here); the logic itself is
// unchanged from what evaluateRSSHealth used to do inline.
//
// Gate 1 (readinessConfirmed): the scheduler cannot be proven to have finished
// starting yet -- first-start grace. Gate 2 (readRSSProgressMarker): no
// completed RSS pass recorded yet, or the marker mechanism/file is
// unreadable -- fail open, same as every other marker read in this file.
// Gate 3 (budgetOverrun): the last completed pass never reached "genuinely
// found nothing" -- its own rss_check_timeout expired before any feed
// answered (internal/rss's resultsOnContextError can hand back a non-nil,
// empty result for exactly this reason). Reported distinctly from the
// data_dir case below, and self-corrects the moment a pass completes inside
// its budget (F1). Otherwise: the last completed pass finished within its
// budget and still recorded nothing for any enabled feed, so a permanently
// empty rss_status.json points at a data_dir the bot cannot write to.
func rssNeverPolledVerdict(cfg *config.Config, msg i18n.Messages, enabled []string, readinessConfirmed bool) rssHealthVerdict {
	if !readinessConfirmed {
		return rssHealthVerdict{}
	}
	progress, ok := readRSSProgressMarker()
	if !ok {
		return rssHealthVerdict{}
	}
	if progress.budgetOverrun {
		return rssHealthVerdict{err: fmt.Errorf(msg.T("health.rss_budget_overrun"),
			len(enabled), cfg.RSSCheckTimeout, cfg.RSSWorkerTimeout)}
	}
	return rssHealthVerdict{err: fmt.Errorf(msg.T("health.rss_never_polled"), len(enabled))}
}

func evaluateRSSHealth(cfg *config.Config, tracker *status.Tracker, now time.Time, msg i18n.Messages, readinessConfirmed bool) rssHealthVerdict {
	if cfg == nil || tracker == nil {
		return rssHealthVerdict{}
	}
	enabled := config.EnabledRSSFeedURLs(cfg)
	if len(enabled) == 0 {
		return rssHealthVerdict{}
	}

	known, failed := 0, 0
	firstError := ""
	var newestSuccess time.Time
	for _, feedURL := range enabled {
		info, ok := tracker.GetRSSFeedInfo(feedURL)
		if !ok {
			continue
		}
		known++
		if info.LastError != nil {
			failed++
			if firstError == "" {
				firstError = *info.LastError
			}
		}
		if info.LastSuccess != nil && info.LastSuccess.After(newestSuccess) {
			newestSuccess = *info.LastSuccess
		}
	}
	if known == 0 {
		return rssNeverPolledVerdict(cfg, msg, enabled, readinessConfirmed)
	}

	allowed := time.Duration(rssStaleSuccessIntervals) * cfg.RSSPollInterval
	stale := cfg.RSSPollInterval > 0 && (newestSuccess.IsZero() || now.Sub(newestSuccess) > allowed)

	if stale && failed == known && known == len(enabled) {
		return rssHealthVerdict{err: fmt.Errorf(msg.T("health.rss_all_feeds_failed"), known, firstError)}
	}
	if stale {
		return rssHealthVerdict{err: fmt.Errorf(msg.T("health.rss_no_recent_success"),
			rssLastSuccessText(newestSuccess, msg), allowed, failed, known)}
	}
	if failed > 0 {
		return rssHealthVerdict{warn: fmt.Sprintf(msg.T("health.rss_feeds_degraded"),
			failed, known, rssLastSuccessText(newestSuccess, msg))}
	}
	return rssHealthVerdict{}
}

// readRSSProgressMarker reads the RSS poller's own progress marker, which is
// the ground truth for whether its most recently completed pass ended in a
// cycle-budget overrun (F1). It reports false for an unconfigured marker
// mechanism or a marker that is missing, unreadable or malformed -- the same
// fail-open contract readPollerProgress already has everywhere else.
func readRSSProgressMarker() (pollerProgress, bool) {
	base := readinessMarkerPath()
	if base == "" {
		return pollerProgress{}, false
	}
	return readPollerProgress(base + progressMarkerSuffixRSS)
}

func rssLastSuccessText(lastSuccess time.Time, msg i18n.Messages) string {
	if lastSuccess.IsZero() {
		return msg.T("health.rss_never")
	}
	return lastSuccess.UTC().Format(time.RFC3339)
}

func ransomwareDeliveryEnabled(cfg *config.Config) bool {
	for _, target := range config.WebhookTargetsForType(cfg, config.WebhookTypeRansomware) {
		if target.Webhook.Enabled {
			return true
		}
	}
	return false
}

func runListDeadLetter(configDir, dataDirFlag string, out io.Writer) error {
	return runListDeadLetterWithMessages(configDir, dataDirFlag, out, i18n.Default())
}

func runListDeadLetterWithMessages(configDir, dataDirFlag string, out io.Writer, msg i18n.Messages) error {
	configDataDir := ""
	if dataDirFlag == "" {
		if _, err := os.Stat(configDir); os.IsNotExist(err) {
			return fmt.Errorf("config directory %q does not exist and --data-dir was not provided", configDir)
		}
		cfg, err := config.LoadConfig(configDir)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		configDataDir = cfg.DataDir
	}

	resolved, err := resolveDataDir(dataDirFlag, configDataDir)
	if err != nil {
		return fmt.Errorf("resolve data_dir: %w", err)
	}
	if err := validateReadOnlyDataDir(resolved); err != nil {
		return fmt.Errorf("data_dir: %w", err)
	}

	tracker, err := status.NewReadOnlyTracker(resolved)
	if err != nil {
		return err
	}
	writeDeadLetterListWithMessages(out, resolved, tracker.GetDeadLetterItems(), msg)
	return nil
}

func writeDeadLetterList(out io.Writer, dataDir string, items []status.DeadLetterEntry) {
	writeDeadLetterListWithMessages(out, dataDir, items, i18n.Default())
}

func writeDeadLetterListWithMessages(out io.Writer, dataDir string, items []status.DeadLetterEntry, msg i18n.Messages) {
	fmt.Fprintf(out, msg.T("dead_letter.items"), len(items))
	fmt.Fprintf(out, msg.T("dead_letter.data_dir"), dataDir)
	if len(items) == 0 {
		return
	}

	for i, item := range items {
		title := item.Title
		if title == "" {
			title = msg.T("dead_letter.untitled")
		}
		fmt.Fprintf(out, "\n%d. [%s/%s] %s\n", i+1, item.Messenger, item.ItemType, title)
		fmt.Fprintf(out, msg.T("dead_letter.key"), item.ItemKey)
		if item.DestinationID != "" {
			fmt.Fprintf(out, msg.T("dead_letter.destination_id"), item.DestinationID)
		}
		fmt.Fprintf(out, msg.T("dead_letter.dead_at"), item.DeadAt)
		fmt.Fprintf(out, msg.T("dead_letter.reason"), item.TerminalReason)
		fmt.Fprintf(out, msg.T("dead_letter.retry_count"), item.RetryCount)
		fmt.Fprintf(out, msg.T("dead_letter.last_error"), item.LastError)
		if len(item.Payload) > 0 {
			fmt.Fprintln(out, msg.T("dead_letter.replay_payload"))
		}
	}
}
