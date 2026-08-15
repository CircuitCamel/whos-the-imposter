# Imposter

A topic-imposter party game for a room full of phones. Everyone gets the same
topic except one player, who gets a single hint word and has to bluff.

Go backend. Stdlib only, apart from `golang.org/x/crypto/bcrypt` for the host
account passwords.

## Run it

```sh
go build -o imposter ./cmd/imposter
./imposter
```

It prints the two URLs on startup:

```
  topics loaded   50

  players join    http://192.168.1.42:8080
  shared screen   http://192.168.1.42:8080/host
```

There's no room code yet at this point - rooms are dealt lazily, one per
host account, the moment each one signs in and claims the shared screen (see
below). Open `/host` on a laptop or TV, sign in, and the room code appears
there. Everyone else opens the join URL on their phone and types that code.
You can play too - open the join URL on your own phone alongside the host
screen.

### Who can run the shared screen

`/host` is behind a sign-in, so a stranger who finds the URL can't hijack
your game. The **first** visit to `/host` gets to create a host account
(email + password); after that, sign-up closes itself to unauthenticated
visitors, and only an already-signed-in host can create another one (there's
an "Add another host" control on the shared screen once you're in).

Passwords are hashed with bcrypt - never stored or logged in the clear.
Accounts live in `accounts.json` next to the binary (path configurable, see
`-accounts` below), so they survive a restart; sessions don't, and last
until you sign out or the server restarts. The session cookie is a 32-character
random value, not a UUID, generated the same way player and room IDs are.

### One room per host account

Each host account gets its own room, so different people (say, you and a
family member) can each run their own game night off the same server at the
same time, without seeing or interfering with each other's rooms. A room is
created the moment its account first claims the shared screen, and reclaiming
later (a reload, a second device) always lands back on that same room rather
than dealing a new one - right up until it's completely empty (every player
gone, host screen closed, both past `-grace`), at which point it's freed
entirely: the code goes back into circulation and the next claim starts
completely fresh, scores included.

`internal/room.Manager` is what this actually is: rooms keyed by join code
for O(1) player lookups, and by owner account so a host always resolves back
to their own room.

### Putting this on the internet

This is built for a LAN game night, not for the public web, but nothing
stops you running it behind a real domain (see `-domain` and the Cloudflare
notes above) so people can join remotely. It isn't hardened against a
determined attacker - there's no CAPTCHA, no WAF, no account lockouts - but
it's meant to survive someone stumbling onto the URL, a search engine
crawling it, or a script idly trying room codes:

- **`/api/join` and `/api/auth/signin` are rate-limited per IP** - a small
  token bucket (`internal/server/ratelimit.go`) generous enough for a whole
  table joining at once or a fumbled password, but a script trying code
  after code gets a `429` within seconds. A 4-character room code is only
  ~390,000 combinations; at the throttled rate that's weeks, not minutes.
- **`-trust-proxy`** decides whether that limiter keys off the real TCP
  connection or `X-Forwarded-For`. It defaults to off - trusting that header
  with nothing in front of this server actually setting it would let a
  client pick a fresh fake IP on every request and walk straight through the
  limit. Turn it on only when something in front of it (Cloudflare,
  `cloudflared`, nginx, Caddy) is genuinely the one setting it.
- **Every request body is capped at 8 KiB** (`http.MaxBytesReader`) before
  it's ever decoded - nothing this app sends is anywhere close to that.
- **Standard response headers** on everything: `X-Content-Type-Options:
  nosniff`, `X-Frame-Options: DENY`, and `Referrer-Policy: same-origin` (the
  last one stops a room code sitting in the URL from leaking to the Google
  Fonts request every page makes, via the `Referer` header).
- **`/robots.txt` disallows everything** - a live game's URLs have no
  business in a search index.
- **Cookies pick up `Secure` automatically** once `-domain` resolves to
  `https://`; they stay plain over the default LAN `http://` setup, where a
  `Secure` cookie would just never be sent back.
- **CSRF** isn't handled with tokens because it doesn't need to be: every
  mutating endpoint is `POST`, and every cookie is `SameSite=Lax`, which
  browsers already refuse to attach to a cross-site `POST`.

Flags:

| flag | default | what it does |
|---|---|---|
| `-addr` | `:8080` | listen address |
| `-topics` | `topics.csv` | path to the topic list |
| `-grace` | `90s` | how long a disconnected player keeps their seat |
| `-domain` | *(LAN IP)* | public address for the join URL and QR code, e.g. `party.example.com` - set this if the game's behind a domain instead of joined by LAN IP |
| `-min-players` | `2` | fewest connected players needed to deal a round |
| `-max-players` | `16` | most players who can be seated in the room |
| `-accounts` | `accounts.json` | path to the host accounts file |
| `-trust-proxy` | `false` | trust `X-Forwarded-For` for rate limiting - only when actually behind a reverse proxy that sets it |

Every flag also has an environment variable (`IMPOSTER_PORT`, `IMPOSTER_DOMAIN`,
`IMPOSTER_TOPICS`, `IMPOSTER_GRACE`, `IMPOSTER_MIN_PLAYERS`,
`IMPOSTER_MAX_PLAYERS`, `IMPOSTER_ACCOUNTS`, `IMPOSTER_TRUST_PROXY`), and the
server reads a `.env` file in the working directory on startup if one exists -
copy `.env.example` to `.env` to use one. Precedence is flag > real
environment variable > `.env` > built-in default.

## Testing

```sh
go test ./...   # ring properties over ~2,800 random rounds, plus auth, scoring, multi-room, and rate limiting
./smoke.sh      # end-to-end HTTP run: two host accounts + 5 players, 61 assertions
```

## Topics

`topics.csv` is two columns - the topic everyone sees, and the hint word the
imposter gets instead. A `topic,hint` header row is optional.

```csv
topic,hint
Military Base,Orders
Ski Resort,Cold
```

Rows missing either column are skipped rather than crashing the server, so a
trailing newline is fine. The file is read once at startup - restart to pick up
edits.

## How a round goes

1. **Lobby** - players join. Three minimum.
2. **Reveal** - host deals. Each phone shows a sealed file; tap to open it.
   Your card stays on screen until *you* put it away, so nobody gets rushed.
3. **Questions** - one structured question per player (see below).
4. **Discussion** - the floor opens. "Check my file" re-shows your card if you
   forget. The host opens voting when the room's ready.
5. **Voting** - everyone picks someone. No self-votes. Auto-closes once all
   votes are in, or the host can close it early.
6. **Results** - topic, imposter, and the tally. A tie lets the imposter walk.

Join mid-round and you're seated but sitting out until the next deal.

## The questioning round

With *n* players there are exactly *n* questions: everyone asks once, everyone
is asked once, and nobody gets handed their own name.

That's implemented as a single random **cycle** rather than a random pairing.
The players are shuffled and read as a ring - each asks the next one along, and
the last asks the first. Reading a shuffled list as a ring gives a uniformly
random cyclic permutation, which has the properties you asked for, plus one
more that matters at a real table: **whoever just answered is the next to
ask**, so play passes naturally instead of jumping around the room.

A plain random pairing would also satisfy "everyone asks once, everyone is
asked once" - but it can split into separate loops (with four players you
could get A↔B and C↔D), which stalls at the table because nothing says who
goes next. A single ring can't do that.

The player being asked taps **I've answered - my turn to ask** to move things
on. The host can also force the next question, or open discussion early, for
when a phone dies mid-answer. If someone leaves partway through, the ring
closes up around the gap rather than deadlocking.

After the last question it drops into open discussion on its own.

## The bits you asked about

**Room code** - four characters, generated at startup, ambiguous glyphs
(`O/0`, `I/1`) left out. It stays put for the whole session and only rerolls
once the room is genuinely empty: every player gone *and* the host screen
closed, both past the grace period.

**Names** - 1 to 16 characters, counted in runes so emoji and Arabic don't get
cut short. Duplicates are rejected case-insensitively. Rejoining with the same
cookie and a different name renames you instead of taking a second seat.

**Reconnecting** - an `HttpOnly` cookie holds your player ID. Lock your phone,
reload, or drop off WiFi and you land back in the same seat with the same role.
The seat is held for `-grace` (90s default) before it's released.

## How it's put together

Standard Go project layout: a thin `cmd/` entrypoint, everything else as
`internal/` packages so nothing here is importable from outside the module.

```
cmd/imposter/main.go           flags, env, startup banner, wiring
internal/server/                routes, cookies, SSE endpoint, request handlers
internal/server/ratelimit.go    per-IP token bucket for join/sign-in attempts
internal/server/static/         the frontend, embedded into the binary
internal/room/                  game state, phase machine, ask ring, topics loader
internal/room/manager.go        one Room per host account, keyed by code and by owner
internal/auth/                  host accounts and sessions (bcrypt, no database)
internal/token/                 random ID generator shared by room and auth
internal/config/                .env loading, environment variable helpers
smoke.sh                        end-to-end HTTP test over a full round
```

Each internal package owns its own `_test.go` files alongside it
(`internal/room/room_test.go`, `internal/room/manager_test.go`,
`internal/auth/auth_test.go`, `internal/server/ratelimit_test.go`).

Pushes go out over **Server-Sent Events** rather than WebSockets. Everything
here is one-way server-to-client, actions are ordinary `POST`s, and SSE gives
you automatic browser reconnection for free - which is exactly the behaviour
you want when a phone sleeps mid-round. If you later want the phones to push a
stream of their own (live reactions, a drawing round), swap in
`gorilla/websocket`; the hub in `internal/room` is already shaped for it.

**Roles never leave the server for the wrong phone.** Each open stream gets its
own snapshot built for that player. The topic isn't in your payload until
you've tapped to open your file, the imposter's payload has no topic field at
all, and the host screen never receives either until the round is over. Nothing
is hidden with CSS - it isn't in the response.

Each room is one struct behind its own mutex, same as always; the Manager
that owns all of them is just a pair of maps behind a second mutex on top.
No database anywhere in the stack; a restart is a clean slate for every
room, which is the right behaviour for a game night.

The frontend is compiled in with `go:embed`, so **rebuild after editing
anything in `internal/server/static/`** or you'll keep serving the old copy.

## What the tests cover

`internal/room/room_test.go` hammers the ring: for every player count from 3
to 16, 200 random rounds each, checking that the question count matches the
player count, that everyone asks and is asked exactly once, that nobody
self-asks, that only the player being asked can close a question, and that
the ring survives someone walking out mid-round. It also checks the order
actually varies, so it can't quietly degrade into join order, plus the
scoring and reveal-gating behaviour described above.

`internal/room/manager_test.go` covers the multi-room behaviour directly:
two accounts get two independently-addressable rooms, reclaiming with a live
session returns the same room instead of a new one, code lookup is
case-insensitive, and reaping an abandoned room frees both its code and its
owner's slot for a genuinely fresh room next time.

`internal/server/ratelimit_test.go` covers the token bucket directly: burst
then block, refilling over time, IPs tracked independently, and the sweep
only ever dropping entries that have actually gone idle rather than ones
that just look "full" (which a drained-and-abandoned bucket never
naturally becomes again on its own).

`smoke.sh` runs the real thing over HTTP on port 8099 - a host and three
players with their own cookie jars and live SSE streams - covering name limits,
wrong codes, duplicate names, role secrecy, the full ring, self-vote rejection,
auto-advance, cookie reconnection, a room's full lifecycle (dealt on claim,
holds its code while occupied, freed once fully empty), a second host
account running a second room at the same time without the two seeing each
other, the host account/session flow (bootstrap sign-up, sign-up closing
itself, wrong passwords, and both layers of the host-endpoint gate), and
flooding both `/api/join` and `/api/auth/signin` until a `429` shows up.

`internal/auth/auth_test.go` covers the account `Store` directly: sign-up
validation, that a wrong password and an unknown email fail the same way,
sign-out actually invalidating a session, that passwords never hit disk in
the clear, and that accounts survive a reload.

## Ideas for later

- Let the imposter guess the topic at the reveal to steal the win
- A second questioning lap before voting, reshuffling the ring
- A round timer on the shared screen
- Topic packs - a folder of CSVs and a picker on the host screen
