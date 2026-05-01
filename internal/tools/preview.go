package tools

// PreviewKind describes the semantic role of one preview line. UI adapters
// decide how to style each kind; tools decide what lines belong in the preview.
type PreviewKind int

const (
	PreviewText PreviewKind = iota
	PreviewCommand
	PreviewRemoved
	PreviewAdded
)

// PreviewLine is one display line for a pending tool call preview.
type PreviewLine struct {
	Kind PreviewKind
	Text string
}

// Previewer is the optional interface for tools that can provide a richer
// confirmation preview than their one-line summary.
type Previewer interface {
	Preview(rawArgs string) []PreviewLine
}

func fallbackPreview(rawArgs string) []PreviewLine {
	return []PreviewLine{{Kind: PreviewText, Text: Truncate(rawArgs, 400)}}
}

// Preview returns a structured confirmation preview for a pending tool call.
// Unknown tools, malformed arguments, and tools without a rich preview fall
// back to a truncated raw-argument view.
func (r Registry) Preview(name, rawArgs string) []PreviewLine {
	t, ok := r[name]
	if !ok {
		return fallbackPreview(rawArgs)
	}
	p, ok := t.(Previewer)
	if !ok {
		return fallbackPreview(rawArgs)
	}
	lines := p.Preview(rawArgs)
	if len(lines) == 0 {
		return fallbackPreview(rawArgs)
	}
	return lines
}
