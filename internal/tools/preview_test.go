package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryPreview(t *testing.T) {
	r := New("", nil)

	t.Run("bash command preview", func(t *testing.T) {
		got := r.Preview(Bash.Name(), bashArgs("echo hello", 0))
		want := []PreviewLine{{Kind: PreviewCommand, Text: "$ echo hello"}}
		assertPreviewLines(t, got, want)
	})

	t.Run("write content preview", func(t *testing.T) {
		raw := writeArgs("a.txt", "one\ntwo")
		got := r.Preview(Write.Name(), raw)

		if len(got) != 3 {
			t.Fatalf("got %d lines, want 3: %#v", len(got), got)
		}
		if got[0].Kind != PreviewText || !strings.Contains(got[0].Text, "a.txt") || !strings.Contains(got[0].Text, "7 bytes, 2 lines") {
			t.Fatalf("bad header: %#v", got[0])
		}
		assertPreviewLines(t, got[1:], []PreviewLine{
			{Kind: PreviewText, Text: "one"},
			{Kind: PreviewText, Text: "two"},
		})
	})

	t.Run("write preview truncates long content", func(t *testing.T) {
		content := strings.Repeat("line\n", 25)
		raw := writeArgs("long.txt", content)
		got := r.Preview(Write.Name(), raw)

		if len(got) != 22 {
			t.Fatalf("got %d lines, want header + 20 lines + truncation: %#v", len(got), got)
		}
		if !strings.Contains(got[len(got)-1].Text, "6 more lines") {
			t.Fatalf("missing truncation note: %#v", got[len(got)-1])
		}
	})

	t.Run("edit diff preview", func(t *testing.T) {
		got := r.Preview(Edit.Name(), editArgs("a.txt", "old", "new", false))
		assertPreviewLines(t, got, []PreviewLine{
			{Kind: PreviewText, Text: "a.txt:"},
			{Kind: PreviewRemoved, Text: "- old"},
			{Kind: PreviewAdded, Text: "+ new"},
		})
	})

	t.Run("malformed args fall back", func(t *testing.T) {
		got := r.Preview(Edit.Name(), "not json")
		assertPreviewLines(t, got, []PreviewLine{{Kind: PreviewText, Text: "not json"}})
	})

	t.Run("tool without rich preview falls back", func(t *testing.T) {
		raw, _ := json.Marshal(ReadArgs{Path: "a.txt"})
		got := r.Preview(Read.Name(), string(raw))
		assertPreviewLines(t, got, []PreviewLine{{Kind: PreviewText, Text: `{"path":"a.txt"}`}})
	})
}

func assertPreviewLines(t *testing.T, got, want []PreviewLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("line %d got %#v, want %#v", i, got[i], want[i])
		}
	}
}
