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
	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("expected disabled session outside a git repo")
	}
	if s.Cwd() != "" {
		t.Errorf("disabled Cwd()=%q, want empty", s.Cwd())
	}
}

func TestStartWithFlagOffDisablesSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)
	s, err := Start(context.Background(), dir, "", false)
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
	s, err := Start(context.Background(), dir, "", true)
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

	s, err := Start(context.Background(), dir, "", true)
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
	if s.BaseBranch != "main" {
		t.Errorf("BaseBranch=%q, want main", s.BaseBranch)
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
	_, err = Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	excl2, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if n := bytes.Count(excl2, []byte("/.lambda/")); n != 1 {
		t.Errorf("exclude should have 1 `/.lambda/` entry, got %d:\n%s", n, excl2)
	}
}

func TestFinalizeCleanWorktreeIsRemovedSilently(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionKeep)
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
	// Empty .lambda/ parents should also be gone.
	if _, err := os.Stat(filepath.Join(dir, ".lambda")); !os.IsNotExist(err) {
		t.Errorf(".lambda/ should be removed, got stat err=%v", err)
	}
}

func TestFinalizeKeepDirtyWorktreePersists(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// Create an uncommitted change inside the worktree.
	if err := os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionKeep)
	out := buf.String()
	if !strings.Contains(out, "left changes on branch "+s.Branch) {
		t.Errorf("missing branch notice:\n%s", out)
	}
	if !strings.Contains(out, "new.txt") {
		t.Errorf("missing status summary for new.txt:\n%s", out)
	}
	if !strings.Contains(out, "merge:   git merge "+s.Branch) {
		t.Errorf("missing merge command hint:\n%s", out)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Errorf("dirty worktree should persist: %v", err)
	}
}

func TestFinalizeKeepWithCommittedChangesPersists(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "added.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitHelper(t, s.Path, "add", "added.go")
	runGitHelper(t, s.Path, "commit", "-q", "-m", "add added.go")

	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionKeep)
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

func TestFinalizeDiscardRemovesWorktreeAndBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "trash.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionDiscard)

	out := buf.String()
	wantLine := "lambda: discarded session branch " + s.Branch + "\n"
	if out != wantLine {
		t.Errorf("discard output mismatch\n got: %q\nwant: %q", out, wantLine)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Errorf("discarded worktree should be gone, got stat err=%v", err)
	}
	branchOut, err := exec.Command("git", "-C", dir, "branch", "--list", s.Branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(branchOut)) != "" {
		t.Errorf("discarded branch %q should be deleted, got:\n%s", s.Branch, branchOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".lambda")); !os.IsNotExist(err) {
		t.Errorf(".lambda/ should be removed after discard, got stat err=%v", err)
	}
}

func TestFinalizeDiscardLeavesParentsWhenSiblingExists(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// Drop a sibling dir under .lambda/worktrees/ to simulate a concurrent session.
	sibling := filepath.Join(dir, ".lambda", "worktrees", "sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionDiscard)

	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling worktree dir should be untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".lambda")); err != nil {
		t.Errorf(".lambda/ should persist while sibling is present: %v", err)
	}
}

func TestSummaryReturnsBodyWhenChangesExist(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, hasChanges := s.Summary(context.Background())
	if !hasChanges {
		t.Fatalf("expected hasChanges=true, body=%q", body)
	}
	if !strings.Contains(body, s.Branch) {
		t.Errorf("body missing branch %q:\n%s", s.Branch, body)
	}
	if !strings.Contains(body, "new.txt") {
		t.Errorf("body missing new.txt status entry:\n%s", body)
	}
	// No command-hint lines in the modal body.
	for _, hint := range []string{"git merge", "git worktree remove", "review:"} {
		if strings.Contains(body, hint) {
			t.Errorf("body should not contain command hint %q:\n%s", hint, body)
		}
	}
}

func TestSummaryReturnsEmptyForCleanSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}

	body, hasChanges := s.Summary(context.Background())
	if hasChanges {
		t.Errorf("clean session should report no changes, got body:\n%s", body)
	}
	if body != "" {
		t.Errorf("clean session body should be empty, got: %q", body)
	}
}

func TestSummaryReturnsEmptyForDisabledSession(t *testing.T) {
	s := &Workspace{Enabled: false}
	body, hasChanges := s.Summary(context.Background())
	if hasChanges || body != "" {
		t.Errorf("disabled session should report no changes; got hasChanges=%v body=%q", hasChanges, body)
	}
}

func TestStatusReportsActiveCleanSession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}

	out := s.Status(context.Background())
	for _, want := range []string{
		"worktree: active",
		"branch:   " + s.Branch,
		"base:     main @ ",
		"path:     " + s.Path,
		"changes:  none",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Status missing %q:\n%s", want, out)
		}
	}
}

func TestStatusOnDisabledWorkspaceIsEmpty(t *testing.T) {
	s := &Workspace{Enabled: false}
	if out := s.Status(context.Background()); out != "" {
		t.Errorf("disabled Workspace.Status should be empty, got: %q", out)
	}
}

func TestStatusReportsDirtySession(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepoWithCommit(t, dir)

	s, err := Start(context.Background(), dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := s.Status(context.Background())
	if !strings.Contains(out, "uncommitted:") || !strings.Contains(out, "new.txt") {
		t.Errorf("Status missing dirty summary:\n%s", out)
	}
}

func TestFinalizeOnDisabledSessionIsNoop(t *testing.T) {
	s := &Workspace{Enabled: false}
	var buf bytes.Buffer
	s.Finalize(context.Background(), &buf, ActionDiscard)
	if buf.Len() != 0 {
		t.Errorf("disabled Finalize should be silent, got: %s", buf.String())
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
