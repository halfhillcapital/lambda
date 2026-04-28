package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func bashArgs(cmd string, timeoutSec int) string {
	b, _ := json.Marshal(BashArgs{Command: cmd, TimeoutSeconds: timeoutSec})
	return string(b)
}

func TestBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	ctx := context.Background()

	t.Run("basic stdout", func(t *testing.T) {
		got := Bash.Execute(ctx, bashArgs("echo hello", 0))
		if !strings.Contains(got, "hello") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		got := Bash.Execute(ctx, bashArgs("exit 7", 0))
		if !strings.Contains(got, "[exit 7]") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("empty command is schema error", func(t *testing.T) {
		got := Bash.Execute(ctx, bashArgs("  ", 0))
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("got %q, want schema error", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		got := Bash.Execute(ctx, bashArgs("sleep 5", 1))
		if !strings.Contains(got, "timed out") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ctx cancelled", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(150 * time.Millisecond)
			cancel()
		}()
		got := Bash.Execute(cctx, bashArgs("sleep 5", 0))
		if !strings.Contains(got, "cancelled") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("combined output", func(t *testing.T) {
		got := Bash.Execute(ctx, bashArgs("echo out; echo err 1>&2", 0))
		if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
			t.Errorf("expected both stdout and stderr in output: %q", got)
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
