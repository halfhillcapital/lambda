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

// --- safety tests ---

func bashClassifyArgs(t *testing.T, cmd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBashClassify_AutoAllow(t *testing.T) {
	cases := []string{
		"ls",
		"ls -la",
		"pwd",
		"cat README.md",
		"head -n 20 foo.txt",
		"grep -r pattern .",
		"rg pattern",
		"git status",
		"git log --oneline -n 5",
		"git diff HEAD~1",
		"git ls-files",
		"go build ./...",
		"go test ./internal/...",
		"go vet ./...",
		"cargo check",
		"cargo test --release",
		"make",
		"make test",
		"ls | wc -l",
		"cat foo.txt | grep bar | head -n 5",
		"git log --oneline | head",
		"echo hello",
		`echo "hello world"`,
		"true",
		"false && true",
		"diff a.txt b.txt",
		"awk '{print $1}' file.txt",
		"sed 's/foo/bar/' file.txt",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := Bash.Classify(bashClassifyArgs(t, c)); got != AutoAllow {
				t.Errorf("%q: got %v, want AutoAllow", c, got)
			}
		})
	}
}

func TestBashClassify_Prompt(t *testing.T) {
	cases := []struct {
		name, cmd string
	}{
		// Destructive / state-changing.
		{"rm", "rm -rf foo"},
		{"mv", "mv a b"},
		{"cp", "cp a b"},
		{"chmod", "chmod 777 foo"},
		{"sudo", "sudo ls"},
		{"curl", "curl https://evil.example.com"},
		{"wget", "wget https://evil.example.com"},
		{"ssh", "ssh host whoami"},
		// Git writes.
		{"git push", "git push origin main"},
		{"git reset", "git reset --hard HEAD~1"},
		{"git checkout", "git checkout main"},
		{"git clean", "git clean -fd"},
		{"git branch", "git branch --list"}, // deliberately omitted from allowlist
		{"git remote", "git remote -v"},
		{"git config", "git config --get user.email"},
		// Go/Cargo risky.
		{"go run", "go run main.go"},
		{"go install", "go install ./..."},
		{"go mod download", "go mod download"},
		{"go generate", "go generate ./..."},
		{"cargo run", "cargo run"},
		{"cargo install", "cargo install ripgrep"},
		// Package managers.
		{"npm install", "npm install"},
		{"pip install", "pip install foo"},
		{"npm test", "npm test"},
		// Sed in-place.
		{"sed -i", "sed -i 's/a/b/' foo"},
		{"sed --in-place", "sed --in-place=.bak 's/a/b/' foo"},
		// Find with exec/delete.
		{"find -exec", "find . -name '*.go' -exec rm {} \\;"},
		{"find -delete", "find . -name cache -delete"},
		// Shell escapes.
		{"backticks", "echo `whoami`"},
		{"command subst", "echo $(whoami)"},
		{"redirection out", "echo hi > file.txt"},
		{"redirection append", "echo hi >> file.txt"},
		{"redirection in", "grep foo < file.txt"},
		{"semicolon chain", "ls; rm -rf /"},
		{"background", "sleep 60 &"},
		// Env assignment prefix.
		{"env assign", "FOO=bar ls"},
		// Unknown command.
		{"unknown", "frobnicate --all"},
		// Bad JSON.
		{"bad json", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := bashClassifyArgs(t, c.cmd)
			if c.cmd == "" {
				args = ""
			}
			if got := Bash.Classify(args); got != Prompt {
				t.Errorf("%q: got %v, want Prompt", c.cmd, got)
			}
		})
	}
}

func TestBashClassify_QuotedSeparatorsAreSafe(t *testing.T) {
	cases := []string{
		`echo "a ; b"`,
		`echo "a > b"`,
		`echo 'foo && bar'`,
		`echo "x | y"`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := Bash.Classify(bashClassifyArgs(t, c)); got != AutoAllow {
				t.Errorf("%q: got %v, want AutoAllow", c, got)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"ls -la", []string{"ls", "-la"}, true},
		{`echo "hello world"`, []string{"echo", "hello world"}, true},
		{`echo 'a b' c`, []string{"echo", "a b", "c"}, true},
		{`echo \"escaped\"`, []string{"echo", `"escaped"`}, true},
		{`unbalanced "quote`, nil, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := tokenize(c.in)
			if ok != c.ok {
				t.Fatalf("ok=%v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestSplitPipeline(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a | b", []string{"a", "b"}},
		{"a && b", []string{"a", "b"}},
		{"a || b", []string{"a", "b"}},
		{"a | b && c", []string{"a", "b", "c"}},
		{`echo "a | b"`, []string{`echo "a | b"`}},
		{`echo 'a && b'`, []string{`echo 'a && b'`}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := splitPipeline(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}
