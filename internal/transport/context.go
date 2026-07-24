package transport

import (
	"context"
	"io"
	"sync"
)

// CloseOnContextDone closes closer if ctx ends before the returned stop
// function transfers ownership to the caller. Stop waits for the watcher to
// exit so a later cancellation cannot close an established connection.
func CloseOnContextDone(ctx context.Context, closer io.Closer) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = closer.Close()
		case <-stop:
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}
