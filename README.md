# Imposter

A topic-imposter party game for a room full of phones. Everyone gets the same
topic except one player, who gets a single hint word and has to bluff.

Go backend, no dependencies outside the standard library.

## Run it

```sh
go build -o imposter .
./imposter
```

It prints the room code and the two URLs on startup:

```
  topics loaded   50
  room code       KVQM

  players join    http://192.168.1.42:8080
  shared screen   http://192.168.1.42:8080/host
```

Open the shared screen on a laptop or TV, everyone else opens the join URL on
their phone and types the code. You can play too - open the join URL on your
own phone alongside the host screen.

Flags:

| flag | default | what it does |
|---|---|---|
| `-addr` | `:8080` | listen address |
| `-topics` | `topics.csv` | path to the topic list |
| `-grace` | `90s` | how long a disconnected player keeps their seat |
| `-domain` | *(LAN IP)* | public address for the join URL and QR code, e.g. `party.example.com` - set this if the game's behind a domain instead of joined by LAN IP |
| `-min-players` | `2` | fewest connected players needed to deal a round |
| `-max-players` | `16` | most players who can be seated in the room |

Every flag also has an environment variable (`IMPOSTER_PORT`, `IMPOSTER_DOMAIN`,
`IMPOSTER_TOPICS`, `IMPOSTER_GRACE`, `IMPOSTER_MIN_PLAYERS`,
`IMPOSTER_MAX_PLAYERS`), and the server reads a `.env` file in the working
directory on startup if one exists - copy `.env.example` to `.env` to use one.
Precedence is flag > real environment variable > `.env` > built-in default.

## Testing

```sh
go test ./...   # ring properties over ~2,800 random rounds, 3-16 players
./smoke.sh      # end-to-end HTTP run: host + 3 players, 45 assertions
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

```
main.go      routes, cookies, SSE endpoint, startup banner
room.go      game state, phase machine, ask ring, per-player snapshots
topics.go    CSV loader
room_test.go property tests for the questioning ring
static/      the frontend, embedded into the binary
smoke.sh     end-to-end HTTP test over a full round
```

Pushes go out over **Server-Sent Events** rather than WebSockets. Everything
here is one-way server-to-client, actions are ordinary `POST`s, and SSE gives
you automatic browser reconnection for free - which is exactly the behaviour
you want when a phone sleeps mid-round. It's also all stdlib, so there's no
module fetch and no `go.sum`. If you later want the phones to push a stream of
their own (live reactions, a drawing round), swap in `gorilla/websocket`; the
hub in `room.go` is already shaped for it.

**Roles never leave the server for the wrong phone.** Each open stream gets its
own snapshot built for that player. The topic isn't in your payload until
you've tapped to open your file, the imposter's payload has no topic field at
all, and the host screen never receives either until the round is over. Nothing
is hidden with CSS - it isn't in the response.

State is one struct behind a mutex. No database; a restart is a fresh room,
which is the right behaviour for a game night.

The frontend is compiled in with `go:embed`, so **rebuild after editing
anything in `static/`** or you'll keep serving the old copy.

## What the tests cover

`room_test.go` hammers the ring: for every player count from 3 to 16, 200
random rounds each, checking that the question count matches the player count,
that everyone asks and is asked exactly once, that nobody self-asks, that only
the player being asked can close a question, and that the ring survives someone
walking out mid-round. It also checks the order actually varies, so it can't
quietly degrade into join order.

`smoke.sh` runs the real thing over HTTP on port 8099 - a host and three
players with their own cookie jars and live SSE streams - covering name limits,
wrong codes, duplicate names, role secrecy, the full ring, self-vote rejection,
auto-advance, cookie reconnection, and the room-code reroll.

## Ideas for later

- Let the imposter guess the topic at the reveal to steal the win
- A second questioning lap before voting, reshuffling the ring
- Multiple rooms (the `Room` struct is already self-contained - key a map by
  code and look it up per request)
- A round timer on the shared screen
- Per-player scoring across a night
- Topic packs - a folder of CSVs and a picker on the host screen
