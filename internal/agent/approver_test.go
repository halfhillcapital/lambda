package agent

import (
	"context"
	"testing"

	"lambda/internal/ai"
	"lambda/internal/tools"
)

// stubTool is a minimal tools.Tool fake that returns a fixed Classify verdict
// and records every (name, args) it was asked about.
type stubTool struct {
	name    string
	verdict tools.Verdict
	calls   *[][2]string
}

func (s *stubTool) Name() string            { return s.name }
func (s *stubTool) Schema() ai.ToolSpec     { return ai.ToolSpec{Name: s.name} }
func (s *stubTool) Summarize(string) string { return "" }
func (s *stubTool) Classify(args string) tools.Verdict {
	*s.calls = append(*s.calls, [2]string{s.name, args})
	return s.verdict
}
func (s *stubTool) Execute(context.Context, string) string { return "" }

// recordingRegistry builds a registry whose every Classify call is recorded
// and returns the same fixed verdict.
func recordingRegistry(verdict tools.Verdict, names ...string) (tools.Registry, *[][2]string) {
	var calls [][2]string
	r := tools.Registry{}
	for _, n := range names {
		r[n] = &stubTool{name: n, verdict: verdict, calls: &calls}
	}
	return r, &calls
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
	reg, polCalls := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(reg, conf, true)

	if !a.Allow(context.Background(), "write", `{"path":"x"}`) {
		t.Error("yolo: want allow")
	}
	if len(*polCalls) != 0 {
		t.Errorf("yolo: classify invoked %d times, want 0", len(*polCalls))
	}
	if len(*confCalls) != 0 {
		t.Errorf("yolo: confirmer invoked %d times, want 0", len(*confCalls))
	}
}

func TestApprover_ClassifyAutoAllow(t *testing.T) {
	reg, _ := recordingRegistry(tools.AutoAllow, "write")
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(reg, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Error("auto-allow: want allow")
	}
	if len(*confCalls) != 0 {
		t.Errorf("auto-allow: confirmer invoked %d times, want 0", len(*confCalls))
	}
}

func TestApprover_ClassifyAutoDeny(t *testing.T) {
	reg, _ := recordingRegistry(tools.AutoDeny, "write")
	conf, confCalls := recordingConfirmer(DecisionAllow)
	a := NewApprover(reg, conf, false)

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
			reg, _ := recordingRegistry(tools.Prompt, "write")
			conf, confCalls := recordingConfirmer(c.decision)
			a := NewApprover(reg, conf, false)

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
	reg, _ := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(reg, conf, false)

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
	reg, _ := recordingRegistry(tools.Prompt, "write", "bash")
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(reg, conf, false)

	if !a.Allow(context.Background(), "write", "{}") {
		t.Fatal("write: want allow")
	}
	a.Allow(context.Background(), "bash", `{"command":"ls"}`)
	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (alwaysTool=write should not allow bash)", len(*confCalls))
	}
}

func TestApprover_AlwaysAllStickyAcrossTools(t *testing.T) {
	reg, polCalls := recordingRegistry(tools.Prompt, "write", "bash")
	conf, confCalls := recordingConfirmer(DecisionAlwaysAll)
	a := NewApprover(reg, conf, false)

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
		t.Errorf("classify invoked %d times, want 1 (alwaysAll bypasses classify too)", len(*polCalls))
	}
}

func TestApprover_ResetClearsAllowlist(t *testing.T) {
	reg, _ := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionAlwaysTool)
	a := NewApprover(reg, conf, false)

	a.Allow(context.Background(), "write", "{}")
	a.Reset()
	a.Allow(context.Background(), "write", "{}")

	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (Reset should clear allowlist)", len(*confCalls))
	}
}

func TestApprover_ResetClearsAlwaysAll(t *testing.T) {
	reg, _ := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionAlwaysAll)
	a := NewApprover(reg, conf, false)

	a.Allow(context.Background(), "write", "{}")
	a.Reset()
	a.Allow(context.Background(), "write", "{}")

	if len(*confCalls) != 2 {
		t.Errorf("confirmer invoked %d times, want 2 (Reset should clear alwaysAll)", len(*confCalls))
	}
}

func TestApprover_ResetPreservesYolo(t *testing.T) {
	reg, polCalls := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(reg, conf, true)

	a.Reset()
	if !a.Allow(context.Background(), "write", "{}") {
		t.Error("after Reset with yolo=true: want allow")
	}
	if len(*polCalls) != 0 || len(*confCalls) != 0 {
		t.Errorf("classify/confirmer should not be consulted when yolo=true (after Reset): pol=%d conf=%d",
			len(*polCalls), len(*confCalls))
	}
}

// TestApprover_UnknownToolFallsThrough pins the contract that an unknown
// tool name is allowed through Approver so the registry can produce its own
// "schema error: unknown tool" message rather than the generic denial.
func TestApprover_UnknownToolFallsThrough(t *testing.T) {
	reg, polCalls := recordingRegistry(tools.Prompt, "write")
	conf, confCalls := recordingConfirmer(DecisionDeny)
	a := NewApprover(reg, conf, false)

	if !a.Allow(context.Background(), "no_such_tool", "{}") {
		t.Error("unknown tool: want allow (so registry can return schema error)")
	}
	if len(*polCalls) != 0 || len(*confCalls) != 0 {
		t.Errorf("unknown tool should bypass classify and confirmer: pol=%d conf=%d", len(*polCalls), len(*confCalls))
	}
}
