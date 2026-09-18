package status

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

// auditRotationSnapshot is the immutable rotation-bounds view read from
// t.auditRotation. Storing it as *auditRotationSnapshot (rather than reading
// t.retention directly) is what lets the audit write path -- which runs
// under t.auditMu only, never t.mutex -- read current rotation bounds without
// a data race against UpdateRetention (t.mutex-protected). Reading
// t.retention from the audit path was proven a real race under -race.
type auditRotationSnapshot struct {
	maxSizeBytes int64
	maxBackups   int
	maxAgeDays   int
	compress     bool
}

func auditRotationSnapshotFromPolicy(policy RetentionPolicy) *auditRotationSnapshot {
	return &auditRotationSnapshot{
		maxSizeBytes: int64(policy.AuditLogMaxSizeMB) * 1024 * 1024,
		maxBackups:   policy.AuditLogMaxBackups,
		maxAgeDays:   policy.AuditLogMaxAgeDays,
		compress:     policy.AuditLogCompress != nil && *policy.AuditLogCompress,
	}
}

// setAuditRotationSnapshot stores an immutable snapshot atomically so the
// audit write path (t.auditMu only, never t.mutex) can read current rotation
// bounds without touching t.mutex. Called from newTracker and UpdateRetention,
// both of which hold or have just computed a normalized RetentionPolicy.
func (t *Tracker) setAuditRotationSnapshot(policy RetentionPolicy) {
	t.auditRotation.Store(auditRotationSnapshotFromPolicy(policy))
}

// rotateAuditFileIfNeeded runs under t.auditMu (called only from
// appendNormalizedDeliveryAuditEvent, before the file is opened for this
// write) and never acquires t.mutex. incomingBytes is the exact payload size
// including the trailing newline.
func (t *Tracker) rotateAuditFileIfNeeded(filePath string, incomingBytes int) error {
	// nil snapshot: an in-memory tracker built by NewMemoryTrackerWithRetention,
	// which bypasses newTracker's wiring. It never reaches here today (see
	// appendDeliveryAuditEvent's inMemory early return) -- this guard is what
	// keeps that true if it ever does. Do not remove it.
	snap := t.auditRotation.Load()
	if snap == nil || snap.maxSizeBytes <= 0 {
		return nil
	}
	info, err := os.Stat(filepath.Clean(filePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat delivery audit file: %w", err)
	}
	if info.Size() == 0 || info.Size()+int64(incomingBytes) <= snap.maxSizeBytes {
		return nil
	}

	now := time.Now().UTC()
	backupPath := log.NextBackupName(filePath, now)
	if err := os.Rename(filepath.Clean(filePath), filepath.Clean(backupPath)); err != nil {
		return fmt.Errorf("rotate delivery audit file: %w", err)
	}
	if snap.compress {
		// Fire-and-forget: the rename already made the active file available
		// again, and compressing an already-detached backup never needs to
		// block a producing call or another writer waiting on t.auditMu.
		// Backups are grouped by rotation (internal/logger's rotationGroupKey),
		// so the in-flight .gz.tmp/.jsonl pair counts once for pruning.
		go func(path string) {
			if err := log.CompressBackupFile(path); err != nil {
				log.WithError(err).WithField("backup", path).
					Warn("Failed to compress rotated delivery audit backup")
			}
		}(backupPath)
	}
	if err := log.CleanupBackups(filePath, snap.maxBackups, snap.maxAgeDays, now); err != nil {
		return fmt.Errorf("prune delivery audit backups: %w", err)
	}
	return nil
}
