package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"lambda/internal/worktree"
)

// Session is the runtime spine for an agent run: the loaded manifest
// plus the Workspace it currently points at. Callers (main, TUI, tools
// registry, agent loop) hold a *Session and resolve paths through
// accessors so that rotation under /merge updates everyone.
type Session struct {
	repoRoot  string
	cwd       string
	manifest  *Manifest
	workspace *worktree.Workspace
	persisted bool
	lockHeld  bool
	suspended bool
}

// Start opens a fresh Session: allocates an id, creates the Workspace
// (or returns a disabled Workspace when not in a git repo / opted out),
// and writes the manifest to disk when the Workspace is enabled.
//
// Oneshot mode (persist=false) skips the disk write entirely — see
// .scratch/sessions-redesign/01-decisions.md §3. The returned Session
// is usable in-memory either way.
//
// model and provider are the values lambda will use for the first turn;
// they are recorded on the manifest and can be mutated later.
func Start(ctx context.Context, cwd string, useWorktree, persist bool, model, provider string) (*Session, error) {
	id := NewID()
	ws, wsErr := worktree.Start(ctx, cwd, id, useWorktree)
	// wsErr is informational: a non-nil error still hands back a usable
	// (disabled) Workspace per worktree.Start's contract.

	now := time.Now().UTC()
	m := &Manifest{
		ID:           id,
		Version:      ManifestVersion,
		CreatedAt:    now,
		LastActiveAt: now,
		WorkspaceID:  id, // first Workspace mirrors Session id; rotation suffixes (.r2, …) added later.
		BaseBranch:   ws.BaseBranch,
		BaseStartSHA: ws.StartSHA,
		Model:        model,
		Provider:     provider,
	}

	root := repoRootFor(ws, cwd)
	s := &Session{
		repoRoot:  root,
		cwd:       cwd,
		manifest:  m,
		workspace: ws,
	}

	// Persistence is justified by resume/enumerate/suspend, none of which
	// apply to a oneshot subagent run. Skip the manifest write in that case.
	if persist && ws.Enabled {
		if err := m.Save(root); err != nil {
			return s, err
		}
		if err := acquireLock(root, id); err != nil {
			return s, fmt.Errorf("session lock: %w", err)
		}
		s.persisted = true
		s.lockHeld = true
	}
	return s, wsErr
}

// repoRootFor picks the directory that .lambda/sessions/ lives under.
// With a live Workspace that's its parent repo; otherwise the caller's
// cwd, which keeps disabled Sessions from creating .lambda/ in random
// places.
func repoRootFor(ws *worktree.Workspace, cwd string) string {
	if ws != nil && ws.RepoRoot != "" {
		return ws.RepoRoot
	}
	return cwd
}

// ID returns the Session's stable id.
func (s *Session) ID() string {
	if s == nil || s.manifest == nil {
		return ""
	}
	return s.manifest.ID
}

// Manifest returns the underlying manifest. Mutations through this
// pointer must be followed by Save() to persist.
func (s *Session) Manifest() *Manifest { return s.manifest }

// Workspace returns the Workspace this Session currently points at.
// Resolved fresh on every call so that rotation under /merge is visible
// to all holders without re-plumbing.
func (s *Session) Workspace() *worktree.Workspace {
	if s == nil {
		return nil
	}
	return s.workspace
}

// Cwd returns the directory tools and the agent should run in. Live
// Workspace → its path; otherwise the original cwd captured at Start.
// Callers should use this in preference to os.Getwd so /merge rotation
// (when it lands) can change the answer without touching every site.
func (s *Session) Cwd() string {
	if s == nil {
		return ""
	}
	if s.workspace != nil && s.workspace.Enabled {
		return s.workspace.Path
	}
	return s.cwd
}

// RepoRoot returns the parent repo's toplevel — the directory that
// .lambda/ lives under. Empty when there's no live Workspace.
func (s *Session) RepoRoot() string {
	if s == nil {
		return ""
	}
	return s.repoRoot
}

// Status renders the user-facing snapshot. With a live Workspace it
// delegates to Workspace.Status; otherwise it renders the disabled-mode
// body (the Workspace doesn't carry the original cwd anymore).
func (s *Session) Status(ctx context.Context) string {
	if s == nil {
		return ""
	}
	if s.workspace != nil && s.workspace.Enabled {
		return s.workspace.Status(ctx)
	}
	return "worktree: disabled\ncwd:      " + s.cwd
}

// Finalize delegates to the Workspace's Finalize and releases the
// Session's lockfile if held. A suspended Session is a no-op: Suspend
// already persisted the manifest and released the lock, and the
// Workspace must be left as-is on disk for the next resume.
func (s *Session) Finalize(ctx context.Context, out io.Writer, action worktree.Action) {
	if s == nil || s.suspended {
		return
	}
	if s.workspace != nil {
		s.workspace.Finalize(ctx, out, action)
	}
	s.releaseLock()
}

// Suspend persists the manifest with an updated LastActiveAt, releases
// the lockfile, and marks the Session so a subsequent Finalize is a
// no-op. The Workspace is left in place on disk for the next resume.
func (s *Session) Suspend(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.suspended {
		return nil
	}
	s.suspended = true
	if !s.persisted {
		// Nothing on disk to update; nothing to release. Still mark
		// suspended so Finalize won't try to tear the worktree down.
		return nil
	}
	if err := s.touchActive(); err != nil {
		return err
	}
	s.releaseLock()
	return nil
}

// Suspended reports whether the Session has been suspended in this
// process. Used by main to decide whether to skip Finalize.
func (s *Session) Suspended() bool {
	return s != nil && s.suspended
}

// touchActive records the wall-clock moment the Session was last
// touched and persists it. Called after user-visible activity (round
// completion, merge, …) so /sessions can sort by recency.
func (s *Session) touchActive() error {
	if s == nil || s.manifest == nil || s.repoRoot == "" || !s.persisted {
		return nil
	}
	s.manifest.LastActiveAt = time.Now().UTC()
	return s.manifest.Save(s.repoRoot)
}

// releaseLock drops the lockfile if this Session holds it. Best-effort:
// failures are silent because a stale lock will be reclaimed on the
// next resume anyway.
func (s *Session) releaseLock() {
	if s == nil || !s.lockHeld {
		return
	}
	_ = removeLock(s.repoRoot, s.manifest.ID)
	s.lockHeld = false
}

// Errors exported for callers that need to distinguish missing-data
// cases from other failures.
var (
	ErrNoManifest = errors.New("session has no manifest")
)
