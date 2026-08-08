package writerlock

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAcquireExcludesAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer lock.Release()

	cmd := exec.Command(os.Args[0], "-test.run=TestWriterLockHelper")
	cmd.Env = append(os.Environ(), "CRAWLOBSERVER_WRITER_LOCK_HELPER=1", "CRAWLOBSERVER_WRITER_LOCK_DIR="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "writer lock rejected\n") {
		t.Fatalf("helper output = %q, want writer lock rejection", output)
	}
}

func TestReleaseAllowsAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestWriterLockHelper")
	cmd.Env = append(os.Environ(), "CRAWLOBSERVER_WRITER_LOCK_HELPER=1", "CRAWLOBSERVER_WRITER_LOCK_DIR="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "writer lock acquired\n") {
		t.Fatalf("helper output = %q, want writer lock acquisition", output)
	}
}

func TestExitedOwnerDoesNotLeaveAStaleLock(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestWriterLockHelper")
	cmd.Env = append(
		os.Environ(),
		"CRAWLOBSERVER_WRITER_LOCK_HELPER=1",
		"CRAWLOBSERVER_WRITER_LOCK_DIR="+dir,
		"CRAWLOBSERVER_WRITER_LOCK_EXIT=1",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("owner process failed: %v\n%s", err, output)
	}

	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() after owner exit error = %v", err)
	}
	defer lock.Release()
}

func TestWriterLockHelper(t *testing.T) {
	if os.Getenv("CRAWLOBSERVER_WRITER_LOCK_HELPER") != "1" {
		return
	}
	lock, err := Acquire(os.Getenv("CRAWLOBSERVER_WRITER_LOCK_DIR"))
	if errors.Is(err, ErrAlreadyLocked) {
		_, _ = os.Stdout.WriteString("writer lock rejected\n")
		return
	}
	if err != nil {
		t.Fatalf("helper Acquire() error = %v", err)
	}
	if os.Getenv("CRAWLOBSERVER_WRITER_LOCK_EXIT") == "1" {
		os.Exit(0)
	}
	defer lock.Release()
	_, _ = os.Stdout.WriteString("writer lock acquired\n")
}
