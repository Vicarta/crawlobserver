//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package writerlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func acquireFileLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func releaseFileLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func isLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
