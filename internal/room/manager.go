package room

import (
	"strings"
	"sync"
)

// Manager owns every active room, keyed two ways: by join code, so a
// player's typed code resolves in O(1), and by owner account, so a host
// always lands back on the room they already have open instead of getting
// a fresh one every time they reload. A room only exists here from the
// moment a host account first claims it until it's fully empty - there's
// no "the server starts with one room" state to reason about anymore.
type Manager struct {
	mu      sync.Mutex
	byCode  map[string]*Room
	byOwner map[string]*Room
	topics  []Topic
	joinURL string
}

func NewManager(topics []Topic, joinURL string) *Manager {
	return &Manager{
		byCode:  map[string]*Room{},
		byOwner: map[string]*Room{},
		topics:  topics,
		joinURL: joinURL,
	}
}

// Lookup finds a room by its join code, case-insensitively.
func (m *Manager) Lookup(code string) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rm, ok := m.byCode[normalizeCode(code)]
	return rm, ok
}

// RoomForOwner finds the room this account currently has open, if any.
func (m *Manager) RoomForOwner(ownerEmail string) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rm, ok := m.byOwner[ownerEmail]
	return rm, ok
}

// ClaimHost finds the room this account already has open, or deals a brand
// new one, and marks sess as the browser currently driving it - the same
// per-room "only one tab drives at a time" rule as always (ErrHostTaken if
// another tab already holds it). Creating the room and claiming it happen
// under the same lock, so a Reap sweep can never observe a room that
// exists but has no host session yet and mistake it for abandoned.
func (m *Manager) ClaimHost(ownerEmail, sess string) (*Room, string, error) {
	m.mu.Lock()
	rm, ok := m.byOwner[ownerEmail]
	if !ok {
		rm = newRoomWithCode(m.topics, m.joinURL, m.newCodeLocked())
		m.byOwner[ownerEmail] = rm
		m.byCode[rm.code] = rm
	}
	m.mu.Unlock()

	newSess, err := rm.ClaimHost(sess)
	if err != nil {
		return nil, "", err
	}
	return rm, newSess, nil
}

// Reap sweeps every room for stale players and a stale host connection,
// then removes any room that comes out of that fully empty - freeing its
// code and its owner's slot so their next claim deals a brand new room
// rather than resuming a dead one.
func (m *Manager) Reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for owner, rm := range m.byOwner {
		if rm.Reap() {
			delete(m.byOwner, owner)
			delete(m.byCode, rm.code)
		}
	}
}

func (m *Manager) newCodeLocked() string {
	for {
		code := newRoomCode()
		if _, exists := m.byCode[code]; !exists {
			return code
		}
	}
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
