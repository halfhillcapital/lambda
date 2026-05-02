package prompt

import (
	"strings"
	"testing"
)

func TestBuild_OmitsProjectBlockWhenNoneLoaded(t *testing.T) {
	got := Build(t.TempDir(), nil, ProjectContext{})
	if strings.Contains(got, "Project instructions") {
		t.Errorf("expected no project block, got:\n%s", got)
	}
}

func TestBuild_IncludesProjectBlock(t *testing.T) {
	pc := ProjectContext{
		Path:         "/repo/AGENTS.md",
		Content:      "always run gofmt",
		OriginalSize: len("always run gofmt"),
	}
	got := Build(t.TempDir(), nil, pc)
	for _, want := range []string{
		"## Project instructions",
		"AGENTS.md",
		"/repo/AGENTS.md",
		"always run gofmt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q; full:\n%s", want, got)
		}
	}
}

func TestBuild_ProjectBlockNamesClaudeMdWhenFallback(t *testing.T) {
	pc := ProjectContext{
		Path:         "/repo/CLAUDE.md",
		Content:      "be terse",
		OriginalSize: 8,
	}
	got := Build(t.TempDir(), nil, pc)
	if !strings.Contains(got, "from CLAUDE.md at /repo/CLAUDE.md") {
		t.Errorf("expected CLAUDE.md naming, got:\n%s", got)
	}
}

func TestBuild_TruncationNote(t *testing.T) {
	pc := ProjectContext{
		Path:         "/repo/AGENTS.md",
		Content:      "head",
		OriginalSize: 12345,
		Truncated:    true,
	}
	got := Build(t.TempDir(), nil, pc)
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation note, got:\n%s", got)
	}
	if !strings.Contains(got, "12345") {
		t.Errorf("expected original size in truncation note, got:\n%s", got)
	}
}

func TestBuild_ProjectBlockBeforeSkillsBlock(t *testing.T) {
	pc := ProjectContext{Path: "/x/AGENTS.md", Content: "rules"}
	got := Build(t.TempDir(), nil, pc)
	envIdx := strings.Index(got, "</environment>")
	projIdx := strings.Index(got, "## Project instructions")
	if envIdx < 0 || projIdx < 0 {
		t.Fatalf("missing markers in:\n%s", got)
	}
	if projIdx < envIdx {
		t.Errorf("project block must come after environment block")
	}
}
