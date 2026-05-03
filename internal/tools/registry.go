// Package tools implements the built-in tools the agent dispatches against:
// read, write, edit, grep, glob, bash, and skill. Each tool lives in its own
// file and exposes a singleton; New(sessionRoot, skillsIndex) builds the
// registry shipped with lambda, binding session-aware tools (write, edit) to
// the given root and the skill tool to the given skill index.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"lambda/internal/ai"
	"lambda/internal/skills"
)

// Tool is the uniform shape the agent dispatches against. Each tool answers
// for itself: how it's named to the model (Name/Schema), how its arguments
// render in UI (Summarize), whether a pending call is safe to run unattended
// (Classify), and how to actually run it (Execute).
//
// Concrete tool singletons (Read, Write, …) also expose a typed Decode
// method so callers needing rich previews (e.g. the modal diff) can read
// fields like "path" or "command" without re-unmarshalling. Decode is *not*
// on this interface because each tool's argument struct is a different type.
type Tool interface {
	Name() string
	Schema() ai.ToolSpec
	Summarize(rawArgs string) string
	Classify(rawArgs string) Verdict
	Execute(ctx context.Context, rawArgs string) string
}

// Registry maps a tool's name to the tool itself. Build one with New(root);
// tests can compose their own with custom or fake tools.
type Registry map[string]Tool

// New assembles the agent's built-in registry. sessionRoot is the agent's
// working directory (typically the worktree path) used by write/edit to
// classify whether a destination is "inside the session" — pass "" if no
// session root applies (writes will always Prompt). skillIdx is the loaded
// skill index; pass skills.Empty() (or nil) when no skills are configured.
func New(sessionRoot string, skillIdx *skills.Index) Registry {
	return mustBuild(Read, Grep, Glob, Bash, NewWrite(sessionRoot), NewEdit(sessionRoot), NewSkill(skillIdx))
}

// mustBuild assembles a Registry, panicking on duplicate names.
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
func (r Registry) Schemas() []ai.ToolSpec {
	out := make([]ai.ToolSpec, 0, len(r))
	for _, t := range r {
		out = append(out, t.Schema())
	}
	return out
}

// SchemaChars returns the JSON-marshalled byte total of every tool's schema
// (name, description, parameters). Used by /context to estimate how many
// prompt tokens the tool definitions consume on every request. The exact
// wire encoding varies per provider, but the relative size dominates the
// difference, so this is good enough for a debug breakdown.
func (r Registry) SchemaChars() int {
	n := 0
	for _, t := range r {
		s := t.Schema()
		b, err := json.Marshal(struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		}{s.Name, s.Description, s.Parameters})
		if err != nil {
			continue
		}
		n += len(b)
	}
	return n
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

// Summarize returns a one-line human-readable summary of a pending tool
// call's arguments, or a generic truncated dump if the tool is unknown.
// Used by UI layers (TUI footer, oneshot stderr) without their needing to
// know about specific tools.
func (r Registry) Summarize(name, rawArgs string) string {
	if t, ok := r[name]; ok {
		return t.Summarize(rawArgs)
	}
	return Truncate(rawArgs, 120)
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

// Truncate trims s to at most n runes (approx), appending an ellipsis if
// clipped. Used by Summarize implementations and as the registry's fallback
// rendering for unknown tools.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// --- schema construction helpers ---

func makeSchema(name, desc string, params map[string]any) ai.ToolSpec {
	return ai.ToolSpec{
		Name:        name,
		Description: desc,
		Parameters:  params,
	}
}

func strProp(d string) map[string]any  { return map[string]any{"type": "string", "description": d} }
func boolProp(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
func intProp(d string) map[string]any  { return map[string]any{"type": "integer", "description": d} }
