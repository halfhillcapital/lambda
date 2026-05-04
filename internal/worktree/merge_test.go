package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeSquashesAndRotates covers the happy path: a session with both
// committed and uncommitted changes squash-merges onto the base branch as
// one commit, the worktree path is preserved, and the session branch is
// renamed to a fresh `lambda/<ts>` rooted at the new tip.
func TestMergeSquashesAndRotates(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	oldBranch := s.Branch
	oldPath := s.Path

	// Two committed changes inside the worktree, plus an uncommitted edit.
	writeAndCommit(t, s.Path, "a.txt", "first\n", "add a")
	writeAndCommit(t, s.Path, "b.txt", "second\n", "add b")
	if err := os.WriteFile(filepath.Join(s.Path, "c.txt"), []byte("third\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := s.Merge(context.Background(), "lambda: test session")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.NoOp {
		t.Fatalf("expected non-noop merge, got %+v", res)
	}
	if res.OldBranch != oldBranch {
		t.Errorf("OldBranch=%q, want %q", res.OldBranch, oldBranch)
	}
	if res.NewBranch == oldBranch || !strings.HasPrefix(res.NewBranch, "lambda/") {
		t.Errorf("NewBranch=%q should be a fresh lambda/ branch", res.NewBranch)
	}
	if res.CommitSHA == "" {
		t.Error("CommitSHA should be set on a non-noop merge")
	}

	// Worktree path is preserved (so the registry/cwd stay valid).
	if s.Path != oldPath {
		t.Errorf("Path mutated: %q -> %q", oldPath, s.Path)
	}
	// Session branch tracks the rename.
	if s.Branch != res.NewBranch {
		t.Errorf("s.Branch=%q, want %q", s.Branch, res.NewBranch)
	}

	// main now has exactly one new commit on top of the initial one (squash).
	logOut := mustGitOut(t, dir, "log", "main", "--oneline")
	lines := strings.Split(strings.TrimSpace(logOut), "\n")
	if len(lines) != 2 {
		t.Errorf("main should have 2 commits (init + squash), got %d:\n%s", len(lines), logOut)
	}
	if !strings.Contains(lines[0], "lambda: test session") {
		t.Errorf("squash commit subject missing from log[0]=%q", lines[0])
	}

	// All three files made it onto main via the squash.
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s on main: %v", f, err)
		}
	}

	// Old session branch is gone.
	if out := mustGitOut(t, dir, "branch", "--list", oldBranch); strings.TrimSpace(out) != "" {
		t.Errorf("old branch %q should be gone, got: %s", oldBranch, out)
	}
	// New session branch exists.
	if out := mustGitOut(t, dir, "branch", "--list", res.NewBranch); strings.TrimSpace(out) == "" {
		t.Errorf("new branch %q should exist", res.NewBranch)
	}
}

func TestMergeNoOpRotatesBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	oldBranch := s.Branch

	res, err := s.Merge(context.Background(), "")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !res.NoOp {
		t.Errorf("expected NoOp result, got %+v", res)
	}
	if res.NewBranch == "" || res.NewBranch == oldBranch {
		t.Errorf("expected fresh branch != %q, got %q", oldBranch, res.NewBranch)
	}
}

func TestMergeRefusesDirtyParent(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, s.Path, "x.txt", "x\n", "add x")

	// Make the parent repo dirty.
	if err := os.WriteFile(filepath.Join(dir, "dirt.txt"), []byte("dirt"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Merge(context.Background(), "lambda: x"); !errors.Is(err, ErrMergeParentDirty) {
		t.Errorf("err=%v, want ErrMergeParentDirty", err)
	}
}

func TestMergeRefusesParentOnWrongBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, s.Path, "x.txt", "x\n", "add x")

	// Switch parent to a different branch.
	runGitHelper(t, dir, "checkout", "-q", "-b", "feature")

	if _, err := s.Merge(context.Background(), "lambda: x"); !errors.Is(err, ErrMergeParentBranch) {
		t.Errorf("err=%v, want ErrMergeParentBranch", err)
	}
}

func TestMergePreviewReportsNoOp(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.MergePreview(context.Background())
	if err != nil {
		t.Fatalf("MergePreview: %v", err)
	}
	if !p.NoOp {
		t.Errorf("expected NoOp preview on clean session, got %+v", p)
	}
	if p.BaseBranch != "main" {
		t.Errorf("BaseBranch=%q, want main", p.BaseBranch)
	}
}

func TestMergeOnDisabledSessionErrors(t *testing.T) {
	s := &Workspace{Enabled: false}
	if _, err := s.Merge(context.Background(), ""); !errors.Is(err, ErrMergeDisabled) {
		t.Errorf("err=%v, want ErrMergeDisabled", err)
	}
	if _, err := s.MergePreview(context.Background()); !errors.Is(err, ErrMergeDisabled) {
		t.Errorf("preview err=%v, want ErrMergeDisabled", err)
	}
}

// --- helpers ---

func writeAndCommit(t *testing.T, cwd, name, contents, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitHelper(t, cwd, "add", name)
	runGitHelper(t, cwd, "commit", "-q", "-m", msg)
}

func mustGitOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
