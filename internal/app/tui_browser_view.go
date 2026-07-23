package app

import (
	"fmt"
	"strings"
)

const tuiBrowserTimestampWidth = 16

func (m tuiModel) renderBrowser(width int) string {
	title := "Choose file or folder"
	if m.browser.mode == pathPickDirectoryOnly {
		title = "Choose output folder"
	}
	sortLabel := "name"
	if m.browser.sort == pathBrowserSortModified {
		sortLabel = "modified"
	}
	filter := m.browser.filter + "|"
	if m.browser.filter == "" {
		filter = tuiMutedStyle.Render("type to filter") + tuiAccentStyle.Render("|")
	}
	lines := []string{
		tuiTitleStyle.Render("KIGO") + "  " + title,
		tuiMutedStyle.Render(truncateTUIText("Directory: "+m.browser.current, width)),
		"Filter: " + filter,
		"Sort: " + tuiAccentStyle.Render(sortLabel),
		"",
	}

	limit := 12
	if m.height > 0 {
		limit = maxInt(4, minInt(20, m.height-12))
	}
	start, end := browserWindow(len(m.browser.entries), m.browser.selected, limit)
	if len(m.browser.entries) == 0 {
		lines = append(lines, tuiMutedStyle.Render("No matching entries"))
	} else {
		if start > 0 {
			lines = append(lines, tuiMutedStyle.Render(fmt.Sprintf("  ... %d above", start)))
		}
		for index := start; index < end; index++ {
			lines = append(lines, m.renderBrowserEntry(m.browser.entries[index], index == m.browser.selected, width))
		}
		if end < len(m.browser.entries) {
			lines = append(lines, tuiMutedStyle.Render(fmt.Sprintf("  ... %d below", len(m.browser.entries)-end)))
		}
	}

	if m.browser.err != "" {
		lines = append(lines, "", tuiErrorStyle.Render(truncateTUIText("Error: "+m.browser.err, width)))
	}
	lines = append(
		lines,
		"",
		tuiMutedStyle.Render("Up/Down select | Enter/Right open | Left parent | Tab sort"),
		tuiMutedStyle.Render("Type filter | Backspace edit/parent | Ctrl+U clear | Esc cancel"),
	)
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderBrowserEntry(entry pathBrowserEntry, selected bool, width int) string {
	label := entry.Label
	if entry.SelectCurrent {
		label = "[Select this folder]"
	}
	prefix := "  "
	if selected {
		prefix = "> "
	}
	timestamp := ""
	if !entry.Parent && !entry.SelectCurrent && !entry.Modified.IsZero() {
		timestamp = entry.Modified.Local().Format("2006-01-02 15:04")
	}
	labelWidth := width - len(prefix)
	if timestamp != "" && width >= 42 {
		labelWidth -= tuiBrowserTimestampWidth + 2
	}
	labelWidth = maxInt(labelWidth, 8)
	line := prefix + truncateTUIText(label, labelWidth)
	if timestamp != "" && width >= 42 {
		line = fmt.Sprintf("%-*s  %s", width-tuiBrowserTimestampWidth-2, line, timestamp)
	}
	line = truncateTUIText(line, width)
	if selected {
		return tuiAccentStyle.Render(line)
	}
	if entry.Parent {
		return tuiMutedStyle.Render(line)
	}
	return line
}

func browserWindow(total, selected, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	if limit >= total {
		return 0, total
	}
	selected = maxInt(0, minInt(selected, total-1))
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}
