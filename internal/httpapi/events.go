package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	eventsv1 "k8s.io/api/events/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// Hub fans informer events out to the SSE streams of one owner. It only signals;
// a stream re-reads the cache and sends when its payload changed, so bursts coalesce.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[chan struct{}]struct{}
	starts *startStats
	now    func() time.Time
	// emptySince is when a pool was first seen empty (U16); the admin status warns on it.
	emptySince map[string]time.Time
}

// adminKey is the subscription key of the admin streams; it never collides with an owner hash either.
const adminKey = "**"

// adminEventGap is the shortest time between two events of an admin stream (U16).
var adminEventGap = 5 * time.Second

// templatesKey is the subscription key of the template stream; owner hashes are hex, so it never collides.
const templatesKey = "*"

// templateEventGap is the shortest time between two events of the template stream (U11).
var templateEventGap = 5 * time.Second

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan struct{}]struct{}{}, starts: newStartStats(), now: time.Now, emptySince: map[string]time.Time{}}
}

func (h *Hub) subscribe(ownerHash string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[ownerHash] == nil {
		h.subs[ownerHash] = map[chan struct{}]struct{}{}
	}
	h.subs[ownerHash][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[ownerHash], ch)
		if len(h.subs[ownerHash]) == 0 {
			delete(h.subs, ownerHash)
		}
		h.mu.Unlock()
	}
}

// Notify wakes the streams of the owner with this hash.
func (h *Hub) Notify(ownerHash string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[ownerHash] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) onEvent(obj any) {
	if t, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	h.Notify(adminKey)
	switch o := obj.(type) {
	case *v1alpha1.IDESession:
		h.Notify(v1alpha1.OwnerHash(o.Spec.Owner))
	case *v1alpha1.IDEWorkspace:
		h.Notify(v1alpha1.OwnerHash(o.Spec.Owner))
	case *v1alpha1.IDETemplate:
		h.Notify(templatesKey)
	case *v1alpha1.IDEPool:
		h.trackEmpty(o)
		h.Notify(templatesKey)
	}
}

// trackEmpty remembers since when a pool has no idle ready instance although it should have one.
func (h *Hub) trackEmpty(p *v1alpha1.IDEPool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !poolEmpty(p) {
		delete(h.emptySince, p.Name)
	} else if _, ok := h.emptySince[p.Name]; !ok {
		h.emptySince[p.Name] = h.now()
	}
}

func (h *Hub) poolEmptySince(name string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.emptySince[name]
	return t, ok
}

// onSessionStart records the start duration when a session becomes Ready (old is nil for the first sight of a session).
func (h *Hub) onSessionStart(old, cur *v1alpha1.IDESession) {
	if old == nil || old.Status.Phase != v1alpha1.SessionFailed {
		h.starts.recordFailure(cur, h.now())
	}
	if old != nil && old.Status.Phase == v1alpha1.SessionReady && old.Status.Timestamps.Ready != nil {
		return
	}
	if h.starts.record(cur) {
		h.Notify(templatesKey)
	}
}

// Register feeds the hub from the informers of the cache.
func (h *Hub) Register(ctx context.Context, c cache.Cache) error {
	for _, obj := range []client.Object{&v1alpha1.IDESession{}, &v1alpha1.IDEWorkspace{}, &v1alpha1.IDETemplate{}, &v1alpha1.IDEPool{}} {
		inf, err := c.GetInformer(ctx, obj)
		if err != nil {
			return err
		}
		handler := toolscache.ResourceEventHandlerFuncs{
			AddFunc:    h.onEvent,
			UpdateFunc: func(_, n any) { h.onEvent(n) },
			DeleteFunc: h.onEvent,
		}
		if _, ok := obj.(*v1alpha1.IDESession); ok {
			handler.AddFunc = func(n any) {
				h.onEvent(n)
				h.onSessionStart(nil, n.(*v1alpha1.IDESession))
			}
			handler.UpdateFunc = func(o, n any) {
				h.onEvent(n)
				h.onSessionStart(o.(*v1alpha1.IDESession), n.(*v1alpha1.IDESession))
			}
		}
		if _, err := inf.AddEventHandler(handler); err != nil {
			return err
		}
	}
	return nil
}

// RegisterEvents wakes the admin streams on Kubernetes events (scaler decisions, session warnings).
// It is called only when the admin API is on, because it starts an informer for events.
func (h *Hub) RegisterEvents(ctx context.Context, c cache.Cache) error {
	inf, err := c.GetInformer(ctx, &eventsv1.Event{})
	if err != nil {
		return err
	}
	_, err = inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{AddFunc: func(any) { h.Notify(adminKey) }})
	return err
}

// snapshot returns the current payload of a stream and whether the stream ends after it.
type snapshot func(ctx context.Context) (payload any, done bool, err *apiError)

// stream serves Server-Sent Events: the current state first, then one event per change,
// a ping comment every s.ping, and no polling.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, ownerHash, event string, snap snapshot) {
	s.streamEvery(w, r, ownerHash, event, 0, snap)
}

// streamEvery is stream with at least gap between two events; changes in between coalesce into the next event.
func (s *Server) streamEvery(w http.ResponseWriter, r *http.Request, ownerHash, event string, gap time.Duration, snap snapshot) {
	changes, cancel := s.hub.subscribe(ownerHash)
	defer cancel()
	payload, done, perr := snap(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var last []byte
	var sentAt time.Time
	send := func(payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if bytes.Equal(b, last) {
			return true
		}
		last, sentAt = b, time.Now()
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(payload) || done {
		return
	}
	tick := time.NewTicker(s.ping)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-changes:
			if wait := gap - time.Since(sentAt); wait > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(wait):
				}
			}
			payload, done, perr := snap(r.Context())
			if perr != nil || !send(payload) || done {
				return
			}
		}
	}
}
