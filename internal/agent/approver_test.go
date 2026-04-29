package agent

import (
	"context"
	"testing"
)

// recordingPolicy returns a Policy that records every (name, args) pair
// it was asked about, alongside a fixed Verdict.
func recordingPolicy(verdict Verdict) (Policy, *[][2]string) {
	var calls [][2]string
	return func(name, args string) Verdict {
		calls = append(calls, [2]string{name, args})
		return verdict
	}, &calls
}

// recordingConfirmer returns a Confirmer that records every invocation
// alongside a fixed Decision.
func recordingConfirmer(decision Decision) (Confirmer, *[][2]string) {
	var calls [][2]string
	return func(_ context.Context, name, args string) Decision {
		calls = append(calls, [2]string{name, args})
		return decision
	}, &calls
}

func TestApprover_YoloShortCircuits(t *testing.T) {
	pol, polCalls := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(pol, conf, true)

	if !a.Allow(context.Background(), "write", `{"path":"x"}`) {
		t.Error("yolo: want allow")
	}
	if len(*polCalls) != 0 {
		t.Errorf("yolo: policy invoked %d times, want 0", len(*polCalls))
	}
	if len(*confCalls) != 0 {
		t.Errorf("yolo: confirmer invoked %d times, want 0", len(*confCalls))
	}
}

func TestApprover_PolicyAutoAllow(t *testing.T) {
	pol, _ := recordingPolicy(AutoAllow)
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(pol, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Error("auto-allow: want allow")
	}
	if len(*confCalls) != 0 {
		t.Errorf("auto-allow: confirmer invoked %d times, want 0", len(*confCalls))
	}
}

func TestApprover_PolicyAutoDeny(t *testing.T) {
	pol, _ := recordingPolicy(AutoDeny)
	conf, confCalls := recordingConfirmer(DecisionAllow)
	a := NewApprover(pol, conf, false)

	if a.Allow(context.Background(), "write", "{}") {
		t.Error("auto-deny: want deny")
	}
	if len(*confCalls) != 0 {
		t.Errorf("auto-deny: confirmer invoked %d times, want 0", len(*confCalls))
	}
}

func TestApprover_PromptToConfirmer(t *testing.T) {
	cases := []struct {
		name      string
		decision  Decision
		wantAllow bool
	}{
		{"allow", DecisionAllow, true},
		{"deny", DecisionDeny, false},
		{"alwaysTool", DecisionAlwaysTool, true},
		{"alwaysAll", DecisionAlwaysAll, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol, _ := recordingPolicy(Prompt)
			conf, confCalls := recordingConfirmer(c.decision)
			a := NewApprover(pol, conf, false)

			got := a.Allow(context.Background(), "write", `{"path":"x"}`)
			if got != c.wantAllow {
				t.Errorf("decision=%v: allow=%v, want %v", c.decision, got, c.wantAllow)
			}
			if len(*confCalls) != 1 {
				t.Errorf("confirmer invoked %d times, want 1", len(*confCalls))
			}
		})
	}
}

func TestApprover_AlwaysToolStickyForSameName(t *testing.T) {
	pol, _ := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(pol, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Fatal("first call: want allow")
	}
	if !a.Allow(context.Background(), "write", "{}") {
		t.Fatal("second call: want allow (sticky)")
	}
	if len(*confCalls) != 1 {
		t.Errorf("confirmer invoked %d times, want 1 (second call should bypass)", len(*confCalls))
	}
}

func TestApprover_AlwaysToolDoesNotLeakAcrossNames(t *testing.T) {
	pol, _ := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(pol, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Fatal("write: want allow")
	}
	// "bash" was never approved — confirmer must be invoked again.
	a.Allow(context.Background(), "bash", `{"command":"ls"}`)
	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (alwaysTool=write should not allow bash)", len(*confCalls))
	}
}

func TestApprover_AlwaysAllStickyAcrossTools(t *testing.T) {
	pol, polCalls := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionAlwaysAll)
	a := NewApprover(pol, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Fatal("first call: want allow")
	}
	if !a.Allow(context.Background(), "bash", `{"command":"ls"}`) {
		t.Fatal("subsequent different tool: want allow (alwaysAll sticky)")
	}
	if len(*confCalls) != 1 {
		t.Errorf("confirmer invoked %d times, want 1", len(*confCalls))
	}
	if len(*polCalls) != 1 {
		t.Errorf("policy invoked %d times, want 1 (alwaysAll bypasses policy too)", len(*polCalls))
	}
}

func TestApprover_ResetClearsAllowlist(t *testing.T) {
	pol, _ := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(pol, conf, false)

	a.Allow(context.Background(), "write", "{}") // sets allowedTools["write"]
	a.Reset()
	a.Allow(context.Background(), "write", "{}") // confirmer should be hit again

	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (Reset should clear allowlist)", len(*confCalls))
	}
}

func TestApprover_ResetClearsAlwaysAll(t *testing.T) {
	pol, _ := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionAlwaysAll)
	a := NewApprover(pol, conf, false)

	a.Allow(context.Background(), "write", "{}") // sets alwaysAll
	a.Reset()
	a.Allow(context.Background(), "write", "{}") // confirmer should be hit again

	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (Reset should clear alwaysAll)", len(*confCalls))
	}
}

func TestApprover_ResetPreservesYolo(t *testing.T) {
	pol, polCalls := recordingPolicy(Prompt)
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(pol, conf, true)

	a.Reset()
	if !a.Allow(context.Background(), "write", "{}") {
		t.Error("after Reset with yolo=true: want allow")
	}
	if len(*polCalls) != 0 || len(*confCalls) != 0 {
		t.Errorf("policy/confirmer should not be consulted when yolo=true (after Reset): pol=%d conf=%d",
			len(*polCalls), len(*confCalls))
	}
}
