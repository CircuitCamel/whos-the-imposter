package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"
)

//go:embed static
var staticFS embed.FS

const (
	cookiePlayer = "imp_pid"
	cookieHost   = "imp_host"
	cookieMaxAge = 12 * 60 * 60 // 12 hours — long enough for a game night
)

type Server struct {
	room *Room
}

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	topicsPath := flag.String("topics", "topics.csv", "path to the topics CSV (topic,hint)")
	grace := flag.Duration("grace", 90*time.Second, "how long a disconnected player keeps their seat")
	flag.Parse()

	graceTTL = *grace

	topics, err := loadTopics(*topicsPath)
	if err != nil {
		log.Fatalf("could not load topics: %v", err)
	}

	joinURL := fmt.Sprintf("http://%s:%s", lanIP(), portOf(*addr))
	s := &Server{room: NewRoom(topics, joinURL)}

	// Sweep often enough that seats free up promptly once the grace period
	// is up, without spinning on a room that nobody's in.
	sweep := graceTTL / 3
	if sweep > 10*time.Second {
		sweep = 10 * time.Second
	}
	if sweep < 250*time.Millisecond {
		sweep = 250 * time.Millisecond
	}
	go func() {
		t := time.NewTicker(sweep)
		defer t.Stop()
		for range t.C {
			s.room.Reap()
		}
	}()

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
	mux.HandleFunc("POST /api/reveal", s.handleReveal)
	mux.HandleFunc("POST /api/answered", s.handleAnswered)
	mux.HandleFunc("POST /api/vote", s.handleVote)

	mux.HandleFunc("POST /api/host/claim", s.handleHostClaim)
	mux.HandleFunc("POST /api/host/start", s.hostOnly(s.handleHostStart))
	mux.HandleFunc("POST /api/host/nextq", s.hostOnly(s.handleHostNextQuestion))
	mux.HandleFunc("POST /api/host/discuss", s.hostOnly(s.handleHostDiscuss))
	mux.HandleFunc("POST /api/host/voting", s.hostOnly(s.handleHostVoting))
	mux.HandleFunc("POST /api/host/close", s.hostOnly(s.handleHostClose))
	mux.HandleFunc("POST /api/host/lobby", s.hostOnly(s.handleHostLobby))
	mux.HandleFunc("POST /api/host/kick", s.hostOnly(s.handleHostKick))

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE streams stay open
		IdleTimeout:  120 * time.Second,
	}

	announce(joinURL, s.room.Code(), len(topics))
	log.Fatal(srv.ListenAndServe())
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	return "8080"
}

func announce(joinURL, code string, nTopics int) {
	fmt.Printf("\n  topics loaded   %d\n", nTopics)
	fmt.Printf("  room code       %s\n\n", code)
	fmt.Printf("  players join    %s\n", joinURL)
	fmt.Printf("  shared screen   %s/host\n\n", joinURL)
}

func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() {
			continue
		}
		if ip4 := n.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "localhost"
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

func (s *Server) hostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.room.IsHost(cookieVal(r, cookieHost)) {
			writeErr(w, http.StatusForbidden, errors.New("only the host screen can do that"))
			return
		}
		next(w, r)
	}
}

// ---------- player endpoints ----------

// handleMe lets a returning phone find out whether its cookie is still good,
// so it can skip the join form entirely after a disconnect.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.room.Lookup(cookieVal(r, cookiePlayer)); ok {
		writeJSON(w, http.StatusOK, map[string]any{"joined": true, "id": p.ID, "name": p.Name})
		return
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

	p, err := s.room.Join(body.Code, body.Name, cookieVal(r, cookiePlayer))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrBadCode) {
			status = http.StatusUnauthorized
		}
		writeErr(w, status, err)
		return
	}

	setCookie(w, cookiePlayer, p.ID, cookieMaxAge)
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "name": p.Name})
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	s.room.Leave(cookieVal(r, cookiePlayer))
	setCookie(w, cookiePlayer, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	if err := s.room.MarkSeen(cookieVal(r, cookiePlayer)); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAnswered(w http.ResponseWriter, r *http.Request) {
	if err := s.room.AnswerQuestion(cookieVal(r, cookiePlayer)); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}
	if err := s.room.Vote(cookieVal(r, cookiePlayer), body.Target); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- host endpoints ----------

func (s *Server) handleHostClaim(w http.ResponseWriter, r *http.Request) {
	sess, err := s.room.ClaimHost(cookieVal(r, cookieHost))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	setCookie(w, cookieHost, sess, cookieMaxAge)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": s.room.Code()})
}

func (s *Server) handleHostStart(w http.ResponseWriter, r *http.Request) {
	if err := s.room.StartRound(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostNextQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.room.NextQuestion(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostDiscuss(w http.ResponseWriter, r *http.Request) {
	if err := s.room.ToDiscussion(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostVoting(w http.ResponseWriter, r *http.Request) {
	if err := s.room.OpenVoting(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostClose(w http.ResponseWriter, r *http.Request) {
	if err := s.room.CloseVoting(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostLobby(w http.ResponseWriter, r *http.Request) {
	s.room.ToLobby()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHostKick(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}
	s.room.Kick(body.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- SSE ----------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	isHost := s.room.IsHost(cookieVal(r, cookieHost))
	pid := cookieVal(r, cookiePlayer)
	if !isHost {
		if _, ok := s.room.Lookup(pid); !ok {
			// Cookie is stale or the player was reaped — the page will fall
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

	sub := s.room.Subscribe(pid, isHost)
	defer s.room.Unsubscribe(sub)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-sub.ch:
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
