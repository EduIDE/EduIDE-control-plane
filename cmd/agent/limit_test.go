package main

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agent"
)

// Every listener bounds headers and connections: all three are reachable from the ide container, and the
// headers are buffered before a token is checked.
func TestServersBoundHeadersAndConnections(t *testing.T) {
	srvs, conns := servers(agent.New("tok", "1", slog.New(slog.DiscardHandler)), "127.0.0.1:0")
	worst := 0
	for i, s := range srvs {
		if s.MaxHeaderBytes <= 0 || conns[i] <= 0 || s.ReadHeaderTimeout <= 0 {
			t.Fatalf("%s: header limit %d, connection limit %d, header timeout %s", s.Addr, s.MaxHeaderBytes, conns[i], s.ReadHeaderTimeout)
		}
		worst += conns[i] * (s.MaxHeaderBytes + 4096) // net/http reads 4096 bytes past the limit
	}
	// charts/eduide-control-plane/values.yaml gives the agent 64Mi.
	if worst > 32<<20 {
		t.Fatalf("all connections with full headers hold %d MiB, more than half of the agent's memory limit", worst>>20)
	}
}

func TestOversizedHeadersAreRejectedBeforeTheHandler(t *testing.T) {
	a := agent.New("tok", "1", slog.New(slog.DiscardHandler))
	srvs, _ := servers(a, "127.0.0.1:0")
	local := srvs[2]
	called := false
	local.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = local.Serve(ln) }()
	defer func() { _ = local.Close() }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		_, _ = io.WriteString(c, "GET /v1/session HTTP/1.1\r\nHost: x\r\n")
		_, _ = c.Write(bytes.Repeat([]byte("X-A: "+strings.Repeat("a", 1000)+"\r\n"), 64)) // 64 KiB, never finished
	}()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge || called {
		t.Fatalf("status %d, handler called: %v", resp.StatusCode, called)
	}
}

func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := limitListener(inner, 2)
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 3)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	for range 3 {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
	}
	first, second := <-accepted, <-accepted
	select {
	case <-accepted:
		t.Fatal("a third connection was accepted while two are open")
	case <-time.After(200 * time.Millisecond):
	}
	_ = first.Close()
	_ = first.Close() // closing twice frees one place, not two
	select {
	case c := <-accepted:
		_ = c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("the place of a closed connection was not reused")
	}
	_ = second.Close()
}
