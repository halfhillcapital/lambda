package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"lambda/internal/agent"
	"lambda/internal/config"
	"lambda/internal/tui"
)

// runOneShot runs a single turn, prints assistant content to stdout and
// tool activity to stderr, and returns the process exit code when the turn
// completes. It never calls os.Exit directly so the caller's deferred
// cleanup (notably worktree teardown) runs on every path.
func runOneShot(ctx context.Context, cfg *config.Config, systemPrompt string, pol agent.Policy, userInput string) int {
	stderrIsTTY := term.IsTerminal(int(os.Stderr.Fd()))

	// In non-interactive mode we cannot prompt; the agent treats all destructive
	// tool calls as denied unless --yolo was passed (which Agent honors internally).
	deny := func(ctx context.Context, name, args string) agent.Decision {
		fmt.Fprintf(os.Stderr, "[denied %s: non-interactive one-shot requires --yolo for destructive tools]\n", name)
		return agent.DecisionDeny
	}

	a := agent.New(cfg, systemPrompt, pol, deny)

	events := make(chan agent.Event, 64)
	go a.Run(ctx, userInput, events)

	toolColor := func(s string) string {
		if stderrIsTTY {
			return tui.ToolCallStyle.Render(s)
		}
		return s
	}

	exitCode := 0
	for ev := range events {
		switch e := ev.(type) {
		case agent.EventContentDelta:
			fmt.Fprint(os.Stdout, e.Text)
		case agent.EventAssistantDone:
			if cfg.NoStream {
				fmt.Fprint(os.Stdout, e.Text)
			}
			fmt.Fprintln(os.Stdout)
		case agent.EventToolStart:
			fmt.Fprintln(os.Stderr, toolColor("→ "+e.Name+" "+tui.TerseArgs(e.Name, e.Args)))
		case agent.EventToolResult:
			// Truncated preview goes to stderr so piping stdout stays clean.
			if first := firstLine(e.Result); first != "" {
				fmt.Fprintln(os.Stderr, "  "+tui.Truncate(first, 200))
			}
		case agent.EventToolDenied:
			fmt.Fprintln(os.Stderr, "  (denied)")
		case agent.EventTurnDone:
			if e.Reason != "done" {
				fmt.Fprintln(os.Stderr, "[", e.Reason, "]")
				exitCode = 1
			}
		case agent.EventError:
			fmt.Fprintln(os.Stderr, "error:", e.Err)
			exitCode = 1
		}
	}
	// SIGINT/SIGTERM came in during the turn — use the conventional 130.
	if errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	return exitCode
}

func firstLine(s string) string {
	s = strings.TrimLeft(s, "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
