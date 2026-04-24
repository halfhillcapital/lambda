package worktree

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartOutsideRepoDisablesSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("expected disabled session outside a git repo")
	}
	if s.Cwd() != dir {
		t.Errorf("Cwd()=%q, want %q", s.Cwd(), dir)
	}
}

func TestStartWithFlagOffDisablesSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)
	s, err := Start(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("expected disabled when enabled=false")
	}
}

func TestStartInEmptyRepoDisablesSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGitHelper(t, dir, "init", "-q", "-b", "main")
	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("expected disabled in a repo with no commits")
	}
}

func TestStartCreatesWorktreeAndExclude(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled {
		t.Fatal("expected enabled session")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree path missing: %v", err)
	}
	if !strings.HasPrefix(s.Branch, "lambda/") {
		t.Errorf("branch=%q, want prefix lambda/", s.Branch)
	}
	// .git/info/exclude should now contain /.lambda/
	excl, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(excl), "/.lambda/") {
		t.Errorf("exclude missing /.lambda/:\n%s", excl)
	}

	// Second call keeps exclude idempotent (no duplicate).
	runGitHelper(t, dir, "worktree", "remove", "--force", s.Path)
	runGitHelper(t, dir, "branch", "-D", s.Branch)
	_, err = Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	excl2, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if n := bytes.Count(excl2, []byte("/.lambda/")); n != 1 {
		t.Errorf("exclude should have 1 `/.lambda/` entry, got %d:\n%s", n, excl2)
	}
}

func TestEndCleanWorktreeIsRemovedSilently(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s.End(context.Background(), &buf)
	if buf.Len() != 0 {
		t.Errorf("expected silent clean teardown, got:\n%s", buf.String())
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path should be gone, got stat err=%v", err)
	}
	// Branch should be deleted.
	out, err := exec.Command("git", "-C", dir, "branch", "--list", s.Branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch %q should be deleted, got:\n%s", s.Branch, out)
	}
}

func TestEndDirtyWorktreePersists(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	// Create an uncommitted change inside the worktree.
	if err := os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	s.End(context.Background(), &buf)
	out := buf.String()
	if !strings.Contains(out, "left changes on branch "+s.Branch) {
		t.Errorf("missing branch notice:\n%s", out)
	}
	if !strings.Contains(out, "new.txt") {
		t.Errorf("missing status summary for new.txt:\n%s", out)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Errorf("dirty worktree should persist: %v", err)
	}
}

func TestEndWithCommittedChangesPersists(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "added.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitHelper(t, s.Path, "add", "added.go")
	runGitHelper(t, s.Path, "commit", "-q", "-m", "add added.go")

	var buf bytes.Buffer
	s.End(context.Background(), &buf)
	out := buf.String()
	if !strings.Contains(out, "committed:") {
		t.Errorf("missing committed section:\n%s", out)
	}
	if !strings.Contains(out, "added.go") {
		t.Errorf("missing added.go in diff stat:\n%s", out)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Errorf("worktree with commits should persist: %v", err)
	}
}

func TestEndOnDisabledSessionIsNoop(t *testing.T) {
	s := &Session{Enabled: false, OriginalCwd: t.TempDir()}
	var buf bytes.Buffer
	s.End(context.Background(), &buf)
	if buf.Len() != 0 {
		t.Errorf("disabled End should be silent, got: %s", buf.String())
	}
}

// --- helpers ---

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func runGitHelper(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		runGitHelper(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitHelper(t, dir, "add", "README.md")
	runGitHelper(t, dir, "commit", "-q", "-m", "init")
}
