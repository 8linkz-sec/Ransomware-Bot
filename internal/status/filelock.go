package status

import (
	"os"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
)

// acquireFileLock is a var, not a func, so tests can inject a classified
// failure (ENOLCK/EINVAL) without a real lock-unsupported filesystem.
var acquireFileLock = filelock.Acquire

func releaseFileLock(file *os.File) error { return filelock.Release(file) }

func isFileLockUnavailable(err error) bool { return filelock.IsUnavailable(err) }
