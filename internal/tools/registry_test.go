package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestDefaultSchemas asserts every built-in tool ships a schema.
func TestDefaultSchemas(t *testing.T) {
	want := map[string]bool{
		Read.Name(): true, Write.Name(): true, Edit.Name(): true,
		Grep.Name(): true, Glob.Name(): true, Bash.Name(): true,
	}
	got := Default.Schemas()
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

// TestIsDestructive pins which tools require confirmation.
func TestIsDestructive(t *testing.T) {
	cases := []struct {
		tool Tool
		want bool
	}{
		{Read, false}, {Grep, false}, {Glob, false},
		{Write, true}, {Edit, true}, {Bash, true},
	}
	for _, c := range cases {
		t.Run(c.tool.Name(), func(t *testing.T) {
			if got := c.tool.IsDestructive(); got != c.want {
				t.Errorf("%s: IsDestructive() = %v, want %v", c.tool.Name(), got, c.want)
			}
		})
	}
}

// TestRegistryExecuteUnknownTool covers the dispatch-miss path.
func TestRegistryExecuteUnknownTool(t *testing.T) {
	got := Default.Execute(context.Background(), "no_such_tool", "{}")
	if !strings.HasPrefix(got, "schema error:") {
		t.Errorf("unknown tool should be a schema error; got %q", got)
	}
	if !strings.Contains(got, "unknown tool") {
		t.Errorf("got %q, want substring \"unknown tool\"", got)
	}
}

// TestExecuteSchemaVsExecutionError pins the prefix contract that callers
// rely on: malformed JSON ⇒ "schema error:", runtime failure ⇒ "error:".
func TestExecuteSchemaVsExecutionError(t *testing.T) {
	t.Run("malformed JSON is schema error", func(t *testing.T) {
		got := Read.Execute(context.Background(), "not json")
		if !strings.HasPrefix(got, "schema error:") {
			t.Errorf("malformed JSON should be \"schema error:\"; got %q", got)
		}
	})

	t.Run("missing file is execution error", func(t *testing.T) {
		got := Read.Execute(context.Background(), `{"path":"/definitely/does/not/exist/zzz"}`)
		if !strings.HasPrefix(got, "error:") || strings.HasPrefix(got, "schema error:") {
			t.Errorf("missing file should be \"error:\" not \"schema error:\"; got %q", got)
		}
	})
}

// TestDecodeArgs covers the package-internal JSON helper.
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

// --- shared test helpers ---

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
