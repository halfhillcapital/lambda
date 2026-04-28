package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func grepArgs(pattern, root, glob string, max int, ci bool) string {
	b, _ := json.Marshal(GrepArgs{Pattern: pattern, Path: root, Glob: glob, MaxResults: max, CaseInsensitive: ci})
	return string(b)
}

func TestGrep(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	mustTree(t, root, map[string]string{
		"a.go":              "package main\nfunc Hello() {}\nfunc World() {}\n",
		"b.go":              "package main\nfunc Goodbye() {}\n",
		"sub/c.go":          "package sub\nfunc HELLO() {}\n",
		"README.md":         "# Hello\n",
		"vendor/skip.go":    "func ShouldSkip() {}\n",
		"node_modules/x.js": "function Hello() {}\n",
	})
	mustWriteBytes(t, filepath.Join(root, "bin"), []byte{0x00, 0x01, 'H', 'e', 'l', 'l', 'o', 0x00})

	t.Run("basic regex match", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("Hello", root, "", 0, false))
		if !strings.Contains(got, "a.go:2:func Hello() {}") {
			t.Errorf("missing a.go match:\n%s", got)
		}
		if !strings.Contains(got, "README.md:1:# Hello") {
			t.Errorf("missing README match:\n%s", got)
		}
	})

	t.Run("skips skipDirs and binary", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("Hello", root, "", 0, false))
		if strings.Contains(got, "vendor/") {
			t.Errorf("should skip vendor/:\n%s", got)
		}
		if strings.Contains(got, "node_modules/") {
			t.Errorf("should skip node_modules/:\n%s", got)
		}
		if strings.Contains(got, "bin:") {
			t.Errorf("should skip binary file:\n%s", got)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("hello", root, "*.go", 0, true))
		if !strings.Contains(got, "a.go:2") {
			t.Errorf("missing a.go match:\n%s", got)
		}
		if !strings.Contains(got, "sub/c.go:2") {
			t.Errorf("missing sub/c.go HELLO match:\n%s", got)
		}
	})

	t.Run("glob filter (basename)", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("Hello", root, "*.md", 0, false))
		if !strings.Contains(got, "README.md") {
			t.Errorf("missing README match:\n%s", got)
		}
		if strings.Contains(got, ".go") {
			t.Errorf("should not match .go files:\n%s", got)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("ZZZNOPE", root, "", 0, false))
		if got != "(no matches)" {
			t.Errorf("got %q, want \"(no matches)\"", got)
		}
	})

	t.Run("max_results truncation", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("func", root, "", 2, false))
		if !strings.Contains(got, "truncated") {
			t.Errorf("missing truncation notice:\n%s", got)
		}
	})

	t.Run("empty pattern is schema error", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("", root, "", 0, false))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("invalid regex is schema error", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("(unclosed", root, "", 0, false))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("invalid glob is schema error", func(t *testing.T) {
		got := Grep.Execute(ctx, grepArgs("x", root, "[", 0, false))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("long line is truncated", func(t *testing.T) {
		dir := t.TempDir()
		long := strings.Repeat("x", grepMaxLineLen+200) + " MARKER"
		mustWriteBytes(t, filepath.Join(dir, "big.txt"), []byte(long))
		got := Grep.Execute(ctx, grepArgs("MARKER", dir, "", 0, false))
		if !strings.Contains(got, "…") {
			t.Errorf("expected truncation marker in output:\n%s", got)
		}
	})
}

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"null byte", []byte{'a', 0x00, 'b'}, true},
		{"plain text", []byte("hello world\n"), false},
		{"empty", nil, false},
		{"ELF", []byte{0x7F, 'E', 'L', 'F', 0x02, 0x01}, true},
		{"PE/MZ", []byte{'M', 'Z', 0x90, 0x00}, true},
		{"ZIP", []byte{'P', 'K', 0x03, 0x04}, true},
		{"PNG", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}, true},
		{"UTF-16 LE BOM", []byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00}, true},
		{"UTF-16 BE BOM", []byte{0xFE, 0xFF, 0x00, 'h', 0x00, 'i'}, true},
		{"UTF-8 BOM followed by text", []byte{0xEF, 0xBB, 0xBF, 'h', 'i'}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBinary(c.in); got != c.want {
				t.Errorf("isBinary(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestGrepRespectsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	mustTree(t, root, map[string]string{
		".gitignore":     "dist/\ntarget/\n",
		"src/a.go":       "package main\nfunc Hello() {}\n",
		"dist/bundle.js": "function Hello() {}\n",
		"target/out.rs":  "fn Hello() {}\n",
	})
	mustGitInit(t, root)

	got := Grep.Execute(context.Background(), grepArgs("Hello", root, "", 0, false))
	if !strings.Contains(got, "src/a.go") {
		t.Errorf("missing src/a.go match:\n%s", got)
	}
	if strings.Contains(got, "dist/") {
		t.Errorf("should skip dist/ (gitignored):\n%s", got)
	}
	if strings.Contains(got, "target/") {
		t.Errorf("should skip target/ (gitignored):\n%s", got)
	}
}
