// Package tools implements the built-in tools the agent dispatches against:
// read, write, edit, grep, glob, and bash. Each tool lives in its own file
// and exposes a singleton; Default is the registry shipped with lambda.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// Tool is the uniform shape the agent dispatches against: a name, a JSON
// schema describing the call, a destructiveness classification (drives the
// confirmation flow), and an Execute that runs the tool against raw JSON
// arguments.
//
// Concrete tool singletons (Read, Write, …) also expose a typed Decode
// method so policy and TUI layers can read fields like "path" or "command"
// without re-unmarshalling the JSON. Decode is *not* on this interface
// because each tool's argument struct is a different type.
type Tool interface {
	Name() string
	Schema() openai.ChatCompletionToolParam
	IsDestructive() bool
	Execute(ctx context.Context, rawArgs string) string
}

// Registry maps a tool's name to the tool itself. Default is the registry
// shipped with lambda; tests can build their own with custom or fake tools.
type Registry map[string]Tool

// Default is the agent's built-in registry: read, write, edit, grep, glob, bash.
var Default = mustBuild(Read, Write, Edit, Grep, Glob, Bash)

// mustBuild assembles a Registry, panicking on duplicate names. Used to
// build Default at package init; tests can either reuse Default or compose
// their own (typically with one or two fake tools).
func mustBuild(tools ...Tool) Registry {
	r := make(Registry, len(tools))
	for _, t := range tools {
		if _, dup := r[t.Name()]; dup {
			panic("tools: duplicate tool name: " + t.Name())
		}
		r[t.Name()] = t
	}
	return r
}

// Schemas returns one schema per tool. Order is unspecified; the model only
// cares about the set, not the sequence.
func (r Registry) Schemas() []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(r))
	for _, t := range r {
		out = append(out, t.Schema())
	}
	return out
}

// Execute dispatches a tool call by name. rawArgs is the raw JSON string
// from the model. Errors are encoded in the result string with a
// "schema error:" or "error:" prefix so the model can recover.
func (r Registry) Execute(ctx context.Context, name, rawArgs string) string {
	t, ok := r[name]
	if !ok {
		return fmt.Sprintf("schema error: unknown tool %q", name)
	}
	return t.Execute(ctx, rawArgs)
}

// decodeArgs unmarshals a tool's raw JSON arguments into a typed struct.
// Empty input is treated as `{}` so tools with all-optional fields can be
// called with no arguments.
func decodeArgs(raw string, into any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		return fmt.Errorf("malformed JSON arguments: %v", err)
	}
	return nil
}

// schemaErr formats an argument-validation failure with the "schema error:"
// prefix. The prefix is part of the agent contract: it tells the model to
// fix its arguments rather than retrying the same call.
func schemaErr(err error) string { return "schema error: " + err.Error() }

// execErr formats a runtime failure with the "error:" prefix — the call
// ran but failed (file not found, exit code, timeout, …).
func execErr(err error) string { return "error: " + err.Error() }

// --- schema construction helpers ---

func makeSchema(name, desc string, params shared.FunctionParameters) openai.ChatCompletionToolParam {
	return openai.ChatCompletionToolParam{
		Function: shared.FunctionDefinitionParam{
			Name:        name,
			Description: openai.String(desc),
			Parameters:  params,
		},
	}
}

func strProp(d string) map[string]any  { return map[string]any{"type": "string", "description": d} }
func boolProp(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
func intProp(d string) map[string]any  { return map[string]any{"type": "integer", "description": d} }
