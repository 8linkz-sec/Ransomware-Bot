package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/i18n"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

func TestDestinationManifestV1ForgottenIDRequiresAcceptance(t *testing.T) {
	dataDir := t.TempDir()
	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tracker.MarkAPIItemSentToDestination("old-item", "old title", "discord.ransomware.2")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(status.DestinationManifest{
		Version: 1, Destinations: config.DestinationURLHashes(destinationManifestConfig(dataDir, destinationWebhookA)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	current := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	diff, pending, skipped := pendingDestinationRemap(current, dataDir)
	if !pending || skipped != nil || diff.Kind() != status.DestinationChangeUnknown {
		t.Fatalf("v1 forgotten ID = diff %+v, pending %v, skipped %v", diff, pending, skipped)
	}
	var out bytes.Buffer
	if code := applyDestinationManifest(current, cliOptions{}, &out, i18n.Default()); code != 1 {
		t.Fatalf("v1 forgotten-ID restart = %d, want 1; out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "previous endpoint binding unknown") {
		t.Fatalf("v1 refusal does not explain unknown binding: %q", out.String())
	}
	if code := applyDestinationManifest(current, cliOptions{acceptDestinationRemap: true}, io.Discard, i18n.Default()); code != 0 {
		t.Fatalf("accepted v1 restart = %d, want 0", code)
	}
	upgraded, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found || upgraded.Version != status.DestinationManifestVersion {
		t.Fatalf("upgraded manifest = %+v, found %v, err %v", upgraded, found, err)
	}
	if diff := status.DiffDestinationManifest(upgraded, config.DestinationURLHashes(current)); diff.HasRemap() {
		t.Fatalf("accepted v1 mapping still refused: %+v", diff)
	}
}

func TestDestinationManifestTwoStartGuardAcrossOperatorSurfaces(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	configDir := filepath.Join(root, "configs")
	initial := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	seedDestinationManifest(t, initial)
	if code := applyDestinationManifest(destinationManifestConfig(dataDir), cliOptions{}, io.Discard, i18n.Default()); code != 0 {
		t.Fatalf("removed-endpoints start = %d, want 0", code)
	}
	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found || len(manifest.Destinations) != 0 || len(manifest.Retired) != 4 {
		t.Fatalf("removed manifest = %+v, found %v, err %v", manifest, found, err)
	}
	swapped := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)
	diff, pending, skipped := pendingDestinationRemap(swapped, dataDir)
	if !pending || skipped != nil || diff.Kind() != status.DestinationChangeReorder {
		t.Fatalf("pendingDestinationRemap() = %+v, pending %v, skipped %v", diff, pending, skipped)
	}
	var out bytes.Buffer
	if code := applyDestinationManifest(swapped, cliOptions{}, &out, i18n.Default()); code != 1 {
		t.Fatalf("swapped restart = %d, want refusal; out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "discord.ransomware<-discord.ransomware.2") {
		t.Fatalf("start refusal lacks original owner: %q", out.String())
	}
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)
	out.Reset()
	if code := runCheckConfig(cliOptions{configDir: configDir, dataDir: dataDir}, &out, i18n.Default()); code != 1 {
		t.Fatalf("--check-config = %d, want 1; out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "discord.ransomware<-discord.ransomware.2") {
		t.Fatalf("--check-config lacks original owner: %q", out.String())
	}
	markerPath := filepath.Join(root, "not-ready")
	t.Setenv(readinessFileEnv, markerPath)
	if err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default()); err == nil || !strings.Contains(err.Error(), "destination remap pending") {
		t.Fatalf("--healthcheck error = %v, want pending remap before readiness", err)
	}
	if code := applyDestinationManifest(swapped, cliOptions{acceptDestinationRemap: true}, io.Discard, i18n.Default()); code != 0 {
		t.Fatalf("accepted restart = %d, want 0", code)
	}
	accepted, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found {
		t.Fatalf("accepted manifest = found %v, err %v", found, err)
	}
	if diff := status.DiffDestinationManifest(accepted, config.DestinationURLHashes(swapped)); diff.HasRemap() {
		t.Fatalf("accepted mapping still refused: %+v", diff)
	}
}

func TestDestinationManifestCorruptThenEmptyThenSwappedRequiresAccept(t *testing.T) {
	dataDir := t.TempDir()
	initial := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	seedDestinationManifest(t, initial)
	if err := os.WriteFile(status.DestinationManifestPath(dataDir), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := applyDestinationManifest(destinationManifestConfig(dataDir), cliOptions{}, io.Discard, i18n.Default()); code != 0 {
		t.Fatalf("empty start after corruption = %d, want 0", code)
	}
	manifest, found, err := status.LoadDestinationManifest(dataDir)
	if err != nil || !found || !manifest.Unattributed {
		t.Fatalf("empty self-heal lost uncertainty: %+v, found %v, err %v", manifest, found, err)
	}
	swapped := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)
	if code := applyDestinationManifest(swapped, cliOptions{}, io.Discard, i18n.Default()); code != 1 {
		t.Fatalf("swapped start after corrupt-empty = %d, want refusal", code)
	}
}

func TestDestinationManifestDeletedWithPersistedMarkerRequiresAccept(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	initial := destinationManifestConfig(dataDir, destinationWebhookA, destinationWebhookB)
	seedDestinationManifest(t, initial)
	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tracker.MarkAPIItemSentToDestination("old-item", "title", "discord.ransomware.2")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(status.DestinationManifestPath(dataDir)); err != nil {
		t.Fatal(err)
	}
	swapped := destinationManifestConfig(dataDir, destinationWebhookB, destinationWebhookA)
	diff, pending, skipped := pendingDestinationRemap(swapped, dataDir)
	if !pending || skipped != nil || diff.Kind() != status.DestinationChangeUnknown {
		t.Fatalf("missing-manifest preflight = %+v, pending %v, skipped %v", diff, pending, skipped)
	}
	var out bytes.Buffer
	if code := applyDestinationManifest(swapped, cliOptions{}, &out, i18n.Default()); code != 1 {
		t.Fatalf("swapped start after manifest deletion = %d, want refusal", code)
	}
	if strings.Contains(out.String(), destinationWebhookA) || strings.Contains(out.String(), destinationWebhookB) {
		t.Fatalf("refusal exposed configured webhook URL: %q", out.String())
	}
	configDir := filepath.Join(root, "configs")
	writeDestinationSubprocessConfig(t, configDir, dataDir, "", destinationWebhookB, destinationWebhookA)
	out.Reset()
	if code := runCheckConfig(cliOptions{configDir: configDir, dataDir: dataDir}, &out, i18n.Default()); code != 1 {
		t.Fatalf("--check-config after manifest deletion = %d, want refusal", code)
	}
	t.Setenv(readinessFileEnv, filepath.Join(root, "not-ready"))
	if err := runHealthcheckWithMessages(configDir, dataDir, io.Discard, i18n.Default()); err == nil ||
		!strings.Contains(err.Error(), "destination remap pending") {
		t.Fatalf("healthcheck after manifest deletion = %v, want pending remap", err)
	}
}
