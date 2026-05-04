package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Repair GCs orphan worktree directories under .lambda/worktrees/ that
// no manifest references. Returns the count of dirs removed.
//
// /merge rotation has a window between creating the new workspace and
// rewriting the manifest where an orphan-looking dir is actually
// load-bearing for another lambda process. To stay correct across
// processes, Repair bails (returns 0, nil) if any *other* Session has
// a live lock — meaning another lambda is attached and might be
// mid-rotation. currentID is the id we ourselves hold the lock on,
// excluded from that liveness check.
//
// Best-effort on the cleanup itself: missing worktree registrations
// and already-deleted branches don't fail. Anything Repair can't
// remove is left alone for the next startup to retry.
func Repair(ctx context.Context, repoRoot, currentID string) (int, error) {
	if repoRoot == "" {
		return 0, errors.New("session: empty repoRoot")
	}
	manifests, err := List(repoRoot)
	if err != nil {
		return 0, err
	}
	for _, m := range manifests {
		if m.ID == currentID {
			continue
		}
		if pid, alive := LockHolder(repoRoot, m.ID); alive && pid > 0 {
			return 0, nil
		}
	}

	inUse := make(map[string]struct{}, len(manifests))
	for _, m := range manifests {
		if m.WorkspaceID != "" {
			inUse[m.WorkspaceID] = struct{}{}
		}
	}

	wsRoot := filepath.Join(repoRoot, ".lambda", "worktrees")
	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("session: read %s: %w", wsRoot, err)
	}

	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, used := inUse[e.Name()]; used {
			continue
		}
		discardWorkspace(ctx, repoRoot, e.Name())
		removed++
	}
	// Tidy empty worktrees parent if everything got removed.
	_ = os.Remove(wsRoot)
	return removed, nil
}
