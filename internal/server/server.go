// Package server is the HTTP layer: routes, cookies, request handlers, and
// the SSE endpoint. It knows how to talk to a room.Manager and an
// auth.Store, but none of the game or account logic lives here.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"imposter/internal/auth"
	"imposter/internal/room"
)

//go:embed static
var staticFS embed.FS

const (
	cookiePlayer     = "imp_pid"    // which seat this browser holds
	cookiePlayerRoom = "imp_room"   // which room that seat is in
	cookieHost       = "imp_host"   // this browser's per-room "I'm driving" session
	cookieHostRoom   = "imp_hroom"  // which room that session belongs to
	cookieAuth       = "imp_auth"   // the signed-in host account
	cookieMaxAge     = 12 * 60 * 60 // 12 hours - long enough for a game night

	// The host login is meant to be a "sign in once" thing, not something
	// that boots you mid-game-night - so its cookie is set to last for
	// years rather than hours. There's no server-side expiry to match; a
	// session is good until SignOut or a server restart clears it.
	authCookieMaxAge = 10 * 365 * 24 * 60 * 60
)

type Server struct {
	rooms *room.Manager
	auth  *auth.Store
}

func New(rooms *room.Manager, authStore *auth.Store) *Server {
	return &Server{rooms: rooms, auth: authStore}
}

// roomHandler is like http.HandlerFunc, but for routes that only make sense
// once a specific room has been resolved - forPlayer and forHost adapt one
// of these into a real http.HandlerFunc after doing that resolution.
type roomHandler func(w http.ResponseWriter, r *http.Request, rm *room.Room)

// Routes builds the full set of HTTP routes, ready to hand to an
// *http.Server as its Handler.
func (s *Server) Routes() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", files))
	mux.HandleFunc("GET /", s.page("static/index.html"))
	mux.HandleFunc("GET /host", s.page("static/host.html"))

	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/join", s.handleJoin)
	mux.HandleFunc("POST /api/leave", s.handleLeave)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/reveal", s.forPlayer(s.handleReveal))
	mux.HandleFunc("POST /api/putaway", s.forPlayer(s.handlePutAway))
	mux.HandleFunc("POST /api/answered", s.forPlayer(s.handleAnswered))
	mux.HandleFunc("POST /api/vote", s.forPlayer(s.handleVote))

	mux.HandleFunc("POST /api/auth/signup", s.handleSignUp)
	mux.HandleFunc("POST /api/auth/signin", s.handleSignIn)
	mux.HandleFunc("POST /api/auth/signout", s.handleSignOut)

	mux.HandleFunc("POST /api/host/claim", s.handleHostClaim)
	mux.HandleFunc("POST /api/host/start", s.forHost(s.handleHostStart))
	mux.HandleFunc("POST /api/host/nextq", s.forHost(s.handleHostNextQuestion))
	mux.HandleFunc("POST /api/host/discuss", s.forHost(s.handleHostDiscuss))
	mux.HandleFunc("POST /api/host/voting", s.forHost(s.handleHostVoting))
	mux.HandleFunc("POST /api/host/close", s.forHost(s.handleHostClose))
	mux.HandleFunc("POST /api/host/lobby", s.forHost(s.handleHostLobby))
	mux.HandleFunc("POST /api/host/kick", s.forHost(s.handleHostKick))
	mux.HandleFunc("POST /api/host/end", s.forHost(s.handleHostEnd))
	mux.HandleFunc("POST /api/host/newgame", s.forHost(s.handleHostNewGame))

	return mux
}

// ---------- helpers ----------

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func cookieVal(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func setCookie(w http.ResponseWriter, name, val string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// forPlayer resolves the room named by this browser's player-room cookie.
// A missing or stale cookie (the room got reaped, or this is a fresh
// browser) reads the same as "not in this room" everywhere else.
func (s *Server) forPlayer(next roomHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rm, ok := s.rooms.Lookup(cookieVal(r, cookiePlayerRoom))
		if !ok {
			writeErr(w, http.StatusUnauthorized, errors.New("not in this room"))
			return
		}
		next(w, r, rm)
	}
}

// forHost resolves the room named by this browser's host-room cookie and
// checks it's still signed in and still the tab actively driving that room
// - both independently meaningful: signing out should stop you from
// driving a room even if these cookies are still sitting in the browser,
// and a second tab shouldn't be able to drive a room it didn't claim.
func (s *Server) forHost(next roomHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.IsSignedIn(cookieVal(r, cookieAuth)) {
			writeErr(w, http.StatusUnauthorized, errors.New("sign in required"))
			return
		}
		rm, ok := s.rooms.Lookup(cookieVal(r, cookieHostRoom))
		if !ok || !rm.IsHost(cookieVal(r, cookieHost)) {
			writeErr(w, http.StatusForbidden, errors.New("only the host screen can do that"))
			return
		}
		next(w, r, rm)
	}
}

// ---------- player endpoints ----------

// handleMe lets a returning phone find out whether its cookies are still
// good, so it can skip the join form entirely after a disconnect.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if rm, ok := s.rooms.Lookup(cookieVal(r, cookiePlayerRoom)); ok {
		if p, ok := rm.Lookup(cookieVal(r, cookiePlayer)); ok {
			writeJSON(w, http.StatusOK, map[string]any{"joined": true, "id": p.ID, "name": p.Name})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"joined": false})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}

	rm, ok := s.rooms.Lookup(body.Code)
	if !ok {
		writeErr(w, http.StatusUnauthorized, room.ErrBadCode)
		return
	}

	p, err := rm.Join(body.Name, cookieVal(r, cookiePlayer))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	setCookie(w, cookiePlayer, p.ID, cookieMaxAge)
	setCookie(w, cookiePlayerRoom, rm.Code(), cookieMaxAge)
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "name": p.Name})
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	if rm, ok := s.rooms.Lookup(cookieVal(r, cookiePlayerRoom)); ok {
		rm.Leave(cookieVal(r, cookiePlayer))
	}
	setCookie(w, cookiePlayer, "", -1)
	setCookie(w, cookiePlayerRoom, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.MarkSeen(cookieVal(r, cookiePlayer)); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePutAway(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.Dismiss(cookieVal(r, cookiePlayer)); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAnswered(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.AnswerQuestion(cookieVal(r, cookiePlayer)); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}
	if err := rm.Vote(cookieVal(r, cookiePlayer), body.Target); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- auth endpoints ----------

func decodeAuthBody(r *http.Request) (email, password string, err error) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return "", "", errors.New("bad request")
	}
	return body.Email, body.Password, nil
}

// handleSignUp creates the first host account for free (there's no other
// way in), then locks itself to signed-in callers only - otherwise anyone
// who found this endpoint on a public domain could register themselves as
// a host. A signed-in host can still use it to add e.g. a co-host.
func (s *Server) handleSignUp(w http.ResponseWriter, r *http.Request) {
	bootstrapping := s.auth.Empty()
	if !bootstrapping && !s.auth.IsSignedIn(cookieVal(r, cookieAuth)) {
		writeErr(w, http.StatusForbidden, auth.ErrSignupsClosed)
		return
	}

	email, password, err := decodeAuthBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.auth.SignUp(email, password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	// Only the very first sign-up doubles as a sign-in - an already signed-in
	// host creating another account shouldn't have their own session swapped.
	if bootstrapping {
		token, err := s.auth.SignIn(email, password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		setCookie(w, cookieAuth, token, authCookieMaxAge)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	email, password, err := decodeAuthBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	token, err := s.auth.SignIn(email, password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	setCookie(w, cookieAuth, token, authCookieMaxAge)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s.auth.SignOut(cookieVal(r, cookieAuth))
	setCookie(w, cookieAuth, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- host endpoints ----------

// handleHostClaim is the one host endpoint that doesn't go through forHost:
// it's what resolves which room an account owns in the first place (dealing
// a new one on a first claim), so there's nothing to look up yet.
func (s *Server) handleHostClaim(w http.ResponseWriter, r *http.Request) {
	email, ok := s.auth.Email(cookieVal(r, cookieAuth))
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("sign in required"))
		return
	}
	rm, sess, err := s.rooms.ClaimHost(email, cookieVal(r, cookieHost))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	setCookie(w, cookieHost, sess, cookieMaxAge)
	setCookie(w, cookieHostRoom, rm.Code(), cookieMaxAge)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": rm.Code()})
}

func (s *Server) handleHostStart(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.StartRound(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostNextQuestion(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.NextQuestion(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostDiscuss(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.ToDiscussion(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostVoting(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.OpenVoting(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostClose(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.CloseVoting(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostLobby(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	rm.ToLobby()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostEnd(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.EndGame(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostNewGame(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	if err := rm.NewGame(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostKick(w http.ResponseWriter, r *http.Request, rm *room.Room) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}
	rm.Kick(body.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- SSE ----------

// roomForRequest resolves which room an SSE connection belongs to. A
// signed-in account with a live host claim on some room wins - matching a
// browser that's driving room A in one tab and, in another, sitting in
// room B as an ordinary player; otherwise it falls back to the player's
// own room cookie.
func (s *Server) roomForRequest(r *http.Request) (rm *room.Room, isHost bool) {
	if s.auth.IsSignedIn(cookieVal(r, cookieAuth)) {
		if hostRoom, ok := s.rooms.Lookup(cookieVal(r, cookieHostRoom)); ok && hostRoom.IsHost(cookieVal(r, cookieHost)) {
			return hostRoom, true
		}
	}
	playerRoom, ok := s.rooms.Lookup(cookieVal(r, cookiePlayerRoom))
	if !ok {
		return nil, false
	}
	return playerRoom, false
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	rm, isHost := s.roomForRequest(r)
	pid := cookieVal(r, cookiePlayer)
	if rm == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("not in this room"))
		return
	}
	if !isHost {
		if _, ok := rm.Lookup(pid); !ok {
			// Cookie is stale or the player was reaped - the page will fall
			// back to the join form.
			writeErr(w, http.StatusUnauthorized, errors.New("not in this room"))
			return
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sub := rm.Subscribe(pid, isHost)
	defer rm.Unsubscribe(sub)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-sub.Ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
