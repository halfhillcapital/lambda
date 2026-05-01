package tui

import (
	"context"
	"testing"

	"lambda/internal/agent"
)

func TestTurnRunnerStartEmitsTaggedEvents(t *testing.T) {
	runner := newTurnRunner(func(ctx context.Context, input string, out chan<- agent.Event) {
		defer close(out)
		if input != "hello" {
			t.Errorf("input=%q, want hello", input)
		}
		out <- agent.EventContentDelta{Text: "hi"}
	})

	msg := runner.Start("hello")()

	evMsg, ok := msg.(agentEventMsg)
	if !ok {
		t.Fatalf("msg=%T, want agentEventMsg", msg)
	}
	if evMsg.turn != 1 {
		t.Fatalf("turn=%d, want 1", evMsg.turn)
	}
	if _, ok := evMsg.ev.(agent.EventContentDelta); !ok {
		t.Fatalf("event=%T, want EventContentDelta", evMsg.ev)
	}
}

func TestTurnRunnerWaitReportsClosedChannel(t *testing.T) {
	runner := newTurnRunner(func(ctx context.Context, input string, out chan<- agent.Event) {
		close(out)
	})

	msg := runner.Start("hello")()

	ended, ok := msg.(turnEndedMsg)
	if !ok {
		t.Fatalf("msg=%T, want turnEndedMsg", msg)
	}
	if ended.turn != 1 {
		t.Fatalf("turn=%d, want 1", ended.turn)
	}
}

func TestTurnRunnerCancelClosesCurrentTurn(t *testing.T) {
	cancelled := make(chan struct{})
	runner := newTurnRunner(func(ctx context.Context, input string, out chan<- agent.Event) {
		defer close(out)
		<-ctx.Done()
		close(cancelled)
	})

	cmd := runner.Start("hello")
	runner.Cancel()
	msg := cmd()

	if _, ok := msg.(turnEndedMsg); !ok {
		t.Fatalf("msg=%T, want turnEndedMsg", msg)
	}
	<-cancelled
}

func TestTurnRunnerStartAdvancesCurrentTurn(t *testing.T) {
	block := make(chan struct{})
	runner := newTurnRunner(func(ctx context.Context, input string, out chan<- agent.Event) {
		defer close(out)
		<-block
	})

	runner.Start("first")
	runner.Start("second")

	if runner.Current(1) {
		t.Fatalf("turn 1 should be stale")
	}
	if !runner.Current(2) {
		t.Fatalf("turn 2 should be current")
	}
	close(block)
}
