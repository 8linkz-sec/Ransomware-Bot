package logger

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
)

const bytesPerMegabyte = 1024 * 1024

// errWriterClosed is returned by Write after Close. A closed writer never
// reopens the log file, so a late log record can no longer resurrect the owner
// lock of a process that is shutting down.
var errWriterClosed = errors.New("log writer is closed")

type rotatingFileWriter struct {
	filename     string
	lockPath     string
	maxSizeBytes int64
	maxBackups   int
	maxAgeDays   int
	compress     bool

	mu       sync.Mutex
	file     *os.File
	lockFile *os.File
	size     int64
	closed   bool
}

// rotatedBackup is one logical rotation, which can exist on disk in more than
// one form at once (the bare backup, an in-progress ".gz.tmp", or a finished
// ".gz") — see compressionSuffixes and rotationGroupKey. paths holds every
// form currently present; modTime is the newest member's.
type rotatedBackup struct {
	paths   []string
	modTime time.Time
}

// compressionSuffixes are the forms one rotation can take on disk beside its
// bare backup name: the finished gzip and the in-progress temp file.
var compressionSuffixes = []string{".gz.tmp", ".gz"}

// rotationGroupKey strips any compression suffix so "x.log", "x.log.gz" and
// "x.log.gz.tmp" collapse to one logical rotation. Without this, a backup
// mid-compression is counted twice and count-based pruning can delete a
// genuinely older backup instead of the still-compressing one.
func rotationGroupKey(name string) string {
	for _, suffix := range compressionSuffixes {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}

// removeBackup deletes every on-disk form of one logical rotation.
func removeBackup(backup rotatedBackup) error {
	for _, path := range backup.paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func newRotatingFileWriter(filename string, cfg LogRotationConfig) (*rotatingFileWriter, error) {
	if strings.TrimSpace(filename) == "" {
		return nil, fmt.Errorf("log file path cannot be empty")
	}
	filename = filepath.Clean(filename)

	w := &rotatingFileWriter{
		filename:     filename,
		lockPath:     filename + ".lock",
		maxSizeBytes: int64(cfg.MaxSizeMB) * bytesPerMegabyte,
		maxBackups:   cfg.MaxBackups,
		maxAgeDays:   cfg.MaxAgeDays,
		compress:     cfg.Compress,
	}
	if w.maxSizeBytes <= 0 {
		w.maxSizeBytes = bytesPerMegabyte
	}

	if err := w.openLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, errWriterClosed
	}
	if w.file == nil {
		if err := w.openLocked(); err != nil {
			return 0, err
		}
	}
	if w.shouldRotate(len(p)) {
		if err := w.rotateLocked(time.Now().UTC()); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeLocked()
}

func (w *rotatingFileWriter) shouldRotate(incomingBytes int) bool {
	return w.maxSizeBytes > 0 && w.size > 0 && w.size+int64(incomingBytes) > w.maxSizeBytes
}

func (w *rotatingFileWriter) rotateLocked(now time.Time) error {
	if err := w.closeFileLocked(); err != nil {
		return err
	}

	if info, err := os.Stat(filepath.Clean(w.filename)); err == nil && info.Size() > 0 {
		backupPath := w.nextBackupName(now)
		if err := os.Rename(filepath.Clean(w.filename), filepath.Clean(backupPath)); err != nil {
			return fmt.Errorf("rotate log file: %w", err)
		}
		if w.compress {
			if err := CompressBackupFile(backupPath); err != nil {
				return err
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat log file before rotation: %w", err)
	}

	if err := w.cleanupBackups(now); err != nil {
		return err
	}
	return w.openLocked()
}

func (w *rotatingFileWriter) openLocked() error {
	if err := w.ensureLogDir(); err != nil {
		return err
	}
	acquired := false
	if w.lockFile == nil {
		if err := w.acquireOwnerLockLocked(); err != nil {
			return err
		}
		acquired = true
	}

	file, err := os.OpenFile(filepath.Clean(w.filename), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		if acquired {
			_ = w.releaseOwnerLockLocked()
		}
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		if acquired {
			_ = w.releaseOwnerLockLocked()
		}
		return fmt.Errorf("stat log file: %w", err)
	}

	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingFileWriter) ensureLogDir() error {
	if dir := filepath.Dir(w.filename); dir != "." && dir != "" {
		if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}
	return nil
}

// acquireOwnerLockLocked takes an exclusive operating-system advisory lock on
// the sidecar lock file and records the owner in it. The kernel releases the
// lock when the process dies, so a lock file left behind by a SIGKILL, an OOM
// kill or a power loss is recovered on the next start instead of refusing
// file logging forever.
func (w *rotatingFileWriter) acquireOwnerLockLocked() error {
	lockFile, err := os.OpenFile(filepath.Clean(w.lockPath), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open log owner lock: %w", err)
	}
	if err := filelock.Acquire(lockFile); err != nil {
		_ = lockFile.Close()
		if filelock.IsUnavailable(err) {
			return fmt.Errorf(
				"log file %q is already owned by another running ransomware-bot process (%s is locked)",
				w.filename,
				w.lockPath,
			)
		}
		return fmt.Errorf("lock log owner lock %q: %w", w.lockPath, err)
	}

	// The lock file survives releases, so the previous owner's marker has to
	// be cleared before the current one is written.
	fail := func(err error) error {
		_ = filelock.Release(lockFile)
		_ = lockFile.Close()
		return err
	}
	if err := lockFile.Truncate(0); err != nil {
		return fail(fmt.Errorf("truncate log owner lock %q: %w", w.lockPath, err))
	}
	if _, err := lockFile.Seek(0, 0); err != nil {
		return fail(fmt.Errorf("seek log owner lock %q: %w", w.lockPath, err))
	}

	host, _ := os.Hostname()
	if _, err := fmt.Fprintf(
		lockFile,
		"pid=%d\nhost=%s\ncreated_at=%s\n",
		os.Getpid(),
		host,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fail(fmt.Errorf("write log owner lock %q: %w", w.lockPath, err))
	}

	w.lockFile = lockFile
	return nil
}

func (w *rotatingFileWriter) closeLocked() error {
	fileErr := w.closeFileLocked()
	lockErr := w.releaseOwnerLockLocked()
	if fileErr != nil {
		return fileErr
	}
	return lockErr
}

func (w *rotatingFileWriter) closeFileLocked() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.size = 0
	return err
}

// releaseOwnerLockLocked unlocks and closes the owner lock handle. The lock
// file intentionally stays on disk: removing an advisory lock file after
// unlocking races with another process that already opened the old inode.
func (w *rotatingFileWriter) releaseOwnerLockLocked() error {
	if w.lockFile == nil {
		return nil
	}
	unlockErr := filelock.Release(w.lockFile)
	closeErr := w.lockFile.Close()
	w.lockFile = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock log owner lock %q: %w", w.lockPath, unlockErr)
	}
	return closeErr
}

func (w *rotatingFileWriter) nextBackupName(now time.Time) string {
	return NextBackupName(w.filename, now)
}

// NextBackupName computes the rotation target for filename at now, skipping
// any candidate already occupied by a bare backup or its finished ".gz".
// Exported so a second rotator (internal/status's delivery-audit rotation)
// can reuse the same naming scheme without duplicating it.
func NextBackupName(filename string, now time.Time) string {
	dir := filepath.Dir(filename)
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	timestamp := now.Format("20060102T150405.000000000Z")

	for i := 0; i < 1000; i++ {
		suffix := timestamp
		if i > 0 {
			suffix = fmt.Sprintf("%s.%d", timestamp, i)
		}
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%s%s", stem, suffix, ext))
		if fileExists(candidate) || fileExists(candidate+".gz") {
			continue
		}
		return candidate
	}

	return filepath.Join(dir, fmt.Sprintf("%s-%s-%d%s", stem, timestamp, time.Now().UnixNano(), ext))
}

func (w *rotatingFileWriter) cleanupBackups(now time.Time) error {
	return CleanupBackups(w.filename, w.maxBackups, w.maxAgeDays, now)
}

// CleanupBackups prunes filename's rotated backups by age then by count,
// exactly as the log writer's own cleanupBackups always has. Exported so a
// second rotator can reuse it; filename need not belong to a rotatingFileWriter
// instance.
func CleanupBackups(filename string, maxBackups, maxAgeDays int, now time.Time) error {
	backups, err := listBackupsFor(filename)
	if err != nil {
		return err
	}

	kept := backups[:0]
	var cutoff time.Time
	if maxAgeDays > 0 {
		cutoff = now.AddDate(0, 0, -maxAgeDays)
	}
	for _, backup := range backups {
		if maxAgeDays > 0 && backup.modTime.Before(cutoff) {
			if err := removeBackup(backup); err != nil {
				return fmt.Errorf("remove expired log backup: %w", err)
			}
			continue
		}
		kept = append(kept, backup)
	}

	if maxBackups <= 0 || len(kept) <= maxBackups {
		return nil
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].modTime.Equal(kept[j].modTime) {
			return kept[i].paths[0] > kept[j].paths[0]
		}
		return kept[i].modTime.After(kept[j].modTime)
	})
	for _, backup := range kept[maxBackups:] {
		if err := removeBackup(backup); err != nil {
			return fmt.Errorf("remove old log backup: %w", err)
		}
	}
	return nil
}

func (w *rotatingFileWriter) listBackups() ([]rotatedBackup, error) {
	return listBackupsFor(w.filename)
}

// listBackupsFor lists filename's rotated backups, grouping every on-disk
// form of one rotation (bare, ".gz.tmp", ".gz") into a single rotatedBackup so
// an in-flight compression is never counted as two backups. Group order is
// first-seen (via a parallel order slice), not map iteration order, so the
// result stays deterministic.
func listBackupsFor(filename string) ([]rotatedBackup, error) {
	matches, err := filepath.Glob(backupPatternFor(filename))
	if err != nil {
		return nil, fmt.Errorf("list log backups: %w", err)
	}

	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	groups := make(map[string]*rotatedBackup)
	order := make([]string, 0, len(matches))
	for _, match := range matches {
		if !isRotatedBackupName(filepath.Base(match), stem, ext) {
			continue
		}
		info, err := os.Stat(filepath.Clean(match))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat log backup: %w", err)
		}
		if info.IsDir() {
			continue
		}

		key := rotationGroupKey(match)
		group, ok := groups[key]
		if !ok {
			group = &rotatedBackup{}
			groups[key] = group
			order = append(order, key)
		}
		group.paths = append(group.paths, match)
		if info.ModTime().After(group.modTime) {
			group.modTime = info.ModTime()
		}
	}

	backups := make([]rotatedBackup, 0, len(order))
	for _, key := range order {
		backups = append(backups, *groups[key])
	}
	return backups, nil
}

// backupNamePattern matches exactly the file names NextBackupName produces
// for the given stem/ext: "<stem>-<20060102T150405.000000000Z>[.N|-<unixnano>]<ext>",
// optionally compressed (ext.gz) or mid-compression (ext.gz.tmp).
// filepath.Glob's "stem-*ext*" is deliberately wider (it has to match before
// rotation ever ran); this regexp is the second, precise filter so cleanup
// only ever removes files the writer itself created.
func backupNamePattern(stem, ext string) *regexp.Regexp {
	return regexp.MustCompile(
		"^" + regexp.QuoteMeta(stem) + `-\d{8}T\d{6}\.\d{9}Z(\.\d+|-\d+)?` + regexp.QuoteMeta(ext) + `(\.gz(\.tmp)?)?$`,
	)
}

func isRotatedBackupName(name, stem, ext string) bool {
	return backupNamePattern(stem, ext).MatchString(name)
}

func (w *rotatingFileWriter) backupPattern() string {
	return backupPatternFor(w.filename)
}

func backupPatternFor(filename string) string {
	dir := filepath.Dir(filename)
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, stem+"-*"+ext+"*")
}

// CompressBackupFile gzips path in place: it writes to a "<path>.gz.tmp"
// sibling and only publishes it as "<path>.gz" by rename once the copy and
// every close have succeeded, then removes the uncompressed source. A process
// death anywhere during the copy therefore never leaves a truncated ".gz"
// beside a missing ".jsonl" — the worst on-disk outcome is a leftover
// ".gz.tmp" next to the still-intact source, which listBackupsFor groups with
// it and CleanupBackups prunes with it (see rotationGroupKey). Once the
// publish rename has succeeded, a later failure to remove the now-redundant
// source (for example another handle still has it open) leaves a valid ".gz"
// next to the still-intact source instead — also a recognised, self-healing
// on-disk form under the same grouping, and never undone by that failure.
func CompressBackupFile(path string) error {
	path = filepath.Clean(path)
	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log backup for compression: %w", err)
	}
	defer source.Close()

	compressedPath := path + ".gz"
	tempPath := compressedPath + ".tmp"
	// Sweep a leftover from an earlier crashed compression: nothing else ever
	// retries this backup, and data_dir's cleanupStatusTempFiles removes only
	// three fixed status file names, never this one.
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale compressed log backup temp file: %w", err)
	}
	target, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compressed log backup: %w", err)
	}

	gzipWriter := gzip.NewWriter(target)
	_, copyErr := io.Copy(gzipWriter, source)
	closeGzipErr := gzipWriter.Close()
	closeTargetErr := target.Close()
	// Close the source explicitly here, before any attempt to remove it
	// below: Windows refuses to remove a file that still has an open handle
	// ("used by another process"), unlike POSIX, where an open-but-unlinked
	// file removes cleanly. The deferred Close above still runs at return and
	// is a harmless no-op the second time.
	closeSourceErr := source.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("compress log backup: %w", copyErr)
	}
	if closeGzipErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("finish compressed log backup: %w", closeGzipErr)
	}
	if closeTargetErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close compressed log backup: %w", closeTargetErr)
	}
	// Atomic publish: no reader and no cleanup pass ever observes a partial
	// .gz. An existing X.gz at this point can only be a leftover from a
	// crashed compression (nextBackupName never targets an occupied name), so
	// replacing it is correct self-healing rather than refusing forever. The
	// archive at tempPath is already complete and valid here regardless of
	// closeSourceErr -- every byte was already copied and both write-side
	// closes already succeeded, so a failure to close the read-only source
	// handle says nothing about what was written. Publish before checking
	// closeSourceErr so that failure cannot discard a good compression, the
	// same principle the final os.Remove(path) failure below already follows.
	// Unreachable on a local filesystem today, since a plain read-only
	// os.File.Close() does not fail there, but a network-mounted data_dir
	// can fail close() after a successful write.
	if err := os.Rename(tempPath, compressedPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("publish compressed log backup: %w", err)
	}
	if closeSourceErr != nil {
		return fmt.Errorf("close log backup source: %w", closeSourceErr)
	}
	// The compressed backup is already published at this point: a failure to
	// remove the now-redundant source must not undo it. Leaving both forms on
	// disk is fine -- rotationGroupKey groups them as one logical backup and
	// a later CleanupBackups/removeBackup deletes both together. IsNotExist
	// is tolerated rather than reported: a background compression's group
	// can be pruned by a later rotation's synchronous CleanupBackups between
	// this goroutine's publish rename and this remove, which already deleted
	// path -- that is a successful outcome (source gone), not a failure.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove uncompressed log backup: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}
