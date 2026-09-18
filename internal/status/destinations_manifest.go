package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

// DestinationManifestVersion is the on-disk schema version of destinations.json.
// A manifest carrying any other version is treated as absent and rewritten.
const DestinationManifestVersion = 1

const destinationManifestFileName = "destinations.json"

// Destination change kinds reported by DestinationDiff.Kind. They are a
// headline only: a single edit can reorder, insert and rotate at once, and the
// precedence below labels it by the first matching branch. The movement list
// returned by Movements is the authoritative description.
const (
	DestinationChangeReorder    = "reorder"
	DestinationChangeInsert     = "insert"
	DestinationChangeDelete     = "delete"
	DestinationChangeURLChanged = "url_changed"
)

const destinationRemapConsequence = "stored dedup markers, retry-queue rows and dead letters are keyed by the " +
	"destination ID; alerts for one endpoint would be suppressed and another endpoint's queued deliveries " +
	"replayed to the wrong webhook"

const destinationRemapRemedy = "restore the previous endpoint order (append new endpoints at the end of " +
	"url/urls/targets, delete only from the end), or start once with --accept-destination-remap to accept " +
	"the new mapping"

const destinationManifestSkippedCheck = "the endpoint-order check is skipped for this start"

// DestinationManifest binds every positional destination ID to a hash of the
// webhook URL it was last used with. It never stores a URL, a host or a token.
type DestinationManifest struct {
	Version      int               `json:"version"`
	UpdatedAt    string            `json:"updated_at"`
	Destinations map[string]string `json:"destinations"`
}

// DestinationManifestPath returns the manifest path inside dataDir, or an empty
// string when no data directory is configured.
func DestinationManifestPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, destinationManifestFileName)
}

// LoadDestinationManifest reads destinations.json. A missing file and an empty
// data directory both return (zero, false, nil); an unreadable, unparseable or
// unknown-version file returns an error, and callers treat that as absent after
// warning. On a version mismatch the returned manifest still carries the parsed
// version so the warning can name it.
func LoadDestinationManifest(dataDir string) (DestinationManifest, bool, error) {
	path := DestinationManifestPath(dataDir)
	if path == "" {
		return DestinationManifest{}, false, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the operator-configured data dir
	if err != nil {
		if os.IsNotExist(err) {
			return DestinationManifest{}, false, nil
		}
		return DestinationManifest{}, false, fmt.Errorf("read destination manifest %s: %w", path, err)
	}

	var manifest DestinationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return DestinationManifest{}, false, fmt.Errorf("parse destination manifest: %w", err)
	}
	if manifest.Version != DestinationManifestVersion {
		return DestinationManifest{
			Version: manifest.Version,
		}, false, fmt.Errorf(
			"unsupported destination manifest version %d", manifest.Version)
	}
	if manifest.Destinations == nil {
		manifest.Destinations = map[string]string{}
	}
	return manifest, true, nil
}

// WriteDestinationManifest writes destinations.json atomically with the same
// 0600 mode as every other status file, creating the data directory first
// because the atomic writer does not.
func WriteDestinationManifest(dataDir string, hashes map[string]string) error {
	path := DestinationManifestPath(dataDir)
	if path == "" {
		return fmt.Errorf("write destination manifest: no data directory configured")
	}
	if err := ensurePrivateDataDir(dataDir); err != nil {
		return fmt.Errorf("write destination manifest: %w", err)
	}

	destinations := make(map[string]string, len(hashes))
	for id, hash := range hashes {
		destinations[id] = hash
	}
	manifest := DestinationManifest{
		Version:      DestinationManifestVersion,
		UpdatedAt:    time.Now().UTC().Format(statusTimestampLayout),
		Destinations: destinations,
	}
	return writeAtomicJSONFileFunc(path, "destination manifest", manifest)
}

// DestinationRemap is one destination ID whose webhook URL changed, together
// with the manifest IDs in the same namespace that previously carried that URL.
type DestinationRemap struct {
	ID          string
	PreviousIDs []string
}

// DestinationDiff compares the manifest against the current configuration.
// Added and Removed IDs are always safe; a non-empty Remapped means stored
// state would be applied to a different webhook.
type DestinationDiff struct {
	Remapped []DestinationRemap
	Added    []string
	Removed  []string
}

// destinationNamespace strips a trailing all-digit positional suffix, so the
// previous-owner search stays inside one webhook block and delivery path. A
// ransomware block owns two families ("<m>.ransomware" and "<m>.rss.ransomware")
// that share a URL; without this they would report each other as owners.
func destinationNamespace(id string) string {
	index := strings.LastIndex(id, ".")
	if index < 0 || index == len(id)-1 {
		return id
	}
	for _, r := range id[index+1:] {
		if r < '0' || r > '9' {
			return id
		}
	}
	return id[:index]
}

// DiffDestinations compares the manifest hashes against the current ones.
// Both outputs are sorted by destination ID, so every rendering is stable.
func DiffDestinations(previous, current map[string]string) DestinationDiff {
	currentIDs := make([]string, 0, len(current))
	for id := range current {
		currentIDs = append(currentIDs, id)
	}
	sort.Strings(currentIDs)

	var diff DestinationDiff
	for _, id := range currentIDs {
		previousHash, known := previous[id]
		if !known {
			diff.Added = append(diff.Added, id)
			continue
		}
		if previousHash == current[id] {
			continue
		}
		diff.Remapped = append(diff.Remapped, DestinationRemap{
			ID:          id,
			PreviousIDs: previousOwnersOf(previous, id, current[id]),
		})
	}

	for id := range previous {
		if _, kept := current[id]; !kept {
			diff.Removed = append(diff.Removed, id)
		}
	}
	sort.Strings(diff.Removed)
	return diff
}

// previousOwnersOf returns the manifest IDs in id's namespace that carried hash.
func previousOwnersOf(previous map[string]string, id, hash string) []string {
	namespace := destinationNamespace(id)
	var owners []string
	for previousID, previousHash := range previous {
		if previousHash != hash || previousID == id {
			continue
		}
		if destinationNamespace(previousID) != namespace {
			continue
		}
		owners = append(owners, previousID)
	}
	sort.Strings(owners)
	return owners
}

// HasRemap reports whether an existing destination ID now points at a different
// webhook URL. This is the only condition that refuses a start or a reload.
func (d DestinationDiff) HasRemap() bool {
	return len(d.Remapped) > 0
}

// RemappedIDs lists the affected destination IDs in sorted order.
func (d DestinationDiff) RemappedIDs() []string {
	if len(d.Remapped) == 0 {
		return nil
	}
	ids := make([]string, 0, len(d.Remapped))
	for _, remap := range d.Remapped {
		ids = append(ids, remap.ID)
	}
	return ids
}

// Kind is a headline for the remap, never a classification: precedence is
// delete > insert > reorder > url_changed, so a combined edit is labelled by
// the first matching branch. It is empty when nothing was remapped, which is
// the append-only and delete-from-the-end case.
func (d DestinationDiff) Kind() string {
	if !d.HasRemap() {
		return ""
	}
	moved := false
	for _, remap := range d.Remapped {
		if len(remap.PreviousIDs) > 0 {
			moved = true
			break
		}
	}
	switch {
	case moved && len(d.Removed) > 0:
		return DestinationChangeDelete
	case moved && len(d.Added) > 0:
		return DestinationChangeInsert
	case moved:
		return DestinationChangeReorder
	default:
		return DestinationChangeURLChanged
	}
}

// Movements renders the remap as "X<-Y, Z<-*": the endpoint now stored under X
// was previously stored under Y, and "*" is a URL the manifest does not know at
// all (a rotated token, or an inserted endpoint). Several previous owners are
// joined with "+", which a shared webhook URL makes reachable. Destination IDs
// only, so the result is safe to embed in a localized operator sentence.
func (d DestinationDiff) Movements() string {
	if len(d.Remapped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(d.Remapped))
	for _, remap := range d.Remapped {
		if len(remap.PreviousIDs) == 0 {
			parts = append(parts, remap.ID+"<-*")
			continue
		}
		parts = append(parts, remap.ID+"<-"+strings.Join(remap.PreviousIDs, "+"))
	}
	return strings.Join(parts, ", ")
}

// DestinationRemapLogFields builds the structured log entry shared by the
// startup refusal, the accepted-remap warning and the reload rejection. It
// carries destination IDs and hashes only, never a URL, host or token.
func DestinationRemapLogFields(diff DestinationDiff, manifestPath string) log.Fields {
	return log.Fields{
		"change":                   diff.Kind(),
		"affected_destination_ids": strings.Join(diff.RemappedIDs(), ", "),
		"movements":                diff.Movements(),
		"new_destination_ids":      strings.Join(diff.Added, ", "),
		"removed_destination_ids":  strings.Join(diff.Removed, ", "),
		"manifest":                 manifestPath,
		"consequence":              destinationRemapConsequence,
		"remedy":                   destinationRemapRemedy,
	}
}

// DestinationManifestUnreadableFields describes a manifest that could not be
// used, so the WARN says which file, why, which version it claimed when that
// was parseable, and what the operator loses for this start.
func DestinationManifestUnreadableFields(dataDir string, manifest DestinationManifest, err error) log.Fields {
	fields := log.Fields{
		"manifest":    DestinationManifestPath(dataDir),
		"consequence": destinationManifestSkippedCheck,
	}
	if err != nil {
		fields["reason"] = err.Error()
	}
	if manifest.Version != 0 {
		fields["manifest_version"] = manifest.Version
	}
	return fields
}

// OrphanedDestinationRows counts queued retries and dead letters whose
// destination ID is no longer configured. Rows without a destination ID are
// pre-suffix legacy rows and are never counted.
func (t *Tracker) OrphanedDestinationRows(known map[string]string) (int, int) {
	retryRows := 0
	for _, itemType := range []string{RetryItemTypeAPI.String(), RetryItemTypeRSS.String()} {
		for _, record := range t.GetQueuedRetryItemsByType(itemType) {
			if isOrphanedDestinationID(record.Item.DestinationID, known) {
				retryRows++
			}
		}
	}

	deadLetterRows := 0
	for _, item := range t.GetDeadLetterItems() {
		if isOrphanedDestinationID(item.DestinationID, known) {
			deadLetterRows++
		}
	}
	return retryRows, deadLetterRows
}

func isOrphanedDestinationID(destinationID string, known map[string]string) bool {
	destinationID = strings.TrimSpace(destinationID)
	if destinationID == "" {
		return false
	}
	_, configured := known[destinationID]
	return !configured
}
