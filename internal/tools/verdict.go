package tools

// Verdict is a tool's own classification of a pending call: whether it is
// safe enough to run unattended, must be denied outright, or should fall
// through to the user. Each Tool decides for itself.
type Verdict int

const (
	// Prompt means the tool has no opinion — fall through to the user.
	Prompt Verdict = iota
	// AutoAllow means the call is safe enough to run without asking.
	AutoAllow
	// AutoDeny means the tool refuses outright, regardless of user opinion.
	AutoDeny
)

func (v Verdict) String() string {
	switch v {
	case AutoAllow:
		return "auto-allow"
	case AutoDeny:
		return "auto-deny"
	default:
		return "prompt"
	}
}
