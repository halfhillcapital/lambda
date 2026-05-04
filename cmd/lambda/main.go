// Command lambda is a minimal CLI coding agent that talks to any
// OpenAI-compatible chat endpoint (Ollama, LM Studio, vLLM, …).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"lambda/internal/config"
	"lambda/internal/prompt"
	"lambda/internal/session"
	"lambda/internal/skills"
	"lambda/internal/tools"
	"lambda/internal/tui"
	"lambda/internal/worktree"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, _ := os.Getwd()

	// Oneshot subagent runs are ephemeral — no manifest, no .lambda/sessions
	// dir. Persistence is justified by resume/enumerate/suspend, none of
	// which apply to a run that exits in one shot.
	_, oneshot := resolveOneShotPrompt(cfg)
	sess, err := startOrResumeSession(ctx, cfg, cwd, oneshot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lambda:", err)
		return 1
	}
	// Default to ActionKeep so non-interactive paths and unexpected exits
	// preserve any work; the TUI overrides via the user's modal choice.
	chosenAction := worktree.ActionKeep
	defer func() {
		// Suspend already persisted state and released the lock; tearing
		// the Workspace down here would defeat /resume.
		if sess.Suspended() {
			return
		}
		sess.Finalize(context.Background(), os.Stderr, chosenAction)
	}()

	if ws := sess.Workspace(); ws.Enabled {
		if err := os.Chdir(ws.Path); err != nil {
			fmt.Fprintln(os.Stderr, "lambda: chdir to worktree failed:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lambda: session worktree %s (branch %s)\n", ws.Path, ws.Branch)
	}

	skillIdx := skills.Load(skills.RootsFromEnv(sess.Cwd(), os.Getenv("LAMBDA_SKILLS_DIR")), os.Stderr)
	buildSections := func() prompt.Sections {
		var pc prompt.ProjectContext
		if !cfg.NoProjectContext {
			pc = prompt.LoadProjectContext(sess.Cwd(), os.Stderr)
		}
		return prompt.BuildSections(sess.Cwd(), skillIdx, pc)
	}
	sections := buildSections()
	registry := tools.New(sess.Cwd(), skillIdx)

	if p, ok := resolveOneShotPrompt(cfg); ok {
		return runOneShot(ctx, cfg, sections.Joined(), registry, p)
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "lambda: stdin is a TTY but stdout is not — won't start REPL. Pass a prompt via -p or positional argument.")
		return 2
	}

	action, err := tui.Run(ctx, cfg, sections, buildSections, registry, skillIdx, sess)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	chosenAction = action
	return 0
}

// startOrResumeSession dispatches between session.Start and session.Resume
// based on --resume. Resume requires a git repo (we need a stable repo
// root to look manifests up under); when --resume is set without one,
// the call errors. Start tolerates a non-repo cwd (returns a disabled
// Workspace).
func startOrResumeSession(ctx context.Context, cfg *config.Config, cwd string, oneshot bool) (*session.Session, error) {
	if strings.TrimSpace(cfg.Resume) != "" {
		root, err := repoTopLevel(ctx, cwd)
		if err != nil {
			return nil, fmt.Errorf("--resume requires a git repository: %w", err)
		}
		return session.Resume(ctx, root, cwd, cfg.Resume)
	}
	sess, err := session.Start(ctx, cwd, !cfg.NoWorktree, !oneshot, cfg.Model, string(cfg.Provider))
	if err != nil {
		fmt.Fprintln(os.Stderr, "lambda: worktree disabled:", err)
	}
	return sess, nil
}

// repoTopLevel asks git for the toplevel of the repo containing cwd.
func repoTopLevel(ctx context.Context, cwd string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git rev-parse returned empty toplevel")
	}
	return root, nil
}

// resolveOneShotPrompt picks the one-shot prompt from (in precedence order):
// flag -p, positional args, or piped stdin. Returns ("", false) if none apply.
func resolveOneShotPrompt(cfg *config.Config) (string, bool) {
	if s := strings.TrimSpace(cfg.Prompt); s != "" {
		return s, true
	}
	if len(cfg.Args) > 0 {
		return strings.Join(cfg.Args, " "), true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := io.ReadAll(os.Stdin)
		if err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" {
				return s, true
			}
		}
	}
	return "", false
}
