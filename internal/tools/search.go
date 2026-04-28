package tools

import (
	"bytes"
	"context"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
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
