package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
)

func newNoteCommand(g *globalOptions) *cobra.Command {
	var pad, customCode string
	var noQRCode, recentJSON bool
	noteCommand := &cobra.Command{
		Use:   "note",
		Short: "Share a live encrypted notepad",
	}
	noteCommand.PersistentFlags().StringVar(&pad, "pad", note.DefaultPad, "notepad name")
	host := &cobra.Command{
		Use:   "host",
		Short: "Create or open a persistent shared notepad",
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
			fmt.Fprintf(cmd.OutOrStdout(), "Open: kigo note join %s\n", code)
			printNoteShareTarget(cmd.OutOrStdout(), g, code, pad)
			printQRCodeIfTerminal(cmd.OutOrStdout(), noteQRCodeTarget(g, code, pad), !noQRCode)
			fmt.Fprintln(cmd.OutOrStdout(), "Opening persistent notepad...")
			return runNoteCommand(cmd.Context(), g, code, true, pad, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	join := &cobra.Command{
		Use:   "join <code>",
		Short: "Open a persistent shared notepad",
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
			fmt.Fprintln(cmd.OutOrStdout(), "Opening notepad:", code)
			return runNoteCommand(cmd.Context(), g, code, false, pad, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	recent := &cobra.Command{
		Use:   "recent",
		Short: "List recently opened notepads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listRecentNotes(cmd.OutOrStdout(), recentJSON)
		},
	}
	recent.Flags().BoolVar(&recentJSON, "json", false, "print recent notepads as JSON")
	favorite := &cobra.Command{
		Use:   "favorite <code>",
		Short: "Pin a recent notepad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setRecentNoteFavorite(args[0], pad, true)
		},
	}
	unfavorite := &cobra.Command{
		Use:   "unfavorite <code>",
		Short: "Unpin a recent notepad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setRecentNoteFavorite(args[0], pad, false)
		},
	}
	forget := &cobra.Command{
		Use:   "forget <code>",
		Short: "Remove a notepad from local recents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return forgetRecentNote(args[0], pad)
		},
	}
	host.Flags().BoolVar(&noQRCode, "no-qrcode", false, "do not print a terminal QR code")
	host.Flags().StringVar(&customCode, "code", "", "custom pairing code; random six-character code when omitted")
	noteCommand.AddCommand(host, join, recent, favorite, unfavorite, forget)
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
	recentWarning := false
	persistDraft := func(document note.Document) {
		if err := saveNoteDraft(drafts, code, host, document); err != nil && !draftWarning {
			draftWarning = true
			fmt.Fprintf(out, "Warning: save encrypted note draft: %v\n", err)
		}
	}
	input := note.NewInteractiveInput(in)
	maxAttempts, err := totalReconnectAttempts(g, true)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		session, openErr := note.OpenPersistentSession(ctx, note.PersistentOptions{
			ServiceBase: g.Signal,
			Code:        code,
			Pad:         pad,
			Dialer:      outboundWebSocketDialer(g),
		})
		if openErr == nil {
			openErr = note.RunInteractive(ctx, session, note.InteractiveOptions{
				Pad:           pad,
				Out:           out,
				Workspace:     workspace,
				Input:         input,
				SyncWorkspace: true,
				OnChange:      persistDraft,
				OnReady: func() {
					if touchErr := noteRecentStore().Touch(code, pad); touchErr != nil && !recentWarning {
						recentWarning = true
						fmt.Fprintf(out, "Warning: save recent notepad: %v\n", touchErr)
					}
				},
			})
			_ = session.Close()
		}
		if openErr == nil {
			return nil
		}
		if attempt >= maxAttempts || !isRetryableTransferError(openErr) {
			return openErr
		}
		fmt.Fprintf(out, "Notepad connection interrupted: %v\n", openErr)
		fmt.Fprintf(out, "Reconnecting attempt %d/%d in %s\n", attempt+1, maxAttempts, g.ReconnectDelay)
		timer := time.NewTimer(g.ReconnectDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
