package gitproxy

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// clientRate and clientBurst bound the requests of one client IP (one session pod). A Git command sends a handful
	// of requests, and a tool that fetches many repositories or submodules sends bursts, so the limit is far above
	// real use. It only stops a flood.
	clientRate  = 100
	clientBurst = 400
	// maxClients bounds the limiter map.
	maxClients = 4096
	// maxInFlight is the number of requests of one instance that run at the same time.
	maxInFlight = 16
	// idleTimeout cancels a request that moves no byte in either direction for this long. The total time is not
	// limited: a large clone over a slow link keeps making progress.
	idleTimeout = 5 * time.Minute
	// processingTimeout replaces the idle time between the end of a request body and the first byte of the answer.
	// The provider works in that phase (it unpacks and checks a push, or builds a pack) and sends nothing, so a
	// quiet wire is not a stall. The limit only frees the slot of a provider that never answers.
	processingTimeout = 30 * time.Minute
	// deadlineGrace is how much longer the connection deadlines run than the timer of the same limit.
	deadlineGrace = 5 * time.Second
	// peekTimeout bounds the wait for the first command of a push, which tells how an error must reach git.
	peekTimeout = 10 * time.Second
	// maxErrMessage bounds the text of an ERR line: a pkt-line length has four hex digits.
	maxErrMessage = 1000
)

// clientLimiter is a token bucket per client IP with a bounded map.
type clientLimiter struct {
	mu  sync.Mutex
	m   map[string]*rate.Limiter
	now func() time.Time
}

func newClientLimiter() *clientLimiter {
	return &clientLimiter{m: map[string]*rate.Limiter{}, now: time.Now}
}

// allow takes one token for the client. When the map is full, buckets that have refilled completely are dropped
// (they behave like no bucket); if all are still draining, an unknown client is refused.
func (c *clientLimiter) allow(ip string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	l := c.m[ip]
	if l == nil {
		if len(c.m) >= maxClients {
			for k, e := range c.m {
				if e.TokensAt(now) >= clientBurst {
					delete(c.m, k)
				}
			}
			if len(c.m) >= maxClients {
				return false
			}
		}
		l = rate.NewLimiter(clientRate, clientBurst)
		c.m[ip] = l
	}
	return l.AllowN(now, 1)
}

// slots caps the running requests per instance. Entries exist only while a request runs, so the map is bounded
// by the number of running requests.
type slots struct {
	mu sync.Mutex
	n  map[string]int
}

func (s *slots) acquire(instance string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[string]int{}
	}
	if s.n[instance] >= maxInFlight {
		return false
	}
	s.n[instance]++
	return true
}

func (s *slots) release(instance string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[instance] <= 1 {
		delete(s.n, instance)
		return
	}
	s.n[instance]--
}
