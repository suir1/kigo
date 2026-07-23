package app

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiBrowserState struct {
	current  string
	mode     pathPickMode
	sort     pathBrowserSort
	all      []pathBrowserEntry
	entries  []pathBrowserEntry
	selected int
	filter   string
	err      string
}

func (m tuiModel) canOpenBrowser() bool {
	return (m.mode == tuiModeSend && m.focus == 1) ||
		(m.mode == tuiModeReceive && m.focus == 2)
}

func (m tuiModel) openBrowser() (tea.Model, tea.Cmd) {
	var start string
	var mode pathPickMode
	if m.mode == tuiModeReceive {
		start = m.outputDir
		mode = pathPickDirectoryOnly
	} else {
		start = m.sendPath
		mode = pathPickFileOrDirectory
	}
	current, err := normalizeBrowserDirectory(start)
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.browser = tuiBrowserState{
		current: current,
		mode:    mode,
		sort:    pathBrowserSortName,
	}
	if err := m.reloadBrowser(false); err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.screen = tuiScreenBrowser
	m.err = ""
	return m, nil
}

func (m tuiModel) updateBrowser(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = tuiScreenForm
		return m, nil
	case "up":
		m.moveBrowserSelection(-1)
		return m, nil
	case "down":
		m.moveBrowserSelection(1)
		return m, nil
	case "pgup":
		m.moveBrowserSelection(-10)
		return m, nil
	case "pgdown":
		m.moveBrowserSelection(10)
		return m, nil
	case "home":
		m.browser.selected = 0
		return m, nil
	case "end":
		if len(m.browser.entries) > 0 {
			m.browser.selected = len(m.browser.entries) - 1
		}
		return m, nil
	case "left":
		m.changeBrowserDirectory(filepath.Dir(m.browser.current))
		return m, nil
	case "right", "enter":
		m.activateBrowserEntry()
		return m, nil
	case "tab":
		m.browser.sort = pathBrowserSort(wrapIndex(int(m.browser.sort)+1, 2))
		if err := m.reloadBrowser(true); err != nil {
			m.browser.err = err.Error()
		}
		return m, nil
	case "backspace":
		if m.browser.filter == "" {
			m.changeBrowserDirectory(filepath.Dir(m.browser.current))
			return m, nil
		}
		_, size := utf8.DecodeLastRuneInString(m.browser.filter)
		m.browser.filter = m.browser.filter[:len(m.browser.filter)-size]
		m.applyBrowserFilter()
		return m, nil
	case "ctrl+u":
		m.browser.filter = ""
		m.applyBrowserFilter()
		return m, nil
	}
	if key.Type == tea.KeyRunes {
		m.browser.filter += string(key.Runes)
		m.applyBrowserFilter()
	}
	return m, nil
}

func (m *tuiModel) reloadBrowser(keepSelection bool) error {
	entries, err := listPathBrowserEntries(m.browser.current, m.browser.mode, m.browser.sort)
	if err != nil {
		m.browser.err = err.Error()
		return err
	}
	m.browser.all = entries
	m.browser.err = ""
	if !keepSelection {
		m.browser.selected = 0
	}
	m.applyBrowserFilter()
	return nil
}

func (m *tuiModel) applyBrowserFilter() {
	m.browser.entries = filterPathBrowserEntries(m.browser.all, m.browser.filter)
	if len(m.browser.entries) == 0 {
		m.browser.selected = 0
		return
	}
	if m.browser.selected >= len(m.browser.entries) {
		m.browser.selected = len(m.browser.entries) - 1
	}
	if m.browser.selected < 0 {
		m.browser.selected = 0
	}
}

func (m *tuiModel) moveBrowserSelection(delta int) {
	if len(m.browser.entries) == 0 {
		return
	}
	m.browser.selected += delta
	if m.browser.selected < 0 {
		m.browser.selected = 0
	}
	if m.browser.selected >= len(m.browser.entries) {
		m.browser.selected = len(m.browser.entries) - 1
	}
}

func (m *tuiModel) changeBrowserDirectory(path string) {
	path = filepath.Clean(path)
	if path == m.browser.current {
		return
	}
	current, err := normalizeBrowserDirectory(path)
	if err != nil {
		m.browser.err = err.Error()
		return
	}
	m.browser.current = current
	m.browser.filter = ""
	if err := m.reloadBrowser(false); err != nil {
		m.browser.err = err.Error()
	}
}

func (m *tuiModel) activateBrowserEntry() {
	if len(m.browser.entries) == 0 {
		return
	}
	entry := m.browser.entries[m.browser.selected]
	switch {
	case entry.Parent:
		m.changeBrowserDirectory(entry.Path)
	case entry.SelectCurrent:
		m.applyBrowserSelection(entry.Path)
	case entry.IsDir:
		m.changeBrowserDirectory(entry.Path)
	default:
		m.applyBrowserSelection(entry.Path)
	}
}

func (m *tuiModel) applyBrowserSelection(path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if m.mode == tuiModeReceive {
		m.outputDir = path
	} else {
		m.sendPath = path
	}
	m.screen = tuiScreenForm
	m.err = ""
}
