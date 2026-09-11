package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Push delivery (EPIC-009 E09-T2).
//
// **This is Server-Sent Events, not the WebSocket the epic sketched.** The
// channel only ever pushes one way — the epic's own rule is "the socket is
// notification, the cursor is truth" — and SSE delivers that over plain
// `net/http`, while a WebSocket would make gorilla the relay's first
// third-party dependency for a stream that never reads. Browsers, WebViews and
// proxies all handle it natively. Nothing in the wire contract depends on the
// transport: a bidirectional socket can replace it later without clients
// changing how they catch up, because catching up is a cursor read either way.
//
// What is pushed is a *notification*, never the message: `{"type":"message"}`
// tells a client to pick up from its cursor. Delivery semantics stay in one
// place, so a dropped frame costs a round trip rather than a message.

const (
	// eventHeartbeatInterval keeps intermediaries from reaping an idle stream.
	eventHeartbeatInterval = 25 * time.Second
	// eventBuffer is how far a slow client may fall behind before the relay
	// stops caring. These are notifications: the cursor read that follows any
	// one of them delivers everything, so dropping them is safe.
	eventBuffer = 8
)

type streamEvent struct {
	Type      string `json:"type"`
	Identity  string `json:"identity"`
	MessageID string `json:"message_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

// hub tracks who is listening for which identity.
type hub struct {
	mu          sync.Mutex
	subscribers map[string]map[int]chan streamEvent
	nextID      int
}

func newHub() *hub {
	return &hub{subscribers: make(map[string]map[int]chan streamEvent)}
}

// subscribe registers a listener, refusing when an identity already holds
// `max` streams — one client reconnecting in a loop should not be able to pin
// a connection per attempt.
func (h *hub) subscribe(identity string, max int) (int, <-chan streamEvent, bool) {
	key := strings.ToLower(identity)
	h.mu.Lock()
	defer h.mu.Unlock()
	if max > 0 && len(h.subscribers[key]) >= max {
		return 0, nil, false
	}
	h.nextID++
	id := h.nextID
	channel := make(chan streamEvent, eventBuffer)
	if h.subscribers[key] == nil {
		h.subscribers[key] = make(map[int]chan streamEvent)
	}
	h.subscribers[key][id] = channel
	return id, channel, true
}

func (h *hub) unsubscribe(identity string, id int) {
	key := strings.ToLower(identity)
	h.mu.Lock()
	defer h.mu.Unlock()
	if streams, ok := h.subscribers[key]; ok {
		if channel, ok := streams[id]; ok {
			close(channel)
			delete(streams, id)
		}
		if len(streams) == 0 {
			delete(h.subscribers, key)
		}
	}
}

// publish notifies every listener for an identity, dropping the notification
// for anyone whose buffer is full rather than blocking the caller — a POST
// must not wait on a stalled reader.
func (h *hub) publish(identity string, event streamEvent) {
	key := strings.ToLower(identity)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, channel := range h.subscribers[key] {
		select {
		case channel <- event:
		default:
		}
	}
}

func (h *hub) count(identity string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers[strings.ToLower(identity)])
}

// handleEvents streams notifications for one identity.
//
// Authenticated with the same challenge-signed headers as the inbox pickup,
// which is why it is a streamed `fetch` rather than `EventSource`: EventSource
// cannot set headers, and the alternative — a signature in the query string —
// puts a credential in every access log.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	if !s.authorizeInboxRead(w, r, identity) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "streaming is not available")
		return
	}

	id, events, ok := s.hub.subscribe(identity, s.cfg.MaxStreamsPerIdentity)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "too_many_streams",
			"this identity already has the maximum number of open streams")
		return
	}
	defer s.hub.unsubscribe(identity, id)
	s.event(r.Context(), "stream.open", "success")
	defer s.event(r.Context(), "stream.close", "success")

	// Presence side-effect (E04-T6 unblocks it). A client that names its
	// device gets a last_seen in the owner's own devices.json — and nowhere
	// else. The privacy decision this task recorded stands: presence is not
	// user-visible in v1, meaning no peer can learn it. There is no endpoint
	// that reports another identity's presence, nothing lands in the public
	// tree, and the row is readable only with owner credentials.
	s.touchDevice(r.Context(), identity, deviceFromRequest(r))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer eagerly would turn a push channel into a poll.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Padding before the first event, because WebKit will not surface a
	// streamed response to `fetch` until enough bytes have arrived: without
	// it, a Capacitor shell holds an open, correctly-authenticated stream and
	// never sees a single event. Two kilobytes of comment costs nothing and is
	// the same trick that defeats buffering proxies.
	if _, err := fmt.Fprint(w, ":"+strings.Repeat(" ", 2048)+"\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// Say hello immediately: a client that has been offline should pick up
	// from its cursor without waiting for the next message to arrive.
	writeEvent(w, flusher, streamEvent{
		Type:      "ready",
		Identity:  identity,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	heartbeat := time.NewTicker(eventHeartbeatInterval)
	defer heartbeat.Stop()

	var idle <-chan time.Time
	if s.cfg.StreamIdleTimeout > 0 {
		timer := time.NewTimer(s.cfg.StreamIdleTimeout)
		defer timer.Stop()
		idle = timer.C
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-idle:
			return
		case <-heartbeat.C:
			// A comment frame: valid SSE, ignored by clients, keeps the
			// connection from being reaped as idle.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			if !writeEvent(w, flusher, event) {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, event streamEvent) bool {
	body, err := json.Marshal(event)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// notify publishes a delivery notification, if anyone is listening.
func (s *Server) notify(identity, kind, messageID string) {
	if s.hub == nil {
		return
	}
	s.hub.publish(identity, streamEvent{
		Type:      kind,
		Identity:  identity,
		MessageID: messageID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}
