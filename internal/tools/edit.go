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

type editTool struct{}

// Edit is the singleton instance of the edit tool.
var Edit editTool

func (editTool) Name() string        { return "edit" }
func (editTool) IsDestructive() bool { return true }

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
