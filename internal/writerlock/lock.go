// Package writerlock prevents concurrent CrawlObserver processes from
// mutating one shared application state directory.
package writerlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const fileName = ".crawlobserver-writer.lock"

// ErrAlreadyLocked means another live process owns the writer lock.
var ErrAlreadyLocked = errors.New("CrawlObserver writer lock is already held")

// Lock is an exclusive, process-lifetime lock. The operating system releases
// it automatically if the owning process exits unexpectedly.
type Lock struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// Acquire takes the non-blocking writer lock stored in dataDir. It never
// removes an existing lock file: liveness is determined by the OS lock, not
// the file's presence.
func Acquire(dataDir string) (*Lock, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("writer lock data directory is empty")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating writer lock directory %q: %w", dataDir, err)
	}

	path := filepath.Join(dataDir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening writer lock %q: %w", path, err)
	}
	if err := acquireFileLock(f); err != nil {
		_ = f.Close()
		if isLockBusy(err) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyLocked, path)
		}
		return nil, fmt.Errorf("acquiring writer lock %q: %w", path, err)
	}

	return &Lock{file: f, path: path}, nil
}

// Path returns the lock-file path for diagnostics.
func (l *Lock) Path() string {
	return l.path
}

// Release relinquishes the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil

	unlockErr := releaseFileLock(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return fmt.Errorf("releasing writer lock %q: %w", l.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing writer lock %q: %w", l.path, closeErr)
	}
	return nil
}
