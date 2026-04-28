package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

const (
	globDefaultMaxResults = 1000
	globMaxResults        = 10000
)

// GlobArgs is the typed shape of glob's JSON arguments.
type GlobArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

type globTool struct{}

// Glob is the singleton instance of the glob tool.
var Glob globTool

func (globTool) Name() string        { return "glob" }
func (globTool) IsDestructive() bool { return false }

func (globTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Glob.Name(),
		"Find files matching a glob pattern. Supports ** for recursive matching. A pattern with no '/' is matched against the basename recursively (so 'config.go' finds it anywhere). Skips .git, node_modules, vendor. Prefer over `bash find`.",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern":     strProp("Glob pattern, e.g. '**/*.go', 'cmd/**/*.go', or 'config.go'."),
				"path":        strProp("Root directory. Defaults to '.'"),
				"max_results": intProp("Max paths to return. Defaults to 1000."),
			},
			"required": []string{"pattern"},
		})
}

func (globTool) Decode(rawArgs string) (GlobArgs, error) {
	var a GlobArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

func (globTool) Execute(ctx context.Context, rawArgs string) string {
	a, err := Glob.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Pattern == "" {
		return schemaErr(errors.New("pattern is required"))
	}
	if _, err := matchPath(a.Pattern, "x"); err != nil {
		return schemaErr(fmt.Errorf("invalid glob pattern: %v", err))
	}
	out, err := runGlob(ctx, a.Pattern, a.Path, a.MaxResults)
	if err != nil {
		return execErr(err)
	}
	return out
}

func runGlob(ctx context.Context, pattern, root string, maxResults int) (string, error) {
	if root == "" {
		root = "."
	}
	if _, err := os.Stat(root); err != nil {
		return "", err
	}
	if maxResults <= 0 {
		maxResults = globDefaultMaxResults
	}
	if maxResults > globMaxResults {
		maxResults = globMaxResults
	}

	files, err := listCandidates(ctx, root)
	if err != nil {
		return "", err
	}

	var matches []string
	truncated := false
	for _, rel := range files {
		if ctx.Err() != nil {
			break
		}
		ok, err := matchPath(pattern, rel)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		matches = append(matches, rel)
		if len(matches) >= maxResults {
			truncated = true
			break
		}
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n… (truncated to first %d matches)", maxResults)
	}
	return out, nil
}
