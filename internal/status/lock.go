package status

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const dataDirLockFileName = ".ransomware-bot.lock"

// DataDirLock is a process lifetime marker that prevents multiple scheduler
// instances from writing the same status files at the same time.
type DataDirLock struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	released bool
}

// AcquireDataDirLock creates an exclusive lock file in dataDir.
func AcquireDataDirLock(dataDir string) (*DataDirLock, error) {
	if err := ensurePrivateDataDir(dataDir); err != nil {
		return nil, fmt.Errorf("prepare data_dir lock directory: %w", err)
	}

	lockPath := filepath.Join(dataDir, dataDirLockFileName)
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open data_dir lock %q: %w", lockPath, err)
	}
	if err := acquireFileLock(file); err != nil {
		_ = file.Close()
		if isFileLockUnavailable(err) {
			return nil, fmt.Errorf(
				"data_dir %q is already locked by another ransomware-bot process (%s is locked)",
				dataDir,
				lockPath,
			)
		}
		return nil, fmt.Errorf("lock data_dir lock %q: %w", lockPath, err)
	}

	if err := file.Truncate(0); err != nil {
		_ = releaseFileLock(file)
		_ = file.Close()
		return nil, fmt.Errorf("truncate data_dir lock %q: %w", lockPath, err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = releaseFileLock(file)
		_ = file.Close()
		return nil, fmt.Errorf("seek data_dir lock %q: %w", lockPath, err)
	}

	host, _ := os.Hostname()
	if _, err := fmt.Fprintf(
		file,
		"pid=%d\nhost=%s\ncreated_at=%s\n",
		os.Getpid(),
		host,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		_ = releaseFileLock(file)
		_ = file.Close()
		return nil, fmt.Errorf("write data_dir lock %q: %w", lockPath, err)
	}

	return &DataDirLock{path: lockPath, file: file}, nil
}

// Release unlocks and closes the lock file. It is safe to call more than once.
//
// The lock file intentionally remains on disk. Removing an advisory lock file
// after unlocking can race with another process opening the old inode while a
// third process creates and locks a new file at the same path.
func (l *DataDirLock) Release() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true

	var releaseErr error
	if l.file != nil {
		if err := releaseFileLock(l.file); err != nil {
			releaseErr = fmt.Errorf("unlock data_dir lock %q: %w", l.path, err)
		}
		if err := l.file.Close(); err != nil {
			if releaseErr != nil {
				releaseErr = fmt.Errorf("%v; close data_dir lock %q: %w", releaseErr, l.path, err)
			} else {
				releaseErr = fmt.Errorf("close data_dir lock %q: %w", l.path, err)
			}
		}
	}
	return releaseErr
}
