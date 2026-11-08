package status

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func writeLegacyDestinationManifest(t *testing.T, dir string, active map[string]string) {
	t.Helper()
	data, err := json.Marshal(DestinationManifest{Version: 1, Destinations: active})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DestinationManifestPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIsPositionalDestinationIDAcceptsAllGeneratedSuffixes(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{id: "discord.ransomware", want: true},
		{id: "discord.rss.ransomware.2", want: true},
		{id: "slack_compatible.rss.government.10", want: true},
		{id: "slack.rss.general.100", want: true},
		{id: "discord.ransomware.1"},
		{id: "discord.ransomware.02"},
		{id: "discord.ransomware.2x"},
		{id: "https://example.test/SECRET"},
	}
	for _, tc := range cases {
		if got := isPositionalDestinationID(tc.id); got != tc.want {
			t.Errorf("isPositionalDestinationID(%q) = %t, want %t", tc.id, got, tc.want)
		}
	}
}

func TestDestinationManifestRetainsRemovedBindingsAcrossStarts(t *testing.T) {
	dir := t.TempDir()
	first := map[string]string{
		"discord.ransomware": hashA, "discord.ransomware.2": hashB,
		"discord.rss.ransomware": hashA, "discord.rss.ransomware.2": hashB,
	}
	if err := WriteDestinationManifest(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := WriteDestinationManifest(dir, nil); err != nil {
		t.Fatal(err)
	}
	removed, found, err := LoadDestinationManifest(dir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if removed.Version != DestinationManifestVersion || !reflect.DeepEqual(removed.Retired, first) {
		t.Fatalf("retired bindings = %v, want %v (version %d)", removed.Retired, first, removed.Version)
	}
	if diff := DiffDestinationManifest(removed, first); diff.HasRemap() {
		t.Fatalf("same-hash return refused: %+v", diff)
	}
	swapped := map[string]string{
		"discord.ransomware": hashB, "discord.ransomware.2": hashA,
		"discord.rss.ransomware": hashB, "discord.rss.ransomware.2": hashA,
	}
	diff := DiffDestinationManifest(removed, swapped)
	if !diff.HasRemap() || diff.Kind() != DestinationChangeReorder || len(diff.RemappedIDs()) != 4 {
		t.Fatalf("swapped return escaped guard: %+v", diff)
	}
	if len(diff.Removed) != 0 || len(diff.Added) != 0 {
		t.Fatalf("historical IDs reported as current add/remove: %+v", diff)
	}
	if err := WriteDestinationManifest(dir, first); err != nil {
		t.Fatal(err)
	}
	restored, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Retired) != 0 || !reflect.DeepEqual(restored.Destinations, first) {
		t.Fatalf("restored manifest = %+v", restored)
	}
}

func TestDestinationManifestV1OnlyBlocksPersistedUnknownIDs(t *testing.T) {
	const activeID = "discord.ransomware"
	const forgottenID = "discord.ransomware.2"
	dir := t.TempDir()
	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracker.MarkAPIItemSentToDestination("item", "title", forgottenID)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	writeLegacyDestinationManifest(t, dir, map[string]string{activeID: hashA})
	manifest, found, err := LoadDestinationManifest(dir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(manifest.UnknownIDs, []string{forgottenID}) || manifest.Unattributed {
		t.Fatalf("v1 recovered history = %+v", manifest)
	}
	unsafe := map[string]string{activeID: hashA, forgottenID: hashB}
	if diff := DiffDestinationManifest(manifest, unsafe); !diff.HasRemap() || diff.Kind() != DestinationChangeUnknown {
		t.Fatalf("v1 forgotten ID escaped unknown-binding guard: %+v", diff)
	}
	safeAppend := map[string]string{activeID: hashA, "discord.ransomware.3": hashC}
	if diff := DiffDestinationManifest(manifest, safeAppend); diff.HasRemap() {
		t.Fatalf("provably fresh append refused: %+v", diff)
	}
	if err := WriteDestinationManifest(dir, map[string]string{activeID: hashA}); err != nil {
		t.Fatal(err)
	}
	upgraded, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Version != DestinationManifestVersion || !reflect.DeepEqual(upgraded.UnknownIDs, []string{forgottenID}) {
		t.Fatalf("v1 unknown ID lost at upgrade: %+v", upgraded)
	}
}

func TestDestinationManifestV2StaleAfterFailedAppendWriteGuardsReusedID(t *testing.T) {
	const staleID = "discord.ransomware.2"
	dir := t.TempDir()
	if err := WriteDestinationManifest(dir, map[string]string{"discord.ransomware": hashA}); err != nil {
		t.Fatal(err)
	}
	// The append ran and sent an alert, but its manifest write failed. The
	// persisted marker is the only evidence of that previously used ID.
	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracker.MarkAPIItemSentToDestination("old-item", "title", staleID)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	manifest, found, err := LoadDestinationManifest(dir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(manifest.UnknownIDs, []string{staleID}) {
		t.Fatalf("stale v2 manifest did not recover used ID: %+v", manifest)
	}
	if diff := DiffDestinationManifest(manifest, map[string]string{
		"discord.ransomware": hashA, staleID: hashC,
	}); !diff.HasRemap() {
		t.Fatalf("reused ID escaped stale-v2 guard: %+v", diff)
	}
}

func TestDestinationManifestV2StaleBlankOwnerGuardsEveryNewID(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDestinationManifest(dir, map[string]string{"discord.ransomware": hashA}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+string(os.PathSeparator)+statusAPIFileName,
		[]byte(`{"sent_items":{"opaque-composite":{"sent_at":"2026-01-01T00:00:00Z"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, found, err := LoadDestinationManifest(dir)
	if err != nil || !found || !manifest.Unattributed {
		t.Fatalf("v2 blank-owner load = manifest %+v, found %v, err %v", manifest, found, err)
	}
	if err := WriteDestinationManifest(dir, map[string]string{
		"discord.ransomware": hashA, "discord.ransomware.2": hashB,
	}); err != nil {
		t.Fatal(err)
	}
	accepted, _, err := LoadDestinationManifest(dir)
	if err != nil || !accepted.Unattributed {
		t.Fatalf("v2 uncertainty lost after acceptance: %+v, err %v", accepted, err)
	}
	if diff := DiffDestinationManifest(accepted, map[string]string{
		"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC,
	}); !diff.HasRemap() {
		t.Fatalf("future ID escaped v2 blank-owner guard: %+v", diff)
	}
}

func TestDestinationManifestV1RecoversEveryPersistedRowKind(t *testing.T) {
	for _, name := range []string{"API sent marker", "RSS sent marker", "queued retry", "dead letter"} {
		t.Run(name, func(t *testing.T) {
			assertV1RecoversPersistedRowKind(t, name)
		})
	}
}

func assertV1RecoversPersistedRowKind(t *testing.T, name string) {
	t.Helper()
	const forgottenID = "discord.ransomware.2"
	dir := t.TempDir()
	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedPersistedRowKind(t, tracker, name, forgottenID)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	writeLegacyDestinationManifest(t, dir, map[string]string{"discord.ransomware": hashA})
	manifest, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.UnknownIDs, []string{forgottenID}) || manifest.Unattributed {
		t.Fatalf("recovered IDs = %v, unattributed = %t", manifest.UnknownIDs, manifest.Unattributed)
	}
}

func seedPersistedRowKind(t *testing.T, tracker *Tracker, name, id string) {
	t.Helper()
	switch name {
	case "API sent marker":
		tracker.MarkAPIItemSentToDestination("api-item", "title", id)
	case "RSS sent marker":
		tracker.MarkRSSItemSentToDestination("rss-item", "title", "feed", id)
	case "queued retry":
		if !tracker.EnqueueRetryForDestination(RetryRequest{
			ItemKey: "retry-item", DestinationID: id, Messenger: MessengerDiscord,
			ItemType: RetryItemTypeAPI, Title: "title", LastError: "failure",
		}) {
			t.Fatal("EnqueueRetryForDestination() = false")
		}
	case "dead letter":
		tracker.MarkRetryDeadLetterForDestination("dead-item", id,
			MessengerDiscord.String(), RetryItemTypeAPI.String(), "title", "failure")
	default:
		t.Fatalf("unknown persisted row kind %q", name)
	}
}

func TestDestinationManifestV1UnattributedStateKeepsFutureIDsGuardedAfterAccept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(DestinationManifestPath(dir), []byte(`{"version":1,"destinations":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+string(os.PathSeparator)+statusAPIFileName,
		[]byte(`{"sent_items":{"opaque-composite":{"sent_at":"2026-01-01T00:00:00Z"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, found, err := LoadDestinationManifest(dir)
	if err != nil || !found || !manifest.Unattributed {
		t.Fatalf("v1 unattributed load = manifest %+v, found %v, err %v", manifest, found, err)
	}
	first := map[string]string{"discord.ransomware": hashA}
	if diff := DiffDestinationManifest(manifest, first); !diff.HasRemap() || diff.Kind() != DestinationChangeUnknown {
		t.Fatalf("unattributed marker escaped first-ID guard: %+v", diff)
	}
	// Simulate explicit --accept for the first ID. Its acceptance must not
	// silently authorize another unknown ID in a later run.
	if err := WriteDestinationManifest(dir, first); err != nil {
		t.Fatal(err)
	}
	accepted, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Unattributed {
		t.Fatalf("accepted manifest lost uncertainty: %+v", accepted)
	}
	second := map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB}
	if diff := DiffDestinationManifest(accepted, second); !diff.HasRemap() {
		t.Fatalf("second unknown ID escaped guard after accept: %+v", diff)
	}
}

func TestDestinationManifestV1UnreadableStatusKeepsNewIDsGuarded(t *testing.T) {
	dir := t.TempDir()
	writeLegacyDestinationManifest(t, dir, map[string]string{"discord.ransomware": hashA})
	if err := os.WriteFile(dir+string(os.PathSeparator)+statusAPIFileName, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, found, err := LoadDestinationManifest(dir)
	if err != nil || !found || !manifest.Unattributed {
		t.Fatalf("v1 corrupt-state load = manifest %+v, found %v, err %v", manifest, found, err)
	}
	current := map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB}
	if diff := DiffDestinationManifest(manifest, current); !diff.HasRemap() || diff.Kind() != DestinationChangeUnknown {
		t.Fatalf("unreadable status escaped guard: %+v", diff)
	}
}

func TestDestinationManifestV1BlankRetryOwnerKeepsNewIDsGuarded(t *testing.T) {
	dir := t.TempDir()
	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !tracker.EnqueueRetryForDestination(RetryRequest{
		ItemKey: "legacy-item", Messenger: MessengerDiscord, ItemType: RetryItemTypeAPI,
		Title: "title", LastError: "failure",
	}) {
		t.Fatal("EnqueueRetryForDestination() = false")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	writeLegacyDestinationManifest(t, dir, map[string]string{"discord.ransomware": hashA})
	manifest, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Unattributed {
		t.Fatal("legacy retry without a destination ID did not mark the binding uncertain")
	}
	if diff := DiffDestinationManifest(manifest, map[string]string{
		"discord.ransomware": hashA, "discord.ransomware.2": hashB,
	}); !diff.HasRemap() {
		t.Fatalf("new ID escaped guard with legacy retry: %+v", diff)
	}
}

func TestDestinationManifestNeverPersistsLegacyWebhookURLAsUnknownID(t *testing.T) {
	dir := t.TempDir()
	const webhook = "https://example.test/webhook/SECRET-TOKEN"
	data, err := json.Marshal(map[string]any{
		"sent_items": map[string]any{"opaque-composite": map[string]string{"destination_id": webhook}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+string(os.PathSeparator)+statusAPIFileName, data, 0600); err != nil {
		t.Fatal(err)
	}
	writeLegacyDestinationManifest(t, dir, map[string]string{})
	manifest, _, err := LoadDestinationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.UnknownIDs) != 0 {
		t.Fatalf("unknown IDs contain raw legacy destination: %v", manifest.UnknownIDs)
	}
	if !manifest.Unattributed {
		t.Fatal("raw legacy destination did not make new IDs uncertain")
	}
	if err := WriteDestinationManifest(dir, nil); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(DestinationManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET-TOKEN") {
		t.Fatalf("destination manifest leaked webhook token: %s", data)
	}
}
