package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/session"
)

// suspend persists the current Session and quits without tearing down
// the Workspace. main.go's deferred Finalize sees the suspended state
// and bails. The lockfile is released so /resume <prefix> from a
// future process succeeds without stale-lock reclamation.
func (m *uiModel) suspend() tea.Cmd {
	if m.turn.Active() {
		m.transcript.AppendError("/suspend: cancel the current turn first (Ctrl+C), then retry")
		m.refreshViewport()
		return nil
	}
	if m.session == nil {
		m.transcript.AppendError("/suspend: no session attached")
		m.refreshViewport()
		return nil
	}
	if err := m.session.Suspend(context.Background()); err != nil {
		m.transcript.AppendError("/suspend: " + err.Error())
		m.refreshViewport()
		return nil
	}
	return tea.Quit
}

// listSessions reads every persisted Session under the current repo
// and renders the table from decisions §12. State is derived live:
// lockfile held by a live PID = "active"; otherwise "suspended".
func (m *uiModel) listSessions() {
	if m.session == nil {
		m.transcript.AppendError("/sessions: no session attached")
		m.refreshViewport()
		return
	}
	repoRoot := m.session.RepoRoot()
	if repoRoot == "" {
		m.transcript.AppendNotice("/sessions: not inside a git repo — no persisted sessions")
		m.refreshViewport()
		return
	}
	manifests, err := session.List(repoRoot)
	if err != nil {
		m.transcript.AppendError("/sessions: " + err.Error())
		m.refreshViewport()
		return
	}
	if len(manifests) == 0 {
		m.transcript.AppendNotice("no persisted sessions")
		m.refreshViewport()
		return
	}
	currentID := m.session.ID()
	m.transcript.AppendNotice(renderSessionsTable(repoRoot, manifests, currentID, time.Now()))
	m.refreshViewport()
}

// renderSessionsTable formats the manifests as the table from §12 of
// the redesign decisions. Sorted newest-first by LastActiveAt.
func renderSessionsTable(repoRoot string, manifests []*session.Manifest, currentID string, now time.Time) string {
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].LastActiveAt.After(manifests[j].LastActiveAt)
	})

	rows := make([][5]string, 0, len(manifests))
	for _, m := range manifests {
		marker := " "
		if m.ID == currentID {
			marker = "*"
		}
		title := "(untitled)"
		if m.Title != nil && strings.TrimSpace(*m.Title) != "" {
			title = *m.Title
		}
		state := "suspended"
		if pid, alive := session.LockHolder(repoRoot, m.ID); alive && pid > 0 {
			state = "active"
		}
		rows = append(rows, [5]string{m.ID, marker, title, humanizeAge(now.Sub(m.LastActiveAt)), state})
	}

	headers := [5]string{"ID", " ", "TITLE", "LAST ACTIVE", "STATE"}
	widths := [5]int{}
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}

	var b strings.Builder
	writeRow := func(cells [5]string) {
		for i, c := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			fmt.Fprintf(&b, "%-*s", widths[i], c)
		}
		b.WriteString("\n")
	}
	writeRow(headers)
	for _, r := range rows {
		writeRow(r)
	}
	return strings.TrimRight(b.String(), "\n")
}

// humanizeAge renders a Duration as a coarse, human-readable string in
// the style of the §12 example ("3 minutes ago", "2 hours ago",
// "yesterday"). Negative durations (clock skew) collapse to "just now".
func humanizeAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	switch {
	case d < time.Hour:
		n := int(d / time.Minute)
		return pluralize(n, "minute") + " ago"
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return pluralize(n, "hour") + " ago"
	case d < 48*time.Hour:
		return "yesterday"
	default:
		n := int(d / (24 * time.Hour))
		return pluralize(n, "day") + " ago"
	}
}

func pluralize(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
