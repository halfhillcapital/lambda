// Command lambda is a minimal CLI coding agent that talks to any
// OpenAI-compatible chat endpoint (Ollama, LM Studio, vLLM, …).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"lambda/internal/config"
	"lambda/internal/prompt"
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
	session, err := worktree.Start(ctx, cwd, !cfg.NoWorktree)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lambda: worktree disabled:", err)
	}
	// Default to ActionKeep so non-interactive paths and unexpected exits
	// preserve any work; the TUI overrides via the user's modal choice.
	chosenAction := worktree.ActionKeep
	defer func() {
		session.Finalize(context.Background(), os.Stderr, chosenAction)
	}()

	if session.Enabled {
		if err := os.Chdir(session.Path); err != nil {
			fmt.Fprintln(os.Stderr, "lambda: chdir to worktree failed:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lambda: session worktree %s (branch %s)\n", session.Path, session.Branch)
	}

	skillIdx := skills.Load(skills.RootsFromEnv(session.Cwd(), os.Getenv("LAMBDA_SKILLS_DIR")), os.Stderr)
	buildSections := func() prompt.Sections {
		var pc prompt.ProjectContext
		if !cfg.NoProjectContext {
			pc = prompt.LoadProjectContext(session.Cwd(), os.Stderr)
		}
		return prompt.BuildSections(session.Cwd(), skillIdx, pc)
	}
	sections := buildSections()
	registry := tools.New(session.Cwd(), skillIdx)

	if p, ok := resolveOneShotPrompt(cfg); ok {
		return runOneShot(ctx, cfg, sections.Joined(), registry, p)
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "lambda: stdin is a TTY but stdout is not — won't start REPL. Pass a prompt via -p or positional argument.")
		return 2
	}

	action, err := tui.Run(ctx, cfg, sections, buildSections, registry, skillIdx, session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	chosenAction = action
	return 0
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
