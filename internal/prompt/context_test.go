package prompt

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRepo builds a directory tree under t.TempDir(). Each entry is a path
// relative to the root; values ending in "/" become directories, the rest are
// files with the given content. A path of ".git" creates an empty marker dir
// so discover() recognizes the root.
func makeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoad_AgentsMdAtRepoRoot(t *testing.T) {
	root := makeRepo(t, map[string]string{
		".git/":      "",
		"AGENTS.md":  "project rules",
		"sub/file.go": "package sub",
	})
	got := LoadProjectContext(filepath.Join(root, "sub"), nil)
	if got.None() {
		t.Fatalf("expected to load AGENTS.md, got none")
	}
	if got.Content != "project rules" {
		t.Errorf("content = %q, want %q", got.Content, "project rules")
	}
	if filepath.Base(got.Path) != "AGENTS.md" {
		t.Errorf("path = %s, want AGENTS.md", got.Path)
	}
}

func TestLoad_PrefersAgentsMdOverClaudeMdAtSameLevel(t *testing.T) {
	root := makeRepo(t, map[string]string{
		".git/":     "",
		"AGENTS.md": "from agents",
		"CLAUDE.md": "from claude",
	})
	got := LoadProjectContext(root, nil)
	if got.Content != "from agents" {
		t.Errorf("content = %q, want %q", got.Content, "from agents")
	}
}

func TestLoad_FallsBackToClaudeMd(t *testing.T) {
	root := makeRepo(t, map[string]string{
		".git/":     "",
		"CLAUDE.md": "from claude",
	})
	got := LoadProjectContext(root, nil)
	if got.Content != "from claude" {
		t.Errorf("content = %q, want %q", got.Content, "from claude")
	}
	if filepath.Base(got.Path) != "CLAUDE.md" {
		t.Errorf("path = %s, want CLAUDE.md", got.Path)
	}
}

func TestLoad_NearestWinsAcrossLevels(t *testing.T) {
	// CLAUDE.md in subdir should beat AGENTS.md at the repo root because
	// per-level resolution stops walking on the first match.
	root := makeRepo(t, map[string]string{
		".git/":         "",
		"AGENTS.md":     "root agents",
		"sub/CLAUDE.md": "sub claude",
	})
	got := LoadProjectContext(filepath.Join(root, "sub"), nil)
	if got.Content != "sub claude" {
		t.Errorf("content = %q, want %q (nearest-wins)", got.Content, "sub claude")
	}
}

func TestLoad_StopsAtGitRoot(t *testing.T) {
	// A file outside the .git boundary must not be picked up.
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := LoadProjectContext(repo, nil)
	if !got.None() {
		t.Errorf("expected no load, got %s with %q", got.Path, got.Content)
	}
}

func TestLoad_NoGitAncestorChecksOnlyCwd(t *testing.T) {
	// Outside any git repo, we must NOT walk up — only cwd is inspected.
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("ancestor"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(parent, "child")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	got := LoadProjectContext(cwd, nil)
	if !got.None() {
		t.Errorf("expected no load (no .git, walk disabled), got %s", got.Path)
	}
}

func TestLoad_NoGitButCwdHasFile(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadProjectContext(cwd, nil)
	if got.Content != "here" {
		t.Errorf("expected to load cwd's AGENTS.md even without .git, got %q", got.Content)
	}
}

func TestLoad_TruncatesAtCap(t *testing.T) {
	big := strings.Repeat("x", projectContextMaxBytes+500)
	root := makeRepo(t, map[string]string{
		".git/":     "",
		"AGENTS.md": big,
	})
	var warn bytes.Buffer
	got := LoadProjectContext(root, &warn)
	if !got.Truncated {
		t.Errorf("expected truncation flag")
	}
	if len(got.Content) != projectContextMaxBytes {
		t.Errorf("content len = %d, want %d", len(got.Content), projectContextMaxBytes)
	}
	if got.OriginalSize != projectContextMaxBytes+500 {
		t.Errorf("original size = %d, want %d", got.OriginalSize, projectContextMaxBytes+500)
	}
	if !strings.Contains(warn.String(), "truncated from") {
		t.Errorf("warn output missing truncation note: %q", warn.String())
	}
}

func TestLoad_EmptyFileSilent(t *testing.T) {
	root := makeRepo(t, map[string]string{
		".git/":     "",
		"AGENTS.md": "",
	})
	var warn bytes.Buffer
	got := LoadProjectContext(root, &warn)
	if !got.None() {
		t.Errorf("expected no load for empty file")
	}
	if warn.Len() != 0 {
		t.Errorf("expected silent skip for empty file, got warn=%q", warn.String())
	}
}

func TestLoad_InvalidUTF8Warns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte{0xff, 0xfe, 0xfd}, 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	got := LoadProjectContext(root, &warn)
	if !got.None() {
		t.Errorf("expected no load for invalid UTF-8")
	}
	if !strings.Contains(warn.String(), "not valid UTF-8") {
		t.Errorf("warn output missing UTF-8 note: %q", warn.String())
	}
}

func TestLoad_SuccessLineWritesToWarn(t *testing.T) {
	root := makeRepo(t, map[string]string{
		".git/":     "",
		"AGENTS.md": "hi",
	})
	var warn bytes.Buffer
	LoadProjectContext(root, &warn)
	if !strings.Contains(warn.String(), "loaded project context from") {
		t.Errorf("expected success line in warn, got %q", warn.String())
	}
}
