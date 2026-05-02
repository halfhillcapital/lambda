package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeArgs(path, content string) string {
	b, _ := json.Marshal(WriteArgs{Path: path, Content: content})
	return string(b)
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	t.Run("creates new file", func(t *testing.T) {
		p := filepath.Join(dir, "new.txt")
		got := Write.Execute(ctx, writeArgs(p, "hi"))
		if !strings.Contains(got, "wrote") {
			t.Errorf("unexpected message: %q", got)
		}
		on, _ := os.ReadFile(p)
		if string(on) != "hi" {
			t.Errorf("file content = %q, want %q", on, "hi")
		}
	})

	t.Run("overwrites existing", func(t *testing.T) {
		p := filepath.Join(dir, "exist.txt")
		mustWrite(t, p, "old", 0o644)
		Write.Execute(ctx, writeArgs(p, "new"))
		on, _ := os.ReadFile(p)
		if string(on) != "new" {
			t.Errorf("got %q, want %q", on, "new")
		}
	})

	t.Run("creates parent dirs", func(t *testing.T) {
		p := filepath.Join(dir, "sub", "deep", "x.txt")
		Write.Execute(ctx, writeArgs(p, "x"))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file not created: %v", err)
		}
	})

	t.Run("missing path arg is schema error", func(t *testing.T) {
		got := Write.Execute(ctx, `{"content":"hi"}`)
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("expected schema error; got %q", got)
		}
	})

	t.Run("preserves executable mode on overwrite", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix file modes only")
		}
		p := filepath.Join(dir, "script.sh")
		mustWrite(t, p, "#!/bin/sh\necho old", 0o755)
		Write.Execute(ctx, writeArgs(p, "#!/bin/sh\necho new"))
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode changed from 0755 to %o", info.Mode().Perm())
		}
	})
}

// --- safety tests ---

func writeClassifyArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteClassify_InsideRootAutoAllow(t *testing.T) {
	root := t.TempDir()
	w := NewWrite(root)
	e := NewEdit(root)
	cases := []string{
		"foo.go",
		"sub/bar.go",
		filepath.Join(root, "absolute.go"),
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if got := w.Classify(writeClassifyArgs(t, p)); got != AutoAllow {
				t.Errorf("write %q: got %v, want AutoAllow", p, got)
			}
			if got := e.Classify(writeClassifyArgs(t, p)); got != AutoAllow {
				t.Errorf("edit %q: got %v, want AutoAllow", p, got)
			}
		})
	}
}

func TestWriteClassify_OutsideRootPrompt(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root), "x.go")
	w := NewWrite(root)
	cases := []string{
		"../outside.go",
		"../../way/outside.go",
		outside,
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if got := w.Classify(writeClassifyArgs(t, p)); got != Prompt {
				t.Errorf("write %q: got %v, want Prompt", p, got)
			}
		})
	}
}

func TestWriteClassify_BadArgsPrompt(t *testing.T) {
	w := NewWrite(t.TempDir())
	cases := []string{
		"",            // not JSON → empty defaults to {}, no path
		"{}",          // no path
		`{"path":""}`, // empty path
		"not json",
	}
	for _, a := range cases {
		t.Run(a, func(t *testing.T) {
			if got := w.Classify(a); got != Prompt {
				t.Errorf("args=%q: got %v, want Prompt", a, got)
			}
		})
	}
}

// TestWriteClassify_NoRootPrompt pins the zero-root singleton's behaviour:
// without a session root, every path classifies as Prompt — the conservative
// default.
func TestWriteClassify_NoRootPrompt(t *testing.T) {
	if got := Write.Classify(writeClassifyArgs(t, "/anywhere/foo.go")); got != Prompt {
		t.Errorf("zero-root Write: got %v, want Prompt", got)
	}
	if got := Edit.Classify(writeClassifyArgs(t, "anywhere.go")); got != Prompt {
		t.Errorf("zero-root Edit: got %v, want Prompt", got)
	}
}

func TestIsUnder(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("some", "root")
	cases := []struct {
		target string
		want   bool
	}{
		{filepath.Join(root, "a.go"), true},
		{filepath.Join(root, "sub", "a.go"), true},
		{root, true},
		{filepath.Join(string(filepath.Separator), "some", "root2", "a.go"), false},
		{filepath.Join(string(filepath.Separator), "elsewhere"), false},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			if got := isUnder(c.target, root); got != c.want {
				t.Errorf("isUnder(%q, %q) = %v, want %v", c.target, root, got, c.want)
			}
		})
	}
}
