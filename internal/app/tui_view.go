package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/suir1/kigo/internal/note"
)

var (
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#D9F35B"))
	tuiMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8B96"))
	tuiAccentStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#46C89B"))
	tuiSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#101820")).
				Background(lipgloss.Color("#D9F35B")).
				Padding(0, 1)
	tuiErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B"))
	tuiSuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#46C89B"))
	tuiWarningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E3A23B"))
	tuiFrameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#40505D")).
			Padding(1, 2)
	tuiEditorStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#40505D")).
			Padding(0, 1)
)

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 84
	}
	width = minInt(width-2, 96)
	width = maxInt(width, 38)
	contentWidth := maxInt(width-6, 30)

	var body string
	switch m.screen {
	case tuiScreenTask:
		body = m.renderTask(contentWidth)
	case tuiScreenBrowser:
		body = m.renderBrowser(contentWidth)
	case tuiScreenNote:
		body = m.renderNote(contentWidth)
	default:
		body = m.renderForm(contentWidth)
	}
	return tuiFrameStyle.Width(width).Render(body)
}

func (m tuiModel) renderForm(width int) string {
	plan := planNativeRoute(m.g)
	lines := []string{
		tuiTitleStyle.Render("KIGO") + "  " + tuiMutedStyle.Render("native transfer console"),
		tuiMutedStyle.Render(truncateTUIText("Route: "+plan.Primary+" | fallback: "+plan.Fallback, width)),
		"",
		m.renderTabs(),
		"",
	}

	switch m.mode {
	case tuiModeSend:
		lines = append(lines,
			m.renderField(1, "Path [Enter]", m.sendPath, "/path/to/file-or-directory", width),
			m.renderField(2, "Code", m.sendCode, "random", width),
			m.renderChoice(3, "Symlinks", []string{"follow targets", "preserve safe links"}[m.symlinkIndex], width),
			m.renderChoice(4, "Gitignored", boolLabel(m.noGitIgnore, "include", "exclude"), width),
			"",
			m.renderButton(5, "Start sending"),
		)
	case tuiModeReceive:
		lines = append(lines,
			m.renderField(1, "Code", m.receiveCode, "K7M9Q2", width),
			m.renderField(2, "Output [Enter]", m.outputDir, ".", width),
			m.renderChoice(3, "Existing", []string{"overwrite", "rename incoming", "skip"}[m.conflictIndex], width),
			"",
			m.renderButton(4, "Start receiving"),
		)
	case tuiModeDoctor:
		lines = append(lines,
			m.renderChoice(1, "Timeout", []string{"2 seconds", "3 seconds", "5 seconds", "10 seconds"}[m.doctorTimeout], width),
			"",
			m.renderButton(2, "Run network doctor"),
		)
	case tuiModeNote:
		role := "create new code"
		if !m.noteHost {
			role = "open code"
		}
		lines = append(lines, m.renderChoice(1, "Role", role, width))
		if m.noteHost {
			lines = append(lines,
				m.renderField(2, "Code", m.noteCode, "random", width),
				m.renderField(3, "Pad", m.notePad, note.DefaultPad, width),
				"",
				m.renderButton(4, "Create notepad"),
			)
		} else {
			lines = append(lines,
				m.renderField(2, "Code", m.noteCode, "K7M9Q2", width),
				m.renderField(3, "Pad", m.notePad, note.DefaultPad, width),
				"",
				m.renderButton(4, "Open notepad"),
			)
		}
	}

	if m.err != "" {
		lines = append(lines, "", tuiErrorStyle.Render(truncateTUIText("Error: "+m.err, width)))
	}
	if m.configWarning != "" {
		lines = append(lines, "", tuiWarningStyle.Render(truncateTUIText("Preferences: "+m.configWarning, width)))
	}
	lines = append(lines, "", tuiMutedStyle.Render("Tab/Up/Down move | Left/Right change | Enter browse/select/start | Esc quit"))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderTabs() string {
	names := []string{"Send", "Receive", "Doctor", "Notepad"}
	parts := make([]string, 0, len(names))
	for index, name := range names {
		if int(m.mode) == index {
			label := tuiSelectedStyle.Render(name)
			if m.focus == 0 {
				label = tuiAccentStyle.Render("> ") + label
			}
			parts = append(parts, label)
		} else {
			parts = append(parts, "  "+name+"  ")
		}
	}
	return strings.Join(parts, "  ")
}

func (m tuiModel) renderField(index int, label, value, placeholder string, width int) string {
	display := value
	if display == "" {
		display = tuiMutedStyle.Render(placeholder)
	} else if m.focus == index {
		display += "|"
	}
	line := fmt.Sprintf("%s %-12s %s", focusMarker(m.focus == index), label, display)
	line = truncateTUIText(line, width)
	if m.focus == index {
		return tuiAccentStyle.Render(line)
	}
	return line
}

func (m tuiModel) renderChoice(index int, label, value string, width int) string {
	line := truncateTUIText(fmt.Sprintf("%s %-12s < %s >", focusMarker(m.focus == index), label, value), width)
	if m.focus == index {
		return tuiAccentStyle.Render(line)
	}
	return line
}

func (m tuiModel) renderButton(index int, label string) string {
	line := focusMarker(m.focus == index) + " [" + label + "]"
	if m.focus == index {
		return tuiSelectedStyle.Render(line)
	}
	return line
}

func (m tuiModel) renderTask(width int) string {
	state := m.task
	title := map[string]string{
		"send":   "Sending",
		"recv":   "Receiving",
		"doctor": "Network doctor",
	}[state.Kind]
	if title == "" {
		title = "Native task"
	}
	status := "running"
	statusStyle := tuiAccentStyle
	switch {
	case state.Canceled:
		status = "canceled"
		statusStyle = tuiWarningStyle
	case state.Failed:
		status = "failed"
		statusStyle = tuiErrorStyle
	case !state.Running:
		status = "complete"
		statusStyle = tuiSuccessStyle
	}
	lines := []string{
		tuiTitleStyle.Render("KIGO") + "  " + title,
		"Status: " + statusStyle.Render(status),
	}
	if state.Code != "" {
		lines = append(lines, "Pairing code: "+tuiSelectedStyle.Render(state.Code))
	}
	if state.Link != "" {
		lines = append(lines, truncateTUIText("Public link: "+state.Link, width))
	}
	if state.Failed && state.Error != "" {
		lines = append(lines, tuiErrorStyle.Render(truncateTUIText("Error: "+state.Error, width)))
	}
	if m.configWarning != "" {
		lines = append(lines, tuiWarningStyle.Render(truncateTUIText("Preferences: "+m.configWarning, width)))
	}
	lines = append(lines, "", tuiMutedStyle.Render("Recent output"))

	logLimit := 12
	if m.height > 0 {
		logLimit = maxInt(5, minInt(18, m.height-13))
	}
	start := maxInt(0, len(state.Logs)-logLimit)
	if start == len(state.Logs) {
		lines = append(lines, tuiMutedStyle.Render("Waiting for output..."))
	} else {
		for _, line := range state.Logs[start:] {
			lines = append(lines, truncateTUIText(line, width))
		}
	}
	lines = append(lines, "")
	if state.Running {
		lines = append(lines, tuiMutedStyle.Render("c/Esc cancel | Ctrl+C quit"))
	} else {
		lines = append(lines, tuiMutedStyle.Render("Enter/Esc return | Q quit"))
	}
	return strings.Join(lines, "\n")
}

func boolLabel(value bool, on, off string) string {
	if value {
		return "[x] " + on
	}
	return "[ ] " + off
}

func focusMarker(focused bool) string {
	if focused {
		return ">"
	}
	return " "
}

func truncateTUIText(value string, width int) string {
	if width <= 3 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width-3]) + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
