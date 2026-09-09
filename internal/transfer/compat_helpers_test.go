package transfer

import (
	"context"

	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/transport"
)

func SendFile(ctx context.Context, t transport.Transport, path string, opts SenderOptions) error {
	return SendPath(ctx, t, path, opts)
}

func SendPath(ctx context.Context, t transport.Transport, path string, opts SenderOptions) error {
	prepared, err := PreparePath(path)
	if err != nil {
		return err
	}
	return prepared.Send(ctx, t, opts)
}

func PreparePath(path string) (*PreparedPath, error) {
	return PreparePathWithOptions(path, PrepareOptions{Symlinks: SymlinkFollow})
}

func NewReceiveStore(manifest *protocol.Manifest, outputDir string, logf Logger) (*ReceiveStore, error) {
	return NewReceiveStoreWithOptions(manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Logf:      logf,
		Conflict:  ConflictOverwrite,
	})
}

func initSender(ctx context.Context, t transport.Transport, code string) (*securePipe, error) {
	pipes, err := initSenderPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	return pipes[0], nil
}

func initReceiver(ctx context.Context, t transport.Transport, code string) (*securePipe, error) {
	pipes, err := initReceiverPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	return pipes[0], nil
}
