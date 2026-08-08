package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/writerlock"
)

var processWriterLock struct {
	sync.Mutex
	lock    *writerlock.Lock
	dataDir string
	holders int
}

// acquireWriterLockBeforeConfig serializes state-mutating commands before
// config.Load can persist first-run settings or recover legacy SQLite data.
// The returned release function is safe to call once.
func acquireWriterLockBeforeConfig() (func(), error) {
	dataDir, err := config.WriterStateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving CrawlObserver state directory: %w", err)
	}
	return acquireWriterLock(dataDir)
}

func acquireWriterLock(dataDir string) (func(), error) {
	dataDir = filepath.Clean(dataDir)
	processWriterLock.Lock()
	defer processWriterLock.Unlock()

	if processWriterLock.lock != nil {
		if processWriterLock.dataDir != dataDir {
			return nil, fmt.Errorf("CrawlObserver writer lock already protects %q, not %q", processWriterLock.dataDir, dataDir)
		}
		processWriterLock.holders++
		return writerLockRelease(), nil
	}

	lock, err := writerlock.Acquire(dataDir)
	if err == nil {
		processWriterLock.lock = lock
		processWriterLock.dataDir = dataDir
		processWriterLock.holders = 1
		return writerLockRelease(), nil
	}
	if errors.Is(err, writerlock.ErrAlreadyLocked) {
		return nil, fmt.Errorf(
			"another CrawlObserver writer already owns the shared state directory %q; stop that process before starting this command (lock: %s)",
			dataDir,
			filepath.Join(dataDir, ".crawlobserver-writer.lock"),
		)
	}
	return nil, fmt.Errorf("acquiring CrawlObserver writer lock for %q: %w", dataDir, err)
}

func writerLockRelease() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			processWriterLock.Lock()
			defer processWriterLock.Unlock()
			processWriterLock.holders--
			if processWriterLock.holders > 0 || processWriterLock.lock == nil {
				return
			}
			lock := processWriterLock.lock
			processWriterLock.lock = nil
			processWriterLock.dataDir = ""
			_ = lock.Release()
		})
	}
}

func releaseAllWriterLocks() {
	processWriterLock.Lock()
	defer processWriterLock.Unlock()
	if processWriterLock.lock == nil {
		return
	}
	lock := processWriterLock.lock
	processWriterLock.lock = nil
	processWriterLock.dataDir = ""
	processWriterLock.holders = 0
	_ = lock.Release()
}

func loadWriterConfig() (*config.Config, func(), error) {
	release, err := acquireWriterLockBeforeConfig()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	if dataDir := filepath.Clean(filepath.Dir(cfg.Server.SQLitePath)); dataDir != processWriterStateDir() {
		release()
		return nil, nil, fmt.Errorf("configured state directory changed while acquiring the writer lock: %q", dataDir)
	}
	return cfg, release, nil
}

func processWriterStateDir() string {
	processWriterLock.Lock()
	defer processWriterLock.Unlock()
	return processWriterLock.dataDir
}
