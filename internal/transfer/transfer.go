package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/mux"
	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/transport"
)

type Logger func(format string, args ...any)

const progressLogInterval = 500 * time.Millisecond

const minimumAdaptiveChunkSize = 16 * 1024

type SenderOptions struct {
	Code string
	Logf Logger
}

type ReceiverOptions struct {
	Code      string
	OutputDir string
	Logf      Logger
	Conflict  ConflictPolicy
}

type ReceivedText struct {
	Name string
	Text string
}

type sendFileEntry struct {
	Path   string
	ItemID int
	Item   protocol.Item
}

type PreparedPath struct {
	files   []sendFileEntry
	items   []protocol.Item
	ignored int
}

type SymlinkMode string

const (
	SymlinkFollow   SymlinkMode = "follow"
	SymlinkPreserve SymlinkMode = "preserve"
)

type PrepareOptions struct {
	Symlinks    SymlinkMode
	NoGitIgnore bool
}

type PreparedSummary struct {
	Files       int
	Directories int
	Symlinks    int
	Bytes       int64
	Ignored     int
}

func (s PreparedSummary) String() string {
	return fmt.Sprintf(
		"%d %s, %d %s, %d %s, %s, %d ignored",
		s.Files, plural(s.Files, "file", "files"),
		s.Directories, plural(s.Directories, "directory", "directories"),
		s.Symlinks, plural(s.Symlinks, "symlink", "symlinks"),
		formatBytes(s.Bytes),
		s.Ignored,
	)
}

func plural(count int, singular, multiple string) string {
	if count == 1 {
		return singular
	}
	return multiple
}

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

func PreparePathWithOptions(path string, opts PrepareOptions) (*PreparedPath, error) {
	symlinks, err := ParseSymlinkMode(string(opts.Symlinks))
	if err != nil {
		return nil, err
	}
	files, items, ignored, err := collectSendItems(path, symlinks, !opts.NoGitIgnore)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("no files or directories to send")
	}
	return &PreparedPath{files: files, items: items, ignored: ignored}, nil
}

func (p *PreparedPath) Summary() PreparedSummary {
	if p == nil {
		return PreparedSummary{}
	}
	summary := PreparedSummary{Ignored: p.ignored}
	for _, item := range p.items {
		switch item.Kind {
		case protocol.ItemFile:
			summary.Files++
			summary.Bytes += item.Size
		case protocol.ItemDirectory:
			summary.Directories++
		case protocol.ItemSymlink:
			summary.Symlinks++
		}
	}
	return summary
}

func (p *PreparedPath) Send(ctx context.Context, t transport.Transport, opts SenderOptions) error {
	if p == nil || len(p.items) == 0 {
		return errors.New("prepared path is empty")
	}
	session, err := NewSenderTransferSession(ctx, t, opts.Code)
	if err != nil {
		return err
	}
	logSessionNegotiation(session, opts.Logf)
	items, err := p.manifestItems(session.DeferredFileSHA256())
	if err != nil {
		return err
	}
	if err := session.SendManifest(ctx, items); err != nil {
		return err
	}
	resumeOffsets := map[int]int64{}
	if p.hasResumableFiles() {
		resumeRequests, err := session.WaitResume(ctx, items)
		if err != nil {
			return err
		}
		resumeAccepted, acceptedOffsets, err := p.acceptResumeRequests(resumeRequests, opts.Logf)
		if err != nil {
			return err
		}
		if err := session.SendResumeAccept(ctx, resumeAccepted); err != nil {
			return err
		}
		resumeOffsets = acceptedOffsets
	}
	progress := newStreamProgressReporter("sent", items, resumeOffsets, opts.Logf)
	progress.Log("resume baseline", true)
	if err := sendPathItemsWeighted(ctx, session, items, p.files, resumeOffsets, opts, progress); err != nil {
		return err
	}
	logCompressionStats(session, opts.Logf)
	if err := session.SendDone(ctx); err != nil {
		return err
	}
	if err := session.WaitComplete(ctx); err != nil {
		return err
	}
	progress.Log("all items", true)
	return nil
}

func (p *PreparedPath) hasResumableFiles() bool {
	for _, item := range p.items {
		if item.Kind == protocol.ItemFile && item.ResumeSupported {
			return true
		}
	}
	return false
}

func (p *PreparedPath) manifestItems(deferredSHA256 bool) ([]protocol.Item, error) {
	if !deferredSHA256 {
		for _, file := range p.files {
			if _, err := p.ensureFileSHA256(file.ItemID); err != nil {
				return nil, err
			}
		}
	}
	items := append([]protocol.Item(nil), p.items...)
	if deferredSHA256 {
		for index := range items {
			if items[index].Kind == protocol.ItemFile {
				items[index].SHA256 = ""
			}
		}
	}
	return items, nil
}

func (p *PreparedPath) ensureFileSHA256(itemID int) (string, error) {
	if itemID < 0 || itemID >= len(p.items) {
		return "", fmt.Errorf("file item index out of range: %d", itemID)
	}
	if p.items[itemID].SHA256 != "" {
		return strings.ToLower(p.items[itemID].SHA256), nil
	}
	for index := range p.files {
		if p.files[index].ItemID != itemID {
			continue
		}
		hash, err := fileSHA256(p.files[index].Path)
		if err != nil {
			return "", err
		}
		p.files[index].Item.SHA256 = hash
		p.items[itemID].SHA256 = hash
		return hash, nil
	}
	return "", fmt.Errorf("file item %d has no source", itemID)
}

func (p *PreparedPath) acceptResumeRequests(requests []protocol.ResumeEntry, logf Logger) ([]protocol.ResumeEntry, map[int]int64, error) {
	accepted := make([]protocol.ResumeEntry, 0, len(requests))
	offsets := make(map[int]int64, len(requests))
	files := make(map[int]sendFileEntry, len(p.files))
	for _, file := range p.files {
		files[file.ItemID] = file
	}
	for _, request := range requests {
		if request.Item < 0 || request.Item >= len(p.items) {
			return nil, nil, fmt.Errorf("resume item index out of range: %d", request.Item)
		}
		entry, ok := files[request.Item]
		if !ok {
			return nil, nil, fmt.Errorf("resume requested for non-file item %s", p.items[request.Item].Name)
		}
		offset := clampInt64(request.Offset, 0, entry.Item.Size)
		if entry.Item.Kind != protocol.ItemFile {
			return nil, nil, fmt.Errorf("resume requested for non-file item %s", entry.Item.Name)
		}
		if request.Skip {
			if request.Complete && request.Offset != entry.Item.Size {
				return nil, nil, fmt.Errorf("completed skip for %s has invalid offset %d", entry.Item.Name, request.Offset)
			}
			offset = entry.Item.Size
			if request.Complete && logf != nil {
				logf("skipped already-complete %s", entry.Item.Name)
			}
		} else {
			sourceSHA256, err := p.ensureFileSHA256(request.Item)
			if err != nil {
				return nil, nil, err
			}
			entry.Item.SHA256 = sourceSHA256
			if offset > 0 && request.PrefixSHA256 != "" {
				sourcePrefix := sourceSHA256
				if offset != entry.Item.Size || sourcePrefix == "" {
					sourcePrefix, err = filePrefixSHA256(entry.Path, offset)
					if err != nil {
						return nil, nil, err
					}
				}
				if sourcePrefix != strings.ToLower(request.PrefixSHA256) {
					if logf != nil {
						logf("resume prefix mismatch for %s; restarting from 0", entry.Item.Name)
					}
					offset = 0
				}
			}
		}
		acceptedEntry := protocol.ResumeEntry{
			Item:     request.Item,
			Stream:   request.Stream,
			Offset:   offset,
			Skip:     request.Skip,
			Complete: request.Complete,
		}
		if !request.Skip {
			acceptedEntry.SHA256 = strings.ToLower(entry.Item.SHA256)
		}
		accepted = append(accepted, acceptedEntry)
		offsets[request.Item] = offset
	}
	return accepted, offsets, nil
}

func ParseSymlinkMode(value string) (SymlinkMode, error) {
	switch mode := SymlinkMode(strings.ToLower(strings.TrimSpace(value))); mode {
	case "", SymlinkFollow:
		return SymlinkFollow, nil
	case SymlinkPreserve:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid symlink mode %q; want follow or preserve", value)
	}
}

func collectSendItems(path string, symlinks SymlinkMode, useGitIgnore bool) ([]sendFileEntry, []protocol.Item, int, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, 0, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 && symlinks == SymlinkPreserve {
		item, err := makeSymlinkItem(path, filepath.Base(path), linkInfo)
		if err != nil {
			return nil, nil, 0, err
		}
		return nil, []protocol.Item{item}, 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, 0, err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, nil, 0, fmt.Errorf("not a regular file or directory: %s", path)
		}
		entry, err := makeSendFileEntry(path, filepath.Base(path), info)
		if err != nil {
			return nil, nil, 0, err
		}
		entry.ItemID = 0
		return []sendFileEntry{entry}, []protocol.Item{entry.Item}, 0, nil
	}

	walkRoot := path
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		walkRoot, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, nil, 0, err
		}
	}
	var gitignore *gitIgnoreMatcher
	if useGitIgnore {
		gitignore, err = loadGitIgnoreStack(walkRoot)
		if err != nil {
			return nil, nil, 0, err
		}
	}
	rootName := filepath.Base(filepath.Clean(path))
	var files []sendFileEntry
	var items []protocol.Item
	ignored := 0
	err = filepath.WalkDir(walkRoot, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(walkRoot, current)
		if err != nil {
			return err
		}
		name := rootName
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(rootName, rel))
		}
		if gitignore.Ignored(rel, d.IsDir()) {
			ignored++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		linkInfo, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			if symlinks == SymlinkPreserve {
				item, err := makeSymlinkItem(current, name, linkInfo)
				if err != nil {
					return err
				}
				items = append(items, item)
				return nil
			}
			targetInfo, err := os.Stat(current)
			if err != nil {
				return err
			}
			if targetInfo.Mode().IsRegular() {
				entry, err := makeSendFileEntry(current, name, targetInfo)
				if err != nil {
					return err
				}
				entry.ItemID = len(items)
				files = append(files, entry)
				items = append(items, entry.Item)
			}
			return nil
		}
		if d.IsDir() {
			items = append(items, makeDirectoryItem(name, linkInfo))
			return nil
		}
		if !linkInfo.Mode().IsRegular() {
			return nil
		}
		entry, err := makeSendFileEntry(current, name, linkInfo)
		if err != nil {
			return err
		}
		entry.ItemID = len(items)
		files = append(files, entry)
		items = append(items, entry.Item)
		return nil
	})
	return files, items, ignored, err
}

func makeSendFileEntry(path, name string, info os.FileInfo) (sendFileEntry, error) {
	sampleHash, err := fileSampleSHA256(path)
	if err != nil {
		return sendFileEntry{}, err
	}
	return sendFileEntry{
		Path: path,
		Item: protocol.Item{
			Kind:            protocol.ItemFile,
			Name:            filepath.ToSlash(name),
			Size:            info.Size(),
			MTime:           info.ModTime().UnixMilli(),
			Mode:            uint32(info.Mode().Perm()),
			SampleSHA256:    sampleHash,
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		},
	}, nil
}

func makeDirectoryItem(name string, info os.FileInfo) protocol.Item {
	return protocol.Item{
		Kind:      protocol.ItemDirectory,
		Name:      filepath.ToSlash(name),
		Size:      0,
		MTime:     info.ModTime().UnixMilli(),
		Mode:      uint32(info.Mode().Perm()),
		ChunkSize: protocol.ChunkSize,
	}
}

func makeSymlinkItem(path, name string, info os.FileInfo) (protocol.Item, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return protocol.Item{}, err
	}
	target = filepath.ToSlash(target)
	if err := validateSafeSymlinkTarget(name, target); err != nil {
		return protocol.Item{}, err
	}
	return protocol.Item{
		Kind:      protocol.ItemSymlink,
		Name:      filepath.ToSlash(name),
		Size:      0,
		MTime:     info.ModTime().UnixMilli(),
		Mode:      uint32(info.Mode().Perm()),
		Target:    target,
		ChunkSize: protocol.ChunkSize,
	}, nil
}

type fileSendState struct {
	index    int
	streamID int
	entry    sendFileEntry
	file     *os.File
	offset   int64
	done     bool
	ended    bool
}

func sendPathItemsWeighted(ctx context.Context, session *TransferSession, items []protocol.Item, files []sendFileEntry, resumeOffsets map[int]int64, opts SenderOptions, progress *progressReporter) error {
	states := make([]*fileSendState, 0, len(files))
	stateByStream := make(map[int]*fileSendState, len(files))
	weightedStreams := make([]mux.WeightedStream, 0, len(files))
	for _, entry := range files {
		resumeOffset := clampInt64(resumeOffsets[entry.ItemID], 0, entry.Item.Size)
		streamID, err := session.StreamIDForItem(entry.ItemID)
		if err != nil {
			closeFileSendStates(states)
			return err
		}
		state := &fileSendState{index: entry.ItemID, streamID: streamID, entry: entry, offset: resumeOffset, done: resumeOffset >= entry.Item.Size}
		if state.done {
			states = append(states, state)
			continue
		}
		file, err := os.Open(entry.Path)
		if err != nil {
			closeFileSendStates(states)
			return err
		}
		state.file = file
		if resumeOffset > 0 {
			if _, err := file.Seek(resumeOffset, io.SeekStart); err != nil {
				closeFileSendStates(append(states, state))
				return err
			}
			if opts.Logf != nil {
				opts.Logf("resuming %s from %d/%d bytes", entry.Item.Name, resumeOffset, entry.Item.Size)
			}
		}
		states = append(states, state)
		stateByStream[streamID] = state
		weightedStreams = append(weightedStreams, mux.WeightedStream{
			ID:     streamID,
			Weight: fileStreamWeight(entry.Item.Size - resumeOffset),
		})
	}
	defer closeFileSendStates(states)

	fileItems := make(map[int]bool, len(files))
	for _, entry := range files {
		fileItems[entry.ItemID] = true
	}
	for itemID := range items {
		if fileItems[itemID] {
			continue
		}
		if err := session.OpenStream(ctx, itemID); err != nil {
			return err
		}
		if err := session.EndStream(ctx, itemID); err != nil {
			return err
		}
	}
	for _, state := range states {
		if err := session.OpenStream(ctx, state.index); err != nil {
			return err
		}
		if state.done {
			if err := sendFileStreamEnd(ctx, session, state); err != nil {
				return err
			}
		}
	}

	scheduler, err := mux.NewWeightedScheduler(weightedStreams, protocol.ChunkSize)
	if err != nil {
		return err
	}
	var chunkSender chunkMessageSender = session
	var parallelSender *parallelChunkSender
	if session.StripesChunks() {
		parallelSender = newParallelChunkSender(ctx, session)
		defer parallelSender.Abort()
		chunkSender = parallelSender
	}
	buf := make([]byte, protocol.ChunkSize)
	for {
		maxChunk := transport.AdaptiveSendBudget(protocol.ChunkSize, minimumAdaptiveChunkSize, session.SendMetrics())
		turn, err := scheduler.Next(maxChunk)
		if err != nil {
			return err
		}
		if !turn.OK {
			break
		}
		state := stateByStream[turn.StreamID]
		if state == nil {
			return fmt.Errorf("scheduler returned unknown stream %d", turn.StreamID)
		}
		used, done, err := sendNextFileChunk(ctx, chunkSender, state, buf[:turn.Budget], progress)
		if err != nil {
			return err
		}
		if err := scheduler.Commit(turn.StreamID, used, done); err != nil {
			return err
		}
	}
	if parallelSender != nil {
		if err := parallelSender.Close(); err != nil {
			return err
		}
		stats := parallelSender.Stats()
		session.RecordPhysicalPathStats(physicalPathStats(stats))
		logChunkPathStats(stats, opts.Logf)
	}
	for _, state := range states {
		if err := sendFileStreamEnd(ctx, session, state); err != nil {
			return err
		}
	}
	return nil
}

func sendNextFileChunk(ctx context.Context, sender chunkMessageSender, state *fileSendState, buf []byte, progress *progressReporter) (int, bool, error) {
	n, readErr := state.file.Read(buf)
	if n > 0 {
		if err := sender.SendChunk(ctx, state.index, state.offset, buf[:n]); err != nil {
			return 0, false, err
		}
		state.offset += int64(n)
		progress.AddStream(state.index, int64(n))
	}
	if errors.Is(readErr, io.EOF) {
		state.done = true
		return n, true, nil
	}
	if readErr != nil {
		return n, false, readErr
	}
	if state.offset >= state.entry.Item.Size {
		state.done = true
		return n, true, nil
	}
	if n == 0 {
		return 0, false, io.ErrNoProgress
	}
	return n, false, nil
}

func logChunkPathStats(stats []chunkPathStats, logf Logger) {
	if logf == nil {
		return
	}
	for _, stat := range stats {
		if stat.Chunks == 0 {
			continue
		}
		rate := float64(stat.Bytes)
		if stat.SendTime > 0 {
			rate /= stat.SendTime.Seconds()
		}
		logf(
			"path %d sent %s in %d chunk(s), transport write %s/s",
			stat.Connection,
			formatBytes(stat.Bytes),
			stat.Chunks,
			formatBytesFloat(rate),
		)
	}
}

func sendFileStreamEnd(ctx context.Context, session *TransferSession, state *fileSendState) error {
	if state.ended {
		return nil
	}
	state.ended = true
	return session.EndStream(ctx, state.index)
}

func closeFileSendStates(states []*fileSendState) {
	for _, state := range states {
		if state != nil && state.file != nil {
			_ = state.file.Close()
		}
	}
}

func fileStreamWeight(remaining int64) int {
	switch {
	case remaining <= 1<<20:
		return 4
	case remaining <= 16<<20:
		return 2
	default:
		return 1
	}
}

func SendText(ctx context.Context, t transport.Transport, text string, opts SenderOptions) error {
	textBytes := []byte(text)
	item := protocol.Item{
		Kind:            protocol.ItemText,
		Name:            "message.txt",
		Size:            int64(len(textBytes)),
		SHA256:          bytesSHA256(textBytes),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: false,
	}
	session, err := NewSenderTransferSession(ctx, t, opts.Code)
	if err != nil {
		return err
	}
	logSessionNegotiation(session, opts.Logf)
	progress := newStreamProgressReporter("sent", []protocol.Item{item}, nil, opts.Logf)
	if err := session.SendManifest(ctx, []protocol.Item{item}); err != nil {
		return err
	}
	if err := session.OpenStream(ctx, 0); err != nil {
		return err
	}
	if err := session.SendChunk(ctx, 0, 0, textBytes); err != nil {
		return err
	}
	logCompressionStats(session, opts.Logf)
	progress.AddStream(0, int64(len(textBytes)))
	if err := session.EndStream(ctx, 0); err != nil {
		return err
	}
	if err := session.SendDone(ctx); err != nil {
		return err
	}
	if opts.Logf != nil {
		opts.Logf("sent done")
	}
	if err := session.WaitComplete(ctx); err != nil {
		return err
	}
	progress.Log("all items", true)
	return nil
}

func Receive(ctx context.Context, t transport.Transport, opts ReceiverOptions) ([]ReceivedText, error) {
	session, err := NewReceiverTransferSession(ctx, t, opts.Code)
	if err != nil {
		return nil, err
	}
	logSessionNegotiation(session, opts.Logf)
	var manifest *protocol.Manifest
	var store *ReceiveStore
	var texts []ReceivedText
	var progress *progressReporter
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	for {
		event, err := session.ReceiveEvent(ctx)
		if err != nil {
			return texts, err
		}
		switch event.Kind {
		case EventManifest:
			manifest = event.Manifest
			if opts.Logf != nil {
				opts.Logf("receiving %d item(s)", len(manifest.Items))
			}
			store, err = NewReceiveStoreWithOptions(manifest, ReceiveStoreOptions{
				OutputDir:  opts.OutputDir,
				Logf:       opts.Logf,
				Conflict:   opts.Conflict,
				OutOfOrder: session.StripesChunks(),
			})
			if err != nil {
				return texts, err
			}
			resume := store.ResumeEntries()
			progress, err = newManifestProgressReporter("received", manifest, resumeEntriesToOffsets(resume), opts.Logf)
			if err != nil {
				return texts, err
			}
			progress.Log("resume baseline", true)
			if len(resume) > 0 {
				if err := session.SendResume(ctx, resume); err != nil {
					return texts, err
				}
				accepted, err := session.WaitResumeAccept(ctx, manifest.Items)
				if err != nil {
					return texts, err
				}
				if session.DeferredFileSHA256() {
					for _, entry := range accepted {
						item := manifest.Items[entry.Item]
						if item.Kind == protocol.ItemFile && !entry.Skip && entry.SHA256 == "" {
							return texts, fmt.Errorf("resume_accept missing deferred sha256 for %s", item.Name)
						}
					}
				}
				if err := store.ApplyResumeAccept(accepted); err != nil {
					return texts, err
				}
				progress, err = newManifestProgressReporter("received", manifest, resumeEntriesToOffsets(accepted), opts.Logf)
				if err != nil {
					return texts, err
				}
				progress.Log("accepted resume", true)
			}
		case EventStreamOpen, EventStreamEnd:
		case EventChunk:
			if store == nil {
				return texts, errors.New("chunk arrived before receive store initialization")
			}
			if err := store.WriteChunk(event.ItemID, event.Offset, event.Data); err != nil {
				return texts, err
			}
			progress.AddStream(event.StreamID, int64(len(event.Data)))
		case EventDone:
			progress.Log("all items", true)
			if store == nil {
				return texts, errors.New("done arrived before receive store initialization")
			}
			texts, err = store.Finalize()
			if err != nil {
				return texts, err
			}
			if err := session.SendComplete(ctx); err != nil {
				return texts, err
			}
			return texts, nil
		case EventError:
			return texts, errors.New(event.Error)
		default:
			return texts, fmt.Errorf("unexpected transfer event %q", event.Kind)
		}
	}
}

func logCompressionStats(session *TransferSession, logf Logger) {
	if session == nil || logf == nil {
		return
	}
	stats := session.CompressionStats()
	if stats.CompressedChunks == 0 || stats.OriginalBytes == 0 {
		return
	}
	saved := stats.OriginalBytes - stats.WireBytes
	percent := float64(saved) * 100 / float64(stats.OriginalBytes)
	logf(
		"compressed %s to %s (%.0f%% saved, %d chunk(s))",
		formatBytes(stats.OriginalBytes),
		formatBytes(stats.WireBytes),
		percent,
		stats.CompressedChunks,
	)
}

func logSessionNegotiation(session *TransferSession, logf Logger) {
	if session == nil || logf == nil {
		return
	}
	logf("secure session established")
	logf("transfer connections: %d", session.ConnectionCount())
	if session.StripesChunks() {
		logf("chunk striping enabled")
	}
	if session.Compression() != "" {
		logf("compression %s negotiated", session.Compression())
	}
}

type progressReporter struct {
	label       string
	total       int64
	done        int64
	transferred int64
	started     time.Time
	lastLog     time.Time
	logf        Logger
	streams     map[int]*progressStream
}

type progressStream struct {
	name string
	size int64
	done int64
}

func newProgressReporter(label string, total, initial int64, logf Logger) *progressReporter {
	now := time.Now()
	return &progressReporter{
		label:   label,
		total:   maxInt64(total, 0),
		done:    clampInt64(initial, 0, maxInt64(total, 0)),
		started: now,
		lastLog: now,
		logf:    logf,
	}
}

func newStreamProgressReporter(label string, items []protocol.Item, initial map[int]int64, logf Logger) *progressReporter {
	p := newProgressReporter(label, totalItemSize(items), 0, logf)
	p.streams = map[int]*progressStream{}
	var done int64
	for i, item := range items {
		size := maxInt64(item.Size, 0)
		streamDone := clampInt64(initial[i], 0, size)
		p.streams[i] = &progressStream{name: item.Name, size: size, done: streamDone}
		done += streamDone
	}
	p.done = clampInt64(done, 0, p.total)
	return p
}

func newManifestProgressReporter(label string, manifest *protocol.Manifest, initial map[int]int64, logf Logger) (*progressReporter, error) {
	plan, err := mux.PlanFromManifest(manifest)
	if err != nil {
		return nil, err
	}
	p := newProgressReporter(label, totalItemSize(manifest.Items), 0, logf)
	p.streams = map[int]*progressStream{}
	var done int64
	for itemID, item := range manifest.Items {
		streamID, ok := plan.StreamForItem(itemID)
		if !ok {
			return nil, fmt.Errorf("manifest item %d has no stream binding", itemID)
		}
		size := maxInt64(item.Size, 0)
		streamDone := clampInt64(initial[itemID], 0, size)
		p.streams[streamID] = &progressStream{name: item.Name, size: size, done: streamDone}
		done += streamDone
	}
	p.done = clampInt64(done, 0, p.total)
	return p, nil
}

func (p *progressReporter) Add(delta int64, item string) {
	if p == nil {
		return
	}
	before := p.done
	p.done = clampInt64(p.done+delta, 0, p.total)
	if p.done > before {
		p.transferred += p.done - before
	}
	p.Log(item, false)
}

func (p *progressReporter) AddStream(streamID int, delta int64) {
	if p == nil {
		return
	}
	stream, ok := p.streams[streamID]
	if !ok {
		p.Add(delta, "")
		return
	}
	before := stream.done
	stream.done = clampInt64(stream.done+delta, 0, stream.size)
	advanced := stream.done - before
	p.done = clampInt64(p.done+advanced, 0, p.total)
	if advanced > 0 {
		p.transferred += advanced
	}
	p.Log(stream.name, false)
}

func (p *progressReporter) Log(item string, force bool) {
	if p == nil || p.logf == nil {
		return
	}
	now := time.Now()
	if !force {
		if p.done >= p.total || now.Sub(p.lastLog) < progressLogInterval {
			return
		}
	}
	p.lastLog = now
	percent := 100
	if p.total > 0 {
		percent = int((p.done * 100) / p.total)
	}
	elapsed := now.Sub(p.started).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	rate := float64(p.transferred) / elapsed
	if item == "" {
		p.logf("%s %s/%s %d%% %s/s", p.label, formatBytes(p.done), formatBytes(p.total), percent, formatBytesFloat(rate))
		return
	}
	p.logf("%s %s %s/%s %d%% %s/s", p.label, item, formatBytes(p.done), formatBytes(p.total), percent, formatBytesFloat(rate))
}

func totalItemSize(items []protocol.Item) int64 {
	var total int64
	for _, item := range items {
		if item.Size > 0 {
			total += item.Size
		}
	}
	return total
}

func totalResumeOffset(items []protocol.Item, offsets map[int]int64) int64 {
	var total int64
	for i, offset := range offsets {
		if i < 0 || i >= len(items) {
			continue
		}
		total += clampInt64(offset, 0, items[i].Size)
	}
	return total
}

func totalResumeEntries(entries []protocol.ResumeEntry) int64 {
	var total int64
	for _, entry := range entries {
		if entry.Offset > 0 {
			total += entry.Offset
		}
	}
	return total
}

func resumeEntriesToOffsets(entries []protocol.ResumeEntry) map[int]int64 {
	offsets := map[int]int64{}
	for _, entry := range entries {
		offsets[entry.Item] = entry.Offset
	}
	return offsets
}

func formatBytes(n int64) string {
	return formatBytesFloat(float64(maxInt64(n, 0)))
}

func formatBytesFloat(value float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	switch {
	case unit == 0 || value >= 100:
		return fmt.Sprintf("%.0f %s", value, units[unit])
	case value >= 10:
		return fmt.Sprintf("%.1f %s", value, units[unit])
	default:
		return fmt.Sprintf("%.2f %s", value, units[unit])
	}
}

func clampInt64(value, minValue, maxValue int64) int64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func filePrefixSHA256(path string, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("prefix size cannot be negative")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, file, size); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func bytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ptr[T any](v T) *T {
	return &v
}
