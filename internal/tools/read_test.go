package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// readArgs builds a JSON args string for Read.Execute.
func readArgs(path string) string {
	b, _ := json.Marshal(ReadArgs{Path: path})
	return string(b)
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	t.Run("basic", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		mustWrite(t, p, "hello", 0o644)
		got := Read.Execute(ctx, readArgs(p))
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("missing path arg is schema error", func(t *testing.T) {
		got := Read.Execute(ctx, `{}`)
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("expected schema error; got %q", got)
		}
	})

	t.Run("file not found is execution error", func(t *testing.T) {
		got := Read.Execute(ctx, readArgs(filepath.Join(dir, "nope")))
		if !strings.HasPrefix(got, "error:") || strings.HasPrefix(got, "schema error:") {
			t.Errorf("expected execution error; got %q", got)
		}
	})

	t.Run("under cap returns full content", func(t *testing.T) {
		p := filepath.Join(dir, "small.txt")
		content := strings.Repeat("x", readFileMaxBytes-100)
		mustWrite(t, p, content, 0o644)
		got := Read.Execute(ctx, readArgs(p))
		if got != content {
			t.Errorf("content modified: got %d bytes, want %d", len(got), len(content))
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

		got := Read.Execute(ctx, readArgs(p))
		if !strings.Contains(got, "truncated to first") {
			t.Errorf("missing truncation notice; tail: %q", got[max(0, len(got)-200):])
		}
		if !strings.Contains(got, "file is ") {
			t.Error("missing original size in notice")
		}
		if len(got) > readFileMaxBytes+200 {
			t.Errorf("result %d bytes exceeds cap %d + slack", len(got), readFileMaxBytes)
		}
	})
}
