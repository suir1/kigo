package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/suir1/kigo/internal/note"
)

func TestTUITaskRequestBuildsSendReceiveAndDoctorTasks(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "kigo-tui-send-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	g := &globalOptions{
		Signal:            "https://signal.example",
		WebURL:            "https://kigo.example",
		Relay:             "relay.example:9000",
		DiscoveryAddr:     "239.1.1.1:9999",
		LANTimeout:        time.Second,
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Second,
		DirectListen:      ":0",
		DirectTimeout:     time.Second,
		Connections:       4,
		NoRouteHistory:    true,
	}
	model := newTUIModelWithConfig(context.Background(), g, &nativeTaskStore{}, defaultUserConfig())
	model.sendPath = file.Name()
	model.sendCode = "release-2026"
	model.symlinkIndex = 1
	model.noGitIgnore = true

	request, err := model.taskRequest()
	if err != nil {
		t.Fatal(err)
	}
	send, ok := request.(sendTaskRequest)
	if !ok {
		t.Fatalf("send request = %#v", request)
	}
	if send.Path != file.Name() || send.Code != "RELEASE-2026" || send.Symlinks != "preserve" || !send.NoGitIgnore {
		t.Fatalf("send request = %#v", send)
	}

	model.mode = tuiModeReceive
	model.receiveCode = " k7m9-q2 "
	model.outputDir = "/tmp/kigo-out"
	model.conflictIndex = 1
	request, err = model.taskRequest()
	if err != nil {
		t.Fatal(err)
	}
	receive, ok := request.(receiveTaskRequest)
	if !ok {
		t.Fatalf("receive request = %#v", request)
	}
	if receive.Code != "K7M9Q2" || receive.OutputDir != "/tmp/kigo-out" || receive.OnConflict != "rename" {
		t.Fatalf("receive request = %#v", receive)
	}

	model.mode = tuiModeDoctor
	model.doctorTimeout = 2
	request, err = model.taskRequest()
	if err != nil {
		t.Fatal(err)
	}
	doctor, ok := request.(doctorTaskRequest)
	if !ok || doctor.Timeout != 5*time.Second {
		t.Fatalf("doctor request = %#v", request)
	}
}

func TestTUIRejectsMissingPathAndInvalidCode(t *testing.T) {
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	if _, err := model.taskRequest(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("missing path error = %v", err)
	}
	model.mode = tuiModeReceive
	model.receiveCode = "INVALID!"
	if _, err := model.taskRequest(); err == nil || !strings.Contains(err.Error(), "pairing code") {
		t.Fatalf("invalid code error = %v", err)
	}
}

func TestTUIFormKeyboardEditsAndSwitchesModes(t *testing.T) {
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(tuiModel)
	if model.mode != tuiModeReceive || model.focus != 0 {
		t.Fatalf("mode=%d focus=%d", model.mode, model.focus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(tuiModel)
	for _, value := range "k7m9q2" {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{value}})
		model = updated.(tuiModel)
	}
	if model.receiveCode != "K7M9Q2" {
		t.Fatalf("receive code = %q", model.receiveCode)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = updated.(tuiModel)
	if model.receiveCode != "K7M9Q" {
		t.Fatalf("receive code after backspace = %q", model.receiveCode)
	}
}

func TestTUIFormShowsNotepadAndRejectsInvalidJoinCode(t *testing.T) {
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.mode = tuiModeNote
	model.noteHost = false
	model.noteCode = "bad"
	model.focus = model.maxFocus()
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if command != nil || model.screen != tuiScreenForm || !strings.Contains(model.err, "pairing code") {
		t.Fatalf("invalid join state: screen=%d err=%q command=%v", model.screen, model.err, command)
	}
	view := model.View()
	for _, expected := range []string{"Notepad", "Role", "open code", "Pad", "Open notepad"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("notepad form missing %q:\n%s", expected, view)
		}
	}
}

func TestTUIRecentNotepadsSelectFavoriteAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-recents.json")
	t.Setenv("KIGO_NOTE_RECENTS_PATH", path)
	store := note.NewRecentStore(path)
	if err := store.Touch("ABC123", "main"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Touch("PROJECT-ALPHA-2026", "roadmap"); err != nil {
		t.Fatal(err)
	}

	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.mode = tuiModeNote
	model.focus = 4
	if len(model.noteRecents) != 2 || !strings.Contains(model.View(), "PROJECT-ALPHA-2026") {
		t.Fatalf("recent notepads not loaded: %#v\n%s", model.noteRecents, model.View())
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(tuiModel)
	if model.noteHost || model.noteCode != "ABC123" || model.notePad != "main" {
		t.Fatalf("selected recent: host=%t code=%q pad=%q", model.noteHost, model.noteCode, model.notePad)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	model = updated.(tuiModel)
	entry, ok := model.currentNoteRecent()
	if !ok || entry.Code != "ABC123" || !entry.Favorite {
		t.Fatalf("favorited recent = %#v ok=%t", entry, ok)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	model = updated.(tuiModel)
	if len(model.noteRecents) != 1 || model.noteRecents[0].Code != "PROJECT-ALPHA-2026" {
		t.Fatalf("recents after remove = %#v", model.noteRecents)
	}
}

func TestTUINotepadHostEditsSyncsClearsAndLeaves(t *testing.T) {
	t.Setenv("KIGO_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{WebURL: "https://kigo.example"},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	store.generateCode = func() (string, error) { return "K7M9Q2", nil }

	model := newTUIModelWithConfig(
		context.Background(),
		&globalOptions{WebURL: "https://kigo.example"},
		&nativeTaskStore{},
		defaultUserConfig(),
	)
	model.note = store
	model.mode = tuiModeNote
	model.noteCode = "project-alpha-2026"
	model.notePad = "Sprint Notes"
	model.focus = model.maxFocus()
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if command == nil || model.screen != tuiScreenNote || model.noteState.Code != "PROJECT-ALPHA-2026" ||
		model.noteState.Pad != "Sprint Notes" || !strings.Contains(model.noteState.Link, "p=Sprint+Notes") {
		t.Fatalf("notepad start state = %#v command=%v", model.noteState, command)
	}
	state := waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Connected
	})
	updated, _ = model.Update(tuiNoteStateMsg{state: state})
	model = updated.(tuiModel)
	if !model.noteEditor.Focused() || !strings.Contains(model.View(), "Public link") ||
		!strings.Contains(model.View(), "Pad:") || !strings.Contains(model.View(), "Sprint Notes") {
		t.Fatalf("connected view:\n%s", model.View())
	}

	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first line")})
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second line")})
	if model.noteEditor.Value() != "first line\nsecond line" || !model.noteDirty {
		t.Fatalf("editor value=%q dirty=%t", model.noteEditor.Value(), model.noteDirty)
	}
	version := model.noteVersion
	updated, command = model.Update(tuiNoteDebounceMsg{version: version})
	model = updated.(tuiModel)
	if command == nil {
		t.Fatal("debounced edit did not schedule a publish")
	}
	updated, _ = model.Update(command())
	model = updated.(tuiModel)
	frame := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if frame.Type != note.FrameUpdate || frame.Text != "first line\nsecond line" || frame.Revision != 1 || frame.Pad != "Sprint Notes" {
		t.Fatalf("published frame = %#v", frame)
	}

	peer.recv <- note.Frame{
		Type:     note.FrameAck,
		Version:  note.ProtocolVersion,
		Pad:      "Sprint Notes",
		Revision: 1,
	}
	state = waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Synced && state.AckedRevision == 1
	})
	updated, _ = model.Update(tuiNoteStateMsg{state: state})
	model = updated.(tuiModel)
	if !strings.Contains(model.View(), "synced") {
		t.Fatalf("synced view:\n%s", model.View())
	}

	peer.recv <- note.FrameFromDocument(note.FrameUpdate, note.Document{
		Pad:       "Sprint Notes",
		Text:      "remote document",
		Revision:  2,
		Timestamp: time.Now().UnixMilli() + 1000,
	})
	state = waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Text == "remote document"
	})
	ack := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if ack.Type != note.FrameAck || ack.Revision != 2 {
		t.Fatalf("remote ack = %#v", ack)
	}
	updated, _ = model.Update(tuiNoteStateMsg{state: state})
	model = updated.(tuiModel)
	if model.noteEditor.Value() != "remote document" {
		t.Fatalf("remote editor value = %q", model.noteEditor.Value())
	}

	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	model = updated.(tuiModel)
	if command == nil {
		t.Fatal("clear did not schedule a write")
	}
	updated, _ = model.Update(command())
	model = updated.(tuiModel)
	clearFrame := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if clearFrame.Type != note.FrameClear || clearFrame.Revision != 3 {
		t.Fatalf("clear frame = %#v", clearFrame)
	}

	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.noteState.Running || model.noteState.Status != "left" {
		t.Fatalf("left state = %#v", model.noteState)
	}
	bye := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if bye.Type != note.FrameBye {
		t.Fatalf("leave frame = %#v", bye)
	}
}

func TestTUINotepadViewFitsCommonTerminal(t *testing.T) {
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.width = 110
	model.height = 38
	model.screen = tuiScreenNote
	model.noteState = localWebNoteSnapshot{
		ID:      1,
		Running: true,
		Host:    true,
		Code:    "K7M9Q2",
		Link:    "https://kigo.example/#n=K7M9Q2",
		Status:  "opening",
	}
	model.resizeNoteEditor()
	view := model.View()
	if height := lipgloss.Height(view); height > model.height {
		t.Fatalf("notepad view height = %d, terminal height = %d\n%s", height, model.height, view)
	}
	if width := lipgloss.Width(view); width > model.width {
		t.Fatalf("notepad view width = %d, terminal width = %d", width, model.width)
	}
}

func TestTUIBrowserSelectsSendFileAndCancelsWithoutChanges(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	file := filepath.Join(child, "payload.txt")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.focus = 1
	model.sendPath = root
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.screen != tuiScreenBrowser || model.browser.mode != pathPickFileOrDirectory {
		t.Fatalf("browser state = %#v", model.browser)
	}
	if !strings.Contains(model.View(), "Choose file or folder") {
		t.Fatalf("browser view missing title:\n%s", model.View())
	}

	model.browser.selected = browserEntryIndex(t, model.browser.entries, pathBrowserDirectoryLabel("child"))
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.browser.current != child {
		t.Fatalf("current directory = %q, want %q", model.browser.current, child)
	}
	model.browser.selected = browserEntryIndex(t, model.browser.entries, "payload.txt")
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.screen != tuiScreenForm || model.sendPath != file {
		t.Fatalf("screen=%d send path=%q", model.screen, model.sendPath)
	}

	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.screen != tuiScreenBrowser {
		t.Fatalf("screen = %d, want browser", model.screen)
	}
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.screen != tuiScreenForm || model.sendPath != file {
		t.Fatalf("cancel changed selection: screen=%d path=%q", model.screen, model.sendPath)
	}
}

func TestTUIBrowserReceiveModeRestrictsSelectionToDirectories(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "output")
	file := filepath.Join(root, "payload.txt")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.mode = tuiModeReceive
	model.focus = 2
	model.outputDir = root
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.browser.mode != pathPickDirectoryOnly {
		t.Fatalf("browser mode = %d", model.browser.mode)
	}
	if slices.Contains(pathBrowserLabels(model.browser.entries), "payload.txt") {
		t.Fatalf("directory browser listed file: %#v", pathBrowserLabels(model.browser.entries))
	}
	model.browser.selected = browserEntryIndex(t, model.browser.entries, pathBrowserDirectoryLabel("output"))
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	model.browser.selected = browserEntryIndex(t, model.browser.entries, "[Select this folder]")
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.screen != tuiScreenForm || model.outputDir != child {
		t.Fatalf("screen=%d output=%q, want %q", model.screen, model.outputDir, child)
	}
}

func TestTUIBrowserFiltersAndTogglesSort(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha.txt", "beta.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, defaultUserConfig())
	model.focus = 1
	model.sendPath = root
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beta")})
	labels := pathBrowserLabels(model.browser.entries)
	if !slices.Contains(labels, "beta.txt") || slices.Contains(labels, "alpha.txt") {
		t.Fatalf("filtered labels = %#v", labels)
	}
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyTab})
	if model.browser.sort != pathBrowserSortModified || model.browser.err != "" {
		t.Fatalf("sort=%d err=%q", model.browser.sort, model.browser.err)
	}
	model = updateTUIKey(model, tea.KeyMsg{Type: tea.KeyCtrlU})
	if model.browser.filter != "" || !slices.Contains(pathBrowserLabels(model.browser.entries), "alpha.txt") {
		t.Fatalf("filter=%q labels=%#v", model.browser.filter, pathBrowserLabels(model.browser.entries))
	}
}

func TestTUIConfigMapsIntoModelAndSavesPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	config := userConfig{
		Version:           userConfigVersion,
		Signal:            "https://kigo.example",
		WebURL:            "https://web.kigo.example",
		Relay:             "relay.kigo.example:9000",
		Transport:         transportModeNative,
		LastMode:          "receive",
		LastSendPath:      "/tmp/source",
		LastReceiveDir:    "/tmp/output",
		SymlinkMode:       "preserve",
		IncludeGitIgnored: true,
		ConflictPolicy:    "skip",
		DoctorTimeout:     "10s",
	}
	if err := saveUserConfig(config); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(context.Background(), &globalOptions{}, &nativeTaskStore{})
	if model.mode != tuiModeReceive ||
		model.sendPath != config.LastSendPath ||
		model.outputDir != config.LastReceiveDir ||
		model.symlinkIndex != 1 ||
		!model.noGitIgnore ||
		model.conflictIndex != 2 ||
		model.doctorTimeout != 3 {
		t.Fatalf("model did not map config: %#v", model)
	}

	model.mode = tuiModeDoctor
	model.sendPath = "/tmp/new-source"
	model.outputDir = "/tmp/new-output"
	model.symlinkIndex = 0
	model.noGitIgnore = false
	model.conflictIndex = 1
	model.doctorTimeout = 0
	model.savePreferences()
	got, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMode != "doctor" ||
		got.LastSendPath != "/tmp/new-source" ||
		got.LastReceiveDir != "/tmp/new-output" ||
		got.SymlinkMode != "follow" ||
		got.IncludeGitIgnored ||
		got.ConflictPolicy != "rename" ||
		got.DoctorTimeout != "2s" {
		t.Fatalf("saved preferences = %#v", got)
	}
	if got.Signal != config.Signal || got.WebURL != config.WebURL || got.Relay != config.Relay ||
		got.Transport != config.Transport {
		t.Fatalf("TUI replaced network config: %#v", got)
	}
}

func TestTUIConfigAcceptsAndPersistsNotepadMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	config := defaultUserConfig()
	config.LastMode = "note"
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, &nativeTaskStore{}, config)
	if model.mode != tuiModeNote {
		t.Fatalf("mode = %d, want note", model.mode)
	}
	model.savePreferences()
	got, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMode != "note" {
		t.Fatalf("saved mode = %q", got.LastMode)
	}
}

func TestTUITaskViewShowsPairingAndCancellation(t *testing.T) {
	started := make(chan struct{})
	job := &nativeTaskStore{
		run: func(ctx context.Context, _ nativeTaskRequest, emit func(nativeTaskEvent)) error {
			close(started)
			emit(nativeTaskEvent{Kind: nativeTaskEventCode, Text: "Code: K7M9Q2", Value: "K7M9Q2"})
			emit(nativeTaskEvent{Kind: nativeTaskEventLink, Text: "Link: https://kigo.example/#c=K7M9Q2", Value: "https://kigo.example/#c=K7M9Q2"})
			emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "Waiting for receiver..."})
			<-ctx.Done()
			return ctx.Err()
		},
	}
	model := newTUIModelWithConfig(context.Background(), &globalOptions{}, job, defaultUserConfig())
	model.screen = tuiScreenTask
	if err := job.Start(context.Background(), sendTaskRequest{Path: "/tmp/file", Symlinks: "follow"}); err != nil {
		t.Fatal(err)
	}
	<-started
	state := waitForNativeTask(t, job, func(state nativeTaskSnapshot) bool { return state.Code != "" })
	model.task = state
	view := model.View()
	for _, expected := range []string{"Sending", "K7M9Q2", "Public link", "Waiting for receiver"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view missing %q:\n%s", expected, view)
		}
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(tuiModel)
	if command == nil {
		t.Fatal("cancel did not schedule task polling")
	}
	state = waitForNativeTask(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	if !state.Canceled || state.Failed {
		t.Fatalf("state = %#v", state)
	}
}

func TestNativeTaskUsesLastErrorLineAndBoundsLogs(t *testing.T) {
	job := &nativeTaskStore{
		run: func(_ context.Context, _ nativeTaskRequest, emit func(nativeTaskEvent)) error {
			for index := 0; index < nativeTaskMaxLogLines+20; index++ {
				emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "line"})
			}
			emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "error: room expired"})
			return errors.New("exit status 1")
		},
	}
	if err := job.Start(context.Background(), receiveTaskRequest{Code: "K7M9Q2"}); err != nil {
		t.Fatal(err)
	}
	state := waitForNativeTask(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	if !state.Failed || state.Error != "room expired" {
		t.Fatalf("state = %#v", state)
	}
	if len(state.Logs) != nativeTaskMaxLogLines {
		t.Fatalf("log count = %d", len(state.Logs))
	}
}

func waitForNativeTask(
	t *testing.T,
	job *nativeTaskStore,
	condition func(nativeTaskSnapshot) bool,
) nativeTaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := job.Snapshot()
		if condition(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	state := job.Snapshot()
	t.Fatalf("task condition not met: %#v", state)
	return nativeTaskSnapshot{}
}

func updateTUIKey(model tuiModel, key tea.KeyMsg) tuiModel {
	updated, _ := model.Update(key)
	return updated.(tuiModel)
}

func browserEntryIndex(t *testing.T, entries []pathBrowserEntry, label string) int {
	t.Helper()
	for index, entry := range entries {
		if entry.Label == label {
			return index
		}
	}
	t.Fatalf("browser entries %#v missing %q", pathBrowserLabels(entries), label)
	return -1
}
