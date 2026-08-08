//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package writerlock

import (
	"os"
)

func acquireFileLock(*os.File) error {
	return &unsupportedPlatformError{}
}

func releaseFileLock(*os.File) error { return nil }

func isLockBusy(err error) bool { return false }

type unsupportedPlatformError struct{}

func (*unsupportedPlatformError) Error() string {
	return "exclusive file locking is unsupported on this platform"
}
