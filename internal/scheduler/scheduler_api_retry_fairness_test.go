package scheduler

// Pins the round-robin selection roundRobinAPIRetryRecords / sortAPIRetryRecordsOldestFirst
// use for processAPIRetryQueue's per-cycle attemptLimit cap (operator
// decision: round-robin across destinations, oldest-first within each -- a
// plain oldest-first sort was implemented first, reviewed and rejected for
// letting one chronically-failing destination starve a healthy one).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// fairnessRetryItemJSON mirrors internal/status/retry_store.go's private
// retryItem JSON shape field-for-field, so a retry_status.json seeded here
// loads through the real status.NewTracker / GetQueuedRetryItemsByType path
// unmodified.
type fairnessRetryItemJSON struct {
	ItemKey       string `json:"item_key"`
	DestinationID string `json:"destination_id,omitempty"`
	Messenger     string `json:"messenger"`
	ItemType      string `json:"item_type"`
	Title         string `json:"title"`
	RetryCount    int    `json:"retry_count"`
	LastError     string `json:"last_error"`
	FirstFailed   string `json:"first_failed"`
	LastRetried   string `json:"last_retried"`
}

// fairnessSeedItem is one queued API retry record to seed, in explicit
// caller-given order -- never derived from map iteration, so the seed data
// itself carries no cross-process randomness for the determinism test to
// mistake for a bug in roundRobinAPIRetryRecords.
type fairnessSeedItem struct {
	itemKey       string
	destinationID string
	messenger     string
	firstFailed   time.Time // zero value seeds an empty "first_failed" string
}

// writeAPIRetryStatusFileForTest writes a retry_status.json directly (the
// technique internal/status/persistence_extra_test.go and
// internal/scheduler/scheduler_paths_coverage_test.go already use for
// api_status.json), so FirstFailed values are pinned exactly rather than
// derived from statusNow(), which has no clock seam.
func writeAPIRetryStatusFileForTest(t *testing.T, dataDir string, items []fairnessSeedItem) {
	t.Helper()
	queue := make(map[string]fairnessRetryItemJSON, len(items))
	for _, it := range items {
		ff := ""
		if !it.firstFailed.IsZero() {
			ff = it.firstFailed.UTC().Format(time.RFC3339Nano)
		}
		queue["queue-"+it.itemKey] = fairnessRetryItemJSON{
			ItemKey:       it.itemKey,
			DestinationID: it.destinationID,
			Messenger:     it.messenger,
			ItemType:      status.RetryItemTypeAPI.String(),
			Title:         it.itemKey,
			RetryCount:    1,
			LastError:     "boom",
			FirstFailed:   ff,
			LastRetried:   ff,
		}
	}
	doc := map[string]any{
		"last_updated": time.Now().UTC(),
		"retry_queue":  queue,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal(retry_status.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "retry_status.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(retry_status.json) error = %v", err)
	}
}

func loadAPIRetryRecordsForTest(t *testing.T, dataDir string) []status.QueuedRetryRecord {
	t.Helper()
	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker(%q) error = %v", dataDir, err)
	}
	return tracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
}

func itemKeysForTest(records []status.QueuedRetryRecord) []string {
	keys := make([]string, len(records))
	for i, r := range records {
		keys[i] = r.Item.ItemKey
	}
	return keys
}

// TestRoundRobinAPIRetryRecordsFirstCapIncludesHealthyDestination pins the
// round-robin guarantee itself, not just that the output is sorted: a
// chronically-failing destination ("A", 20 old items) must not be able to
// crowd a healthy destination ("B", 3 much newer items) out of the first
// cap-sized slice of roundRobinAPIRetryRecords's own output.
func TestRoundRobinAPIRetryRecordsFirstCapIncludesHealthyDestination(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var items []fairnessSeedItem
	for i := 0; i < 20; i++ {
		items = append(items, fairnessSeedItem{
			itemKey:       fmt.Sprintf("A-%02d", i),
			destinationID: "A",
			messenger:     status.MessengerSlack.String(),
			firstFailed:   base.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < 3; i++ {
		items = append(items, fairnessSeedItem{
			itemKey:       fmt.Sprintf("B-%02d", i),
			destinationID: "B",
			messenger:     status.MessengerDiscord.String(),
			firstFailed:   base.Add(2*time.Hour + time.Duration(i)*time.Minute),
		})
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	ordered := roundRobinAPIRetryRecords(records, map[string]string{})

	const cap_ = 5
	if len(ordered) < cap_ {
		t.Fatalf("ordered records = %d, want at least %d", len(ordered), cap_)
	}
	firstCap := ordered[:cap_]
	bInFirstCap := false
	for _, r := range firstCap {
		if r.Item.DestinationID == "B" {
			bInFirstCap = true
		}
	}
	if !bInFirstCap {
		t.Fatalf("destination B has no item in the first %d records %v -- round-robin guarantee violated", cap_, itemKeysForTest(firstCap))
	}
	if ordered[0].Item.ItemKey != "A-00" {
		t.Fatalf("first item overall = %q, want %q (the globally oldest failure must still be attempted first)", ordered[0].Item.ItemKey, "A-00")
	}
}

// TestProcessAPIRetryQueueRoundRobinAttemptsEveryDestination drives the real
// processAPIRetryQueue end to end (not a reimplementation) across two real
// destinations, with the per-cycle cap below the combined backlog. Before
// this fix, whichever records the map happened to yield first that call
// decided the attempted set, with no destination awareness; the healthy
// destination (discord, 2 items) could be starved by the larger,
// older-enqueued destination (slack, 5 items) purely by map-order luck.
func TestProcessAPIRetryQueueRoundRobinAttemptsEveryDestination(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIMaxEntriesPerCycle = 3
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T12345678/B12345678/retry-token",
	}
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123456789012345678/retry-token",
	}
	slackSender := &recordingWebhookSender{}
	discordSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender
	s.discordWebhookSender = discordSender

	for i := 0; i < 5; i++ {
		enqueueAPIRetryPayloadForDestination(t, s, fmt.Sprintf("slack-victim-%d", i), apiDestinationID(status.MessengerSlack), status.MessengerSlack.String())
	}
	for i := 0; i < 2; i++ {
		enqueueAPIRetryPayloadForDestination(t, s, fmt.Sprintf("discord-victim-%d", i), apiDestinationID(status.MessengerDiscord), status.MessengerDiscord.String())
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if got := slackSender.apiCallCount() + discordSender.apiCallCount(); got != 3 {
		t.Fatalf("total API retry send attempts = %d, want 3 capped attempts", got)
	}
	if discordSender.apiCallCount() == 0 {
		t.Fatalf("discord (healthy, smaller) destination got 0 attempts this cycle; round-robin guarantee violated (slack calls=%d, discord calls=%d)",
			slackSender.apiCallCount(), discordSender.apiCallCount())
	}
	if remaining := s.statusTracker.GetQueuedRetryItemsByType("api"); len(remaining) != 4 {
		t.Fatalf("remaining API retry queue length = %d, want 4", len(remaining))
	}
}

// TestRoundRobinAPIRetryRecordsDeterministicAcrossRepeatedLoads pins that
// repeated fresh loads of the same on-disk queue always produce the same
// round-robin order -- never Go's randomized map iteration leaking through.
func TestRoundRobinAPIRetryRecordsDeterministicAcrossRepeatedLoads(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var items []fairnessSeedItem
	n := 0
	for _, dest := range []string{"slack.ransomware", "discord.ransomware", "slack.rss.general"} {
		count := map[string]int{"slack.ransomware": 7, "discord.ransomware": 5, "slack.rss.general": 3}[dest]
		messenger := status.MessengerSlack.String()
		if dest == "discord.ransomware" {
			messenger = status.MessengerDiscord.String()
		}
		for k := 0; k < count; k++ {
			items = append(items, fairnessSeedItem{
				itemKey:       fmt.Sprintf("%s-%02d", dest, k),
				destinationID: dest,
				messenger:     messenger,
				firstFailed:   base.Add(time.Duration(n) * time.Minute),
			})
			n++
		}
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	var want []string
	for i := 0; i < 30; i++ {
		records := loadAPIRetryRecordsForTest(t, dataDir)
		ordered := roundRobinAPIRetryRecords(records, map[string]string{})
		got := itemKeysForTest(ordered)
		if i == 0 {
			want = got
			continue
		}
		if len(got) != len(want) {
			t.Fatalf("iteration %d: order length = %d, want %d", i, len(got), len(want))
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: order = %v, want %v (non-deterministic)", i, got, want)
			}
		}
	}
}

// TestSortAPIRetryRecordsOldestFirstMissingFirstFailedSortsFirst mirrors
// sortAPIEntriesOldestFirst's zero-Discovered handling: a record with a
// missing FirstFailed parses to the zero time.Time via
// timeutil.ParseFlexibleTimestampOrZero and sorts first.
func TestSortAPIRetryRecordsOldestFirstMissingFirstFailedSortsFirst(t *testing.T) {
	dataDir := t.TempDir()
	items := []fairnessSeedItem{
		{itemKey: "has-timestamp", destinationID: "A", messenger: status.MessengerSlack.String(), firstFailed: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{itemKey: "missing-timestamp", destinationID: "A", messenger: status.MessengerSlack.String()}, // firstFailed left zero -> empty string
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	sortAPIRetryRecordsOldestFirst(records)

	if records[0].Item.ItemKey != "missing-timestamp" {
		t.Fatalf("first record = %q, want %q (missing FirstFailed must sort first)", records[0].Item.ItemKey, "missing-timestamp")
	}
}

// TestRoundRobinAPIRetryRecordsPreservesOldestFirstWithinDestination checks
// that adding round-robin across destinations does not disturb the original
// per-destination fairness property: within one destination's group, the
// oldest record still goes first, in the same relative order as the plain
// sort would give it.
func TestRoundRobinAPIRetryRecordsPreservesOldestFirstWithinDestination(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var items []fairnessSeedItem
	for i := 0; i < 5; i++ {
		items = append(items, fairnessSeedItem{
			itemKey:       fmt.Sprintf("slack.ransomware-%02d", i),
			destinationID: "slack.ransomware",
			messenger:     status.MessengerSlack.String(),
			firstFailed:   base.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < 2; i++ {
		items = append(items, fairnessSeedItem{
			itemKey:       fmt.Sprintf("discord.ransomware-%02d", i),
			destinationID: "discord.ransomware",
			messenger:     status.MessengerDiscord.String(),
			firstFailed:   base.Add(10*time.Minute + time.Duration(i)*time.Minute),
		})
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	ordered := roundRobinAPIRetryRecords(records, map[string]string{})

	var slackSeen []string
	for _, r := range ordered {
		if r.Item.DestinationID == "slack.ransomware" {
			slackSeen = append(slackSeen, r.Item.ItemKey)
		}
	}
	want := []string{"slack.ransomware-00", "slack.ransomware-01", "slack.ransomware-02", "slack.ransomware-03", "slack.ransomware-04"}
	if len(slackSeen) != len(want) {
		t.Fatalf("slack.ransomware item count = %d, want %d", len(slackSeen), len(want))
	}
	for i := range want {
		if slackSeen[i] != want[i] {
			t.Fatalf("slack.ransomware relative order = %v, want %v (oldest-first within destination must be preserved)", slackSeen, want)
		}
	}
	if ordered[0].Item.ItemKey != "slack.ransomware-00" {
		t.Fatalf("first item overall = %q, want %q", ordered[0].Item.ItemKey, "slack.ransomware-00")
	}
}

// TestRoundRobinAPIRetryRecordsSingleDestinationMatchesPlainSort pins the
// degenerate single-destination case: roundRobinAPIRetryRecords must be
// byte-identical to calling sortAPIRetryRecordsOldestFirst directly.
func TestRoundRobinAPIRetryRecordsSingleDestinationMatchesPlainSort(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var items []fairnessSeedItem
	for i := 0; i < 6; i++ {
		items = append(items, fairnessSeedItem{
			itemKey:       fmt.Sprintf("slack.ransomware-%02d", i),
			destinationID: "slack.ransomware",
			messenger:     status.MessengerSlack.String(),
			firstFailed:   base.Add(time.Duration(i) * time.Minute),
		})
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	rrRecords := loadAPIRetryRecordsForTest(t, dataDir)
	rrOrdered := roundRobinAPIRetryRecords(rrRecords, map[string]string{})

	plainRecords := loadAPIRetryRecordsForTest(t, dataDir)
	sortAPIRetryRecordsOldestFirst(plainRecords)

	rrKeys := itemKeysForTest(rrOrdered)
	plainKeys := itemKeysForTest(plainRecords)
	if len(rrKeys) != len(plainKeys) {
		t.Fatalf("round-robin length = %d, plain sort length = %d", len(rrKeys), len(plainKeys))
	}
	for i := range rrKeys {
		if rrKeys[i] != plainKeys[i] {
			t.Fatalf("single-destination round-robin order = %v, want identical to plain oldest-first = %v", rrKeys, plainKeys)
		}
	}
}

// TestRoundRobinAPIRetryRecordsDegenerateInputs pins nil and single-record
// input being returned unchanged.
func TestRoundRobinAPIRetryRecordsDegenerateInputs(t *testing.T) {
	if got := roundRobinAPIRetryRecords(nil, map[string]string{}); len(got) != 0 {
		t.Fatalf("nil input: got %d records, want 0", len(got))
	}

	one := []status.QueuedRetryRecord{{
		QueueKey: "queue-solo",
		Item: status.RetryRecord{
			ItemKey:       "solo",
			DestinationID: "slack.ransomware",
			FirstFailed:   "2026-01-01T00:00:00Z",
		},
	}}
	got := roundRobinAPIRetryRecords(one, map[string]string{})
	if len(got) != 1 || got[0].Item.ItemKey != "solo" {
		t.Fatalf("single-record input: got %v, want unchanged [solo]", itemKeysForTest(got))
	}
}

// TestRoundRobinAPIRetryRecordsCapSmallerThanDestinationCount pins the
// documented residual trade-off (plan §2/§9): when the per-cycle cap is
// smaller than the number of distinct destinations with queued items,
// exactly the destinations holding the globally oldest items are served,
// deterministically -- not an arbitrary map-order subset.
func TestRoundRobinAPIRetryRecordsCapSmallerThanDestinationCount(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []fairnessSeedItem{
		{itemKey: "slack.a-00", destinationID: "slack.a", messenger: status.MessengerSlack.String(), firstFailed: base},
		{itemKey: "slack.b-00", destinationID: "slack.b", messenger: status.MessengerSlack.String(), firstFailed: base.Add(time.Minute)},
		{itemKey: "slack.c-00", destinationID: "slack.c", messenger: status.MessengerSlack.String(), firstFailed: base.Add(2 * time.Minute)},
		{itemKey: "slack.d-00", destinationID: "slack.d", messenger: status.MessengerSlack.String(), firstFailed: base.Add(3 * time.Minute)},
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	ordered := roundRobinAPIRetryRecords(records, map[string]string{})

	const cap_ = 2
	if len(ordered) < cap_ {
		t.Fatalf("ordered records = %d, want at least %d", len(ordered), cap_)
	}
	attempted := ordered[:cap_]
	attemptedDest := make(map[string]bool, cap_)
	for _, r := range attempted {
		attemptedDest[r.Item.DestinationID] = true
	}
	if len(attemptedDest) != cap_ {
		t.Fatalf("cap=%d < 4 destinations: expected exactly %d distinct destinations attempted, got %d (%v)", cap_, cap_, len(attemptedDest), attemptedDest)
	}
	if !attemptedDest["slack.a"] || !attemptedDest["slack.b"] {
		t.Fatalf("expected the two globally-oldest destinations (slack.a, slack.b) to be attempted under cap=%d, got %v", cap_, attemptedDest)
	}
}

// TestRoundRobinAPIRetryRecordsDestinationVisitOrderTiesBreakByDestinationID
// pins that two destinations whose oldest record shares the exact same
// FirstFailed are visited in destinationID string order -- deterministic,
// never sort.Slice's unspecified behaviour for the destination-visit sort.
func TestRoundRobinAPIRetryRecordsDestinationVisitOrderTiesBreakByDestinationID(t *testing.T) {
	dataDir := t.TempDir()
	tie := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []fairnessSeedItem{
		{itemKey: "zzz-00", destinationID: "zzz.dest", messenger: status.MessengerSlack.String(), firstFailed: tie},
		{itemKey: "aaa-00", destinationID: "aaa.dest", messenger: status.MessengerSlack.String(), firstFailed: tie},
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	ordered := roundRobinAPIRetryRecords(records, map[string]string{})

	if len(ordered) != 2 || ordered[0].Item.DestinationID != "aaa.dest" || ordered[1].Item.DestinationID != "zzz.dest" {
		t.Fatalf("tied destination-visit order = %v, want [aaa.dest zzz.dest] (ties broken by destinationID string)", itemKeysForTest(ordered))
	}
}

// TestRoundRobinAPIRetryRecordsLegacyDestinationFallbackGroupsWithStableID
// pins that grouping uses the RESOLVED destinationID from apiRetryRoute, not
// the raw Item.DestinationID struct field: a legacy row (empty
// DestinationID, resolved through legacyMessengerDestination) and
// stable-destination-ID rows for the same actual endpoint must land in one
// group, interleaved with a second real destination on equal footing.
//
// A two-row fixture (one legacy row plus one stable-ID row) does not
// discriminate here: under correct (resolved) grouping the two form a single
// group giving [legacy-00 stable-00], but under a mutant that groups on the
// raw Item.DestinationID field instead, "" becomes its own one-record group
// that -- being the older record -- is visited first, emitting the exact
// same [legacy-00 stable-00] sequence. The order-only assertion that used to
// live here could not tell those two behaviours apart.
//
// This fixture adds a second real destination (discord.ransomware, 3 rows)
// alongside one legacy slack row plus three stable-ID slack rows for the
// SAME slack endpoint. Correct grouping folds all four slack rows (legacy +
// stable) into one group and round-robins 1:1 with discord, oldest overall
// first: [L1 D1 S1 D2 S2 D3 S3]. Raw-field grouping instead treats "" as a
// third, separate one-record group visited between the two real
// destinations, so slack gets two turns per round and discord is
// under-served: [L1 S1 D1 S2 D2 S3 D3]. Asserting the full key order (not
// just the legacy/stable relative order) is what makes the two diverge.
func TestRoundRobinAPIRetryRecordsLegacyDestinationFallbackGroupsWithStableID(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []fairnessSeedItem{
		{itemKey: "L1", destinationID: "", messenger: status.MessengerSlack.String(), firstFailed: base},
	}
	for i := 1; i <= 3; i++ {
		items = append(items, fairnessSeedItem{
			itemKey: fmt.Sprintf("S%d", i), destinationID: "slack.ransomware",
			messenger: status.MessengerSlack.String(), firstFailed: base.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 1; i <= 3; i++ {
		items = append(items, fairnessSeedItem{
			itemKey: fmt.Sprintf("D%d", i), destinationID: "discord.ransomware",
			messenger: status.MessengerDiscord.String(), firstFailed: base.Add(time.Duration(9+i) * time.Minute),
		})
	}
	writeAPIRetryStatusFileForTest(t, dataDir, items)

	records := loadAPIRetryRecordsForTest(t, dataDir)
	legacy := map[string]string{status.MessengerSlack.String(): "slack.ransomware"}
	got := itemKeysForTest(roundRobinAPIRetryRecords(records, legacy))

	want := []string{"L1", "D1", "S1", "D2", "S2", "D3", "S3"}
	if len(got) != len(want) {
		t.Fatalf("order = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (legacy row must group with the stable-ID rows for the same endpoint, not form its own bogus destination)", got, want)
		}
	}
}
