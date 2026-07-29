package note

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

type InteractiveOptions struct {
	Pad           string
	In            io.Reader
	Out           io.Writer
	Workspace     *Workspace
	Input         *InteractiveInput
	SyncWorkspace bool
	OnChange      func(Document)
	OnReady       func()
}

type InteractiveInput struct {
	lines  <-chan string
	errors <-chan error
}

type receiveResult struct {
	frame Frame
	err   error
}

type InteractiveSession interface {
	Send(context.Context, Frame) error
	Recv(context.Context) (Frame, error)
	SyncWorkspace(context.Context, *Workspace, string) ([]Document, error)
}

func RunInteractive(ctx context.Context, session InteractiveSession, opts InteractiveOptions) error {
	if session == nil {
		return transport.ErrClosed
	}
	pad := NormalizePad(opts.Pad)
	if err := ValidatePad(pad); err != nil {
		return err
	}
	if opts.In == nil {
		opts.In = strings.NewReader("")
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	workspace := opts.Workspace
	if workspace == nil {
		workspace = NewWorkspace()
	}
	inputSource := opts.Input
	if inputSource == nil {
		inputSource = NewInteractiveInput(opts.In)
	}

	var outputMu sync.Mutex
	write := func(format string, args ...any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		_, _ = fmt.Fprintf(opts.Out, format, args...)
	}
	if opts.SyncWorkspace {
		applied, err := session.SyncWorkspace(ctx, workspace, pad)
		if err != nil {
			return err
		}
		for _, document := range applied {
			writeDocument(write, "Recovered", document)
			notifyDocumentChange(opts.OnChange, document)
		}
	}
	if opts.OnReady != nil {
		opts.OnReady()
	}
	write("Connected. Pad: %s\n", pad)
	write("Enter a line to publish it. Commands: /show, /clear, /quit\n")

	received := make(chan receiveResult, 1)
	recvCtx, cancelRecv := context.WithCancel(ctx)
	defer cancelRecv()
	go func() {
		for {
			frame, err := session.Recv(recvCtx)
			select {
			case received <- receiveResult{frame: frame, err: err}:
			case <-recvCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputSource.errors:
			if err != nil {
				return err
			}
			_ = session.Send(ctx, Frame{Type: FrameBye, Version: ProtocolVersion, Pad: pad})
			return nil
		case line := <-inputSource.lines:
			switch strings.TrimSpace(line) {
			case "/show":
				document := workspace.Snapshot(pad)
				writeDocument(write, "Local", document)
			case "/clear":
				document, err := workspace.Clear(pad, time.Now())
				if err != nil {
					return err
				}
				notifyDocumentChange(opts.OnChange, document)
				if err := session.Send(ctx, FrameFromDocument(FrameClear, document)); err != nil {
					return err
				}
				write("Sent clear revision %d\n", document.Revision)
			case "/quit":
				_ = session.Send(ctx, Frame{Type: FrameBye, Version: ProtocolVersion, Pad: pad})
				return nil
			default:
				document, err := workspace.Update(pad, line, time.Now())
				if err != nil {
					return err
				}
				notifyDocumentChange(opts.OnChange, document)
				if err := session.Send(ctx, FrameFromDocument(FrameUpdate, document)); err != nil {
					return err
				}
				write("Sent revision %d\n", document.Revision)
			}
		case result := <-received:
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					return ctx.Err()
				}
				return result.err
			}
			if result.frame.Pad != "" && NormalizePad(result.frame.Pad) != pad {
				return fmt.Errorf("peer selected note pad %q, local pad is %q", result.frame.Pad, pad)
			}
			switch result.frame.Type {
			case FrameUpdate, FrameClear:
				applied, document, err := workspace.ApplyRemote(result.frame.Document())
				if err != nil {
					return err
				}
				if applied {
					notifyDocumentChange(opts.OnChange, document)
				}
				if err := session.Send(ctx, Frame{
					Type:      FrameAck,
					Version:   ProtocolVersion,
					Pad:       pad,
					Revision:  document.Revision,
					Timestamp: document.Timestamp,
				}); err != nil {
					return err
				}
				if applied {
					writeDocument(write, "Remote", document)
				}
			case FrameAck:
				write("Synced revision %d\n", result.frame.Revision)
			case FramePing:
				if err := session.Send(ctx, Frame{Type: FramePong, Version: ProtocolVersion, Pad: pad}); err != nil {
					return err
				}
			case FramePong:
				write("Pong\n")
			case FrameBye:
				write("Peer closed the notepad.\n")
				return nil
			}
		}
	}
}

func notifyDocumentChange(callback func(Document), document Document) {
	if callback != nil {
		callback(document)
	}
}

func NewInteractiveInput(in io.Reader) *InteractiveInput {
	if in == nil {
		in = strings.NewReader("")
	}
	lines := make(chan string)
	errorsOut := make(chan error, 1)
	go readLines(in, lines, errorsOut)
	return &InteractiveInput{lines: lines, errors: errorsOut}
}

func readLines(in io.Reader, lines chan<- string, errorsOut chan<- error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), MaxTextSize+64*1024)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		errorsOut <- err
		return
	}
	errorsOut <- nil
}

func writeDocument(write func(string, ...any), owner string, document Document) {
	write("%s revision %d:\n", owner, document.Revision)
	if document.Text == "" {
		write("(empty)\n")
		return
	}
	write("%s\n", document.Text)
}
