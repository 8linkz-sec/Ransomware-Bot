package filelock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAcquireRefusesSecondHolderAndReleaseAllowsReacquire(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "resource.lock")

	first, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open first handle: %v", err)
	}
	defer first.Close()

	if err := Acquire(first); err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}

	second, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer second.Close()

	secondErr := Acquire(second)
	if secondErr == nil {
		_ = Release(second)
		t.Fatal("Acquire(second) succeeded while the first handle held the lock")
	}
	if !IsUnavailable(secondErr) {
		t.Fatalf("IsUnavailable(%v) = false, want true for a contended lock", secondErr)
	}

	if err := Release(first); err != nil {
		t.Fatalf("Release(first) error = %v", err)
	}

	if err := Acquire(second); err != nil {
		t.Fatalf("Acquire(second) after release error = %v", err)
	}
	if err := Release(second); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
}

func TestIsUnavailableRejectsUnrelatedErrors(t *testing.T) {
	if IsUnavailable(nil) {
		t.Fatal("IsUnavailable(nil) = true, want false")
	}
	if IsUnavailable(os.ErrNotExist) {
		t.Fatal("IsUnavailable(os.ErrNotExist) = true, want false")
	}
}

// TestIsUnsupportedRecognizesENOLCKAndEINVAL pins the classification this
// finding adds: ENOLCK/EINVAL (returned by filesystems that do not support
// advisory locking at all -- 9p and some CIFS/SMB mounts) must be classified
// as IsUnsupported, and must NOT be classified as IsUnavailable (that means
// "already locked by another process", a different condition with a
// different remedy). A FUSE mount such as an Unraid /mnt/user/... share is
// deliberately not claimed here: with no_flock set, its lock can silently
// succeed locally instead of returning either errno -- see IsUnsupported's
// doc comment.
func TestIsUnsupportedRecognizesENOLCKAndEINVAL(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOLCK, syscall.EINVAL} {
		if !IsUnsupported(errno) {
			t.Fatalf("IsUnsupported(%v) = false, want true", errno)
		}
		if IsUnavailable(errno) {
			t.Fatalf("IsUnavailable(%v) = true, want false (that marker is for contention, not an unsupported filesystem)", errno)
		}
		wrapped := errWrap(errno)
		if !IsUnsupported(wrapped) {
			t.Fatalf("IsUnsupported(wrapped %v) = false, want true (must unwrap)", errno)
		}
	}
}

func errWrap(err error) error {
	return &wrappedErr{err: err}
}

type wrappedErr struct{ err error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

// TestIsUnsupportedRejectsNilAndUnrelatedErrors mirrors
// TestIsUnavailableRejectsUnrelatedErrors for the new classifier.
func TestIsUnsupportedRejectsNilAndUnrelatedErrors(t *testing.T) {
	if IsUnsupported(nil) {
		t.Fatal("IsUnsupported(nil) = true, want false")
	}
	if IsUnsupported(os.ErrNotExist) {
		t.Fatal("IsUnsupported(os.ErrNotExist) = true, want false")
	}
}

// TestIsUnsupportedRejectsContentionAndNotExistErrnos is the reject table
// TestIsUnsupportedRejectsNilAndUnrelatedErrors's doc comment claims to be but
// is not: os.ErrNotExist is a sentinel, not syscall.ENOENT -- errors.Is
// between the two is false, since syscall.Errno.Is only special-cases
// oserror.ErrNotExist, not the other direction -- so that assertion never
// actually drove a real errno through the classifier and would not catch
// syscall.ENOENT (or the contention errnos EWOULDBLOCK/EAGAIN) being added to
// the matcher. This test drives the real errno values IsUnavailable matches
// (EWOULDBLOCK, EAGAIN) plus ENOENT directly, and must fail if any of them is
// ever classified as IsUnsupported.
func TestIsUnsupportedRejectsContentionAndNotExistErrnos(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EWOULDBLOCK, syscall.EAGAIN, syscall.ENOENT} {
		if IsUnsupported(errno) {
			t.Fatalf("IsUnsupported(%v) = true, want false (this is a contention/not-exist errno, not the unsupported-filesystem condition)", errno)
		}
	}
}

// TestIsUnsupportedRecognizesErrUnsupportedClass pins the finding that
// ENOLCK/EINVAL alone miss part of their own class: Go's syscall.Errno.Is
// (syscall/syscall_unix.go, verified against this machine's installed Go)
// maps ENOSYS and EOPNOTSUPP/ENOTSUP -- returned by filesystems that decline
// an operation entirely rather than answering ENOLCK/EINVAL -- to
// errors.ErrUnsupported. Each must classify as IsUnsupported, must not also
// classify as IsUnavailable (contention is a different condition), and the
// classification must survive one level of wrapping.
func TestIsUnsupportedRecognizesErrUnsupportedClass(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOSYS, syscall.ENOTSUP, syscall.EOPNOTSUPP} {
		if !IsUnsupported(errno) {
			t.Fatalf("IsUnsupported(%v) = false, want true", errno)
		}
		if IsUnavailable(errno) {
			t.Fatalf("IsUnavailable(%v) = true, want false (that marker is for contention, not an unsupported filesystem)", errno)
		}
		wrapped := errWrap(errno)
		if !IsUnsupported(wrapped) {
			t.Fatalf("IsUnsupported(wrapped %v) = false, want true (must unwrap)", errno)
		}
	}
}
