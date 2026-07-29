package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) renderNote(width int) string {
	state := m.noteState
	status, style := tuiNoteStatus(state, m.noteDirty, m.notePublishing != 0)
	lines := []string{
		tuiTitleStyle.Render("KIGO") + "  Shared notepad",
		"Status: " + style.Render(status),
	}
	if state.Code != "" {
		lines = append(lines, "Pairing code: "+tuiSelectedStyle.Render(state.Code))
	}
	if state.Pad != "" {
		lines = append(lines, "Pad: "+tuiSelectedStyle.Render(state.Pad))
	}
	if state.Link != "" {
		lines = append(lines, truncateTUIText("Public link: "+state.Link, width))
	}
	lines = append(lines,
		fmt.Sprintf("Revision: %d | acknowledged: %d", state.Revision, state.AckedRevision),
		"",
		tuiEditorStyle.Render(m.noteEditor.View()),
	)
	if m.noteErr != "" {
		lines = append(lines, "", tuiErrorStyle.Render(truncateTUIText("Error: "+m.noteErr, width)))
	}
	if m.noteRecentWarn != "" {
		lines = append(lines, "", tuiWarningStyle.Render(truncateTUIText("Recent notepads: "+m.noteRecentWarn, width)))
	}
	lines = append(lines, "")
	switch {
	case state.Connected:
		lines = append(lines,
			tuiMutedStyle.Render("Edit normally | Ctrl+S sync now | Ctrl+L clear"),
			tuiMutedStyle.Render("Esc leave | Ctrl+C quit"),
		)
	case state.Running:
		lines = append(lines, tuiMutedStyle.Render("Esc leave | Ctrl+C quit"))
	default:
		lines = append(lines, tuiMutedStyle.Render("Enter/Esc return | Q quit"))
	}
	return strings.Join(lines, "\n")
}

func tuiNoteStatus(
	state localWebNoteSnapshot,
	dirty bool,
	publishing bool,
) (string, lipgloss.Style) {
	if dirty && !publishing {
		return "editing", tuiWarningStyle
	}
	if publishing {
		return "syncing", tuiAccentStyle
	}
	switch state.Status {
	case "opening":
		return "opening", tuiAccentStyle
	case "reconnecting":
		return "reconnecting", tuiWarningStyle
	case "available":
		return "available", tuiSuccessStyle
	case "syncing":
		return "syncing", tuiAccentStyle
	case "synced":
		return "synced", tuiSuccessStyle
	case "peer_left":
		return "peer left", tuiWarningStyle
	case "left":
		return "closed", tuiWarningStyle
	case "error":
		return "failed", tuiErrorStyle
	default:
		return "idle", tuiMutedStyle
	}
}
