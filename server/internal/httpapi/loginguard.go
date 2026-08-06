package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginGuard throttles password guessing per client address. The panel is
// reachable from the internet, and a login form without a lockout is an open
// invitation to a dictionary run.
type loginGuard struct {
	maxFails int
	lockout  time.Duration
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]*guardEntry
}

type guardEntry struct {
	fails      int
	lockedTill time.Time
	lastSeen   time.Time
}

func newLoginGuard(maxFails int, lockout time.Duration) *loginGuard {
	return &loginGuard{
		maxFails: maxFails,
		lockout:  lockout,
		now:      time.Now,
		entries:  map[string]*guardEntry{},
	}
}

// Locked reports whether the address is currently blocked, and for how long.
func (g *loginGuard) Locked(key string) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[key]
	if e == nil {
		return false, 0
	}
	if left := e.lockedTill.Sub(g.now()); left > 0 {
		return true, left
	}
	return false, 0
}

// Fail records a failed attempt and reports whether it tripped the lockout.
func (g *loginGuard) Fail(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()

	e := g.entries[key]
	if e == nil {
		e = &guardEntry{}
		g.entries[key] = e
	}
	e.fails++
	e.lastSeen = g.now()
	if e.fails >= g.maxFails {
		e.lockedTill = g.now().Add(g.lockout)
		e.fails = 0
		return true
	}
	return false
}

// Reset clears the counter after a successful login.
func (g *loginGuard) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, key)
}

// sweepLocked drops stale entries so the map cannot grow without bound when the
// panel is scanned from many addresses. Callers must hold the lock.
func (g *loginGuard) sweepLocked() {
	cutoff := g.now().Add(-2 * g.lockout)
	for key, e := range g.entries {
		if e.lockedTill.Before(g.now()) && e.lastSeen.Before(cutoff) {
			delete(g.entries, key)
		}
	}
}

// clientIP returns the address a request is attributed to. Forwarded headers
// are honoured only when the deployment says it sits behind a trusted proxy:
// otherwise anyone could spoof them to dodge the lockout — or to get someone
// else's address blocked.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustedProxy {
		if v := r.Header.Get("CF-Connecting-IP"); v != "" {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			// Left-most entry is the original client.
			v, _, _ = strings.Cut(v, ",")
			return strings.TrimSpace(v)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
