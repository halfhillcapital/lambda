// Package skills loads markdown skill packs from a configurable set of root
// directories and exposes them through an Index that the prompt builder and
// the skill tool both read from.
//
// A skill is a directory containing a SKILL.md with YAML frontmatter:
//
//	---
//	name: grill-with-docs
//	description: Interview the user relentlessly about a plan...
//	---
//	<body the model follows once the skill is loaded>
//
// Only `name` and `description` are parsed. Other fields are ignored;
// `allowed-tools` triggers a one-time stderr warning because lambda does not
// enforce per-skill tool restrictions (see docs/adr/0001).
package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// bodySoftCap is the size above which a SKILL.md body triggers a warning.
// Bodies are still loaded — the cap is advisory, not enforced.
const bodySoftCap = 50 * 1024

// Skill is a loaded skill's metadata. The body is read lazily from Path on
// each call so edits to SKILL.md take effect on the next skill invocation
// without a restart.
type Skill struct {
	Name        string
	Description string
	// Dir is the absolute path to the skill's directory. It is prepended to
	// the body returned by Body() so the model can locate bundled files.
	Dir string
	// Path is the absolute path to SKILL.md.
	Path string
}

// Body re-reads SKILL.md and returns the markdown body (frontmatter stripped),
// prefixed with a one-line "Base directory" preamble pointing at Dir.
func (s Skill) Body() (string, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(b))
	return fmt.Sprintf("Base directory for this skill: %s\n\n%s", s.Dir, body), nil
}

// Index holds the set of skills discovered at startup, keyed by name.
type Index struct {
	skills map[string]Skill
	// order preserves the deterministic listing order used by List() so the
	// system-prompt block is stable across runs.
	order []string
}

// Empty returns an Index with no skills. Useful for tests and for the
// degenerate "no roots configured" case.
func Empty() *Index { return &Index{skills: map[string]Skill{}} }

// Get returns the skill with the given name, or false if none exists.
func (i *Index) Get(name string) (Skill, bool) {
	s, ok := i.skills[name]
	return s, ok
}

// List returns the skills in stable order (sorted by name).
func (i *Index) List() []Skill {
	out := make([]Skill, 0, len(i.order))
	for _, n := range i.order {
		out = append(out, i.skills[n])
	}
	return out
}

// Names returns the set of registered skill names. Used by the slash
// dispatcher to decide whether `/foo` matches a skill.
func (i *Index) Names() []string {
	out := make([]string, len(i.order))
	copy(out, i.order)
	return out
}

// Load scans roots in order and returns an Index. Earlier roots win on name
// collision (so a project-local skills dir overrides a user-global one).
// Missing roots are silently skipped; malformed skills are skipped with a
// warning written to warn.
//
// warn may be nil, in which case warnings are discarded.
func Load(roots []string, warn io.Writer) *Index {
	if warn == nil {
		warn = io.Discard
	}
	idx := &Index{skills: map[string]Skill{}}
	var allowedToolsOnce sync.Once

	for _, root := range roots {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // missing roots are normal
		}
		for _, e := range entries {
			absDir, err := filepath.Abs(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			info, err := os.Stat(absDir) // follow symlinks
			if err != nil || !info.IsDir() {
				continue
			}
			path := filepath.Join(absDir, "SKILL.md")
			raw, err := os.ReadFile(path)
			if err != nil {
				continue // not a skill dir
			}
			s, err := parseSkill(absDir, path, raw, e.Name(), &allowedToolsOnce, warn)
			if err != nil {
				fmt.Fprintf(warn, "lambda: skipping skill %s: %v\n", path, err)
				continue
			}
			if _, dup := idx.skills[s.Name]; dup {
				continue // earlier root wins
			}
			if len(raw) > bodySoftCap {
				fmt.Fprintf(warn, "lambda: skill %s body is %d bytes (soft cap %d); loading anyway\n", s.Name, len(raw), bodySoftCap)
			}
			idx.skills[s.Name] = s
		}
	}

	idx.order = make([]string, 0, len(idx.skills))
	for n := range idx.skills {
		idx.order = append(idx.order, n)
	}
	sort.Strings(idx.order)
	return idx
}

func parseSkill(dir, path string, raw []byte, dirName string, allowedToolsOnce *sync.Once, warn io.Writer) (Skill, error) {
	fm, _ := splitFrontmatter(string(raw))
	if fm == nil {
		return Skill{}, fmt.Errorf("missing YAML frontmatter")
	}
	name := fm["name"]
	desc := fm["description"]
	if name == "" {
		return Skill{}, fmt.Errorf("frontmatter missing required field: name")
	}
	if desc == "" {
		return Skill{}, fmt.Errorf("frontmatter missing required field: description")
	}
	if name != dirName {
		return Skill{}, fmt.Errorf("frontmatter name %q does not match directory name %q", name, dirName)
	}
	if _, hasAllowed := fm["allowed-tools"]; hasAllowed {
		allowedToolsOnce.Do(func() {
			fmt.Fprintf(warn, "lambda: skill %s declares allowed-tools; lambda does not enforce per-skill tool restrictions (warned once)\n", name)
		})
	}
	return Skill{Name: name, Description: desc, Dir: dir, Path: path}, nil
}

// utf8BOM is the byte sequence some editors prepend to files. Stripped before
// parsing so a SKILL.md saved with a BOM still loads.
const utf8BOM = "\xef\xbb\xbf"

// splitFrontmatter splits a SKILL.md into a key/value frontmatter map and the
// remaining body. Returns (nil, raw) if no `---`-delimited frontmatter is
// present.
//
// The parser is intentionally minimal: scalar `key: value` pairs only, no
// nesting, no multiline strings, no lists. Skill frontmatter in the wild is
// flat, so a real YAML dependency is overkill.
func splitFrontmatter(raw string) (map[string]string, string) {
	s := strings.TrimPrefix(raw, utf8BOM)
	if !strings.HasPrefix(s, "---") {
		return nil, raw
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, raw
	}
	fmText := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, "\r\n")

	fm := map[string]string{}
	for _, line := range strings.Split(fmText, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		fm[key] = val
	}
	return fm, body
}

// DefaultRoots returns the search path used when no LAMBDA_SKILLS_DIR override
// is set: project-local first (./.claude/skills), then user-global
// (~/.claude/skills). Missing entries are still returned — Load skips them.
func DefaultRoots(cwd string) []string {
	roots := []string{filepath.Join(cwd, ".claude", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".claude", "skills"))
	}
	return roots
}

// RootsFromEnv parses LAMBDA_SKILLS_DIR (comma-separated) and prepends it to
// the default roots. Returns DefaultRoots(cwd) when the env var is unset or
// empty.
func RootsFromEnv(cwd, env string) []string {
	defaults := DefaultRoots(cwd)
	env = strings.TrimSpace(env)
	if env == "" {
		return defaults
	}
	var extra []string
	for _, p := range strings.Split(env, ",") {
		if p = strings.TrimSpace(p); p != "" {
			extra = append(extra, p)
		}
	}
	return append(extra, defaults...)
}
