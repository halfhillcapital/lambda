package session

import (
	"context"
	"strings"
	"testing"
)

func TestStatusReportsDisabledSession(t *testing.T) {
	dir := t.TempDir()
	s := &Session{cwd: dir}

	out := s.Status(context.Background())
	for _, want := range []string{"worktree: disabled", "cwd:      " + dir} {
		if !strings.Contains(out, want) {
			t.Errorf("Status missing %q:\n%s", want, out)
		}
	}
}

func TestCwdFallsBackToCapturedDirWhenWorkspaceDisabled(t *testing.T) {
	dir := t.TempDir()
	s := &Session{cwd: dir}
	if got := s.Cwd(); got != dir {
		t.Errorf("Cwd()=%q, want %q", got, dir)
	}
}

func TestNewIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for range 64 {
		id := NewID()
		if !strings.HasPrefix(id, "lambda-") {
			t.Errorf("id %q missing lambda- prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSetTitlePersistsToManifest(t *testing.T) {
	root := t.TempDir()
	id := NewID()
	m := &Manifest{ID: id, Version: ManifestVersion, WorkspaceID: id}
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	s := &Session{repoRoot: root, manifest: m, persisted: true}

	if err := s.SetTitle("the redesign"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	loaded, err := Load(root, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title == nil || *loaded.Title != "the redesign" {
		t.Errorf("title not persisted: got %v", loaded.Title)
	}

	if err := s.SetTitle(""); err != nil {
		t.Fatalf("SetTitle clear: %v", err)
	}
	cleared, _ := Load(root, id)
	if cleared.Title != nil {
		t.Errorf("title not cleared: got %v", *cleared.Title)
	}
}

func TestSetModelPersistsToManifest(t *testing.T) {
	root := t.TempDir()
	id := NewID()
	m := &Manifest{ID: id, Version: ManifestVersion, WorkspaceID: id, Model: "old-model"}
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	s := &Session{repoRoot: root, manifest: m, persisted: true}

	if err := s.SetModel("new-model"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	loaded, _ := Load(root, id)
	if loaded.Model != "new-model" {
		t.Errorf("model not persisted: got %q", loaded.Model)
	}
}

func TestSetTitleOnEphemeralSessionIsInMemoryOnly(t *testing.T) {
	root := t.TempDir()
	id := NewID()
	s := &Session{
		repoRoot:  root,
		manifest:  &Manifest{ID: id, Version: ManifestVersion, WorkspaceID: id},
		persisted: false,
	}
	if err := s.SetTitle("ephemeral"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if s.Manifest().Title == nil || *s.Manifest().Title != "ephemeral" {
		t.Errorf("in-memory title not set: %+v", s.Manifest().Title)
	}
	if _, err := Load(root, id); err == nil {
		t.Errorf("ephemeral session should not have written manifest")
	}
}

func TestManifestSaveLoadRoundtrip(t *testing.T) {
	root := t.TempDir()
	id := NewID()
	title := "the redesign"
	original := &Manifest{
		ID:           id,
		Version:      ManifestVersion,
		WorkspaceID:  id,
		BaseBranch:   "main",
		BaseStartSHA: "abc123",
		Model:        "claude-opus-4-7",
		Provider:     "openrouter",
		Title:        &title,
	}
	if err := original.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(root, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != original.ID || loaded.WorkspaceID != original.WorkspaceID ||
		loaded.BaseBranch != original.BaseBranch || loaded.BaseStartSHA != original.BaseStartSHA ||
		loaded.Model != original.Model || loaded.Provider != original.Provider {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", loaded, original)
	}
	if loaded.Title == nil || *loaded.Title != title {
		t.Errorf("title roundtrip: got %v, want %q", loaded.Title, title)
	}
}
