// Package worktree manages a per-Session git Workspace so the agent edits
// and tool calls land on an isolated branch. A clean Workspace ("nothing
// changed") is garbage-collected silently; a dirty Workspace leaves the
// worktree and branch in place for the user to review, merge, or discard.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workspace carries the git-side state of a single agent invocation: an
// isolated worktree path, the branch on it, and the base branch and start
// SHA it was rooted at. When Enabled is false the Workspace is a no-op:
// New decided the environment didn't qualify (not a git repo, no HEAD,
// user opted out), and Finalize does nothing.
type Workspace struct {
	Enabled    bool
	Path       string // absolute worktree path
	Branch     string // branch created alongside the worktree
	BaseBranch string // branch/ref the Workspace was created from
	StartSHA   string // parent HEAD when the worktree was created
	RepoRoot   string // parent repo toplevel
}

// Cwd returns the working directory the caller should use when this
// Workspace is live. A disabled Workspace returns "" — the caller (Session)
// owns the fallback cwd.
func (w *Workspace) Cwd() string {
	if w.Enabled {
		return w.Path
	}
	return ""
}

// Start creates a Workspace at <repo>/.lambda/worktrees/<ts> on a new branch
// lambda/<ts> rooted at HEAD. It returns a disabled Workspace (no error)
// when cwd isn't a git work tree, the repo has no HEAD, or enabled is
// false. A non-nil error means a worktree was attempted and failed; the
// caller should carry on with the disabled Workspace.
func Start(ctx context.Context, cwd, id string, enabled bool) (*Workspace, error) {
	w := &Workspace{}
	if !enabled {
		return w, nil
	}
	root, sha, gitDir, ok := probeRepo(ctx, cwd)
	if !ok {
		// Not a git work tree, or an empty repo (no HEAD to root the worktree at).
		return w, nil
	}

	if id == "" {
		id = time.Now().Format("20060102-150405")
	}
	branch := "lambda/" + id
	path := filepath.Join(root, ".lambda", "worktrees", id)
	baseBranch := currentBranch(ctx, root)

	if gitDir != "" {
		_ = ensureExclude(filepath.Join(gitDir, "info", "exclude"))
	}
	if err := runGit(ctx, root, "worktree", "add", "-b", branch, path); err != nil {
		return w, fmt.Errorf("git worktree add: %w", err)
	}
	w.Enabled = true
	w.Path = path
	w.Branch = branch
	w.BaseBranch = baseBranch
	w.StartSHA = sha
	w.RepoRoot = root
	return w, nil
}

// Action selects how Finalize handles a Workspace that has changes.
type Action int

const (
	// ActionKeep leaves the worktree and branch in place and writes a
	// summary with merge/discard command hints to the caller's writer.
	ActionKeep Action = iota
	// ActionDiscard removes the worktree and branch and writes a single
	// confirmation line.
	ActionDiscard
)

// Summary returns a short body describing the Workspace's changes (branch,
// path, committed diff stat, uncommitted status) plus whether it has
// anything worth showing. A disabled Workspace, or one with no dirty
// files and no advanced HEAD, returns ("", false). Intended for an
// interactive prompt that asks the user keep-or-discard before quit.
func (w *Workspace) Summary(ctx context.Context) (string, bool) {
	if !w.Enabled {
		return "", false
	}
	advanced := headAdvanced(ctx, w.Path, w.StartSHA)
	dirty := isDirty(ctx, w.Path)
	if !advanced && !dirty {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "branch: %s\n", w.Branch)
	fmt.Fprintf(&b, "path:   %s", w.Path)
	if advanced {
		if out, err := runGitOutput(ctx, w.Path, "diff", "--stat", w.StartSHA+"..HEAD"); err == nil {
			b.WriteString("\n")
			writeIndented(&b, "committed:", out)
		}
	}
	if dirty {
		if out, err := runGitOutput(ctx, w.Path, "status", "--short"); err == nil {
			b.WriteString("\n")
			writeIndented(&b, "uncommitted:", out)
		}
	}
	return b.String(), true
}

// Status returns a user-facing snapshot of the live Workspace. A nil or
// disabled Workspace returns "" — the caller (Session) owns the
// disabled-mode rendering, which needs the original cwd it captured at
// startup.
func (w *Workspace) Status(ctx context.Context) string {
	if w == nil || !w.Enabled {
		return ""
	}

	var b strings.Builder
	fmt.Fprintln(&b, "worktree: active")
	fmt.Fprintf(&b, "branch:   %s\n", w.Branch)
	if w.BaseBranch != "" {
		fmt.Fprintf(&b, "base:     %s @ %s\n", w.BaseBranch, shortSHA(w.StartSHA))
	} else {
		fmt.Fprintf(&b, "base:     %s\n", shortSHA(w.StartSHA))
	}
	fmt.Fprintf(&b, "path:     %s\n", w.Path)

	advanced := headAdvanced(ctx, w.Path, w.StartSHA)
	dirty := isDirty(ctx, w.Path)
	if !advanced && !dirty {
		fmt.Fprint(&b, "changes:  none")
		return b.String()
	}
	if advanced {
		if out, err := runGitOutput(ctx, w.Path, "diff", "--stat", w.StartSHA+"..HEAD"); err == nil && len(bytes.TrimSpace(out)) > 0 {
			writeIndented(&b, "committed:", out)
		} else {
			fmt.Fprintln(&b, "committed: HEAD moved")
		}
	}
	if dirty {
		if out, err := runGitOutput(ctx, w.Path, "status", "--short"); err == nil && len(bytes.TrimSpace(out)) > 0 {
			writeIndented(&b, "uncommitted:", out)
		} else {
			fmt.Fprintln(&b, "uncommitted: status unavailable")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Finalize tears down or preserves the Workspace.
//
//   - Disabled Workspace: no-op.
//   - Clean Workspace (no dirty files, HEAD didn't move): worktree and
//     branch are removed and the empty .lambda/ parents cleaned up. Silent.
//   - Has changes + ActionDiscard: same removal as clean; w gets a one-line
//     "discarded …" notice.
//   - Has changes + ActionKeep: worktree and branch are kept; out gets the
//     full summary plus review/merge/discard command hints.
//
// Safe to call on a disabled Workspace.
func (w *Workspace) Finalize(ctx context.Context, out io.Writer, action Action) {
	if !w.Enabled {
		return
	}
	advanced := headAdvanced(ctx, w.Path, w.StartSHA)
	dirty := isDirty(ctx, w.Path)
	hasChanges := advanced || dirty

	if !hasChanges {
		w.removeWorktree(ctx)
		return
	}
	if action == ActionDiscard {
		w.removeWorktree(ctx)
		fmt.Fprintf(out, "lambda: discarded session branch %s\n", w.Branch)
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "lambda: session left changes on branch %s\n", w.Branch)
	fmt.Fprintf(out, "  path:   %s\n", w.Path)
	if advanced {
		if buf, err := runGitOutput(ctx, w.Path, "diff", "--stat", w.StartSHA+"..HEAD"); err == nil {
			writeIndented(out, "  committed:", buf)
		}
	}
	if dirty {
		if buf, err := runGitOutput(ctx, w.Path, "status", "--short"); err == nil {
			writeIndented(out, "  uncommitted:", buf)
		}
	}
	fmt.Fprintf(out, "  review:  git -C %s log %s..HEAD\n", w.Path, w.StartSHA)
	fmt.Fprintf(out, "  merge:   git merge %s\n", w.Branch)
	fmt.Fprintf(out, "  discard: git worktree remove --force %s && git branch -D %s\n", w.Path, w.Branch)
}

// removeWorktree drops the Workspace's worktree + branch and tries to remove
// the now-empty .lambda/worktrees/ and .lambda/ parents. Errors are ignored:
// non-empty parents are expected when sibling Workspaces are still running,
// and the worktree/branch teardown is best-effort either way.
//
// On Windows a directory that's any process's cwd cannot be removed, so
// if the caller is running with cwd inside the worktree (lambda itself
// does this in main) we step out to the repo root first.
func (w *Workspace) removeWorktree(ctx context.Context) {
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(w.Path, cwd); err == nil && !strings.HasPrefix(rel, "..") {
			_ = os.Chdir(w.RepoRoot)
		}
	}
	_ = runGit(ctx, w.RepoRoot, "worktree", "remove", "--force", w.Path)
	_ = runGit(ctx, w.RepoRoot, "branch", "-D", w.Branch)
	_ = os.Remove(filepath.Join(w.RepoRoot, ".lambda", "worktrees"))
	_ = os.Remove(filepath.Join(w.RepoRoot, ".lambda"))
}

func writeIndented(w io.Writer, header string, body []byte) {
	body = bytes.TrimRight(body, "\n")
	if len(body) == 0 {
		return
	}
	fmt.Fprintln(w, header)
	for line := range strings.SplitSeq(string(body), "\n") {
		fmt.Fprintln(w, "    "+line)
	}
}

// --- git helpers ---

// probeRepo queries repo toplevel, HEAD sha, and the common git dir in a
// single `git rev-parse` invocation. Returns ok=false if cwd isn't a git
// work tree or the repo has no HEAD (empty repo). gitDir is resolved
// against cwd when git prints a relative path.
func probeRepo(ctx context.Context, cwd string) (root, sha, gitDir string, ok bool) {
	out, err := runGitOutput(ctx, cwd, "rev-parse", "--show-toplevel", "HEAD", "--git-common-dir")
	if err != nil {
		return "", "", "", false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 3 {
		return "", "", "", false
	}
	root = strings.TrimSpace(lines[0])
	sha = strings.TrimSpace(lines[1])
	gitDir = strings.TrimSpace(lines[2])
	if gitDir != "" && !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(cwd, gitDir)
	}
	if root == "" || sha == "" {
		return "", "", "", false
	}
	return root, sha, gitDir, true
}

func currentBranch(ctx context.Context, cwd string) string {
	out, err := runGitOutput(ctx, cwd, "branch", "--show-current")
	if err == nil {
		if branch := strings.TrimSpace(string(out)); branch != "" {
			return branch
		}
	}
	out, err = runGitOutput(ctx, cwd, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return ""
	}
	return "detached:" + sha
}

// isDirty reports whether cwd has any working-tree or index changes.
// On probe failure we conservatively return true so Finalize doesn't silently
// delete a worktree whose state couldn't be verified.
func isDirty(ctx context.Context, cwd string) bool {
	out, err := runGitOutput(ctx, cwd, "status", "--porcelain")
	if err != nil {
		return true
	}
	return len(bytes.TrimSpace(out)) > 0
}

// headAdvanced reports whether cwd's HEAD has moved past startSHA. Fail-safe
// to true for the same reason as isDirty.
func headAdvanced(ctx context.Context, cwd, startSHA string) bool {
	out, err := runGitOutput(ctx, cwd, "rev-parse", "HEAD")
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != startSHA
}

// ensureExclude appends `/.lambda/` to the given git exclude file if it
// isn't already present, so the worktree dir is ignored in the main repo.
func ensureExclude(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	needle := "/.lambda/"
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.TrimSpace(line) == needle {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString("# lambda agent worktrees\n/.lambda/\n")
	return err
}

func runGit(ctx context.Context, cwd string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func runGitOutput(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	return cmd.Output()
}
