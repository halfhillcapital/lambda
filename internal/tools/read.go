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

// readFileMaxBytes caps how much of a file Read returns inline. Larger files
// are returned truncated with a notice; the model is nudged toward bash with
// sed/awk/head/tail for slicing past the cap.
const readFileMaxBytes = 256 * 1024

// ReadArgs is the typed shape of read's JSON arguments.
type ReadArgs struct {
	Path string `json:"path"`
}

type readTool struct{}

// Read is the singleton instance of the read tool.
var Read readTool

func (readTool) Name() string        { return "read" }
func (readTool) IsDestructive() bool { return false }

func (readTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Read.Name(),
		"Read the contents of a file. Returns the file text, truncated to ~256KB with a notice if the file is larger (use bash with sed/awk/head/tail for slicing larger files).",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": strProp("Path to the file, absolute or relative to the agent's working directory."),
			},
			"required": []string{"path"},
		})
}

// Decode unmarshals Read's arguments into a typed struct. Returns an error
// only when the JSON itself is malformed; field-level validation (e.g. the
// path being non-empty) lives in Execute.
func (readTool) Decode(rawArgs string) (ReadArgs, error) {
	var a ReadArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

func (readTool) Execute(_ context.Context, rawArgs string) string {
	a, err := Read.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Path == "" {
		return schemaErr(errors.New("path is required"))
	}
	out, err := readFile(a.Path)
	if err != nil {
		return execErr(err)
	}
	return out
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) <= readFileMaxBytes {
		return string(b), nil
	}
	s := string(b[:readFileMaxBytes])
	if idx := strings.LastIndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	return fmt.Sprintf("%s\n… (file is %d bytes, truncated to first %d)", s, len(b), len(s)), nil
}
