// Command imposter runs the game server: flags and environment in, one
// *http.Server out. All the actual logic lives in the internal packages.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"imposter/internal/auth"
	"imposter/internal/config"
	"imposter/internal/room"
	"imposter/internal/server"
)

func main() {
	config.LoadDotenv(".env")

	graceDefault, err := time.ParseDuration(config.EnvOr("IMPOSTER_GRACE", "90s"))
	if err != nil {
		log.Fatalf("IMPOSTER_GRACE: %v", err)
	}

	addr := flag.String("addr", ":"+config.EnvOr("IMPOSTER_PORT", "8080"), "address to listen on")
	topicsPath := flag.String("topics", config.EnvOr("IMPOSTER_TOPICS", "topics.csv"), "path to the topics CSV (topic,hint)")
	grace := flag.Duration("grace", graceDefault, "how long a disconnected player keeps their seat")
	domain := flag.String("domain", config.EnvOr("IMPOSTER_DOMAIN", ""),
		"public address players should use to join, e.g. party.example.com — overrides the LAN IP normally printed and put in the QR code")
	minPlayers := flag.Int("min-players", config.EnvIntOr("IMPOSTER_MIN_PLAYERS", room.MinPlayers), "fewest connected players needed to deal a round")
	maxPlayersFlag := flag.Int("max-players", config.EnvIntOr("IMPOSTER_MAX_PLAYERS", room.MaxPlayers), "most players who can be seated in the room")
	accountsPath := flag.String("accounts", config.EnvOr("IMPOSTER_ACCOUNTS", "accounts.json"), "path to the host accounts file")
	trustProxy := flag.Bool("trust-proxy", config.EnvBoolOr("IMPOSTER_TRUST_PROXY", false),
		"trust the X-Forwarded-For header for rate limiting - only turn this on when something in front of this server (Cloudflare, cloudflared, nginx, Caddy) actually sets it, or a client can forge it to dodge the limits")
	flag.Parse()

	if *minPlayers < 1 {
		log.Fatalf("-min-players must be at least 1, got %d", *minPlayers)
	}
	if *maxPlayersFlag < *minPlayers {
		log.Fatalf("-max-players (%d) can't be less than -min-players (%d)", *maxPlayersFlag, *minPlayers)
	}
	room.GraceTTL = *grace
	room.MinPlayers = *minPlayers
	room.MaxPlayers = *maxPlayersFlag

	topics, err := room.LoadTopics(*topicsPath)
	if err != nil {
		log.Fatalf("could not load topics: %v", err)
	}

	accounts, err := auth.Load(*accountsPath)
	if err != nil {
		log.Fatalf("could not load host accounts: %v", err)
	}

	joinURL := publicURL(*domain)
	if joinURL == "" {
		joinURL = fmt.Sprintf("http://%s:%s", lanIP(), portOf(*addr))
	}
	rooms := room.NewManager(topics, joinURL)
	// A domain that resolves to https:// means there's a real HTTPS front
	// door (see publicURL) - cookies can be marked Secure. The default LAN
	// join URL is always http://, where that flag would just stop the
	// browser from ever sending them back.
	secureCookies := strings.HasPrefix(joinURL, "https://")
	srv := server.New(rooms, accounts, *trustProxy, secureCookies)

	// Sweep often enough that seats free up promptly once the grace period
	// is up, without spinning on a server that nobody's using.
	sweep := room.GraceTTL / 3
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
			rooms.Reap()
			srv.Sweep()
		}
	}()

	httpServer := &http.Server{
		Addr:         *addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE streams stay open
		IdleTimeout:  120 * time.Second,
	}

	announce(joinURL, len(topics))
	log.Fatal(httpServer.ListenAndServe())
}

// publicURL turns a bare domain (or a full URL) into the address printed at
// startup and encoded in the host screen's QR code. A scheme-less value is
// assumed to sit behind a reverse proxy terminating TLS, which is the usual
// reason to set this in the first place, so it defaults to https. An empty
// domain means "no override" — the caller falls back to the LAN IP.
func publicURL(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimSuffix(domain, "/")
	if domain == "" {
		return ""
	}
	if !strings.Contains(domain, "://") {
		domain = "https://" + domain
	}
	return domain
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	return "8080"
}

// announce prints what's known at startup. There's no room code to show
// yet - rooms are dealt lazily, one per host account, the moment each one
// signs in and claims the shared screen.
func announce(joinURL string, nTopics int) {
	fmt.Printf("\n  topics loaded   %d\n\n", nTopics)
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
