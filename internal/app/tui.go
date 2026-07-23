package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
)

type tuiMode int

const (
	tuiModeSend tuiMode = iota
	tuiModeReceive
	tuiModeDoctor
	tuiModeNote
)

type tuiScreen int

const (
	tuiScreenForm tuiScreen = iota
	tuiScreenTask
	tuiScreenBrowser
	tuiScreenNote
)

type tuiTaskMsg struct {
	state nativeTaskSnapshot
}

type tuiModel struct {
	ctx  context.Context
	g    *globalOptions
	job  *nativeTaskStore
	note *localWebNoteStore

	mode   tuiMode
	screen tuiScreen
	focus  int
	width  int
	height int
	err    string

	sendPath       string
	sendCode       string
	symlinkIndex   int
	noGitIgnore    bool
	receiveCode    string
	outputDir      string
	conflictIndex  int
	doctorTimeout  int
	task           nativeTaskSnapshot
	noteHost       bool
	noteCode       string
	notePad        string
	noteState      localWebNoteSnapshot
	noteEditor     tuiNoteEditor
	noteLastID     uint64
	noteDirty      bool
	noteVersion    uint64
	notePublishing uint64
	noteErr        string
	quitting       bool
	configWarning  string
	browser        tuiBrowserState
}

func newTUICommand(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive native transfer console",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := withInterrupt(cmd.Context())
			job := &nativeTaskStore{run: newNativeTaskRunner(g)}
			model := newTUIModel(ctx, g, job)
			program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
			_, err := program.Run()
			job.Cancel()
			model.note.Leave()
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
}

func newTUIModel(ctx context.Context, g *globalOptions, job *nativeTaskStore) tuiModel {
	config, err := loadUserConfig()
	model := newTUIModelWithConfig(ctx, g, job, config)
	if err != nil {
		model.configWarning = err.Error()
	}
	return model
}

func newTUIModelWithConfig(ctx context.Context, g *globalOptions, job *nativeTaskStore, config userConfig) tuiModel {
	sanitizeUserConfig(&config)
	editor := newTUINoteEditor()
	model := tuiModel{
		ctx:         ctx,
		g:           g,
		job:         job,
		note:        newLocalWebNoteStore(ctx, g),
		noteHost:    true,
		notePad:     note.DefaultPad,
		noteEditor:  editor,
		sendPath:    config.LastSendPath,
		noGitIgnore: config.IncludeGitIgnored,
		outputDir:   config.LastReceiveDir,
	}
	switch config.LastMode {
	case "receive":
		model.mode = tuiModeReceive
	case "doctor":
		model.mode = tuiModeDoctor
	case "note":
		model.mode = tuiModeNote
	}
	if config.SymlinkMode == "preserve" {
		model.symlinkIndex = 1
	}
	switch config.ConflictPolicy {
	case "rename":
		model.conflictIndex = 1
	case "skip":
		model.conflictIndex = 2
	}
	for index, value := range []string{"2s", "3s", "5s", "10s"} {
		if value == config.DoctorTimeout {
			model.doctorTimeout = index
			break
		}
	}
	return model
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

func (m tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.resizeNoteEditor()
		return m, nil
	case tuiTaskMsg:
		m.task = message.state
		if message.state.Running {
			return m, pollTUITask(m.job)
		}
		return m, nil
	case tuiNoteStateMsg:
		focus := m.applyNoteState(message.state)
		if message.state.Running {
			return m, tea.Batch(focus, pollTUINote(m.note))
		}
		return m, focus
	case tuiNoteDebounceMsg:
		if message.version != m.noteVersion || !m.noteDirty || !m.noteState.Connected {
			return m, nil
		}
		return m.startNotePublish(message.version)
	case tuiNoteWriteMsg:
		return m.applyNoteWrite(message)
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			m.savePreferences()
			m.job.Cancel()
			m.note.Leave()
			m.quitting = true
			return m, tea.Quit
		}
		if m.screen == tuiScreenTask {
			return m.updateTask(message)
		}
		if m.screen == tuiScreenBrowser {
			return m.updateBrowser(message)
		}
		if m.screen == tuiScreenNote {
			return m.updateNote(message)
		}
		return m.updateForm(message)
	default:
		if m.screen == tuiScreenNote {
			return m.updateNote(message)
		}
		return m, nil
	}
}

func (m tuiModel) updateTask(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "c", "C":
		if m.task.Running {
			m.job.Cancel()
			return m, pollTUITask(m.job)
		}
		m.screen = tuiScreenForm
		m.err = ""
		return m, nil
	case "enter":
		if !m.task.Running {
			m.screen = tuiScreenForm
			m.err = ""
		}
	case "q":
		if !m.task.Running {
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m tuiModel) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.savePreferences()
		m.quitting = true
		return m, tea.Quit
	case "tab", "down":
		m.focus = (m.focus + 1) % (m.maxFocus() + 1)
		m.err = ""
		return m, nil
	case "shift+tab", "up":
		m.focus--
		if m.focus < 0 {
			m.focus = m.maxFocus()
		}
		m.err = ""
		return m, nil
	case "left":
		m.adjustFocusedChoice(-1)
		return m, nil
	case "right":
		m.adjustFocusedChoice(1)
		return m, nil
	case "enter":
		if m.canOpenBrowser() {
			return m.openBrowser()
		}
		if m.focus == m.maxFocus() {
			return m.startTask()
		}
		m.activateFocusedChoice()
		return m, nil
	case " ":
		if m.isTextFocus() {
			m.appendInput(" ")
		} else {
			m.activateFocusedChoice()
		}
		return m, nil
	case "backspace":
		if m.isTextFocus() {
			m.removeInputRune()
		}
		return m, nil
	case "ctrl+u":
		if m.isTextFocus() {
			m.setInput("")
		}
		return m, nil
	}
	if key.Type == tea.KeyRunes && m.isTextFocus() {
		m.appendInput(string(key.Runes))
	}
	return m, nil
}

func (m tuiModel) startTask() (tea.Model, tea.Cmd) {
	if m.mode == tuiModeNote {
		return m.startNoteSession()
	}
	request, err := m.taskRequest()
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.savePreferences()
	if err := m.job.Start(m.ctx, request); err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.screen = tuiScreenTask
	m.err = ""
	m.task = m.job.Snapshot()
	return m, pollTUITask(m.job)
}

func (m *tuiModel) savePreferences() {
	config, err := loadUserConfig()
	if err != nil {
		config = defaultUserConfig()
	}
	config.LastMode = tuiModeName(m.mode)
	config.LastSendPath = strings.TrimSpace(m.sendPath)
	config.LastReceiveDir = strings.TrimSpace(m.outputDir)
	config.SymlinkMode = []string{"follow", "preserve"}[m.symlinkIndex]
	config.IncludeGitIgnored = m.noGitIgnore
	config.ConflictPolicy = []string{"overwrite", "rename", "skip"}[m.conflictIndex]
	config.DoctorTimeout = []string{"2s", "3s", "5s", "10s"}[m.doctorTimeout]
	if err := saveUserConfig(config); err != nil {
		m.configWarning = err.Error()
	} else {
		m.configWarning = ""
	}
}

func (m tuiModel) taskRequest() (nativeTaskRequest, error) {
	switch m.mode {
	case tuiModeSend:
		path := strings.TrimSpace(m.sendPath)
		if path == "" {
			return nil, errors.New("send path is required")
		}
		if _, err := os.Lstat(path); err != nil {
			return nil, fmt.Errorf("send path: %w", err)
		}
		symlinks := []string{"follow", "preserve"}[m.symlinkIndex]
		code := ""
		if strings.TrimSpace(m.sendCode) != "" {
			validated, err := secure.ValidateCode(m.sendCode)
			if err != nil {
				return nil, err
			}
			code = validated
		}
		return sendTaskRequest{
			Path:        path,
			Code:        code,
			Symlinks:    symlinks,
			NoGitIgnore: m.noGitIgnore,
			NoQRCode:    true,
		}, nil
	case tuiModeReceive:
		code, err := secure.ValidateCode(m.receiveCode)
		if err != nil {
			return nil, err
		}
		out := strings.TrimSpace(m.outputDir)
		if out == "" {
			out = "."
		}
		conflict := []string{"overwrite", "rename", "skip"}[m.conflictIndex]
		return receiveTaskRequest{Code: code, OutputDir: out, OnConflict: conflict}, nil
	case tuiModeDoctor:
		timeout, err := time.ParseDuration([]string{"2s", "3s", "5s", "10s"}[m.doctorTimeout])
		if err != nil {
			return nil, err
		}
		return doctorTaskRequest{Timeout: timeout}, nil
	case tuiModeNote:
		return nil, errors.New("notepad sessions run in-process")
	default:
		return nil, errors.New("unknown TUI mode")
	}
}

func pollTUITask(job *nativeTaskStore) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return tuiTaskMsg{state: job.Snapshot()}
	})
}

func (m tuiModel) maxFocus() int {
	switch m.mode {
	case tuiModeDoctor:
		return 2
	case tuiModeNote:
		return 4
	case tuiModeSend:
		return 5
	default:
		return 4
	}
}

func (m tuiModel) isTextFocus() bool {
	switch m.mode {
	case tuiModeSend:
		return m.focus == 1 || m.focus == 2
	case tuiModeReceive:
		return m.focus == 1 || m.focus == 2
	case tuiModeNote:
		return m.focus == 2 || m.focus == 3
	default:
		return false
	}
}

func (m *tuiModel) appendInput(value string) {
	switch m.mode {
	case tuiModeSend:
		if m.focus == 1 {
			m.sendPath += value
		} else {
			m.sendCode += strings.ToUpper(value)
		}
	case tuiModeReceive:
		if m.focus == 1 {
			m.receiveCode += strings.ToUpper(value)
		} else {
			m.outputDir += value
		}
	case tuiModeNote:
		if m.focus == 2 {
			m.noteCode += strings.ToUpper(value)
		} else {
			m.notePad += value
		}
	}
	m.err = ""
}

func (m *tuiModel) removeInputRune() {
	value := m.input()
	if value == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(value)
	m.setInput(value[:len(value)-size])
	m.err = ""
}

func (m tuiModel) input() string {
	switch m.mode {
	case tuiModeSend:
		if m.focus == 1 {
			return m.sendPath
		}
		return m.sendCode
	case tuiModeReceive:
		if m.focus == 1 {
			return m.receiveCode
		}
		return m.outputDir
	case tuiModeNote:
		if m.focus == 2 {
			return m.noteCode
		}
		return m.notePad
	default:
		return ""
	}
}

func (m *tuiModel) setInput(value string) {
	switch m.mode {
	case tuiModeSend:
		if m.focus == 1 {
			m.sendPath = value
		} else {
			m.sendCode = value
		}
	case tuiModeReceive:
		if m.focus == 1 {
			m.receiveCode = value
		} else {
			m.outputDir = value
		}
	case tuiModeNote:
		if m.focus == 2 {
			m.noteCode = value
		} else {
			m.notePad = value
		}
	}
}

func (m *tuiModel) adjustFocusedChoice(delta int) {
	if m.focus == 0 {
		m.mode = tuiMode(wrapIndex(int(m.mode)+delta, 4))
		m.focus = 0
		m.err = ""
		return
	}
	switch m.mode {
	case tuiModeSend:
		if m.focus == 3 {
			m.symlinkIndex = wrapIndex(m.symlinkIndex+delta, 2)
		} else if m.focus == 4 {
			m.noGitIgnore = !m.noGitIgnore
		}
	case tuiModeReceive:
		if m.focus == 3 {
			m.conflictIndex = wrapIndex(m.conflictIndex+delta, 3)
		}
	case tuiModeDoctor:
		if m.focus == 1 {
			m.doctorTimeout = wrapIndex(m.doctorTimeout+delta, 4)
		}
	case tuiModeNote:
		if m.focus == 1 {
			m.noteHost = !m.noteHost
		}
	}
	m.err = ""
}

func (m *tuiModel) activateFocusedChoice() {
	switch m.mode {
	case tuiModeSend:
		if m.focus == 3 {
			m.symlinkIndex = wrapIndex(m.symlinkIndex+1, 2)
		} else if m.focus == 4 {
			m.noGitIgnore = !m.noGitIgnore
		}
	case tuiModeReceive:
		if m.focus == 3 {
			m.conflictIndex = wrapIndex(m.conflictIndex+1, 3)
		}
	case tuiModeDoctor:
		if m.focus == 1 {
			m.doctorTimeout = wrapIndex(m.doctorTimeout+1, 4)
		}
	case tuiModeNote:
		if m.focus == 1 {
			m.noteHost = !m.noteHost
		}
	}
	m.err = ""
}

func tuiModeName(mode tuiMode) string {
	switch mode {
	case tuiModeReceive:
		return "receive"
	case tuiModeDoctor:
		return "doctor"
	case tuiModeNote:
		return "note"
	default:
		return "send"
	}
}

func wrapIndex(value, count int) int {
	value %= count
	if value < 0 {
		value += count
	}
	return value
}
