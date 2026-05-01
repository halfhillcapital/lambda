package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/agent"
)

type agentEventMsg struct {
	ev   agent.Event
	turn int
}

// turnEndedMsg is emitted when a turn event channel closes. It is the
// catch-all for cancellation paths where the agent exits without a final
// TurnDone/Error event.
type turnEndedMsg struct{ turn int }

type runTurnFunc func(context.Context, string, chan<- agent.Event)

type turnRunner struct {
	run    runTurnFunc
	turn   int
	active bool
	cancel context.CancelFunc
	events <-chan agent.Event
}

func newTurnRunner(run runTurnFunc) *turnRunner {
	return &turnRunner{run: run}
}

func (r *turnRunner) Active() bool {
	return r != nil && r.active
}

func (r *turnRunner) Current(turn int) bool {
	return r != nil && turn == r.turn
}

func (r *turnRunner) Start(input string) tea.Cmd {
	if r.cancel != nil {
		r.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan agent.Event, 128)
	r.turn++
	r.active = true
	r.cancel = cancel
	r.events = events
	go r.run(ctx, input, events)
	return waitForTurnEvent(events, r.turn)
}

func (r *turnRunner) Cancel() {
	if r == nil || r.cancel == nil {
		return
	}
	r.cancel()
}

func (r *turnRunner) Finish() {
	if r == nil {
		return
	}
	r.active = false
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *turnRunner) Wait() tea.Cmd {
	if r == nil || r.events == nil {
		return nil
	}
	return waitForTurnEvent(r.events, r.turn)
}

// waitForTurnEvent captures the channel and turn id at scheduling time so a
// stale waiter from a previous turn cannot be confused with the active turn.
func waitForTurnEvent(ch <-chan agent.Event, turn int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnEndedMsg{turn: turn}
		}
		return agentEventMsg{ev: ev, turn: turn}
	}
}
