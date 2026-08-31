package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const nativeTaskMaxLogLines = 240

type nativeTaskRequest interface {
	taskKind() string
}

type sendTaskRequest struct {
	Path        string
	Code        string
	Symlinks    string
	NoGitIgnore bool
	NoQRCode    bool
	Remember    bool
}

func (sendTaskRequest) taskKind() string { return "send" }

type receiveTaskRequest struct {
	Code       string
	OutputDir  string
	OnConflict string
	Remember   bool
}

func (receiveTaskRequest) taskKind() string { return "recv" }

type textSendTaskRequest struct {
	Text     string
	Code     string
	NoQRCode bool
}

func (textSendTaskRequest) taskKind() string { return "text-send" }

type doctorTaskRequest struct {
	Timeout   time.Duration
	JSON      bool
	AIExplain bool
	AITimeout time.Duration
}

func (doctorTaskRequest) taskKind() string { return "doctor" }

type nativeTaskEventKind uint8

const (
	nativeTaskEventLine nativeTaskEventKind = iota
	nativeTaskEventCode
	nativeTaskEventLink
)

type nativeTaskEvent struct {
	Kind  nativeTaskEventKind
	Text  string
	Value string
}

type nativeTaskRunFunc func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error

type nativeTaskSnapshot struct {
	ID         uint64   `json:"id"`
	Kind       string   `json:"kind,omitempty"`
	Running    bool     `json:"running"`
	Canceled   bool     `json:"canceled"`
	Failed     bool     `json:"failed"`
	StartedAt  int64    `json:"started_at,omitempty"`
	FinishedAt int64    `json:"finished_at,omitempty"`
	Code       string   `json:"code,omitempty"`
	Link       string   `json:"link,omitempty"`
	Error      string   `json:"error,omitempty"`
	Logs       []string `json:"logs,omitempty"`
}

type nativeTaskStore struct {
	run      nativeTaskRunFunc
	state    nativeTaskSnapshot
	cancel   context.CancelFunc
	sequence uint64
	mu       sync.Mutex
}

type clientTaskOutput struct {
	emit func(nativeTaskEvent)
	out  io.Writer
	err  io.Writer
}

func newClientTaskOutput(out, errOut io.Writer, emit func(nativeTaskEvent)) *clientTaskOutput {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = out
	}
	if emit == nil {
		emit = func(event nativeTaskEvent) {
			if strings.TrimSpace(event.Text) != "" {
				fmt.Fprintln(out, event.Text)
			}
		}
	}
	return &clientTaskOutput{emit: emit, out: out, err: errOut}
}

func newEventTaskOutput(emit func(nativeTaskEvent)) (*clientTaskOutput, *nativeTaskLineWriter) {
	writer := &nativeTaskLineWriter{emit: emit}
	return newClientTaskOutput(writer, writer, emit), writer
}

func (o *clientTaskOutput) line(text string) {
	if o == nil {
		return
	}
	o.emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: text})
}

func (o *clientTaskOutput) linef(format string, args ...any) {
	o.line(fmt.Sprintf(format, args...))
}

func (o *clientTaskOutput) code(code string) {
	if o == nil {
		return
	}
	o.emit(nativeTaskEvent{Kind: nativeTaskEventCode, Text: "Code: " + code, Value: code})
}

func (o *clientTaskOutput) link(link string) {
	if o == nil {
		return
	}
	o.emit(nativeTaskEvent{Kind: nativeTaskEventLink, Text: "Link: " + link, Value: link})
}

func (o *clientTaskOutput) logf(format string, args ...any) {
	o.line(time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...))
}

type nativeTaskLineWriter struct {
	emit    func(nativeTaskEvent)
	pending string
	mu      sync.Mutex
}

func (w *nativeTaskLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(data)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.emitLine(strings.TrimSuffix(w.pending[:index], "\r"))
		w.pending = w.pending[index+1:]
	}
	return len(data), nil
}

func (w *nativeTaskLineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		w.emitLine(w.pending)
		w.pending = ""
	}
}

func (w *nativeTaskLineWriter) emitLine(line string) {
	if w.emit != nil && strings.TrimSpace(line) != "" {
		w.emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: line})
	}
}

func newNativeTaskRunner(g *globalOptions) nativeTaskRunFunc {
	return func(ctx context.Context, request nativeTaskRequest, emit func(nativeTaskEvent)) error {
		output, writer := newEventTaskOutput(emit)
		defer writer.Flush()
		return runClientTask(ctx, g, request, output)
	}
}

func (s *nativeTaskStore) Start(parent context.Context, request nativeTaskRequest) error {
	if request == nil {
		return errors.New("native task request is required")
	}
	if s.run == nil {
		return errors.New("native task runner is unavailable")
	}
	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return errors.New("another native task is already running")
	}
	s.sequence++
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.state = nativeTaskSnapshot{
		ID:        s.sequence,
		Kind:      request.taskKind(),
		Running:   true,
		StartedAt: time.Now().UnixMilli(),
		Logs:      []string{},
	}
	id := s.sequence
	s.mu.Unlock()

	go func() {
		err := s.run(ctx, request, func(event nativeTaskEvent) {
			s.appendEvent(id, event)
		})
		s.finish(id, err)
	}()
	return nil
}

func (s *nativeTaskStore) Snapshot() nativeTaskSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Logs = append([]string(nil), s.state.Logs...)
	return out
}

func (s *nativeTaskStore) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *nativeTaskStore) appendEvent(id uint64, event nativeTaskEvent) {
	line := strings.TrimSpace(event.Text)
	if line == "" && event.Value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id {
		return
	}
	if line != "" {
		s.state.Logs = append(s.state.Logs, line)
		if len(s.state.Logs) > nativeTaskMaxLogLines {
			s.state.Logs = append([]string(nil), s.state.Logs[len(s.state.Logs)-nativeTaskMaxLogLines:]...)
		}
	}
	switch event.Kind {
	case nativeTaskEventCode:
		s.state.Code = event.Value
	case nativeTaskEventLink:
		s.state.Link = event.Value
	}
}

func (s *nativeTaskStore) finish(id uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id {
		return
	}
	s.state.Running = false
	s.state.FinishedAt = time.Now().UnixMilli()
	s.cancel = nil
	if errors.Is(err, context.Canceled) {
		s.state.Canceled = true
		return
	}
	if err != nil {
		s.state.Failed = true
		s.state.Error = nativeTaskError(err, s.state.Logs)
	}
}

func nativeTaskError(err error, lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if strings.HasPrefix(strings.ToLower(line), "error:") {
			return strings.TrimSpace(line[len("error:"):])
		}
	}
	return FormatError(err)
}
