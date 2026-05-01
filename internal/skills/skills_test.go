package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_basic(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "name: alpha\ndescription: Do alpha things", "alpha body")
	writeSkill(t, root, "beta", "name: beta\ndescription: Do beta things", "beta body")

	var warn bytes.Buffer
	idx := Load([]string{root}, &warn)

	if got := len(idx.List()); got != 2 {
		t.Fatalf("want 2 skills, got %d", got)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warnings: %s", warn.String())
	}
	s, ok := idx.Get("alpha")
	if !ok {
		t.Fatal("alpha missing")
	}
	if s.Description != "Do alpha things" {
		t.Errorf("description: got %q", s.Description)
	}
	body, err := s.Body()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Base directory for this skill:") || !strings.Contains(body, "alpha body") {
		t.Errorf("body wrong: %q", body)
	}
}

func TestLoad_projectOverridesUser(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "shared", "name: shared\ndescription: USER", "user")
	writeSkill(t, project, "shared", "name: shared\ndescription: PROJECT", "project")

	idx := Load([]string{project, user}, nil)
	s, ok := idx.Get("shared")
	if !ok {
		t.Fatal("shared missing")
	}
	if s.Description != "PROJECT" {
		t.Errorf("project should win, got %q", s.Description)
	}
}

func TestLoad_skipsMalformed(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "ok", "name: ok\ndescription: fine", "")
	writeSkill(t, root, "noname", "description: missing name", "")
	writeSkill(t, root, "mismatch", "name: other\ndescription: dir mismatch", "")

	// Skill dir exists but no SKILL.md → silently skipped.
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	idx := Load([]string{root}, &warn)

	names := idx.Names()
	if len(names) != 1 || names[0] != "ok" {
		t.Errorf("want only [ok], got %v", names)
	}
	w := warn.String()
	if !strings.Contains(w, "noname") || !strings.Contains(w, "mismatch") {
		t.Errorf("expected warnings for noname and mismatch, got: %s", w)
	}
}

func TestLoad_allowedToolsWarnsOnce(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "a", "name: a\ndescription: a\nallowed-tools: read,write", "")
	writeSkill(t, root, "b", "name: b\ndescription: b\nallowed-tools: bash", "")

	var warn bytes.Buffer
	Load([]string{root}, &warn)
	count := strings.Count(warn.String(), "allowed-tools")
	if count != 1 {
		t.Errorf("want exactly 1 allowed-tools warning, got %d (%q)", count, warn.String())
	}
}

func TestLoad_missingRoot(t *testing.T) {
	idx := Load([]string{filepath.Join(t.TempDir(), "does-not-exist")}, nil)
	if len(idx.List()) != 0 {
		t.Errorf("want 0 skills, got %d", len(idx.List()))
	}
}

func TestRootsFromEnv(t *testing.T) {
	cwd := t.TempDir()
	defaults := DefaultRoots(cwd)

	if got := RootsFromEnv(cwd, ""); len(got) != len(defaults) {
		t.Errorf("empty env: want defaults, got %v", got)
	}
	got := RootsFromEnv(cwd, "/foo, /bar")
	if len(got) < 2 || got[0] != "/foo" || got[1] != "/bar" {
		t.Errorf("env extras should prepend, got %v", got)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	fm, body := splitFrontmatter("---\nname: x\ndescription: y\n---\nhello")
	if fm["name"] != "x" || fm["description"] != "y" {
		t.Errorf("fm wrong: %v", fm)
	}
	if body != "hello" {
		t.Errorf("body: %q", body)
	}

	fm, body = splitFrontmatter("no frontmatter here")
	if fm != nil {
		t.Errorf("want nil fm, got %v", fm)
	}
	if body != "no frontmatter here" {
		t.Errorf("body should be raw")
	}
}
