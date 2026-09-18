package status

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

// Opaque stand-ins for sha256 hashes. DiffDestinations never interprets a hash,
// so readable placeholders keep the scenario table legible.
const (
	hashA = "hash-A"
	hashB = "hash-B"
	hashC = "hash-C"
	hashD = "hash-D"
)

func TestDiffDestinationsClassifiesScenarios(t *testing.T) {
	tests := []struct {
		name         string
		previous     map[string]string
		current      map[string]string
		wantHasRemap bool
		wantKind     string
		wantRemapped []string
		wantAdded    []string
		wantRemoved  []string
		wantMovement string
	}{
		{
			// Section 4 row (a): urls [A,B] -> [B,A] on a ransomware block, so
			// both the API and the RSS ID family move together.
			name: "a swap two urls",
			previous: map[string]string{
				"discord.ransomware":       hashA,
				"discord.ransomware.2":     hashB,
				"discord.rss.ransomware":   hashA,
				"discord.rss.ransomware.2": hashB,
			},
			current: map[string]string{
				"discord.ransomware":       hashB,
				"discord.ransomware.2":     hashA,
				"discord.rss.ransomware":   hashB,
				"discord.rss.ransomware.2": hashA,
			},
			wantHasRemap: true,
			wantKind:     "reorder",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2", "discord.rss.ransomware", "discord.rss.ransomware.2"},
			wantMovement: "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware, " +
				"discord.rss.ransomware<-discord.rss.ransomware.2, discord.rss.ransomware.2<-discord.rss.ransomware",
		},
		{
			name:         "b insert a url in front",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			current:      map[string]string{"discord.ransomware": hashC, "discord.ransomware.2": hashA, "discord.ransomware.3": hashB},
			wantHasRemap: true,
			wantKind:     "insert",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2"},
			wantAdded:    []string{"discord.ransomware.3"},
			wantMovement: "discord.ransomware<-*, discord.ransomware.2<-discord.ransomware",
		},
		{
			name:         "c delete the first of three",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC},
			current:      map[string]string{"discord.ransomware": hashB, "discord.ransomware.2": hashC},
			wantHasRemap: true,
			wantKind:     "delete",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2"},
			wantRemoved:  []string{"discord.ransomware.3"},
			wantMovement: "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware.3",
		},
		{
			name:     "d urls converted to targets keeping the order",
			previous: map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			current:  map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			wantKind: "",
		},
		{
			name:      "e append a third url",
			previous:  map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			current:   map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC},
			wantKind:  "",
			wantAdded: []string{"discord.ransomware.3"},
		},
		{
			name:        "f delete the last url",
			previous:    map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC},
			current:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			wantKind:    "",
			wantRemoved: []string{"discord.ransomware.3"},
		},
		{
			name:         "g rotate the token of the first url",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			current:      map[string]string{"discord.ransomware": hashD, "discord.ransomware.2": hashB},
			wantHasRemap: true,
			wantKind:     "url_changed",
			wantRemapped: []string{"discord.ransomware"},
			wantMovement: "discord.ransomware<-*",
		},
		{
			name:         "h swap and append in one edit",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
			current:      map[string]string{"discord.ransomware": hashB, "discord.ransomware.2": hashA, "discord.ransomware.3": hashC},
			wantHasRemap: true,
			wantKind:     "insert",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2"},
			wantAdded:    []string{"discord.ransomware.3"},
			wantMovement: "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware",
		},
		{
			name:         "h2 swap and delete in one edit",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC},
			current:      map[string]string{"discord.ransomware": hashB, "discord.ransomware.2": hashA},
			wantHasRemap: true,
			wantKind:     "delete",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2"},
			wantRemoved:  []string{"discord.ransomware.3"},
			wantMovement: "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware",
		},
		{
			name:         "h3 swap and rotate the last url",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashC},
			current:      map[string]string{"discord.ransomware": hashB, "discord.ransomware.2": hashA, "discord.ransomware.3": hashD},
			wantHasRemap: true,
			wantKind:     "reorder",
			wantRemapped: []string{"discord.ransomware", "discord.ransomware.2", "discord.ransomware.3"},
			wantMovement: "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware, discord.ransomware.3<-*",
		},
		{
			name:     "i shared url endpoints swapped does not trip",
			previous: map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashA},
			current:  map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashA},
			wantKind: "",
		},
		{
			name:     "i2 shared url in a three endpoint block does not trip",
			previous: map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashA},
			current:  map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashA},
			wantKind: "",
		},
		{
			// The multi-previous-owner case: two endpoints share URL A, and one
			// of them swaps with the endpoint carrying B.
			name:         "i3 shared url endpoint swapped with a different one",
			previous:     map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashA, "discord.ransomware.3": hashB},
			current:      map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB, "discord.ransomware.3": hashA},
			wantHasRemap: true,
			wantKind:     "reorder",
			wantRemapped: []string{"discord.ransomware.2", "discord.ransomware.3"},
			wantMovement: "discord.ransomware.2<-discord.ransomware.3, discord.ransomware.3<-discord.ransomware+discord.ransomware.2",
		},
		{
			name:     "j disabled block re-enabled with unchanged urls",
			previous: map[string]string{"slack.rss.government": hashA, "slack.rss.government.2": hashB},
			current:  map[string]string{"slack.rss.government": hashA, "slack.rss.government.2": hashB},
			wantKind: "",
		},
		{
			name:         "k disabled block whose urls were swapped",
			previous:     map[string]string{"slack.rss.government": hashA, "slack.rss.government.2": hashB},
			current:      map[string]string{"slack.rss.government": hashB, "slack.rss.government.2": hashA},
			wantHasRemap: true,
			wantKind:     "reorder",
			wantRemapped: []string{"slack.rss.government", "slack.rss.government.2"},
			wantMovement: "slack.rss.government<-slack.rss.government.2, slack.rss.government.2<-slack.rss.government",
		},
		{
			name:     "l a whitespace only entry removed",
			previous: map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			current:  map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			wantKind: "",
		},
		{
			name:     "l2 a blank inserted in the middle",
			previous: map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			current:  map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			wantKind: "",
		},
		{
			name:     "l3 same url padded with spaces",
			previous: map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			current:  map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			wantKind: "",
		},
		{
			name:         "l4 the first url blanked",
			previous:     map[string]string{"discord.rss.general": hashA, "discord.rss.general.2": hashB},
			current:      map[string]string{"discord.rss.general": hashB},
			wantHasRemap: true,
			wantKind:     "delete",
			wantRemapped: []string{"discord.rss.general"},
			wantRemoved:  []string{"discord.rss.general.2"},
			wantMovement: "discord.rss.general<-discord.rss.general.2",
		},
		{
			name:         "m slack_compatible government endpoints swapped",
			previous:     map[string]string{"slack_compatible.rss.government": hashA, "slack_compatible.rss.government.2": hashB},
			current:      map[string]string{"slack_compatible.rss.government": hashB, "slack_compatible.rss.government.2": hashA},
			wantHasRemap: true,
			wantKind:     "reorder",
			wantRemapped: []string{"slack_compatible.rss.government", "slack_compatible.rss.government.2"},
			wantMovement: "slack_compatible.rss.government<-slack_compatible.rss.government.2, " +
				"slack_compatible.rss.government.2<-slack_compatible.rss.government",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := DiffDestinations(tt.previous, tt.current)

			if got := diff.HasRemap(); got != tt.wantHasRemap {
				t.Fatalf("HasRemap() = %v, want %v (diff=%+v)", got, tt.wantHasRemap, diff)
			}
			if got := diff.Kind(); got != tt.wantKind {
				t.Fatalf("Kind() = %q, want %q (diff=%+v)", got, tt.wantKind, diff)
			}
			if got := diff.RemappedIDs(); !equalStringSlices(got, tt.wantRemapped) {
				t.Fatalf("RemappedIDs() = %v, want %v", got, tt.wantRemapped)
			}
			if !equalStringSlices(diff.Added, tt.wantAdded) {
				t.Fatalf("Added = %v, want %v", diff.Added, tt.wantAdded)
			}
			if !equalStringSlices(diff.Removed, tt.wantRemoved) {
				t.Fatalf("Removed = %v, want %v", diff.Removed, tt.wantRemoved)
			}
			if got := diff.Movements(); got != tt.wantMovement {
				t.Fatalf("Movements() = %q, want %q", got, tt.wantMovement)
			}
		})
	}
}

// TestDiffDestinationsKeepsPreviousOwnersInsideOneNamespace pins finding 1 of the
// plan: without destinationNamespace the API family reports the RSS family as a
// previous owner, because a ransomware block feeds both from one URL.
func TestDiffDestinationsKeepsPreviousOwnersInsideOneNamespace(t *testing.T) {
	previous := map[string]string{
		"discord.ransomware":       hashA,
		"discord.ransomware.2":     hashB,
		"discord.rss.ransomware":   hashA,
		"discord.rss.ransomware.2": hashB,
	}
	current := map[string]string{
		"discord.ransomware":       hashB,
		"discord.ransomware.2":     hashA,
		"discord.rss.ransomware":   hashB,
		"discord.rss.ransomware.2": hashA,
	}

	diff := DiffDestinations(previous, current)
	owners := map[string][]string{}
	for _, remap := range diff.Remapped {
		owners[remap.ID] = remap.PreviousIDs
	}

	want := map[string][]string{
		"discord.ransomware":       {"discord.ransomware.2"},
		"discord.ransomware.2":     {"discord.ransomware"},
		"discord.rss.ransomware":   {"discord.rss.ransomware.2"},
		"discord.rss.ransomware.2": {"discord.rss.ransomware"},
	}
	if !reflect.DeepEqual(owners, want) {
		t.Fatalf("previous owners = %v, want %v", owners, want)
	}
}

func TestDiffDestinationsEmptyInputs(t *testing.T) {
	empty := map[string]string{}
	tests := []struct {
		name              string
		previous, current map[string]string
	}{
		{name: "nil and nil"},
		{name: "nil and empty", current: empty},
		{name: "empty and nil", previous: empty},
		{name: "empty and empty", previous: empty, current: empty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := DiffDestinations(tt.previous, tt.current)
			if diff.HasRemap() {
				t.Fatalf("HasRemap() = true, want false")
			}
			if got := diff.Kind(); got != "" {
				t.Fatalf("Kind() = %q, want empty", got)
			}
			if len(diff.Remapped) != 0 || len(diff.Added) != 0 || len(diff.Removed) != 0 {
				t.Fatalf("diff = %+v, want zero valued", diff)
			}
			if got := diff.Movements(); got != "" {
				t.Fatalf("Movements() = %q, want empty", got)
			}
			if got := diff.RemappedIDs(); len(got) != 0 {
				t.Fatalf("RemappedIDs() = %v, want empty", got)
			}
		})
	}
}

func TestDestinationManifestRoundTrip(t *testing.T) {
	const (
		urlA = "https://discord.com/api/webhooks/111/tokenAAA"
		urlB = "https://hooks.slack.com/services/T1/B1/tokenBBB"
		urlC = "https://discord.com/api/webhooks/333/tokenCCC"
	)
	dataDir := t.TempDir()
	hashes := map[string]string{
		"discord.ransomware":     sha256HexForTest(urlA),
		"slack.rss.general":      sha256HexForTest(urlB),
		"discord.rss.government": sha256HexForTest(urlC),
	}

	if err := WriteDestinationManifest(dataDir, hashes); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}

	manifest, found, err := LoadDestinationManifest(dataDir)
	if err != nil {
		t.Fatalf("LoadDestinationManifest() error = %v", err)
	}
	if !found {
		t.Fatal("LoadDestinationManifest() found = false, want true")
	}
	if manifest.Version != DestinationManifestVersion {
		t.Fatalf("Version = %d, want %d", manifest.Version, DestinationManifestVersion)
	}
	if !reflect.DeepEqual(manifest.Destinations, hashes) {
		t.Fatalf("Destinations = %v, want %v", manifest.Destinations, hashes)
	}
	if _, err := time.Parse(timeutil.LayoutDateTime, manifest.UpdatedAt); err != nil {
		t.Fatalf("UpdatedAt %q does not parse with %q: %v", manifest.UpdatedAt, timeutil.LayoutDateTime, err)
	}

	path := DestinationManifestPath(dataDir)
	raw, err := os.ReadFile(path) //nolint:gosec // test reads the file it just wrote
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	for _, webhookURL := range []string{urlA, urlB, urlC} {
		if strings.Contains(string(raw), webhookURL) {
			t.Fatalf("manifest bytes contain the webhook URL %q", webhookURL)
		}
	}
	for _, token := range []string{"tokenAAA", "tokenBBB", "tokenCCC", "discord.com", "hooks.slack.com"} {
		if strings.Contains(string(raw), token) {
			t.Fatalf("manifest bytes contain %q", token)
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != statusFileMode {
		t.Fatalf("manifest mode = %v, want %v", got, os.FileMode(statusFileMode))
	}
}

func TestLoadDestinationManifestMissingFileIsNotAnError(t *testing.T) {
	tests := []struct {
		name    string
		dataDir string
	}{
		{name: "missing file", dataDir: t.TempDir()},
		{name: "empty data dir", dataDir: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, found, err := LoadDestinationManifest(tt.dataDir)
			if err != nil {
				t.Fatalf("LoadDestinationManifest() error = %v, want nil", err)
			}
			if found {
				t.Fatal("LoadDestinationManifest() found = true, want false")
			}
			if len(manifest.Destinations) != 0 {
				t.Fatalf("Destinations = %v, want empty", manifest.Destinations)
			}
		})
	}
}

func TestLoadDestinationManifestRejectsCorruptJSONAndUnknownVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantErr     string
		wantVersion int
	}{
		{name: "truncated json", content: "{", wantErr: "parse destination manifest"},
		{
			name:        "unknown version",
			content:     `{"version":99,"updated_at":"2026-09-03 07:49:01","destinations":{"discord.ransomware":"abc"}}`,
			wantErr:     "unsupported destination manifest version 99",
			wantVersion: 99,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(DestinationManifestPath(dataDir), []byte(tt.content), 0600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			manifest, found, err := LoadDestinationManifest(dataDir)
			if err == nil {
				t.Fatal("LoadDestinationManifest() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if found {
				t.Fatal("LoadDestinationManifest() found = true, want false")
			}
			if manifest.Version != tt.wantVersion {
				t.Fatalf("Version = %d, want %d so the WARN can name it", manifest.Version, tt.wantVersion)
			}
		})
	}
}

func TestWriteDestinationManifestSurfacesWriteFailure(t *testing.T) {
	original := writeAtomicJSONFileFunc
	defer func() { writeAtomicJSONFileFunc = original }()
	wantErr := errors.New("disk full")
	writeAtomicJSONFileFunc = func(string, string, interface{}) error { return wantErr }

	err := WriteDestinationManifest(t.TempDir(), map[string]string{"discord.ransomware": hashA})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteDestinationManifest() error = %v, want %v", err, wantErr)
	}
}

func TestWriteDestinationManifestCreatesDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")

	if err := WriteDestinationManifest(dataDir, map[string]string{"discord.ransomware": hashA}); err != nil {
		t.Fatalf("WriteDestinationManifest() error = %v", err)
	}
	if _, found, err := LoadDestinationManifest(dataDir); err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v, want found true", found, err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != os.FileMode(privateDataDirMode) {
		t.Fatalf("data dir mode = %v, want %v", got, os.FileMode(privateDataDirMode))
	}
}

func TestWriteDestinationManifestRejectsEmptyDataDir(t *testing.T) {
	if err := WriteDestinationManifest("", map[string]string{"discord.ransomware": hashA}); err == nil {
		t.Fatal("WriteDestinationManifest(\"\") error = nil, want an error")
	}
}

func TestOrphanedDestinationRowsCountsUnknownDestinationIDs(t *testing.T) {
	tracker := NewMemoryTracker()
	if !tracker.EnqueueRetryForDestination(RetryRequest{
		ItemKey:       "api-item",
		DestinationID: "discord.ransomware.3",
		Messenger:     MessengerDiscord,
		ItemType:      RetryItemTypeAPI,
		Title:         "api",
		LastError:     "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination(api) = false, want true")
	}
	if !tracker.EnqueueRetryForDestination(RetryRequest{
		ItemKey:       "rss-item",
		DestinationID: "discord.ransomware.3",
		Messenger:     MessengerDiscord,
		ItemType:      RetryItemTypeRSS,
		Title:         "rss",
		LastError:     "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination(rss) = false, want true")
	}
	// A pre-suffix legacy row carries no destination ID and must never count.
	if !tracker.EnqueueRetryForDestination(RetryRequest{
		ItemKey:   "legacy-item",
		Messenger: MessengerDiscord,
		ItemType:  RetryItemTypeAPI,
		Title:     "legacy",
		LastError: "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination(legacy) = false, want true")
	}
	tracker.MarkRetryDeadLetterForDestination(
		"dead-item", "discord.ransomware.3",
		MessengerDiscord.String(), RetryItemTypeAPI.String(), "dead", "boom",
	)
	tracker.MarkRetryDeadLetterForDestination(
		"dead-legacy", "",
		MessengerDiscord.String(), RetryItemTypeAPI.String(), "dead legacy", "boom",
	)

	unknown := map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB}
	retryRows, deadLetterRows := tracker.OrphanedDestinationRows(unknown)
	if retryRows != 2 || deadLetterRows != 1 {
		t.Fatalf("OrphanedDestinationRows(unknown) = (%d, %d), want (2, 1)", retryRows, deadLetterRows)
	}

	known := map[string]string{
		"discord.ransomware":   hashA,
		"discord.ransomware.2": hashB,
		"discord.ransomware.3": hashC,
	}
	retryRows, deadLetterRows = tracker.OrphanedDestinationRows(known)
	if retryRows != 0 || deadLetterRows != 0 {
		t.Fatalf("OrphanedDestinationRows(known) = (%d, %d), want (0, 0)", retryRows, deadLetterRows)
	}
}

func TestDestinationRemapLogFieldsNamesTheChangeWithoutURLs(t *testing.T) {
	diff := DiffDestinations(
		map[string]string{"discord.ransomware": hashA, "discord.ransomware.2": hashB},
		map[string]string{"discord.ransomware": hashB, "discord.ransomware.2": hashA},
	)
	fields := DestinationRemapLogFields(diff, "/app/data/destinations.json")

	for _, key := range []string{
		"change", "affected_destination_ids", "movements", "new_destination_ids",
		"removed_destination_ids", "manifest", "consequence", "remedy",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("fields = %v, want key %q", fields, key)
		}
	}
	if got := fields["change"]; got != "reorder" {
		t.Fatalf("change = %v, want reorder", got)
	}
	if got := fields["affected_destination_ids"]; got != "discord.ransomware, discord.ransomware.2" {
		t.Fatalf("affected_destination_ids = %v", got)
	}
	if got := fields["movements"]; got != "discord.ransomware<-discord.ransomware.2, discord.ransomware.2<-discord.ransomware" {
		t.Fatalf("movements = %v", got)
	}
	if got := fields["manifest"]; got != "/app/data/destinations.json" {
		t.Fatalf("manifest = %v", got)
	}
	if got := fields["new_destination_ids"]; got != "" {
		t.Fatalf("new_destination_ids = %v, want empty", got)
	}
	if got := fields["removed_destination_ids"]; got != "" {
		t.Fatalf("removed_destination_ids = %v, want empty", got)
	}
}

func TestDestinationManifestUnreadableFieldsNamesTheReason(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(DestinationManifestPath(dataDir),
		[]byte(`{"version":99,"destinations":{}}`), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	manifest, _, err := LoadDestinationManifest(dataDir)
	if err == nil {
		t.Fatal("LoadDestinationManifest() error = nil, want an error")
	}

	fields := DestinationManifestUnreadableFields(dataDir, manifest, err)
	if got := fields["manifest"]; got != DestinationManifestPath(dataDir) {
		t.Fatalf("manifest = %v", got)
	}
	if got, ok := fields["manifest_version"]; !ok || got != 99 {
		t.Fatalf("manifest_version = %v (ok=%v), want 99", got, ok)
	}
	reason, _ := fields["reason"].(string)
	if !strings.Contains(reason, "unsupported destination manifest version 99") {
		t.Fatalf("reason = %q, want the load error", reason)
	}
	consequence, _ := fields["consequence"].(string)
	if !strings.Contains(consequence, "endpoint-order check is skipped") {
		t.Fatalf("consequence = %q, want the skipped-check sentence", consequence)
	}

	// A parse failure carries no version, so the field must be absent.
	if err := os.WriteFile(DestinationManifestPath(dataDir), []byte("{"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	manifest, _, err = LoadDestinationManifest(dataDir)
	if err == nil {
		t.Fatal("LoadDestinationManifest() error = nil, want an error")
	}
	if _, ok := DestinationManifestUnreadableFields(dataDir, manifest, err)["manifest_version"]; ok {
		t.Fatal("manifest_version present for an unparseable manifest, want it absent")
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// sha256HexForTest mirrors config.DestinationURLHash; internal/config imports
// internal/status, so the real helper cannot be called from here.
func sha256HexForTest(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func TestLoadDestinationManifestSurfacesUnreadableFile(t *testing.T) {
	dataDir := t.TempDir()
	// A directory in the manifest's place: os.ReadFile fails with something
	// other than "not exists", which callers must not mistake for a first run.
	if err := os.Mkdir(DestinationManifestPath(dataDir), 0700); err != nil {
		t.Fatalf("Mkdir(manifest path) error = %v", err)
	}

	_, found, err := LoadDestinationManifest(dataDir)
	if err == nil {
		t.Fatal("LoadDestinationManifest() error = nil, want a read failure")
	}
	if !strings.Contains(err.Error(), "read destination manifest") {
		t.Fatalf("error = %v, want it to name the read failure", err)
	}
	if found {
		t.Fatal("LoadDestinationManifest() found = true, want false")
	}
}

func TestLoadDestinationManifestNormalisesMissingDestinationsMap(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(DestinationManifestPath(dataDir),
		[]byte(`{"version":1,"updated_at":"2026-09-03 07:49:01"}`), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manifest, found, err := LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("LoadDestinationManifest() = found %v, err %v, want found true", found, err)
	}
	if manifest.Destinations == nil {
		t.Fatal("Destinations = nil, want an empty map so callers can diff it")
	}
	if len(manifest.Destinations) != 0 {
		t.Fatalf("Destinations = %v, want empty", manifest.Destinations)
	}
}

func TestWriteDestinationManifestSurfacesDataDirFailure(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data-is-a-file")
	if err := os.WriteFile(dataDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataDir) error = %v", err)
	}

	err := WriteDestinationManifest(dataDir, map[string]string{"discord.ransomware": hashA})
	if err == nil {
		t.Fatal("WriteDestinationManifest() error = nil, want the data dir failure")
	}
	if !strings.Contains(err.Error(), "write destination manifest") {
		t.Fatalf("error = %v, want it to name the manifest write", err)
	}
}

func TestDestinationNamespaceStripsOnlyPositionalSuffixes(t *testing.T) {
	tests := []struct{ id, want string }{
		{id: "discord.ransomware", want: "discord.ransomware"},
		{id: "discord.ransomware.2", want: "discord.ransomware"},
		{id: "discord.rss.general.2", want: "discord.rss.general"},
		{id: "discord.rss.general", want: "discord.rss.general"},
		{id: "slack_compatible.rss.government.10", want: "slack_compatible.rss.government"},
		// No dot at all, and a trailing dot: both stay as they are.
		{id: "discord", want: "discord"},
		{id: "discord.", want: "discord."},
		{id: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := destinationNamespace(tt.id); got != tt.want {
				t.Fatalf("destinationNamespace(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestNewReadOnlyTrackerLazyAPIDefersTheAPIStatusLoad(t *testing.T) {
	dataDir := t.TempDir()
	writer, err := NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	writer.UpdateAPIStatus(true, 3, "")
	if !writer.EnqueueRetryForDestination(RetryRequest{
		ItemKey:       "queued",
		DestinationID: "discord.ransomware",
		Messenger:     MessengerDiscord,
		ItemType:      RetryItemTypeAPI,
		Title:         "queued",
		LastError:     "boom",
	}) {
		t.Fatal("EnqueueRetryForDestination() = false, want true")
	}
	if err := writer.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	tracker, err := NewReadOnlyTrackerLazyAPI(dataDir)
	if err != nil {
		t.Fatalf("NewReadOnlyTrackerLazyAPI() error = %v", err)
	}
	if tracker.apiLoaded {
		t.Fatal("apiLoaded = true, want the API status load deferred")
	}
	// The retry store is loaded, which is what the orphan count needs.
	if retryRows, _ := tracker.OrphanedDestinationRows(nil); retryRows != 1 {
		t.Fatalf("OrphanedDestinationRows retry rows = %d, want 1", retryRows)
	}
}

// TestDiffDestinationsOutputIsSortedAndDeterministic pins invariant 8: the diff
// is rendered straight into operator-facing log fields, so its order must not
// depend on Go's randomised map iteration. Removed is the only slice built by
// ranging over a map, and every scenario in the table above removes at most one
// ID, so the ordering is exercised here with several.
func TestDiffDestinationsOutputIsSortedAndDeterministic(t *testing.T) {
	previous := map[string]string{
		"discord.ransomware":       hashA,
		"discord.ransomware.2":     hashB,
		"discord.ransomware.3":     hashC,
		"discord.rss.general":      hashA,
		"discord.rss.general.2":    hashB,
		"slack.rss.government":     hashA,
		"slack.rss.government.2":   hashB,
		"slack_compatible.rss.gen": hashC,
	}
	current := map[string]string{"discord.ransomware": hashA}

	wantRemoved := []string{
		"discord.ransomware.2",
		"discord.ransomware.3",
		"discord.rss.general",
		"discord.rss.general.2",
		"slack.rss.government",
		"slack.rss.government.2",
		"slack_compatible.rss.gen",
	}
	// Repeated so a map-iteration order that happens to be sorted once cannot
	// pass by luck.
	for i := 0; i < 50; i++ {
		diff := DiffDestinations(previous, current)
		if !equalStringSlices(diff.Removed, wantRemoved) {
			t.Fatalf("Removed = %v, want %v (run %d)", diff.Removed, wantRemoved, i)
		}
		fields := DestinationRemapLogFields(diff, "/data/destinations.json")
		got, _ := fields["removed_destination_ids"].(string)
		want := strings.Join(wantRemoved, ", ")
		if got != want {
			t.Fatalf("removed_destination_ids = %q, want %q (run %d)", got, want, i)
		}
	}

	// Added is built from a sorted key list, so it is pinned the same way.
	grown := map[string]string{
		"discord.ransomware":    hashA,
		"discord.ransomware.2":  hashB,
		"discord.rss.general":   hashA,
		"discord.rss.general.2": hashB,
		"slack.rss.government":  hashC,
	}
	wantAdded := []string{
		"discord.ransomware.2",
		"discord.rss.general",
		"discord.rss.general.2",
		"slack.rss.government",
	}
	for i := 0; i < 50; i++ {
		diff := DiffDestinations(map[string]string{"discord.ransomware": hashA}, grown)
		if !equalStringSlices(diff.Added, wantAdded) {
			t.Fatalf("Added = %v, want %v (run %d)", diff.Added, wantAdded, i)
		}
	}
}
