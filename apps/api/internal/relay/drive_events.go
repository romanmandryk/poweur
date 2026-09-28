package relay

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/engine"
)

// Drive event streams (E20-T5/T7). `GET /drive/{identity}/events` streams
// `drive.changed` for one drive to its owner or to any member, wherever the
// member is homed: a member on another relay authenticates here directly.
// Each change is delivered only to callers who may see it, decided against
// the state the change produced; a revocation that leaves a member with no
// share on the drive closes their stream.

// maxDriveStreamsPerActor bounds one caller's open streams on one drive.
const maxDriveStreamsPerActor = 8

type driveSubscriber struct {
	actor  string
	events chan streamEvent
	closed bool
}

type driveStreams struct {
	mu      sync.Mutex
	next    int
	byDrive map[string]map[int]*driveSubscriber
}

func newDriveStreams() *driveStreams {
	return &driveStreams{byDrive: map[string]map[int]*driveSubscriber{}}
}

func (d *driveStreams) subscribe(driveID, actor string) (int, *driveSubscriber, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, sub := range d.byDrive[driveID] {
		if sub.actor == actor {
			count++
		}
	}
	if count >= maxDriveStreamsPerActor {
		return 0, nil, false
	}
	d.next++
	sub := &driveSubscriber{actor: actor, events: make(chan streamEvent, eventBuffer)}
	if d.byDrive[driveID] == nil {
		d.byDrive[driveID] = map[int]*driveSubscriber{}
	}
	d.byDrive[driveID][d.next] = sub
	return d.next, sub, true
}

func (d *driveStreams) unsubscribe(driveID string, id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sub, ok := d.byDrive[driveID][id]; ok {
		if !sub.closed {
			close(sub.events)
			sub.closed = true
		}
		delete(d.byDrive[driveID], id)
		if len(d.byDrive[driveID]) == 0 {
			delete(d.byDrive, driveID)
		}
	}
}

// publish runs under the drive's lock: it only filters and queues.
func (d *driveStreams) publish(driveID string, event streamEvent, change engine.Change, audience engine.Audience) {
	d.mu.Lock()
	defer d.mu.Unlock()
	revocation := change.Operation == "unshare"
	for _, sub := range d.byDrive[driveID] {
		if sub.closed {
			continue
		}
		if audience.CanRead(sub.actor) {
			select {
			case sub.events <- event:
			default:
			}
		}
		if revocation && !audience.HasAccess(sub.actor) {
			close(sub.events)
			sub.closed = true
		}
	}
}

func (s *Server) handleDriveEvents(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if access, err := s.engine.HasAccess(r.Context(), driveID, actor); err != nil || !access {
		if err == nil {
			err = engine.ErrForbidden
		}
		s.writeDriveError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "streaming is not available")
		return
	}
	id, sub, ok := s.driveStreams.subscribe(driveID, actor)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "too many open streams on this drive")
		return
	}
	defer s.driveStreams.unsubscribe(driveID, id)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// See handleEvents: WebKit surfaces a streamed body only after ~2 KiB.
	if _, err := fmt.Fprint(w, ":"+strings.Repeat(" ", 2048)+"\n\n"); err != nil {
		return
	}
	flusher.Flush()
	writeEvent(w, flusher, streamEvent{Type: "ready", Identity: driveID, Timestamp: time.Now().UTC().Format(time.RFC3339)})

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
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-sub.events:
			if !open {
				writeEvent(w, flusher, streamEvent{Type: "drive.revoked", Identity: driveID, Timestamp: time.Now().UTC().Format(time.RFC3339)})
				return
			}
			if !writeEvent(w, flusher, event) {
				return
			}
		}
	}
}
