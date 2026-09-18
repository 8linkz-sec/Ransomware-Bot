package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/discord"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/quiethours"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/slack"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

// APIClient is the scheduler-owned port for reading ransomware entries.
type APIClient interface {
	GetLatestEntries(context.Context) ([]model.RansomwareEntry, error)
	Close() error
}

// RSSParser is the scheduler-owned port for parsing configured RSS feeds.
type RSSParser interface {
	ParseMultipleFeedsWithValidators(context.Context, []string, int, map[string]rss.FeedHTTPValidators) (*rss.FeedResults, error)
}

// WebhookSender is the scheduler-owned port for webhook delivery.
type WebhookSender interface {
	SendRansomwareEntry(context.Context, string, model.RansomwareEntry, *notifyfmt.FormatOptions) error
	SendRSSEntry(context.Context, string, model.RSSEntry, string, *notifyfmt.FormatOptions) error
	Close() error
}

// ConfigReloader is the scheduler-owned port for config reload detection.
type ConfigReloader interface {
	Interval() time.Duration
	Check() (*config.Config, bool, error)
	Reload() (*config.Config, error)
	MarkApplied()
}

// Dependencies contains the concrete runtime dependencies consumed by the
// scheduler. New is the production factory; NewWithDependencies is the
// injection boundary for tests and controlled wiring.
type Dependencies struct {
	APIClient            APIClient
	RSSParser            RSSParser
	DiscordWebhookSender WebhookSender
	SlackWebhookSender   WebhookSender
	StatusTracker        *status.Tracker
	DataDirLock          *status.DataDirLock
	FeedTypeMap          map[string]string
	ConfigReloader       ConfigReloader
}

const (
	schedulerShutdownTimeout    = 30 * time.Second
	schedulerForcedShutdownWait = 2 * time.Second

	pollTypeAPI = "api"
	pollTypeRSS = "rss"

	// dryRunModeLabel is the "mode" field value logged wherever a dry run
	// substitutes a preview for an actual delivery.
	dryRunModeLabel = "DRY-RUN"
)

func newPollLogFields(pollType string) log.Fields {
	return log.Fields{
		"poll_type": pollType,
		"run_id":    fmt.Sprintf("%s-%d", pollType, time.Now().UTC().UnixNano()),
	}
}

func mergeLogFields(fields ...log.Fields) log.Fields {
	merged := log.Fields{}
	for _, fieldSet := range fields {
		for key, value := range fieldSet {
			merged[key] = value
		}
	}
	return merged
}

// The destination ID helpers live in internal/config so the destinations.json
// manifest is built from the identical strings the delivery path sends with.
// These wrappers keep the scheduler call sites unchanged.

func apiDestinationID(messenger status.Messenger) string {
	return config.APIDestinationID(messenger)
}

func apiDestinationIDForTarget(messenger status.Messenger, suffix string) string {
	return config.APIDestinationIDForTarget(messenger, suffix)
}

func appendDestinationSuffix(base, suffix string) string {
	return config.AppendDestinationSuffix(base, suffix)
}

func messengerForWebhookPlatform(platform string) (status.Messenger, bool) {
	return config.MessengerForWebhookPlatform(platform)
}

func apiHTTPPolicyForConfig(cfg *config.Config) api.HTTPPolicy {
	return api.HTTPPolicy{
		RequestTimeout: cfg.APIRequestTimeout,
		MaxAttempts:    cfg.APIMaxRetries,
		RetryBaseDelay: cfg.APIRetryDelay,
	}
}

func shouldLoadAPIStatusAtStartup(cfg *config.Config) bool {
	return cfg != nil && hasActiveRansomwareWebhook(cfg)
}

func dataDirAlreadyLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already locked")
}

// dataDirLockUnsupported reports whether AcquireDataDirLock failed because the
// data_dir's filesystem does not support advisory locking at all (ENOLCK/
// EINVAL, or the wider errors.ErrUnsupported class -- ENOSYS/EOPNOTSUPP/
// ENOTSUP -- that some 9p and CIFS/SMB mounts return for the same reason), as
// opposed to the lock being held by another live instance
// (dataDirAlreadyLocked, fatal) or an unrelated I/O error (the existing
// generic warning). A FUSE mount (including an Unraid /mnt/user/... share) is
// not reliably covered by any of this -- see filelock.IsUnsupported's doc
// comment. This condition is deliberately non-fatal: see
// dataDirLockUnsupportedWarning.
func dataDirLockUnsupported(err error) bool {
	return err != nil && filelock.IsUnsupported(err)
}

// dataDirLockUnsupportedWarning is the single, prominent startup WARN for the
// dataDirLockUnsupported condition. The bot keeps starting rather than
// refusing -- refusing would turn a working install into a dead one on an
// update, for a risk that may never apply to that deployment -- so this
// message is the loud, operator-facing signal in place of that refusal. It
// names the concrete consequence (no protection against a second instance)
// and, conditioned on the log directory sharing the same unlockable mount
// with data_dir (true for the shipped docker-compose.yml, not guaranteed in
// general), the fact that the log owner lock degrades identically and this
// very message then becomes stdout-only.
const dataDirLockUnsupportedWarning = "Data directory's filesystem does not support file locking (ENOLCK/EINVAL); " +
	"nothing prevents a second ransomware-bot instance from writing the same data_dir. " +
	"If the log directory shares that same unlockable mount with data_dir -- true for the shipped " +
	"docker-compose.yml, where ./logs and ./data are siblings, but not guaranteed in general -- the log " +
	"file's owner lock degrades the same way: file logging falls back to stdout only, bot.log stays " +
	"empty, and this very warning is then visible only on stdout (docker logs), never in bot.log. Check " +
	"stdout either way. Move data_dir to a filesystem that supports locking; on Unraid, use /mnt/cache " +
	"or a disk share, not /mnt/user."

// acquireDataDirLockFunc is a var, not a direct call to status.AcquireDataDirLock,
// so tests can inject a classified lock failure (ENOLCK/EINVAL) without a real
// lock-unsupported filesystem.
var acquireDataDirLockFunc = status.AcquireDataDirLock

func webhookHTTPPolicyForConfig(cfg *config.Config) webhookhttp.Policy {
	return webhookhttp.Policy{
		RequestTimeout: cfg.WebhookRequestTimeout,
		MaxAttempts:    cfg.WebhookMaxRetries,
		RetryBaseDelay: cfg.WebhookRetryDelay,
	}
}

func statusRetentionPolicyForConfig(cfg *config.Config) status.RetentionPolicy {
	if cfg == nil {
		return status.DefaultRetentionPolicy()
	}
	retention := cfg.StatusRetention
	compress := retention.AuditLogRotation.Compress
	return status.RetentionPolicy{
		MaxAPISentItems:    retention.MaxAPISentItems,
		MaxRSSParsedItems:  retention.MaxRSSParsedItems,
		RSSParsedMaxAge:    retention.RSSParsedMaxAge,
		MaxRSSSentItems:    retention.MaxRSSSentItems,
		MaxRetryQueueItems: retention.MaxRetryQueueItems,
		RetryQueueMaxAge:   retention.RetryQueueMaxAge,
		MaxDeadLetterItems: retention.MaxDeadLetterItems,
		DeadLetterMaxAge:   retention.DeadLetterMaxAge,
		AuditLogMaxSizeMB:  retention.AuditLogRotation.MaxSizeMB,
		AuditLogMaxBackups: retention.AuditLogRotation.MaxBackups,
		AuditLogMaxAgeDays: retention.AuditLogRotation.MaxAgeDays,
		AuditLogCompress:   &compress,
	}
}

// apiAuthNow is the clock for the API auth-suspension back-off; tests replace it
// to exercise the re-probe schedule without sleeping.
var apiAuthNow = time.Now

// Back-off bounds for the API authentication suspension. Deliberately constants
// and not config keys: the repo exposes no operator key for cooldowns.
const (
	apiAuthSuspensionBase = 15 * time.Minute
	apiAuthSuspensionMax  = 6 * time.Hour

	apiAuthSuspensionLiftsOn = "api_key/api_base_url change, restart, or automatic re-probe"
)

// apiAuthSuspension is the in-memory 401/403 latch. Not persisted on purpose: a
// restart re-probes at once, which is what an operator who just rotated the key expects.
type apiAuthSuspension struct {
	key        string
	since      time.Time
	nextProbe  time.Time
	failures   int
	statusCode int
}

// nextAPIAuthProbeAfter returns the next re-probe time for the given number of
// consecutive authentication failures: 15m, 30m, 1h, 2h, 4h, then 6h forever.
func nextAPIAuthProbeAfter(now time.Time, failures int) time.Time {
	delay := apiAuthSuspensionBase
	for i := 1; i < failures; i++ {
		if delay *= 2; delay >= apiAuthSuspensionMax {
			delay = apiAuthSuspensionMax
			break
		}
	}
	return now.Add(delay)
}

// apiAuthSuspensionFields describes the current suspension for operators.
func (s *Scheduler) apiAuthSuspensionFields() log.Fields {
	return log.Fields{
		"status_code":               s.apiAuthSuspension.statusCode,
		"consecutive_auth_failures": s.apiAuthSuspension.failures,
		"suspended_since":           s.apiAuthSuspension.since.Format(time.RFC3339),
		"next_probe_at":             s.apiAuthSuspension.nextProbe.Format(time.RFC3339),
		"lifts_on":                  apiAuthSuspensionLiftsOn,
	}
}

// armAPIAuthSuspension (re-)arms the latch and logs exactly one WARN per arming.
// The start time is kept while the same api_key keeps failing.
func (s *Scheduler) armAPIAuthSuspension(key string, now time.Time, err error, pollFields log.Fields) {
	if s.apiAuthSuspension.key != key {
		s.apiAuthSuspension = apiAuthSuspension{key: key, since: now}
	}
	s.apiAuthSuspension.failures++
	s.apiAuthSuspension.statusCode = apiAuthStatusCode(err)
	s.apiAuthSuspension.nextProbe = nextAPIAuthProbeAfter(now, s.apiAuthSuspension.failures)

	log.WithFields(mergeLogFields(pollFields, s.apiAuthSuspensionFields())).
		Warn("Ransomware API polling suspended after authentication failure")
}

// apiAuthStatusCode returns the HTTP status carried by an API error, or 0.
func apiAuthStatusCode(err error) int {
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	return 0
}

func apiClientSettingsChanged(oldCfg, newCfg *config.Config) bool {
	return oldCfg.APIKey != newCfg.APIKey ||
		oldCfg.APIBaseURL != newCfg.APIBaseURL ||
		oldCfg.APIRequestTimeout != newCfg.APIRequestTimeout ||
		oldCfg.APIMaxRetries != newCfg.APIMaxRetries ||
		oldCfg.APIRetryDelay != newCfg.APIRetryDelay
}

func recoverSchedulerPanic(component string) {
	if recovered := recover(); recovered != nil {
		log.WithFields(log.Fields{
			"component": component,
			"panic":     fmt.Sprint(recovered),
			"stack":     string(debug.Stack()),
		}).Error("Recovered panic in scheduler goroutine")
	}
}

// checkOutcome is what one poll pass reports back to its caller. "ran" is the
// liveness signal: false only when the pass never started because the previous
// one is still in progress, which is exactly the wedge the readiness surface
// must detect. "ok" is the outcome signal used by --dry-run. "rssBudgetOverrun"
// is set only by checkRSSOnce (unused by checkAPIOnce): it reports whether the
// pass ended because its rss_check_timeout budget expired before every feed
// answered -- the same condition logRSSBatchParseError already distinguishes
// from a genuine parse/fetch failure. --healthcheck needs this to tell "the
// data directory cannot persist RSS state" apart from "rss_check_timeout is
// too small for these feeds" instead of reporting the former for both (F1).
type checkOutcome struct {
	ran              bool
	ok               bool
	rssBudgetOverrun bool
}

// ProgressRecorder persists that a poller completed a pass, together with the
// staleness allowance the running configuration implies. Implemented in main.go
// by the per-poller progress markers. Each method is called from exactly one
// goroutine (RecordAPIPass from the API poller, RecordRSSPass from the RSS
// poller), and Clear only after both have exited, so implementations need no
// locking of their own. RecordRSSPass's budgetOverrun reports whether the pass
// it is recording ended in a cycle-budget overrun, so a reader across a process
// boundary (--healthcheck) can tell that apart from a pass that completed
// within budget and still found nothing.
type ProgressRecorder interface {
	RecordAPIPass(staleAfter time.Duration)
	RecordRSSPass(staleAfter time.Duration, budgetOverrun bool)
	Clear()
}

// readinessStaleIntervals is how many poll cycles may pass without a completed
// pass before a poller is called wedged. A "cycle" is the poll interval plus
// the worst-case duration of one pass, so a slow-but-healthy pass can never
// trip it.
const readinessStaleIntervals = 3

// APIProgressStaleAfter is the allowance for the API poller: three cycles of
// one poll interval plus one pass. The "2 *" below is a floor, not the true
// worst case: processAPIDeliveryTarget (api_delivery.go) gives each enabled
// ransomware delivery destination its own api_check_timeout budget, so one
// pass can cost up to (2 + N) * api_check_timeout -- one for the fetch, one
// per destination for delivery, one for the retry queue. Counting only 2
// undercounts that true bound for N > 0, but the x3 multiplier
// (readinessStaleIntervals) absorbs any realistic N, so this is not a
// false-red risk in the deployments this bot actually runs (F9).
func APIProgressStaleAfter(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.APIPollInterval <= 0 {
		return 0
	}
	return readinessStaleIntervals * (cfg.APIPollInterval + 2*apiCheckTimeout(cfg))
}

// RSSProgressStaleAfter is the allowance for the RSS poller: three cycles of
// one poll interval plus one pass, which rss_check_timeout bounds in full.
func RSSProgressStaleAfter(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.RSSPollInterval <= 0 {
		return 0
	}
	return readinessStaleIntervals * (cfg.RSSPollInterval + rssCheckTimeout(cfg))
}

func runSchedulerComponent(component string, fn func()) {
	defer recoverSchedulerPanic(component)
	fn()
}

// Scheduler manages the execution of API polling and RSS feed checking
type Scheduler struct {
	config               *config.Config
	apiClient            APIClient
	rssParser            RSSParser
	discordWebhookSender WebhookSender
	slackWebhookSender   WebhookSender
	statusTracker        *status.Tracker
	dataDirLock          *status.DataDirLock

	// Feed type lookup map for O(1) categorization
	feedTypeMap map[string]string

	// Channels for graceful shutdown
	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Tickers for scheduled tasks
	apiTicker *time.Ticker
	rssTicker *time.Ticker

	// Guards against overlapping runs (only one check at a time per type)
	apiMu sync.Mutex
	rssMu sync.Mutex

	webhookLocksMu sync.Mutex
	webhookLocks   map[string]*sync.Mutex

	apiAuthSuspension apiAuthSuspension

	// Config hot-reload support
	configReloader ConfigReloader
	configMu       sync.RWMutex

	// Dry-run mode: log messages instead of sending
	dryRun  bool
	started bool

	// progressRecorder, when set, records each completed API or RSS pass.
	// Must be set before Start(); read without a lock, which is safe only
	// because it is written once, before the poller goroutines exist.
	progressRecorder ProgressRecorder
}

// SetProgressRecorder registers the sink for per-poller progress. Must be
// called before Start(). A nil recorder disables the mechanism.
func (s *Scheduler) SetProgressRecorder(r ProgressRecorder) {
	s.progressRecorder = r
}

func (s *Scheduler) recordAPIPass(cfg *config.Config) {
	if s.progressRecorder == nil || s.dryRun {
		return
	}
	s.progressRecorder.RecordAPIPass(APIProgressStaleAfter(cfg))
}

func (s *Scheduler) recordRSSPass(cfg *config.Config, budgetOverrun bool) {
	if s.progressRecorder == nil || s.dryRun {
		return
	}
	s.progressRecorder.RecordRSSPass(RSSProgressStaleAfter(cfg), budgetOverrun)
}

// New creates a new scheduler instance
func New(cfg *config.Config, configDir string, dryRun bool) (*Scheduler, error) {
	components, err := newSchedulerComponents(cfg, configDir, dryRun)
	if err != nil {
		return nil, err
	}

	return newWithComponents(cfg, dryRun, components), nil
}

// NewWithDependencies creates a scheduler from supplied dependencies.
func NewWithDependencies(cfg *config.Config, _ string, dryRun bool, deps Dependencies) (*Scheduler, error) {
	components, err := schedulerComponentsFromDependencies(cfg, deps)
	if err != nil {
		return nil, err
	}
	return newWithComponents(cfg, dryRun, components), nil
}

type schedulerComponents struct {
	apiClient            APIClient
	rssParser            RSSParser
	discordWebhookSender WebhookSender
	slackWebhookSender   WebhookSender
	statusTracker        *status.Tracker
	dataDirLock          *status.DataDirLock
	feedTypeMap          map[string]string
	configReloader       ConfigReloader
}

func schedulerComponentsFromDependencies(cfg *config.Config, deps Dependencies) (*schedulerComponents, error) {
	if deps.APIClient == nil {
		return nil, errors.New("scheduler dependencies missing API client")
	}
	if deps.RSSParser == nil {
		return nil, errors.New("scheduler dependencies missing RSS parser")
	}
	if deps.DiscordWebhookSender == nil {
		return nil, errors.New("scheduler dependencies missing Discord webhook sender")
	}
	if deps.SlackWebhookSender == nil {
		return nil, errors.New("scheduler dependencies missing Slack webhook sender")
	}
	if deps.StatusTracker == nil {
		return nil, errors.New("scheduler dependencies missing status tracker")
	}
	if deps.ConfigReloader == nil {
		return nil, errors.New("scheduler dependencies missing config reloader")
	}
	feedTypeMap := deps.FeedTypeMap
	if feedTypeMap == nil {
		feedTypeMap = buildFeedTypeMap(cfg)
	}
	return &schedulerComponents{
		apiClient:            deps.APIClient,
		rssParser:            deps.RSSParser,
		discordWebhookSender: deps.DiscordWebhookSender,
		slackWebhookSender:   deps.SlackWebhookSender,
		statusTracker:        deps.StatusTracker,
		dataDirLock:          deps.DataDirLock,
		feedTypeMap:          feedTypeMap,
		configReloader:       deps.ConfigReloader,
	}, nil
}

func newSchedulerComponents(cfg *config.Config, configDir string, dryRun bool) (*schedulerComponents, error) {
	apiClient, err := api.NewClientWithBaseURLAndPolicy(cfg.APIKey, cfg.APIBaseURL, apiHTTPPolicyForConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	components := &schedulerComponents{
		apiClient: apiClient,
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			components.close()
		}
	}()

	// Initialize status tracker FIRST (uses configured data directory outside dry-run)
	retentionPolicy := statusRetentionPolicyForConfig(cfg)
	if dryRun {
		components.statusTracker = status.NewMemoryTrackerWithRetention(retentionPolicy)
	} else {
		components.dataDirLock, err = acquireDataDirLockFunc(cfg.DataDir)
		if err != nil {
			if dataDirAlreadyLocked(err) {
				return nil, fmt.Errorf("failed to acquire data_dir lock: %w", err)
			}
			if dataDirLockUnsupported(err) {
				log.WithError(err).WithField("data_dir", cfg.DataDir).Warn(dataDirLockUnsupportedWarning)
			} else {
				log.WithError(err).WithField("data_dir", cfg.DataDir).Warn("Data directory unavailable; continuing with in-memory status only")
			}
			components.statusTracker = status.NewMemoryTrackerWithRetention(retentionPolicy)
		} else {
			if shouldLoadAPIStatusAtStartup(cfg) {
				components.statusTracker, err = status.NewTrackerWithRetention(cfg.DataDir, retentionPolicy)
			} else {
				components.statusTracker, err = status.NewTrackerWithRetentionLazyAPI(cfg.DataDir, retentionPolicy)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to create status tracker: %w", err)
			}
		}
	}

	// Initialize RSS parser; persistent deduplication is owned by the scheduler.
	components.rssParser = newRSSParserForConfig(cfg)

	// Initialize webhook senders
	webhookPolicy := webhookHTTPPolicyForConfig(cfg)
	components.discordWebhookSender, err = discord.NewWebhookSender(webhookPolicy)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord webhook sender: %w", err)
	}
	components.slackWebhookSender = slack.NewWebhookSender(webhookPolicy)

	// Build feed type lookup map for O(1) categorization
	components.feedTypeMap = buildFeedTypeMap(cfg)
	pruneRSSFeedStatusForConfig(components.statusTracker, cfg, dryRun)

	components.configReloader, err = config.NewReloader(configDir)
	if err != nil {
		log.WithError(err).Warn("Failed to capture initial config file signature")
	}

	cleanupOnError = false
	return components, nil
}

func (c *schedulerComponents) close() {
	if c == nil {
		return
	}
	if c.discordWebhookSender != nil {
		_ = c.discordWebhookSender.Close()
		c.discordWebhookSender = nil
	}
	if c.slackWebhookSender != nil {
		_ = c.slackWebhookSender.Close()
		c.slackWebhookSender = nil
	}
	if c.dataDirLock != nil {
		_ = c.dataDirLock.Release()
		c.dataDirLock = nil
	}
	if c.apiClient != nil {
		_ = c.apiClient.Close()
		c.apiClient = nil
	}
}

func newWithComponents(cfg *config.Config, dryRun bool, components *schedulerComponents) *Scheduler {
	scheduler := &Scheduler{
		config:               cfg,
		apiClient:            components.apiClient,
		rssParser:            components.rssParser,
		discordWebhookSender: components.discordWebhookSender,
		slackWebhookSender:   components.slackWebhookSender,
		statusTracker:        components.statusTracker,
		dataDirLock:          components.dataDirLock,
		webhookLocks:         make(map[string]*sync.Mutex),
		feedTypeMap:          components.feedTypeMap,
		stopChan:             make(chan struct{}),
		configReloader:       components.configReloader,
		dryRun:               dryRun,
	}
	return scheduler
}

// buildFeedTypeMap creates a lookup map for O(1) feed type categorization
func buildFeedTypeMap(cfg *config.Config) map[string]string {
	feedTypeMap := make(map[string]string)

	for _, route := range rssFeedRoutes(cfg) {
		for _, url := range route.feedURLs {
			feedTypeMap[url] = route.feedType
		}
	}

	return feedTypeMap
}

func newRSSParserForConfig(cfg *config.Config) *rss.Parser {
	return rss.NewParser(
		cfg.RSSRetryCount,
		cfg.RSSRetryDelay,
		cfg.RSSWorkerTimeout,
	)
}

func activeRSSFeedURLs(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}

	urls := make([]string, 0, len(cfg.Feeds.GeneralFeeds)+len(cfg.Feeds.GovernmentFeeds)+len(cfg.Feeds.RansomwareFeeds))
	for _, route := range rssFeedRoutes(cfg) {
		urls = append(urls, route.feedURLs...)
	}
	return urls
}

type rssFeedRouteDefinition struct {
	feedType    string
	webhookType string
	urls        func(*config.Config) []string
}

type rssFeedBatch struct {
	route    rssFeedRoute
	feedURLs []string
	targets  []webhookTarget
}

type rssFeedRoute struct {
	feedType    string
	webhookType string
	feedURLs    []string
}

var rssFeedRouteDefinitions = []rssFeedRouteDefinition{
	{
		feedType:    config.FeedTypeGeneral,
		webhookType: config.WebhookTypeRSS,
		urls: func(cfg *config.Config) []string {
			if cfg == nil {
				return nil
			}
			return cfg.Feeds.GeneralFeeds
		},
	},
	{
		feedType:    config.FeedTypeGovernment,
		webhookType: config.WebhookTypeGovernment,
		urls: func(cfg *config.Config) []string {
			if cfg == nil {
				return nil
			}
			return cfg.Feeds.GovernmentFeeds
		},
	},
	{
		feedType:    config.FeedTypeRansomware,
		webhookType: config.WebhookTypeRansomware,
		urls: func(cfg *config.Config) []string {
			if cfg == nil {
				return nil
			}
			return cfg.Feeds.RansomwareFeeds
		},
	},
}

func rssFeedRoutes(cfg *config.Config) []rssFeedRoute {
	routes := make([]rssFeedRoute, 0, len(rssFeedRouteDefinitions))
	for _, definition := range rssFeedRouteDefinitions {
		routes = append(routes, rssFeedRoute{
			feedType:    definition.feedType,
			webhookType: definition.webhookType,
			feedURLs:    definition.urls(cfg),
		})
	}
	return routes
}

func rssFeedRouteForType(cfg *config.Config, feedType string) (rssFeedRoute, bool) {
	for _, route := range rssFeedRoutes(cfg) {
		if route.feedType == feedType {
			return route, true
		}
	}
	return rssFeedRoute{}, false
}

func pruneRSSFeedStatusForConfig(tracker *status.Tracker, cfg *config.Config, dryRun bool) {
	if dryRun || tracker == nil {
		return
	}
	if removed := tracker.PruneRSSFeedStatus(activeRSSFeedURLs(cfg)); removed > 0 {
		if err := tracker.SavePendingChanges(); err != nil {
			log.WithError(err).Warn("Failed to persist pruned RSS feed status")
		}
	}
}

// getConfig returns the current config with read-lock protection.
func (s *Scheduler) getConfig() *config.Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

// errSchedulerAlreadyStarted is returned by a second Start() call on a
// Scheduler that was already started (whether still running or already
// stopped). Restarting a stopped scheduler is not supported: Stop() closes
// stopChan exactly once (guarded by stopOnce, which cannot be reset) and
// releases apiClient/the webhook senders/the data_dir lock, so a second
// Start() would spawn poller goroutines that exit immediately on the
// already-closed stopChan and create tickers nothing would ever stop again.
// Failing loudly here is deliberately simpler than making a restart actually
// work; every production caller (main.go) already calls Start() and Stop()
// each exactly once per process.
var errSchedulerAlreadyStarted = errors.New("scheduler: Start already called once; a running or already-stopped scheduler cannot be started again on the same instance")

// Start begins the scheduler operations
func (s *Scheduler) Start(ctx context.Context) error {
	if s.started {
		return errSchedulerAlreadyStarted
	}
	log.Info("Starting scheduler")

	s.cleanupStatusRetention()

	summary := s.statusTracker.StatusSummary()
	log.WithFields(log.Fields{
		"api_sent_items":    summary.APISentItems,
		"rss_feeds":         summary.RSSFeeds,
		"rss_feed_errors":   summary.RSSFeedErrors,
		"rss_parsed_items":  summary.RSSParsedItems,
		"rss_sent_items":    summary.RSSSentItems,
		"retry_queue_items": summary.RetryQueueItems,
		"dead_letter_items": summary.DeadLetterItems,
		"last_api_error":    summary.LastAPIError,
		"last_rss_error":    summary.LastRSSError,
	}).Info("Scheduler status summary")

	cfg := s.getConfig()

	log.Info("Running initial scheduler checks")
	var initialWG sync.WaitGroup
	initialWG.Add(2)
	go func() {
		defer initialWG.Done()
		runSchedulerComponent("initial API check", func() {
			if s.checkAPIOnce(ctx).ran {
				s.recordAPIPass(cfg)
			}
		})
	}()
	go func() {
		defer initialWG.Done()
		runSchedulerComponent("initial RSS check", func() {
			if outcome := s.checkRSSOnce(ctx); outcome.ran {
				s.recordRSSPass(cfg, outcome.rssBudgetOverrun)
			}
		})
	}()
	initialWG.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	log.Info("Initial scheduler checks completed")

	// Create tickers for periodic tasks
	s.apiTicker = time.NewTicker(cfg.APIPollInterval)
	s.rssTicker = time.NewTicker(cfg.RSSPollInterval)
	s.started = true

	// Start API polling goroutine
	s.wg.Add(1)
	go s.runAPIPoller(ctx)

	// Start RSS feed checking goroutine
	s.wg.Add(1)
	go s.runRSSChecker(ctx)

	// Start config watcher goroutine
	s.wg.Add(1)
	go s.runConfigWatcher(ctx)

	log.Info("Scheduler started successfully")
	return nil
}

// RunOnce performs a single API check and RSS check, then returns.
// Used for dry-run mode.
func (s *Scheduler) RunOnce(ctx context.Context) bool {
	log.Info("Running single cycle (dry-run mode)")
	// Both checks always run: assign first, combine second. A short-circuited
	// "return s.checkAPIOnce(ctx).ok && s.checkRSSOnce(ctx).ok" would silently
	// skip the RSS check whenever the API check failed.
	apiOutcome := s.checkAPIOnce(ctx)
	rssOutcome := s.checkRSSOnce(ctx)
	log.Info("Single cycle completed")
	return apiOutcome.ok && rssOutcome.ok
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() error {
	started := s.started
	if started {
		log.Info("Stopping scheduler")
	} else {
		log.Debug("Closing scheduler resources without active lifecycle")
	}

	s.signalStop()
	s.stopTickers()
	graceful := s.waitForWorkers(started)
	// Clear only after a graceful wait: waitForWorkers returning false means
	// schedulerShutdownTimeout expired with a poller goroutine still running,
	// which can still complete a pass and write a marker after Clear() ran,
	// recreating exactly the file this call is meant to remove. This is the
	// same forced-shutdown race already fixed once in this repo for
	// apiClient/the webhook senders in closeSchedulerResources below -- on
	// that path the corresponding resource is left for the process exit to
	// reclaim rather than touched unsynchronized.
	if graceful && s.progressRecorder != nil {
		s.progressRecorder.Clear()
	}

	shutdownErr := s.flushPendingStatus()

	if !graceful {
		// Wait a bit more for goroutines to notice closed channels before closing clients
		<-time.After(schedulerForcedShutdownWait)
	}

	shutdownErr = errors.Join(shutdownErr, s.closeSchedulerResources())

	if started {
		log.Info("Scheduler stopped")
	} else {
		log.Debug("Scheduler resources closed without active lifecycle")
	}
	return shutdownErr
}

func (s *Scheduler) signalStop() {
	s.stopOnce.Do(func() { close(s.stopChan) })
}

func (s *Scheduler) stopTickers() {
	if s.apiTicker != nil {
		s.apiTicker.Stop()
	}
	if s.rssTicker != nil {
		s.rssTicker.Stop()
	}
}

func (s *Scheduler) waitForWorkers(started bool) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverSchedulerPanic("scheduler stop waiter")
		s.wg.Wait()
	}()

	select {
	case <-done:
		if started {
			log.Info("All goroutines stopped gracefully")
		}
		return true
	case <-time.After(schedulerShutdownTimeout):
		log.Warn("Timeout waiting for goroutines to stop - forcing shutdown")
		return false
	}
}

func (s *Scheduler) flushPendingStatus() error {
	if s.dryRun {
		return nil
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		shutdownErr := fmt.Errorf("flush pending status changes: %w", err)
		log.WithError(shutdownErr).Error("Failed to flush pending status changes during shutdown")
		return shutdownErr
	}
	return nil
}

// closeSchedulerResources closes the resources owned by a stopped scheduler.
// It takes apiMu/rssMu via TryLock, mirroring checkAPIOnce/checkRSSOnce's own
// non-blocking pattern, instead of touching apiClient/the two webhook senders
// with no lock at all: those fields are read from inside checkAPIOnce's
// apiMu-guarded critical section (and the webhook senders from inside
// checkRSSOnce's rssMu-guarded one too), so writing them unlocked here raced
// a still-running cycle on the forced-shutdown path. A blocking Lock() would
// reintroduce the exact hang schedulerForcedShutdownWait exists to route
// around, so when a lock cannot be acquired immediately the corresponding
// resources are deliberately left open (not nil'd, not closed) with a WARN,
// rather than raced. This is safe: Stop() runs at most once per process,
// immediately before main.go's os.Exit, so an occasionally-unclosed idle
// connection pool is a no-op cost at process exit, and TryLock never blocks,
// so the 30s (schedulerShutdownTimeout) + 2s (schedulerForcedShutdownWait)
// shutdown budget and Stop()'s return timing are unchanged either way.
func (s *Scheduler) closeSchedulerResources() error {
	var shutdownErr error

	gotAPI := s.apiMu.TryLock()
	if gotAPI {
		defer s.apiMu.Unlock()
	} else {
		log.Warn("closeSchedulerResources: an API check is still in progress; leaving the API client open")
	}
	gotRSS := s.rssMu.TryLock()
	if gotRSS {
		defer s.rssMu.Unlock()
	} else {
		log.Warn("closeSchedulerResources: an RSS check is still in progress; leaving the shared webhook senders open")
	}

	if gotAPI && s.apiClient != nil {
		apiClient := s.apiClient
		s.apiClient = nil
		if err := apiClient.Close(); err != nil {
			log.WithError(err).Warn("Failed to close API client")
		}
	}
	// discordWebhookSender/slackWebhookSender are read from BOTH the API
	// delivery path (under apiMu) and the RSS delivery path (under rssMu),
	// so both locks must be held before nil-ing them.
	if gotAPI && gotRSS {
		if s.discordWebhookSender != nil {
			discordWebhookSender := s.discordWebhookSender
			s.discordWebhookSender = nil
			if err := discordWebhookSender.Close(); err != nil {
				log.WithError(err).Warn("Failed to close Discord webhook sender")
			}
		}
		if s.slackWebhookSender != nil {
			slackWebhookSender := s.slackWebhookSender
			s.slackWebhookSender = nil
			if err := slackWebhookSender.Close(); err != nil {
				log.WithError(err).Warn("Failed to close Slack webhook sender")
			}
		}
	}

	if s.dataDirLock != nil {
		// unaffected by this fix: no concurrent reader exists today.
		dataDirLock := s.dataDirLock
		s.dataDirLock = nil
		if err := dataDirLock.Release(); err != nil {
			log.WithError(err).Warn("Failed to release data_dir lock")
			if shutdownErr == nil {
				shutdownErr = fmt.Errorf("release data_dir lock: %w", err)
			}
		}
	}
	return shutdownErr
}

// runAPIPoller runs the API polling loop
func (s *Scheduler) runAPIPoller(ctx context.Context) {
	defer s.wg.Done()
	defer recoverSchedulerPanic("api poller")

	log.Info("API poller started")

	for {
		select {
		case <-ctx.Done():
			log.Info("API poller stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("API poller stopping due to stop signal")
			return
		case <-s.apiTicker.C:
			runSchedulerComponent("api poller tick", func() {
				if s.checkAPIOnce(ctx).ran {
					s.recordAPIPass(s.getConfig())
				}
			})
		}
	}
}

// runRSSChecker runs the RSS feed checking loop
func (s *Scheduler) runRSSChecker(ctx context.Context) {
	defer s.wg.Done()
	defer recoverSchedulerPanic("rss checker")

	log.Info("RSS checker started")

	for {
		select {
		case <-ctx.Done():
			log.Info("RSS checker stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("RSS checker stopping due to stop signal")
			return
		case <-s.rssTicker.C:
			runSchedulerComponent("rss checker tick", func() {
				if outcome := s.checkRSSOnce(ctx); outcome.ran {
					s.recordRSSPass(s.getConfig(), outcome.rssBudgetOverrun)
				}
			})
		}
	}
}

// runConfigWatcher polls the configured reload source for changes.
func (s *Scheduler) runConfigWatcher(ctx context.Context) {
	defer s.wg.Done()
	defer recoverSchedulerPanic("config watcher")

	ticker := time.NewTicker(s.configReloader.Interval())
	defer ticker.Stop()

	log.Info("Config watcher started")

	for {
		select {
		case <-ctx.Done():
			log.Info("Config watcher stopping due to context cancellation")
			return
		case <-s.stopChan:
			log.Info("Config watcher stopping due to stop signal")
			return
		case <-ticker.C:
			runSchedulerComponent("config watcher tick", func() {
				newCfg, needsReload, err := s.configReloader.Check()
				if err != nil {
					if needsReload {
						log.WithError(err).Error("Config reload failed, keeping current config")
					} else {
						log.WithError(err).Warn("Failed to check config file state")
					}
					return
				}
				if needsReload {
					log.Info("Config file change detected, reloading...")
					changed, err := s.applyReloadedConfig(newCfg)
					switch {
					case errors.Is(err, errDestinationRemapRejected):
						// The WARN is already logged. Consume the version so the
						// same rejected file is not re-warned on every tick.
						s.configReloader.MarkApplied()
					case err != nil:
						log.WithError(err).Error("Config reload failed, keeping current config")
					case changed:
						s.configReloader.MarkApplied()
						log.Info("Config reloaded successfully")
					}
				}
			})
		}
	}
}

// ReloadConfig loads config from disk and applies safe-to-reload fields.
func (s *Scheduler) ReloadConfig() (bool, error) {
	newCfg, err := s.configReloader.Reload()
	if err != nil {
		return false, fmt.Errorf("config reload failed validation: %w", err)
	}
	changed, err := s.applyReloadedConfig(newCfg)
	if errors.Is(err, errDestinationRemapRejected) {
		s.configReloader.MarkApplied()
		return false, err
	}
	if err != nil {
		return false, err
	}
	if changed {
		s.configReloader.MarkApplied()
	}
	return changed, nil
}

// errDestinationRemapRejected reports a reload whose webhook endpoints would be
// remapped onto existing destination IDs. Callers consume the config version
// with MarkApplied instead of logging an ERROR, so a rejected file is warned
// about exactly once rather than on every watcher tick.
var errDestinationRemapRejected = errors.New("config reload rejected: webhook endpoints were remapped onto existing destination IDs")

//nolint:gocyclo // core workflow, kept linear on purpose; see WORKFLOW.md §3 (config hot-reload loop)
func (s *Scheduler) applyReloadedConfig(newCfg *config.Config) (bool, error) {
	s.configMu.RLock()
	oldCfg := s.config
	s.configMu.RUnlock()

	// Preserve non-reloadable fields from old config
	newCfg.DataDir = oldCfg.DataDir
	newCfg.LogFilePath = oldCfg.LogFilePath
	newCfg.LogRotation = oldCfg.LogRotation
	newCfg.WebhookRequestTimeout = oldCfg.WebhookRequestTimeout
	newCfg.WebhookMaxRetries = oldCfg.WebhookMaxRetries
	newCfg.WebhookRetryDelay = oldCfg.WebhookRetryDelay

	// Gate before any client is rebuilt or any field is swapped, so a rejected
	// reload leaves the running configuration completely untouched.
	if err := rejectDestinationRemapForReload(newCfg); err != nil {
		return false, err
	}

	var replacementAPIClient *api.Client
	var err error
	if apiClientSettingsChanged(oldCfg, newCfg) || s.apiClient == nil {
		replacementAPIClient, err = api.NewClientWithBaseURLAndPolicy(
			newCfg.APIKey,
			newCfg.APIBaseURL,
			apiHTTPPolicyForConfig(newCfg),
		)
		if err != nil {
			return false, fmt.Errorf("failed to rebuild API client during config reload: %w", err)
		}
	}

	var replacementRSSParser *rss.Parser
	if oldCfg.RSSRetryCount != newCfg.RSSRetryCount ||
		oldCfg.RSSRetryDelay != newCfg.RSSRetryDelay ||
		oldCfg.RSSWorkerTimeout != newCfg.RSSWorkerTimeout ||
		s.rssParser == nil {
		replacementRSSParser = newRSSParserForConfig(newCfg)
	}

	if replacementAPIClient != nil {
		s.apiMu.Lock()
		defer s.apiMu.Unlock()
	}
	if replacementRSSParser != nil {
		s.rssMu.Lock()
		defer s.rssMu.Unlock()
	}

	s.configMu.Lock()
	s.config = newCfg

	// Rebuild feed type map (must be inside configMu to avoid race with readers)
	s.feedTypeMap = buildFeedTypeMap(newCfg)
	s.configMu.Unlock()

	if s.statusTracker != nil {
		s.statusTracker.UpdateRetention(statusRetentionPolicyForConfig(newCfg))
	}

	auditConfigReloadChanges(oldCfg, newCfg)

	if replacementAPIClient != nil {
		oldClient := s.apiClient
		s.apiClient = replacementAPIClient
		if oldCfg.APIKey != newCfg.APIKey || oldCfg.APIBaseURL != newCfg.APIBaseURL {
			s.apiAuthSuspension = apiAuthSuspension{}
		}
		if oldClient != nil {
			if err := oldClient.Close(); err != nil {
				log.WithError(err).Warn("Failed to close replaced API client")
			}
		}
		log.Info("API client rebuilt after config reload")
	}

	if replacementRSSParser != nil {
		s.rssParser = replacementRSSParser
		log.WithFields(log.Fields{
			"retry_count":       newCfg.RSSRetryCount,
			"retry_delay_ms":    newCfg.RSSRetryDelay.Milliseconds(),
			"worker_timeout_ms": newCfg.RSSWorkerTimeout.Milliseconds(),
		}).Info("RSS parser rebuilt after config reload")
	}

	// Update tickers if intervals changed (nil-safe for calls before Start())
	if oldCfg.APIPollInterval != newCfg.APIPollInterval && s.apiTicker != nil {
		s.apiTicker.Reset(newCfg.APIPollInterval)
		log.WithField("interval_ms", newCfg.APIPollInterval.Milliseconds()).Info("API poll interval updated")
	}
	if oldCfg.RSSPollInterval != newCfg.RSSPollInterval && s.rssTicker != nil {
		s.rssTicker.Reset(newCfg.RSSPollInterval)
		log.WithField("interval_ms", newCfg.RSSPollInterval.Milliseconds()).Info("RSS poll interval updated")
	}

	// Update log level if changed
	if !strings.EqualFold(oldCfg.LogLevel, newCfg.LogLevel) {
		if err := log.SetLevel(newCfg.LogLevel); err == nil {
			log.WithField("level", newCfg.LogLevel).Info("Log level updated")
		} else {
			log.WithError(err).WithField("level", newCfg.LogLevel).Warn("Log level update failed")
		}
	}

	filter.ClearRegexCache()
	pruneRSSFeedStatusForConfig(s.statusTracker, newCfg, s.dryRun)
	// NOT under apiMu/rssMu unless a replacement client/parser was built above
	// (:931-938) -- those locks are conditional, this write is not.
	// applyReloadedConfig has two internal callers: runConfigWatcher (a single
	// ticker-driven goroutine -- the only one actually wired into Start()) and
	// the exported ReloadConfig() (no production caller today, but nothing
	// stops a future one running concurrently with the watcher). Today's safety
	// rests entirely on runConfigWatcher being the only caller anything in this
	// binary invokes, plus status.WriteDestinationManifest writing atomically
	// (temp+rename) to a path fixed by cfg.DataDir. If ReloadConfig() is ever
	// wired to a second live trigger (admin endpoint, signal handler, ...), it
	// must take its own lock around this call (or a dedicated manifest mutex).
	s.writeDestinationManifestAfterReload(newCfg)

	return true, nil
}

// rejectDestinationRemapForReload refuses a reload that would move a webhook URL
// onto a destination ID that already carries another endpoint's stored state.
// A manifest that cannot be read is self-healed: it is warned about and treated
// as absent, so a local corruption never turns into a stuck configuration.
func rejectDestinationRemapForReload(newCfg *config.Config) error {
	if newCfg.DataDir == "" {
		return nil
	}
	manifest, found, err := status.LoadDestinationManifest(newCfg.DataDir)
	if err != nil {
		log.WithFields(status.DestinationManifestUnreadableFields(newCfg.DataDir, manifest, err)).
			Warn("Destination manifest unreadable; rewriting it")
		return nil
	}
	if !found {
		return nil
	}
	diff := status.DiffDestinations(manifest.Destinations, config.DestinationURLHashes(newCfg))
	if !diff.HasRemap() {
		return nil
	}
	log.WithFields(status.DestinationRemapLogFields(diff, status.DestinationManifestPath(newCfg.DataDir))).
		Warn("Config reload rejected: webhook endpoints were remapped onto existing destination IDs")
	return errDestinationRemapRejected
}

// writeDestinationManifestAfterReload records the endpoint-to-ID binding of the
// configuration that was just applied. A write failure is a WARN: the manifest
// must never stop delivery.
func (s *Scheduler) writeDestinationManifestAfterReload(cfg *config.Config) {
	if s.dryRun || cfg.DataDir == "" {
		return
	}
	if err := status.WriteDestinationManifest(cfg.DataDir, config.DestinationURLHashes(cfg)); err != nil {
		log.WithError(err).WithField("manifest", status.DestinationManifestPath(cfg.DataDir)).
			Warn("Failed to write the destination manifest after config reload")
	}
}

func auditConfigReloadChanges(oldCfg, newCfg *config.Config) {
	for _, event := range configReloadAuditEvents(oldCfg, newCfg) {
		log.WithFields(event).Info("Config reload alert routing changed")
	}
}

// configReloadAuditEvents reports every changed setting between two config
// snapshots for the "Config reload alert routing changed" log line. Secret
// values (the API key, the API base URL -- which may carry userinfo
// credentials, see NormalizeBaseURL -- and every webhook URL, which embeds
// its delivery token) are reported only as old_present/new_present: no hash,
// prefix, or other value derived from the secret. An unsalted hash of a
// guessable or low-entropy secret can be verified offline by anyone holding
// the log, which defeats the point of not logging the secret itself.
// safeHashPrefix stays in use for non-secret settings (format, filters, quiet
// hours, feed URL lists -- internal/feedurl rejects userinfo and deny-listed
// credential query keys in feed URLs at load time).
func configReloadAuditEvents(oldCfg, newCfg *config.Config) []log.Fields {
	if oldCfg == nil || newCfg == nil {
		return nil
	}

	events := make([]log.Fields, 0)
	events = append(events, coreConfigReloadAuditEvents(oldCfg, newCfg)...)
	events = append(events, webhookReloadAuditEvents(oldCfg, newCfg)...)
	events = append(events, feedReloadAuditEvents(oldCfg, newCfg)...)
	if oldHash, newHash := safeHashPrefix(oldCfg.Format), safeHashPrefix(newCfg.Format); oldHash != newHash {
		events = append(events, configReloadAuditEvent("format.sha256_prefix", log.Fields{
			"old_sha256_prefix": oldHash,
			"new_sha256_prefix": newHash,
		}))
	}
	return events
}

func coreConfigReloadAuditEvents(oldCfg, newCfg *config.Config) []log.Fields {
	events := make([]log.Fields, 0)
	if oldCfg.APIKey != newCfg.APIKey {
		events = append(events, configReloadAuditEvent("api.key", log.Fields{
			"old_present": strings.TrimSpace(oldCfg.APIKey) != "",
			"new_present": strings.TrimSpace(newCfg.APIKey) != "",
		}))
	}
	if oldCfg.APIBaseURL != newCfg.APIBaseURL {
		events = append(events, configReloadAuditEvent("api.base_url", log.Fields{
			"old_present": strings.TrimSpace(oldCfg.APIBaseURL) != "",
			"new_present": strings.TrimSpace(newCfg.APIBaseURL) != "",
		}))
	}

	durationChanges := []struct {
		field string
		old   time.Duration
		new   time.Duration
	}{
		{field: "api_request_timeout_ms", old: oldCfg.APIRequestTimeout, new: newCfg.APIRequestTimeout},
		{field: "api_poll_interval_ms", old: oldCfg.APIPollInterval, new: newCfg.APIPollInterval},
		{field: "api_check_timeout_ms", old: oldCfg.APICheckTimeout, new: newCfg.APICheckTimeout},
		{field: "rss_poll_interval_ms", old: oldCfg.RSSPollInterval, new: newCfg.RSSPollInterval},
		{field: "rss_check_timeout_ms", old: oldCfg.RSSCheckTimeout, new: newCfg.RSSCheckTimeout},
		{field: "rss_retry_delay_ms", old: oldCfg.RSSRetryDelay, new: newCfg.RSSRetryDelay},
		{field: "rss_worker_timeout_ms", old: oldCfg.RSSWorkerTimeout, new: newCfg.RSSWorkerTimeout},
		{field: "discord_delay_ms", old: oldCfg.DiscordDelay, new: newCfg.DiscordDelay},
		{field: "slack_delay_ms", old: oldCfg.SlackDelay, new: newCfg.SlackDelay},
		{field: "retry_window_ms", old: oldCfg.RetryWindow, new: newCfg.RetryWindow},
	}
	for _, change := range durationChanges {
		if change.old == change.new {
			continue
		}
		events = append(events, configReloadAuditEvent(change.field, log.Fields{
			"old": change.old.Milliseconds(),
			"new": change.new.Milliseconds(),
		}))
	}

	intChanges := []struct {
		field string
		old   int
		new   int
	}{
		{field: "api_max_retries", old: oldCfg.APIMaxRetries, new: newCfg.APIMaxRetries},
		{field: "rss_retry_count", old: oldCfg.RSSRetryCount, new: newCfg.RSSRetryCount},
		{field: "max_rss_workers", old: oldCfg.MaxRSSWorkers, new: newCfg.MaxRSSWorkers},
		{field: "max_api_entries_per_cycle", old: oldCfg.APIMaxEntriesPerCycle, new: newCfg.APIMaxEntriesPerCycle},
		{field: "max_rss_entries_per_cycle", old: oldCfg.RSSMaxEntriesPerCycle, new: newCfg.RSSMaxEntriesPerCycle},
		{field: "retry_max_attempts", old: oldCfg.RetryMaxAttempts, new: newCfg.RetryMaxAttempts},
	}
	for _, change := range intChanges {
		if change.old == change.new {
			continue
		}
		events = append(events, configReloadAuditEvent(change.field, log.Fields{
			"old": change.old,
			"new": change.new,
		}))
	}

	return events
}

func webhookReloadAuditEvents(oldCfg, newCfg *config.Config) []log.Fields {
	oldTargets := map[string]config.WebhookTargetConfig{}
	for _, target := range config.WebhookTargets(oldCfg) {
		oldTargets[target.QualifiedName()] = target
	}

	events := make([]log.Fields, 0)
	for _, newTarget := range config.WebhookTargets(newCfg) {
		qualifiedName := newTarget.QualifiedName()
		oldTarget := oldTargets[qualifiedName]

		if oldTarget.Webhook.Enabled != newTarget.Webhook.Enabled {
			events = append(events, configReloadAuditEvent(qualifiedName+".enabled", log.Fields{
				"old": oldTarget.Webhook.Enabled,
				"new": newTarget.Webhook.Enabled,
			}))
		}
		if oldTarget.Webhook.URL != newTarget.Webhook.URL {
			events = append(events, configReloadAuditEvent(qualifiedName+".url", log.Fields{
				"old_present": strings.TrimSpace(oldTarget.Webhook.URL) != "",
				"new_present": strings.TrimSpace(newTarget.Webhook.URL) != "",
			}))
		}
		if oldHash, newHash := safeHashPrefix(oldTarget.Webhook.Filters), safeHashPrefix(newTarget.Webhook.Filters); oldHash != newHash {
			events = append(events, configReloadAuditEvent(qualifiedName+".filters_sha256_prefix", log.Fields{
				"old_sha256_prefix": oldHash,
				"new_sha256_prefix": newHash,
			}))
		}
		if oldHash, newHash := safeHashPrefix(oldTarget.Webhook.QuietHours), safeHashPrefix(newTarget.Webhook.QuietHours); oldHash != newHash {
			events = append(events, configReloadAuditEvent(qualifiedName+".quiet_hours_sha256_prefix", log.Fields{
				"old_sha256_prefix": oldHash,
				"new_sha256_prefix": newHash,
			}))
		}
	}
	return events
}

func feedReloadAuditEvents(oldCfg, newCfg *config.Config) []log.Fields {
	oldGroups := map[string][]string{}
	for _, group := range oldCfg.Feeds.Groups() {
		oldGroups[group.JSONName] = group.URLs
	}

	events := make([]log.Fields, 0)
	for _, group := range newCfg.Feeds.Groups() {
		oldURLs := oldGroups[group.JSONName]
		if stringSlicesEqual(oldURLs, group.URLs) {
			continue
		}
		events = append(events, configReloadAuditEvent("feeds."+group.JSONName, log.Fields{
			"old_count":         len(oldURLs),
			"new_count":         len(group.URLs),
			"old_sha256_prefix": safeHashPrefix(oldURLs),
			"new_sha256_prefix": safeHashPrefix(group.URLs),
		}))
	}
	return events
}

func configReloadAuditEvent(field string, fields log.Fields) log.Fields {
	event := log.Fields{
		"component": "config_reload",
		"field":     field,
	}
	for key, value := range fields {
		event[key] = value
	}
	return event
}

func safeHashPrefix(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		data = []byte("marshal_error")
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])[:12]
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkAPIOnce performs a single API check with individual sending
func (s *Scheduler) checkAPIOnce(ctx context.Context) checkOutcome {
	pollFields := newPollLogFields(pollTypeAPI)

	// Prevent overlapping API runs – skip if a previous run is still in progress
	if !s.apiMu.TryLock() {
		log.WithFields(pollFields).Warn("Skipping API check: previous run still in progress")
		return checkOutcome{ran: false, ok: true}
	}
	defer s.apiMu.Unlock()
	defer s.cleanupStatusRetention()

	cfg := s.getConfig()

	if !hasActiveRansomwareWebhook(cfg) {
		log.WithFields(pollFields).Debug("No ransomware webhook enabled, skipping API check")
		return checkOutcome{ran: true, ok: true}
	}

	if cfg.APIKey == "" {
		message := "API key not configured; set api_key to enable ransomware polling"
		log.WithFields(pollFields).Warn(message)
		if !s.dryRun {
			s.statusTracker.UpdateAPIStatusWithErrorInfo(false, 0, message, status.SourceErrorInfo{
				ErrorCategory: "configuration",
				Retryable:     boolPtr(false),
				Timeout:       boolPtr(false),
			})
			s.persistStatus("api key not configured status")
		}
		return checkOutcome{ran: true, ok: false}
	}

	now := apiAuthNow().UTC()
	if s.apiAuthSuspension.key == cfg.APIKey {
		fields := mergeLogFields(pollFields, s.apiAuthSuspensionFields())
		if now.Before(s.apiAuthSuspension.nextProbe) {
			log.WithFields(fields).Debug("API polling suspended after authentication failure")
			return checkOutcome{ran: true, ok: true}
		}
		log.WithFields(fields).Info("Re-probing ransomware API after authentication suspension")
	}

	if !s.dryRun {
		if err := s.statusTracker.EnsureAPIStatusLoaded(); err != nil {
			log.WithError(err).WithFields(pollFields).Error("Failed to load API status before API polling")
			return checkOutcome{ran: true, ok: true}
		}
		s.migrateLegacyAPIFetchedItemsForConfig(cfg)
	}

	log.WithFields(pollFields).Debug("Checking API for new ransomware data")

	// Get all ransomware entries from API
	checkTimeout := apiCheckTimeout(cfg)
	fetchCtx, fetchCancel := context.WithTimeout(ctx, checkTimeout)
	allEntries, err := s.apiClient.GetLatestEntries(fetchCtx)
	fetchErr := fetchCtx.Err()
	fetchCancel()
	if err != nil {
		if isAPIAuthError(err) {
			s.armAPIAuthSuspension(cfg.APIKey, now, err, pollFields)
		} else {
			s.apiAuthSuspension = apiAuthSuspension{}
		}
		logAPIDataError(err, pollFields)
		if !s.dryRun {
			s.statusTracker.UpdateAPIStatusWithErrorInfo(false, 0, api.OperatorErrorMessage(err), apiSourceErrorInfo(err))
			s.persistStatus("api poll failure status")
			if fetchErr == nil {
				retryCtx, retryCancel := context.WithTimeout(ctx, checkTimeout)
				s.processAPIRetryQueue(retryCtx, nil)
				retryCancel()
			}
		}
		// A shutdown cancellation is not a failed cycle: symmetric with
		// processRSSFeedBatches' own errors.Is(err, context.Canceled)
		// exemption, so Ctrl-C during --dry-run cannot report a false failure
		// on the API side while the RSS side stays exempt (F5).
		return checkOutcome{ran: true, ok: errors.Is(err, context.Canceled)}
	}
	if s.apiAuthSuspension.key != "" {
		log.WithFields(pollFields).Info("Ransomware API authentication recovered; polling resumed")
	}
	s.apiAuthSuspension = apiAuthSuspension{}

	// Update status with success
	if !s.dryRun {
		s.statusTracker.UpdateAPIStatus(true, len(allEntries), "")
		s.persistStatus("api poll success status")
	}

	s.deliverAPIEntriesAndProcessRetries(ctx, cfg, allEntries, checkTimeout)
	return checkOutcome{ran: true, ok: true}
}

func logAPIDataError(err error, pollFields log.Fields) {
	fields := log.Fields{
		"error": api.OperatorErrorMessage(err),
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) {
		fields["status_code"] = statusErr.StatusCode
		fields["status"] = statusErr.Status
	}
	log.WithFields(mergeLogFields(pollFields, fields)).Error("Failed to get API data")
}

func hasActiveRansomwareWebhook(cfg *config.Config) bool {
	return cfg.DiscordWebhooks.Ransomware.Enabled ||
		cfg.SlackWebhooks.Ransomware.Enabled ||
		cfg.SlackCompatibleWebhooks.Ransomware.Enabled
}

func isAPIAuthError(err error) bool {
	var statusErr *api.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden
}

func apiSourceErrorInfo(err error) status.SourceErrorInfo {
	retryable := true
	timeout := false
	info := status.SourceErrorInfo{
		ErrorCategory: "network",
		Retryable:     boolPtr(retryable),
		Timeout:       boolPtr(timeout),
	}
	if err == nil {
		info.ErrorCategory = ""
		return info
	}
	if errors.Is(err, context.Canceled) {
		retryable = false
		info.ErrorCategory = "canceled"
		info.Retryable = boolPtr(retryable)
		return info
	}
	if errors.Is(err, context.DeadlineExceeded) {
		timeout = true
		info.ErrorCategory = "timeout"
		info.Timeout = boolPtr(timeout)
		return info
	}
	var cooldownErr *api.RateLimitCooldownError
	if errors.As(err, &cooldownErr) {
		info.ErrorCategory = "rate_limited"
		info.StatusCode = http.StatusTooManyRequests
		return info
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) {
		info.StatusCode = statusErr.StatusCode
		info.ErrorCategory = apiHTTPErrorCategory(statusErr.StatusCode)
		retryable = statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= 500
		info.Retryable = boolPtr(retryable)
		return info
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		timeout = true
		info.ErrorCategory = "timeout"
		info.Timeout = boolPtr(timeout)
		return info
	}
	if strings.Contains(strings.ToLower(err.Error()), "decode") {
		info.ErrorCategory = "decode_error"
	}
	return info
}

func apiHTTPErrorCategory(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limited"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "authentication"
	case statusCode == http.StatusNotFound:
		return "not_found"
	case statusCode >= 400 && statusCode < 500:
		return "request_rejected"
	case statusCode >= 500:
		return "provider_unavailable"
	default:
		return "http_status"
	}
}

func rssSourceErrorInfo(err error) status.SourceErrorInfo {
	if err == nil {
		return status.SourceErrorInfo{}
	}
	return rssSourceErrorInfoFromString(err.Error())
}

func rssSourceErrorInfoFromString(message string) status.SourceErrorInfo {
	lower := strings.ToLower(message)
	retryable := true
	timeout := false
	info := status.SourceErrorInfo{
		ErrorCategory: "fetch",
		Retryable:     boolPtr(retryable),
		Timeout:       boolPtr(timeout),
	}
	switch {
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		timeout = true
		info.ErrorCategory = "timeout"
		info.Timeout = boolPtr(timeout)
	case strings.Contains(lower, "status 429") || strings.Contains(lower, "rate limit"):
		info.ErrorCategory = "rate_limited"
		info.StatusCode = http.StatusTooManyRequests
	case strings.Contains(lower, "status 4"):
		retryable = false
		info.ErrorCategory = "request_rejected"
		info.Retryable = boolPtr(retryable)
	case strings.Contains(lower, "status 5"):
		info.ErrorCategory = "provider_unavailable"
	case strings.Contains(lower, "parse") || strings.Contains(lower, "malformed"):
		retryable = false
		info.ErrorCategory = "parse_error"
		info.Retryable = boolPtr(retryable)
	case strings.Contains(lower, "blocked") || strings.Contains(lower, "private") || strings.Contains(lower, "invalid url"):
		retryable = false
		info.ErrorCategory = "validation"
		info.Retryable = boolPtr(retryable)
	}
	return info
}

func deliveryStatusMessage(messenger string, err error) string {
	if err == nil {
		return ""
	}
	messengerName := messenger
	if messengerName == "" {
		messengerName = "webhook"
	}
	if errors.Is(err, context.Canceled) {
		return "Webhook delivery canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Webhook delivery timed out; provider or network did not respond before the deadline"
	}

	var statusErr *webhookhttp.HTTPStatusError
	if errors.As(err, &statusErr) {
		return deliveryStatusMessageForHTTPStatus(messengerName, statusErr.StatusCode)
	}

	// *webhookhttp.TransportError is checked ahead of the generic net.Error
	// match below: *url.Error -- what
	// http.Client.Do returns on every transport failure -- always satisfies
	// Go's net.Error interface regardless of the underlying cause, because
	// its Timeout/Temporary methods exist unconditionally (they still
	// inspect the wrapped cause; what never varies is that the interface is
	// satisfied). A permanent transport failure (an untrusted certificate,
	// for example) therefore wraps a *url.Error that matches net.Error just
	// as readily as a genuinely transient one, so the net.Error arm
	// previously won for both and reported a certificate failure the bot
	// cannot recover from the same way as a passing network blip. The more
	// specific, already-classified TransportError is checked first so a
	// non-retryable failure is named as terminal.
	//
	// Within the retryable case, a net.Error whose Timeout() is true is
	// still named as a timeout rather than the generic "provider
	// unavailable": every
	// client.Do error reaches this branch wrapped in a TransportError, so
	// without this check a TLS-handshake timeout -- webhookhttp.NewTransport
	// hard-codes TLSHandshakeTimeout at 10s, well under the operator-
	// configurable 1s-5m webhook_request_timeout that governs
	// http.Client.Timeout -- silently degraded from the accurate "timed
	// out; provider or network did not respond" to the misleading "provider
	// unavailable", which points the operator at the wrong cause (a host
	// that accepts TCP but stalls the handshake -- a firewall, a MITM proxy
	// or an overloaded load balancer -- rather than an unreachable
	// endpoint).
	var transportErr *webhookhttp.TransportError
	if errors.As(err, &transportErr) {
		if !transportErr.Retryable() {
			return fmt.Sprintf("%s webhook connection failed permanently; verify the certificate and network path", messengerName)
		}
		var transportNetErr net.Error
		if errors.As(err, &transportNetErr) && transportNetErr.Timeout() {
			return "Webhook delivery timed out; provider or network did not respond before the deadline"
		}
		return fmt.Sprintf("%s webhook provider unavailable", messengerName)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "Webhook delivery timed out; provider or network did not respond before the deadline"
		}
		return fmt.Sprintf("%s webhook provider unavailable", messengerName)
	}

	// None of the branches below append a "retry scheduled" (or similar)
	// prediction: this function only classifies
	// THIS attempt's error, before the tracker's own retry-window/
	// max-attempts bookkeeping decides whether the item is actually queued
	// or dead-lettered. A transient-looking error (a 503, a 429) can still
	// end up dead-lettered on this very attempt because the retry window
	// already elapsed or the attempt budget is already spent, and the text
	// built here is exactly what gets persisted as last_error / rendered by
	// --list-dead-letter -- a forward-looking promise here can outlive its
	// own truth. Describing the failure without predicting the outcome
	// keeps the persisted text accurate regardless of what the tracker
	// decides afterwards.
	lowerErr := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lowerErr, "invalid webhook url"),
		strings.Contains(lowerErr, "unknown webhook"),
		containsHTTPStatusText(lowerErr, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound):
		return fmt.Sprintf("%s webhook rejected delivery; verify or rotate the webhook URL", messengerName)
	case containsHTTPStatusText(lowerErr, http.StatusBadRequest),
		strings.Contains(lowerErr, "invalid_blocks"),
		strings.Contains(lowerErr, "invalid payload"),
		strings.Contains(lowerErr, "cannot send an empty message"):
		return fmt.Sprintf("%s webhook rejected the message payload; check formatting configuration", messengerName)
	case containsHTTPStatusText(lowerErr, http.StatusTooManyRequests),
		strings.Contains(lowerErr, "rate limit"):
		return fmt.Sprintf("%s webhook rate limited delivery", messengerName)
	case containsHTTPStatusText(
		lowerErr,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	):
		return fmt.Sprintf("%s webhook provider unavailable", messengerName)
	default:
		return fmt.Sprintf("%s webhook delivery failed; check webhook configuration and provider availability", messengerName)
	}
}

// deliveryStatusMessageForHTTPStatus never predicts a retry, for the same
// reason deliveryStatusMessage's own classification
// arms do not: this text is persisted as last_error before the tracker's own
// retry-window/max-attempts decision runs, and that decision can still
// dead-letter the item on this very attempt.
func deliveryStatusMessageForHTTPStatus(messengerName string, statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Sprintf("%s webhook rate limited delivery", messengerName)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusNotFound:
		return fmt.Sprintf("%s webhook rejected delivery; verify or rotate the webhook URL", messengerName)
	case statusCode >= 400 && statusCode < 500:
		return fmt.Sprintf("%s webhook rejected the message payload; check formatting configuration", messengerName)
	case statusCode >= 500:
		return fmt.Sprintf("%s webhook provider unavailable", messengerName)
	default:
		return fmt.Sprintf("%s webhook delivery failed; check webhook configuration and provider availability", messengerName)
	}
}

func containsHTTPStatusText(lowerErr string, statuses ...int) bool {
	for _, statusCode := range statuses {
		statusText := fmt.Sprint(statusCode)
		if strings.Contains(lowerErr, "http "+statusText) ||
			strings.Contains(lowerErr, "http status "+statusText) ||
			strings.Contains(lowerErr, "status "+statusText) ||
			strings.Contains(lowerErr, "webhook http "+statusText) {
			return true
		}
	}
	return false
}

func webhookRetryErrorInfo(err error) status.RetryErrorInfo {
	retryable := webhookhttp.IsRetryableError(err)
	info := status.RetryErrorInfo{
		ErrorCategory: "network",
		Retryable:     boolPtr(retryable),
	}
	if err == nil {
		info.ErrorCategory = ""
		return info
	}
	if errors.Is(err, context.Canceled) {
		info.ErrorCategory = "canceled"
		return info
	}
	if errors.Is(err, context.DeadlineExceeded) {
		info.ErrorCategory = "timeout"
		return info
	}

	var statusErr *webhookhttp.HTTPStatusError
	if errors.As(err, &statusErr) {
		info.StatusCode = statusErr.StatusCode
		info.Retryable = boolPtr(statusErr.Retryable())
		info.ErrorCategory = webhookHTTPErrorCategory(statusErr.StatusCode)
		return info
	}

	if !retryable {
		info.ErrorCategory = "permanent"
	}
	return info
}

func webhookHTTPErrorCategory(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limited"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusNotFound:
		return "invalid_webhook"
	case statusCode >= 400 && statusCode < 500:
		return "payload_rejected"
	case statusCode >= 500:
		return "provider_unavailable"
	default:
		return "http_status"
	}
}

// webhookFailureIsRetryable decides queue-vs-dead-letter for a failed webhook
// delivery. A cancelled context is a shutdown signal propagated into the
// delivery context, not a delivery verdict, so the item stays in the retry
// queue. A delivery context that expires on its own yields DeadlineExceeded,
// which is retryable on its own merits and never reaches this guard.
func webhookFailureIsRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	return webhookhttp.IsRetryableError(err)
}

func boolPtr(value bool) *bool {
	return &value
}

func webhookFailureFields(messenger string, err error, fields log.Fields) log.Fields {
	if fields == nil {
		fields = log.Fields{}
	}
	fields["error"] = deliveryStatusMessage(messenger, err)
	errorInfo := webhookRetryErrorInfo(err)
	if errorInfo.ErrorCategory != "" {
		fields["error_category"] = errorInfo.ErrorCategory
	}
	if errorInfo.Retryable != nil {
		fields["retryable"] = *errorInfo.Retryable
	}

	var statusErr *webhookhttp.HTTPStatusError
	if errors.As(err, &statusErr) {
		fields["status_code"] = statusErr.StatusCode
		fields["status"] = statusErr.Status
	}
	return fields
}

func (s *Scheduler) recordWebhookDeliveryFailure(
	itemKey, destinationID, messenger, itemType, title string,
	err error,
	maxAttempts int,
	retryWindow time.Duration,
	payload ...[]byte,
) {
	message := deliveryStatusMessage(messenger, err)
	errorInfo := webhookRetryErrorInfo(err)
	if webhookFailureIsRetryable(err) {
		req := status.RetryRequest{
			ItemKey:       itemKey,
			DestinationID: destinationID,
			Messenger:     status.Messenger(messenger),
			ItemType:      status.RetryItemType(itemType),
			Title:         title,
			LastError:     message,
			ErrorInfo:     errorInfo,
			MaxAttempts:   maxAttempts,
			RetryWindow:   retryWindow,
		}
		if len(payload) > 0 {
			req.Payload = payload[0]
		}
		s.statusTracker.EnqueueRetryForDestination(req)
		return
	}

	log.WithFields(log.Fields{
		"item_key":         itemKey,
		"destination_id":   destinationID,
		"messenger":        messenger,
		"item_type":        itemType,
		"error":            message,
		"error_category":   errorInfo.ErrorCategory,
		"recovery_command": "--list-dead-letter",
	}).Warn("Webhook delivery failed permanently; moving item to dead letter")
	s.statusTracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
		itemKey,
		destinationID,
		messenger,
		itemType,
		title,
		message,
		errorInfo,
	)
}

func (s *Scheduler) recordRetryQueueDeliveryFailure(
	queueKey string,
	item status.RetryRecord,
	err error,
	maxAttempts int,
	retryWindow time.Duration,
) {
	message := deliveryStatusMessage(item.Messenger, err)
	errorInfo := webhookRetryErrorInfo(err)
	if webhookFailureIsRetryable(err) {
		s.statusTracker.RecordRetryQueueFailureWithErrorInfo(queueKey, message, errorInfo, maxAttempts, retryWindow)
		return
	}

	log.WithFields(log.Fields{
		"item_key":         item.ItemKey,
		"destination_id":   item.DestinationID,
		"queue_key":        queueKey,
		"messenger":        item.Messenger,
		"item_type":        item.ItemType,
		"error":            message,
		"error_category":   errorInfo.ErrorCategory,
		"recovery_command": "--list-dead-letter",
	}).Warn("Retry delivery failed permanently; moving item to dead letter")
	s.statusTracker.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, message, errorInfo)
}

func webhookProgressFields(index, total int, destinationID string) log.Fields {
	current := index + 1
	if current < 1 {
		current = 1
	}
	if total < current {
		total = current
	}
	remaining := total - current
	percentComplete := 0
	if total > 0 {
		percentComplete = (current * 100) / total
	}

	return log.Fields{
		"current":          current,
		"total":            total,
		"remaining":        remaining,
		"percent_complete": percentComplete,
		"destination_id":   destinationID,
	}
}

func optionalDestinationID(fallback string, destinationIDs ...string) string {
	if len(destinationIDs) > 0 && destinationIDs[0] != "" {
		return destinationIDs[0]
	}
	return fallback
}

func markAPIItemSentForDestination(tracker *status.Tracker, itemKey, title, destinationID, webhookURL string) {
	tracker.MarkAPIItemSentToDestination(itemKey, title, destinationID)
	if webhookURL != "" && webhookURL != destinationID {
		tracker.MarkAPIItemSentToLegacyDestination(itemKey, title, webhookURL, destinationID)
	}
}

func markRSSItemSentForDestination(
	tracker *status.Tracker,
	itemKey, title, feedTitle, destinationID, webhookURL string,
) {
	tracker.MarkRSSItemSentToDestination(itemKey, title, feedTitle, destinationID)
	if webhookURL != "" && webhookURL != destinationID {
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, title, feedTitle, webhookURL, destinationID)
	}
}

// markRSSItemDedupedForDestination records the marker written for an entry that
// a content-signature dedup hit suppressed. It suppresses exactly like a delivery
// marker but is flagged derived, so the load-time content-signature backfill
// never turns it into a new dedup anchor.
func markRSSItemDedupedForDestination(
	tracker *status.Tracker,
	itemKey, title, feedTitle, destinationID, webhookURL string,
) {
	tracker.MarkRSSItemDedupedForDestination(itemKey, title, feedTitle, destinationID)
	if webhookURL != "" && webhookURL != destinationID {
		tracker.MarkRSSItemDedupedForLegacyDestination(itemKey, title, feedTitle, webhookURL, destinationID)
	}
}

func markRSSItemSkippedForDestination(tracker *status.Tracker, itemKey, destinationID, webhookURL string) {
	tracker.MarkRSSItemSkippedForDestination(itemKey, destinationID)
	if webhookURL != "" && webhookURL != destinationID {
		tracker.MarkRSSItemSkippedForLegacyDestination(itemKey, webhookURL, destinationID)
	}
}

func (s *Scheduler) recordDeliveryAuditEvent(event status.DeliveryAuditEvent) {
	if s == nil || s.dryRun || s.statusTracker == nil {
		return
	}
	s.statusTracker.RecordDeliveryAuditEvent(event)
}

func (s *Scheduler) recordRSSDeliveryAuditEvent(
	item rssDeliveryItem,
	feedType string,
	target webhookTarget,
	mode rssDeliveryMode,
	outcome string,
	reason string,
	details map[string]string,
) {
	details = auditDetailsWithMode(details, mode)
	s.recordDeliveryAuditEvent(status.DeliveryAuditEvent{
		EventType:     status.DeliveryAuditEventAlertCandidate,
		Source:        status.RetryItemTypeRSS.String(),
		ItemType:      status.RetryItemTypeRSS.String(),
		ItemKey:       item.key,
		Title:         item.title,
		Messenger:     target.messenger,
		DestinationID: targetDestinationID(target),
		FeedType:      feedType,
		FeedURL:       item.feedURL,
		Outcome:       outcome,
		Reason:        reason,
		Details:       details,
	})
}

// recordRSSQuietHoursAuditCapSummary emits the single aggregate line that
// stands in for every quiet-hours candidate suppressed past the per-cycle
// audit cap. Shared by the fresh and recovery paths so the two can never
// drift apart again.
func (s *Scheduler) recordRSSQuietHoursAuditCapSummary(
	feedType string,
	target webhookTarget,
	mode rssDeliveryMode,
	total int,
	limit int,
	suppressed int,
) {
	details := auditDetailsWithMode(quietHoursAuditDetails(target.quietHours), mode)
	if details == nil {
		details = map[string]string{}
	}
	details["deferred_total"] = strconv.Itoa(total)
	details["audit_cap"] = strconv.Itoa(limit)
	s.recordDeliveryAuditEvent(status.DeliveryAuditEvent{
		EventType:     status.DeliveryAuditEventCleanup,
		Source:        status.RetryItemTypeRSS.String(),
		ItemType:      status.RetryItemTypeRSS.String(),
		Messenger:     target.messenger,
		DestinationID: targetDestinationID(target),
		FeedType:      feedType,
		Outcome:       status.DeliveryAuditOutcomeQuietHours,
		Reason:        status.DeliveryAuditReasonQuietHoursCycleCapped,
		Count:         suppressed,
		Details:       details,
	})
}

// recordRSSQuietHoursDeferredEntries audits at most limit of the fresh
// entries deferred by an active quiet-hours window, plus one summary line for
// the remainder (see recordRSSQuietHoursAuditCapSummary). A limit <= 0
// disables the cap, mirroring limitRSSEntriesForCycle's own handling.
func (s *Scheduler) recordRSSQuietHoursDeferredEntries(
	entries []model.RSSEntry,
	feedType string,
	target webhookTarget,
	mode rssDeliveryMode,
	limit int,
) {
	audited := entries
	suppressed := 0
	if limit > 0 && len(entries) > limit {
		audited = entries[:limit]
		suppressed = len(entries) - limit
	}
	for _, entry := range audited {
		s.recordRSSDeliveryAuditEvent(
			freshRSSDeliveryItem(entry),
			feedType,
			target,
			mode,
			status.DeliveryAuditOutcomeQuietHours,
			status.DeliveryAuditReasonQuietHours,
			quietHoursAuditDetails(target.quietHours),
		)
	}
	if suppressed > 0 {
		s.recordRSSQuietHoursAuditCapSummary(feedType, target, mode, len(entries), limit, suppressed)
	}
}

// recordRSSQuietHoursDeferredItems audits at most limit of the recovery
// backlog items deferred by an active quiet-hours window, plus one summary
// line for the remainder (see recordRSSQuietHoursAuditCapSummary). A limit
// <= 0 disables the cap, mirroring limitRSSEntriesForCycle's own handling.
// This is a flat cap (items[:limit]), not limitUnsentRSSItemsForCycle's
// stale/sendable split: nothing is sent here, quiet hours defers everything,
// so there is no send-fairness question, only how many lines get written.
func (s *Scheduler) recordRSSQuietHoursDeferredItems(
	items []status.UnsentRSSItem,
	feedType string,
	target webhookTarget,
	mode rssDeliveryMode,
	limit int,
) {
	audited := items
	suppressed := 0
	if limit > 0 && len(items) > limit {
		audited = items[:limit]
		suppressed = len(items) - limit
	}
	for _, item := range audited {
		s.recordDeliveryAuditEvent(status.DeliveryAuditEvent{
			EventType:     status.DeliveryAuditEventAlertCandidate,
			Source:        status.RetryItemTypeRSS.String(),
			ItemType:      status.RetryItemTypeRSS.String(),
			ItemKey:       item.Key,
			Title:         item.Title,
			Messenger:     target.messenger,
			DestinationID: targetDestinationID(target),
			FeedType:      feedType,
			FeedURL:       item.FeedURL,
			Outcome:       status.DeliveryAuditOutcomeQuietHours,
			Reason:        status.DeliveryAuditReasonQuietHours,
			Details:       auditDetailsWithMode(quietHoursAuditDetails(target.quietHours), mode),
		})
	}
	if suppressed > 0 {
		s.recordRSSQuietHoursAuditCapSummary(feedType, target, mode, len(items), limit, suppressed)
	}
}

func quietHoursAuditDetails(policy *quiethours.Policy) map[string]string {
	if policy == nil {
		return nil
	}
	return map[string]string{
		"quiet_hours_start": policy.Start,
		"quiet_hours_end":   policy.End,
	}
}

func auditDetailsWithMode(details map[string]string, mode rssDeliveryMode) map[string]string {
	merged := make(map[string]string, len(details)+1)
	for key, value := range details {
		merged[key] = value
	}
	merged["mode"] = string(mode)
	return merged
}

type messengerDelivery struct {
	logName string
	delay   time.Duration
	sender  WebhookSender
}

func (s *Scheduler) messengerDeliveryForConfig(messenger string, cfg *config.Config) (messengerDelivery, bool) {
	deliveries := map[string]messengerDelivery{
		status.MessengerDiscord.String(): {
			logName: "Discord",
			delay:   cfg.DiscordDelay,
			sender:  s.discordWebhookSender,
		},
		status.MessengerSlack.String(): {
			logName: "Slack",
			delay:   cfg.SlackDelay,
			sender:  s.slackWebhookSender,
		},
		status.MessengerSlackCompatible.String(): {
			logName: "Slack-compatible",
			delay:   cfg.SlackDelay,
			sender:  s.slackWebhookSender,
		},
	}
	delivery, ok := deliveries[messenger]
	return delivery, ok
}

type messengerPreview struct {
	api func(model.RansomwareEntry, *notifyfmt.FormatOptions) map[string]any
	rss func(model.RSSEntry, string, *notifyfmt.FormatOptions) map[string]any
}

var messengerPreviews = map[string]messengerPreview{
	status.MessengerDiscord.String(): {
		api: discord.PreviewRansomwareEntry,
		rss: discord.PreviewRSSEntry,
	},
	status.MessengerSlack.String(): {
		api: slack.PreviewRansomwareEntry,
		rss: func(entry model.RSSEntry, feedType string, formatConfig *notifyfmt.FormatOptions) map[string]any {
			return slack.PreviewRSSEntry(entry, formatConfig, feedType)
		},
	},
	status.MessengerSlackCompatible.String(): {
		api: slack.PreviewRansomwareEntry,
		rss: func(entry model.RSSEntry, feedType string, formatConfig *notifyfmt.FormatOptions) map[string]any {
			return slack.PreviewRSSEntry(entry, formatConfig, feedType)
		},
	},
}

func addDryRunRansomwarePreview(
	fields log.Fields,
	messenger string,
	entry model.RansomwareEntry,
	formatConfig *notifyfmt.FormatOptions,
) {
	previewer, ok := messengerPreviews[messenger]
	if !ok || previewer.api == nil {
		return
	}
	preview := previewer.api(entry, formatConfig)
	addDryRunPreviewFields(fields, preview)
}

func addDryRunRSSPreview(
	fields log.Fields,
	messenger string,
	entry model.RSSEntry,
	formatConfig *notifyfmt.FormatOptions,
	feedType string,
) {
	previewer, ok := messengerPreviews[messenger]
	if !ok || previewer.rss == nil {
		return
	}
	preview := previewer.rss(entry, feedType, formatConfig)
	addDryRunPreviewFields(fields, preview)
}

func addDryRunPreviewFields(fields log.Fields, preview map[string]any) {
	if len(preview) == 0 {
		return
	}
	fields["rendered_preview"] = preview
	if previewFields, ok := preview["fields"].([]map[string]any); ok && len(previewFields) > 0 {
		fields["preview_fields"] = previewFields
	}
	if previewBlocks, ok := preview["blocks"].([]map[string]any); ok && len(previewBlocks) > 0 {
		fields["preview_blocks"] = previewBlocks
	}
}

func firstPreviewBlockText(blocks []map[string]any) string {
	for _, block := range blocks {
		if text := previewText(block["text"]); text != "" {
			return text
		}
		if fields, ok := block["fields"].([]map[string]any); ok {
			for _, field := range fields {
				if text := previewText(field); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func previewText(value any) string {
	textObject, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	text, ok := textObject["text"].(string)
	if !ok {
		return ""
	}
	return text
}

// webhookTarget represents a single webhook destination for RSS items
type webhookTarget struct {
	url           string
	destinationID string
	messenger     string
	filters       *filter.Rules
	quietHours    *quiethours.Policy
}

type rssDeliveryAttemptSet map[string]struct{}

func rssDeliveryAttemptKey(itemKey, destinationID string) string {
	return itemKey + "\x00" + destinationID
}

type rssDeliveryMode string

const (
	rssDeliveryModeFresh    rssDeliveryMode = "fresh"
	rssDeliveryModeRecovery rssDeliveryMode = "recovery"
)

type rssDeliveryOutcome int

const (
	rssDeliveryOutcomeSkipped rssDeliveryOutcome = iota
	rssDeliveryOutcomeSent
	rssDeliveryOutcomeAlreadySent
	rssDeliveryOutcomeFiltered
	rssDeliveryOutcomeStale
	rssDeliveryOutcomeFailed
	rssDeliveryOutcomeCanceled
)

type rssDeliveryItem struct {
	key        string
	entry      model.RSSEntry
	title      string
	feedTitle  string
	feedURL    string
	lookupKeys []string
}

func freshRSSDeliveryItem(entry model.RSSEntry) rssDeliveryItem {
	key := rss.GenerateEntryKeyForEntry(entry)
	return rssDeliveryItem{
		key:        key,
		entry:      entry,
		title:      entry.Title,
		feedTitle:  entry.FeedTitle,
		feedURL:    entry.FeedURL,
		lookupKeys: rss.GenerateEntryLookupKeysForEntry(entry),
	}
}

func recoveryRSSDeliveryItem(storedItem status.UnsentRSSItem, entry model.RSSEntry) rssDeliveryItem {
	return rssDeliveryItem{
		key:        storedItem.Key,
		entry:      entry,
		title:      storedItem.Title,
		feedTitle:  storedItem.FeedTitle,
		feedURL:    storedItem.FeedURL,
		lookupKeys: []string{storedItem.Key},
	}
}

func rssDestinationID(messenger status.Messenger, feedType string) string {
	return config.RSSDestinationID(messenger, feedType)
}

func rssDestinationIDForTarget(messenger status.Messenger, feedType, suffix string) string {
	return config.RSSDestinationIDForTarget(messenger, feedType, suffix)
}

func targetDestinationID(target webhookTarget) string {
	if target.destinationID != "" {
		return target.destinationID
	}
	return target.url
}

func webhookLockKey(messenger, webhookURL string) string {
	return strings.TrimSpace(messenger) + "\x00" + strings.TrimSpace(webhookURL)
}

func (s *Scheduler) webhookLock(messenger, webhookURL string) *sync.Mutex {
	key := webhookLockKey(messenger, webhookURL)
	s.webhookLocksMu.Lock()
	defer s.webhookLocksMu.Unlock()

	if s.webhookLocks == nil {
		s.webhookLocks = make(map[string]*sync.Mutex)
	}
	lock, exists := s.webhookLocks[key]
	if !exists {
		lock = &sync.Mutex{}
		s.webhookLocks[key] = lock
	}
	return lock
}

func (s *Scheduler) sendWithWebhookLock(ctx context.Context, messenger, webhookURL string, send func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	lock := s.webhookLock(messenger, webhookURL)
	lock.Lock()
	defer lock.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return send()
}

func newWebhookTarget(webhook config.WebhookConfig, destinationID string, messenger status.Messenger) webhookTarget {
	return webhookTarget{
		url:           webhook.URL,
		destinationID: destinationID,
		messenger:     messenger.String(),
		filters:       config.FilterRules(webhook.Filters),
		quietHours:    config.QuietHoursPolicy(webhook.QuietHours),
	}
}

func waitForSendDelay(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// getWebhookTargets returns all enabled webhook targets for a given feed type.
func (s *Scheduler) getWebhookTargets(feedType string) []webhookTarget {
	cfg := s.getConfig()
	return webhookTargetsForConfig(cfg, feedType)
}

func webhookTargetsForConfig(cfg *config.Config, feedType string) []webhookTarget {
	route, ok := rssFeedRouteForType(cfg, feedType)
	if !ok {
		return nil
	}
	return webhookTargetsForRoute(cfg, route)
}

func webhookTargetsForRoute(cfg *config.Config, route rssFeedRoute) []webhookTarget {
	var targets []webhookTarget
	for _, targetConfig := range config.WebhookTargetsForType(cfg, route.webhookType) {
		if !targetConfig.Webhook.Enabled {
			continue
		}
		messenger, ok := messengerForWebhookPlatform(targetConfig.Platform)
		if !ok {
			continue
		}
		targets = append(targets, newWebhookTarget(
			targetConfig.Webhook,
			rssDestinationIDForTarget(messenger, route.feedType, targetConfig.DestinationSuffix),
			messenger,
		))
	}

	return targets
}

func webhookURLsFromTargets(targets []webhookTarget) []string {
	urls := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.url != "" {
			urls = append(urls, target.url)
		}
	}
	return urls
}

func destinationIDsFromTargets(targets []webhookTarget) []string {
	destinationIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if destinationID := targetDestinationID(target); destinationID != "" {
			destinationIDs = append(destinationIDs, destinationID)
		}
	}
	return destinationIDs
}

func (s *Scheduler) activeRSSDestinationIDs() []string {
	cfg := s.getConfig()
	targets := make([]webhookTarget, 0, 6)
	for _, route := range rssFeedRoutes(cfg) {
		targets = append(targets, webhookTargetsForRoute(cfg, route)...)
	}
	return destinationIDsFromTargets(targets)
}

func (s *Scheduler) cleanupStatusRetention() {
	if s.dryRun || s.statusTracker == nil {
		return
	}
	s.statusTracker.CleanupOldEntriesForRSSDestinations(s.activeRSSDestinationIDs())
	s.persistStatus("status retention cleanup")
}

// checkRSSOnce performs a single RSS feed check with parse-once pattern
// Each feed type is parsed exactly once, then results are sent to all enabled webhooks
func (s *Scheduler) checkRSSOnce(ctx context.Context) checkOutcome {
	pollFields := newPollLogFields(pollTypeRSS)

	// Prevent overlapping RSS runs – skip if a previous run is still in progress
	if !s.rssMu.TryLock() {
		log.WithFields(pollFields).Warn("Skipping RSS check: previous run still in progress")
		return checkOutcome{ran: false, ok: true}
	}
	defer s.rssMu.Unlock()
	defer s.cleanupStatusRetention()

	cfg := s.getConfig()

	log.WithFields(pollFields).Debug("Checking RSS feeds for new entries")

	{
		cycleCtx, cycleCancel := context.WithTimeout(ctx, rssCheckTimeout(cfg))
		defer cycleCancel()

		// Parse all enabled feed types through one worker pool. Results are
		// partitioned by feed type so delivery behavior stays scoped per target type.
		attemptedRSSDeliveries := rssDeliveryAttemptSet{}
		batches, feedURLsToParse := s.rssFeedBatchesForConfig(cfg, pollFields)
		ok, budgetOverrun := s.processRSSFeedBatches(cycleCtx, cfg, pollFields, batches, feedURLsToParse, attemptedRSSDeliveries)

		if !s.dryRun {
			s.sendUnsentRSSItemsWithConfig(cycleCtx, cfg, attemptedRSSDeliveries)
			pruneRSSFeedStatusForConfig(s.statusTracker, cfg, s.dryRun)
		}
		return checkOutcome{ran: true, ok: ok, rssBudgetOverrun: budgetOverrun}
	}

}

// processRSSFeedBatches parses every feed due this cycle and applies the
// results. Its second return, budgetOverrun, is true whenever the parse ended
// because ctx's deadline (the caller's rss_check_timeout) expired before every
// feed answered -- regardless of whether that also emptied feedResults (F1):
// resultsOnContextError can hand back a non-nil but empty result, in which
// case this function still walks batches and applies nothing, silently. A
// caller that only looked at "ok" (true in both cases, since a budget overrun
// is not treated as a feed failure) could not tell "genuinely found nothing"
// apart from "never got the chance to look" -- which is exactly the
// distinction --healthcheck's empty-data_dir gate needs.
func (s *Scheduler) processRSSFeedBatches(
	ctx context.Context,
	cfg *config.Config,
	pollFields log.Fields,
	batches []rssFeedBatch,
	feedURLsToParse []string,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) (ok bool, budgetOverrun bool) {
	feedURLsToParse = s.filterRSSFeedURLsForCooldown(feedURLsToParse, pollFields)
	if len(feedURLsToParse) == 0 {
		return true, false
	}

	feedValidators := s.rssFeedHTTPValidators(feedURLsToParse)
	feedResults, err := s.rssParser.ParseMultipleFeedsWithValidators(
		ctx,
		feedURLsToParse,
		cfg.MaxRSSWorkers,
		feedValidators,
	)
	budgetOverrun = errors.Is(err, context.DeadlineExceeded)
	if err != nil {
		logRSSBatchParseError(cfg, pollFields, feedURLsToParse, feedResults, err)
	}
	if feedResults == nil {
		s.recordRSSBatchFailure(feedURLsToParse, err)
		return err == nil || errors.Is(err, context.Canceled) || budgetOverrun, budgetOverrun
	}

	for _, batch := range batches {
		s.processRSSBatch(ctx, cfg, feedResults, batch, attemptedRSSDeliveries)
	}
	return true, budgetOverrun
}

// logRSSBatchParseError reports a batch parse that did not complete normally.
// An exhausted cycle budget is not a feed failure: feeds that answered in time
// are applied by the caller, and feeds that did not are reported as not polled
// and keep their previous health, cooldown and cache validators.
func logRSSBatchParseError(
	cfg *config.Config,
	pollFields log.Fields,
	feedURLs []string,
	feedResults *rss.FeedResults,
	err error,
) {
	if errors.Is(err, context.DeadlineExceeded) {
		completed := 0
		if feedResults != nil {
			completed = len(feedResults.Entries)
		}
		log.WithFields(mergeLogFields(pollFields, log.Fields{
			"feeds_total":      len(feedURLs),
			"feeds_completed":  completed,
			"feeds_not_polled": len(feedURLs) - completed,
			"budget":           rssCheckTimeout(cfg).String(),
		})).Warn("RSS cycle budget exhausted, remaining feeds were not polled")
		return
	}
	log.WithError(err).WithFields(mergeLogFields(pollFields, log.Fields{
		"feed_count": len(feedURLs),
	})).Error("Failed to parse RSS feed batch")
}

func (s *Scheduler) filterRSSFeedURLsForCooldown(feedURLs []string, pollFields log.Fields) []string {
	if s.dryRun || s.statusTracker == nil || len(feedURLs) == 0 {
		return feedURLs
	}

	now := time.Now().UTC()
	pollable := feedURLs[:0]
	skipped := 0
	for _, feedURL := range feedURLs {
		if s.statusTracker.ShouldPollRSSFeed(feedURL, now) {
			pollable = append(pollable, feedURL)
			continue
		}
		skipped++
		log.WithFields(mergeLogFields(pollFields, log.Fields{
			"feed_url": textutil.RedactURLCredentials(feedURL),
		})).Info("Skipping RSS feed during failure cooldown")
	}
	if skipped > 0 {
		log.WithFields(mergeLogFields(pollFields, log.Fields{
			"skipped_feed_count": skipped,
			"pollable_feeds":     len(pollable),
		})).Info("Skipped RSS feeds still in failure cooldown")
	}
	return pollable
}

func (s *Scheduler) processRSSBatch(
	ctx context.Context,
	cfg *config.Config,
	feedResults *rss.FeedResults,
	batch rssFeedBatch,
	attemptedRSSDeliveries rssDeliveryAttemptSet,
) {
	feedType := batch.route.feedType
	batchResults := feedResultsForURLs(feedResults, batch.feedURLs)

	s.filterAndRecordNewRSSItems(batchResults, feedType)
	if !s.dryRun {
		s.recordParsedRSSFeedTypes(batchResults, feedType)
		s.minimizeRSSItemsWithoutMatchingTarget(batchResults, batch.targets)
		s.persistStatus("rss batch processed")
	}

	s.sendParsedRSSToWebhooksWithConfig(ctx, cfg, batchResults, feedType, batch.targets, attemptedRSSDeliveries)
}

func (s *Scheduler) rssFeedBatchesForConfig(cfg *config.Config, pollFields log.Fields) ([]rssFeedBatch, []string) {
	routes := rssFeedRoutes(cfg)
	batches := make([]rssFeedBatch, 0, len(routes))
	feedURLsToParse := make([]string, 0)

	for _, route := range routes {
		feedType := route.feedType
		feedURLs := route.feedURLs
		targets := webhookTargetsForRoute(cfg, route)
		if len(feedURLs) == 0 {
			if len(targets) > 0 && feedType != config.FeedTypeRansomware {
				log.WithFields(mergeLogFields(pollFields, log.Fields{
					"feed_type":       feedType,
					"configured_urls": len(feedURLs),
					"targets":         len(targets),
				})).Warn("RSS webhook targets are enabled but no feed URLs are configured for this feed type")
			}
			continue
		}

		if len(targets) == 0 {
			log.WithFields(mergeLogFields(pollFields, log.Fields{
				"feed_type":       feedType,
				"configured_urls": len(feedURLs),
			})).Warn("RSS feed URLs are configured but no webhook target is enabled for this feed type")
			continue
		}

		batches = append(batches, rssFeedBatch{
			route:    route,
			feedURLs: feedURLs,
			targets:  targets,
		})
		feedURLsToParse = append(feedURLsToParse, feedURLs...)
	}

	return batches, feedURLsToParse
}

func (s *Scheduler) rssFeedHTTPValidators(feedURLs []string) map[string]rss.FeedHTTPValidators {
	if s.dryRun || s.statusTracker == nil || len(feedURLs) == 0 {
		return nil
	}
	return s.statusTracker.GetRSSFeedHTTPValidators(feedURLs)
}

func feedResultsForURLs(source *rss.FeedResults, feedURLs []string) *rss.FeedResults {
	results := &rss.FeedResults{
		Entries:         make(map[string][]model.RSSEntry, len(feedURLs)),
		FeedErrors:      make(map[string]string),
		FeedValidators:  make(map[string]rss.FeedHTTPValidators),
		FeedNotModified: make(map[string]bool),
	}
	if source == nil {
		return results
	}
	for _, feedURL := range feedURLs {
		if entries, ok := source.Entries[feedURL]; ok {
			results.Entries[feedURL] = entries
		}
		if errMsg, ok := source.FeedErrors[feedURL]; ok {
			results.FeedErrors[feedURL] = errMsg
			if _, exists := results.Entries[feedURL]; !exists {
				results.Entries[feedURL] = []model.RSSEntry{}
			}
		}
		if validators, ok := source.FeedValidators[feedURL]; ok {
			results.FeedValidators[feedURL] = validators
		}
		if source.FeedNotModified[feedURL] {
			results.FeedNotModified[feedURL] = true
		}
	}
	return results
}

func apiCheckTimeout(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.APICheckTimeout > 0 {
		return cfg.APICheckTimeout
	}
	return config.DefaultAPICheckTimeout
}

func rssCheckTimeout(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.RSSCheckTimeout > 0 {
		return cfg.RSSCheckTimeout
	}
	return config.DefaultRSSCheckTimeout
}

// recordRSSBatchFailure marks every feed of a batch that produced no results at
// all as failed. A context error is excluded: an exhausted cycle budget and a
// shutdown cancellation are statements about this process, not about a feed, so
// neither may write a feed error or arm the failure cooldown.
func (s *Scheduler) recordRSSBatchFailure(feedURLs []string, err error) {
	if s.dryRun || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	operatorError := rss.OperatorErrorMessage(err)
	errorInfo := rssSourceErrorInfo(err)
	for _, feedURL := range feedURLs {
		s.statusTracker.UpdateFeedStatusWithErrorInfo(feedURL, false, 0, operatorError, errorInfo)
	}
	s.persistStatus("rss batch failure status")
}

func (s *Scheduler) filterAndRecordNewRSSItems(feedResults *rss.FeedResults, feedType string) int {
	if s.statusTracker == nil || feedResults == nil {
		return 0
	}

	recorded := 0
	skipped := 0
	for feedURL, entries := range feedResults.Entries {
		if len(entries) == 0 {
			continue
		}

		parsedKeys := s.statusTracker.GetRSSParsedItemKeys(feedURL)
		if parsedKeys == nil {
			parsedKeys = make(map[string]struct{})
		}
		newEntries := entries[:0]
		storedEntries := make(map[string]status.StoredRSSEntry)
		for _, entry := range entries {
			key := rss.GenerateEntryKeyForEntry(entry)
			if key == "" {
				continue
			}

			alreadyParsed := false
			for _, lookupKey := range rss.GenerateEntryLookupKeysForEntry(entry) {
				if _, exists := parsedKeys[lookupKey]; exists {
					alreadyParsed = true
					break
				}
			}
			if alreadyParsed {
				skipped++
				continue
			}

			stored := status.StoredRSSEntryFromRSS(entry, key)
			stored.FeedType = feedType
			storedEntries[key] = stored
			parsedKeys[key] = struct{}{}
			newEntries = append(newEntries, entry)
			recorded++
		}

		if len(storedEntries) > 0 {
			s.statusTracker.MarkRSSItemsParsed(feedURL, storedEntries)
		}
		feedResults.Entries[feedURL] = newEntries
	}

	if recorded > 0 || skipped > 0 {
		log.WithFields(log.Fields{
			"feed_type":       feedType,
			"recorded_items":  recorded,
			"duplicate_items": skipped,
			"status_backed":   !s.dryRun,
		}).Debug("Filtered and recorded parsed RSS items")
	}
	return recorded
}

func (s *Scheduler) recordParsedRSSFeedTypes(feedResults *rss.FeedResults, feedType string) int {
	if s.dryRun || s.statusTracker == nil || feedResults == nil || feedType == "" {
		return 0
	}

	updated := 0
	for _, entries := range feedResults.Entries {
		for _, entry := range entries {
			key := rss.GenerateEntryKeyForEntry(entry)
			if key == "" {
				continue
			}
			if s.statusTracker.SetRSSParsedItemFeedType(entry.FeedURL, key, feedType) {
				updated++
			}
		}
	}
	if updated > 0 {
		log.WithFields(log.Fields{
			"feed_type":     feedType,
			"updated_items": updated,
		}).Debug("Recorded feed type for parsed RSS items")
	}
	return updated
}

func (s *Scheduler) minimizeRSSItemsWithoutMatchingTarget(feedResults *rss.FeedResults, targets []webhookTarget) int {
	if s.dryRun || s.statusTracker == nil || feedResults == nil {
		return 0
	}

	minimized := 0
	for _, entries := range feedResults.Entries {
		for _, entry := range entries {
			if rssEntryMatchesAnyTargetFilter(entry, targets) {
				continue
			}
			key := rss.GenerateEntryKeyForEntry(entry)
			if key == "" {
				continue
			}
			if s.statusTracker.MinimizeRSSParsedItem(entry.FeedURL, key) {
				minimized++
			}
		}
	}
	if minimized > 0 {
		log.WithFields(log.Fields{
			"minimized_items": minimized,
		}).Debug("Minimized RSS parsed items that do not match any enabled target filter")
	}
	return minimized
}

func rssEntryMatchesAnyTargetFilter(entry model.RSSEntry, targets []webhookTarget) bool {
	for _, target := range targets {
		if filter.MatchesRSSEntry(target.filters, entry) {
			return true
		}
	}
	return false
}

func formatLogTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

// effectiveRSSSendMaxAge returns the strictest positive age limit that governs
// whether an RSS item is fresh enough to send. It combines the explicit send
// freshness gate (rss_max_item_age) with the dedup horizon
// (status_retention.rss_parsed_max_age).
//
// The dedup horizon must gate sending because sending an item older than the
// horizon guarantees a future duplicate: retention prunes the item's sent
// marker together with the parsed item by age, while the entry can still be
// present upstream (e.g. BSI keeps old advisories in its feed). The next poll
// then re-parses and re-delivers it. In short: never send what you cannot
// remember having sent. A value of 0 means "no limit" for either input; the
// result is 0 only when both are unlimited.
func effectiveRSSSendMaxAge(sendMaxAge, dedupHorizon time.Duration) time.Duration {
	if sendMaxAge <= 0 {
		if dedupHorizon <= 0 {
			return 0
		}
		return dedupHorizon
	}
	if dedupHorizon > 0 && dedupHorizon < sendMaxAge {
		return dedupHorizon
	}
	return sendMaxAge
}

func rssEntryExceedsMaxAge(published time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	if published.IsZero() {
		// Undated: no date at all, or one internal/rss could not parse (it WARNs
		// once per feed). "Unknown age" is not "too old" - treating it as too old
		// drops 100% of such a feed's items, and readme.md documents
		// rss_max_item_age as a limit on how old an item may be, not as an
		// undated policy. Both the send path and the recovery limiter go through
		// this predicate, so they cannot diverge on an undated item.
		return false
	}
	return time.Since(published) > maxAge
}

func staleRSSLogFields(itemKey string, published time.Time, maxAge time.Duration) log.Fields {
	fields := log.Fields{
		"item_key":        itemKey,
		"published":       formatLogTimestamp(published),
		"max_age_minutes": int64(maxAge / time.Minute),
	}
	if published.IsZero() {
		fields["age_known"] = false
		return fields
	}
	fields["age_known"] = true
	fields["age_minutes"] = int64(time.Since(published).Round(time.Minute) / time.Minute)
	return fields
}

// persistStatus flushes pending tracker state to disk. SavePendingChanges
// already logs the underlying I/O error and re-marks the affected state as
// dirty, so this only adds the scheduler step that triggered the flush.
func (s *Scheduler) persistStatus(step string) {
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		log.WithError(err).WithField("step", step).Warn("Pending status changes not persisted; will retry on next flush")
	}
}
