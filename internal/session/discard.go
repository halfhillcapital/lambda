package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Discard tears down everything tied to a Session: the git worktree
// and its branch, then the .lambda/sessions/<id>/ directory. Refuses
// when the lockfile is held by a different live process — that's
// another lambda instance attached, and yanking its workspace would
// leave it operating on deleted state.
//
// Best-effort on git ops: a missing worktree dir or already-deleted
// branch is fine. The manifest dir removal is the only step whose
// failure is fatal — without that, the Session would still appear in
// /sessions.
func Discard(ctx context.Context, repoRoot, id string) error {
	if repoRoot == "" || id == "" {
		return errors.New("session: empty repoRoot or id")
	}
	if pid, alive := LockHolder(repoRoot, id); alive && pid != os.Getpid() {
		return fmt.Errorf("%w (pid %d)", ErrLockHeld, pid)
	}
	m, err := Load(repoRoot, id)
	if err != nil && !errors.Is(err, ErrManifestNotFound) {
		return fmt.Errorf("session: load manifest for discard: %w", err)
	}
	if m != nil {
		discardWorkspace(ctx, repoRoot, m.WorkspaceID)
	}
	dir := filepath.Join(SessionsDir(repoRoot), id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("session: remove %s: %w", dir, err)
	}
	// Best-effort tidy of empty parents — same convention as worktree teardown.
	_ = os.Remove(SessionsDir(repoRoot))
	_ = os.Remove(filepath.Join(repoRoot, ".lambda"))
	return nil
}

// Discard is the current-Session convenience: calls Discard(repoRoot,
// ID), and marks the Session so a subsequent Finalize is a no-op. The
// caller (TUI) should exit the process after this returns; the
// in-memory Workspace pointer is now dangling.
func (s *Session) Discard(ctx context.Context) error {
	if s == nil || s.manifest == nil || s.repoRoot == "" {
		return errors.New("session: cannot discard ephemeral session")
	}
	id := s.manifest.ID
	// Release our own lock first so Discard's self-check doesn't trip.
	s.releaseLock()
	if err := Discard(ctx, s.repoRoot, id); err != nil {
		return err
	}
	s.suspended = true // keep Finalize from re-tearing-down what we just deleted
	return nil
}

func discardWorkspace(ctx context.Context, repoRoot, workspaceID string) {
	if workspaceID == "" {
		return
	}
	wsDir := filepath.Join(repoRoot, ".lambda", "worktrees", workspaceID)
	if _, err := os.Stat(wsDir); err == nil {
		// `git worktree remove --force` also drops the branch's worktree
		// registration; the branch itself stays until we delete it below.
		_ = exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "remove", "--force", wsDir).Run()
	}
	// Branch name is conventionally lambda/<workspaceID>; fall back to
	// "git branch --list" patterns isn't worth the complexity — if the
	// user renamed the branch, a stale label after discard is harmless.
	branch := "lambda/" + workspaceID
	_ = exec.CommandContext(ctx, "git", "-C", repoRoot, "branch", "-D", branch).Run()
	// If the worktree dir somehow survived (worktree remove failed because
	// the registration was already broken), nuke the directory directly.
	_ = os.RemoveAll(wsDir)
	_ = os.Remove(filepath.Join(repoRoot, ".lambda", "worktrees"))
}
