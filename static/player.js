const $ = (sel) => document.querySelector(sel);

const screens = {
  join:     $("#s-join"),
  lobby:    $("#s-lobby"),
  reveal:   $("#s-reveal"),
  question: $("#s-question"),
  discuss:  $("#s-discuss"),
  vote:     $("#s-vote"),
  results:  $("#s-results"),
};

let stream = null;
let lastSnap = null;
let cardRound = 0;      // which round the reveal card is currently showing
let justTapped = false; // animate the flip only when the player opened it
let dismissed = false;  // player has finished reading their file this round
let peeking = false;

function show(name) {
  for (const [key, el] of Object.entries(screens)) el.hidden = key !== name;
}

async function post(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || "Something went wrong");
  return data;
}

/* ── join ───────────────────────────────────────────────────────────── */

const nameInput = $("#f-name");
const codeInput = $("#f-code");
const joinNote = $("#join-note");

nameInput.addEventListener("input", () => {
  $("#f-count").textContent = `${[...nameInput.value].length}/16`;
});

codeInput.addEventListener("input", () => {
  codeInput.value = codeInput.value.toUpperCase().replace(/[^A-Z0-9]/g, "");
});

for (const el of [codeInput, nameInput]) {
  el.addEventListener("keydown", (e) => { if (e.key === "Enter") join(); });
}

$("#b-join").addEventListener("click", join);

async function join() {
  const btn = $("#b-join");
  joinNote.textContent = "";
  btn.disabled = true;
  try {
    await post("/api/join", { code: codeInput.value, name: nameInput.value });
    connect();
  } catch (err) {
    joinNote.textContent = err.message;
  } finally {
    btn.disabled = false;
  }
}

$("#b-leave").addEventListener("click", async () => {
  if (stream) { stream.close(); stream = null; }
  await post("/api/leave").catch(() => {});
  show("join");
});

/* ── stream ─────────────────────────────────────────────────────────── */

function connect() {
  if (stream) stream.close();
  stream = new EventSource("/api/events");
  stream.onmessage = (e) => render(JSON.parse(e.data));
  stream.onerror = async () => {
    // Either the server blipped (EventSource retries on its own) or this
    // seat is gone. Check which, and fall back to the join form if needed.
    const me = await fetch("/api/me").then((r) => r.json()).catch(() => null);
    if (me && !me.joined) {
      stream.close();
      stream = null;
      show("join");
    }
  };
}

/* ── rendering ──────────────────────────────────────────────────────── */

function render(s) {
  if (!s.you) { show("join"); return; }
  lastSnap = s;

  $("#l-code").textContent = s.code;
  $("#l-name").textContent = s.you.name;
  $("#r-for").textContent = s.you.name;
  for (const id of ["#r-round", "#q-round", "#d-round", "#v-round", "#x-round"]) {
    $(id).textContent = s.round;
  }

  if (s.round !== cardRound) {  // new round — reseal the card
    cardRound = s.round;
    peeking = false;
    dismissed = false;
    resetCard();
  }

  // Everyone else finishing shouldn't snatch the file out of your hands —
  // stay on the card until you've put it away yourself.
  let phase = s.phase;
  if ((phase === "question" || phase === "discuss") &&
      s.you.inRound && s.role && !dismissed) {
    phase = "reveal";
  }

  switch (phase) {
    case "lobby":   renderLobby(s);   break;
    case "reveal":  renderReveal(s);  break;
    case "question": renderQuestion(s); break;
    case "discuss": renderDiscuss(s); break;
    case "vote":    renderVote(s);    break;
    case "results": renderResults(s); break;
  }
}

function renderLobby(s) {
  show("lobby");
  const seated = s.players.filter((p) => p.connected).length;
  $("#l-status").textContent = s.hostOnline
    ? (seated < s.minPlayers
        ? `${seated} seated — ${s.minPlayers} needed to start.`
        : "Waiting for the host to deal the files.")
    : "The shared screen isn't connected yet.";

  $("#l-roster").replaceChildren(...s.players.map((p) => {
    const li = document.createElement("li");
    const dot = document.createElement("span");
    dot.className = "dot" + (p.connected ? " dot--on" : "");
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = p.name;
    li.append(dot, who);
    if (!p.connected) {
      const tag = document.createElement("span");
      tag.className = "tag";
      tag.textContent = "away";
      li.append(tag);
    }
    return li;
  }));
}

function renderReveal(s) {
  show("reveal");
  if (!s.you.inRound) {
    $("#r-status").textContent = "You joined mid-round — you're in from the next one.";
    return;
  }
  if (s.role) {
    paintOpenFace($("#r-open"), s.role);
    openCard(justTapped);
    justTapped = false;
    $("#b-got").hidden = false;
    const left = s.players.filter((p) => p.inRound && p.connected && !p.ready).length;
    $("#r-status").textContent = left > 0
      ? `Waiting on ${left} more to open their file.`
      : "Everyone's opened theirs. Put it away when you're ready.";
  } else {
    $("#b-got").hidden = true;
    $("#r-status").textContent = "Nobody else can see this. Open it, then keep it to yourself.";
  }
}

function renderQuestion(s) {
  show("question");
  const a = s.ask;
  if (!a) return;

  $("#q-n").textContent = a.number;
  $("#q-total").textContent = a.total;
  $("#q-asker").textContent = a.asker;
  $("#q-target").textContent = a.target;
  $("#q-next").textContent = a.next
    ? `Up next — ${a.target} asks ${a.next}`
    : "Last question, then the floor opens.";

  $("#q-pips").replaceChildren(...Array.from({ length: a.total }, (_, i) => {
    const d = document.createElement("span");
    d.className = "pip" + (i < a.number - 1 ? " pip--done" : i === a.number - 1 ? " pip--now" : "");
    return d;
  }));

  const me = s.you.id;
  const asking = a.askerId === me;
  const answering = a.targetId === me;

  $("#q-title").textContent = asking ? "Your turn to ask"
                            : answering ? "You're being asked"
                            : `Question ${a.number} of ${a.total}`;
  $("#q-sub").textContent = asking
    ? `Ask ${a.target} one question, out loud. Something only someone who knows the topic could answer well.`
    : answering
      ? `Answer ${a.asker} out loud. Then it's your turn to ask.`
      : "Listen closely — this is where people slip up.";

  $("#b-answered").hidden = !answering;
  if (s.role) paintOpenFace($("#q-open"), s.role);
  $("#q-slot").hidden = !peeking;
  $("#b-peek-q").textContent = peeking ? "Hide my file" : "Check my file";
}

function renderDiscuss(s) {
  show("discuss");
  if (s.role) paintOpenFace($("#d-open"), s.role);
  $("#d-slot").hidden = !peeking;
  $("#b-peek").textContent = peeking ? "Hide my file" : "Check my file";
}

function renderVote(s) {
  show("vote");
  const me = s.you;
  $("#v-status").textContent = me.inRound
    ? "Pick one. You can change your mind until everyone's in."
    : "You're sitting this round out — watch the accusations fly.";

  $("#v-choices").replaceChildren(...s.players.filter((p) => p.inRound).map((p) => {
    const b = document.createElement("button");
    b.className = "choice";
    b.type = "button";
    b.setAttribute("aria-pressed", String(me.votedFor === p.id));
    b.disabled = !me.inRound || p.id === me.id;

    const nm = document.createElement("span");
    nm.textContent = p.id === me.id ? `${p.name} (you)` : p.name;
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = p.ready ? "voted" : "";
    b.append(nm, tag);

    b.addEventListener("click", () => post("/api/vote", { target: p.id }).catch(() => {}));
    return b;
  }));
}

function renderResults(s) {
  show("results");
  const r = s.results;
  if (!r) return;

  const v = document.createElement("span");
  v.className = "verdict " + (r.caught ? "verdict--caught" : "verdict--escaped");
  v.textContent = r.tie ? "Hung vote — imposter walks"
                        : (r.caught ? "Imposter caught" : "Imposter walked free");
  $("#x-verdict").replaceChildren(v);

  $("#x-topic").textContent = r.topic;
  $("#x-imposter").textContent = r.imposter || "—";
  $("#x-hint").textContent = r.hint;

  $("#x-tally").replaceChildren(...r.tally.map((t) => {
    const li = document.createElement("li");
    const nm = document.createElement("span");
    nm.textContent = t.name;
    const n = document.createElement("span");
    n.className = "n";
    n.textContent = t.votes === 1 ? "1 vote" : `${t.votes} votes`;
    li.append(nm, n);
    return li;
  }));
}

/* ── the file card ──────────────────────────────────────────────────── */

function paintOpenFace(el, role) {
  el.replaceChildren();
  if (role.imposter) {
    const stamp = document.createElement("div");
    stamp.className = "stamp";
    stamp.textContent = "No clearance";
    const label = document.createElement("div");
    label.className = "file__hintlabel";
    label.textContent = "All you have is";
    const word = document.createElement("p");
    word.className = "file__word";
    word.textContent = role.hint;
    el.append(stamp, label, word);
  } else {
    const kicker = document.createElement("div");
    kicker.className = "file__kicker";
    kicker.textContent = "Topic — cleared";
    const word = document.createElement("p");
    word.className = "file__word";
    word.textContent = role.topic;
    el.append(kicker, word);
  }
}

function openCard(animate) {
  const card = $("#r-card");
  card.classList.toggle("is-instant", !animate);
  card.classList.add("is-open");
  if (!animate) {
    // Drop the no-animation flag once the browser has painted the open state,
    // so a later round still gets its flip.
    requestAnimationFrame(() => requestAnimationFrame(() => card.classList.remove("is-instant")));
  }
}

function resetCard() {
  const card = $("#r-card");
  card.classList.add("is-instant");
  card.classList.remove("is-open");
  requestAnimationFrame(() => requestAnimationFrame(() => card.classList.remove("is-instant")));
}

$("#r-card").addEventListener("click", () => {
  if ($("#r-card").classList.contains("is-open")) return;
  justTapped = true;
  post("/api/reveal").catch(() => { justTapped = false; });
});

$("#b-got").addEventListener("click", () => {
  dismissed = true;
  if (lastSnap) render(lastSnap);
});

$("#b-answered").addEventListener("click", () => {
  post("/api/answered").catch(() => {});
});

function wirePeek(btnSel, slotSel) {
  $(btnSel).addEventListener("click", () => {
    peeking = !peeking;
    if (lastSnap) render(lastSnap);
  });
}

wirePeek("#b-peek");
wirePeek("#b-peek-q");

/* ── boot ───────────────────────────────────────────────────────────── */

fetch("/api/me")
  .then((r) => r.json())
  .then((me) => {
    if (me.joined) connect();  // cookie still good — straight back in
    else show("join");
  })
  .catch(() => show("join"));
