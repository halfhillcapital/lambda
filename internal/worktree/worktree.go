// Package worktree manages a per-session git worktree so the agent edits
// and tool calls land on an isolated branch. A clean session ("nothing
// changed") is garbage-collected silently; a dirty session leaves the
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

// Session carries the state of a single agent invocation's worktree. When
// Enabled is false the session is a no-op: Start decided the environment
// didn't qualify (not a git repo, no HEAD, user opted out), and End does
// nothing.
type Session struct {
	Enabled     bool
	Path        string // absolute worktree path
	Branch      string // branch created alongside the worktree
	StartSHA    string // parent HEAD when the worktree was created
	RepoRoot    string // parent repo toplevel
	OriginalCwd string
}

// Cwd returns the working directory the caller should use. With a live
// worktree that's the worktree path; otherwise the caller's original cwd.
func (s *Session) Cwd() string {
	if s.Enabled {
		return s.Path
	}
	return s.OriginalCwd
}

// Start creates a worktree at <repo>/.lambda/worktrees/<ts> on a new branch
// lambda/<ts> rooted at HEAD. It returns a disabled Session (no error) when
// cwd isn't a git work tree, the repo has no HEAD, or enabled is false. A
// non-nil error means a worktree was attempted and failed; the caller
// should carry on with the disabled Session.
func Start(ctx context.Context, cwd string, enabled bool) (*Session, error) {
	s := &Session{OriginalCwd: cwd}
	if !enabled {
		return s, nil
	}
	root, ok := repoRoot(ctx, cwd)
	if !ok {
		return s, nil
	}
	sha, ok := headSHA(ctx, cwd)
	if !ok {
		// Empty repo (no commits) — git worktree add requires a commit.
		return s, nil
	}
	gitDir, _ := commonGitDir(ctx, cwd)

	ts := time.Now().Format("20060102-150405")
	branch := "lambda/" + ts
	path := filepath.Join(root, ".lambda", "worktrees", ts)

	if gitDir != "" {
		_ = ensureExclude(filepath.Join(gitDir, "info", "exclude"))
	}
	if err := runGit(ctx, root, "worktree", "add", "-b", branch, path); err != nil {
		return s, fmt.Errorf("git worktree add: %w", err)
	}
	s.Enabled = true
	s.Path = path
	s.Branch = branch
	s.StartSHA = sha
	s.RepoRoot = root
	return s, nil
}

// End finalizes the session. A clean worktree (no new commits, no dirty
// files) is removed and its branch deleted. Otherwise it's left in place
// and a short summary is written to w so the user can merge or discard it.
// Safe to call on a disabled session.
func (s *Session) End(ctx context.Context, w io.Writer) {
	if !s.Enabled {
		return
	}
	dirty := isDirty(ctx, s.Path)
	advanced := headAdvanced(ctx, s.Path, s.StartSHA)
	if !dirty && !advanced {
		_ = runGit(ctx, s.RepoRoot, "worktree", "remove", "--force", s.Path)
		_ = runGit(ctx, s.RepoRoot, "branch", "-D", s.Branch)
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "lambda: session left changes on branch %s\n", s.Branch)
	fmt.Fprintf(w, "  path:   %s\n", s.Path)
	if advanced {
		if out, err := runGitOutput(ctx, s.Path, "diff", "--stat", s.StartSHA+"..HEAD"); err == nil {
			writeIndented(w, "  committed:", out)
		}
	}
	if dirty {
		if out, err := runGitOutput(ctx, s.Path, "status", "--short"); err == nil {
			writeIndented(w, "  uncommitted:", out)
		}
	}
	fmt.Fprintf(w, "  review:  git -C %s log %s..HEAD\n", s.Path, s.StartSHA)
	fmt.Fprintf(w, "  merge:   git merge %s\n", s.Branch)
	fmt.Fprintf(w, "  discard: git worktree remove --force %s && git branch -D %s\n", s.Path, s.Branch)
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

func repoRoot(ctx context.Context, cwd string) (string, bool) {
	out, err := runGitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func commonGitDir(ctx context.Context, cwd string) (string, bool) {
	out, err := runGitOutput(ctx, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return p, true
}

func headSHA(ctx context.Context, cwd string) (string, bool) {
	out, err := runGitOutput(ctx, cwd, "rev-parse", "HEAD")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// isDirty reports whether cwd has any working-tree or index changes.
// On probe failure we conservatively return true so End doesn't silently
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
