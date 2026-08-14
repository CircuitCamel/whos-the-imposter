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
    $("#h-code-corner-code").textContent = s.code;

    // The big code + QR only earn their space while people are still
    // joining - once the round's under way, shrink to a corner reminder.
    const inLobby = s.phase === "lobby";
    $("#h-hero").hidden = !inLobby;
    $("#h-code-corner").hidden = inLobby;
    if (inLobby) renderQR(s.code, s.joinURL || location.origin);

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
            $("#h-phase").textContent = `Round ${s.round} - reading files`;
            $("#h-hint").textContent = waiting > 0
                ? `${waiting} still to put theirs away.`
                : "Everyone's put theirs away.";
            controls.append(button("Skip to open discussion", "/api/host/discuss"));
            break;
        }
        case "question": {
            const a = s.ask;
            $("#h-phase").textContent = a
                ? `Round ${s.round} - question ${a.number} of ${a.total}`
                : `Round ${s.round} - questions`;
            $("#h-hint").textContent = a
                ? `${a.target} answers, then asks the next one.`
                : "";
            if (a) renderAsk(a);
            controls.append(button("Next question", "/api/host/nextq", "quiet"));
            controls.append(button("Open discussion now", "/api/host/discuss"));
            break;
        }
        case "discuss": {
            $("#h-phase").textContent = `Round ${s.round} - discussion`;
            $("#h-hint").textContent = "Let them talk. Open voting when the room's ready.";
            controls.append(button("Open voting", "/api/host/voting"));
            break;
        }
        case "vote": {
            $("#h-phase").textContent = `Round ${s.round} - voting`;
            $("#h-hint").textContent = waiting > 0 ? `${waiting} still to vote.` : "All votes in.";
            controls.append(button("Close voting now", "/api/host/close", "danger"));
            break;
        }
        case "results": {
            $("#h-phase").textContent = `Round ${s.round} - closed`;
            $("#h-hint").textContent = seated < s.minPlayers
                ? `${s.minPlayers} players needed to deal another round.`
                : "";
            const next = button("Deal the next round", "/api/host/start");
            next.disabled = seated < s.minPlayers;
            controls.append(next);
            controls.append(button("Back to the lobby", "/api/host/lobby", "quiet"));
            controls.append(button("End the game", "/api/host/end", "danger"));
            if (s.results) renderResults(s.results);
            break;
        }
        case "gameover": {
            $("#h-phase").textContent = "Game over";
            $("#h-hint").textContent = "";
            controls.append(button("New game", "/api/host/newgame"));
            if (s.results) renderPodium(s.results.board);
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
        else if (s.phase === "reveal") tag.textContent = !p.inRound ? "next round"
            : p.ready ? "put away" : p.opened ? "reading" : "sealed";
        else if (s.phase === "question") {
            const a = s.ask;
            tag.textContent = !p.inRound ? "watching"
                : a && a.askerId === p.id ? "asking now"
                    : a && a.targetId === p.id ? "answering"
                        : p.ready ? "asked"
                            : "to ask";
        }
        else if (s.phase === "vote") tag.textContent = p.inRound ? (p.ready ? "voted" : "thinking") : "watching";
        else if (!p.inRound && s.round > 0 && s.phase !== "lobby" && s.phase !== "gameover") tag.textContent = "next round";
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
        ? `Up next - ${a.target} asks ${a.next}`
        : "Last question, then the floor opens.";

    $("#h-ask").replaceChildren(pips, turn, next);
}

function renderResults(r) {
    const box = $("#h-results");

    const v = document.createElement("span");
    v.className = "verdict " + (r.caught ? "verdict--caught" : "verdict--escaped");
    v.textContent = r.tie ? "Hung vote - imposter walks"
        : (r.caught ? "Imposter caught" : "Imposter walked free");

    const topic = document.createElement("p");
    topic.className = "reveal-line";
    topic.append("The topic was ", strong(r.topic), ".");

    const imp = document.createElement("p");
    imp.className = "reveal-line";
    imp.append("The imposter was ", strong(r.imposter || "-"), ", working from ", strong(r.hint), ".");

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

    const boardTitle = document.createElement("h2");
    boardTitle.style.marginTop = "1.5rem";
    boardTitle.textContent = "Leaderboard";

    box.replaceChildren(v, topic, imp, tally, boardTitle, boardList(r.board));
}

function boardList(board) {
    const ol = document.createElement("ol");
    ol.className = "board";
    ol.replaceChildren(...(board || []).map((b, i) => {
        const li = document.createElement("li");
        const rank = document.createElement("span");
        rank.className = "rank" + (i < 3 ? ` rank--${i + 1}` : "");
        rank.textContent = i + 1;
        const nm = document.createElement("span");
        nm.className = "who";
        nm.textContent = b.name;
        const n = document.createElement("span");
        n.className = "n";
        n.textContent = b.score === 1 ? "1 pt" : `${b.score} pts`;
        li.append(rank, nm, n);
        return li;
    }));
    return ol;
}

function renderPodium(board) {
    const box = $("#h-results");
    const title = document.createElement("h2");
    title.textContent = "Final standings";

    const ol = document.createElement("ol");
    ol.className = "podium";
    ol.replaceChildren(...(board || []).slice(0, 3).map((b, i) => {
        const li = document.createElement("li");
        const place = document.createElement("span");
        place.className = "place";
        place.textContent = i + 1;
        const nm = document.createElement("span");
        nm.className = "who";
        nm.textContent = b.name;
        const n = document.createElement("span");
        n.className = "n";
        n.textContent = b.score === 1 ? "1 pt" : `${b.score} pts`;
        li.append(place, nm, n);
        return li;
    }));

    box.replaceChildren(title, ol);
}

/* ── join QR ────────────────────────────────────────────────────────── */

let qrKey = null;

// The code and URL only ever change between rounds, not every SSE tick, so
// skip re-encoding the QR unless one of them actually moved.
function renderQR(code, joinURL) {
    const key = code + "|" + joinURL;
    if (key === qrKey) return;
    qrKey = key;

    const box = $("#h-qr");
    const url = `${joinURL}/?code=${encodeURIComponent(code)}`;
    const qr = qrcode(0, "M");
    qr.addData(url);
    qr.make();
    box.innerHTML = qr.createSvgTag({ cellSize: 5, margin: 8, scalable: true, alt: `Join at ${url}` });
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
