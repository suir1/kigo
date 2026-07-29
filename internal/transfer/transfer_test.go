package transfer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/transport"
)

func TestSendReceiveText(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendText(ctx, transport.NewTCPTransport(a), "hello from test", SenderOptions{Code: "ABC123"})
	}()

	texts, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 || texts[0].Text != "hello from test" {
		t.Fatalf("unexpected texts: %#v", texts)
	}
}

func TestSenderFinalProgressWaitsForCompleteAcknowledgement(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var logs []string
	var logsMu sync.Mutex
	logf := func(format string, args ...any) {
		logsMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logsMu.Unlock()
	}
	hasFinalProgress := func() bool {
		logsMu.Lock()
		defer logsMu.Unlock()
		for _, line := range logs {
			if strings.HasPrefix(line, "sent ") && strings.Contains(line, " 100% ") {
				return true
			}
		}
		return false
	}

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendText(ctx, transport.NewTCPTransport(a), "hello from test", SenderOptions{
			Code: "ABC123",
			Logf: logf,
		})
	}()

	session, err := NewReceiverTransferSession(ctx, transport.NewTCPTransport(b), "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	for {
		event, err := session.ReceiveEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == EventDone {
			break
		}
	}
	if hasFinalProgress() {
		t.Fatal("sender reported final progress before receiver acknowledgement")
	}
	if err := session.SendComplete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if !hasFinalProgress() {
		t.Fatalf("sender omitted acknowledged final progress: %#v", logs)
	}
}

func TestReceiveRejectsCorruptTextHash(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemText,
			Name:            "message.txt",
			Size:            int64(len("hello")),
			SHA256:          strings.Repeat("0", 64),
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: false,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("hello"))}); err != nil {
			sendErr <- err
			return
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "done"})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("corrupt text hash was accepted")
	}
	_ = <-sendErr
}

func TestReceiveRejectsTextChunkPastDeclaredSize(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemText,
			Name:            "message.txt",
			Size:            3,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: false,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("hello"))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("oversized text chunk was accepted")
	}
	_ = <-sendErr
}

func TestSendReceiveFile(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	want := bytes.Repeat([]byte("kigo-file-transfer\n"), 9000)
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file mismatch: got %d bytes want %d", len(got), len(want))
	}
}

func TestReceiveRejectsFileChunkPastDeclaredSize(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            "input.txt",
			Size:            3,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		if _, err := pipe.recvMessage(ctx); err != nil {
			sendErr <- err
			return
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("hello"))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("oversized file chunk was accepted")
	}
	_ = <-sendErr
}

func TestReceiveRejectsUnexpectedFileChunkOffset(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            "input.txt",
			Size:            10,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		if _, err := pipe.recvMessage(ctx); err != nil {
			sendErr <- err
			return
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Offset: 5, Data: base64.StdEncoding.EncodeToString([]byte("hello"))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("unexpected file chunk offset was accepted")
	}
	_ = <-sendErr
}

func TestSendRejectsMismatchedResumeStream(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recvErr := make(chan error, 1)
	go func() {
		pipe, err := initReceiver(ctx, transport.NewTCPTransport(b), "ABC123")
		if err != nil {
			recvErr <- err
			return
		}
		msg, err := pipe.recvMessage(ctx)
		if err != nil {
			recvErr <- err
			return
		}
		if msg.Type != "manifest" {
			recvErr <- fmt.Errorf("expected manifest, got %q", msg.Type)
			return
		}
		stream := 99
		recvErr <- pipe.sendMessage(ctx, protocol.Message{
			Type:   "resume",
			Resume: []protocol.ResumeEntry{{Item: 0, Stream: &stream, Offset: 0}},
			At:     protocol.NowMillis(),
		})
	}()

	if err := SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"}); err == nil {
		t.Fatal("mismatched resume stream was accepted")
	}
	if err := <-recvErr; err != nil {
		t.Fatal(err)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0:               "0 B",
		999:             "999 B",
		1024:            "1.00 KB",
		10 * 1024:       "10.0 KB",
		100 * 1024:      "100 KB",
		5 * 1024 * 1024: "5.00 MB",
	}
	for input, want := range tests {
		if got := formatBytes(input); got != want {
			t.Fatalf("formatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestTotalResumeOffsetClampsToItemSize(t *testing.T) {
	items := []protocol.Item{
		{Name: "a", Size: 10},
		{Name: "b", Size: 20},
	}
	got := totalResumeOffset(items, map[int]int64{
		0: 5,
		1: 25,
		2: 100,
	})
	if got != 25 {
		t.Fatalf("got %d", got)
	}
}

func TestStreamProgressReporterClampsPerStream(t *testing.T) {
	items := []protocol.Item{
		{Name: "a", Size: 10},
		{Name: "b", Size: 20},
	}
	p := newStreamProgressReporter("sent", items, map[int]int64{0: 8}, nil)
	if p.done != 8 {
		t.Fatalf("initial done = %d, want 8", p.done)
	}
	if p.transferred != 0 {
		t.Fatalf("initial transferred = %d, want 0", p.transferred)
	}
	p.AddStream(0, 10)
	if p.streams[0].done != 10 {
		t.Fatalf("stream 0 done = %d, want 10", p.streams[0].done)
	}
	if p.done != 10 {
		t.Fatalf("total done after stream 0 clamp = %d, want 10", p.done)
	}
	p.AddStream(1, 7)
	if p.done != 17 {
		t.Fatalf("total done after stream 1 add = %d, want 17", p.done)
	}
	if p.transferred != 9 {
		t.Fatalf("transferred = %d, want 9", p.transferred)
	}
}

func TestManifestProgressReporterUsesStreamBindings(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{
		{Name: "a", Size: 10},
		{Name: "b", Size: 20},
	})
	manifest.Streams = []protocol.StreamBinding{
		{ID: 10, Item: 0},
		{ID: 20, Item: 1},
	}
	p, err := newManifestProgressReporter("received", &manifest, map[int]int64{0: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.done != 8 {
		t.Fatalf("initial done = %d, want 8", p.done)
	}
	p.AddStream(10, 2)
	p.AddStream(20, 7)
	if p.done != 17 {
		t.Fatalf("done = %d, want 17", p.done)
	}
	if p.transferred != 9 {
		t.Fatalf("transferred = %d, want 9", p.transferred)
	}
}

func TestSendReceiveDirectoryPreservesTopLevelDirectory(t *testing.T) {
	srcRoot := t.TempDir()
	outDir := t.TempDir()
	dir := filepath.Join(srcRoot, "bundle")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendPath(ctx, transport.NewTCPTransport(a), dir, SenderOptions{Code: "ABC123"})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(outDir, "bundle", "a.txt"), "alpha")
	assertFileContent(t, filepath.Join(outDir, "bundle", "nested", "b.txt"), "beta")
}

func TestSendReceiveEmptyDirectoryPreservesMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory mode assertions")
	}
	srcRoot := t.TempDir()
	outDir := t.TempDir()
	dir := filepath.Join(srcRoot, "empty")
	if err := os.Mkdir(dir, 0o710); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_700_000_123, 0)
	if err := os.Chtimes(dir, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendPath(ctx, transport.NewTCPTransport(a), dir, SenderOptions{Code: "ABC123"})
	}()
	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(outDir, "empty"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o710 {
		t.Fatalf("received directory mode = %v", info.Mode())
	}
	if info.ModTime().UnixMilli() != wantTime.UnixMilli() {
		t.Fatalf("received directory mtime = %s, want %s", info.ModTime(), wantTime)
	}
}

func TestSendReceivePreservedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation may require privileges")
	}
	srcRoot := t.TempDir()
	outDir := t.TempDir()
	dir := filepath.Join(srcRoot, "bundle")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePathWithOptions(dir, PrepareOptions{Symlinks: SymlinkPreserve})
	if err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- prepared.Send(ctx, transport.NewTCPTransport(a), SenderOptions{Code: "ABC123"})
	}()
	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(outDir, "bundle", "link.txt")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("received path is not a symlink: %v", info.Mode())
	}
	if target, err := os.Readlink(linkPath); err != nil || target != "target.txt" {
		t.Fatalf("symlink target = %q, err=%v", target, err)
	}
}

func TestPreparePathFollowsFileSymlinkByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation may require privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePath(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.items) != 1 || prepared.items[0].Kind != protocol.ItemFile || prepared.items[0].Name != "link.txt" {
		t.Fatalf("prepared items = %#v", prepared.items)
	}
}

func TestPreparePathAddsSampleFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.bin")
	data := []byte("sample fingerprint payload")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.items) != 1 || prepared.items[0].SampleSHA256 != bytesSHA256(data) || prepared.items[0].SHA256 != "" {
		t.Fatalf("prepared items = %#v", prepared.items)
	}
	deferred, err := prepared.manifestItems(true)
	if err != nil {
		t.Fatal(err)
	}
	if deferred[0].SHA256 != "" {
		t.Fatalf("deferred manifest unexpectedly hashed source: %#v", deferred[0])
	}
	accepted, _, err := prepared.acceptResumeRequests([]protocol.ResumeEntry{{
		Item: 0, Offset: int64(len(data)), Skip: true, Complete: true,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].SHA256 != "" || prepared.items[0].SHA256 != "" {
		t.Fatalf("completed skip hashed source: accepted=%#v item=%#v", accepted, prepared.items[0])
	}
	legacy, err := prepared.manifestItems(false)
	if err != nil {
		t.Fatal(err)
	}
	if legacy[0].SHA256 != bytesSHA256(data) {
		t.Fatalf("legacy manifest sha256=%q", legacy[0].SHA256)
	}
}

func TestPreparePathRejectsUnsafePreservedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation may require privileges")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "escape")
	if err := os.Symlink("../outside", link); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePathWithOptions(link, PrepareOptions{Symlinks: SymlinkPreserve}); err == nil {
		t.Fatal("unsafe symlink target was accepted")
	}
}

func TestParseSymlinkMode(t *testing.T) {
	for _, value := range []string{"", "follow", "preserve", " PRESERVE "} {
		if _, err := ParseSymlinkMode(value); err != nil {
			t.Fatalf("ParseSymlinkMode(%q): %v", value, err)
		}
	}
	if _, err := ParseSymlinkMode("copy"); err == nil {
		t.Fatal("invalid symlink mode was accepted")
	}
}

func TestSendPathUsesWeightedDirectoryFileScheduling(t *testing.T) {
	srcRoot := t.TempDir()
	dir := filepath.Join(srcRoot, "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), bytes.Repeat([]byte("a"), protocol.ChunkSize*2+17), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), bytes.Repeat([]byte("b"), protocol.ChunkSize*2+19), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendPath(ctx, transport.NewTCPTransport(a), dir, SenderOptions{Code: "ABC123"})
	}()

	pipe, err := initReceiver(ctx, transport.NewTCPTransport(b), "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := pipe.recvMessage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != "manifest" || msg.Manifest == nil {
		t.Fatalf("expected manifest, got %#v", msg)
	}
	if len(msg.Manifest.Items) != 3 || msg.Manifest.Items[0].Kind != protocol.ItemDirectory {
		t.Fatalf("expected root directory and 2 files, got %#v", msg.Manifest.Items)
	}
	for _, item := range msg.Manifest.Items[1:] {
		if item.SHA256 != "" || !isHexSHA256(item.SampleSHA256) {
			t.Fatalf("deferred manifest file hashes = %#v", item)
		}
	}
	streamForItem := map[int]int{}
	for _, binding := range msg.Manifest.Streams {
		streamForItem[binding.Item] = binding.ID
	}
	resume := []protocol.ResumeEntry{{Item: 1, Offset: 0}, {Item: 2, Offset: 0}}
	if err := pipe.sendMessage(ctx, protocol.Message{Type: "resume", Resume: resume, At: protocol.NowMillis()}); err != nil {
		t.Fatal(err)
	}

	var order []int
	opens := map[int]int{}
	ends := map[int]int{}
	for {
		msg, err := pipe.recvMessage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch msg.Type {
		case "resume_accept":
			if len(msg.Resume) != 2 || msg.Resume[0].Offset != 0 || msg.Resume[1].Offset != 0 {
				t.Fatalf("resume_accept = %#v", msg.Resume)
			}
			if !isHexSHA256(msg.Resume[0].SHA256) || !isHexSHA256(msg.Resume[1].SHA256) {
				t.Fatalf("resume_accept omitted deferred hashes: %#v", msg.Resume)
			}
		case "stream_open":
			if msg.Stream == nil {
				t.Fatalf("stream_open for item %d omitted stream", msg.Item)
			}
			if *msg.Stream != streamForItem[msg.Item] {
				t.Fatalf("stream_open stream = %d, want binding %d", *msg.Stream, streamForItem[msg.Item])
			}
			opens[msg.Item]++
		case "chunk":
			if msg.Stream == nil {
				t.Fatalf("chunk for item %d omitted stream", msg.Item)
			}
			if *msg.Stream != streamForItem[msg.Item] {
				t.Fatalf("chunk stream = %d, want binding %d", *msg.Stream, streamForItem[msg.Item])
			}
			order = append(order, msg.Item)
		case "stream_end":
			if msg.Stream == nil {
				t.Fatalf("stream_end for item %d omitted stream", msg.Item)
			}
			if *msg.Stream != streamForItem[msg.Item] {
				t.Fatalf("stream_end stream = %d, want binding %d", *msg.Stream, streamForItem[msg.Item])
			}
			ends[msg.Item]++
		case "done":
			if err := pipe.sendMessage(ctx, protocol.Message{Type: "complete", At: protocol.NowMillis()}); err != nil {
				t.Fatal(err)
			}
			if err := <-sendErr; err != nil {
				t.Fatal(err)
			}
			wantPrefix := []int{1, 1, 1, 2}
			if len(order) < len(wantPrefix) {
				t.Fatalf("chunk order too short: %v", order)
			}
			for i, want := range wantPrefix {
				if order[i] != want {
					t.Fatalf("chunk order prefix = %v, want %v", order[:len(wantPrefix)], wantPrefix)
				}
			}
			for _, item := range []int{0, 1, 2} {
				if opens[item] != 1 {
					t.Fatalf("stream_open count for item %d = %d, want 1", item, opens[item])
				}
				if ends[item] != 1 {
					t.Fatalf("stream_end count for item %d = %d, want 1", item, ends[item])
				}
			}
			return
		default:
			t.Fatalf("unexpected message type %q", msg.Type)
		}
	}
}

func TestFileStreamWeightPrioritizesSmallerRemainders(t *testing.T) {
	if got := fileStreamWeight(1 << 20); got != 4 {
		t.Fatalf("small file weight = %d, want 4", got)
	}
	if got := fileStreamWeight(2 << 20); got != 2 {
		t.Fatalf("medium file weight = %d, want 2", got)
	}
	if got := fileStreamWeight(32 << 20); got != 1 {
		t.Fatalf("large file weight = %d, want 1", got)
	}
}

func TestReceiveRejectsMismatchedChunkStream(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            "input.txt",
			Size:            5,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		if _, err := pipe.recvMessage(ctx); err != nil {
			sendErr <- err
			return
		}
		stream := 1
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Stream: &stream, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("hello"))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("mismatched chunk stream was accepted")
	}
	_ = <-sendErr
}

func TestReceiveRejectsChunkAfterStreamEnd(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            "input.txt",
			Size:            5,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))}); err != nil {
			sendErr <- err
			return
		}
		if _, err := pipe.recvMessage(ctx); err != nil {
			sendErr <- err
			return
		}
		stream := 0
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "stream_open", Item: 0, Stream: &stream}); err != nil {
			sendErr <- err
			return
		}
		if err := pipe.sendMessage(ctx, protocol.Message{Type: "stream_end", Item: 0, Stream: &stream}); err != nil {
			sendErr <- err
			return
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "chunk", Item: 0, Stream: &stream, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("hello"))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("chunk after stream_end was accepted")
	}
	_ = <-sendErr
}

func TestReceiveRejectsUnsafeManifestPath(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            "../evil.txt",
			Size:            0,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		}
		sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("unsafe manifest path was accepted")
	}
	_ = <-sendErr
}

func TestReceiveRejectsInvalidManifestItems(t *testing.T) {
	tests := []struct {
		name  string
		items []protocol.Item
	}{
		{
			name: "negative-size",
			items: []protocol.Item{{
				Kind:      protocol.ItemFile,
				Name:      "input.txt",
				Size:      -1,
				ChunkSize: protocol.ChunkSize,
			}},
		},
		{
			name: "duplicate-file-path",
			items: []protocol.Item{
				{Kind: protocol.ItemFile, Name: "input.txt", Size: 0, ChunkSize: protocol.ChunkSize},
				{Kind: protocol.ItemFile, Name: "input.txt", Size: 0, ChunkSize: protocol.ChunkSize},
			},
		},
		{
			name: "duplicate-path-across-kinds",
			items: []protocol.Item{
				{Kind: protocol.ItemDirectory, Name: "shared", ChunkSize: protocol.ChunkSize},
				{Kind: protocol.ItemFile, Name: "shared", ChunkSize: protocol.ChunkSize},
			},
		},
		{
			name: "bad-sha256",
			items: []protocol.Item{{
				Kind:      protocol.ItemText,
				Name:      "message.txt",
				Size:      0,
				SHA256:    "not-a-sha256",
				ChunkSize: protocol.ChunkSize,
			}},
		},
		{
			name: "bad-sample-sha256",
			items: []protocol.Item{{
				Kind:         protocol.ItemFile,
				Name:         "input.txt",
				Size:         0,
				SampleSHA256: "not-a-sample-sha256",
				ChunkSize:    protocol.ChunkSize,
			}},
		},
		{
			name: "bad-chunk-size",
			items: []protocol.Item{{
				Kind:      protocol.ItemText,
				Name:      "message.txt",
				Size:      0,
				ChunkSize: protocol.ChunkSize + 1,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			sendErr := make(chan error, 1)
			go func() {
				tp := transport.NewTCPTransport(a)
				pipe, err := initSender(ctx, tp, "ABC123")
				if err != nil {
					sendErr <- err
					return
				}
				sendErr <- pipe.sendMessage(ctx, protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest(tt.items))})
			}()

			if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
			_ = <-sendErr
		})
	}
}

func TestReceiveRejectsUnexpectedEnvelopeSequence(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(a)
		pipe, err := initSender(ctx, tp, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		item := protocol.Item{
			Kind:      protocol.ItemText,
			Name:      "message.txt",
			Size:      0,
			ChunkSize: protocol.ChunkSize,
		}
		plain, err := json.Marshal(protocol.Message{Type: "manifest", Version: protocol.Version, Manifest: ptr(protocol.NewManifest([]protocol.Item{item}))})
		if err != nil {
			sendErr <- err
			return
		}
		ciphertext, err := pipe.sendSession.Encrypt(1, plain)
		if err != nil {
			sendErr <- err
			return
		}
		payload, err := json.Marshal(envelope{Version: protocol.Version, Seq: 1, Body: base64.StdEncoding.EncodeToString(ciphertext)})
		if err != nil {
			sendErr <- err
			return
		}
		sendErr <- tp.Send(ctx, payload)
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("unexpected envelope sequence was accepted")
	}
	_ = <-sendErr
}

func TestReceiverRejectsUnsupportedHelloVersion(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- sendPlain(ctx, transport.NewTCPTransport(a), helloMessage{
			Type:        "hello",
			Version:     protocol.Version + 1,
			SenderNonce: "sender",
		})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: t.TempDir()}); err == nil {
		t.Fatal("unsupported hello version was accepted")
	}
	_ = <-sendErr
}

func TestSenderRejectsUnsupportedHelloAckVersion(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recvErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(b)
		var hello helloMessage
		if err := recvPlain(ctx, tp, &hello); err != nil {
			recvErr <- err
			return
		}
		recvErr <- sendPlain(ctx, tp, helloMessage{
			Type:          "hello_ack",
			Version:       protocol.Version + 1,
			ReceiverNonce: "receiver",
		})
	}()

	if err := SendText(ctx, transport.NewTCPTransport(a), "hello", SenderOptions{Code: "ABC123"}); err == nil {
		t.Fatal("unsupported hello_ack version was accepted")
	}
	_ = <-recvErr
}

func TestSenderRequiresCompleteAcknowledgement(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receiverDone := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(b)
		session, err := NewReceiverTransferSession(ctx, tp, "ABC123")
		if err != nil {
			receiverDone <- err
			return
		}
		for {
			event, err := session.ReceiveEvent(ctx)
			if err != nil {
				receiverDone <- err
				return
			}
			if event.Kind == EventDone {
				receiverDone <- tp.Close()
				return
			}
		}
	}()

	err := SendText(ctx, transport.NewTCPTransport(a), "hello", SenderOptions{Code: "ABC123"})
	if err == nil {
		t.Fatal("sender accepted a closed connection without complete acknowledgement")
	}
	if err := <-receiverDone; err != nil {
		t.Fatal(err)
	}
}

func TestReceiveResumesExistingPart(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	prefix := []byte(strings.Repeat("prefix-", 10_000))
	suffix := []byte(strings.Repeat("suffix-", 10_000))
	want := append(append([]byte(nil), prefix...), suffix...)
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "input.txt.kigopart"), prefix, 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("resumed file mismatch: got %d bytes want %d", len(got), len(want))
	}
}

func TestReceiveRestartsCorruptResumedPart(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	prefix := []byte(strings.Repeat("prefix-", 10_000))
	suffix := []byte(strings.Repeat("suffix-", 10_000))
	want := append(append([]byte(nil), prefix...), suffix...)
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "input.txt.kigopart"), bytes.Repeat([]byte("x"), len(prefix)), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"})
	}()

	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{Code: "ABC123", OutputDir: outDir}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restarted file mismatch: got %d bytes want %d", len(got), len(want))
	}
	if _, err := os.Stat(filepath.Join(outDir, "input.txt.kigopart")); !os.IsNotExist(err) {
		t.Fatalf("part file still exists: %v", err)
	}
}

func TestReceiveFastSkipsCompletedFile(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.bin")
	dst := filepath.Join(outDir, "input.bin")
	data := bytes.Repeat([]byte("completed-file-sample"), 100_000)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o640); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var senderLogs []string
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{
			Code: "ABC123",
			Logf: func(format string, args ...any) {
				senderLogs = append(senderLogs, fmt.Sprintf(format, args...))
			},
		})
	}()
	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{
		Code:      "ABC123",
		OutputDir: outDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range senderLogs {
		if line == "skipped already-complete input.bin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sender did not report completed skip: %#v", senderLogs)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fast-skipped destination changed")
	}
}

func TestReceiveSkipsDifferentExistingFile(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	if err := os.WriteFile(src, []byte("new payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(outDir, "input.txt")
	if err := os.WriteFile(dst, []byte("keep existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"})
	}()
	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{
		Code:      "ABC123",
		OutputDir: outDir,
		Conflict:  ConflictSkip,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, dst, "keep existing")
}

func TestReceiveRenamesDifferentExistingFile(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := filepath.Join(srcDir, "input.txt")
	if err := os.WriteFile(src, []byte("new payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "input.txt"), []byte("keep existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendFile(ctx, transport.NewTCPTransport(a), src, SenderOptions{Code: "ABC123"})
	}()
	if _, err := Receive(ctx, transport.NewTCPTransport(b), ReceiverOptions{
		Code:      "ABC123",
		OutputDir: outDir,
		Conflict:  ConflictRename,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(outDir, "input.txt"), "keep existing")
	assertFileContent(t, filepath.Join(outDir, "input (1).txt"), "new payload")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
