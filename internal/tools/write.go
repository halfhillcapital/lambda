package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lambda/internal/ai"
)

// WriteArgs is the typed shape of write's JSON arguments.
type WriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// writeTool is parameterized by sessionRoot so Classify can decide whether a
// destination is "inside the session" (AutoAllow) or elsewhere (Prompt).
// Other methods don't depend on root.
type writeTool struct{ root string }

// Write is the zero-root singleton, suitable for callers that only need the
// stateless ops (Decode, Schema, Name, Execute, Summarize). With an empty
// root every path classifies as Prompt — the conservative default. Use
// NewWrite(sessionRoot) to build an instance whose Classify recognises a
// session.
var Write writeTool

// NewWrite returns a writeTool bound to a session root. The agent's Registry
// holds this form so destructive-write classification works.
func NewWrite(sessionRoot string) writeTool {
	return writeTool{root: sessionRoot}
}

func (writeTool) Name() string { return "write" }

func (writeTool) Schema() ai.ToolSpec {
	return makeSchema(Write.Name(),
		"Create a new file or completely overwrite an existing one with the given content.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    strProp("Path of the file to create or overwrite."),
				"content": strProp("Full file content to write."),
			},
			"required": []string{"path", "content"},
		})
}

// Decode unmarshals Write's arguments into a typed struct. Used by the
// modal preview to read path/content without re-parsing JSON.
func (writeTool) Decode(rawArgs string) (WriteArgs, error) {
	var a WriteArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

// Classify checks the destination path against the session root: AutoAllow
// inside the root (and outside any "dangerous" path like /etc or ~/.ssh),
// Prompt anywhere else.
func (w writeTool) Classify(rawArgs string) Verdict {
	a, err := Write.Decode(rawArgs)
	if err != nil {
		return Prompt
	}
	return classifyWritePath(a.Path, w.root)
}

// Summarize returns "path (N bytes)".
func (writeTool) Summarize(rawArgs string) string {
	if a, err := Write.Decode(rawArgs); err == nil {
		return fmt.Sprintf("%s (%d bytes)", a.Path, len(a.Content))
	}
	return Truncate(rawArgs, 120)
}

// Preview returns the destination plus the first chunk of the file content.
func (writeTool) Preview(rawArgs string) []PreviewLine {
	a, err := Write.Decode(rawArgs)
	if err != nil {
		return nil
	}
	contentLines := strings.Split(a.Content, "\n")
	shown := contentLines
	if len(shown) > 20 {
		shown = shown[:20]
	}
	lines := []PreviewLine{{
		Kind: PreviewText,
		Text: fmt.Sprintf("%s — %d bytes, %d lines", a.Path, len(a.Content), len(contentLines)),
	}}
	for _, line := range shown {
		lines = append(lines, PreviewLine{Kind: PreviewText, Text: line})
	}
	if len(contentLines) > 20 {
		lines = append(lines, PreviewLine{Kind: PreviewText, Text: fmt.Sprintf("… (%d more lines)", len(contentLines)-20)})
	}
	return lines
}

func (writeTool) Execute(_ context.Context, rawArgs string) string {
	a, err := Write.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Path == "" {
		return schemaErr(errors.New("path is required"))
	}
	out, err := writeFile(a.Path, a.Content)
	if err != nil {
		return execErr(err)
	}
	return out
}

func writeFile(path, content string) (string, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", path, len(content)), nil
}
