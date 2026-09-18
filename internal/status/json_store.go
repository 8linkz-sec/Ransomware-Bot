package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	log "github.com/8linkz-sec/Ransomware-News-Bot/internal/logger"
)

var writeAtomicJSONFileFunc = writeAtomicJSONFile

// Durability seams. Production never reassigns them; the tests in
// json_store_fsync_test.go swap them to record the syscall order and to inject
// failures no real filesystem produces on demand.
var (
	syncFileFunc = func(f *os.File) error { return f.Sync() }
	renameFunc   = os.Rename
	syncDirFunc  = syncDir
	sleepFunc    = time.Sleep
)

// dirSyncWarned keeps the directory-fsync warning to a single line per process:
// a filesystem that cannot fsync a directory cannot do it for any save, and one
// WARN per save would drown the log. Atomic because status saves and the
// destination-manifest write reach this helper from different goroutines.
var dirSyncWarned atomic.Bool

func writeAtomicJSONFile(filePath, label string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal %s for %s: %w", label, filePath, err)
	}
	data = append(data, '\n')

	tempFile := filePath + statusTempFileSuffix
	if err := writeAndSyncFile(tempFile, data); err != nil {
		return fmt.Errorf("failed to write temporary %s file %s: %w", label, tempFile, err)
	}

	var renameErr error
	for attempt := 0; attempt < statusRenameAttempts; attempt++ {
		renameErr = renameFunc(tempFile, filePath)
		if renameErr == nil {
			syncStatusDirAfterRename(filePath, label)
			return nil
		}
		log.WithFields(log.Fields{
			"attempt":     attempt + 1,
			"temp_file":   tempFile,
			"target_file": filePath,
			"error":       renameErr,
		}).Warnf("Failed to rename %s file, retrying...", label)
		if attempt < statusRenameAttempts-1 {
			sleepFunc(statusRenameRetryDelay)
		}
	}

	if cleanupErr := os.Remove(tempFile); cleanupErr != nil {
		log.WithError(cleanupErr).WithField("temp_file", tempFile).Errorf("Failed to cleanup temporary %s file", label)
	}
	return fmt.Errorf(
		"failed to rename %s file from %s to %s after %d attempts: %w",
		label,
		tempFile,
		filePath,
		statusRenameAttempts,
		renameErr,
	)
}

// writeAndSyncFile writes data to path with statusFileMode and flushes it to
// stable storage before returning. Without the fsync, a hard kill shortly after
// the rename can leave the renamed file zero-length or truncated on overlayfs,
// XFS and network mounts, which either stops the bot at the next start or
// silently loses the save and re-delivers everything it recorded.
func writeAndSyncFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, statusFileMode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = syncFileFunc(file)
	}
	closeErr := file.Close()
	switch {
	case writeErr != nil:
		return writeErr
	case syncErr != nil:
		return syncErr
	default:
		return closeErr
	}
}

// syncStatusDirAfterRename makes the completed rename itself durable. It never
// fails the save: the file content is already on stable storage and the rename
// is already visible to every reader, so the only thing at stake is whether the
// directory entry survives a power loss in the next few seconds. Turning that
// into a write error would report a write that landed as failed, re-dirty the
// store and, on a mount that never supports directory fsync, stop status
// persistence for good -- a far worse outcome than the durability gap. Because
// the warning is emitted once per process, the label names only the store whose
// save happened to hit the failure first; every other status file in the same
// data directory is affected identically and stays silent.
func syncStatusDirAfterRename(filePath, label string) {
	directory := filepath.Dir(filePath)
	if err := syncDirFunc(directory); err != nil {
		if dirSyncWarned.CompareAndSwap(false, true) {
			log.WithError(err).WithFields(log.Fields{
				"directory": directory,
				"label":     label,
			}).Warn("Status directory fsync failed; status file contents are still flushed, only the rename could be lost on a hard power loss. Logged once per process.")
		}
	}
}
