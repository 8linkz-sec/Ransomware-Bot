//go:build !windows

package status

import (
	"os"
)

// syncDir flushes a directory's metadata so a completed rename survives a hard
// power loss. Opening the directory read-only and calling fsync(2) on the
// handle is the POSIX way to do that; ext4, XFS, overlayfs and FUSE mounts all
// accept it.
func syncDir(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
