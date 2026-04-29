package agent

import "context"

// Approver decides whether a destructive tool call may proceed and records
// any session-level allowlist updates the user requests along the way. It is
// the single owner of "may this call run?" — folding together the yolo
// bypass, the per-session allowlist (`alwaysAll`, `allowedTools`), the
// static Policy, and the interactive Confirmer round-trip.
type Approver struct {
	policy    Policy
	confirmer Confirmer

	yolo         bool
	alwaysAll    bool
	allowedTools map[string]bool
}

// NewApprover builds an Approver. yolo (typically the --yolo flag) bypasses
// every check and is also the value alwaysAll resets to. policy is the
// static rule layer; confirmer is the interactive fallback, invoked only
// when both the policy and the session allowlist defer.
func NewApprover(policy Policy, confirmer Confirmer, yolo bool) *Approver {
	return &Approver{
		policy:       policy,
		confirmer:    confirmer,
		yolo:         yolo,
		alwaysAll:    yolo,
		allowedTools: map[string]bool{},
	}
}

// Allow reports whether the destructive tool call (name, rawArgs) may
// proceed. Session state (alwaysAll, allowedTools) is updated internally
// when the confirmer's reply asks for it.
func (a *Approver) Allow(ctx context.Context, name, rawArgs string) bool {
	if a.alwaysAll || a.allowedTools[name] {
		return true
	}
	switch a.policy(name, rawArgs) {
	case AutoAllow:
		return true
	case AutoDeny:
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
