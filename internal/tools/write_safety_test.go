package tools

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

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
