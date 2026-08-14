#!/usr/bin/env bash
# End-to-end smoke test: host + 3 players through a full round.
set -uo pipefail
cd "$(dirname "$0")"
rm -rf /tmp/imp && mkdir -p /tmp/imp
PORT=8099
BASE="http://127.0.0.1:$PORT"
PASS=0; FAIL=0

ok()   { PASS=$((PASS+1)); echo "  ok    $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL  $1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want '$3', got '$2')"; fi; }
has()  { if grep -q "$2" <<<"$1"; then ok "$3"; else bad "$3 -- in: $1"; fi; }

./bin/imposter -addr ":$PORT" -topics topics.csv -grace 2s > /tmp/imp/server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; pkill -P $$ curl 2>/dev/null; exit' EXIT
sleep 0.6

CODE=$(awk '/room code/{print $3}' /tmp/imp/server.log)
echo "room code: $CODE"

j()  { curl -s -b /tmp/imp/$1.jar -c /tmp/imp/$1.jar "${@:2}"; }
post(){ j "$1" -X POST -H 'Content-Type: application/json' -d "$3" "$BASE$2"; }
sse() { curl -s -N -b /tmp/imp/$1.jar "$BASE/api/events" > /tmp/imp/$1.sse & }
last(){ tr -d '\r' < /tmp/imp/$1.sse | grep '^data: ' | tail -1 | cut -c7-; }
field(){ python3 -c "import json,sys;d=json.loads(sys.stdin.read() or '{}');print(json.dumps(eval('d'+sys.argv[1]),default=str) if 1 else '')" "$1" 2>/dev/null; }

echo; echo "-- validation"
r=$(post h "/api/join" "{\"code\":\"ZZZZ\",\"name\":\"Nope\"}");     has "$r" "wrong room code" "wrong code rejected"
r=$(post h "/api/join" "{\"code\":\"$CODE\",\"name\":\"\"}");         has "$r" "1-16" "empty name rejected"
r=$(post h "/api/join" "{\"code\":\"$CODE\",\"name\":\"abcdefghijklmnopq\"}"); has "$r" "1-16" "17-char name rejected"
r=$(post h "/api/join" "{\"code\":\"$CODE\",\"name\":\"abcdefghijklmnop\"}");  has "$r" "\"name\"" "16-char name accepted"
r=$(post h2 "/api/join" "{\"code\":\"$CODE\",\"name\":\"ABCDEFGHIJKLMNOP\"}"); has "$r" "already has that name" "duplicate name rejected"

echo; echo "-- host + players"
r=$(post host "/api/host/claim" '{}'); has "$r" '"ok":true' "host claimed the screen"
sse host
for i in 1 2 3; do
  post p$i "/api/join" "{\"code\":\"$CODE\",\"name\":\"P$i\"}" >/dev/null
  sse p$i
done
sleep 0.5
snap=$(last host)
n=$(python3 -c "import json,sys;print(len(json.loads(sys.argv[1])['players']))" "$snap")
check "host sees all seated players" "$n" "4"

echo; echo "-- non-host cannot drive the game"
r=$(post p1 "/api/host/start" '{}'); has "$r" "only the host" "player blocked from starting"

echo; echo "-- deal a round"
post host "/api/host/start" '{}' >/dev/null
sleep 0.4
check "phase is reveal" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$(last p1)")" "reveal"

for i in 1 2 3; do
  s=$(last p$i)
  hasrole=$(python3 -c "import json,sys;print('role' in json.loads(sys.argv[1]))" "$s")
  check "P$i has no role before opening the file" "$hasrole" "False"
done

echo; echo "-- open the files"
for i in 1 2 3; do post p$i "/api/reveal" '{}' >/dev/null; done
sleep 0.5
check "phase auto-advances to the questioning round" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$(last p1)")" "question"

imp=0; topics=0
for i in 1 2 3 4; do
  [ $i -eq 4 ] && break
  s=$(last p$i)
  isimp=$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['role']['imposter'])" "$s")
  if [ "$isimp" = "True" ]; then imp=$i; else topics=$((topics+1)); fi
done
check "exactly one imposter" "$imp" "$imp"
check "the other two got the topic" "$topics" "2"
[ "$imp" != "0" ] && ok "imposter assigned (P$imp)" || bad "no imposter assigned"

s=$(last p$imp)
has "$s" '"hint"' "imposter got a hint word"
if python3 -c "import json,sys;d=json.loads(sys.argv[1]);exit(0 if 'topic' not in d['role'] else 1)" "$s"; then
  ok "imposter did NOT get the topic"; else bad "imposter leaked the topic"; fi

hs=$(last host)
if python3 -c "import json,sys;d=json.loads(sys.argv[1]);exit(0 if 'role' not in d and 'results' not in d else 1)" "$hs"; then
  ok "host screen never sees the topic mid-round"; else bad "host screen leaked the round"; fi

echo; echo "-- questioning ring"
jarfor(){ case "$1" in P1) echo p1;; P2) echo p2;; P3) echo p3;; esac; }
ask(){ python3 -c "import json,sys;a=json.loads(sys.argv[1]).get('ask');print(a[sys.argv[2]] if a else '')" "$(last host)" "$1"; }

check "three questions for three players" "$(ask total)" "3"
ASKERS=""; TARGETS=""
for n in 1 2 3; do
  A=$(ask asker); T=$(ask target); NUM=$(ask number)
  check "question $n is numbered $n" "$NUM" "$n"
  [ "$A" = "$T" ] && bad "q$n: $A was asked to question themselves" || ok "q$n: $A asks $T"
  ASKERS="$ASKERS $A"; TARGETS="$TARGETS $T"

  if [ $n -eq 1 ]; then
    r=$(post "$(jarfor "$A")" "/api/answered" '{}')
    has "$r" "isn't your turn" "the asker cannot answer their own question"
  fi
  post "$(jarfor "$T")" "/api/answered" '{}' >/dev/null
  sleep 0.35
done

u_ask=$(tr " " "\n" <<<"$ASKERS" | grep -c . ); d_ask=$(tr " " "\n" <<<"$ASKERS" | grep . | sort -u | wc -l)
u_tgt=$(tr " " "\n" <<<"$TARGETS" | grep -c . ); d_tgt=$(tr " " "\n" <<<"$TARGETS" | grep . | sort -u | wc -l)
check "everyone asked exactly once" "$u_ask/$d_ask" "3/3"
check "everyone was asked exactly once" "$u_tgt/$d_tgt" "3/3"

# the answerer becomes the next asker -- that is what makes the ring flow
prev_t=$(cut -d" " -f3 <<<"$TARGETS"); next_a=$(cut -d" " -f4 <<<"$ASKERS")
check "the ring closes back to the first asker" \
  "$(cut -d" " -f4 <<<"$TARGETS")" "$(cut -d" " -f2 <<<"$ASKERS")"

check "questions done means open discussion" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$(last host)")" "discuss"

echo; echo "-- voting"
post host "/api/host/voting" '{}' >/dev/null
sleep 0.3
ids=$(python3 -c "
import json,sys
d=json.loads(sys.argv[1])
print(' '.join(p['id'] for p in d['players'] if p['inRound']))" "$(last host)")
read -r ID1 ID2 ID3 <<<"$ids"

r=$(post p1 "/api/vote" "{\"target\":\"$ID1\"}"); has "$r" "vote for yourself" "self-vote rejected"
post p1 "/api/vote" "{\"target\":\"$ID2\"}" >/dev/null
post p2 "/api/vote" "{\"target\":\"$ID3\"}" >/dev/null
sleep 0.3
check "still voting with one outstanding" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$(last host)")" "vote"
post p3 "/api/vote" "{\"target\":\"$ID2\"}" >/dev/null
sleep 0.4

fs=$(last host)
check "results once everyone voted" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$fs")" "results"
has "$fs" '"topic"' "results reveal the topic"
has "$fs" '"imposter"' "results name the imposter"
votes=$(python3 -c "import json,sys;print(sum(t['votes'] for t in json.loads(sys.argv[1])['results']['tally']))" "$fs")
check "all three votes counted" "$votes" "3"

echo; echo "-- reconnect with the cookie"
pkill -f "api/events" 2>/dev/null; sleep 0.3
r=$(j p1 "$BASE/api/me"); has "$r" '"joined":true' "cookie still valid after a drop"
rm -f /tmp/imp/p1.sse /tmp/imp/host.sse
sse p1; sse host; sleep 0.5
has "$(last p1)" '"phase":"results"' "rejoined straight into the live round"

echo; echo "-- back to the lobby"
post host "/api/host/lobby" '{}' >/dev/null
sleep 0.3
check "phase back to lobby" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['phase'])" "$(last host)")" "lobby"
check "room code unchanged while occupied" "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['code'])" "$(last host)")" "$CODE"

echo; echo "-- room code lifecycle (grace = 2s)"
for i in 1 2 3; do post p$i "/api/leave" '{}' >/dev/null; done
pkill -f "api/events" 2>/dev/null
sleep 0.5
rm -f /tmp/imp/host.sse; sse host; sleep 0.5
check "code holds while the host screen is still up" \
  "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['code'])" "$(last host)")" "$CODE"

# now the host leaves too -- room is fully empty, code should reroll
pkill -f "api/events" 2>/dev/null
sleep 3.5
rm -f /tmp/imp/host.sse
post host2 "/api/host/claim" '{}' >/dev/null
sse host2; sleep 0.5
NEW=$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['code'])" "$(last host2)")
if [ "$NEW" != "$CODE" ]; then ok "code rerolled once the room fully emptied ($CODE -> $NEW)"
else bad "code did not reroll after everyone left"; fi
r=$(post p9 "/api/join" "{\"code\":\"$CODE\",\"name\":\"Late\"}")
has "$r" "wrong room code" "the old code no longer works"

echo
echo "passed: $PASS   failed: $FAIL"
[ "$FAIL" -eq 0 ]
