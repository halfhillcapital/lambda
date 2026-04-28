// Policy statically decides whether a destructive tool call can be
// auto-allowed without bothering the user. Anything it can't decide is
// left to fall through to the interactive confirmer.
//
// The rules are intentionally narrow: a tight allowlist of read-only shell
// commands and "writes inside the session root are fine, writes anywhere
// else must be confirmed." When in doubt, return Prompt.
package agent

import (
	"os"
	"path/filepath"
	"strings"

	"lambda/internal/tools"
)

// Verdict is the policy's decision on a candidate tool call.
type Verdict int

const (
	// Prompt means the policy has no opinion — fall through to the user.
	Prompt Verdict = iota
	// AutoAllow means the tool call is safe enough to run without asking.
	AutoAllow
	// AutoDeny means the policy refuses outright, regardless of user opinion.
	AutoDeny
)

func (v Verdict) String() string {
	switch v {
	case AutoAllow:
		return "auto-allow"
	case AutoDeny:
		return "auto-deny"
	default:
		return "prompt"
	}
}

// Policy decides a verdict for a pending tool call. name is the tool's
// registry name (e.g. "write"), rawArgs is the JSON argument object
// the model sent.
type Policy func(name, rawArgs string) Verdict

// NewPolicy builds a Policy whose write-path checks are relative to
// sessionRoot (typically the agent's worktree or cwd).
func NewPolicy(sessionRoot string) Policy {
	root, err := filepath.Abs(sessionRoot)
	if err != nil {
		root = sessionRoot
	}
	dangerous := dangerousWritePaths()
	return func(name, rawArgs string) Verdict {
		switch name {
		case tools.Write.Name():
			a, err := tools.Write.Decode(rawArgs)
			if err != nil {
				return Prompt
			}
			return evalWritePath(a.Path, root, dangerous)
		case tools.Edit.Name():
			a, err := tools.Edit.Decode(rawArgs)
			if err != nil {
				return Prompt
			}
			return evalWritePath(a.Path, root, dangerous)
		case tools.Bash.Name():
			a, err := tools.Bash.Decode(rawArgs)
			if err != nil {
				return Prompt
			}
			return evalBashCommand(a.Command)
		}
		return Prompt
	}
}

// --- write / edit ---

// evalWritePath classifies a write/edit destination: AutoAllow inside the
// session root (and outside any "dangerous" path like /etc or ~/.ssh),
// Prompt anywhere else.
func evalWritePath(p, root string, dangerous []string) Verdict {
	if p == "" {
		return Prompt
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	for _, d := range dangerous {
		if isUnder(abs, d) {
			return Prompt
		}
	}
	if isUnder(abs, root) {
		return AutoAllow
	}
	return Prompt
}

// dangerousWritePaths lists locations where writes always prompt, even
// inside the session root. Aimed at credential/secret stores and system
// directories.
func dangerousWritePaths() []string {
	paths := []string{"/etc", "/boot", "/usr", "/bin", "/sbin", "/lib", "/lib64"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".config", "gcloud"),
			filepath.Join(home, ".kube"),
			filepath.Join(home, ".docker"),
			filepath.Join(home, ".netrc"),
		)
	}
	return paths
}

func isUnder(target, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// --- bash ---

// evalBashCommand classifies a bash command: AutoAllow when every pipeline
// segment maps to an entry in the read-only allowlist (with no shell escapes
// or denied flags), Prompt otherwise.
func evalBashCommand(command string) Verdict {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return Prompt
	}
	if hasShellEscapes(cmd) {
		return Prompt
	}
	segs := splitPipeline(cmd)
	if len(segs) == 0 {
		return Prompt
	}
	for _, s := range segs {
		if !segmentIsSafe(s) {
			return Prompt
		}
	}
	return AutoAllow
}

// hasShellEscapes flags shell features we can't reason about statically,
// or that a safe allowlist has no business needing (redirection, bare `&`,
// `;` chains, command substitution).
func hasShellEscapes(cmd string) bool {
	if strings.Contains(cmd, "`") || strings.Contains(cmd, "$(") {
		return true
	}
	if containsUnquoted(cmd, "<>;") {
		return true
	}
	// Bare `&` (backgrounding). `&&` is handled by splitPipeline.
	stripped := strings.ReplaceAll(cmd, "&&", "  ")
	stripped = strings.ReplaceAll(stripped, "||", "  ")
	if containsUnquoted(stripped, "&") {
		return true
	}
	return false
}

// splitPipeline splits cmd on top-level `&&`, `||`, and `|`, preserving
// single and double quoted substrings as opaque.
func splitPipeline(cmd string) []string {
	var segs []string
	var buf strings.Builder
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			segs = append(segs, s)
		}
		buf.Reset()
	}
	inSingle, inDouble := false, false
	i := 0
	for i < len(cmd) {
		c := cmd[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			buf.WriteByte(c)
			i++
		case c == '"' && !inSingle:
			inDouble = !inDouble
			buf.WriteByte(c)
			i++
		case inSingle || inDouble:
			buf.WriteByte(c)
			i++
		case i+1 < len(cmd) && (cmd[i:i+2] == "&&" || cmd[i:i+2] == "||"):
			flush()
			i += 2
		case c == '|':
			flush()
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()
	return segs
}

// segmentIsSafe checks one pipeline segment: argv[0] in allowlist, no
// env-var assignment prefix, no argument-specific denials tripped, and
// subcommand-style commands have an allowed subcommand.
func segmentIsSafe(seg string) bool {
	tokens, ok := tokenize(seg)
	if !ok || len(tokens) == 0 {
		return false
	}
	argv0 := tokens[0]
	// Leading VAR=value shadows a cmd0 lookup, and passes env through to a
	// child process we haven't classified. Prompt.
	if strings.Contains(argv0, "=") {
		return false
	}
	exact := argDenyExact[argv0]
	prefixes := argDenyPrefix[argv0]
	if exact != nil || prefixes != nil {
		for _, arg := range tokens[1:] {
			if exact[arg] {
				return false
			}
			for _, p := range prefixes {
				if strings.HasPrefix(arg, p) {
					return false
				}
			}
		}
	}
	if subs, ok := subCmdRules[argv0]; ok {
		if len(tokens) < 2 {
			return false
		}
		return subs[tokens[1]]
	}
	return readOnlyCmds[argv0]
}

// tokenize splits s into shell-like words, honoring single/double quotes
// and basic `\`-escapes. Returns (nil, false) on unbalanced quotes.
func tokenize(s string) ([]string, bool) {
	var tokens []string
	var cur strings.Builder
	inSingle, inDouble, have := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
				have = true
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(s) {
				cur.WriteByte(s[i+1])
				have = true
				i++
			} else {
				cur.WriteByte(c)
				have = true
			}
		case c == '\'':
			inSingle = true
			have = true
		case c == '"':
			inDouble = true
			have = true
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			have = true
			i++
		case c == ' ' || c == '\t':
			if have {
				tokens = append(tokens, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if inSingle || inDouble {
		return nil, false
	}
	if have {
		tokens = append(tokens, cur.String())
	}
	return tokens, true
}

// containsUnquoted reports whether any byte in `set` appears in s outside
// of single or double quotes (no backslash handling — callers pre-strip
// `&&`/`||` when those would otherwise trip the check).
func containsUnquoted(s, set string) bool {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case !inSingle && !inDouble && strings.IndexByte(set, c) >= 0:
			return true
		}
	}
	return false
}

// --- allowlist tables ---

// readOnlyCmds are argv[0] names safe on their own. They read state, print
// things, or compute — they don't write, install, or touch the network.
var readOnlyCmds = map[string]bool{
	// Filesystem inspection.
	"ls": true, "dir": true, "pwd": true, "stat": true, "file": true,
	"tree": true, "du": true, "df": true, "realpath": true, "readlink": true,
	"basename": true, "dirname": true,
	// Reading files.
	"cat": true, "head": true, "tail": true, "wc": true,
	// Text.
	"echo": true, "printf": true, "grep": true, "rg": true, "ag": true,
	"ack": true, "awk": true, "gawk": true, "mawk": true, "sed": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "paste": true,
	"join": true, "comm": true, "diff": true, "cmp": true, "jq": true, "yq": true,
	// Env / identity.
	"whoami": true, "id": true, "uname": true, "date": true,
	"which": true, "whence": true, "type": true, "command": true,
	// Builtins-ish.
	"true": true, "false": true, "test": true, "[": true,
	// Build drivers that don't install or execute scripts by default.
	"make": true,
}

// subCmdRules maps argv[0] to the set of allowed argv[1] subcommands.
// Every command here is read-only or benign under the given subcommands.
var subCmdRules = map[string]map[string]bool{
	"git":   gitReadSubs,
	"go":    goSafeSubs,
	"cargo": cargoSafeSubs,
}

// gitReadSubs are the git subcommands that cannot alter repo or index
// state — even with unusual flags. Branch/tag/remote/stash/worktree/config
// are deliberately omitted because their write variants (add, rename,
// remove, set-url) share the subcommand name.
var gitReadSubs = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "blame": true,
	"rev-parse": true, "ls-files": true, "ls-tree": true, "cat-file": true,
	"describe": true, "shortlog": true, "reflog": true, "grep": true,
	"whatchanged": true, "for-each-ref": true, "symbolic-ref": true,
}

// goSafeSubs excludes: run (executes code), install/get/mod download
// (network + install), generate (executes //go:generate directives).
var goSafeSubs = map[string]bool{
	"build": true, "test": true, "vet": true, "doc": true,
	"env": true, "list": true, "version": true, "fmt": true,
}

// cargoSafeSubs excludes: run, install, publish, new, init, add, remove,
// update (network), generate-lockfile (writes).
var cargoSafeSubs = map[string]bool{
	"build": true, "check": true, "test": true, "fmt": true,
	"clippy": true, "doc": true, "tree": true, "metadata": true,
	"version": true, "help": true,
}

// argDenyExact trips a command out of the allowlist when any arg matches
// a listed flag exactly.
var argDenyExact = map[string]map[string]bool{
	"sed":  {"-i": true, "--in-place": true},
	"find": {"-exec": true, "-execdir": true, "-delete": true, "-ok": true, "-okdir": true, "-fprint": true, "-fprintf": true, "-fprint0": true},
}

// argDenyPrefix trips a command when any arg starts with one of these
// prefixes — used for long options that take a `=value` form.
var argDenyPrefix = map[string][]string{
	"sed": {"--in-place="},
}
