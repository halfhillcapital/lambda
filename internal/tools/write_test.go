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
