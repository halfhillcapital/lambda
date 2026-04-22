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

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		if errors.Is(err, ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	cwd, _ := os.Getwd()
	systemPrompt := BuildSystemPrompt(cwd)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prompt, ok := resolveOneShotPrompt(cfg)
	if ok {
		if err := runOneShot(ctx, cfg, systemPrompt, prompt); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("error: "+err.Error()))
			os.Exit(1)
		}
		return
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "lambda: stdin is a TTY but stdout is not — won't start REPL. Pass a prompt via -p or positional argument.")
		os.Exit(2)
	}

	if err := RunTUI(ctx, cfg, systemPrompt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// resolveOneShotPrompt picks the one-shot prompt from (in precedence order):
// flag -p, positional args, or piped stdin. Returns ("", false) if none apply.
func resolveOneShotPrompt(cfg *Config) (string, bool) {
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

// runOneShot runs a single turn, prints assistant content to stdout and
// tool activity to stderr, and exits when the turn completes.
func runOneShot(ctx context.Context, cfg *Config, systemPrompt, userInput string) error {
	stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd()))
	stderrIsTTY := term.IsTerminal(int(os.Stderr.Fd()))

	confirmer := func(ctx context.Context, name, args string) Decision {
		fmt.Fprintf(os.Stderr, "[denied %s: non-interactive one-shot requires --yolo for destructive tools]\n", name)
		return DecisionDeny
	}

	agent := NewAgent(cfg, systemPrompt, confirmer)

	events := make(chan Event, 64)
	go agent.Run(ctx, userInput, events)

	steps := 0
	exitCode := 0

	toolColor := func(s string) string {
		if stderrIsTTY {
			return toolCallStyle.Render(s)
		}
		return s
	}
	assistantPlain := func(s string) string { return s } // stdout: keep clean unless TTY
	if stdoutIsTTY {
		assistantPlain = func(s string) string { return lipgloss.NewStyle().Render(s) }
	}
	_ = assistantPlain

	for ev := range events {
		switch e := ev.(type) {
		case EventContentDelta:
			fmt.Fprint(os.Stdout, e.Text)
		case EventAssistantDone:
			if cfg.NoStream {
				fmt.Fprint(os.Stdout, e.Text)
			}
			fmt.Fprintln(os.Stdout)
		case EventToolStart:
			steps++
			fmt.Fprintln(os.Stderr, toolColor("→ "+e.Name+" "+terseArgs(e.Name, e.Args)))
		case EventToolResult:
			// Truncated preview goes to stderr so piping stdout stays clean.
			first := firstLine(e.Result)
			if first != "" {
				fmt.Fprintln(os.Stderr, "  "+truncate(first, 200))
			}
		case EventToolDenied:
			fmt.Fprintln(os.Stderr, "  (denied)")
		case EventTurnDone:
			if e.Reason != "done" {
				fmt.Fprintln(os.Stderr, "[", e.Reason, "]")
				exitCode = 1
			}
		case EventError:
			fmt.Fprintln(os.Stderr, "error:", e.Err)
			exitCode = 1
		}
	}
	_ = steps
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimLeft(s, "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
