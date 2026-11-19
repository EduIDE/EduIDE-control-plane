package main

import (
	"net"
	"sync"
)

// Header and connection limits of the three listeners. Every one of them is reachable from the ide container
// over 127.0.0.1, and net/http buffers the headers of a connection before any handler checks a token. Without
// these limits, code in the IDE fills the agent's memory, the kernel kills the agent, and the code takes over
// its ports. The limits keep the worst case (all connections with full headers) well below the memory limit.
const (
	// The proxy takes what the gateway forwards: Envoy accepts up to 60 KiB of request headers.
	proxyHeaderBytes = 64 << 10
	proxyConns       = 384
	// Operator, kubelet and the IDE extension send a few hundred bytes.
	smallHeaderBytes = 8 << 10
	smallConns       = 64
)

// limitListener accepts at most n connections at a time; further ones wait in the kernel backlog.
func limitListener(l net.Listener, n int) net.Listener {
	return &limited{Listener: l, slots: make(chan struct{}, n)}
}

type limited struct {
	net.Listener
	slots chan struct{}
}

func (l *limited) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: sync.OnceFunc(func() { <-l.slots })}, nil
}

type limitedConn struct {
	net.Conn
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
