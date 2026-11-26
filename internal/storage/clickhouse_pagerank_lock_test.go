package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPageRankSessionLockContextCancellation(t *testing.T) {
	sessionID := uuid.NewString()
	lock := pageRankAttemptLock(sessionID)
	if aliasLock := pageRankAttemptLock(strings.ToUpper(strings.ReplaceAll(sessionID, "-", ""))); aliasLock != lock {
		t.Fatal("UUID aliases did not resolve to the same session lock")
	}

	lock.Lock()
	otherSessionLock := pageRankAttemptLock(uuid.NewString())
	otherCtx, cancelOther := context.WithTimeout(context.Background(), time.Second)
	if err := otherSessionLock.LockContext(otherCtx); err != nil {
		cancelOther()
		t.Fatalf("distinct session lock was blocked: %v", err)
	}
	otherSessionLock.Unlock()
	cancelOther()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		err := lock.LockContext(ctx)
		if err == nil {
			lock.Unlock()
		}
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("LockContext error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		lock.Unlock()
		t.Fatal("LockContext did not return after cancellation")
	}
	lock.Unlock()

	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), time.Second)
	defer cancelVerify()
	if err := lock.LockContext(verifyCtx); err != nil {
		t.Fatalf("session lock was leaked after canceled acquisition: %v", err)
	}
	lock.Unlock()
}
