package tools

import (
	"os"
	"os/exec"
	"path/filepath"
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

// --- shared test helpers used by glob_test.go and grep_test.go ---

func mustGitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

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
