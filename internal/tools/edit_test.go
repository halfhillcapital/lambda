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

func editArgs(path, oldStr, newStr string, replaceAll bool) string {
	b, _ := json.Marshal(EditArgs{Path: path, OldString: oldStr, NewString: newStr, ReplaceAll: replaceAll})
	return string(b)
}

func TestEdit(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	t.Run("basic replace", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		mustWrite(t, p, "hello world", 0o644)
		got := Edit.Execute(ctx, editArgs(p, "world", "lambda", false))
		if !strings.Contains(got, "edited") {
			t.Errorf("unexpected message: %q", got)
		}
		on, _ := os.ReadFile(p)
		if string(on) != "hello lambda" {
			t.Errorf("got %q, want %q", on, "hello lambda")
		}
	})

	t.Run("not found is execution error", func(t *testing.T) {
		p := filepath.Join(dir, "b.txt")
		mustWrite(t, p, "hello", 0o644)
		got := Edit.Execute(ctx, editArgs(p, "missing", "x", false))
		if !strings.HasPrefix(got, "error:") || !strings.Contains(got, "not found") {
			t.Errorf("got %q, want execution error with \"not found\"", got)
		}
	})

	t.Run("ambiguous match without replace_all preserves file", func(t *testing.T) {
		p := filepath.Join(dir, "c.txt")
		original := "foo foo foo"
		mustWrite(t, p, original, 0o644)
		got := Edit.Execute(ctx, editArgs(p, "foo", "bar", false))
		if !strings.HasPrefix(got, "error:") || !strings.Contains(got, "matches 3 times") {
			t.Errorf("got %q, want execution error with \"matches 3 times\"", got)
		}
		on, _ := os.ReadFile(p)
		if string(on) != original {
			t.Errorf("file modified despite error: got %q, want %q", on, original)
		}
	})

	t.Run("replace_all", func(t *testing.T) {
		p := filepath.Join(dir, "d.txt")
		mustWrite(t, p, "foo foo foo", 0o644)
		Edit.Execute(ctx, editArgs(p, "foo", "bar", true))
		on, _ := os.ReadFile(p)
		if string(on) != "bar bar bar" {
			t.Errorf("got %q, want %q", on, "bar bar bar")
		}
	})

	t.Run("missing args are schema errors", func(t *testing.T) {
		// empty path
		if got := Edit.Execute(ctx, editArgs("", "a", "b", false)); !strings.HasPrefix(got, "schema error:") {
			t.Errorf("empty path: got %q, want schema error", got)
		}
		// empty old_string
		p := filepath.Join(dir, "e.txt")
		mustWrite(t, p, "hi", 0o644)
		if got := Edit.Execute(ctx, editArgs(p, "", "b", false)); !strings.HasPrefix(got, "schema error:") {
			t.Errorf("empty old_string: got %q, want schema error", got)
		}
	})

	t.Run("preserves executable mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix file modes only")
		}
		p := filepath.Join(dir, "edit-script.sh")
		mustWrite(t, p, "#!/bin/sh\necho old", 0o755)
		Edit.Execute(ctx, editArgs(p, "old", "new", false))
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode changed from 0755 to %o", info.Mode().Perm())
		}
	})
}
