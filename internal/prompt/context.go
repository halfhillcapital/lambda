package prompt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// projectContextMaxBytes is the hard cap on AGENTS.md / CLAUDE.md payload
// size after which content is truncated from the tail. 8 KiB is generous for
// human-curated guidance and keeps the system prompt lean even on small
// local models.
const projectContextMaxBytes = 8 * 1024

// projectContextNames is the per-directory lookup order. AGENTS.md is the
// modern vendor-neutral convention; CLAUDE.md is the fallback for repos
// that predate it. First hit at any level wins.
var projectContextNames = []string{"AGENTS.md", "CLAUDE.md"}

// ProjectContext is a loaded AGENTS.md / CLAUDE.md payload, or the zero
// value when nothing was found / loading was disabled.
type ProjectContext struct {
	// Path is the absolute path to the file that was loaded, or "" when
	// nothing was found.
	Path string
	// Content is the file body, possibly tail-truncated. Empty when nothing
	// was loaded.
	Content string
	// OriginalSize is the file's size on disk before any truncation. Equals
	// len(Content) when no truncation happened.
	OriginalSize int
	// Truncated is true when Content was tail-truncated to fit the cap.
	Truncated bool
}

// None reports whether nothing was loaded.
func (p ProjectContext) None() bool { return p.Path == "" }

// LoadProjectContext discovers and reads the nearest AGENTS.md (or CLAUDE.md
// fallback) starting at cwd.
//
// Discovery:
//   - Walk cwd → parent → … checking AGENTS.md then CLAUDE.md at each level.
//   - First hit wins; stop walking.
//   - Stop at the first directory containing a .git entry.
//   - If no .git ancestor exists, only check cwd itself (no walk).
//
// Warnings about unreadable / non-UTF-8 files, and a single advisory line on
// successful load, are written to warn (which may be nil — output is then
// discarded). Empty files are treated as "nothing to load" silently.
func LoadProjectContext(cwd string, warn io.Writer) ProjectContext {
	if warn == nil {
		warn = io.Discard
	}
	path := discoverProjectContext(cwd)
	if path == "" {
		return ProjectContext{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(warn, "lambda: project context found at %s but could not be read: %v\n", path, err)
		return ProjectContext{}
	}
	if len(raw) == 0 {
		return ProjectContext{}
	}
	if !utf8.Valid(raw) {
		fmt.Fprintf(warn, "lambda: project context at %s is not valid UTF-8; skipping\n", path)
		return ProjectContext{}
	}

	loaded := ProjectContext{Path: path, OriginalSize: len(raw)}
	if len(raw) > projectContextMaxBytes {
		loaded.Content = string(raw[:projectContextMaxBytes])
		loaded.Truncated = true
	} else {
		loaded.Content = string(raw)
	}

	if loaded.Truncated {
		fmt.Fprintf(warn, "lambda: loaded project context from %s (%d bytes, truncated from %d)\n", path, projectContextMaxBytes, loaded.OriginalSize)
	} else {
		fmt.Fprintf(warn, "lambda: loaded project context from %s (%d bytes)\n", path, loaded.OriginalSize)
	}
	return loaded
}

// discoverProjectContext walks up from start checking each directory for any
// candidate filename, in projectContextNames order. The walk stops at the
// first directory containing a .git entry. When no .git ancestor exists,
// only the starting directory is considered — we do not search the entire
// filesystem above an unrelated cwd. Returns "" when nothing matches.
func discoverProjectContext(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	if !hasGitAncestor(abs) {
		return firstProjectContextMatch(abs)
	}
	dir := abs
	for {
		if hit := firstProjectContextMatch(dir); hit != "" {
			return hit
		}
		if isGitRoot(dir) {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func firstProjectContextMatch(dir string) string {
	for _, name := range projectContextNames {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func hasGitAncestor(dir string) bool {
	for {
		if isGitRoot(dir) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// isGitRoot reports whether dir contains a .git entry (file or directory —
// .git is a file inside git worktrees).
func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
