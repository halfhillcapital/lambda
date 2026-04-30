package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 600 * time.Second
	bashOutputMaxBytes = 20 * 1024
	bashOutputMaxLines = 200
)

// BashArgs is the typed shape of bash's JSON arguments.
type BashArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type bashTool struct{}

// Bash is the singleton instance of the bash tool.
var Bash bashTool

func (bashTool) Name() string { return "bash" }

// Classify defers to the bash allowlist: AutoAllow for read-only pipelines
// in the allowlist, Prompt for anything else (writes, network, shell escapes).
func (bashTool) Classify(rawArgs string) Verdict {
	a, err := Bash.Decode(rawArgs)
	if err != nil {
		return Prompt
	}
	return classifyBashCommand(a.Command)
}

// Summarize returns the (truncated) command line.
func (bashTool) Summarize(rawArgs string) string {
	if a, err := Bash.Decode(rawArgs); err == nil {
		return Truncate(a.Command, 240)
	}
	return Truncate(rawArgs, 120)
}

func (bashTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Bash.Name(),
		"Run a bash command non-interactively (empty stdin) and return its combined stdout+stderr. Bash is required on PATH (git bash on Windows). Default timeout 120s.",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command":         strProp("Bash command line, e.g. 'go test ./...'"),
				"timeout_seconds": intProp("Optional per-call timeout in seconds (max 600). Defaults to 120."),
			},
			"required": []string{"command"},
		})
}

func (bashTool) Decode(rawArgs string) (BashArgs, error) {
	var a BashArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

func (bashTool) Execute(ctx context.Context, rawArgs string) string {
	a, err := Bash.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return schemaErr(errors.New("command is required"))
	}
	out, err := runBash(ctx, a.Command, a.TimeoutSeconds)
	if err != nil {
		return execErr(err)
	}
	return out
}

func runBash(ctx context.Context, command string, timeoutSec int) (string, error) {
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
