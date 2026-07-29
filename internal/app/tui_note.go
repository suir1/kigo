package app

import (
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/suir1/kigo/internal/note"
)

const tuiNoteDebounce = 250 * time.Millisecond

type tuiNoteEditor struct {
	textarea.Model
}

type tuiNoteStateMsg struct {
	state localWebNoteSnapshot
}

type tuiNoteDebounceMsg struct {
	version uint64
}

type tuiNoteWriteMsg struct {
	version uint64
	state   localWebNoteSnapshot
	err     error
}

func newTUINoteEditor() tuiNoteEditor {
	editor := textarea.New()
	editor.Placeholder = "Shared text"
	editor.Prompt = ""
	editor.ShowLineNumbers = true
	editor.CharLimit = note.MaxTextSize
	editor.SetWidth(72)
	editor.SetHeight(10)
	return tuiNoteEditor{Model: editor}
}

func (m *tuiModel) resizeNoteEditor() {
	width := m.width
	if width <= 0 {
		width = 84
	}
	width = minInt(width-8, 90)
	width = maxInt(width, 28)
	height := 8
	if m.height > 0 {
		height = maxInt(4, minInt(8, m.height-22))
	}
	m.noteEditor.SetWidth(width)
	m.noteEditor.SetHeight(height)
}

func (m tuiModel) startNoteSession() (tea.Model, tea.Cmd) {
	if m.note == nil {
		m.err = "notepad is unavailable"
		return m, nil
	}
	var (
		state localWebNoteSnapshot
		err   error
	)
	if m.noteHost {
		state, err = m.note.StartHostWithCodeAndPad(m.noteCode, m.notePad)
	} else {
		state, err = m.note.StartJoinPad(m.noteCode, m.notePad)
	}
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.savePreferences()
	m.screen = tuiScreenNote
	m.err = ""
	m.noteErr = ""
	m.noteState = state
	m.noteLastID = state.ID
	m.noteCode = state.Code
	m.notePad = state.Pad
	m.noteDirty = false
	m.noteVersion = 0
	m.notePublishing = 0
	m.noteEditor.SetValue(state.Text)
	m.noteEditor.Blur()
	m.resizeNoteEditor()
	return m, pollTUINote(m.note)
}

func pollTUINote(store *localWebNoteStore) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		if store == nil {
			return tuiNoteStateMsg{state: localWebNoteSnapshot{
				Status: "error",
				Error:  "notepad is unavailable",
			}}
		}
		return tuiNoteStateMsg{state: store.Snapshot()}
	})
}

func debounceTUINote(version uint64) tea.Cmd {
	return tea.Tick(tuiNoteDebounce, func(time.Time) tea.Msg {
		return tuiNoteDebounceMsg{version: version}
	})
}

func (m *tuiModel) applyNoteState(state localWebNoteSnapshot) tea.Cmd {
	newSession := state.ID != m.noteLastID
	m.noteState = state
	m.noteCode = state.Code
	m.notePad = state.Pad
	if state.Error != "" {
		m.noteErr = state.Error
	} else if state.DraftWarning != "" {
		m.noteErr = "draft: " + state.DraftWarning
	}
	if state.RecentWarning != "" {
		m.noteRecentWarn = state.RecentWarning
	}
	if state.Connected && state.ID != m.noteRecentID {
		m.noteRecentID = state.ID
		m.refreshNoteRecents(state.Code, state.Pad)
	}
	if newSession {
		m.noteLastID = state.ID
		m.noteDirty = false
		m.noteVersion = 0
		m.notePublishing = 0
		m.noteEditor.SetValue(state.Text)
	} else if !m.noteDirty && m.noteEditor.Value() != state.Text {
		m.noteEditor.SetValue(state.Text)
	}
	if state.Connected {
		if !m.noteEditor.Focused() {
			return m.noteEditor.Focus()
		}
		return nil
	}
	m.noteEditor.Blur()
	return nil
}

func (m *tuiModel) refreshNoteRecents(code, pad string) {
	if m.note == nil {
		m.noteRecents = nil
		m.noteRecent = 0
		return
	}
	entries, err := m.note.RecentNotes()
	if err != nil {
		m.noteRecentWarn = err.Error()
		return
	}
	m.noteRecentWarn = ""
	m.noteRecents = entries
	if len(entries) == 0 {
		m.noteRecent = 0
		return
	}
	if m.noteRecent >= len(entries) {
		m.noteRecent = len(entries) - 1
	}
	for index, entry := range entries {
		if entry.Code == code && entry.Pad == pad {
			m.noteRecent = index
			break
		}
	}
}

func (m *tuiModel) currentNoteRecent() (note.RecentEntry, bool) {
	if len(m.noteRecents) == 0 || m.noteRecent < 0 || m.noteRecent >= len(m.noteRecents) {
		return note.RecentEntry{}, false
	}
	return m.noteRecents[m.noteRecent], true
}

func (m *tuiModel) adjustNoteRecent(delta int) {
	if len(m.noteRecents) == 0 {
		m.refreshNoteRecents("", "")
	}
	if len(m.noteRecents) == 0 {
		return
	}
	m.noteRecent = wrapIndex(m.noteRecent+delta, len(m.noteRecents))
	m.selectNoteRecent()
}

func (m *tuiModel) selectNoteRecent() {
	entry, ok := m.currentNoteRecent()
	if !ok {
		m.refreshNoteRecents("", "")
		entry, ok = m.currentNoteRecent()
	}
	if !ok {
		return
	}
	m.noteHost = false
	m.noteCode = entry.Code
	m.notePad = entry.Pad
	m.err = ""
}

func (m *tuiModel) toggleNoteRecentFavorite() {
	entry, ok := m.currentNoteRecent()
	if !ok || m.note == nil {
		return
	}
	if err := m.note.SetRecentFavorite(entry.Code, entry.Pad, !entry.Favorite); err != nil {
		m.noteRecentWarn = err.Error()
		return
	}
	m.refreshNoteRecents(entry.Code, entry.Pad)
	m.selectNoteRecent()
}

func (m *tuiModel) removeNoteRecent() {
	entry, ok := m.currentNoteRecent()
	if !ok || m.note == nil {
		return
	}
	if err := m.note.ForgetRecent(entry.Code, entry.Pad); err != nil {
		m.noteRecentWarn = err.Error()
		return
	}
	m.refreshNoteRecents("", "")
	if len(m.noteRecents) > 0 {
		m.selectNoteRecent()
	}
}

func (m tuiModel) updateNote(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			if m.noteState.Running {
				state := m.note.Leave()
				m.applyNoteState(state)
				return m, nil
			}
			m.screen = tuiScreenForm
			m.noteErr = ""
			return m, nil
		case "q":
			if !m.noteState.Running {
				m.savePreferences()
				m.quitting = true
				return m, tea.Quit
			}
		case "enter":
			if !m.noteState.Running {
				m.screen = tuiScreenForm
				m.noteErr = ""
				return m, nil
			}
		case "ctrl+s":
			if m.noteState.Connected && m.noteDirty {
				m.noteVersion++
				return m.startNotePublish(m.noteVersion)
			}
			return m, nil
		case "ctrl+l":
			if m.noteState.Connected {
				m.noteEditor.SetValue("")
				m.noteDirty = true
				m.noteVersion++
				version := m.noteVersion
				m.notePublishing = version
				return m, clearTUINote(m.note, version)
			}
			return m, nil
		}
	}
	if !m.noteState.Connected {
		return m, nil
	}

	previous := m.noteEditor.Value()
	editor, command := m.noteEditor.Model.Update(message)
	m.noteEditor.Model = editor
	value := m.noteEditor.Value()
	if value == previous {
		return m, command
	}
	if err := note.ValidateText(value); err != nil {
		m.noteEditor.SetValue(previous)
		m.noteErr = err.Error()
		return m, nil
	}
	m.noteErr = ""
	m.noteDirty = true
	m.noteVersion++
	return m, tea.Batch(command, debounceTUINote(m.noteVersion))
}

func (m tuiModel) startNotePublish(version uint64) (tea.Model, tea.Cmd) {
	if m.note == nil || !m.noteState.Connected {
		m.noteErr = "notepad is not connected"
		return m, nil
	}
	m.notePublishing = version
	return m, publishTUINote(m.note, version, m.noteEditor.Value())
}

func publishTUINote(store *localWebNoteStore, version uint64, text string) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return tuiNoteWriteMsg{version: version, err: errors.New("notepad is unavailable")}
		}
		state, err := store.Update(text)
		return tuiNoteWriteMsg{version: version, state: state, err: err}
	}
}

func clearTUINote(store *localWebNoteStore, version uint64) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return tuiNoteWriteMsg{version: version, err: errors.New("notepad is unavailable")}
		}
		state, err := store.Clear()
		return tuiNoteWriteMsg{version: version, state: state, err: err}
	}
}

func (m tuiModel) applyNoteWrite(message tuiNoteWriteMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.noteErr = message.err.Error()
		if message.version == m.notePublishing {
			m.notePublishing = 0
		}
		return m, nil
	}
	if message.version == m.noteVersion {
		m.noteDirty = false
		m.notePublishing = 0
	}
	focus := m.applyNoteState(message.state)
	return m, focus
}
