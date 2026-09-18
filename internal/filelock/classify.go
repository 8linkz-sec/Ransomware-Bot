package filelock

import (
	"errors"
	"syscall"
)

// IsUnsupported reports whether err means the underlying filesystem does not
// support advisory locking at all -- ENOLCK/EINVAL, returned by mounts such as
// 9p and some CIFS/SMB mounts, plus the whole errors.ErrUnsupported class
// (ENOSYS, EOPNOTSUPP/ENOTSUP; see syscall.Errno.Is in syscall/syscall_unix.go,
// verified against this machine's installed Go) that other filesystems may
// return for the same reason -- as opposed to a lock already held by another
// process (IsUnavailable) or an unrelated I/O error. A FUSE mount (including
// an Unraid /mnt/user/... share) is NOT reliably covered by any of this: when
// the FUSE server sets no_flock, the kernel falls back to a purely local BSD
// lock that simply succeeds, so this classifier never sees an error for that
// case -- a known, undetectable limitation. ENOLCK and EINVAL (and the
// errors.ErrUnsupported errnos) are all defined on every platform this
// package builds for, so this classifier needs no build tag; the condition
// itself is only ever observed on Linux mounts that do not implement locking.
func IsUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, errors.ErrUnsupported)
}
