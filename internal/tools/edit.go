package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// EditArgs is the typed shape of edit's JSON arguments.
type EditArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// editTool is parameterized by sessionRoot for Classify; other methods
// don't depend on root.
type editTool struct{ root string }

// Edit is the zero-root singleton. See Write for the same pattern.
var Edit editTool

// NewEdit returns an editTool bound to a session root.
func NewEdit(sessionRoot string) editTool {
	return editTool{root: sessionRoot}
}

func (editTool) Name() string { return "edit" }

func (editTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Edit.Name(),
		"Replace a unique substring in a file. Fails if old_string is not found or matches more than once (unless replace_all is true). Pick old_string with enough surrounding context to be unique.",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path":        strProp("Path of the file to edit."),
				"old_string":  strProp("Exact text to replace. Must be unique in the file unless replace_all is true."),
				"new_string":  strProp("Text to replace it with."),
				"replace_all": boolProp("If true, replace every occurrence instead of requiring uniqueness. Default false."),
			},
			"required": []string{"path", "old_string", "new_string"},
		})
}

func (editTool) Decode(rawArgs string) (EditArgs, error) {
	var a EditArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

// Classify checks the edit destination against the session root, same rules
// as write.
func (e editTool) Classify(rawArgs string) Verdict {
	a, err := Edit.Decode(rawArgs)
	if err != nil {
		return Prompt
	}
	return classifyWritePath(a.Path, e.root)
}

// Summarize returns the path being edited.
func (editTool) Summarize(rawArgs string) string {
	if a, err := Edit.Decode(rawArgs); err == nil {
		return a.Path
	}
	return Truncate(rawArgs, 120)
}

// Preview returns a simple line-oriented diff of the requested replacement.
func (editTool) Preview(rawArgs string) []PreviewLine {
	a, err := Edit.Decode(rawArgs)
	if err != nil {
		return nil
	}
	lines := []PreviewLine{{Kind: PreviewText, Text: a.Path + ":"}}
	for _, line := range strings.Split(a.OldString, "\n") {
		lines = append(lines, PreviewLine{Kind: PreviewRemoved, Text: "- " + line})
	}
	for _, line := range strings.Split(a.NewString, "\n") {
		lines = append(lines, PreviewLine{Kind: PreviewAdded, Text: "+ " + line})
	}
	return lines
}

func (editTool) Execute(_ context.Context, rawArgs string) string {
	a, err := Edit.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Path == "" {
		return schemaErr(errors.New("path is required"))
	}
	if a.OldString == "" {
		return schemaErr(errors.New("old_string must not be empty"))
	}
	out, err := editFile(a.Path, a.OldString, a.NewString, a.ReplaceAll)
	if err != nil {
		return execErr(err)
	}
	return out
}

func editFile(path, oldStr, newStr string, replaceAll bool) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(b)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", path)
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string matches %d times in %s; include more surrounding context to make it unique, or pass replace_all=true", count, path)
	}
	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", path, count, pluralS(count)), nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
