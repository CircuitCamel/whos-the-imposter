const $ = (sel) => document.querySelector(sel);

let stream = null;

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

function act(path, body) {
  $("#h-note").textContent = "";
  post(path, body).catch((err) => { $("#h-note").textContent = err.message; });
}

function button(label, path, variant) {
  const b = document.createElement("button");
  b.className = "btn" + (variant ? ` btn--${variant}` : "");
  b.type = "button";
  b.textContent = label;
  b.addEventListener("click", () => act(path));
  return b;
}

/* ── rendering ──────────────────────────────────────────────────────── */

function render(s) {
  $("#h-code").textContent = s.code;
  $("#h-url").textContent = s.joinURL || location.origin;

  const seated = s.players.filter((p) => p.connected).length;
  const inRound = s.players.filter((p) => p.inRound && p.connected);
  const waiting = inRound.filter((p) => !p.ready).length;

  const controls = $("#h-controls");
  controls.replaceChildren();
  $("#h-results").replaceChildren();
  $("#h-ask").replaceChildren();

  switch (s.phase) {
    case "lobby": {
      $("#h-phase").textContent = seated === 0 ? "Waiting for players" : `${seated} seated`;
      $("#h-hint").textContent = seated < s.minPlayers
        ? `${s.minPlayers} players needed to deal a round.`
        : "Ready when you are.";
      const start = button(s.round === 0 ? "Deal the files" : "Deal the next round", "/api/host/start");
      start.disabled = seated < s.minPlayers;
      controls.append(start);
      break;
    }
    case "reveal": {
      $("#h-phase").textContent = `Round ${s.round} — opening files`;
      $("#h-hint").textContent = waiting > 0
        ? `${waiting} still to open theirs.`
        : "Everyone's looked.";
      controls.append(button("Skip to open discussion", "/api/host/discuss"));
      break;
    }
    case "question": {
      const a = s.ask;
      $("#h-phase").textContent = a
        ? `Round ${s.round} — question ${a.number} of ${a.total}`
        : `Round ${s.round} — questions`;
      $("#h-hint").textContent = a
        ? `${a.target} answers, then asks the next one.`
        : "";
      if (a) renderAsk(a);
      controls.append(button("Next question", "/api/host/nextq", "quiet"));
      controls.append(button("Open discussion now", "/api/host/discuss"));
      break;
    }
    case "discuss": {
      $("#h-phase").textContent = `Round ${s.round} — discussion`;
      $("#h-hint").textContent = "Let them talk. Open voting when the room's ready.";
      controls.append(button("Open voting", "/api/host/voting"));
      break;
    }
    case "vote": {
      $("#h-phase").textContent = `Round ${s.round} — voting`;
      $("#h-hint").textContent = waiting > 0 ? `${waiting} still to vote.` : "All votes in.";
      controls.append(button("Close voting now", "/api/host/close", "danger"));
      break;
    }
    case "results": {
      $("#h-phase").textContent = `Round ${s.round} — closed`;
      $("#h-hint").textContent = "";
      controls.append(button("Back to the lobby", "/api/host/lobby"));
      if (s.results) renderResults(s.results);
      break;
    }
  }

  $("#h-roster").replaceChildren(...s.players.map((p) => {
    const li = document.createElement("li");
    const dot = document.createElement("span");
    dot.className = "dot" + (p.ready ? " dot--ready" : p.connected ? " dot--on" : "");
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = p.name;
    li.append(dot, who);

    const tag = document.createElement("span");
    tag.className = "tag";
    if (!p.connected) tag.textContent = "away";
    else if (s.phase === "reveal") tag.textContent = p.inRound ? (p.ready ? "opened" : "sealed") : "next round";
    else if (s.phase === "question") {
      const a = s.ask;
      tag.textContent = !p.inRound ? "watching"
        : a && a.askerId === p.id ? "asking now"
        : a && a.targetId === p.id ? "answering"
        : p.ready ? "asked"
        : "to ask";
    }
    else if (s.phase === "vote") tag.textContent = p.inRound ? (p.ready ? "voted" : "thinking") : "watching";
    else if (!p.inRound && s.round > 0 && s.phase !== "lobby") tag.textContent = "next round";
    li.append(tag);

    if (s.phase === "lobby") {
      const k = document.createElement("button");
      k.className = "kick";
      k.type = "button";
      k.textContent = "remove";
      k.addEventListener("click", () => act("/api/host/kick", { id: p.id }));
      li.append(k);
    }
    return li;
  }));
}

function renderAsk(a) {
  const pips = document.createElement("div");
  pips.className = "pips";
  for (let i = 0; i < a.total; i++) {
    const d = document.createElement("span");
    d.className = "pip" + (i < a.number - 1 ? " pip--done" : i === a.number - 1 ? " pip--now" : "");
    pips.append(d);
  }

  const turn = document.createElement("div");
  turn.className = "turn turn--host";
  const asker = document.createElement("span");
  asker.className = "turn__who turn__who--asks";
  asker.textContent = a.asker;
  const arrow = document.createElement("span");
  arrow.className = "turn__arrow";
  arrow.textContent = "asks";
  const target = document.createElement("span");
  target.className = "turn__who turn__who--answers";
  target.textContent = a.target;
  turn.append(asker, arrow, target);

  const next = document.createElement("p");
  next.className = "upnext";
  next.textContent = a.next
    ? `Up next — ${a.target} asks ${a.next}`
    : "Last question, then the floor opens.";

  $("#h-ask").replaceChildren(pips, turn, next);
}

function renderResults(r) {
  const box = $("#h-results");

  const v = document.createElement("span");
  v.className = "verdict " + (r.caught ? "verdict--caught" : "verdict--escaped");
  v.textContent = r.tie ? "Hung vote — imposter walks"
                        : (r.caught ? "Imposter caught" : "Imposter walked free");

  const topic = document.createElement("p");
  topic.className = "reveal-line";
  topic.append("The topic was ", strong(r.topic), ".");

  const imp = document.createElement("p");
  imp.className = "reveal-line";
  imp.append("The imposter was ", strong(r.imposter || "—"), ", working from ", strong(r.hint), ".");

  const tally = document.createElement("ul");
  tally.className = "tally";
  tally.replaceChildren(...r.tally.map((t) => {
    const li = document.createElement("li");
    const nm = document.createElement("span");
    nm.textContent = t.name;
    const n = document.createElement("span");
    n.className = "n";
    n.textContent = t.votes === 1 ? "1 vote" : `${t.votes} votes`;
    li.append(nm, n);
    return li;
  }));

  box.replaceChildren(v, topic, imp, tally);
}

function strong(text) {
  const b = document.createElement("b");
  b.textContent = text;
  return b;
}

/* ── boot ───────────────────────────────────────────────────────────── */

post("/api/host/claim")
  .then(() => {
    $("#s-main").hidden = false;
    stream = new EventSource("/api/events");
    stream.onmessage = (e) => render(JSON.parse(e.data));
  })
  .catch(() => { $("#s-blocked").hidden = false; });
