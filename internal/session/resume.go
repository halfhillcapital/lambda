package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lambda/internal/worktree"
)

// ErrSessionNotFound is returned by Resume when no Session id matches
// the given prefix.
var ErrSessionNotFound = errors.New("no session matches prefix")

// ErrSessionAmbiguous is returned by Resume when more than one Session
// id or title prefix-matches.
var ErrSessionAmbiguous = errors.New("session prefix is ambiguous")

// ErrWorkspaceMissing is returned by Resume when the manifest's
// Workspace dir is gone. Hard error per decisions §10 — silent
// recreation would hide real data loss.
var ErrWorkspaceMissing = errors.New("workspace directory missing")

// Resume reattaches to a previously persisted Session under repoRoot.
// prefix is matched against Session ids and titles; on a unique hit
// the manifest is loaded, the Workspace is rebound, the lockfile is
// taken, and a *Session is returned ready to drive a fresh
// conversation. (History replay lands in a later step.)
func Resume(ctx context.Context, repoRoot, cwd, prefix string) (*Session, error) {
	if repoRoot == "" {
		return nil, errors.New("session: empty repoRoot")
	}
	manifests, err := List(repoRoot)
	if err != nil {
		return nil, err
	}
	m, err := matchPrefix(manifests, prefix)
	if err != nil {
		return nil, err
	}

	wsDir := filepath.Join(repoRoot, ".lambda", "worktrees", m.WorkspaceID)
	if info, err := os.Stat(wsDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceMissing, wsDir)
	}

	branch, err := resolveWorkspaceBranch(ctx, repoRoot, wsDir)
	if err != nil {
		return nil, fmt.Errorf("session: resolve workspace branch: %w", err)
	}
	startSHA, err := worktreeHeadSHA(ctx, wsDir)
	if err != nil {
		return nil, fmt.Errorf("session: read workspace HEAD: %w", err)
	}

	if err := acquireLock(repoRoot, m.ID); err != nil {
		return nil, err
	}

	ws := &worktree.Workspace{
		Enabled:    true,
		Path:       wsDir,
		Branch:     branch,
		BaseBranch: m.BaseBranch,
		StartSHA:   startSHA,
		RepoRoot:   repoRoot,
	}

	s := &Session{
		repoRoot:  repoRoot,
		cwd:       cwd,
		manifest:  m,
		workspace: ws,
		persisted: true,
		lockHeld:  true,
	}
	return s, nil
}

// matchPrefix selects the manifest whose id (or title) starts with the
// given prefix. An empty prefix is rejected — Resume is opt-in and
// should never silently pick a Session.
func matchPrefix(manifests []*Manifest, prefix string) (*Manifest, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("session: empty resume prefix")
	}
	var hits []*Manifest
	for _, m := range manifests {
		if strings.HasPrefix(m.ID, prefix) {
			hits = append(hits, m)
			continue
		}
		if m.Title != nil && strings.HasPrefix(*m.Title, prefix) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, prefix)
	case 1:
		return hits[0], nil
	default:
		var ids []string
		for _, m := range hits {
			ids = append(ids, m.ID)
		}
		return nil, fmt.Errorf("%w: %q matches %s", ErrSessionAmbiguous, prefix, strings.Join(ids, ", "))
	}
}

// resolveWorkspaceBranch returns the branch checked out in wsDir. If
// the worktree is sitting on a detached HEAD because the branch was
// deleted externally, recreate `lambda/<workspaceID>` at HEAD and
// return that — per decisions §10.
func resolveWorkspaceBranch(ctx context.Context, repoRoot, wsDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", wsDir, "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch != "" {
		return branch, nil
	}
	// Detached HEAD: recreate the conventional branch at the worktree's
	// current SHA and check it out.
	sha, err := worktreeHeadSHA(ctx, wsDir)
	if err != nil {
		return "", err
	}
	wsName := filepath.Base(wsDir)
	branch = "lambda/" + wsName
	if err := exec.CommandContext(ctx, "git", "-C", repoRoot, "branch", branch, sha).Run(); err != nil {
		return "", fmt.Errorf("recreate branch %s: %w", branch, err)
	}
	if err := exec.CommandContext(ctx, "git", "-C", wsDir, "checkout", "-q", branch).Run(); err != nil {
		return "", fmt.Errorf("checkout %s: %w", branch, err)
	}
	return branch, nil
}

func worktreeHeadSHA(ctx context.Context, wsDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", wsDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
