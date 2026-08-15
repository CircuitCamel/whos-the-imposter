package server

import (
	"sync"
	"time"
)

// ipLimiter is a simple per-IP token bucket. Burst tokens are available
// immediately - enough for a whole table joining, or a fumbled password, in
// one go - and refill slowly after that, so a real person never notices it
// while a script trying code after code (or password after password) gets
// throttled down hard within a few seconds.
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	refill  time.Duration // time to regain one token
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newIPLimiter(burst int, refill time.Duration) *ipLimiter {
	return &ipLimiter{
		buckets: map[string]*bucket{},
		burst:   float64(burst),
		refill:  refill,
	}
}

// Allow reports whether ip may make another attempt right now, consuming a
// token if so.
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[ip] = b
	} else {
		b.tokens += now.Sub(b.lastSeen).Seconds() / l.refill.Seconds()
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastSeen = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Sweep drops buckets that haven't been touched in a while, so a
// long-running server doesn't accumulate one entry per IP that's ever
// tried, forever. Idle time alone is the right test here, not token count:
// tokens only get refilled lazily inside Allow, so a bucket an attacker
// drained and then abandoned would sit at a low count and never look
// "full" again on its own - checking idle time catches that too.
func (l *ipLimiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for ip, b := range l.buckets {
		if now.Sub(b.lastSeen) > time.Hour {
			delete(l.buckets, ip)
		}
	}
}
