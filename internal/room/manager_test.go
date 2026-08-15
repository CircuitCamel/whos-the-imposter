package room

import (
	"testing"
	"time"
)

func TestManagerGivesDifferentOwnersDifferentRooms(t *testing.T) {
	m := NewManager(topicsForTest(), "http://test")

	roomA, sessA, err := m.ClaimHost("alice@example.com", "")
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	roomB, sessB, err := m.ClaimHost("bob@example.com", "")
	if err != nil {
		t.Fatalf("bob claim: %v", err)
	}

	if roomA.Code() == roomB.Code() {
		t.Fatalf("two different owners got the same room code %q", roomA.Code())
	}
	if sessA == sessB {
		t.Fatal("two different owners got the same host session")
	}

	// They're genuinely independent: seating a player in one doesn't touch
	// the other.
	if _, err := roomA.Join("Alice's guest", ""); err != nil {
		t.Fatal(err)
	}
	if got := len(roomB.players); got != 0 {
		t.Fatalf("bob's room should still be empty, has %d players", got)
	}
}

func TestManagerReclaimReturnsTheSameRoom(t *testing.T) {
	m := NewManager(topicsForTest(), "http://test")

	first, sess, err := m.ClaimHost("alice@example.com", "")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// A real claim is always followed by the host screen opening its SSE
	// stream, which is what actually makes the tab "active" for ErrHostTaken
	// purposes - ClaimHost alone doesn't touch hostConns.
	first.Subscribe("", true)

	// Same account, same browser (same session cookie) - e.g. a reload.
	second, sess2, err := m.ClaimHost("alice@example.com", sess)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if first != second {
		t.Fatal("reclaiming with a valid session should return the same room, not a new one")
	}
	if sess2 != sess {
		t.Fatalf("reclaiming with a valid session should keep the same session, got a new one")
	}

	// Same account, no session (e.g. a second tab) - blocked while the
	// first tab is still active, same rule as the single-room days.
	if _, _, err := m.ClaimHost("alice@example.com", ""); err != ErrHostTaken {
		t.Fatalf("a second tab should be turned away with ErrHostTaken, got %v", err)
	}
}

func TestManagerLookupByCode(t *testing.T) {
	m := NewManager(topicsForTest(), "http://test")
	rm, _, err := m.ClaimHost("alice@example.com", "")
	if err != nil {
		t.Fatal(err)
	}

	got, ok := m.Lookup(rm.Code())
	if !ok || got != rm {
		t.Fatal("looking up the room's own code should find it")
	}
	// Codes are compared case-insensitively, same as the old single-room Join.
	if _, ok := m.Lookup(lower(rm.Code())); !ok {
		t.Fatal("code lookup should be case-insensitive")
	}
	if _, ok := m.Lookup("ZZZZ"); ok {
		t.Fatal("an unused code should not resolve to a room")
	}
}

func TestManagerReapRemovesEmptyRooms(t *testing.T) {
	m := NewManager(topicsForTest(), "http://test")
	rm, _, err := m.ClaimHost("alice@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	code := rm.Code()

	// The host disconnects (Conns back to 0) and enough time passes.
	old := GraceTTL
	GraceTTL = 0
	defer func() { GraceTTL = old }()
	rm.hostConns = 0
	rm.hostLastSeen = time.Now().Add(-time.Millisecond) // safely past GraceTTL=0

	m.Reap()

	if _, ok := m.Lookup(code); ok {
		t.Fatal("an abandoned room should be removed and its code freed")
	}
	if _, ok := m.RoomForOwner("alice@example.com"); ok {
		t.Fatal("an abandoned room should free its owner's slot too")
	}

	// The same account claiming again gets a genuinely fresh room.
	fresh, _, err := m.ClaimHost("alice@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == rm {
		t.Fatal("reclaiming after a reap should not resurrect the old room")
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
