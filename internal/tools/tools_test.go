package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsDestructive(t *testing.T) {
	cases := []struct {
		name Name
		want bool
	}{
		{ReadFile, false},
		{ListDir, false},
		{WriteFile, true},
		{EditFile, true},
		{Bash, true},
	}
	for _, c := range cases {
		if got := c.name.IsDestructive(); got != c.want {
			t.Errorf("%s: IsDestructive() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSchemas(t *testing.T) {
	want := map[string]bool{
		string(ReadFile): true, string(WriteFile): true, string(EditFile): true,
		string(ListDir): true, string(Grep): true, string(Glob): true, string(Bash): true,
	}
	got := Schemas()
	if len(got) != len(want) {
		t.Fatalf("Schemas() returned %d tools, want %d", len(got), len(want))
	}
	for _, s := range got {
		delete(want, s.Function.Name)
	}
	if len(want) != 0 {
		t.Errorf("Schemas() missing tools: %v", want)
	}
}

func TestDoReadFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("basic", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		mustWrite(t, p, "hello", 0o644)
		s, err := doReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if s != "hello" {
			t.Errorf("got %q, want %q", s, "hello")
		}
	})

	t.Run("missing path arg", func(t *testing.T) {
		if _, err := doReadFile(""); err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		if _, err := doReadFile(filepath.Join(dir, "nope")); err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("under cap returns full content", func(t *testing.T) {
		p := filepath.Join(dir, "small.txt")
		content := strings.Repeat("x", readFileMaxBytes-100)
		mustWrite(t, p, content, 0o644)
		s, err := doReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if s != content {
			t.Errorf("content modified: got %d bytes, want %d", len(s), len(content))
		}
	})

	t.Run("truncates large file with notice", func(t *testing.T) {
		p := filepath.Join(dir, "big.txt")
		var sb strings.Builder
		line := strings.Repeat("x", 100) + "\n"
		for sb.Len() < readFileMaxBytes+10*1024 {
			sb.WriteString(line)
		}
		full := sb.String()
		mustWrite(t, p, full, 0o644)

		s, err := doReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "truncated to first") {
			t.Errorf("missing truncation notice; tail: %q", s[max(0, len(s)-200):])
		}
		if !strings.Contains(s, "file is ") {
			t.Error("missing original size in notice")
		}
		// Result should not exceed cap by more than the notice suffix.
		if len(s) > readFileMaxBytes+200 {
			t.Errorf("result %d bytes exceeds cap %d + slack", len(s), readFileMaxBytes)
		}
	})
}

func TestDoWriteFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("creates new file", func(t *testing.T) {
		p := filepath.Join(dir, "new.txt")
		msg, err := doWriteFile(p, "hi")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "wrote") {
			t.Errorf("unexpected message: %q", msg)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "hi" {
			t.Errorf("file content = %q, want %q", got, "hi")
		}
	})

	t.Run("overwrites existing", func(t *testing.T) {
		p := filepath.Join(dir, "exist.txt")
		mustWrite(t, p, "old", 0o644)
		if _, err := doWriteFile(p, "new"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "new" {
			t.Errorf("got %q, want %q", got, "new")
		}
	})

	t.Run("creates parent dirs", func(t *testing.T) {
		p := filepath.Join(dir, "sub", "deep", "x.txt")
		if _, err := doWriteFile(p, "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file not created: %v", err)
		}
	})

	t.Run("missing path arg", func(t *testing.T) {
		if _, err := doWriteFile("", "x"); err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("preserves executable mode on overwrite", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix file modes only")
		}
		p := filepath.Join(dir, "script.sh")
		mustWrite(t, p, "#!/bin/sh\necho old", 0o755)
		if _, err := doWriteFile(p, "#!/bin/sh\necho new"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode changed from 0755 to %o", info.Mode().Perm())
		}
	})
}

func TestDoEditFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("basic replace", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		mustWrite(t, p, "hello world", 0o644)
		msg, err := doEditFile(p, "world", "lambda", false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "edited") {
			t.Errorf("unexpected message: %q", msg)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "hello lambda" {
			t.Errorf("got %q, want %q", got, "hello lambda")
		}
	})

	t.Run("not found", func(t *testing.T) {
		p := filepath.Join(dir, "b.txt")
		mustWrite(t, p, "hello", 0o644)
		_, err := doEditFile(p, "missing", "x", false)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("got err=%v, want \"not found\"", err)
		}
	})

	t.Run("ambiguous match without replace_all preserves file", func(t *testing.T) {
		p := filepath.Join(dir, "c.txt")
		original := "foo foo foo"
		mustWrite(t, p, original, 0o644)
		_, err := doEditFile(p, "foo", "bar", false)
		if err == nil || !strings.Contains(err.Error(), "matches 3 times") {
			t.Errorf("got err=%v, want \"matches 3 times\"", err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != original {
			t.Errorf("file modified despite error: got %q, want %q", got, original)
		}
	})

	t.Run("replace_all", func(t *testing.T) {
		p := filepath.Join(dir, "d.txt")
		mustWrite(t, p, "foo foo foo", 0o644)
		if _, err := doEditFile(p, "foo", "bar", true); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "bar bar bar" {
			t.Errorf("got %q, want %q", got, "bar bar bar")
		}
	})

	t.Run("missing args", func(t *testing.T) {
		if _, err := doEditFile("", "a", "b", false); err == nil {
			t.Error("expected error for empty path")
		}
		p := filepath.Join(dir, "e.txt")
		mustWrite(t, p, "hi", 0o644)
		if _, err := doEditFile(p, "", "b", false); err == nil {
			t.Error("expected error for empty old_string")
		}
	})

	t.Run("preserves executable mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix file modes only")
		}
		p := filepath.Join(dir, "edit-script.sh")
		mustWrite(t, p, "#!/bin/sh\necho old", 0o755)
		if _, err := doEditFile(p, "old", "new", false); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode changed from 0755 to %o", info.Mode().Perm())
		}
	})
}

func TestDoListDir(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "subdir"), 0o755)
	mustWrite(t, filepath.Join(dir, "b.txt"), "", 0o644)
	mustWrite(t, filepath.Join(dir, "a.txt"), "", 0o644)

	t.Run("sorted with dir suffix", func(t *testing.T) {
		s, err := doListDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := "a.txt\nb.txt\nsubdir/"
		if s != want {
			t.Errorf("got %q, want %q", s, want)
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		s, err := doListDir(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if s != "(empty directory)" {
			t.Errorf("got %q", s)
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		if _, err := doListDir(filepath.Join(dir, "nope")); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("default to current dir", func(t *testing.T) {
		s, err := doListDir("")
		if err != nil {
			t.Fatal(err)
		}
		if s == "" {
			t.Error("expected non-empty listing of cwd")
		}
	})
}

func TestTruncateOutput(t *testing.T) {
	t.Run("under both limits unchanged", func(t *testing.T) {
		s := "a\nb\nc"
		if got := truncateOutput(s); got != s {
			t.Errorf("modified short input: got %q", got)
		}
	})

	t.Run("line limit", func(t *testing.T) {
		var sb strings.Builder
		for range bashOutputMaxLines + 50 {
			sb.WriteString("line\n")
		}
		got := truncateOutput(sb.String())
		if !strings.Contains(got, "more lines truncated") {
			t.Errorf("missing line-truncation notice; tail: %q", got[max(0, len(got)-200):])
		}
	})

	t.Run("byte limit", func(t *testing.T) {
		s := strings.Repeat("a", bashOutputMaxBytes+1024) + "\nb"
		got := truncateOutput(s)
		if !strings.Contains(got, "truncated") {
			t.Error("missing byte-truncation notice")
		}
		if len(got) > bashOutputMaxBytes+200 {
			t.Errorf("result %d bytes exceeds cap %d + slack", len(got), bashOutputMaxBytes)
		}
	})
}

func TestDecodeArgs(t *testing.T) {
	var dst struct {
		X int `json:"x"`
	}

	if err := decodeArgs("", &dst); err != nil {
		t.Errorf("empty input should default to {}: %v", err)
	}
	if err := decodeArgs(`{"x":5}`, &dst); err != nil {
		t.Fatal(err)
	}
	if dst.X != 5 {
		t.Errorf("got X=%d, want 5", dst.X)
	}
	if err := decodeArgs("not json", &dst); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	got := Execute(context.Background(), "no_such_tool", "{}")
	if !strings.HasPrefix(got, "schema error:") {
		t.Errorf("unknown tool should be a schema error; got %q", got)
	}
	if !strings.Contains(got, "unknown tool") {
		t.Errorf("got %q, want substring \"unknown tool\"", got)
	}
}

func TestExecuteSchemaVsExecutionError(t *testing.T) {
	t.Run("malformed JSON is schema error", func(t *testing.T) {
		got := Execute(context.Background(), string(ReadFile), "not json")
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("malformed JSON should be \"schema error:\"; got %q", got)
		}
	})

	t.Run("missing file is execution error", func(t *testing.T) {
		got := Execute(context.Background(), string(ReadFile), `{"path":"/definitely/does/not/exist/zzz"}`)
		if !strings.HasPrefix(got, "error:") || strings.HasPrefix(got, "schema error:") {
			t.Errorf("missing file should be \"error:\" not \"schema error:\"; got %q", got)
		}
	})
}

func TestDoBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}

	t.Run("basic stdout", func(t *testing.T) {
		s, err := doBash(context.Background(), "echo hello", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "hello") {
			t.Errorf("got %q", s)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		s, err := doBash(context.Background(), "exit 7", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "[exit 7]") {
			t.Errorf("got %q", s)
		}
	})

	t.Run("empty command rejected", func(t *testing.T) {
		if _, err := doBash(context.Background(), "  ", 0); err == nil {
			t.Error("expected error for empty command")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		s, err := doBash(context.Background(), "sleep 5", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "timed out") {
			t.Errorf("got %q", s)
		}
	})

	t.Run("ctx cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(150 * time.Millisecond)
			cancel()
		}()
		s, err := doBash(ctx, "sleep 5", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "cancelled") {
			t.Errorf("got %q", s)
		}
	})

	t.Run("combined output", func(t *testing.T) {
		s, err := doBash(context.Background(), "echo out; echo err 1>&2", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "out") || !strings.Contains(s, "err") {
			t.Errorf("expected both stdout and stderr in output: %q", s)
		}
	})
}

// --- helpers ---

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
}
