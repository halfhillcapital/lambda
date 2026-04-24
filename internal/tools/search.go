package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	grepDefaultMaxResults = 100
	grepMaxResults        = 2000
	grepMaxLineLen        = 500
	grepMaxFileSize       = 5 * 1024 * 1024
	grepBinaryProbeBytes  = 8 * 1024
	globDefaultMaxResults = 1000
	globMaxResults        = 10000
)

// skipDirs are directory basenames not descended into during the filesystem
// fallback walk. They match anywhere except the explicit root, so the model
// can still target them by passing path explicitly (e.g.
// path="node_modules/foo"). When root is inside a git work tree, listing
// goes through `git ls-files -co --exclude-standard` and .gitignore takes
// over instead.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

// listCandidates returns candidate file paths under root as forward-slash
// relative paths. When root is inside a git work tree and `git` is on PATH,
// it uses `git ls-files -co --exclude-standard` so .gitignore (and
// .git/info/exclude and global excludes) are respected. Otherwise it walks
// the filesystem and skips entries in skipDirs.
func listCandidates(ctx context.Context, root string) ([]string, error) {
	if files, ok := gitListFiles(ctx, root); ok {
		return files, nil
	}
	return fsListFiles(ctx, root)
}

func gitListFiles(ctx context.Context, root string) ([]string, bool) {
	probe := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree")
	out, err := probe.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	out, err = cmd.Output()
	if err != nil {
		return nil, false
	}
	var files []string
	for rel := range bytes.SplitSeq(out, []byte{0}) {
		if len(rel) == 0 {
			continue
		}
		files = append(files, string(rel))
	}
	return files, true
}

func fsListFiles(ctx context.Context, root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			rel = filepath.Base(p)
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// matchPath matches pattern against p with doublestar (`**`) semantics.
// If pattern contains no '/', it matches against the basename of p only
// (recursive). Otherwise it matches against the full forward-slash path.
func matchPath(pattern, p string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	p = filepath.ToSlash(p)
	if !strings.Contains(pattern, "/") {
		return matchSegments(strings.Split(pattern, "/"), []string{path.Base(p)})
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

// matchSegments matches a pattern split into / segments against a target
// split into / segments. The "**" segment matches zero or more target segments.
func matchSegments(pat, target []string) (bool, error) {
	for len(pat) > 0 {
		seg := pat[0]
		if seg == "**" {
			if len(pat) == 1 {
				return true, nil
			}
			for i := 0; i <= len(target); i++ {
				ok, err := matchSegments(pat[1:], target[i:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(target) == 0 {
			return false, nil
		}
		ok, err := path.Match(seg, target[0])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		pat, target = pat[1:], target[1:]
	}
	return len(target) == 0, nil
}

func doGlob(ctx context.Context, pattern, root string, maxResults int) (string, error) {
	if pattern == "" {
		return "", &schemaError{err: errors.New("pattern is required")}
	}
	// Validate pattern syntax up front.
	if _, err := matchPath(pattern, "x"); err != nil {
		return "", &schemaError{err: fmt.Errorf("invalid glob pattern: %v", err)}
	}
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

func doGrep(ctx context.Context, pattern, root, glob string, maxResults int, caseInsensitive bool) (string, error) {
	if pattern == "" {
		return "", &schemaError{err: errors.New("pattern is required")}
	}
	expr := pattern
	if caseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", &schemaError{err: fmt.Errorf("invalid regex: %v", err)}
	}
	if glob != "" {
		if _, err := matchPath(glob, "x"); err != nil {
			return "", &schemaError{err: fmt.Errorf("invalid glob filter: %v", err)}
		}
	}
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
