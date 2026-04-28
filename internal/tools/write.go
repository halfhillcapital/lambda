package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// WriteArgs is the typed shape of write's JSON arguments.
type WriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeTool struct{}

// Write is the singleton instance of the write tool.
var Write writeTool

func (writeTool) Name() string        { return "write" }
func (writeTool) IsDestructive() bool { return true }

func (writeTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Write.Name(),
		"Create a new file or completely overwrite an existing one with the given content.",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path":    strProp("Path of the file to create or overwrite."),
				"content": strProp("Full file content to write."),
			},
			"required": []string{"path", "content"},
		})
}

// Decode unmarshals Write's arguments into a typed struct. Used by the
// agent's policy and the TUI's modal preview to read the path/content
// without a second round of JSON parsing.
func (writeTool) Decode(rawArgs string) (WriteArgs, error) {
	var a WriteArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
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
