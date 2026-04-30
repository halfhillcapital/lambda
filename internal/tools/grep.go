package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

const (
	grepDefaultMaxResults = 100
	grepMaxResults        = 2000
	grepMaxLineLen        = 500
	grepMaxFileSize       = 5 * 1024 * 1024
	grepBinaryProbeBytes  = 8 * 1024
)

// GrepArgs is the typed shape of grep's JSON arguments.
type GrepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	MaxResults      int    `json:"max_results"`
	CaseInsensitive bool   `json:"case_insensitive"`
}

type grepTool struct{}

// Grep is the singleton instance of the grep tool.
var Grep grepTool

func (grepTool) Name() string { return "grep" }

// Classify always returns AutoAllow: grep doesn't mutate anything.
func (grepTool) Classify(string) Verdict { return AutoAllow }

// Summarize returns the pattern being searched (with optional path scope).
func (grepTool) Summarize(rawArgs string) string {
	if a, err := Grep.Decode(rawArgs); err == nil {
		s := a.Pattern
		if a.Path != "" {
			s += " in " + a.Path
		}
		return Truncate(s, 120)
	}
	return Truncate(rawArgs, 120)
}

func (grepTool) Schema() openai.ChatCompletionToolParam {
	return makeSchema(Grep.Name(),
		"Search file contents for a regex pattern (RE2 syntax). Returns matching lines as path:line:text. Skips .git, node_modules, vendor, and binary files. Prefer over `bash grep` — faster and budget-aware.",
		shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern":          strProp("Regex pattern (RE2 syntax)."),
				"path":             strProp("Root directory to search. Defaults to '.'"),
				"glob":             strProp("Optional file filter, e.g. '*.go'. If it has no '/', matches against the basename recursively; otherwise full-path match with ** support."),
				"max_results":      intProp("Max matches to return. Defaults to 100, capped at 2000."),
				"case_insensitive": boolProp("Case-insensitive match. Default false."),
			},
			"required": []string{"pattern"},
		})
}

func (grepTool) Decode(rawArgs string) (GrepArgs, error) {
	var a GrepArgs
	if err := decodeArgs(rawArgs, &a); err != nil {
		return a, err
	}
	return a, nil
}

func (grepTool) Execute(ctx context.Context, rawArgs string) string {
	a, err := Grep.Decode(rawArgs)
	if err != nil {
		return schemaErr(err)
	}
	if a.Pattern == "" {
		return schemaErr(errors.New("pattern is required"))
	}
	expr := a.Pattern
	if a.CaseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return schemaErr(fmt.Errorf("invalid regex: %v", err))
	}
	if a.Glob != "" {
		if _, err := matchPath(a.Glob, "x"); err != nil {
			return schemaErr(fmt.Errorf("invalid glob filter: %v", err))
		}
	}
	out, err := runGrep(ctx, re, a.Path, a.Glob, a.MaxResults)
	if err != nil {
		return execErr(err)
	}
	return out
}

func runGrep(ctx context.Context, re *regexp.Regexp, root, glob string, maxResults int) (string, error) {
	if root == "" {
		root = "."
	}
	if _, err := os.Stat(root); err != nil {
		return "", err
	}
	if maxResults <= 0 {
		maxResults = grepDefaultMaxResults
	}
	if maxResults > grepMaxResults {
		maxResults = grepMaxResults
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
		if glob != "" {
			ok, _ := matchPath(glob, rel)
			if !ok {
				continue
			}
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() > grepMaxFileSize {
			continue
		}
		grepFile(p, rel, re, &matches, maxResults, &truncated)
		if truncated {
			break
		}
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n… (truncated to first %d matches)", maxResults)
	}
	return out, nil
}

func grepFile(path, rel string, re *regexp.Regexp, matches *[]string, maxResults int, truncated *bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	br := bufio.NewReader(f)
	if head, _ := br.Peek(grepBinaryProbeBytes); isBinary(head) {
		return
	}
	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !re.MatchString(line) {
			continue
		}
		if len(line) > grepMaxLineLen {
			line = line[:grepMaxLineLen] + "…"
		}
		*matches = append(*matches, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), lineNo, line))
		if len(*matches) >= maxResults {
			*truncated = true
			return
		}
	}
}

// binarySignatures are leading-byte patterns for common non-text formats that
// might not contain a NUL in the first probe window.
var binarySignatures = [][]byte{
	{0x7F, 'E', 'L', 'F'},      // ELF
	{'M', 'Z'},                 // PE / DOS
	{'P', 'K', 0x03, 0x04},     // ZIP / jar / docx / xlsx
	{'P', 'K', 0x05, 0x06},     // ZIP (empty)
	{0xCA, 0xFE, 0xBA, 0xBE},   // Java class / Universal binary
	{0xFE, 0xED, 0xFA, 0xCE},   // Mach-O 32
	{0xFE, 0xED, 0xFA, 0xCF},   // Mach-O 64
	{0x89, 'P', 'N', 'G'},      // PNG
	{0xFF, 0xD8, 0xFF},         // JPEG
	{'G', 'I', 'F', '8'},       // GIF
	{'%', 'P', 'D', 'F'},       // PDF
	{0x1F, 0x8B},               // gzip
	{'B', 'Z', 'h'},            // bzip2
	{0xFD, '7', 'z', 'X', 'Z'}, // xz
	{0xFF, 0xFE},               // UTF-16 LE BOM (unsearchable with UTF-8 regex)
	{0xFE, 0xFF},               // UTF-16 BE BOM
}

func isBinary(b []byte) bool {
	for _, sig := range binarySignatures {
		if bytes.HasPrefix(b, sig) {
			return true
		}
	}
	return slices.Contains(b, 0)
}
