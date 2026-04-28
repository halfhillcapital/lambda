package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func globArgs(pattern, root string, max int) string {
	b, _ := json.Marshal(GlobArgs{Pattern: pattern, Path: root, MaxResults: max})
	return string(b)
}

func TestGlob(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	mustTree(t, root, map[string]string{
		"main.go":                 "",
		"README.md":               "",
		"cmd/lambda/main.go":      "",
		"cmd/lambda/oneshot.go":   "",
		"internal/agent/agent.go": "",
		"internal/tools/tools.go": "",
		".git/config":             "",
		"node_modules/lib/x.js":   "",
		"vendor/pkg/y.go":         "",
	})

	t.Run("basename match recursive", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("*.go", root, 0))
		for _, want := range []string{"main.go", "cmd/lambda/main.go", "internal/agent/agent.go"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in result:\n%s", want, got)
			}
		}
		if strings.Contains(got, "vendor/") {
			t.Errorf("should skip vendor/:\n%s", got)
		}
		if strings.Contains(got, ".git/") {
			t.Errorf("should skip .git/:\n%s", got)
		}
	})

	t.Run("doublestar full path", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("cmd/**/*.go", root, 0))
		if !strings.Contains(got, "cmd/lambda/main.go") {
			t.Errorf("missing cmd/lambda/main.go:\n%s", got)
		}
		if strings.Contains(got, "internal/") {
			t.Errorf("should not match outside cmd/:\n%s", got)
		}
	})

	t.Run("specific filename anywhere", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("tools.go", root, 0))
		if !strings.Contains(got, "internal/tools/tools.go") {
			t.Errorf("expected internal/tools/tools.go:\n%s", got)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("*.rs", root, 0))
		if got != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\"", got)
		}
	})

	t.Run("max_results truncation", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("*.go", root, 2))
		if !strings.Contains(got, "truncated") {
			t.Errorf("missing truncation notice:\n%s", got)
		}
	})

	t.Run("empty pattern is schema error", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("", root, 0))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("invalid pattern is schema error", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("[", root, 0))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("nonexistent root is execution error", func(t *testing.T) {
		got := Glob.Execute(ctx, globArgs("*.go", filepath.Join(root, "nope"), 0))
		if !strings.HasPrefix(got, "error:") || strings.HasPrefix(got, "schema error:") {
			t.Errorf("missing root should be execution error: %q", got)
		}
	})

	t.Run("ctx cancellation stops walk", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		got := Glob.Execute(cctx, globArgs("*.go", root, 0))
		if got != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\" after cancel", got)
		}
	})
}

func TestGlobRespectsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	mustTree(t, root, map[string]string{
		".gitignore":        "dist/\nbuild/\n__pycache__/\n*.pyc\n",
		"main.go":           "",
		"src/lib.go":        "",
		"dist/bundle.js":    "",
		"build/out.o":       "",
		"__pycache__/m.pyc": "",
		"cache.pyc":         "",
		"keep/README.md":    "",
	})
	mustGitInit(t, root)

	got := Glob.Execute(context.Background(), globArgs("**/*", root, 0))
	for _, want := range []string{"main.go", "src/lib.go", "keep/README.md", ".gitignore"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, unwant := range []string{"dist/", "build/", "__pycache__/", "cache.pyc"} {
		if strings.Contains(got, unwant) {
			t.Errorf("should exclude %q (gitignored):\n%s", unwant, got)
		}
	}
}
