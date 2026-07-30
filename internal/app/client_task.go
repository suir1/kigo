package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transfer"
	"github.com/suir1/kigo/internal/transport"
	"github.com/suir1/kigo/internal/transport/webrtcx"
)

func runClientTask(
	ctx context.Context,
	g *globalOptions,
	request nativeTaskRequest,
	output *clientTaskOutput,
) error {
	options := cloneGlobalOptions(g)
	options.taskOutput = output
	switch request := request.(type) {
	case sendTaskRequest:
		return runSendTask(ctx, options, request)
	case receiveTaskRequest:
		return runReceiveTask(ctx, options, request)
	case textSendTaskRequest:
		return runTextSendTask(ctx, options, request)
	case doctorTaskRequest:
		return runDoctorTask(ctx, options, request)
	default:
		return fmt.Errorf("unsupported native task %T", request)
	}
}

func runSendTask(ctx context.Context, g *globalOptions, request sendTaskRequest) error {
	symlinks, err := transfer.ParseSymlinkMode(request.Symlinks)
	if err != nil {
		return err
	}
	prepared, err := transfer.PreparePathWithOptions(request.Path, transfer.PrepareOptions{
		Symlinks:    symlinks,
		NoGitIgnore: request.NoGitIgnore,
	})
	if err != nil {
		return err
	}
	taskLinef(g, "Prepared: %s", prepared.Summary())
	code, err := secure.ResolveSenderCode(request.Code)
	if err != nil {
		return err
	}
	taskCode(g, code)
	printShareTarget(g, code)
	printQRCodeIfTerminal(taskWriter(g), transferQRCodeTarget(g, code), !request.NoQRCode)
	taskLine(g, "Waiting for receiver...")
	err = runPairedTransfer(
		ctx, g, code, "sender",
		func(ctx context.Context, t transport.Transport, routeOptions *globalOptions) error {
			return prepared.Send(ctx, t, transfer.SenderOptions{Code: code, Logf: taskLogger(routeOptions)})
		},
	)
	if err != nil {
		return err
	}
	if request.Remember {
		if err := rememberSendSettings(request.Path, string(symlinks), request.NoGitIgnore); err != nil {
			fmt.Fprintln(taskErrorWriter(g), "warning: could not remember send settings:", err)
		}
	}
	return nil
}

func runReceiveTask(ctx context.Context, g *globalOptions, request receiveTaskRequest) error {
	conflict, err := transfer.ParseConflictPolicy(request.OnConflict)
	if err != nil {
		return err
	}
	code, err := secure.ValidateCode(request.Code)
	if err != nil {
		return err
	}
	out := strings.TrimSpace(request.OutputDir)
	if out == "" {
		out = "."
	}
	taskLinef(g, "Joining room: %s", code)
	var texts []transfer.ReceivedText
	err = runPairedTransfer(
		ctx, g, code, "receiver",
		func(ctx context.Context, t transport.Transport, routeOptions *globalOptions) error {
			var receiveErr error
			texts, receiveErr = transfer.Receive(ctx, t, transfer.ReceiverOptions{
				Code:      code,
				OutputDir: out,
				Logf:      taskLogger(routeOptions),
				Conflict:  conflict,
			})
			return receiveErr
		},
	)
	if err != nil {
		return err
	}
	for _, text := range texts {
		taskLine(g, text.Text)
	}
	if request.Remember {
		if err := rememberReceiveSettings(out, string(conflict)); err != nil {
			fmt.Fprintln(taskErrorWriter(g), "warning: could not remember receive settings:", err)
		}
	}
	return nil
}

func runTextSendTask(ctx context.Context, g *globalOptions, request textSendTaskRequest) error {
	code, err := secure.ResolveSenderCode(request.Code)
	if err != nil {
		return err
	}
	taskCode(g, code)
	printShareTarget(g, code)
	printQRCodeIfTerminal(taskWriter(g), transferQRCodeTarget(g, code), !request.NoQRCode)
	taskLine(g, "Waiting for receiver...")
	return runPairedTransfer(
		ctx, g, code, "sender",
		func(ctx context.Context, t transport.Transport, routeOptions *globalOptions) error {
			return transfer.SendText(ctx, t, request.Text, transfer.SenderOptions{
				Code: code,
				Logf: taskLogger(routeOptions),
			})
		},
	)
}

type pairedTransferAttempt func(context.Context, transport.Transport, *globalOptions) error

func runPairedTransfer(
	ctx context.Context,
	g *globalOptions,
	code string,
	role string,
	attempt pairedTransferAttempt,
) error {
	token := secure.RoomToken(code)
	pairing := newPairingWindow(g)
	routeOptions, err := withPairingWindow(ctx, pairing, func(pairCtx context.Context) (*globalOptions, error) {
		return resolveTransferOptions(pairCtx, g, token, role)
	})
	if err != nil {
		return err
	}
	reconnect := &webrtcx.ReconnectState{}
	dial := pairing.dialer(func(ctx context.Context) (transport.Transport, error) {
		return dialTransport(ctx, routeOptions, token, role, reconnect)
	})
	return runTransferWithReconnectGate(
		ctx,
		routeOptions,
		func() bool { return webRTCReconnectAllowed(routeOptions, reconnect) },
		dial,
		func(ctx context.Context, t transport.Transport) error {
			return attempt(ctx, t, routeOptions)
		},
	)
}

func runDoctorTask(ctx context.Context, g *globalOptions, request doctorTaskRequest) error {
	if request.Timeout <= 0 {
		request.Timeout = 3 * time.Second
	}
	if request.AITimeout <= 0 {
		request.AITimeout = 8 * time.Second
	}
	report := buildDoctorReportWithExplanation(
		ctx, g, "native-native", request.Timeout, request.AIExplain, request.AITimeout,
	)
	if request.JSON {
		return writeDoctorJSON(taskWriter(g), report)
	}
	return printDoctorReport(taskWriter(g), report)
}

func taskLine(g *globalOptions, text string) {
	if g != nil && g.taskOutput != nil {
		g.taskOutput.line(text)
		return
	}
	fmt.Fprintln(os.Stdout, text)
}

func taskLinef(g *globalOptions, format string, args ...any) {
	taskLine(g, fmt.Sprintf(format, args...))
}

func taskCode(g *globalOptions, code string) {
	if g != nil && g.taskOutput != nil {
		g.taskOutput.code(code)
		return
	}
	fmt.Fprintln(os.Stdout, "Code:", code)
}

func taskLink(g *globalOptions, link string) {
	if g != nil && g.taskOutput != nil {
		g.taskOutput.link(link)
		return
	}
	fmt.Fprintln(os.Stdout, "Link:", link)
}

func taskLogger(g *globalOptions) func(string, ...any) {
	return func(format string, args ...any) {
		if g != nil && g.taskOutput != nil {
			g.taskOutput.logf(format, args...)
			return
		}
		fmt.Fprintf(os.Stdout, time.Now().Format("15:04:05")+" "+format+"\n", args...)
	}
}

func taskLogf(g *globalOptions, format string, args ...any) {
	taskLogger(g)(format, args...)
}

func taskWriter(g *globalOptions) io.Writer {
	if g != nil && g.taskOutput != nil {
		return g.taskOutput.out
	}
	return os.Stdout
}

func taskErrorWriter(g *globalOptions) io.Writer {
	if g != nil && g.taskOutput != nil {
		return g.taskOutput.err
	}
	return os.Stderr
}
