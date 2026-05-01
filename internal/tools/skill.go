package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"

	"lambda/internal/skills"
)

// SkillArgs is the typed shape of skill's JSON arguments.
type SkillArgs struct {
	Name string `json:"name"`
}

// skillTool loads the body of a named skill from the index. Read-only,
// no confirmation required.
type skillTool struct {
	idx *skills.Index
}

// NewSkill builds the skill tool bound to a skills index. Pass skills.Empty()
// when no index is available; the tool will report no skills are registered.
func NewSkill(idx *skills.Index) Tool {
	if idx == nil {
		idx = skills.Empty()
	}
	return skillTool{idx: idx}
}

func (skillTool) Name() string { return "skill" }

func (skillTool) Classify(string) Verdict { return AutoAllow }

func (s skillTool) Summarize(rawArgs string) string {
	a, err := s.Decode(rawArgs)
	if err != nil {
		return Truncate(rawArgs, 120)
	}
	return a.Name
}

func (s skillTool) Schema() openai.ChatCompletionToolParam {
	desc := "Load the body of a named skill (markdown instructions). Use this when the user references a skill by name or when the available-skills listing in the system prompt indicates a skill matches the task."
	if names := s.idx.Names(); len(names) > 0 {
		desc += " Available skills: " + strings.Join(names, ", ") + "."
	}
	return makeSchema(s.Name(), desc,
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"name": strProp("Exact name of the skill to load (case-sensitive)."),
			},
			"required": []string{"name"},
		})
}

func (skillTool) Decode(rawArgs string) (SkillArgs, error) {
	var a SkillArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

func (s skillTool) Execute(_ context.Context, rawArgs string) string {
	a, err := s.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Name == "" {
		return schemaErr(errors.New("name is required"))
	}
	sk, ok := s.idx.Get(a.Name)
	if !ok {
		return execErr(fmt.Errorf("unknown skill %q (available: %s)", a.Name, availableNames(s.idx)))
	}
	body, err := sk.Body()
	if err != nil {
		return execErr(err)
	}
	return body
}

func availableNames(idx *skills.Index) string {
	names := idx.Names()
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
