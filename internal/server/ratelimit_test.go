package server

import (
	"testing"
	"time"
)

func TestIPLimiterAllowsBurstThenBlocks(t *testing.T) {
	l := newIPLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d within burst should be allowed", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("attempt past the burst should be blocked")
	}
}

func TestIPLimiterRefillsOverTime(t *testing.T) {
	l := newIPLimiter(1, time.Minute)

	if !l.Allow("1.2.3.4") {
		t.Fatal("first attempt should be allowed")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("second attempt with no time passed should be blocked")
	}

	// Back-date the bucket's last-seen time to simulate the refill window
	// elapsing, rather than actually sleeping in the test.
	l.buckets["1.2.3.4"].lastSeen = time.Now().Add(-time.Minute)
	if !l.Allow("1.2.3.4") {
		t.Fatal("attempt after a full refill window should be allowed again")
	}
}

func TestIPLimiterTracksIPsIndependently(t *testing.T) {
	l := newIPLimiter(1, time.Minute)

	if !l.Allow("1.2.3.4") {
		t.Fatal("first IP's first attempt should be allowed")
	}
	if !l.Allow("5.6.7.8") {
		t.Fatal("a different IP should have its own untouched budget")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("first IP should still be out of budget")
	}
}

func TestIPLimiterSweepDropsOnlyLongIdleBuckets(t *testing.T) {
	l := newIPLimiter(2, time.Minute)

	// Idle for an hour, regardless of how much of its budget it used - this
	// is the case that matters most: an attacker who got throttled and gave
	// up should still eventually be forgotten, not haunt the map forever.
	l.Allow("drained-and-old")
	l.Allow("drained-and-old")
	l.buckets["drained-and-old"].lastSeen = time.Now().Add(-time.Hour)

	l.Allow("recently-touched")

	l.Sweep()

	if _, ok := l.buckets["drained-and-old"]; ok {
		t.Error("a bucket idle for an hour should be swept regardless of its token count")
	}
	if _, ok := l.buckets["recently-touched"]; !ok {
		t.Error("a recently-touched bucket should survive the sweep")
	}
}
