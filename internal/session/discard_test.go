package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDiscardRemovesManifestAndWorkspace(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	id := NewID()
	wsDir := filepath.Join(repo, ".lambda", "worktrees", id)
	branch := "lambda/" + id
	if err := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, wsDir).Run(); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	m := &Manifest{ID: id, Version: ManifestVersion, WorkspaceID: id, BaseBranch: "main"}
	if err := m.Save(repo); err != nil {
		t.Fatal(err)
	}

	if err := Discard(context.Background(), repo, id); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace dir should be gone, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(SessionsDir(repo), id)); !os.IsNotExist(err) {
		t.Errorf("manifest dir should be gone, stat err=%v", err)
	}
	out, _ := exec.Command("git", "-C", repo, "branch", "--list", branch).Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch %q should be deleted, got: %s", branch, out)
	}
}

func TestDiscardRefusesWhenLockedByLiveProcess(t *testing.T) {
	repo := t.TempDir()
	id := NewID()
	if err := (&Manifest{ID: id, Version: ManifestVersion, WorkspaceID: id}).Save(repo); err != nil {
		t.Fatal(err)
	}
	// Use our own PID to simulate "another live process": readLockPID
	// finds it alive, and the test driver spoofs Discard's self-check
	// failure by calling through a different recorded id. We write
	// os.Getpid() but point Discard at a *different* repo so the
	// self-PID equality check won't bypass the refusal — wait, that
	// doesn't help because LockHolder reads from the same repo.
	//
	// Simplest: write a PID we know is live and isn't ours. The parent
	// process (`go test`) is always alive while the test runs.
	parentPID := os.Getppid()
	if parentPID == os.Getpid() || parentPID <= 1 {
		t.Skip("can't isolate a non-self live PID on this platform")
	}
	if err := os.WriteFile(LockPath(repo, id), []byte(strconv.Itoa(parentPID)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Discard(context.Background(), repo, id)
	if !errors.Is(err, ErrLockHeld) {
		t.Errorf("err=%v, want ErrLockHeld", err)
	}
	if _, statErr := os.Stat(filepath.Join(SessionsDir(repo), id)); statErr != nil {
		t.Errorf("manifest dir should still exist after refusal: %v", statErr)
	}
}

func TestDiscardOnMissingSessionIsClean(t *testing.T) {
	repo := t.TempDir()
	if err := Discard(context.Background(), repo, "nonexistent-id"); err != nil {
		t.Errorf("Discard on missing id: %v", err)
	}
}

// --- helpers ---

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-q", "-m", "init"}} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return dir
}
