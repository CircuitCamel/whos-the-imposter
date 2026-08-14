package main

import (
	"fmt"
	"testing"
)

func topicsForTest() []Topic {
	return []Topic{{Name: "Beach", Hint: "Sand"}, {Name: "Casino", Hint: "Chips"}}
}

// seatPlayers joins n players and marks them all connected.
func seatPlayers(t *testing.T, r *Room, n int) []*Player {
	t.Helper()
	out := make([]*Player, 0, n)
	for i := 0; i < n; i++ {
		p, err := r.Join(r.Code(), fmt.Sprintf("P%d", i), "")
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		p.Conns = 1 // stand in for an open event stream
		out = append(out, p)
	}
	return out
}

// TestAskRingCoversEveryone is the property the questioning round has to hold:
// for n players there are exactly n questions, everyone asks once, everyone is
// asked once, and nobody is handed their own name.
func TestAskRingCoversEveryone(t *testing.T) {
	for n := 3; n <= 16; n++ {
		for trial := 0; trial < 200; trial++ {
			r := NewRoom(topicsForTest(), "http://test")
			seatPlayers(t, r, n)

			if err := r.StartRound(); err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
			for _, p := range r.players {
				if err := r.MarkSeen(p.ID); err != nil {
					t.Fatalf("n=%d mark seen: %v", n, err)
				}
			}
			if r.phase != PhaseQuestion {
				t.Fatalf("n=%d: expected question phase, got %s", n, r.phase)
			}

			asked := map[string]int{}
			answered := map[string]int{}
			for q := 0; q < n; q++ {
				asker, target, ok := r.currentAskLocked()
				if !ok {
					t.Fatalf("n=%d: ran out of questions at %d of %d", n, q+1, n)
				}
				if asker.ID == target.ID {
					t.Fatalf("n=%d: %s was asked to question themselves", n, asker.Name)
				}
				asked[asker.ID]++
				answered[target.ID]++

				// Only the player being asked may close the question out.
				if err := r.AnswerQuestion(asker.ID); err != ErrNotYourTurn {
					t.Fatalf("n=%d: asker was allowed to answer (%v)", n, err)
				}
				if err := r.AnswerQuestion(target.ID); err != nil {
					t.Fatalf("n=%d: answer: %v", n, err)
				}
			}

			if r.phase != PhaseDiscuss {
				t.Fatalf("n=%d: expected open discussion after %d questions, got %s", n, n, r.phase)
			}
			if len(asked) != n || len(answered) != n {
				t.Fatalf("n=%d: %d distinct askers, %d distinct answerers", n, len(asked), len(answered))
			}
			for id, c := range asked {
				if c != 1 {
					t.Fatalf("n=%d: %s asked %d times", n, r.players[id].Name, c)
				}
			}
			for id, c := range answered {
				if c != 1 {
					t.Fatalf("n=%d: %s was asked %d times", n, r.players[id].Name, c)
				}
			}
		}
	}
}

// TestAskRingIsShuffled guards against the ring silently degrading into join
// order, which would make the imposter's job far too easy to predict.
func TestAskRingIsShuffled(t *testing.T) {
	seen := map[string]bool{}
	for trial := 0; trial < 300; trial++ {
		r := NewRoom(topicsForTest(), "http://test")
		ps := seatPlayers(t, r, 5)
		if err := r.StartRound(); err != nil {
			t.Fatal(err)
		}
		names := ""
		for _, id := range r.askOrder {
			for _, p := range ps {
				if p.ID == id {
					names += p.Name + " "
				}
			}
		}
		seen[names] = true
	}
	if len(seen) < 10 {
		t.Fatalf("ask order barely varies: only %d distinct rings in 300 rounds", len(seen))
	}
}

// TestRingSurvivesAPlayerLeaving covers someone walking off mid-round: the
// ring should close up rather than deadlock on a player who isn't there.
func TestRingSurvivesAPlayerLeaving(t *testing.T) {
	r := NewRoom(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.players {
		if err := r.MarkSeen(p.ID); err != nil {
			t.Fatal(err)
		}
	}

	// The player due to be asked next walks out.
	_, target, ok := r.currentAskLocked()
	if !ok {
		t.Fatal("no question in progress")
	}
	r.Leave(target.ID)

	if r.phase != PhaseQuestion {
		t.Fatalf("expected the round to carry on, got %s", r.phase)
	}
	asker2, target2, ok := r.currentAskLocked()
	if !ok {
		t.Fatal("ring stalled after a player left")
	}
	if asker2.ID == target2.ID {
		t.Fatal("ring collapsed onto a single player")
	}

	// Everything still terminates in open discussion.
	for i := 0; i < 8 && r.phase == PhaseQuestion; i++ {
		_, tg, ok := r.currentAskLocked()
		if !ok {
			break
		}
		if err := r.AnswerQuestion(tg.ID); err != nil {
			t.Fatal(err)
		}
	}
	if r.phase != PhaseDiscuss {
		t.Fatalf("expected open discussion, got %s", r.phase)
	}
}

// TestFourPlayersGetFourQuestions is the example case: 4 players, 4 questions.
func TestFourPlayersGetFourQuestions(t *testing.T) {
	r := NewRoom(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.players {
		if err := r.MarkSeen(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for r.phase == PhaseQuestion {
		_, target, ok := r.currentAskLocked()
		if !ok {
			t.Fatal("ring stalled")
		}
		if err := r.AnswerQuestion(target.ID); err != nil {
			t.Fatal(err)
		}
		count++
		if count > 10 {
			t.Fatal("ring never ended")
		}
	}
	if count != 4 {
		t.Fatalf("4 players should get 4 questions, got %d", count)
	}
}
