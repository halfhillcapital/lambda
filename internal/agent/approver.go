package agent

import (
	"context"

	"lambda/internal/tools"
)

// Approver decides whether a tool call may proceed and records any
// session-level allowlist updates the user requests along the way. It folds
// together the yolo bypass, the per-session allowlist (`alwaysAll`,
// `allowedTools`), each tool's static Classify verdict, and the interactive
// Confirmer round-trip.
type Approver struct {
	registry  tools.Registry
	confirmer Confirmer

	yolo         bool
	alwaysAll    bool
	allowedTools map[string]bool
}

// NewApprover builds an Approver. yolo (typically the --yolo flag) bypasses
// every check and is also the value alwaysAll resets to. registry supplies
// the per-tool Classify rule layer; confirmer is the interactive fallback,
// invoked only when both the tool's verdict and the session allowlist defer.
func NewApprover(registry tools.Registry, confirmer Confirmer, yolo bool) *Approver {
	return &Approver{
		registry:     registry,
		confirmer:    confirmer,
		yolo:         yolo,
		alwaysAll:    yolo,
		allowedTools: map[string]bool{},
	}
}

// Allow reports whether the tool call (name, rawArgs) may proceed. Session
// state (alwaysAll, allowedTools) is updated internally when the confirmer's
// reply asks for it. Unknown tool names are allowed through so the registry
// can produce its own "schema error: unknown tool" message.
func (a *Approver) Allow(ctx context.Context, name, rawArgs string) bool {
	if a.alwaysAll || a.allowedTools[name] {
		return true
	}
	tool, known := a.registry[name]
	if !known {
		return true
	}
	switch tool.Classify(rawArgs) {
	case tools.AutoAllow:
		return true
	case tools.AutoDeny:
		return false
	}
	switch a.confirmer(ctx, name, rawArgs) {
	case DecisionAllow:
		return true
	case DecisionAlwaysTool:
		a.allowedTools[name] = true
		return true
	case DecisionAlwaysAll:
		a.alwaysAll = true
		return true
	default: // DecisionDeny
		return false
	}
}

// Reset clears the per-session allowlist and reverts alwaysAll to the
// initial yolo value. Used by `/new` to start a fresh approval slate.
func (a *Approver) Reset() {
	a.allowedTools = map[string]bool{}
	a.alwaysAll = a.yolo
}
