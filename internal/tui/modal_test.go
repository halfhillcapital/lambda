package tui

import (
	"testing"

	"lambda/internal/agent"
	"lambda/internal/worktree"
)

func TestApprovalDecisionForKey(t *testing.T) {
	tests := []struct {
		key  string
		want agent.Decision
	}{
		{key: "y", want: agent.DecisionAllow},
		{key: "Y", want: agent.DecisionAllow},
		{key: "enter", want: agent.DecisionAllow},
		{key: "a", want: agent.DecisionAlwaysTool},
		{key: "A", want: agent.DecisionAlwaysAll},
		{key: "n", want: agent.DecisionDeny},
		{key: "N", want: agent.DecisionDeny},
		{key: "esc", want: agent.DecisionDeny},
		{key: "ctrl+c", want: agent.DecisionDeny},
	}

	for _, tt := range tests {
		got, ok := approvalDecisionForKey(tt.key)
		if !ok {
			t.Fatalf("approvalDecisionForKey(%q) ok=false, want true", tt.key)
		}
		if got != tt.want {
			t.Fatalf("approvalDecisionForKey(%q)=%v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestApprovalDialogHandleKeyRepliesAndClears(t *testing.T) {
	dialog := newApprovalDialog(nil)
	reply := make(chan agent.Decision, 1)
	dialog.Open(&confirmRequest{name: "write", args: "{}", reply: reply})

	if !dialog.HandleKey("A") {
		t.Fatalf("HandleKey returned false, want true")
	}
	if dialog.Active() {
		t.Fatalf("dialog still active after decision")
	}
	if got := <-reply; got != agent.DecisionAlwaysAll {
		t.Fatalf("reply=%v, want always all", got)
	}
}

func TestApprovalDialogIgnoresUnknownKey(t *testing.T) {
	dialog := newApprovalDialog(nil)
	reply := make(chan agent.Decision, 1)
	dialog.Open(&confirmRequest{name: "write", args: "{}", reply: reply})

	if dialog.HandleKey("x") {
		t.Fatalf("HandleKey returned true, want false")
	}
	if !dialog.Active() {
		t.Fatalf("dialog inactive after ignored key")
	}
	if len(reply) != 0 {
		t.Fatalf("reply channel received decision for ignored key")
	}
}

func TestQuitDialogHandleKey(t *testing.T) {
	dialog := &quitDialog{}
	dialog.Open("changes")

	if got := dialog.HandleKey("x"); got != quitDialogNoop {
		t.Fatalf("HandleKey(x)=%v, want noop", got)
	}
	if !dialog.Active() {
		t.Fatalf("dialog inactive after ignored key")
	}
	if got := dialog.HandleKey("d"); got != quitDialogDiscard {
		t.Fatalf("HandleKey(d)=%v, want discard", got)
	}
	if dialog.Active() {
		t.Fatalf("dialog still active after discard")
	}
}

func TestQuitDialogResultWorktreeAction(t *testing.T) {
	if got := quitDialogKeep.WorktreeAction(); got != worktree.ActionKeep {
		t.Fatalf("keep action=%v, want keep", got)
	}
	if got := quitDialogDiscard.WorktreeAction(); got != worktree.ActionDiscard {
		t.Fatalf("discard action=%v, want discard", got)
	}
}
