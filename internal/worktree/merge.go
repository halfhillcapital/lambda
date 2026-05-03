package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MergePreview describes the merge that /merge would perform, computed before
// any destructive work runs. The TUI uses this to populate the confirmation
// modal so the user sees what's about to land.
type MergePreview struct {
	BaseBranch    string
	BaseShortSHA  string
	SessionBranch string
	DiffStat      string // `git diff --stat base..session`; "" when NoOp.
	Subject       string // proposed squash-commit subject.
	NoOp          bool   // true when the session has no changes to merge.
}

// MergeResult is what Merge returns after a successful merge + rotate.
type MergeResult struct {
	Subject     string
	CommitSHA   string // squash commit that landed on BaseBranch; "" when NoOp.
	NoOp        bool
	OldBranch   string // pre-merge session branch name (now gone).
	NewBranch   string // fresh session branch the worktree now sits on.
}

var (
	// ErrMergeDisabled means the session is not running with a worktree (e.g.
	// not a git repo, or --no-worktree). /merge has nothing to merge.
	ErrMergeDisabled = errors.New("worktree session is disabled")
	// ErrMergeDetached means the session was started from a detached HEAD, so
	// there's no branch to land the squash commit on.
	ErrMergeDetached = errors.New("session was started from detached HEAD; no branch to merge into")
	// ErrMergeParentDirty means the parent repo's working tree has uncommitted
	// changes. /merge refuses rather than risk mixing them with the squash.
	ErrMergeParentDirty = errors.New("parent repo has uncommitted changes; commit or stash before /merge")
	// ErrMergeParentBranch means the parent repo is checked out on a branch
	// other than the session's recorded base.
	ErrMergeParentBranch = errors.New("parent repo is not on the session base branch")
)

// MergePreview computes (without mutating anything) what a subsequent Merge
// call would do. Errors flag preconditions the user must fix first.
func (s *Session) MergePreview(ctx context.Context) (MergePreview, error) {
	if s == nil || !s.Enabled {
		return MergePreview{}, ErrMergeDisabled
	}
	if s.BaseBranch == "" || strings.HasPrefix(s.BaseBranch, "detached:") {
		return MergePreview{}, ErrMergeDetached
	}
	if err := s.checkParentReady(ctx); err != nil {
		return MergePreview{}, err
	}

	advanced := headAdvanced(ctx, s.Path, s.StartSHA)
	dirty := isDirty(ctx, s.Path)
	hasChanges := advanced || dirty

	preview := MergePreview{
		BaseBranch:    s.BaseBranch,
		BaseShortSHA:  shortSHA(s.StartSHA),
		SessionBranch: s.Branch,
		Subject:       defaultSubject(s.Branch),
		NoOp:          !hasChanges,
	}
	if !hasChanges {
		return preview, nil
	}
	// Diff stat covers committed changes; uncommitted edits won't appear here
	// but Merge auto-commits them before the squash.
	if advanced {
		if out, err := runGitOutput(ctx, s.Path, "diff", "--stat", s.StartSHA+"..HEAD"); err == nil {
			preview.DiffStat = strings.TrimRight(string(out), "\n")
		}
	}
	return preview, nil
}

// Merge squash-merges the session's changes onto BaseBranch in the parent repo
// using the given subject as the commit message, then rotates the worktree's
// branch to a fresh `lambda/<ts>` rooted at the just-merged tip. The worktree
// path is reused (avoids invalidating the agent's tools registry / cwd).
//
// On any error before the squash commit lands, no destructive work has been
// done and the caller can retry. After the commit lands we still attempt the
// rotation; rotation failures are reported but the merge is preserved.
func (s *Session) Merge(ctx context.Context, subject string) (MergeResult, error) {
	if s == nil || !s.Enabled {
		return MergeResult{}, ErrMergeDisabled
	}
	if s.BaseBranch == "" || strings.HasPrefix(s.BaseBranch, "detached:") {
		return MergeResult{}, ErrMergeDetached
	}
	if err := s.checkParentReady(ctx); err != nil {
		return MergeResult{}, err
	}
	if subject == "" {
		subject = defaultSubject(s.Branch)
	}

	advanced := headAdvanced(ctx, s.Path, s.StartSHA)
	dirty := isDirty(ctx, s.Path)
	if !advanced && !dirty {
		// Nothing to merge — caller should still rotate. We rotate here so
		// callers don't need to think about the no-op path separately.
		newBranch, err := s.rotateBranch(ctx)
		if err != nil {
			return MergeResult{}, fmt.Errorf("rotate branch: %w", err)
		}
		return MergeResult{
			Subject:   subject,
			NoOp:      true,
			OldBranch: s.Branch, // rotateBranch already updated s.Branch
			NewBranch: newBranch,
		}, nil
	}

	// Auto-commit any uncommitted edits so the squash picks them up.
	if dirty {
		if err := runGit(ctx, s.Path, "add", "-A"); err != nil {
			return MergeResult{}, fmt.Errorf("git add (auto-commit pending): %w", err)
		}
		if err := runGit(ctx, s.Path, "commit", "-q", "-m", "lambda: pending changes"); err != nil {
			return MergeResult{}, fmt.Errorf("git commit (auto-commit pending): %w", err)
		}
	}

	oldBranch := s.Branch
	if err := runGit(ctx, s.RepoRoot, "merge", "--squash", oldBranch); err != nil {
		// Best-effort cleanup of any partial squash state so the parent repo
		// isn't left with a half-applied index.
		_ = runGit(ctx, s.RepoRoot, "merge", "--abort")
		_ = runGit(ctx, s.RepoRoot, "reset", "--hard", "HEAD")
		return MergeResult{}, fmt.Errorf("git merge --squash: %w", err)
	}
	if err := runGit(ctx, s.RepoRoot, "commit", "-q", "-m", subject); err != nil {
		_ = runGit(ctx, s.RepoRoot, "reset", "--hard", "HEAD")
		return MergeResult{}, fmt.Errorf("git commit: %w", err)
	}
	commitSHA := ""
	if out, err := runGitOutput(ctx, s.RepoRoot, "rev-parse", "HEAD"); err == nil {
		commitSHA = strings.TrimSpace(string(out))
	}

	newBranch, rotErr := s.rotateBranch(ctx)
	result := MergeResult{
		Subject:   subject,
		CommitSHA: commitSHA,
		OldBranch: oldBranch,
		NewBranch: newBranch,
	}
	if rotErr != nil {
		return result, fmt.Errorf("merge committed (%s) but rotate failed: %w", shortSHA(commitSHA), rotErr)
	}
	return result, nil
}

// rotateBranch resets the worktree branch to BaseBranch's current tip and
// renames it to a fresh `lambda/<ts>`. The worktree path stays the same so
// downstream callers (tools registry, agent cwd) keep working. On success
// it updates s.Branch and s.StartSHA in place.
func (s *Session) rotateBranch(ctx context.Context) (string, error) {
	tip, err := runGitOutput(ctx, s.RepoRoot, "rev-parse", s.BaseBranch)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", s.BaseBranch, err)
	}
	tipSHA := strings.TrimSpace(string(tip))

	// Reset the worktree's checkout to the new base tip. Using --hard discards
	// the auto-commit we made earlier; the changes are already on BaseBranch
	// via the squash, so the original session branch is now redundant.
	if err := runGit(ctx, s.Path, "reset", "--hard", tipSHA); err != nil {
		return "", fmt.Errorf("reset --hard: %w", err)
	}

	newBranch := freshBranchName(s.Branch)
	if err := runGit(ctx, s.RepoRoot, "branch", "-m", s.Branch, newBranch); err != nil {
		return "", fmt.Errorf("branch -m: %w", err)
	}
	s.Branch = newBranch
	s.StartSHA = tipSHA
	return newBranch, nil
}

// freshBranchName returns a `lambda/<timestamp>` name that differs from
// previous so the rename can't collide on a fast double-/merge.
func freshBranchName(previous string) string {
	for range 5 {
		ts := time.Now().Format("20060102-150405")
		name := "lambda/" + ts
		if name != previous {
			return name
		}
		time.Sleep(1100 * time.Millisecond) // bump past the second resolution
	}
	// Shouldn't happen; fall back to a suffixed variant.
	return previous + "-r"
}

// checkParentReady verifies the parent repo is on BaseBranch and clean.
// Returns ErrMergeParentBranch / ErrMergeParentDirty otherwise.
func (s *Session) checkParentReady(ctx context.Context) error {
	cur := currentBranch(ctx, s.RepoRoot)
	if cur != s.BaseBranch {
		return fmt.Errorf("%w: parent on %q, session base is %q", ErrMergeParentBranch, cur, s.BaseBranch)
	}
	if isDirty(ctx, s.RepoRoot) {
		return ErrMergeParentDirty
	}
	return nil
}

// defaultSubject is the boilerplate squash-commit subject used when the
// caller hasn't synthesised something better.
func defaultSubject(branch string) string {
	return "lambda: session " + strings.TrimPrefix(branch, "lambda/")
}
