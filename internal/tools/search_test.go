package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// Basename match (no '/' in pattern) — recursive.
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", true},
		{"*.go", "a/b/c/main.go", true},
		{"main.go", "cmd/main.go", true},
		{"*.go", "main.txt", false},
		{"config.go", "internal/config/config.go", true},

		// Full-path match (pattern contains '/').
		{"cmd/*.go", "cmd/main.go", true},
		{"cmd/*.go", "cmd/x/main.go", false},
		{"**/*.go", "main.go", true},
		{"**/*.go", "cmd/main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"cmd/**/*.go", "cmd/main.go", true},
		{"cmd/**/*.go", "cmd/lambda/main.go", true},
		{"cmd/**/*.go", "internal/main.go", false},
		{"**", "anything", true},
		{"**", "a/b/c", true},
		{"a/**", "a/b/c", true},
		{"a/**", "b/c", false},

		// Single-char and char class — inherited from path.Match.
		{"file?.go", "file1.go", true},
		{"file?.go", "file12.go", false},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
	}
	for _, c := range cases {
		t.Run(c.pattern+" vs "+c.path, func(t *testing.T) {
			got, err := matchPath(c.pattern, c.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("matchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
			}
		})
	}
}

func TestMatchPathInvalidPattern(t *testing.T) {
	if _, err := matchPath("[", "x"); err == nil {
		t.Error("expected error for malformed pattern")
	}
}

func TestDoGlob(t *testing.T) {
	root := t.TempDir()
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
		s, err := doGlob(context.Background(), "*.go", root, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Should find all .go files except those in skip dirs.
		for _, want := range []string{"main.go", "cmd/lambda/main.go", "internal/agent/agent.go"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in result:\n%s", want, s)
			}
		}
		if strings.Contains(s, "vendor/") {
			t.Errorf("should skip vendor/:\n%s", s)
		}
		if strings.Contains(s, ".git/") {
			t.Errorf("should skip .git/:\n%s", s)
		}
	})

	t.Run("doublestar full path", func(t *testing.T) {
		s, err := doGlob(context.Background(), "cmd/**/*.go", root, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "cmd/lambda/main.go") {
			t.Errorf("missing cmd/lambda/main.go:\n%s", s)
		}
		if strings.Contains(s, "internal/") {
			t.Errorf("should not match outside cmd/:\n%s", s)
		}
	})

	t.Run("specific filename anywhere", func(t *testing.T) {
		s, err := doGlob(context.Background(), "tools.go", root, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "internal/tools/tools.go") {
			t.Errorf("expected internal/tools/tools.go:\n%s", s)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		s, err := doGlob(context.Background(), "*.rs", root, 0)
		if err != nil {
			t.Fatal(err)
		}
		if s != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\"", s)
		}
	})

	t.Run("max_results truncation", func(t *testing.T) {
		s, err := doGlob(context.Background(), "*.go", root, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "truncated") {
			t.Errorf("missing truncation notice:\n%s", s)
		}
	})

	t.Run("empty pattern is schema error", func(t *testing.T) {
		_, err := doGlob(context.Background(), "", root, 0)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("got err=%v, want schemaError", err)
		}
	})

	t.Run("invalid pattern is schema error", func(t *testing.T) {
		_, err := doGlob(context.Background(), "[", root, 0)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("got err=%v, want schemaError", err)
		}
	})

	t.Run("nonexistent root is execution error", func(t *testing.T) {
		_, err := doGlob(context.Background(), "*.go", filepath.Join(root, "nope"), 0)
		if err == nil {
			t.Error("expected error for missing root")
		}
		var se *schemaError
		if errors.As(err, &se) {
			t.Errorf("missing root should not be a schema error: %v", err)
		}
	})

	t.Run("ctx cancellation stops walk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s, err := doGlob(ctx, "*.go", root, 0)
		if err != nil {
			t.Fatal(err)
		}
		// With cancelled ctx, walk yields nothing.
		if s != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\" after cancel", s)
		}
	})
}

func TestDoGrep(t *testing.T) {
	root := t.TempDir()
	mustTree(t, root, map[string]string{
		"a.go":              "package main\nfunc Hello() {}\nfunc World() {}\n",
		"b.go":              "package main\nfunc Goodbye() {}\n",
		"sub/c.go":          "package sub\nfunc HELLO() {}\n",
		"README.md":         "# Hello\n",
		"vendor/skip.go":    "func ShouldSkip() {}\n",
		"node_modules/x.js": "function Hello() {}\n",
	})
	// A binary file with null bytes.
	mustWriteBytes(t, filepath.Join(root, "bin"), []byte{0x00, 0x01, 'H', 'e', 'l', 'l', 'o', 0x00})

	t.Run("basic regex match", func(t *testing.T) {
		s, err := doGrep(context.Background(), "Hello", root, "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "a.go:2:func Hello() {}") {
			t.Errorf("missing a.go match:\n%s", s)
		}
		if !strings.Contains(s, "README.md:1:# Hello") {
			t.Errorf("missing README match:\n%s", s)
		}
	})

	t.Run("skips skipDirs and binary", func(t *testing.T) {
		s, err := doGrep(context.Background(), "Hello", root, "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s, "vendor/") {
			t.Errorf("should skip vendor/:\n%s", s)
		}
		if strings.Contains(s, "node_modules/") {
			t.Errorf("should skip node_modules/:\n%s", s)
		}
		if strings.Contains(s, "bin:") {
			t.Errorf("should skip binary file:\n%s", s)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		s, err := doGrep(context.Background(), "hello", root, "*.go", 0, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "a.go:2") {
			t.Errorf("missing a.go match:\n%s", s)
		}
		if !strings.Contains(s, "sub/c.go:2") {
			t.Errorf("missing sub/c.go HELLO match:\n%s", s)
		}
	})

	t.Run("glob filter (basename)", func(t *testing.T) {
		s, err := doGrep(context.Background(), "Hello", root, "*.md", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "README.md") {
			t.Errorf("missing README match:\n%s", s)
		}
		if strings.Contains(s, ".go") {
			t.Errorf("should not match .go files:\n%s", s)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		s, err := doGrep(context.Background(), "ZZZNOPE", root, "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if s != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\"", s)
		}
	})

	t.Run("max_results truncation", func(t *testing.T) {
		s, err := doGrep(context.Background(), "func", root, "", 2, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "truncated") {
			t.Errorf("missing truncation notice:\n%s", s)
		}
	})

	t.Run("empty pattern is schema error", func(t *testing.T) {
		_, err := doGrep(context.Background(), "", root, "", 0, false)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("got err=%v, want schemaError", err)
		}
	})

	t.Run("invalid regex is schema error", func(t *testing.T) {
		_, err := doGrep(context.Background(), "(unclosed", root, "", 0, false)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("got err=%v, want schemaError", err)
		}
	})

	t.Run("invalid glob is schema error", func(t *testing.T) {
		_, err := doGrep(context.Background(), "x", root, "[", 0, false)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("got err=%v, want schemaError", err)
		}
	})

	t.Run("long line is truncated", func(t *testing.T) {
		dir := t.TempDir()
		long := strings.Repeat("x", grepMaxLineLen+200) + " MARKER"
		mustWriteBytes(t, filepath.Join(dir, "big.txt"), []byte(long))
		s, err := doGrep(context.Background(), "MARKER", dir, "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		// MARKER is past the truncation point, but we still match because
		// regexp scans the full line; the displayed line is truncated.
		if !strings.Contains(s, "…") {
			t.Errorf("expected truncation marker in output:\n%s", s)
		}
	})
}

func TestIsBinary(t *testing.T) {
	if !isBinary([]byte{'a', 0x00, 'b'}) {
		t.Error("should detect null byte")
	}
	if isBinary([]byte("hello world\n")) {
		t.Error("text is not binary")
	}
	if isBinary(nil) {
		t.Error("empty input is not binary")
	}
}

// --- helpers ---

func mustTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustWriteBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
