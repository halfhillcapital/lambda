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
	"lambda/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	cwd, _ := os.Getwd()
	systemPrompt := prompt.Build(cwd)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if p, ok := resolveOneShotPrompt(cfg); ok {
		if err := runOneShot(ctx, cfg, systemPrompt, p); err != nil {
			fmt.Fprintln(os.Stderr, tui.ErrorStyle.Render("error: "+err.Error()))
			os.Exit(1)
		}
		return
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "lambda: stdin is a TTY but stdout is not — won't start REPL. Pass a prompt via -p or positional argument.")
		os.Exit(2)
	}

	if err := tui.Run(ctx, cfg, systemPrompt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
