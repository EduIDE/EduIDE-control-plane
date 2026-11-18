package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// Hub fans informer events out to the SSE streams of one owner. It only signals;
// a stream re-reads the cache and sends when its payload changed, so bursts coalesce.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan struct{}]struct{}{}} }

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
	switch o := obj.(type) {
	case *v1alpha1.IDESession:
		h.Notify(v1alpha1.OwnerHash(o.Spec.Owner))
	case *v1alpha1.IDEWorkspace:
		h.Notify(v1alpha1.OwnerHash(o.Spec.Owner))
	}
}

// Register feeds the hub from the informers of the cache.
func (h *Hub) Register(ctx context.Context, c cache.Cache) error {
	for _, obj := range []client.Object{&v1alpha1.IDESession{}, &v1alpha1.IDEWorkspace{}} {
		inf, err := c.GetInformer(ctx, obj)
		if err != nil {
			return err
		}
		if _, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc:    h.onEvent,
			UpdateFunc: func(_, n any) { h.onEvent(n) },
			DeleteFunc: h.onEvent,
		}); err != nil {
			return err
		}
	}
	return nil
}

// snapshot returns the current payload of a stream and whether the stream ends after it.
type snapshot func(ctx context.Context) (payload any, done bool, err *apiError)

// stream serves Server-Sent Events: the current state first, then one event per change,
// a ping comment every s.ping, and no polling.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, ownerHash, event string, snap snapshot) {
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
	send := func(payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if bytes.Equal(b, last) {
			return true
		}
		last = b
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
			payload, done, perr := snap(r.Context())
			if perr != nil || !send(payload) || done {
				return
			}
		}
	}
}
