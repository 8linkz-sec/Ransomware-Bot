package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

// DestinationManifestVersion is the on-disk schema version of destinations.json.
// Version 1 is upgraded in memory; other versions are treated as unreadable.
const DestinationManifestVersion = 2

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
	DestinationChangeUnknown    = "previous_binding_unknown"
)

const destinationRemapConsequence = "stored dedup markers, retry-queue rows and dead letters are keyed by the " +
	"destination ID; alerts for one endpoint could be suppressed and another endpoint's queued deliveries " +
	"replayed to the wrong webhook"

const destinationRemapRemedy = "restore the previous endpoint order (append new endpoints at the end of " +
	"url/urls/targets, delete only from the end), or start once with --accept-destination-remap to accept " +
	"the new mapping"

const destinationManifestSkippedCheck = "old endpoint bindings are unknown; new destination IDs require explicit acceptance"

// DestinationManifest binds active and removed positional IDs to webhook URL
// hashes. UnknownIDs and Unattributed preserve uncertainty from version 1.
// It never stores a URL, a host or a token.
type DestinationManifest struct {
	Version      int               `json:"version"`
	UpdatedAt    string            `json:"updated_at"`
	Destinations map[string]string `json:"destinations"`
	Retired      map[string]string `json:"retired,omitempty"`
	UnknownIDs   []string          `json:"unknown_ids,omitempty"`
	Unattributed bool              `json:"unattributed,omitempty"`
}

// DestinationManifestPath returns the manifest path inside dataDir, or an empty
// string when no data directory is configured.
func DestinationManifestPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, destinationManifestFileName)
}

// LoadDestinationManifest reads destinations.json. A missing file with no status
// history and an empty data directory return (zero, false, nil). A missing file
// with status history returns an uncertain in-memory manifest. Unreadable or
// unsupported files return an uncertain manifest with an error. Both supported
// versions are reconciled against persisted status rows.
func LoadDestinationManifest(dataDir string) (DestinationManifest, bool, error) {
	path := DestinationManifestPath(dataDir)
	if path == "" {
		return DestinationManifest{}, false, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the operator-configured data dir
	if err != nil {
		if os.IsNotExist(err) {
			if _, dirErr := os.Stat(dataDir); os.IsNotExist(dirErr) {
				return DestinationManifest{}, false, nil
			}
			if !hasPersistedStatusFiles(dataDir) {
				return DestinationManifest{}, false, nil
			}
			missing := reconcilePersistedDestinationHistory(dataDir, DestinationManifest{
				Version: DestinationManifestVersion, Destinations: map[string]string{},
			})
			missing.Unattributed = true // absent manifest cannot prove pruned old bindings
			return missing, true, nil
		}
		return uncertainDestinationManifest(), false, fmt.Errorf("read destination manifest %s: %w", path, err)
	}

	var manifest DestinationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return uncertainDestinationManifest(), false, fmt.Errorf("parse destination manifest: %w", err)
	}
	if manifest.Version != 1 && manifest.Version != DestinationManifestVersion {
		uncertain := uncertainDestinationManifest()
		uncertain.Version = manifest.Version
		return uncertain, false, fmt.Errorf(
			"unsupported destination manifest version %d", manifest.Version)
	}
	if manifest.Destinations == nil {
		manifest.Destinations = map[string]string{}
	}
	manifest = reconcilePersistedDestinationHistory(dataDir, manifest)
	return manifest, true, nil
}

func hasPersistedStatusFiles(dataDir string) bool {
	for _, name := range []string{statusAPIFileName, statusRSSFileName, statusRetryFileName} {
		_, err := os.Stat(filepath.Join(dataDir, name))
		if err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func uncertainDestinationManifest() DestinationManifest {
	return DestinationManifest{Destinations: map[string]string{}, Unattributed: true}
}

// WriteDestinationManifest advances destinations.json atomically with the same
// 0600 mode as every other status file. It reloads the previous manifest so
// removed-ID bindings survive successful loads and reloads.
func WriteDestinationManifest(dataDir string, hashes map[string]string) error {
	path := DestinationManifestPath(dataDir)
	if path == "" {
		return fmt.Errorf("write destination manifest: no data directory configured")
	}
	if err := ensurePrivateDataDir(dataDir); err != nil {
		return fmt.Errorf("write destination manifest: %w", err)
	}

	previous, found, _ := LoadDestinationManifest(dataDir)
	if !found && !previous.Unattributed {
		previous = DestinationManifest{}
	}
	manifest := NextDestinationManifest(previous, hashes)
	return writeAtomicJSONFileFunc(path, "destination manifest", manifest)
}

// NextDestinationManifest advances a successfully checked or explicitly
// accepted mapping. Removed IDs retain their last hash until that ID returns.
func NextDestinationManifest(previous DestinationManifest, current map[string]string) DestinationManifest {
	next := DestinationManifest{
		Version:      DestinationManifestVersion,
		UpdatedAt:    time.Now().UTC().Format(statusTimestampLayout),
		Destinations: make(map[string]string, len(current)),
		Retired:      make(map[string]string, len(previous.Retired)+len(previous.Destinations)),
		Unattributed: previous.Unattributed,
	}
	for id, hash := range previous.Retired {
		next.Retired[id] = hash
	}
	for id, hash := range previous.Destinations {
		if _, present := current[id]; !present {
			next.Retired[id] = hash
		}
	}
	for id, hash := range current {
		next.Destinations[id] = hash
		delete(next.Retired, id)
	}
	for _, id := range previous.UnknownIDs {
		if _, present := current[id]; !present {
			next.UnknownIDs = append(next.UnknownIDs, id)
		}
	}
	sort.Strings(next.UnknownIDs)
	return next
}

// DiffDestinationManifest compares current endpoints against active and retired
// bindings. A v1 ID whose previous hash was lost is conservatively treated as
// a remap when it returns. Unattributed legacy markers make every new ID unsafe.
func DiffDestinationManifest(manifest DestinationManifest, current map[string]string) DestinationDiff {
	known := make(map[string]string, len(manifest.Destinations)+len(manifest.Retired)+len(manifest.UnknownIDs))
	for id, hash := range manifest.Retired {
		known[id] = hash
	}
	for _, id := range manifest.UnknownIDs {
		known[id] = ""
	}
	for id, hash := range manifest.Destinations {
		known[id] = hash
	}
	diff := DiffDestinations(known, current)
	for _, remap := range diff.Remapped {
		if known[remap.ID] == "" {
			diff.Unknown = true
			break
		}
	}
	if manifest.Unattributed {
		for _, id := range diff.Added {
			diff.Remapped = append(diff.Remapped, DestinationRemap{ID: id})
			diff.Unknown = true
		}
	}
	sort.Slice(diff.Remapped, func(i, j int) bool { return diff.Remapped[i].ID < diff.Remapped[j].ID })
	// Retired and unknown IDs are historical bindings, not IDs removed by this
	// configuration change. Keep the operator's removed-ID log meaningful.
	diff.Removed = diff.Removed[:0]
	for id := range manifest.Destinations {
		if _, present := current[id]; !present {
			diff.Removed = append(diff.Removed, id)
		}
	}
	sort.Strings(diff.Removed)
	return diff
}

// reconcilePersistedDestinationHistory recovers IDs visible in status state
// that a v1 manifest forgot or a v2 manifest missed after a failed write. A
// blank sent-marker owner cannot be reversed from its hashed composite key, so
// future new IDs remain uncertain instead.
func reconcilePersistedDestinationHistory(dataDir string, manifest DestinationManifest) DestinationManifest {
	tracker, err := NewReadOnlyTracker(dataDir)
	if err != nil {
		manifest.Unattributed = true
		log.WithError(err).WithField("manifest", DestinationManifestPath(dataDir)).
			Warn("Destination manifest could not inspect status history; new destination IDs require explicit acceptance")
		return manifest
	}
	ids, unattributed := tracker.persistedDestinationIDs()
	manifest.Unattributed = manifest.Unattributed || unattributed
	known := make(map[string]struct{}, len(manifest.Destinations)+len(manifest.Retired)+len(manifest.UnknownIDs))
	for id := range manifest.Destinations {
		known[id] = struct{}{}
	}
	for id := range manifest.Retired {
		known[id] = struct{}{}
	}
	for _, id := range manifest.UnknownIDs {
		known[id] = struct{}{}
	}
	for id := range ids {
		if _, present := known[id]; !present {
			manifest.UnknownIDs = append(manifest.UnknownIDs, id)
		}
	}
	sort.Strings(manifest.UnknownIDs)
	return manifest
}

// persistedDestinationIDs returns the destination IDs still present in status
// rows. A blank sent-marker owner is ambiguous because its composite key is a
// SHA-256 digest and cannot be reversed to determine whether it used an ID.
func (t *Tracker) persistedDestinationIDs() (map[string]struct{}, bool) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	ids := make(map[string]struct{})
	unattributed := false
	for _, item := range t.apiStatus.SentItems {
		if !recordPersistedDestinationID(ids, item.DestinationID) {
			unattributed = true
		}
	}
	for _, item := range t.rssStatus.SentItems {
		if !recordPersistedDestinationID(ids, item.DestinationID) {
			unattributed = true
		}
	}
	for _, item := range t.retryStore.status.RetryQueue {
		if !recordPersistedDestinationID(ids, item.DestinationID) {
			unattributed = true
		}
	}
	for _, item := range t.retryStore.status.DeadLetterItems {
		if !recordPersistedDestinationID(ids, item.DestinationID) {
			unattributed = true
		}
	}
	return ids, unattributed
}

func recordPersistedDestinationID(ids map[string]struct{}, id string) bool {
	if !isPositionalDestinationID(id) {
		return false
	}
	ids[id] = struct{}{}
	return true
}

func isPositionalDestinationID(id string) bool {
	namespace := destinationNamespace(id)
	switch namespace {
	case "discord.ransomware", "discord.rss.ransomware", "discord.rss.general", "discord.rss.government",
		"slack.ransomware", "slack.rss.ransomware", "slack.rss.general", "slack.rss.government",
		"slack_compatible.ransomware", "slack_compatible.rss.ransomware", "slack_compatible.rss.general", "slack_compatible.rss.government":
	default:
		return false
	}
	if id == namespace {
		return true
	}
	suffix := strings.TrimPrefix(id, namespace+".")
	position, err := strconv.Atoi(suffix)
	return err == nil && position >= 2 && strconv.Itoa(position) == suffix
}

// DestinationRemap is one destination ID whose webhook URL changed, together
// with the manifest IDs in the same namespace that previously carried that URL.
type DestinationRemap struct {
	ID          string
	PreviousIDs []string
}

// DestinationDiff compares the manifest against the current configuration.
// Added and Removed describe current IDs only; Unknown marks a remap whose
// previous binding cannot be reconstructed from legacy state.
type DestinationDiff struct {
	Remapped []DestinationRemap
	Added    []string
	Removed  []string
	Unknown  bool
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

// HasRemap reports whether an ID has a changed or unknowable previous binding.
// This is the only condition that refuses a start or a reload.
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
// delete > insert > reorder > unknown > url_changed. It is empty when nothing
// was remapped, including a provably fresh append or removal from the end.
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
	case d.Unknown:
		return DestinationChangeUnknown
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
// was parseable, and why new IDs require explicit acceptance.
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
