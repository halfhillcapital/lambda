package tui

import "strings"

// setTitle handles `/title [<text>]`. Empty text clears the title.
// Persists immediately to the manifest; ephemeral sessions keep it
// in-memory only (with a notice so the user knows it won't survive).
func (m *uiModel) setTitle(text string) {
	if m.session == nil {
		m.transcript.AppendError("/title: no session attached")
		m.refreshViewport()
		return
	}
	text = strings.TrimSpace(text)
	if err := m.session.SetTitle(text); err != nil {
		m.transcript.AppendError("/title: " + err.Error())
		m.refreshViewport()
		return
	}
	if text == "" {
		m.transcript.AppendNotice("title cleared")
	} else {
		m.transcript.AppendNotice("title: " + text)
	}
	m.refreshViewport()
}

// setModel handles `/model <name>`. Updates the manifest, the live
// agent, and cfg (so the status line reflects it). Refuses an empty
// argument — there's no useful "no model" state.
func (m *uiModel) setModel(name string) {
	if m.session == nil {
		m.transcript.AppendError("/model: no session attached")
		m.refreshViewport()
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		m.transcript.AppendError("/model: usage: /model <name>")
		m.refreshViewport()
		return
	}
	if err := m.session.SetModel(name); err != nil {
		m.transcript.AppendError("/model: " + err.Error())
		m.refreshViewport()
		return
	}
	m.cfg.Model = name
	m.agent.SetModel(name)
	m.transcript.AppendNotice("model: " + name)
	m.refreshViewport()
}
