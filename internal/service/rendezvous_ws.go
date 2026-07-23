package service

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func (s *Server) openRendezvousWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	pathPrefix string,
	roleName string,
	readLimit int64,
) (token string, role string, conn *websocket.Conn, ok bool) {
	if !s.allowRequest(r) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return "", "", nil, false
	}
	token = strings.TrimPrefix(r.URL.Path, pathPrefix)
	if !isRoomToken(token) {
		http.Error(w, "invalid room token", http.StatusBadRequest)
		return "", "", nil, false
	}
	role = r.URL.Query().Get("role")
	if !isSignalRole(role) {
		http.Error(w, "invalid "+roleName+" role", http.StatusBadRequest)
		return "", "", nil, false
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return "", "", nil, false
	}
	conn.SetReadLimit(readLimit)
	_ = conn.SetReadDeadline(time.Now().Add(negotiationWebSocketWait))
	return token, role, conn, true
}

func closeRendezvousWebSocket(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(websocketWriteWait),
	)
	_ = conn.Close()
}

type rendezvousOutcome[T any] struct {
	response T
	err      error
}

type rendezvousPeer[C, R any] struct {
	role    string
	cap     C
	passive bool
	result  chan rendezvousOutcome[R]
}

type rendezvousRoom[C, R any] struct {
	expires time.Time
	slots   map[string]*rendezvousPeer[C, R]
}

type rendezvousRegistry[C, R any] struct {
	rooms      map[string]*rendezvousRoom[C, R]
	name       string
	passiveTTL time.Duration
}

func newRendezvousRegistry[C, R any](name string, passiveTTL time.Duration) *rendezvousRegistry[C, R] {
	return &rendezvousRegistry[C, R]{rooms: map[string]*rendezvousRoom[C, R]{}, name: name, passiveTTL: passiveTTL}
}

func (r *rendezvousRegistry[C, R]) join(
	token string,
	role string,
	capability C,
	passive bool,
	now time.Time,
	ttl time.Duration,
) (*rendezvousPeer[C, R], *rendezvousRoom[C, R], bool, error) {
	if !isRoomToken(token) || !isSignalRole(role) {
		return nil, nil, false, errors.New("invalid " + r.name + " room")
	}
	room := r.rooms[token]
	if room != nil && now.After(room.expires) {
		r.expire(token, room)
		room = nil
	}
	if room == nil {
		expires := now.Add(ttl)
		if passive && r.passiveTTL > 0 {
			expires = now.Add(r.passiveTTL)
		}
		room = &rendezvousRoom[C, R]{
			expires: expires,
			slots:   map[string]*rendezvousPeer[C, R]{},
		}
		r.rooms[token] = room
	}
	if existing := room.slots[role]; existing != nil {
		if passive {
			return existing, room, false, nil
		}
		if !existing.passive {
			message := role + " already negotiating"
			if r.passiveTTL == 0 {
				message = role + " already waiting for " + r.name
			}
			return nil, nil, false, errors.New(message)
		}
	}
	peer := &rendezvousPeer[C, R]{
		role: role, cap: capability, passive: passive,
		result: make(chan rendezvousOutcome[R], 1),
	}
	room.slots[role] = peer
	if !passive && room.expires.Before(now.Add(ttl)) {
		room.expires = now.Add(ttl)
	}
	return peer, room, true, nil
}

func (r *rendezvousRegistry[C, R]) leave(token, role string, peer *rendezvousPeer[C, R]) {
	if peer == nil {
		return
	}
	room := r.rooms[token]
	if room == nil || room.slots[role] != peer {
		return
	}
	delete(room.slots, role)
	if len(room.slots) == 0 {
		delete(r.rooms, token)
	}
}

func (r *rendezvousRegistry[C, R]) complete(token string) {
	delete(r.rooms, token)
}

func (r *rendezvousRegistry[C, R]) expireBefore(now time.Time) {
	for token, room := range r.rooms {
		if now.After(room.expires) {
			r.expire(token, room)
		}
	}
}

func (r *rendezvousRegistry[C, R]) expire(token string, room *rendezvousRoom[C, R]) {
	err := errors.New(r.name + " room expired")
	for _, peer := range room.slots {
		if peer.passive {
			continue
		}
		select {
		case peer.result <- rendezvousOutcome[R]{err: err}:
		default:
		}
	}
	delete(r.rooms, token)
}

func serveRendezvousResult[T any](conn *websocket.Conn, result <-chan rendezvousOutcome[T]) {
	_ = conn.SetReadDeadline(time.Now().Add(websocketPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(websocketPongWait))
	})
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(websocketPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case outcome := <-result:
			if outcome.err != nil {
				_ = conn.WriteJSON(map[string]string{"type": "error", "error": outcome.err.Error()})
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			_ = conn.WriteJSON(outcome.response)
			return
		case <-disconnected:
			return
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
