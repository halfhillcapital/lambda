// Package tools implements the tool schema registry and dispatcher used by
// the agent: read_file, write_file, edit_file, grep, glob, and bash.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

type Name string

const (
	ReadFile  Name = "read"
	WriteFile Name = "write"
	EditFile  Name = "edit"
	Grep      Name = "grep"
	Glob      Name = "glob"
	Bash      Name = "bash"
)

// IsDestructive reports whether a tool can modify the filesystem or
// execute arbitrary commands. Destructive tools go through the confirmation flow.
func (n Name) IsDestructive() bool {
	switch n {
	case WriteFile, EditFile, Bash:
		return true
	default:
		return false
	}
}

// Schemas returns the OpenAI tool definitions for every built-in tool.
func Schemas() []openai.ChatCompletionToolParam {
	mk := func(name, desc string, params shared.FunctionParameters) openai.ChatCompletionToolParam {
		return openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        name,
				Description: openai.String(desc),
				Parameters:  params,
			},
		}
	}
	strProp := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	boolProp := func(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
	intProp := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }

	return []openai.ChatCompletionToolParam{
		mk(string(ReadFile), "Read the contents of a file. Returns the file text, truncated to ~256KB with a notice if the file is larger (use bash with sed/awk/head/tail for slicing larger files).", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": strProp("Path to the file, absolute or relative to the agent's working directory."),
			},
			"required": []string{"path"},
		}),
		mk(string(WriteFile), "Create a new file or completely overwrite an existing one with the given content.", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path":    strProp("Path of the file to create or overwrite."),
				"content": strProp("Full file content to write."),
			},
			"required": []string{"path", "content"},
		}),
		mk(string(EditFile), "Replace a unique substring in a file. Fails if old_string is not found or matches more than once (unless replace_all is true). Pick old_string with enough surrounding context to be unique.", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path":        strProp("Path of the file to edit."),
				"old_string":  strProp("Exact text to replace. Must be unique in the file unless replace_all is true."),
				"new_string":  strProp("Text to replace it with."),
				"replace_all": boolProp("If true, replace every occurrence instead of requiring uniqueness. Default false."),
			},
			"required": []string{"path", "old_string", "new_string"},
		}),
		mk(string(Grep), "Search file contents for a regex pattern (RE2 syntax). Returns matching lines as path:line:text. Skips .git, node_modules, vendor, and binary files. Prefer over `bash grep` — faster and budget-aware.", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern":          strProp("Regex pattern (RE2 syntax)."),
				"path":             strProp("Root directory to search. Defaults to '.'"),
				"glob":             strProp("Optional file filter, e.g. '*.go'. If it has no '/', matches against the basename recursively; otherwise full-path match with ** support."),
				"max_results":      intProp("Max matches to return. Defaults to 100, capped at 2000."),
				"case_insensitive": boolProp("Case-insensitive match. Default false."),
			},
			"required": []string{"pattern"},
		}),
		mk(string(Glob), "Find files matching a glob pattern. Supports ** for recursive matching. A pattern with no '/' is matched against the basename recursively (so 'config.go' finds it anywhere). Skips .git, node_modules, vendor. Prefer over `bash find`.", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern":     strProp("Glob pattern, e.g. '**/*.go', 'cmd/**/*.go', or 'config.go'."),
				"path":        strProp("Root directory. Defaults to '.'"),
				"max_results": intProp("Max paths to return. Defaults to 1000."),
			},
			"required": []string{"pattern"},
		}),
		mk(string(Bash), "Run a bash command non-interactively (empty stdin) and return its combined stdout+stderr. Bash is required on PATH (git bash on Windows). Default timeout 120s.", shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command":         strProp("Bash command line, e.g. 'go test ./...'"),
				"timeout_seconds": intProp("Optional per-call timeout in seconds (max 600). Defaults to 120."),
			},
			"required": []string{"command"},
		}),
	}
}

// schemaError marks errors caused by malformed arguments or unknown tool names —
// the model should fix its call rather than retry. Execute formats them with a
// distinct "schema error:" prefix.
type schemaError struct{ err error }

func (e *schemaError) Error() string { return e.err.Error() }
func (e *schemaError) Unwrap() error { return e.err }

// Execute dispatches a tool call by name. rawArgs is the raw JSON string from the model.
// It returns a textual result to feed back to the model. Errors are never returned —
// error conditions are reported as the tool result so the model can recover.
func Execute(ctx context.Context, name, rawArgs string) string {
	res, err := executeInner(ctx, name, rawArgs)
	if err != nil {
		var se *schemaError
		if errors.As(err, &se) {
			return "schema error: " + err.Error()
		}
		return "error: " + err.Error()
	}
	return res
}

func executeInner(ctx context.Context, name, rawArgs string) (string, error) {
	switch Name(name) {
	case ReadFile:
		var a struct {
			Path string `json:"path"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doReadFile(a.Path)
	case WriteFile:
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doWriteFile(a.Path, a.Content)
	case EditFile:
		var a struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doEditFile(a.Path, a.OldString, a.NewString, a.ReplaceAll)
	case Grep:
		var a struct {
			Pattern         string `json:"pattern"`
			Path            string `json:"path"`
			Glob            string `json:"glob"`
			MaxResults      int    `json:"max_results"`
			CaseInsensitive bool   `json:"case_insensitive"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doGrep(ctx, a.Pattern, a.Path, a.Glob, a.MaxResults, a.CaseInsensitive)
	case Glob:
		var a struct {
			Pattern    string `json:"pattern"`
			Path       string `json:"path"`
			MaxResults int    `json:"max_results"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doGlob(ctx, a.Pattern, a.Path, a.MaxResults)
	case Bash:
		var a struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := decodeArgs(rawArgs, &a); err != nil {
			return "", err
		}
		return doBash(ctx, a.Command, a.TimeoutSeconds)
	default:
		return "", &schemaError{err: fmt.Errorf("unknown tool %q", name)}
	}
}

func decodeArgs(raw string, into any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		return &schemaError{err: fmt.Errorf("malformed JSON arguments: %v", err)}
	}
	return nil
}

const readFileMaxBytes = 256 * 1024

func doReadFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
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

func doWriteFile(path, content string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
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

func doEditFile(path, oldStr, newStr string, replaceAll bool) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if oldStr == "" {
		return "", errors.New("old_string must not be empty")
	}
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

const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 600 * time.Second
	bashOutputMaxBytes = 20 * 1024
	bashOutputMaxLines = 200
)

func doBash(ctx context.Context, command string, timeoutSec int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is required")
	}
	timeout := bashDefaultTimeout
	if timeoutSec > 0 {
		t := time.Duration(timeoutSec) * time.Second
		if t > bashMaxTimeout {
			t = bashMaxTimeout
		}
		timeout = t
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	cmd.Stdin = nil
	configureProcessGroup(cmd)

	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	out, err := cmd.CombinedOutput()
	s := truncateOutput(string(out))
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return s + fmt.Sprintf("\n[timed out after %s]", timeout), nil
	case errors.Is(ctx.Err(), context.Canceled):
		return s + "\n[cancelled by user]", nil
	case err != nil:
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return s + fmt.Sprintf("\n[exit %d]", xe.ExitCode()), nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return "", fmt.Errorf("bash not found on PATH (install git bash on Windows)")
		}
		return s + "\n[error: " + err.Error() + "]", nil
	}
	return s, nil
}

func truncateOutput(s string) string {
	if len(s) <= bashOutputMaxBytes && strings.Count(s, "\n") <= bashOutputMaxLines {
		return s
	}
	lines := strings.Split(s, "\n")
	dropped := 0
	if len(lines) > bashOutputMaxLines {
		dropped = len(lines) - bashOutputMaxLines
		lines = lines[:bashOutputMaxLines]
	}
	out := strings.Join(lines, "\n")
	if len(out) > bashOutputMaxBytes {
		out = out[:bashOutputMaxBytes]
		if idx := strings.LastIndexByte(out, '\n'); idx > 0 {
			out = out[:idx]
		}
	}
	if dropped > 0 {
		out += fmt.Sprintf("\n… (%d more lines truncated)", dropped)
	} else {
		out += "\n… (output truncated)"
	}
	return out
}
