// Command fake-ide is a tiny stand-in for an IDE image in e2e tests.
// It listens on loopback like a real IDE, echoes WebSocket frames, shows the
// request header names it received and mimics the eduide-session extension:
// it long-polls the agent local API and keeps the launch data in memory.
// With providers configured it also routes Git through the agent and offers POST /git-test (git.go).
// The data is never returned: /data-state shows key names and a hash only.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type localSession struct {
	Version int64             `json:"version"`
	Claimed bool              `json:"claimed"`
	Data    map[string]string `json:"data"`
	Notices []struct {
		Kind string `json:"kind"`
	} `json:"notices"`
}

type ide struct {
	agent string
	mu    sync.Mutex
	state localSession
	// gitHosts are the provider hosts the agent reported (git.go).
	gitHosts []string
}

func main() {
	addr := env("FAKE_IDE_ADDR", "127.0.0.1:3000")
	i := &ide{agent: env("FAKE_IDE_AGENT", "http://127.0.0.1:8082")}
	go i.poll(context.Background())
	go i.setupGit(context.Background())
	log.Printf("fake-ide listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: i.handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (i *ide) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><title>fake-ide</title><h1>fake-ide</h1>")
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for {
			typ, msg, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), typ, msg) != nil {
				return
			}
		}
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(r.Header))
		for k := range r.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		writeJSON(w, map[string]any{"headers": names})
	})
	// Cookie names only, never values: the e2e checks that the gateway's login cookies do not reach the IDE.
	mux.HandleFunc("/cookies", func(w http.ResponseWriter, r *http.Request) {
		names := []string{}
		for _, c := range r.Cookies() {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		writeJSON(w, map[string]any{"cookies": names})
	})
	mux.HandleFunc("/data-state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, i.dataState())
	})
	// Like the extension on real user input.
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, i.agent+"/v1/heartbeat", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "agent unreachable", http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	})
	mux.HandleFunc("POST /git-test", i.gitTest)
	mux.HandleFunc("POST /try-approve", i.tryApprove)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// dataState reports key names, a hash over the values in key order and the kinds of received notices.
func (i *ide) dataState() map[string]any {
	i.mu.Lock()
	defer i.mu.Unlock()
	keys := make([]string, 0, len(i.state.Data))
	for k := range i.state.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(i.state.Data[k]))
	}
	notices := []string{}
	for _, n := range i.state.Notices {
		notices = append(notices, n.Kind)
	}
	return map[string]any{
		"claimed": i.state.Claimed,
		"keys":    keys,
		"sha256":  hex.EncodeToString(h.Sum(nil)),
		"notices": notices,
	}
}

func (i *ide) poll(ctx context.Context) {
	client := &http.Client{Timeout: 45 * time.Second}
	var after int64
	for ctx.Err() == nil {
		u := fmt.Sprintf("%s/v1/session?after=%d&timeout=%s", i.agent, after, url.QueryEscape(env("FAKE_IDE_POLL_TIMEOUT", "30")))
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		var s localSession
		err = json.NewDecoder(resp.Body).Decode(&s)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			time.Sleep(time.Second)
			continue
		}
		after = s.Version
		i.mu.Lock()
		i.state = s
		i.mu.Unlock()
	}
}
