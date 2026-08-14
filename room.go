package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Phase string

const (
	PhaseLobby    Phase = "lobby"
	PhaseReveal   Phase = "reveal"
	PhaseQuestion Phase = "question"
	PhaseDiscuss  Phase = "discuss"
	PhaseVote     Phase = "vote"
	PhaseResults  Phase = "results"
)

const (
	MaxNameLen = 16
	MinPlayers = 2
)

// graceTTL is how long a player (or the host screen) may stay disconnected
// before being dropped from the room. Long enough by default to survive a
// phone locking its screen, a browser reload, or a walk to the kitchen.
// Tunable with the -grace flag.
var graceTTL = 90 * time.Second

var (
	ErrBadName     = errors.New("name must be 1-16 characters")
	ErrNameTaken   = errors.New("someone already has that name")
	ErrBadCode     = errors.New("wrong room code")
	ErrRoomFull    = errors.New("room is full")
	ErrNotEnough   = errors.New("need at least 3 connected players")
	ErrWrongPhase  = errors.New("that isn't allowed right now")
	ErrNotInRound  = errors.New("you're sitting this round out")
	ErrBadTarget   = errors.New("no such player to vote for")
	ErrSelfVote    = errors.New("you can't vote for yourself")
	ErrHostTaken   = errors.New("another screen is already hosting")
	ErrNotYourTurn = errors.New("it isn't your turn to answer")
	maxPlayers     = 16
	codeAlphabet   = "ABCDEFGHIJKLMNPQRSTUVWXYZ2"
	roomCodeLength = 4
)

type Player struct {
	ID       string
	Name     string
	Order    int
	Conns    int
	LastSeen time.Time

	InRound    bool
	IsImposter bool
	SeenRole   bool
	VotedFor   string
}

func (p *Player) connected() bool { return p.Conns > 0 }

// sub is one open SSE stream. Every state change pushes a fresh snapshot,
// tailored to who is listening, down each sub's channel.
type sub struct {
	playerID string // empty when isHost
	isHost   bool
	ch       chan []byte
}

type Room struct {
	mu sync.Mutex

	code string
	// occupied flips true the moment anyone joins or claims the host screen.
	// The code is only rerolled once an occupied room empties out again, so a
	// freshly started server doesn't immediately reroll its own code.
	occupied bool

	phase   Phase
	round   int
	players map[string]*Player
	nextOrd int

	topic      Topic
	imposterID string
	results    *resultsView

	// askOrder is a random cycle of the players in the round: each asks the
	// next one along, and the last asks the first. That gives exactly one
	// question per player, with everyone asked exactly once, and it flows at
	// the table because whoever just answered is the next to ask.
	askOrder []string
	askIdx   int

	hostSess     string
	hostConns    int
	hostLastSeen time.Time

	topics  []Topic
	joinURL string
	subs    map[*sub]bool
}

func NewRoom(topics []Topic, joinURL string) *Room {
	return &Room{
		code:    newRoomCode(),
		phase:   PhaseLobby,
		players: map[string]*Player{},
		topics:  topics,
		joinURL: joinURL,
		subs:    map[*sub]bool{},
	}
}

func (r *Room) Code() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// ---------- wire types ----------

type playerView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	InRound   bool   `json:"inRound"`
	Ready     bool   `json:"ready"` // seen their role, or cast their vote
}

type youView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	InRound  bool   `json:"inRound"`
	SeenRole bool   `json:"seenRole"`
	VotedFor string `json:"votedFor"`
}

type roleView struct {
	Imposter bool   `json:"imposter"`
	Topic    string `json:"topic,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type askView struct {
	Number   int    `json:"number"` // 1-based
	Total    int    `json:"total"`
	AskerID  string `json:"askerId"`
	Asker    string `json:"asker"`
	TargetID string `json:"targetId"`
	Target   string `json:"target"`
	Next     string `json:"next,omitempty"` // who the current target will ask
}

type tallyEntry struct {
	Name  string `json:"name"`
	Votes int    `json:"votes"`
}

type resultsView struct {
	Topic      string       `json:"topic"`
	Hint       string       `json:"hint"`
	Imposter   string       `json:"imposter"`
	ImposterID string       `json:"imposterId"`
	Tally      []tallyEntry `json:"tally"`
	Accused    string       `json:"accused"`
	Caught     bool         `json:"caught"`
	Tie        bool         `json:"tie"`
}

type snapshot struct {
	Phase      Phase        `json:"phase"`
	Code       string       `json:"code"`
	Round      int          `json:"round"`
	MinPlayers int          `json:"minPlayers"`
	MaxName    int          `json:"maxName"`
	HostOnline bool         `json:"hostOnline"`
	IsHost     bool         `json:"isHost"`
	JoinURL    string       `json:"joinURL,omitempty"`
	Players    []playerView `json:"players"`
	Ask        *askView     `json:"ask,omitempty"`
	You        *youView     `json:"you,omitempty"`
	Role       *roleView    `json:"role,omitempty"`
	Results    *resultsView `json:"results,omitempty"`
}

// ---------- joining ----------

func (r *Room) Join(code, name, existingID string) (*Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !strings.EqualFold(strings.TrimSpace(code), r.code) {
		return nil, ErrBadCode
	}

	name = strings.Join(strings.Fields(name), " ")
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxNameLen {
		return nil, ErrBadName
	}

	// Already in the room? Treat this as a rename rather than a second seat.
	if p, ok := r.players[existingID]; ok {
		if err := r.nameFreeLocked(name, p.ID); err != nil {
			return nil, err
		}
		p.Name = name
		r.publishLocked()
		return p, nil
	}

	if len(r.players) >= maxPlayers {
		return nil, ErrRoomFull
	}
	if err := r.nameFreeLocked(name, ""); err != nil {
		return nil, err
	}

	p := &Player{
		ID:       newID(),
		Name:     name,
		Order:    r.nextOrd,
		LastSeen: time.Now(),
	}
	r.nextOrd++
	r.players[p.ID] = p
	r.occupied = true
	r.publishLocked()
	return p, nil
}

func (r *Room) nameFreeLocked(name, exceptID string) error {
	for _, p := range r.players {
		if p.ID != exceptID && strings.EqualFold(p.Name, name) {
			return ErrNameTaken
		}
	}
	return nil
}

func (r *Room) Lookup(id string) (*Player, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.players[id]
	return p, ok
}

func (r *Room) Leave(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.players[id]; !ok {
		return
	}
	delete(r.players, id)
	r.clearVotesForLocked(id)
	r.dropFromOrderLocked(id)
	r.maybeAdvanceLocked()
	r.publishLocked()
}

// ---------- host screen ----------

func (r *Room) ClaimHost(sess string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if sess != "" && sess == r.hostSess {
		r.occupied = true
		return sess, nil
	}
	// Free, or the previous host screen has gone away.
	if r.hostSess == "" || r.hostConns == 0 {
		r.hostSess = newID()
		r.hostLastSeen = time.Now()
		r.occupied = true
		r.publishLocked()
		return r.hostSess, nil
	}
	return "", ErrHostTaken
}

func (r *Room) IsHost(sess string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sess != "" && sess == r.hostSess
}

// ---------- round flow ----------

func (r *Room) StartRound() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	eligible := make([]*Player, 0, len(r.players))
	for _, p := range r.players {
		if p.connected() {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) < MinPlayers {
		return ErrNotEnough
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Order < eligible[j].Order })

	for _, p := range r.players {
		p.InRound = false
		p.IsImposter = false
		p.SeenRole = false
		p.VotedFor = ""
	}
	for _, p := range eligible {
		p.InRound = true
	}

	r.topic = r.topics[randIndex(len(r.topics))]
	imp := eligible[randIndex(len(eligible))]
	imp.IsImposter = true
	r.imposterID = imp.ID

	r.buildAskOrderLocked(eligible)
	r.results = nil
	r.round++
	r.phase = PhaseReveal
	r.publishLocked()
	return nil
}

func (r *Room) MarkSeen(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.players[id]
	if !ok {
		return ErrNotInRound
	}
	if r.phase != PhaseReveal && r.phase != PhaseDiscuss {
		return ErrWrongPhase
	}
	if !p.InRound {
		return ErrNotInRound
	}
	p.SeenRole = true
	r.maybeAdvanceLocked()
	r.publishLocked()
	return nil
}

func (r *Room) OpenVoting() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == PhaseVote || r.phase == PhaseResults || r.phase == PhaseLobby {
		return ErrWrongPhase
	}
	r.phase = PhaseVote
	r.publishLocked()
	return nil
}

// ---------- the questioning round ----------

// buildAskOrderLocked lays the players out in a random cycle. Reading a
// shuffled list as a ring gives a uniformly random cyclic permutation, which
// is exactly the property we want: nobody asks themselves, everybody asks
// once, everybody is asked once, in n questions for n players.
func (r *Room) buildAskOrderLocked(players []*Player) {
	ids := make([]string, len(players))
	for i, p := range players {
		ids[i] = p.ID
	}
	for i := len(ids) - 1; i > 0; i-- {
		j := randIndex(i + 1)
		ids[i], ids[j] = ids[j], ids[i]
	}
	r.askOrder = ids
	r.askIdx = 0
}

// currentAskLocked returns who is asking whom right now.
func (r *Room) currentAskLocked() (asker, target *Player, ok bool) {
	n := len(r.askOrder)
	if n < 2 || r.askIdx >= n {
		return nil, nil, false
	}
	a, aok := r.players[r.askOrder[r.askIdx]]
	t, tok := r.players[r.askOrder[(r.askIdx+1)%n]]
	if !aok || !tok {
		return nil, nil, false
	}
	return a, t, true
}

// AnswerQuestion is called by the player who was just asked. Answering hands
// them the next question, which is what keeps the ring moving.
func (r *Room) AnswerQuestion(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseQuestion {
		return ErrWrongPhase
	}
	_, target, ok := r.currentAskLocked()
	if !ok {
		r.phase = PhaseDiscuss
		r.publishLocked()
		return nil
	}
	if target.ID != playerID {
		return ErrNotYourTurn
	}
	r.advanceQuestionLocked()
	r.publishLocked()
	return nil
}

// NextQuestion lets the host move things along when a phone has died or
// someone has wandered off mid-answer.
func (r *Room) NextQuestion() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseQuestion {
		return ErrWrongPhase
	}
	r.advanceQuestionLocked()
	r.publishLocked()
	return nil
}

// ToDiscussion cuts the questioning round short and opens the floor.
func (r *Room) ToDiscussion() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseReveal && r.phase != PhaseQuestion {
		return ErrWrongPhase
	}
	r.phase = PhaseDiscuss
	r.askIdx = len(r.askOrder)
	r.publishLocked()
	return nil
}

func (r *Room) advanceQuestionLocked() {
	r.askIdx++
	if r.askIdx >= len(r.askOrder) {
		r.phase = PhaseDiscuss
	}
}

// dropFromOrderLocked keeps the ring intact when someone leaves mid-round.
// Questions already asked stay asked; the ring just closes up around the gap.
func (r *Room) dropFromOrderLocked(id string) {
	for i, pid := range r.askOrder {
		if pid != id {
			continue
		}
		r.askOrder = append(r.askOrder[:i], r.askOrder[i+1:]...)
		if i < r.askIdx {
			r.askIdx--
		}
		if r.phase == PhaseQuestion && (len(r.askOrder) < 2 || r.askIdx >= len(r.askOrder)) {
			r.phase = PhaseDiscuss
		}
		return
	}
}

func (r *Room) Vote(voterID, targetID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseVote {
		return ErrWrongPhase
	}
	v, ok := r.players[voterID]
	if !ok || !v.InRound {
		return ErrNotInRound
	}
	if voterID == targetID {
		return ErrSelfVote
	}
	t, ok := r.players[targetID]
	if !ok || !t.InRound {
		return ErrBadTarget
	}
	v.VotedFor = targetID
	r.maybeAdvanceLocked()
	r.publishLocked()
	return nil
}

// CloseVoting ends voting early — useful when someone rage-quits mid-vote.
func (r *Room) CloseVoting() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseVote {
		return ErrWrongPhase
	}
	r.tallyLocked()
	r.publishLocked()
	return nil
}

// ToLobby returns to the lobby keeping everyone seated, ready for a new round.
func (r *Room) ToLobby() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = PhaseLobby
	r.results = nil
	r.imposterID = ""
	r.topic = Topic{}
	r.askOrder = nil
	r.askIdx = 0
	for _, p := range r.players {
		p.InRound = false
		p.IsImposter = false
		p.SeenRole = false
		p.VotedFor = ""
	}
	r.publishLocked()
}

func (r *Room) Kick(id string) {
	r.Leave(id)
}

func (r *Room) maybeAdvanceLocked() {
	switch r.phase {
	case PhaseReveal:
		n, done := 0, 0
		for _, p := range r.players {
			if p.InRound && p.connected() {
				n++
				if p.SeenRole {
					done++
				}
			}
		}
		if n > 0 && done == n {
			r.phase = PhaseQuestion
			if len(r.askOrder) < 2 {
				r.phase = PhaseDiscuss
			}
		}
	case PhaseVote:
		n, done := 0, 0
		for _, p := range r.players {
			if p.InRound && p.connected() {
				n++
				if p.VotedFor != "" {
					done++
				}
			}
		}
		if n > 0 && done == n {
			r.tallyLocked()
		}
	}
}

// hasAskedLocked reports whether this player has already had their question.
func (r *Room) hasAskedLocked(id string) bool {
	for i := 0; i < r.askIdx && i < len(r.askOrder); i++ {
		if r.askOrder[i] == id {
			return true
		}
	}
	return false
}

func (r *Room) clearVotesForLocked(targetID string) {
	for _, p := range r.players {
		if p.VotedFor == targetID {
			p.VotedFor = ""
		}
	}
}

func (r *Room) tallyLocked() {
	counts := map[string]int{}
	for _, p := range r.players {
		if p.InRound && p.VotedFor != "" {
			counts[p.VotedFor]++
		}
	}

	tally := make([]tallyEntry, 0, len(counts))
	top, tie := 0, false
	var accusedID string
	for id, c := range counts {
		p, ok := r.players[id]
		if !ok {
			continue
		}
		tally = append(tally, tallyEntry{Name: p.Name, Votes: c})
		switch {
		case c > top:
			top, accusedID, tie = c, id, false
		case c == top:
			tie = true
		}
	}
	sort.Slice(tally, func(i, j int) bool {
		if tally[i].Votes != tally[j].Votes {
			return tally[i].Votes > tally[j].Votes
		}
		return tally[i].Name < tally[j].Name
	})

	res := &resultsView{
		Topic:      r.topic.Name,
		Hint:       r.topic.Hint,
		ImposterID: r.imposterID,
		Tally:      tally,
	}
	if imp, ok := r.players[r.imposterID]; ok {
		res.Imposter = imp.Name
	}
	if tie || accusedID == "" {
		res.Tie = true
	} else {
		res.Accused = r.players[accusedID].Name
		res.Caught = accusedID == r.imposterID
	}

	r.results = res
	r.phase = PhaseResults
}

// ---------- SSE subscribers ----------

func (r *Room) Subscribe(playerID string, isHost bool) *sub {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := &sub{playerID: playerID, isHost: isHost, ch: make(chan []byte, 8)}
	r.subs[s] = true

	if isHost {
		r.hostConns++
	} else if p, ok := r.players[playerID]; ok {
		p.Conns++
		p.LastSeen = time.Now()
	}
	r.publishLocked()
	return s
}

func (r *Room) Unsubscribe(s *sub) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.subs[s] {
		return
	}
	delete(r.subs, s)
	close(s.ch)

	if s.isHost {
		if r.hostConns > 0 {
			r.hostConns--
		}
		r.hostLastSeen = time.Now()
	} else if p, ok := r.players[s.playerID]; ok {
		if p.Conns > 0 {
			p.Conns--
		}
		p.LastSeen = time.Now()
	}
	r.maybeAdvanceLocked()
	r.publishLocked()
}

func (r *Room) publishLocked() {
	byPlayer := map[string][]byte{}
	var hostMsg []byte

	for s := range r.subs {
		var msg []byte
		if s.isHost {
			if hostMsg == nil {
				hostMsg, _ = json.Marshal(r.snapshotLocked("", true))
			}
			msg = hostMsg
		} else {
			m, ok := byPlayer[s.playerID]
			if !ok {
				m, _ = json.Marshal(r.snapshotLocked(s.playerID, false))
				byPlayer[s.playerID] = m
			}
			msg = m
		}

		select {
		case s.ch <- msg:
		default:
			// Slow or dead reader — drop it; the browser will reconnect and
			// pick up a fresh snapshot on the way back in.
		}
	}
}

func (r *Room) snapshotLocked(playerID string, isHost bool) snapshot {
	list := make([]*Player, 0, len(r.players))
	for _, p := range r.players {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Order < list[j].Order })

	views := make([]playerView, 0, len(list))
	for _, p := range list {
		ready := false
		switch r.phase {
		case PhaseReveal:
			ready = p.SeenRole
		case PhaseQuestion:
			ready = r.hasAskedLocked(p.ID)
		case PhaseVote:
			ready = p.VotedFor != ""
		case PhaseDiscuss:
			ready = true
		}
		views = append(views, playerView{
			ID:        p.ID,
			Name:      p.Name,
			Connected: p.connected(),
			InRound:   p.InRound,
			Ready:     ready && p.InRound,
		})
	}

	snap := snapshot{
		Phase:      r.phase,
		Code:       r.code,
		Round:      r.round,
		MinPlayers: MinPlayers,
		MaxName:    MaxNameLen,
		HostOnline: r.hostConns > 0,
		IsHost:     isHost,
		Players:    views,
	}
	if r.phase == PhaseQuestion {
		if asker, target, ok := r.currentAskLocked(); ok {
			n := len(r.askOrder)
			a := &askView{
				Number:   r.askIdx + 1,
				Total:    n,
				AskerID:  asker.ID,
				Asker:    asker.Name,
				TargetID: target.ID,
				Target:   target.Name,
			}
			// The player being asked becomes the next asker, so "up next" is
			// simply whoever sits after them in the ring.
			if r.askIdx+1 < n {
				if nx, ok := r.players[r.askOrder[(r.askIdx+2)%n]]; ok {
					a.Next = nx.Name
				}
			}
			snap.Ask = a
		}
	}
	if r.phase == PhaseResults {
		snap.Results = r.results
	}
	if isHost {
		snap.JoinURL = r.joinURL
		return snap
	}

	p, ok := r.players[playerID]
	if !ok {
		return snap
	}
	snap.You = &youView{
		ID:       p.ID,
		Name:     p.Name,
		InRound:  p.InRound,
		SeenRole: p.SeenRole,
		VotedFor: p.VotedFor,
	}
	// The role only ever leaves the server for the one player it belongs to,
	// and only once they've tapped to reveal it.
	if p.InRound && p.SeenRole && r.phase != PhaseLobby {
		if p.IsImposter {
			snap.Role = &roleView{Imposter: true, Hint: r.topic.Hint}
		} else {
			snap.Role = &roleView{Topic: r.topic.Name}
		}
	}
	return snap
}

// ---------- reaping ----------

// Reap drops players and host screens that have been gone longer than the
// grace period. When an occupied room empties completely — every player gone
// and no host screen — the room resets and picks up a brand new code.
func (r *Room) Reap() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	changed := false

	for id, p := range r.players {
		if !p.connected() && now.Sub(p.LastSeen) > graceTTL {
			delete(r.players, id)
			r.clearVotesForLocked(id)
			r.dropFromOrderLocked(id)
			changed = true
		}
	}
	if r.hostSess != "" && r.hostConns == 0 && now.Sub(r.hostLastSeen) > graceTTL {
		r.hostSess = ""
		changed = true
	}

	if r.occupied && len(r.players) == 0 && r.hostSess == "" {
		r.code = newRoomCode()
		r.occupied = false
		r.phase = PhaseLobby
		r.round = 0
		r.results = nil
		r.imposterID = ""
		r.topic = Topic{}
		r.askOrder = nil
		r.askIdx = 0
		r.nextOrd = 0
		changed = true
	}

	if changed {
		r.maybeAdvanceLocked()
		r.publishLocked()
	}
}

// ---------- randomness ----------

func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func newRoomCode() string {
	b := make([]byte, roomCodeLength)
	for i := range b {
		b[i] = codeAlphabet[randIndex(len(codeAlphabet))]
	}
	return string(b)
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}
