package transfer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suir1/kigo/internal/mux"
	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
)

type helloMessage struct {
	Type          string   `json:"type"`
	Version       int      `json:"version"`
	SenderNonce   string   `json:"sender_nonce,omitempty"`
	ReceiverNonce string   `json:"receiver_nonce,omitempty"`
	Compressions  []string `json:"compressions,omitempty"`
	Compression   string   `json:"compression,omitempty"`
	Connections   int      `json:"connections,omitempty"`
	Features      []string `json:"features,omitempty"`
}

const (
	featureChunkStriping      = "chunk-striping"
	featureDeferredFileSHA256 = "deferred-file-sha256"
)

type envelope struct {
	Version int    `json:"version"`
	Seq     uint64 `json:"seq"`
	Body    string `json:"body"`
}

type securePipe struct {
	t                  transport.Transport
	index              int
	sendSession        *secure.Session
	recvSession        *secure.Session
	nextSeq            uint64
	recvSeq            uint64
	compression        string
	striping           bool
	deferredFileSHA256 bool
}

type TransferSession struct {
	transport          transport.Transport
	pipe               *securePipe
	pipes              []*securePipe
	sendManifest       *protocol.Manifest
	sendPlan           *mux.Plan
	receiveManifest    *protocol.Manifest
	receivePlan        *mux.Plan
	receiveStreams     *mux.Tracker
	compressionState   map[int]*chunkCompressionState
	compressionStats   CompressionStats
	receiveMessages    chan receivedPipeMessage
	deferredMessages   []receivedPipeMessage
	pendingDone        bool
	receiveEnded       int
	receiveCoverage    map[int]*byteRanges
	sendChunkCursor    map[int]int
	striping           bool
	deferredFileSHA256 bool
	pathWeights        []float64
}

type receivedPipeMessage struct {
	pipeIndex int
	message   protocol.Message
	err       error
}

type EventKind string

const (
	EventManifest     EventKind = "manifest"
	EventResume       EventKind = "resume"
	EventResumeAccept EventKind = "resume_accept"
	EventStreamOpen   EventKind = "stream_open"
	EventChunk        EventKind = "chunk"
	EventStreamEnd    EventKind = "stream_end"
	EventDone         EventKind = "done"
	EventComplete     EventKind = "complete"
	EventError        EventKind = "error"
)

type TransferEvent struct {
	Kind     EventKind
	Manifest *protocol.Manifest
	Resume   []protocol.ResumeEntry
	StreamID int
	ItemID   int
	Offset   int64
	Data     []byte
	Error    string
	At       int64
}

func NewSenderTransferSession(ctx context.Context, t transport.Transport, code string) (*TransferSession, error) {
	pipes, err := initSenderPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	session := newTransferSessionWithPipes(pipes, false)
	session.transport = t
	session.pathWeights = transport.SnapshotPhysicalPathWeights(t)
	return session, nil
}

func NewReceiverTransferSession(ctx context.Context, t transport.Transport, code string) (*TransferSession, error) {
	pipes, err := initReceiverPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	session := newTransferSessionWithPipes(pipes, true)
	session.transport = t
	return session, nil
}

func newTransferSessionWithPipes(pipes []*securePipe, receiveAll bool) *TransferSession {
	session := &TransferSession{
		pipes:            pipes,
		compressionState: map[int]*chunkCompressionState{},
		sendChunkCursor:  map[int]int{},
	}
	if len(pipes) > 0 {
		session.pipe = pipes[0]
		session.striping = pipes[0].striping
		session.deferredFileSHA256 = pipes[0].deferredFileSHA256
	}
	if receiveAll {
		session.receiveMessages = make(chan receivedPipeMessage, max(1, len(pipes)*2))
		for _, pipe := range pipes {
			go session.receivePipe(pipe)
		}
	}
	return session
}

func initSender(ctx context.Context, t transport.Transport, code string) (*securePipe, error) {
	pipes, err := initSenderPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	return pipes[0], nil
}

func initSenderPipes(ctx context.Context, t transport.Transport, code string) ([]*securePipe, error) {
	channels := transport.Channels(t)
	if len(channels) == 0 {
		return nil, transport.ErrClosed
	}
	senderNonce, err := secure.RandomNonce()
	if err != nil {
		return nil, err
	}
	hello := helloMessage{
		Type:         "hello",
		Version:      protocol.Version,
		SenderNonce:  senderNonce,
		Compressions: []string{compressionGzip},
		Connections:  len(channels),
		Features:     []string{featureChunkStriping, featureDeferredFileSHA256},
	}
	if err := sendPlain(ctx, channels[0], hello); err != nil {
		return nil, err
	}
	var ack helloMessage
	if err := recvPlain(ctx, channels[0], &ack); err != nil {
		return nil, err
	}
	if err := validateHello(ack, "hello_ack"); err != nil {
		return nil, err
	}
	if ack.Compression != "" && !containsString(hello.Compressions, ack.Compression) {
		return nil, fmt.Errorf("receiver selected unsupported compression %q", ack.Compression)
	}
	for _, feature := range ack.Features {
		if !containsString(hello.Features, feature) {
			return nil, fmt.Errorf("receiver selected unsupported feature %q", feature)
		}
	}
	connectionCount := normalizedConnectionCount(ack.Connections)
	if connectionCount > len(channels) {
		return nil, fmt.Errorf("receiver selected %d connections, sender has %d", connectionCount, len(channels))
	}
	striping := connectionCount > 1 && containsString(ack.Features, featureChunkStriping)
	deferredFileSHA256 := containsString(ack.Features, featureDeferredFileSHA256)
	closeUnusedChannels(channels, connectionCount)
	pipes := make([]*securePipe, connectionCount)
	for index := range connectionCount {
		pipe, err := newSecurePipe(channels[index], index, code, senderNonce, ack.ReceiverNonce, true, ack.Compression)
		if err != nil {
			return nil, err
		}
		pipes[index] = pipe
		pipe.striping = striping
		pipe.deferredFileSHA256 = deferredFileSHA256
	}
	return pipes, nil
}

func initReceiver(ctx context.Context, t transport.Transport, code string) (*securePipe, error) {
	pipes, err := initReceiverPipes(ctx, t, code)
	if err != nil {
		return nil, err
	}
	return pipes[0], nil
}

func initReceiverPipes(ctx context.Context, t transport.Transport, code string) ([]*securePipe, error) {
	channels := transport.Channels(t)
	if len(channels) == 0 {
		return nil, transport.ErrClosed
	}
	var hello helloMessage
	if err := recvPlain(ctx, channels[0], &hello); err != nil {
		return nil, err
	}
	if err := validateHello(hello, "hello"); err != nil {
		return nil, err
	}
	receiverNonce, err := secure.RandomNonce()
	if err != nil {
		return nil, err
	}
	compression := selectCompression(hello.Compressions)
	connectionCount := min(normalizedConnectionCount(hello.Connections), len(channels))
	features := selectFeatures(hello.Features, connectionCount)
	if err := sendPlain(ctx, channels[0], helloMessage{
		Type:          "hello_ack",
		Version:       protocol.Version,
		ReceiverNonce: receiverNonce,
		Compression:   compression,
		Connections:   connectionCount,
		Features:      features,
	}); err != nil {
		return nil, err
	}
	closeUnusedChannels(channels, connectionCount)
	pipes := make([]*securePipe, connectionCount)
	for index := range connectionCount {
		pipe, err := newSecurePipe(channels[index], index, code, hello.SenderNonce, receiverNonce, false, compression)
		if err != nil {
			return nil, err
		}
		pipes[index] = pipe
		pipe.striping = containsString(features, featureChunkStriping)
		pipe.deferredFileSHA256 = containsString(features, featureDeferredFileSHA256)
	}
	return pipes, nil
}

func newSecurePipe(t transport.Transport, index int, code, senderNonce, receiverNonce string, sender bool, compression string) (*securePipe, error) {
	sendDirection := "receiver-to-sender"
	recvDirection := "sender-to-receiver"
	if sender {
		sendDirection, recvDirection = recvDirection, sendDirection
	}
	sendSession, err := secure.NewSessionWithInfo(code, senderNonce, receiverNonce, channelKeyInfo(sendDirection, index))
	if err != nil {
		return nil, err
	}
	recvSession, err := secure.NewSessionWithInfo(code, senderNonce, receiverNonce, channelKeyInfo(recvDirection, index))
	if err != nil {
		return nil, err
	}
	return &securePipe{
		t:           t,
		index:       index,
		sendSession: sendSession,
		recvSession: recvSession,
		compression: compression,
	}, nil
}

func channelKeyInfo(direction string, index int) string {
	if index == 0 {
		return "kigo-v1 " + direction + " aes-128-gcm"
	}
	return fmt.Sprintf("kigo-v1 %s channel-%d aes-128-gcm", direction, index)
}

func normalizedConnectionCount(count int) int {
	if count <= 0 {
		return 1
	}
	return count
}

func closeUnusedChannels(channels []transport.Transport, used int) {
	for _, channel := range channels[used:] {
		_ = channel.Close()
	}
}

func validateHello(msg helloMessage, wantType string) error {
	if msg.Type != wantType {
		return fmt.Errorf("invalid hello type: got %q want %q", msg.Type, wantType)
	}
	if msg.Version != protocol.Version {
		return fmt.Errorf("unsupported hello version %d", msg.Version)
	}
	switch wantType {
	case "hello":
		if msg.SenderNonce == "" {
			return errors.New("invalid sender hello nonce")
		}
	case "hello_ack":
		if msg.ReceiverNonce == "" {
			return errors.New("invalid receiver hello ack nonce")
		}
		if msg.Compression != "" && msg.Compression != compressionGzip {
			return fmt.Errorf("unsupported hello compression %q", msg.Compression)
		}
	}
	return nil
}

func selectCompression(offered []string) string {
	if containsString(offered, compressionGzip) {
		return compressionGzip
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func selectFeatures(offered []string, connectionCount int) []string {
	var selected []string
	if connectionCount > 1 && containsString(offered, featureChunkStriping) {
		selected = append(selected, featureChunkStriping)
	}
	if containsString(offered, featureDeferredFileSHA256) {
		selected = append(selected, featureDeferredFileSHA256)
	}
	return selected
}

func sendPlain(ctx context.Context, t transport.Transport, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return t.Send(ctx, payload)
}

func recvPlain(ctx context.Context, t transport.Transport, out any) error {
	payload, err := t.Recv(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}

func (s *TransferSession) SendManifest(ctx context.Context, items []protocol.Item) error {
	manifest := protocol.NewManifest(items)
	plan := mux.NewPlan(len(items))
	plan.Apply(&manifest)
	s.sendManifest = &manifest
	s.sendPlan = plan
	return s.pipe.sendMessage(ctx, protocol.Message{
		Type:     "manifest",
		Version:  protocol.Version,
		Manifest: &manifest,
	})
}

func (s *TransferSession) WaitResume(ctx context.Context, items []protocol.Item) ([]protocol.ResumeEntry, error) {
	event, err := s.ReceiveEvent(ctx)
	if err != nil {
		return nil, err
	}
	if event.Kind == EventError {
		return nil, errors.New(event.Error)
	}
	if event.Kind != EventResume {
		return nil, fmt.Errorf("expected resume, got %q", event.Kind)
	}
	entries := make([]protocol.ResumeEntry, 0, len(event.Resume))
	seen := make(map[int]struct{}, len(event.Resume))
	for _, entry := range event.Resume {
		if s.sendPlan == nil {
			return nil, errors.New("resume arrived before send manifest")
		}
		offset, err := s.sendPlan.ValidateResumeEntry(items, entry)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[entry.Item]; ok {
			return nil, fmt.Errorf("duplicate resume entry for item %d", entry.Item)
		}
		seen[entry.Item] = struct{}{}
		entry.Offset = offset
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *TransferSession) SendResume(ctx context.Context, entries []protocol.ResumeEntry) error {
	return s.pipe.sendMessage(ctx, protocol.Message{
		Type:   "resume",
		Resume: entries,
		At:     protocol.NowMillis(),
	})
}

func (s *TransferSession) SendResumeAccept(ctx context.Context, entries []protocol.ResumeEntry) error {
	return s.pipe.sendMessage(ctx, protocol.Message{
		Type:   "resume_accept",
		Resume: entries,
		At:     protocol.NowMillis(),
	})
}

func (s *TransferSession) WaitResumeAccept(ctx context.Context, items []protocol.Item) ([]protocol.ResumeEntry, error) {
	received, err := s.receiveControlMessage(ctx)
	if err != nil {
		return nil, err
	}
	if received.message.Type == "error" {
		return nil, errors.New(received.message.Error)
	}
	if received.message.Type != "resume_accept" {
		return nil, fmt.Errorf("expected resume_accept, got %q", received.message.Type)
	}
	entries := make([]protocol.ResumeEntry, 0, len(received.message.Resume))
	seen := make(map[int]struct{}, len(received.message.Resume))
	for _, entry := range received.message.Resume {
		if s.receivePlan == nil {
			return nil, errors.New("resume_accept arrived before receive manifest")
		}
		offset, err := s.receivePlan.ValidateResumeEntry(items, entry)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[entry.Item]; ok {
			return nil, fmt.Errorf("duplicate resume_accept entry for item %d", entry.Item)
		}
		seen[entry.Item] = struct{}{}
		entry.Offset = offset
		entries = append(entries, entry)
	}
	s.applyReceiveResume(entries)
	return entries, nil
}

func (s *TransferSession) OpenStream(ctx context.Context, itemID int) error {
	streamID, err := s.sendStreamForItem(itemID)
	if err != nil {
		return err
	}
	return s.pipeForItem(itemID).sendMessage(ctx, protocol.Message{
		Type:   "stream_open",
		Item:   itemID,
		Stream: ptr(streamID),
		At:     protocol.NowMillis(),
	})
}

func (s *TransferSession) SendChunk(ctx context.Context, itemID int, offset int64, data []byte) error {
	message, err := s.buildChunkMessage(itemID, offset, data)
	if err != nil {
		return err
	}
	return s.chunkPipeForItem(itemID).sendMessage(ctx, message)
}

func (s *TransferSession) buildChunkMessage(itemID int, offset int64, data []byte) (protocol.Message, error) {
	streamID, err := s.sendStreamForItem(itemID)
	if err != nil {
		return protocol.Message{}, err
	}
	payload, encoding, err := s.encodeChunk(itemID, data)
	if err != nil {
		return protocol.Message{}, err
	}
	return protocol.Message{
		Type:     "chunk",
		Item:     itemID,
		Stream:   ptr(streamID),
		Offset:   offset,
		Data:     base64.StdEncoding.EncodeToString(payload),
		Encoding: encoding,
		At:       protocol.NowMillis(),
	}, nil
}

func (s *TransferSession) encodeChunk(itemID int, data []byte) ([]byte, string, error) {
	s.compressionStats.OriginalBytes += int64(len(data))
	if s.pipe == nil || s.pipe.compression != compressionGzip {
		s.compressionStats.WireBytes += int64(len(data))
		return data, "", nil
	}
	state := s.compressionState[itemID]
	if state == nil {
		state = &chunkCompressionState{}
		s.compressionState[itemID] = state
	}
	if state.disabled {
		s.compressionStats.WireBytes += int64(len(data))
		return data, "", nil
	}
	encoded, err := compressGzipChunk(data)
	if err != nil {
		return nil, "", err
	}
	state.attempts++
	if compressionWorthwhile(len(data), len(encoded)) {
		s.compressionStats.WireBytes += int64(len(encoded))
		s.compressionStats.CompressedChunks++
		return encoded, compressionGzip, nil
	}
	state.misses++
	if state.attempts >= 3 && state.misses == state.attempts {
		state.disabled = true
	}
	s.compressionStats.WireBytes += int64(len(data))
	return data, "", nil
}

func (s *TransferSession) Compression() string {
	if s == nil || s.pipe == nil {
		return ""
	}
	return s.pipe.compression
}

func (s *TransferSession) CompressionStats() CompressionStats {
	if s == nil {
		return CompressionStats{}
	}
	return s.compressionStats
}

func (s *TransferSession) EndStream(ctx context.Context, itemID int) error {
	streamID, err := s.sendStreamForItem(itemID)
	if err != nil {
		return err
	}
	return s.pipeForItem(itemID).sendMessage(ctx, protocol.Message{
		Type:   "stream_end",
		Item:   itemID,
		Stream: ptr(streamID),
		At:     protocol.NowMillis(),
	})
}

func (s *TransferSession) StreamIDForItem(itemID int) (int, error) {
	return s.sendStreamForItem(itemID)
}

func (s *TransferSession) SendMetrics() transport.SendMetrics {
	if s == nil || len(s.pipes) == 0 {
		return transport.SendMetrics{}
	}
	var metrics transport.SendMetrics
	for _, pipe := range s.pipes {
		current := transport.SnapshotSendMetrics(pipe.t)
		metrics.BufferedBytes += current.BufferedBytes
		metrics.BufferLimit += current.BufferLimit
		if current.LastWait > metrics.LastWait {
			metrics.LastWait = current.LastWait
		}
	}
	return metrics
}

func (s *TransferSession) ConnectionCount() int {
	if s == nil {
		return 0
	}
	return len(s.pipes)
}

func (s *TransferSession) StripesChunks() bool {
	return s != nil && s.striping
}

func (s *TransferSession) DeferredFileSHA256() bool {
	return s != nil && s.deferredFileSHA256
}

func (s *TransferSession) PhysicalPathWeight(connection int) float64 {
	if s == nil || connection < 0 || connection >= len(s.pathWeights) || s.pathWeights[connection] <= 0 {
		return 1
	}
	return s.pathWeights[connection]
}

func (s *TransferSession) RecordPhysicalPathStats(stats []transport.PhysicalPathStats) {
	if s == nil || s.transport == nil {
		return
	}
	transport.RecordPhysicalPathStats(s.transport, stats)
}

func (s *TransferSession) pipeForItem(itemID int) *securePipe {
	if len(s.pipes) <= 1 {
		return s.pipe
	}
	return s.pipes[1+itemID%(len(s.pipes)-1)]
}

func (s *TransferSession) chunkPipeForItem(itemID int) *securePipe {
	if !s.striping || len(s.pipes) <= 1 {
		return s.pipeForItem(itemID)
	}
	cursor, ok := s.sendChunkCursor[itemID]
	if !ok {
		cursor = itemID
	}
	pipe := s.pipes[1+cursor%(len(s.pipes)-1)]
	s.sendChunkCursor[itemID] = cursor + 1
	return pipe
}

func (s *TransferSession) sendStreamForItem(itemID int) (int, error) {
	if s == nil || s.sendManifest == nil || s.sendPlan == nil {
		return 0, errors.New("stream operation attempted before send manifest")
	}
	if itemID < 0 || itemID >= len(s.sendManifest.Items) {
		return 0, fmt.Errorf("stream item index out of range: %d", itemID)
	}
	streamID, ok := s.sendPlan.StreamForItem(itemID)
	if !ok {
		return 0, fmt.Errorf("manifest item %d has no stream binding", itemID)
	}
	return streamID, nil
}

func (s *TransferSession) SendDone(ctx context.Context) error {
	return s.pipe.sendMessage(ctx, protocol.Message{Type: "done", At: protocol.NowMillis()})
}

func (s *TransferSession) WaitComplete(ctx context.Context) error {
	event, err := s.ReceiveEvent(ctx)
	if err != nil {
		return err
	}
	if event.Kind == EventError {
		return errors.New(event.Error)
	}
	if event.Kind != EventComplete {
		return fmt.Errorf("expected complete, got %q", event.Kind)
	}
	return nil
}

func (s *TransferSession) SendComplete(ctx context.Context) error {
	return s.pipe.sendMessage(ctx, protocol.Message{Type: "complete", At: protocol.NowMillis()})
}

func (s *TransferSession) Receive(ctx context.Context) (protocol.Message, error) {
	received, err := s.receiveMessage(ctx)
	return received.message, err
}

func (s *TransferSession) ReceiveEvent(ctx context.Context) (TransferEvent, error) {
	for {
		if s.pendingDone && s.receiveReady() {
			s.pendingDone = false
			return TransferEvent{Kind: EventDone, At: protocol.NowMillis()}, nil
		}
		received, err := s.receiveMessage(ctx)
		if err != nil {
			return TransferEvent{}, err
		}
		msg := received.message
		if s.receiveManifest == nil && received.pipeIndex != 0 {
			s.deferredMessages = append(s.deferredMessages, received)
			continue
		}
		if err := s.validateMessageChannel(received.pipeIndex, msg); err != nil {
			return TransferEvent{}, err
		}
		if msg.Type == "done" && s.receiveManifest != nil && !s.receiveReady() {
			s.pendingDone = true
			continue
		}
		event := TransferEvent{Kind: EventKind(msg.Type), ItemID: msg.Item, StreamID: msg.Item, Offset: msg.Offset, At: msg.At}
		switch msg.Type {
		case "manifest":
			if msg.Manifest == nil {
				return TransferEvent{}, errors.New("manifest message missing manifest")
			}
			if s.receiveManifest != nil {
				return TransferEvent{}, errors.New("manifest received more than once")
			}
			plan, err := mux.PlanFromManifest(msg.Manifest)
			if err != nil {
				return TransferEvent{}, err
			}
			s.receiveManifest = msg.Manifest
			s.receivePlan = plan
			s.receiveStreams = mux.NewTracker(plan)
			s.receiveCoverage = make(map[int]*byteRanges, len(msg.Manifest.Items))
			for itemID := range msg.Manifest.Items {
				s.receiveCoverage[itemID] = newByteRanges(0)
			}
			event.Manifest = msg.Manifest
		case "resume":
			event.Resume = msg.Resume
		case "resume_accept":
			event.Resume = msg.Resume
		case "stream_open":
			if s.receiveManifest == nil {
				return TransferEvent{}, errors.New("stream_open arrived before manifest")
			}
			binding, err := s.receiveStreams.AcceptOpen(s.receiveManifest, msg)
			if err != nil {
				return TransferEvent{}, err
			}
			event.StreamID = binding.ID
			event.ItemID = binding.Item
		case "chunk":
			if s.receiveManifest == nil {
				return TransferEvent{}, errors.New("chunk arrived before manifest")
			}
			var binding protocol.StreamBinding
			if s.striping {
				binding, err = s.receiveStreams.AcceptChunkAfterEnd(s.receiveManifest, msg)
			} else {
				binding, err = s.receiveStreams.AcceptChunk(s.receiveManifest, msg)
			}
			if err != nil {
				return TransferEvent{}, err
			}
			event.StreamID = binding.ID
			event.ItemID = binding.Item
			data, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				return TransferEvent{}, err
			}
			data, err = decodeTransferChunk(s.pipe.compression, msg.Encoding, data)
			if err != nil {
				return TransferEvent{}, err
			}
			item := s.receiveManifest.Items[binding.Item]
			if err := validateChunkRange(item, msg.Offset, len(data)); err != nil {
				return TransferEvent{}, err
			}
			if err := s.receiveCoverage[binding.Item].Add(msg.Offset, msg.Offset+int64(len(data))); err != nil {
				return TransferEvent{}, fmt.Errorf("invalid chunk range for %s: %w", item.Name, err)
			}
			event.Data = data
		case "stream_end":
			if s.receiveManifest == nil {
				return TransferEvent{}, errors.New("stream_end arrived before manifest")
			}
			binding, err := s.receiveStreams.AcceptEnd(s.receiveManifest, msg)
			if err != nil {
				return TransferEvent{}, err
			}
			event.StreamID = binding.ID
			event.ItemID = binding.Item
			s.receiveEnded++
		case "done":
			if s.receiveManifest == nil {
				return TransferEvent{}, errors.New("done arrived before manifest")
			}
		case "complete":
		case "error":
			event.Error = msg.Error
		default:
			return TransferEvent{}, fmt.Errorf("unknown message type %q", msg.Type)
		}
		return event, nil
	}
}

func (s *TransferSession) applyReceiveResume(entries []protocol.ResumeEntry) {
	if s == nil || s.receiveCoverage == nil {
		return
	}
	for _, entry := range entries {
		s.receiveCoverage[entry.Item] = newByteRanges(entry.Offset)
	}
}

func (s *TransferSession) receiveReady() bool {
	if s == nil || s.receiveManifest == nil || s.receiveEnded != len(s.receiveManifest.Items) {
		return false
	}
	for itemID, item := range s.receiveManifest.Items {
		ranges := s.receiveCoverage[itemID]
		if ranges == nil || !ranges.Complete(item.Size) {
			return false
		}
	}
	return true
}

func (s *TransferSession) receivePipe(pipe *securePipe) {
	for {
		message, err := pipe.recvMessage(context.Background())
		s.receiveMessages <- receivedPipeMessage{pipeIndex: pipe.index, message: message, err: err}
		if err != nil {
			return
		}
	}
}

func (s *TransferSession) receiveMessage(ctx context.Context) (receivedPipeMessage, error) {
	if s.receiveManifest != nil && len(s.deferredMessages) > 0 {
		received := s.deferredMessages[0]
		s.deferredMessages = s.deferredMessages[1:]
		return received, received.err
	}
	return s.receiveFreshMessage(ctx)
}

func (s *TransferSession) receiveControlMessage(ctx context.Context) (receivedPipeMessage, error) {
	for {
		received, err := s.receiveFreshMessage(ctx)
		if err != nil {
			return receivedPipeMessage{}, err
		}
		if received.pipeIndex == 0 {
			if err := s.validateMessageChannel(received.pipeIndex, received.message); err != nil {
				return receivedPipeMessage{}, err
			}
			return received, nil
		}
		s.deferredMessages = append(s.deferredMessages, received)
	}
}

func (s *TransferSession) receiveFreshMessage(ctx context.Context) (receivedPipeMessage, error) {
	if s.receiveMessages == nil {
		message, err := s.pipe.recvMessage(ctx)
		return receivedPipeMessage{message: message}, err
	}
	select {
	case <-ctx.Done():
		return receivedPipeMessage{}, ctx.Err()
	case received := <-s.receiveMessages:
		return received, received.err
	}
}

func (s *TransferSession) validateMessageChannel(pipeIndex int, msg protocol.Message) error {
	switch msg.Type {
	case "chunk":
		if s.striping && len(s.pipes) > 1 {
			if pipeIndex == 0 || pipeIndex >= len(s.pipes) {
				return fmt.Errorf("chunk for item %d arrived on invalid data connection %d", msg.Item, pipeIndex)
			}
			return nil
		}
		expected := s.pipeForItem(msg.Item).index
		if pipeIndex != expected {
			return fmt.Errorf("%s for item %d arrived on connection %d, want %d", msg.Type, msg.Item, pipeIndex, expected)
		}
	case "stream_open", "stream_end":
		expected := s.pipeForItem(msg.Item).index
		if pipeIndex != expected {
			return fmt.Errorf("%s for item %d arrived on connection %d, want %d", msg.Type, msg.Item, pipeIndex, expected)
		}
	default:
		if pipeIndex != 0 {
			return fmt.Errorf("%s arrived on data connection %d", msg.Type, pipeIndex)
		}
	}
	return nil
}

func (p *securePipe) sendMessage(ctx context.Context, msg protocol.Message) error {
	plain, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	seq := p.nextSeq
	p.nextSeq++
	ciphertext, err := p.sendSession.Encrypt(seq, plain)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope{
		Version: protocol.Version,
		Seq:     seq,
		Body:    base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return err
	}
	return p.t.Send(ctx, payload)
}

func (p *securePipe) recvMessage(ctx context.Context) (protocol.Message, error) {
	payload, err := p.t.Recv(ctx)
	if err != nil {
		return protocol.Message{}, err
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return protocol.Message{}, err
	}
	if env.Version != protocol.Version {
		return protocol.Message{}, fmt.Errorf("unsupported envelope version %d", env.Version)
	}
	if env.Seq != p.recvSeq {
		return protocol.Message{}, fmt.Errorf("unexpected envelope sequence: got %d want %d", env.Seq, p.recvSeq)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Body)
	if err != nil {
		return protocol.Message{}, err
	}
	plain, err := p.recvSession.Decrypt(env.Seq, ciphertext)
	if err != nil {
		return protocol.Message{}, err
	}
	var msg protocol.Message
	if err := json.Unmarshal(plain, &msg); err != nil {
		return protocol.Message{}, err
	}
	p.recvSeq++
	return msg, nil
}
