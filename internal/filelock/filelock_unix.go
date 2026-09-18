//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

func Acquire(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func Release(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func IsUnavailable(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
