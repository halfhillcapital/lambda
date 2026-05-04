package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRepairRemovesOrphanWorkspaces(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	// Manifest references workspace A; workspace B has no manifest (orphan).
	idA := NewID()
	idB := NewID()
	mA := &Manifest{ID: idA, Version: ManifestVersion, WorkspaceID: idA, BaseBranch: "main"}
	if err := mA.Save(repo); err != nil {
		t.Fatal(err)
	}
	wsA := filepath.Join(repo, ".lambda", "worktrees", idA)
	wsB := filepath.Join(repo, ".lambda", "worktrees", idB)
	if err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "lambda/"+idA, wsA).Run(); err != nil {
		t.Fatalf("worktree add A: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "lambda/"+idB, wsB).Run(); err != nil {
		t.Fatalf("worktree add B: %v", err)
	}

	n, err := Repair(context.Background(), repo, idA)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if n != 1 {
		t.Errorf("removed=%d, want 1", n)
	}
	if _, err := os.Stat(wsA); err != nil {
		t.Errorf("manifest-referenced workspace should survive: %v", err)
	}
	if _, err := os.Stat(wsB); !os.IsNotExist(err) {
		t.Errorf("orphan workspace should be gone, stat err=%v", err)
	}
}

func TestRepairBailsWhenAnotherLambdaIsAlive(t *testing.T) {
	repo := t.TempDir()

	// Two manifests; one's lock is held by a foreign live process.
	ours := NewID()
	other := NewID()
	for _, m := range []*Manifest{
		{ID: ours, Version: ManifestVersion, WorkspaceID: ours},
		{ID: other, Version: ManifestVersion, WorkspaceID: other},
	} {
		if err := m.Save(repo); err != nil {
			t.Fatal(err)
		}
	}
	parentPID := os.Getppid()
	if parentPID == os.Getpid() || parentPID <= 1 {
		t.Skip("can't isolate a non-self live PID on this platform")
	}
	if err := os.WriteFile(LockPath(repo, other), []byte(strconv.Itoa(parentPID)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Drop an orphan worktree dir that Repair would normally GC.
	orphan := filepath.Join(repo, ".lambda", "worktrees", "orphan-id")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	n, err := Repair(context.Background(), repo, ours)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if n != 0 {
		t.Errorf("removed=%d, want 0 (other lambda alive)", n)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphan should be untouched while another lambda is alive: %v", err)
	}
}

func TestRepairOnFreshRepoIsClean(t *testing.T) {
	repo := t.TempDir()
	n, err := Repair(context.Background(), repo, "")
	if err != nil {
		t.Errorf("Repair on fresh repo: %v", err)
	}
	if n != 0 {
		t.Errorf("removed=%d, want 0", n)
	}
}
