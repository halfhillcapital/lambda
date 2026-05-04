package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ErrLockHeld is returned by acquireLock when a live process already
// holds the Session's lockfile. The caller cannot attach to that Session.
var ErrLockHeld = errors.New("session lock held by another process")

// LockPath returns .lambda/sessions/<id>/lock. Empty inputs yield "".
func LockPath(repoRoot, id string) string {
	if repoRoot == "" || id == "" {
		return ""
	}
	return filepath.Join(SessionsDir(repoRoot), id, "lock")
}

// acquireLock writes the current process's PID to the Session's lock
// file. If the lock already exists and its PID is a live process,
// returns ErrLockHeld. A stale lock (PID gone) is reclaimed.
func acquireLock(repoRoot, id string) error {
	path := LockPath(repoRoot, id)
	if path == "" {
		return errors.New("session: empty repoRoot or id")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("session: mkdir lock dir: %w", err)
	}
	if pid, ok := readLockPID(path); ok {
		if processAlive(pid) {
			return fmt.Errorf("%w (pid %d)", ErrLockHeld, pid)
		}
		// Stale: fall through and overwrite.
	}
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o644); err != nil {
		return fmt.Errorf("session: write lock: %w", err)
	}
	return nil
}

// removeLock deletes the Session's lock file. Missing-file is not an error.
func removeLock(repoRoot, id string) error {
	path := LockPath(repoRoot, id)
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LockHolder returns the PID recorded in the Session's lockfile and
// whether that process is currently alive. (0, false) when no lockfile.
func LockHolder(repoRoot, id string) (pid int, alive bool) {
	pid, ok := readLockPID(LockPath(repoRoot, id))
	if !ok {
		return 0, false
	}
	return pid, processAlive(pid)
}

func readLockPID(path string) (int, bool) {
	if path == "" {
		return 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether pid refers to a running process. On
// Windows os.FindProcess only succeeds for live PIDs (it opens an OS
// handle); on Unix FindProcess always succeeds and we fall back to
// signal 0, which returns ESRCH for dead processes.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		_ = p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
