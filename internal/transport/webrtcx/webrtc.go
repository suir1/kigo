package webrtcx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/suir1/kigo/internal/transport"
)

type Signal struct {
	Type               string                   `json:"type"`
	SDP                string                   `json:"sdp,omitempty"`
	Candidate          *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Error              string                   `json:"error,omitempty"`
	Role               string                   `json:"role,omitempty"`
	ReconnectSupported bool                     `json:"reconnect_supported,omitempty"`
	ReconnectToken     string                   `json:"reconnect_token,omitempty"`
	Generation         uint64                   `json:"generation,omitempty"`
}

type Options struct {
	SignalBase      string
	RoomToken       string
	ICEServers      []webrtc.ICEServer
	Timeout         time.Duration
	Reconnect       *ReconnectState
	Protocol        string
	DialContext     func(context.Context, string, string) (net.Conn, error)
	TLSClientConfig *tls.Config
	InterfaceFilter func(string) bool
	IPFilter        func(net.IP) bool
}

type ReconnectState struct {
	mu         sync.RWMutex
	token      string
	supported  bool
	generation uint64
}

type DataChannelTransport struct {
	pc     *webrtc.PeerConnection
	dc     dataChannel
	ws     *websocket.Conn
	recv   chan []byte
	opened chan struct{}
	closed chan struct{}
	low    chan struct{}
	waitNS atomic.Int64
}

type dataChannel interface {
	Send([]byte) error
	Close() error
	BufferedAmount() uint64
}

const (
	dataChannelBufferedHigh = 4 * 1024 * 1024
	dataChannelBufferedLow  = 1 * 1024 * 1024
	dataChannelDrainTimeout = time.Second
	signalReconnectProtocol = "kigo-reconnect-v1"
)

type signaler struct {
	ws       *websocket.Conn
	writeMu  sync.Mutex
	messages chan Signal
	errs     chan error
}

func DefaultICEServers() []webrtc.ICEServer {
	return []webrtc.ICEServer{{URLs: []string{
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",
	}}}
}

func DialSender(ctx context.Context, opts Options) (transport.Transport, error) {
	opts = withDefaults(opts)
	ws, _, err := signalDialer(opts).DialContext(ctx, signalURL(opts.SignalBase, opts.RoomToken, "sender", opts.Protocol), nil)
	if err != nil {
		return nil, err
	}
	if err := writeSignalJoin(ws, opts.Reconnect); err != nil {
		_ = ws.Close()
		return nil, err
	}
	sig := newSignaler(ws)
	pc, err := newPeerConnection(opts)
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	candidates := newRemoteCandidateBuffer(pc)
	go sig.readLoop(candidates, opts.Reconnect)
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		_ = sig.write(Signal{Type: "candidate", Candidate: &init})
	})
	dc, err := pc.CreateDataChannel("kigo", nil)
	if err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	t := wrap(pc, dc, ws)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Close()
		return nil, err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Close()
		return nil, err
	}
	if err := sig.write(Signal{Type: "offer", SDP: pc.LocalDescription().SDP}); err != nil {
		t.Close()
		return nil, err
	}
	answer, err := sig.wait(ctx, "answer")
	if err != nil {
		t.Close()
		return nil, err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Close()
		return nil, err
	}
	if err := candidates.markReady(); err != nil {
		t.Close()
		return nil, err
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, opts.Timeout)
	err = t.waitOpen(openCtx)
	cancelOpen()
	if err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

func DialReceiver(ctx context.Context, opts Options) (transport.Transport, error) {
	opts = withDefaults(opts)
	ws, _, err := signalDialer(opts).DialContext(ctx, signalURL(opts.SignalBase, opts.RoomToken, "receiver", opts.Protocol), nil)
	if err != nil {
		return nil, err
	}
	if err := writeSignalJoin(ws, opts.Reconnect); err != nil {
		_ = ws.Close()
		return nil, err
	}
	sig := newSignaler(ws)
	pc, err := newPeerConnection(opts)
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	candidates := newRemoteCandidateBuffer(pc)
	go sig.readLoop(candidates, opts.Reconnect)
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		_ = sig.write(Signal{Type: "candidate", Candidate: &init})
	})
	var t *DataChannelTransport
	gotDC := make(chan struct{})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		t = wrap(pc, dc, ws)
		close(gotDC)
	})
	offer, err := sig.wait(ctx, "offer")
	if err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	if err := candidates.markReady(); err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	if err := sig.write(Signal{Type: "answer", SDP: pc.LocalDescription().SDP}); err != nil {
		_ = ws.Close()
		_ = pc.Close()
		return nil, err
	}
	dataChannelCtx, cancelDataChannel := context.WithTimeout(ctx, opts.Timeout)
	select {
	case <-gotDC:
		cancelDataChannel()
	case <-dataChannelCtx.Done():
		cancelDataChannel()
		_ = ws.Close()
		_ = pc.Close()
		return nil, errors.New("timed out waiting for data channel")
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, opts.Timeout)
	err = t.waitOpen(openCtx)
	cancelOpen()
	if err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

func wrap(pc *webrtc.PeerConnection, dc *webrtc.DataChannel, ws *websocket.Conn) *DataChannelTransport {
	t := &DataChannelTransport{
		pc:     pc,
		dc:     dc,
		ws:     ws,
		recv:   make(chan []byte, 64),
		opened: make(chan struct{}),
		closed: make(chan struct{}),
		low:    make(chan struct{}, 1),
	}
	dc.SetBufferedAmountLowThreshold(dataChannelBufferedLow)
	dc.OnBufferedAmountLow(func() {
		select {
		case t.low <- struct{}{}:
		default:
		}
	})
	dc.OnOpen(func() {
		close(t.opened)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		t.recv <- append([]byte(nil), msg.Data...)
	})
	dc.OnClose(func() {
		close(t.closed)
		close(t.recv)
	})
	return t
}

func (t *DataChannelTransport) waitOpen(ctx context.Context) error {
	select {
	case <-t.opened:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return transport.ErrClosed
	}
}

func (t *DataChannelTransport) Send(ctx context.Context, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return transport.ErrClosed
	default:
	}
	started := time.Now()
	if err := t.waitBuffered(ctx); err != nil {
		return err
	}
	t.waitNS.Store(time.Since(started).Nanoseconds())
	return t.dc.Send(payload)
}

func (t *DataChannelTransport) SendMetrics() transport.SendMetrics {
	if t == nil || t.dc == nil {
		return transport.SendMetrics{}
	}
	return transport.SendMetrics{
		BufferedBytes: t.dc.BufferedAmount(),
		BufferLimit:   dataChannelBufferedHigh,
		LastWait:      time.Duration(t.waitNS.Load()),
	}
}

func (t *DataChannelTransport) waitBuffered(ctx context.Context) error {
	if t.dc.BufferedAmount() <= dataChannelBufferedHigh {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for t.dc.BufferedAmount() > dataChannelBufferedHigh {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.closed:
			return transport.ErrClosed
		case <-t.low:
		case <-ticker.C:
		}
	}
	return nil
}

func (t *DataChannelTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case payload, ok := <-t.recv:
		if !ok {
			return nil, transport.ErrClosed
		}
		return payload, nil
	}
}

func (t *DataChannelTransport) Close() error {
	if t.ws != nil {
		_ = t.ws.Close()
	}
	if t.dc != nil {
		t.waitDrain(dataChannelDrainTimeout)
		_ = t.dc.Close()
	}
	if t.pc != nil {
		return t.pc.Close()
	}
	return nil
}

func (t *DataChannelTransport) waitDrain(timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for t.dc.BufferedAmount() > 0 {
		select {
		case <-t.closed:
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func newSignaler(ws *websocket.Conn) *signaler {
	return &signaler{
		ws:       ws,
		messages: make(chan Signal, 16),
		errs:     make(chan error, 1),
	}
}

func (s *signaler) readLoop(candidates *remoteCandidateBuffer, reconnect *ReconnectState) {
	for {
		_, payload, err := s.ws.ReadMessage()
		if err != nil {
			select {
			case s.errs <- err:
			default:
			}
			return
		}
		var sig Signal
		if err := json.Unmarshal(payload, &sig); err != nil {
			continue
		}
		if sig.Type == "signal_ready" {
			if reconnect != nil && sig.ReconnectSupported && sig.ReconnectToken != "" {
				reconnect.update(sig.ReconnectToken, sig.Generation)
			} else if reconnect != nil {
				reconnect.disable()
			}
			continue
		}
		if sig.Type == "candidate" && sig.Candidate != nil {
			_ = candidates.add(*sig.Candidate)
			continue
		}
		select {
		case s.messages <- sig:
		default:
		}
	}
}

func (s *signaler) write(sig Signal) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.WriteJSON(sig)
}

func (s *signaler) wait(ctx context.Context, typ string) (Signal, error) {
	for {
		select {
		case sig := <-s.messages:
			if sig.Type == "error" {
				return Signal{}, errors.New(sig.Error)
			}
			if sig.Type == typ {
				return sig, nil
			}
		case err := <-s.errs:
			return Signal{}, err
		case <-ctx.Done():
			return Signal{}, ctx.Err()
		}
	}
}

type remoteCandidateBuffer struct {
	pc      *webrtc.PeerConnection
	mu      sync.Mutex
	ready   bool
	pending []webrtc.ICECandidateInit
}

func newRemoteCandidateBuffer(pc *webrtc.PeerConnection) *remoteCandidateBuffer {
	return &remoteCandidateBuffer{pc: pc}
}

func (b *remoteCandidateBuffer) add(candidate webrtc.ICECandidateInit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready {
		b.pending = append(b.pending, candidate)
		return nil
	}
	return b.pc.AddICECandidate(candidate)
}

func (b *remoteCandidateBuffer) markReady() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ready = true
	for _, candidate := range b.pending {
		if err := b.pc.AddICECandidate(candidate); err != nil {
			return err
		}
	}
	b.pending = nil
	return nil
}

func withDefaults(opts Options) Options {
	if opts.ICEServers == nil {
		opts.ICEServers = DefaultICEServers()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	return opts
}

func writeSignalJoin(ws *websocket.Conn, reconnect *ReconnectState) error {
	if ws.Subprotocol() != signalReconnectProtocol {
		return nil
	}
	token := ""
	if reconnect != nil {
		token = reconnect.Token()
	}
	return ws.WriteJSON(Signal{Type: "signal_join", ReconnectToken: token})
}

func signalDialer(opts Options) *websocket.Dialer {
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{signalReconnectProtocol}
	if opts.DialContext != nil {
		dialer.NetDialContext = opts.DialContext
	}
	if opts.TLSClientConfig != nil {
		dialer.TLSClientConfig = opts.TLSClientConfig.Clone()
	}
	return &dialer
}

func newPeerConnection(opts Options) (*webrtc.PeerConnection, error) {
	configuration := webrtc.Configuration{ICEServers: opts.ICEServers}
	if opts.InterfaceFilter == nil && opts.IPFilter == nil {
		return webrtc.NewPeerConnection(configuration)
	}
	var settings webrtc.SettingEngine
	if opts.InterfaceFilter != nil {
		settings.SetInterfaceFilter(opts.InterfaceFilter)
	}
	if opts.IPFilter != nil {
		settings.SetIPFilter(opts.IPFilter)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	return api.NewPeerConnection(configuration)
}

func (s *ReconnectState) update(token string, generation uint64) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	s.token = token
	s.supported = true
	s.generation = generation
	s.mu.Unlock()
}

func (s *ReconnectState) disable() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.token = ""
	s.supported = false
	s.generation = 0
	s.mu.Unlock()
}

func (s *ReconnectState) Token() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

func (s *ReconnectState) Supported() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.supported
}

func (s *ReconnectState) Generation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func signalURL(base, roomToken, role string, protocol ...string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasPrefix(base, "http://") {
		base = "ws://" + strings.TrimPrefix(base, "http://")
	} else if strings.HasPrefix(base, "https://") {
		base = "wss://" + strings.TrimPrefix(base, "https://")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		return "ws://" + base + "/api/signal/" + roomToken
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/signal/" + roomToken
	query := u.Query()
	if role != "" {
		query.Set("role", role)
	}
	if len(protocol) > 0 && protocol[0] == "note" {
		query.Set("protocol", "note")
	}
	u.RawQuery = query.Encode()
	return u.String()
}
