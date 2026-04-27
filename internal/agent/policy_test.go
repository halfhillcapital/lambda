package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func writeArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func bashArgs(t *testing.T, cmd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWritePathsInsideRootAutoAllow(t *testing.T) {
	root := t.TempDir()
	pol := NewPolicy(root)
	cases := []string{
		"foo.go",
		"sub/bar.go",
		filepath.Join(root, "absolute.go"),
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if got := pol("write", writeArgs(t, p)); got != AutoAllow {
				t.Errorf("write %q: got %v, want AutoAllow", p, got)
			}
			if got := pol("edit", writeArgs(t, p)); got != AutoAllow {
				t.Errorf("edit %q: got %v, want AutoAllow", p, got)
			}
		})
	}
}

func TestWritePathsOutsideRootPrompt(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root), "x.go")
	pol := NewPolicy(root)
	cases := []string{
		"../outside.go",
		"../../way/outside.go",
		outside, // absolute path on this platform, guaranteed outside root
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if got := pol("write", writeArgs(t, p)); got != Prompt {
				t.Errorf("write %q: got %v, want Prompt", p, got)
			}
		})
	}
}

func TestWriteBadArgsPrompt(t *testing.T) {
	pol := NewPolicy(t.TempDir())
	cases := []string{
		"",           // not JSON
		"{}",         // no path
		`{"path":""}`, // empty path
		"not json",
	}
	for _, a := range cases {
		t.Run(a, func(t *testing.T) {
			if got := pol("write", a); got != Prompt {
				t.Errorf("args=%q: got %v, want Prompt", a, got)
			}
		})
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

func TestBashAutoAllow(t *testing.T) {
	pol := NewPolicy(t.TempDir())
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
			if got := pol("bash", bashArgs(t, c)); got != AutoAllow {
				t.Errorf("%q: got %v, want AutoAllow", c, got)
			}
		})
	}
}

func TestBashPrompt(t *testing.T) {
	pol := NewPolicy(t.TempDir())
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
			args := bashArgs(t, c.cmd)
			if c.cmd == "" {
				args = ""
			}
			if got := pol("bash", args); got != Prompt {
				t.Errorf("%q: got %v, want Prompt", c.cmd, got)
			}
		})
	}
}

func TestBashQuotedSeparatorsAreSafe(t *testing.T) {
	pol := NewPolicy(t.TempDir())
	// A ';' or '>' inside a quoted string shouldn't trip the escape check,
	// and the pipeline splitter shouldn't break on |/&& inside quotes.
	cases := []string{
		`echo "a ; b"`,
		`echo "a > b"`,
		`echo 'foo && bar'`,
		`echo "x | y"`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := pol("bash", bashArgs(t, c)); got != AutoAllow {
				t.Errorf("%q: got %v, want AutoAllow", c, got)
			}
		})
	}
}

func TestNonDestructiveToolsPrompt(t *testing.T) {
	// The agent only consults the policy for destructive tools, but if
	// someone calls us with an unrecognised tool name we default to Prompt.
	pol := NewPolicy(t.TempDir())
	if got := pol("read", `{"path":"foo"}`); got != Prompt {
		t.Errorf("read: got %v, want Prompt", got)
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
