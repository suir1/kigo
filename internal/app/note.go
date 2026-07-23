package app

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
	"github.com/suir1/kigo/internal/transport/webrtcx"
)

func newNoteCommand(g *globalOptions) *cobra.Command {
	var pad, customCode string
	var noQRCode bool
	noteCommand := &cobra.Command{
		Use:   "note",
		Short: "Share a live encrypted notepad",
	}
	noteCommand.PersistentFlags().StringVar(&pad, "pad", note.DefaultPad, "notepad name")
	host := &cobra.Command{
		Use:   "host",
		Short: "Host a shared notepad",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pad = note.NormalizePad(pad)
			if err := note.ValidatePad(pad); err != nil {
				return err
			}
			code, err := secure.ResolveSenderCode(customCode)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Code:", code)
			fmt.Fprintf(cmd.OutOrStdout(), "Join: kigo note join %s\n", code)
			printNoteShareTarget(cmd.OutOrStdout(), g, code, pad)
			printQRCodeIfTerminal(cmd.OutOrStdout(), noteQRCodeTarget(g, code, pad), !noQRCode)
			fmt.Fprintln(cmd.OutOrStdout(), "Waiting for peer...")
			return runNoteCommand(cmd.Context(), g, code, true, pad, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	join := &cobra.Command{
		Use:   "join <code>",
		Short: "Join a shared notepad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pad = note.NormalizePad(pad)
			if err := note.ValidatePad(pad); err != nil {
				return err
			}
			code, err := secure.ValidateCode(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Joining room:", code)
			return runNoteCommand(cmd.Context(), g, code, false, pad, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	host.Flags().BoolVar(&noQRCode, "no-qrcode", false, "do not print a terminal QR code")
	host.Flags().StringVar(&customCode, "code", "", "custom pairing code; random six-character code when omitted")
	noteCommand.AddCommand(host, join)
	return noteCommand
}

func printNoteShareTarget(out io.Writer, g *globalOptions, code, pad string) {
	if link := notePublicLink(g, code, pad); link != "" {
		fmt.Fprintln(out, "Link:", link)
	}
}

func runNoteCommand(
	parent context.Context,
	g *globalOptions,
	code string,
	host bool,
	pad string,
	in io.Reader,
	out io.Writer,
) error {
	ctx := withInterrupt(parent)
	role := "receiver"
	if host {
		role = "sender"
	}
	pairing := newPairingWindow(g)
	routeOptions, err := withPairingWindow(ctx, pairing, func(pairCtx context.Context) (*globalOptions, error) {
		return resolveNoteOptions(pairCtx, g, secure.RoomToken(code), role)
	})
	if err != nil {
		return err
	}
	reconnect := &webrtcx.ReconnectState{}
	workspace := note.NewWorkspace()
	var drafts *note.DraftStore
	if !g.NoNoteDrafts {
		drafts = note.NewDraftStore(noteDraftPath())
	}
	if document, ok, loadErr := loadNoteDraft(drafts, code, host, pad); loadErr != nil {
		fmt.Fprintf(out, "Warning: load encrypted note draft: %v\n", loadErr)
	} else if ok {
		if _, _, applyErr := workspace.ApplyRemote(document); applyErr != nil {
			fmt.Fprintf(out, "Warning: restore encrypted note draft: %v\n", applyErr)
		} else {
			fmt.Fprintf(out, "Recovered encrypted draft revision %d.\n", document.Revision)
		}
	}
	draftWarning := false
	persistDraft := func(document note.Document) {
		if err := saveNoteDraft(drafts, code, host, document); err != nil && !draftWarning {
			draftWarning = true
			fmt.Fprintf(out, "Warning: save encrypted note draft: %v\n", err)
		}
	}
	input := note.NewInteractiveInput(in)
	return runTransferWithReconnectGate(
		ctx,
		routeOptions,
		func() bool { return webRTCReconnectAllowed(routeOptions, reconnect) },
		pairing.dialer(func(ctx context.Context) (transport.Transport, error) {
			return dialTransport(ctx, routeOptions, secure.RoomToken(code), role, reconnect)
		}),
		func(ctx context.Context, t transport.Transport) error {
			var session *note.Session
			var err error
			if host {
				session, err = note.NewHost(ctx, t, code)
			} else {
				session, err = note.NewJoin(ctx, t, code)
			}
			if err != nil {
				return err
			}
			defer session.Close()
			return note.RunInteractive(ctx, session, note.InteractiveOptions{
				Pad:           pad,
				Out:           out,
				Workspace:     workspace,
				Input:         input,
				SyncWorkspace: true,
				OnChange:      persistDraft,
			})
		},
	)
}
