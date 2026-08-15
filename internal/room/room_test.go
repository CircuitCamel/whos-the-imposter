package room

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
		p, err := r.Join(fmt.Sprintf("P%d", i), "")
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		p.Conns = 1 // stand in for an open event stream
		out = append(out, p)
	}
	return out
}

// openAndDismissAll opens and puts away every seated player's file - reveal
// doesn't hand off to questioning on opens alone, see the comment on
// maybeAdvanceLocked's PhaseReveal case.
func openAndDismissAll(t *testing.T, r *Room) {
	t.Helper()
	for _, p := range r.players {
		if err := r.MarkSeen(p.ID); err != nil {
			t.Fatalf("mark seen: %v", err)
		}
	}
	for _, p := range r.players {
		if err := r.Dismiss(p.ID); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
	}
}

// TestAskRingCoversEveryone is the property the questioning round has to hold:
// for n players there are exactly n questions, everyone asks once, everyone is
// asked once, and nobody is handed their own name.
func TestAskRingCoversEveryone(t *testing.T) {
	for n := 3; n <= 16; n++ {
		for trial := 0; trial < 200; trial++ {
			r := New(topicsForTest(), "http://test")
			seatPlayers(t, r, n)

			if err := r.StartRound(); err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
			openAndDismissAll(t, r)
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
		r := New(topicsForTest(), "http://test")
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
	r := New(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	openAndDismissAll(t, r)

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
	r := New(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	openAndDismissAll(t, r)
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

// TestRevealWaitsForEveryoneToPutAway guards against the round handing off
// to questioning the instant the last player opens their file, while
// everyone else has only opened - not put away - theirs. That would let
// whoever's already dismissed watch the ask/answer pairing form before the
// last player has even read their own role.
func TestRevealWaitsForEveryoneToPutAway(t *testing.T) {
	r := New(topicsForTest(), "http://test")
	ps := seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}

	for _, p := range ps {
		if err := r.MarkSeen(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if r.phase != PhaseReveal {
		t.Fatalf("everyone opening their file (without putting any away) should not advance the round, got %s", r.phase)
	}

	for _, p := range ps[:3] {
		if err := r.Dismiss(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if r.phase != PhaseReveal {
		t.Fatalf("one player still hasn't put their file away, expected reveal, got %s", r.phase)
	}

	if err := r.Dismiss(ps[3].ID); err != nil {
		t.Fatal(err)
	}
	if r.phase != PhaseQuestion {
		t.Fatalf("everyone has put their file away, expected question, got %s", r.phase)
	}
}

// imposterOf returns the current round's imposter, failing the test if
// there isn't exactly one.
func imposterOf(t *testing.T, r *Room) *Player {
	t.Helper()
	for _, p := range r.players {
		if p.IsImposter {
			return p
		}
	}
	t.Fatal("no imposter assigned")
	return nil
}

// advanceToVote pushes a freshly dealt round through reveal and questioning
// (if any) into voting, without caring who says what.
func advanceToVote(t *testing.T, r *Room) {
	t.Helper()
	openAndDismissAll(t, r)
	for r.phase == PhaseQuestion {
		_, target, ok := r.currentAskLocked()
		if !ok {
			t.Fatal("ring stalled")
		}
		if err := r.AnswerQuestion(target.ID); err != nil {
			t.Fatalf("answer: %v", err)
		}
	}
	if err := r.OpenVoting(); err != nil {
		t.Fatalf("open voting: %v", err)
	}
}

// TestScoringCorrectVotersAndCaughtImposter covers a unanimous vote against
// the real imposter: every innocent guessed right and should score, and the
// imposter - since nobody guessed wrong - should not.
func TestScoringCorrectVotersAndCaughtImposter(t *testing.T) {
	r := New(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	imp := imposterOf(t, r)
	advanceToVote(t, r)

	var innocents []*Player
	for _, p := range r.players {
		if p.ID != imp.ID {
			innocents = append(innocents, p)
		}
	}
	for _, v := range innocents {
		if err := r.Vote(v.ID, imp.ID); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	// The imposter votes too, same as any other player at the table - just
	// not for themselves.
	if err := r.Vote(imp.ID, innocents[0].ID); err != nil {
		t.Fatalf("imposter vote: %v", err)
	}

	if r.phase != PhaseResults {
		t.Fatalf("expected results once everyone voted, got %s", r.phase)
	}
	if !r.results.Caught {
		t.Fatal("a unanimous vote against the imposter should count as caught")
	}
	for _, v := range innocents {
		if v.Score != 1 {
			t.Errorf("%s correctly named the imposter, want score 1, got %d", v.Name, v.Score)
		}
	}
	if imp.Score != 0 {
		t.Errorf("a caught imposter should not score, got %d", imp.Score)
	}
}

// TestScoringImposterEscapes covers a split vote where the imposter isn't
// the accused: whoever still named them correctly scores, and the imposter
// scores once for every innocent who guessed wrong instead.
func TestScoringImposterEscapes(t *testing.T) {
	r := New(topicsForTest(), "http://test")
	seatPlayers(t, r, 4)
	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	imp := imposterOf(t, r)
	advanceToVote(t, r)

	var innocents []*Player
	for _, p := range r.players {
		if p.ID != imp.ID {
			innocents = append(innocents, p)
		}
	}
	// innocents[0] correctly suspects the imposter; the other two pile onto
	// a different innocent, so the imposter is never the accused.
	if err := r.Vote(innocents[0].ID, imp.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Vote(innocents[1].ID, innocents[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Vote(innocents[2].ID, innocents[0].ID); err != nil {
		t.Fatal(err)
	}
	// The imposter votes too, piling onto the same decoy as the others.
	if err := r.Vote(imp.ID, innocents[0].ID); err != nil {
		t.Fatal(err)
	}

	if r.phase != PhaseResults {
		t.Fatalf("expected results once everyone voted, got %s", r.phase)
	}
	if r.results.Caught {
		t.Fatal("the imposter wasn't the accused, so this should not count as caught")
	}
	if innocents[0].Score != 1 {
		t.Errorf("innocents[0] correctly named the imposter, want score 1, got %d", innocents[0].Score)
	}
	if innocents[1].Score != 0 || innocents[2].Score != 0 {
		t.Errorf("innocents 1 and 2 guessed wrong, want score 0, got %d and %d", innocents[1].Score, innocents[2].Score)
	}
	if imp.Score != 2 {
		t.Errorf("innocents 1 and 2 both guessed wrong, want imposter score 2, got %d", imp.Score)
	}
}

// TestBoardOrdersByScoreThenName checks the leaderboard sorts highest score
// first, ties broken alphabetically so a repeated tally doesn't reshuffle.
func TestBoardOrdersByScoreThenName(t *testing.T) {
	r := New(topicsForTest(), "http://test")
	ps := seatPlayers(t, r, 3) // P0, P1, P2
	ps[0].Score = 1
	ps[1].Score = 3
	ps[2].Score = 3

	board := r.boardLocked()
	want := []string{"P1", "P2", "P0"} // tied at 3 sort alphabetically, then P0 at 1
	if len(board) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(board))
	}
	for i, name := range want {
		if board[i].Name != name {
			t.Errorf("position %d: want %s, got %s", i, name, board[i].Name)
		}
	}
}

// TestEndGameAndNewGame covers the host's end-of-night flow: EndGame only
// works from results, NewGame only works from game-over, the leaderboard
// survives into the game-over screen, and a new game clears every score
// without unseating anyone.
func TestEndGameAndNewGame(t *testing.T) {
	r := New(topicsForTest(), "http://test")
	seatPlayers(t, r, 3)

	if err := r.EndGame(); err != ErrWrongPhase {
		t.Fatalf("EndGame from the lobby should fail with ErrWrongPhase, got %v", err)
	}

	if err := r.StartRound(); err != nil {
		t.Fatal(err)
	}
	imp := imposterOf(t, r)
	advanceToVote(t, r)
	var decoy string
	for _, p := range r.players {
		if p.ID == imp.ID {
			continue
		}
		if err := r.Vote(p.ID, imp.ID); err != nil {
			t.Fatal(err)
		}
		decoy = p.ID
	}
	// The imposter votes too, same as everyone else at the table.
	if err := r.Vote(imp.ID, decoy); err != nil {
		t.Fatal(err)
	}
	if r.phase != PhaseResults {
		t.Fatalf("expected results, got %s", r.phase)
	}

	if err := r.NewGame(); err != ErrWrongPhase {
		t.Fatalf("NewGame before EndGame should fail with ErrWrongPhase, got %v", err)
	}
	if err := r.EndGame(); err != nil {
		t.Fatalf("EndGame: %v", err)
	}
	if r.phase != PhaseGameOver {
		t.Fatalf("expected gameover, got %s", r.phase)
	}
	if r.results == nil || len(r.results.Board) != 3 {
		t.Fatalf("expected the leaderboard to survive into game-over, got %+v", r.results)
	}

	if err := r.NewGame(); err != nil {
		t.Fatalf("NewGame: %v", err)
	}
	if r.phase != PhaseLobby {
		t.Fatalf("expected lobby after a new game, got %s", r.phase)
	}
	if len(r.players) != 3 {
		t.Fatalf("a new game should keep everyone seated, got %d players", len(r.players))
	}
	for _, p := range r.players {
		if p.Score != 0 {
			t.Errorf("%s should have a clean slate for a new game, got score %d", p.Name, p.Score)
		}
	}
}
