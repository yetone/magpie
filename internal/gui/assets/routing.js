// Routing, live: the gateway's own trace of each request, played as it
// happens. A magpie carries each request from its agent through magpie to
// the account routing put first; another brings the answer back — or,
// from one that can't answer, the failure back to magpie, and the first
// takes the request on to the next. Every agent sending at once plays at
// once, each from its own place on the left. Every row, number and sentence comes from what the gateway
// recorded while deciding (see internal/gateway/trace.go) — the order it
// weighed the accounts in, what it weighed them by, what each answered,
// how long a failed one rests. Nothing here is worked out again or made up.
(() => {
  const box = $("#rt");
  if (!box) return;
  const NS = "http://www.w3.org/2000/svg";
  const still = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
  const shown = () => !$("#view-routing").hidden && !document.hidden;
  // steady redraws a part of the page where the reader is: WebKit has no
  // scroll anchoring, and a part emptied and filled again, measured between,
  // pulls the page up to what was left of it for that moment
  function steady(fn) {
    const v = $("#view-routing"), top = v.scrollTop;
    try { return fn(); } finally { if (v.scrollTop !== top) v.scrollTop = top; }
  }

  // ---------- the stage ----------

  const top = el("div", "rt-top");
  const what = el("div", "rt-what");
  const mode = el("p", "rt-mode");
  top.append(what, mode);
  const stage = el("div", "rt-stage");
  const wires = document.createElementNS(NS, "svg");
  wires.setAttribute("class", "rt-wires");
  wires.setAttribute("aria-hidden", "true");
  const srcs = el("div", "rt-srcs"); // an agent's node each
  const hub = el("div", "rt-node rt-hub");
  const logo = document.createElementNS(NS, "svg");
  logo.setAttribute("viewBox", "0 0 44 44");
  logo.setAttribute("class", "rt-bird");
  logo.innerHTML = '<use href="#bird"/>';
  const hubSub = el("small"), chip = el("i");
  hub.append(logo, el("b", "", "magpie"), hubSub, chip);
  const list = el("ol", "rt-accts");
  // the magpies fly over the nodes, the wires run under them
  const sky = document.createElementNS(NS, "svg");
  sky.setAttribute("class", "rt-sky");
  sky.setAttribute("aria-hidden", "true");
  stage.append(wires, srcs, hub, list, sky);
  const foot = el("div", "rt-foot");
  const cap = el("p", "rt-cap");
  cap.setAttribute("aria-live", "polite");
  const stats = el("div", "rt-stats");
  const statB = [];
  for (const k of ["requests", "rerouted", "errors your agent saw"]) {
    const s = el("span"), b = el("b", "", "0");
    s.append(b, el("span", "", k));
    s.dataset.label = k;
    statB.push(b);
    stats.append(s);
  }
  foot.append(cap, stats);
  const log = el("div", "rt-log");
  const logHead = el("div", "rt-log-head");
  const steps = el("ol", "rt-steps");
  log.append(logHead, steps);
  const off = el("div", "none rt-off");
  box.append(top, stage, foot, log, off);

  // under the stage: every request the gateway keeps, and each account or
  // key as those requests found it
  const more = $("#rtMore");
  const reqHead = el("div", "row-head"), reqNote = el("span", "note");
  const reqs = el("div", "list rt-reqs");
  const actHead = el("div", "row-head"), actNote = el("span", "note");
  const acts = el("div", "list rt-acts");
  const hist = el("div", "rt-cols");
  const colA = el("div", "rt-col"), colB = el("div", "rt-col");
  colA.append(reqHead, reqs);
  colB.append(actHead, acts);
  hist.append(colA, colB);
  more.append(hist);

  const path = () => { const p = document.createElementNS(NS, "path"); wires.appendChild(p); return p; };
  const tick = (e) => { e.classList.remove("tick"); void e.offsetWidth; e.classList.add("tick"); };

  // ---------- words ----------

  let skew = 0; // the gateway's clock less this page's
  const now = () => Date.now() + skew;
  const at = (s) => new Date(s).getTime();
  const known0 = (s) => s && !s.startsWith("0001-");
  function dur(ms) {
    const s = Math.max(1, Math.round(ms / 1000));
    if (s < 60) return t("{n} s", { n: s });
    const m = Math.round(s / 60);
    if (m < 60) return t("{n} min", { n: m });
    const h = Math.floor(m / 60), mm = m % 60;
    if (h < 10 && mm) return t("{h} h {m} min", { h, m: mm });
    if (h < 48) return t("{n} h", { n: Math.round(m / 60) });
    return t("{n} d", { n: Math.round(m / 1440) });
  }
  const took = (ms = 0) => ms < 1000 ? t("{n} ms", { n: ms }) : t("{n} s", { n: (ms / 1000).toFixed(ms < 10e3 ? 1 : 0) });
  function clock(s) {
    const d = new Date(s), n = new Date();
    const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    return d.toDateString() === n.toDateString() ? hm : d.toLocaleDateString([], { weekday: "short" }) + " " + hm;
  }
  const tokens = (n) => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(Math.round(n));
  const pct = (n) => Math.round(n) + "%";
  const FAIL = { rate: "rate limited", credit: "out of credit", quota: "quota used up", other: "failed", canceled: "canceled", foreign: "another account's reasoning", floor: "reply too short" };
  const failWord = (why) => t(FAIL[why] || "failed");
  const API = { anthropic: "Anthropic", chat: "OpenAI", responses: "OpenAI Responses", gemini: "Gemini" };
  const MODES = {
    "": ["Smart", "Smart: of the accounts with quota to spare, the one whose allowance renews soonest goes first — what it has left would be lost at the reset. One at 90% or more waits until the others can't answer; one resting after a failure goes last."],
    order: ["In order", "In order: the first answers everything until it can't; then the next."],
    rotate: ["In turn", "In turn: each conversation's next turn goes to the account after the one that answered its last, and a new conversation starts one further along; the requests within a turn stay put, keeping the prompt cache."],
    usage: ["Least used", "Least used first: the account with the most of its allowance left goes first; a key by the tokens magpie sent it lately."],
  };
  const GROUP_ORDER = "In order: member by member, the first model the group names until it can't answer, each over its own accounts or keys as its provider routes them.";
  const KEYS_SMART = "Smart: keys that suit the request go first — one made for the model's own API — then in their order. One resting after a failure goes last.";

  const agentOf = (id) => (state.clients || state.agents).find((a) => a.id === id);
  const agentName = (id) => agentOf(id)?.name || (id && id !== "other" ? id : t("your agent"));
  // who names an account or key in a sentence
  const who = (w) => w.kind === "provider" ? w.name : w.who;
  // why one is left out: an account's plan lacks the model; a key's list
  // from its vendor does — relays list each key its own group's models
  const unlistedWord = (w) => w.kind === "key" ? t("{name}'s list for this key has no {model}", { name: w.name, model: w.model }) : t("its plan doesn't list {model}", { model: w.model });
  const group = (w) => w.used >= 98 ? "spent" : w.used >= 90 ? "low" : "fine";
  const renews = (w) => (w.renews || []).map((s) => known0(s) ? at(s) : 0);

  // cmpRenews orders two accounts' windows as the gateway does: the
  // biggest first, to the hour, one not known after those known. It says
  // which window decided, too.
  function cmpRenews(a, b) {
    const ra = renews(a), rb = renews(b), H = 3600e3;
    for (let k = 0; k < ra.length || k < rb.length; k++) {
      const x = ra[k] ? Math.floor(ra[k] / H) : 0, y = rb[k] ? Math.floor(rb[k] / H) : 0;
      if (x === y) continue;
      if (!x || !y) return { c: x ? -1 : 1, k };
      return { c: x < y ? -1 : 1, k };
    }
    return { c: 0, k: -1 };
  }

  // how long a rest is from a moment: now for a row, the moment it was
  // decided for the story of a request
  function restWhen(rest, from = now()) {
    const left = at(rest.until) - from;
    return left < 3600e3 ? t("back in {d}", { d: dur(left) }) : t("back at {time}", { time: clock(rest.until) });
  }
  // restHow says how long a failed account sits out, and what said so.
  function restHow(rest, from) {
    const d = dur(at(rest.until) - from), time = clock(rest.until);
    switch (rest.by) {
      case "retry-after": return t("It rests {d}, as the vendor's Retry-After says", { d });
      case "credit": return t("It sits out half an hour, until someone tops it up");
      case "window": return t("Its allowance is used up: it rests until that renews, at {time}", { time });
      case "resets": return t("It rests until {time}, when Claude Code says the limit resets", { time });
      case "quota": return t("It rests 15 minutes: out of quota, with no word of when it resets");
      case "backoff": return rest.failures > 1
        ? t("It has failed {n} times in a row: it rests {d}, longer each time", { n: rest.failures, d })
        : t("It rests {d}, longer if it fails again", { d });
      case "cooldown": return rest.why === "rate"
        ? t("It cools down {d}: the vendor didn't say for how long", { d })
        : t("It rests {d}", { d });
      default: return t("It rests {d}", { d });
    }
  }

  // why routing put the first where it did
  function firstWhy(r) {
    const f = r.order[0];
    if (!f) return t("Nothing could take {model}.", { model: r.model });
    const w = who(f);
    if (r.order.length === 1) {
      if (f.rest) return t("{who} is the only one, so it's tried though it is resting.", { who: w });
      return f.kind === "account" ? t("{who} is the only account on for {model} — nothing to choose between.", { who: w, model: r.model })
        : t("{name} has one key on — nothing to choose between.", { name: f.name });
    }
    if (f.rest) return t("Every one is resting after a failure, so {who}, first in line, is tried all the same.", { who: w });
    if (f.fallback) {
      const name = r.order.find((x) => !x.fallback)?.name || r.provider;
      return t("{name} is resting, so its fallback {fb} goes first.", { name, fb: `${f.provider}/${f.model}` });
    }
    const peers = r.order.filter((x) => x !== f && !x.fallback && !x.rest);
    const rested = r.order.filter((x) => x !== f && !x.fallback && x.rest).map(who);
    const restedTo = (s) => rested.length ? t("With {rested} resting after a failure, {who} goes first: ", { rested: rested.join(", "), who: w }) + s : null;
    switch (f.routing) {
      case "order": return rested.length
        ? t("In order: with {rested} resting after a failure, {who} is the first that can answer.", { rested: rested.join(", "), who: w })
        : t("In order: {who} is first, and answers everything while it can.", { who: w });
      case "rotate": return t("In turn: it's {who}'s turn — each request starts one further along.", { who: w });
      case "usage":
        if (f.kind === "account" && f.known) return t("Least used first: {who} has the most of its allowance left — {n} used.", { who: w, n: pct(f.used) });
        if (f.kind === "key") return t("Least used first: {who} served the fewest tokens lately — {n}.", { who: w, n: tokens(f.tokens || 0) });
        return t("Least used first: {who} goes first.", { who: w });
    }
    if (f.kind === "key") {
      if (!f.fit && peers.some((p) => p.fit > 0)) return t("{who} goes first: it's made for {api}, the API {model} is at home in, so nothing is translated.", { who: w, api: API[f.speaks] || f.speaks, model: f.model });
      return restedTo(t("the others go in their order.")) || t("{who} goes first: keys go in their order, those that suit the request first.", { who: w });
    }
    if (f.kind !== "account") return t("{who} goes first.", { who: w });
    if (!f.known && !peers.some((p) => p.known)) return t("The vendor hasn't said yet what these accounts have left, so they go in their order: {who} first.", { who: w });
    if (group(f) !== "fine") return t("Every account is at 90% or more of its allowance, so the one with the most left goes first: {who}, at {n}.", { who: w, n: pct(f.used) });
    const next = peers.find((p) => p.known && group(p) === "fine");
    const soon = renews(f).find(Boolean);
    if (!next) return t("{who} goes first: it has quota to spare, and the others are kept for last.", { who: w });
    const { c, k } = cmpRenews(f, next);
    if (c < 0 && k === 0) return t("{who} goes first: of those with quota to spare, its allowance renews soonest — in {d} — and what it has left then is lost. {other} renews later and keeps its own.", { who: w, d: dur(renews(f)[0] - at(r.time)), other: who(next) });
    if (c < 0) return t("{who} goes first: its allowance renews in the same hour as {other}'s, and its shorter one sooner — in {d}.", { who: w, other: who(next), d: dur(renews(f)[k] - at(r.time)) });
    if (soon) return t("{who} and {other} renew within the same hour, so the order given stays — and the vendor's prompt cache stays warm.", { who: w, other: who(next) });
    return t("{who} goes first, in the order given: when its allowance renews isn't known.", { who: w });
  }

  // asides: the others' places, where they say something
  function asides(r) {
    const out = [];
    const aff = affWhy(r, true) ? null : affWhy(r, false);
    if (aff) out.push(aff);
    const cls = classWhy(r.rule);
    if (cls) out.push(cls);
    for (const n of r.nested || []) {
      const c = classWhy(n.rule);
      if (c) out.push(t("In {group}: {text}", { group: n.name || n.group, text: c }));
    }
    const rule = ruleWhy(r, false);
    if (rule) out.push(rule);
    const smart = (x) => !x.routing && x.kind === "account";
    const someKnown = r.order.some((x) => x.known);
    for (const x of r.order.slice(1)) {
      if (x.rest) out.push(t("{who} is resting — {why}, {when} — so it waits at the back.", { who: who(x), why: `${x.rest.status} · ${failWord(x.rest.why)}`, when: restWhen(x.rest, at(r.time)) }));
      else if (smart(x) && x.known && group(x) === "spent") out.push(t("{who} is at {n} — all but used up, it answers only when nothing else can.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && x.known && group(x) === "low") out.push(t("{who} is at {n} — kept for when the others can't.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && !x.known && someKnown) out.push(t("{who}: what it has left isn't known yet, so it goes after those known.", { who: who(x) }));
    }
    const pooled = r.order.find((x) => !x.aside && x.kind === "key");
    for (const x of r.order.filter((x) => x.aside)) out.push(t("{who} is made for {api}, not {other} as the keys routed over are, so it isn't one of them: it's tried after them.", { who: who(x), api: API[x.speaks] || x.speaks || t("any API"), other: API[pooled?.speaks] || pooled?.speaks || t("any API") }));
    for (const x of r.left || []) out.push(x.kind === "key"
      ? t("{who} is left out: {name} lists {model} to its other keys, not this one.", { who: who(x), name: x.name, model: x.model })
      : t("{who} is left out: its plan doesn't list {model}.", { who: who(x), model: x.model }));
    return out;
  }

  // affWhy tells whether a request stayed with who answered its
  // conversation last, and why — as the gateway decided it. With lead, only
  // when that is what put the first where it is.
  function affWhy(r, lead) {
    const a = r.affinity;
    if (!a) return null;
    const lw = r.order.find((x) => x.id === a.last), last = lw ? who(lw) : a.last;
    const routing = r.group ? r.group.routing : r.order[0]?.routing;
    const ago = known0(a.at) ? dur(at(r.time) - at(a.at)) : "";
    switch (a.why) {
      case "session": return t("Kept on {who}: it answered this conversation before, and affinity keeps a session with one account.", { who: last });
      case "turn": return t("Kept on {who}: {agent} is handing back tool results within turn {n}, and moving now would lose what the vendor cached of it.", { who: last, agent: agentName(r.agent), n: a.turn });
      case "cache": return t("Kept on {who}: the vendor read {n} tokens of this conversation from its cache {d} ago; anyone else would be sent them afresh and paid in full.", { who: last, n: tokens(a.cacheRead), d: ago });
      case "new-turn": return routing === "rotate"
        ? t("Turn {n} begins: in turn, it goes to the one after {who}, which answered the last turn — {next}.", { n: a.turn, who: last, next: who(r.order[0]) })
        : lead ? null : t("Turn {n} begins: affinity keeps a conversation only within a turn, so routing decides afresh.", { n: a.turn });
    }
    if (lead) return null;
    switch (a.why) {
      case "resting": return t("{who} answered this conversation last, but it is resting, so the conversation moves.", { who: last });
      case "spent": return t("{who} answered this conversation last, but its allowance is all but used up, so the conversation moves.", { who: last });
      case "gone": return t("{who} answered this conversation last, but it is no longer one to route to.", { who: last });
      case "no-cache": return t("{who} answered this conversation last, but the vendor read only {n} tokens of it from its cache then — not worth staying for.", { who: last, n: tokens(a.cacheRead || 0) });
      case "cold": return t("{who} answered this conversation {d} ago, longer than the 5 minutes a vendor keeps a prompt cached — so routing decides afresh.", { who: last, d: ago });
      case "off": return t("Affinity is off: each request is routed afresh, whoever answered its conversation before.");
      case "rule": {
        // the group's rule, or that of a group in it, that moved it
        const x = r.nested?.findLast((n) => n.rule?.use && !n.rule.unready && !n.rule.held)?.rule || r.rule;
        return t("{who} answered this conversation last, but a new turn begins and the group's rule {n} puts {use} first.", { who: last, n: x?.n, use: useName(r, x?.use) });
      }
    }
    return null;
  }

  // ruleWhy tells what the group's rules did with a request. With lead,
  // only when a rule put the first where it is.
  function ruleWhy(r, lead) {
    if (!r.rule || r.rule.bare) return null;
    const x = { ...r.rule, use: useName(r, r.rule.use) };
    const when = (x.when || []).map(condText).join(", ");
    if (x.use && !x.unready) {
      const held = t("Rule {n} ({when}) sent turn {turn} to {use} as it began; the turn stays there.", { n: x.n, when, turn: x.turn, use: x.use });
      if (x.held) return lead || affWhy(r, true) ? held : null;
      if (x.grown) return lead ? t("Within turn {turn} the conversation grew to about {tokens} tokens, more than the model it was on takes, so rule {n} ({when}) moves it to {use}.", { turn: x.turn, tokens: tokens(x.tokens), n: x.n, when, use: x.use }) : null;
      return lead ? t("Turn {turn} begins and rule {n} matches — {when} — so {use} goes first; the group's others stay behind it if it fails.", { turn: x.turn, n: x.n, when, use: x.use }) : null;
    }
    if (lead) return null;
    if (x.use) return t("Rule {n} matches, but {use} has nothing ready now, so the group's order stands.", { n: x.n, use: x.use });
    if (x.waits) return t("This turn began before magpie saw it, so the rules wait for the next one.");
    if (x.held) return null;
    return t("No rule matches turn {turn} (about {n} tokens{img}), so the group routes it as usual.", { turn: x.turn, n: tokens(x.tokens), img: x.images ? t(", with an image") : "" });
  }
  // treeText is a group's models, those of a group in it in brackets after
  // its name: a/m, Fast [b/m, c/m]
  function treeText(g) {
    const name = (id) => g.subs?.find((s) => s.id === id)?.name || id;
    let out = "";
    const open = [];
    (g.members || []).forEach((m, i) => {
      const via = (g.via?.[i] || "").split(">").filter(Boolean);
      let k = 0;
      while (k < open.length && k < via.length && open[k] === via[k]) k++;
      while (open.length > k) { out += "]"; open.pop(); }
      if (out && !out.endsWith("[")) out += ", ";
      while (open.length < via.length) { const v = via[open.length]; out += name(v) + " ["; open.push(v); }
      out += m;
    });
    return out + "]".repeat(open.length);
  }
  // useName is what a rule sends to: a model, or a group in the group
  const useName = (r, id) => id?.startsWith("group/") ? t("the group {name}", { name: r.group?.subs?.find((s) => "group/" + s.id === id)?.name || id.slice(6) }) : id;
  // nestedWhy tells, for each group in the group down to the one that
  // went first, what its own rules did — and which group it came through
  function nestedWhy(r) {
    const out = [];
    for (const n of r.nested || []) {
      const x = n.rule, g = n.name || n.group;
      if (!x) continue;
      const when = (x.when || []).map(condText).join(", ");
      if (x.use && x.unready) out.push(t("In {group}, rule {n} matches, but {use} has nothing ready now, so {group}'s order stands.", { group: g, n: x.n, use: x.use }));
      else if (x.use && x.held) out.push(t("In {group}, rule {n} ({when}) sent turn {turn} to {use} as it began; the turn stays there.", { group: g, n: x.n, when, turn: x.turn, use: x.use }));
      else if (x.use) out.push(t("In {group}, rule {n} matches — {when} — so {use} goes first there.", { group: g, n: x.n, when, use: x.use }));
      else if (!x.waits) out.push(t("In {group}, no rule matches, so it routes as usual.", { group: g }));
    }
    const f = r.order[0];
    if (f?.via?.length && r.group) {
      const names = f.via.map((id) => r.group.subs?.find((s) => s.id === id)?.name || id);
      const inner = r.group.subs?.find((s) => s.id === f.via[f.via.length - 1]);
      out.push(t("{who} is one of {path}, a group in {name}; that group routes {mode}.", { who: who(f), path: names.join(" › "), name: r.group.name, mode: t((MODES[inner?.routing || ""] || MODES[""])[0]).toLowerCase() }));
    }
    return out;
  }
  // classWhy tells what a group's classifier said of the turn's message,
  // for its rule step x
  function classWhy(x) {
    const c = x?.classified;
    if (!c) return x?.pick ? t("Turn {turn} reasons at {level}, as Jev picked when it began.", { turn: x.turn, level: x.pick }) : null;
    const r = { rule: x };
    const by = c.by || t("the classifier"), kinds = (c.intents || []).map((x) => `“${x}”`).join(", ");
    if (c.error) return c.intents?.length
      ? t("{by} was to tell which of {kinds} turn {turn} is, but couldn't — {err} — so no rule with an intent matches it.", { by, kinds, turn: r.rule.turn, err: c.error })
      : t("{by} was to rate how hard turn {turn} is, but couldn't — {err} — so it reasons as the agent asked.", { by, turn: r.rule.turn, err: c.error });
    const when = c.cached ? t("said before, for the same message") : t("in {ms}", { ms: took(c.ms) });
    const told = c.after ? " " + t("It was told turn {prev} was “{after}”, which a message that only carries on from it is too.", { prev: r.rule.turn - 1, after: c.after }) : "";
    const toldEffort = c.afterEffort ? " " + t("It was told turn {prev} reasoned at {level}, which a message that only carries on from it needs too.", { prev: r.rule.turn - 1, level: c.afterEffort }) : "";
    const effort = c.effort ? t("{by} rated turn {turn} {score} of 3, so it picks {level} reasoning for the turn — each model gets the level it has nearest.", { by, turn: r.rule.turn, score: (c.score || 0).toFixed(1), level: c.effort }) + toldEffort : "";
    if (!c.intents?.length) return effort ? `${effort} (${when})` : null;
    const sure = c.sure ? t(", {n} sure", { n: Math.round(c.sure * 100) + "%" }) : "";
    const said = (!c.intent
      ? t("{by} was asked which of {kinds} turn {turn} is, and said none ({took}).", { by, kinds, turn: r.rule.turn, took: when + sure })
      : t("{by} was asked which of {kinds} turn {turn} is, and said “{intent}” ({took}).", { by, kinds, turn: r.rule.turn, intent: c.intent, took: when + sure })) + told;
    return effort ? `${said} ${effort}` : said;
  }
  // a rule's condition as the gateway writes it, in the page's words
  function condText(c) {
    let m;
    if ((m = /^intent "(.*)"$/.exec(c))) return t("asks for “{intent}”", { intent: m[1].replace(/\\"/g, '"').replace(/\\\\/g, "\\") });
    if ((m = /^tokens ≥ (\d+)$/.exec(c))) return t("≥ {n} tokens", { n: Number(m[1]).toLocaleString() });
    if (c === "images") return t("has an image");
    if (c === "reasoning") return t("reasoning on");
    if ((m = /^effort ≥ (\w+)$/.exec(c))) return t("reasoning ≥ {level}", { level: m[1] });
    if ((m = /^agent (.+)$/.exec(c))) return m[1].split("|").map(agentName).join(" / ");
    return c;
  }

  function tryWhy(r, i) {
    const tr = r.tries[i], w = tried(r, tr), agent = agentName(r.agent);
    let name = w ? `${who(w)} (${w.model})` : tr.id;
    if (tr.effort) name += " " + t("at {level} reasoning", { level: tr.effort });
    if (!tr.done) return t("{who} is answering…", { who: name });
    if (tr.status < 400) {
      const tk = r.tokens ? " · " + t("{n} tokens", { n: tokens(r.tokens) }) : "";
      return i > 0
        ? t("{who} answered in {ms}{tk}. {agent} got one clean reply and never saw the {n} that failed first.", { who: name, ms: took(tr.ms), tk, agent, n: i })
        : t("{who} answered in {ms}{tk}.", { who: name, ms: took(tr.ms), tk });
    }
    if (tr.fail === "canceled")
      return t("{agent} canceled the request while {who} was answering: nobody failed, so nobody rests and nobody else is asked.", { who: name, agent });
    if (tr.fail === "foreign")
      return t("{who} couldn't read the reasoning another account wrote earlier in this conversation, so it is asked again without it, before any of the reply reaches {agent}.", { who: name, agent });
    if (tr.fail === "floor")
      return t("{who} takes no request for a reply as short as this one asked for, so it is asked again for the shortest it gives, before any of the reply reaches {agent}.", { who: name, agent });
    if (tr.again)
      return t("{who} answered {status} · {fail}, and nobody else is left to ask — a failure that may pass, so it is tried again in {d}, before any of the reply reaches {agent}.",
        { who: name, status: tr.status, fail: failWord(tr.fail), d: took(tr.again), agent });
    if (tr.rest) {
      const next = r.tries[i + 1], nw = next && tried(r, next);
      return t("{who} answered {status} · {fail}. {how}; the request goes on to {next} before any of the reply reaches {agent}.",
        { who: name, status: tr.status, fail: failWord(tr.fail), how: restHow(tr.rest, at(tr.start) + (tr.ms || 0)), next: nw ? who(nw) : t("the next"), agent });
    }
    const last = i >= r.order.length - 1;
    return last
      ? t("{who} answered {status} and nobody is left to try, so {agent} gets the error.", { who: name, status: tr.status, agent })
      : t("{who} answered {status} — an error another account wouldn't fix, so {agent} gets it.", { who: name, status: tr.status, agent });
  }

  // ---------- state ----------

  const routes = new Map(); // id → the latest of each route
  let seq = 0, mine = true, loaded = false;
  let cur = null;           // the route the header and the log tell of: the newest played
  let pinned = null;        // a past route picked from the strip
  let rows = new Map();     // id → { li, wire, st, bi, tg, w, rid, up }
  const subs = new Map();   // a group in the group's way down → its heading { li, wire, key, up }
  const agents = new Map(); // agent → { node, ic, name, sub, wire }
  let sets = [];            // the account sets on the stage, in the order they came
  const playing = new Set(); // the routes being played
  const LINGER = 12e3;      // how long an agent's last request stays on the stage
  let gen = 0, trips = [], waiters = [];
  let capQ = [], capAt = -1e9, capLo = false, flipUntil = 0;

  // fly carries a dot along paths one after another, as one flight — and
  // the magpie holding it in its beak, if there is one
  const fly = (dot, bird, legs, ms) => new Promise((res) => {
    const tr = { dot, bird, legs, t0: performance.now(), ms: still() || !shown() ? 0 : ms, res, g: gen };
    pose(tr, 0);
    trips.push(tr);
  }).finally(() => { for (const l of legs) if (l.j) l.p.remove(); });

  // tip is where a flight meets a wire's end or start, and which way it
  // heads there: along the wire, or back along it
  const tip = (p, atEnd, back) => () => {
    const L = p.isConnected && p.getTotalLength?.() || 0;
    if (!L) return null; // not laid out: nowhere to meet it yet
    const a = p.getPointAtLength(atEnd ? L : 0), b = p.getPointAtLength(atEnd ? Math.max(0, L - 2) : Math.min(L, 2));
    let tx = atEnd ? a.x - b.x : b.x - a.x, ty = atEnd ? a.y - b.y : b.y - a.y;
    if (back) { tx = -tx; ty = -ty; }
    const n = Math.hypot(tx, ty) || 1;
    return { x: a.x, y: a.y, tx: tx / n, ty: ty / n };
  };
  // via is the way through magpie from one wire to the next: a gentle arc
  // across it, carrying on the way the flight came in and leaving the way
  // the next wire goes — or, out the side it came in, a loop round inside.
  // It follows the wires as they move.
  function via(from, to) {
    const p = document.createElementNS(NS, "path");
    p.setAttribute("class", "via");
    sky.appendChild(p);
    const j = () => {
      const a = from(), b = to();
      if (!a || !b) { p.removeAttribute("d"); return; }
      const dx = b.x - a.x, dy = b.y - a.y, dist = Math.hypot(dx, dy);
      let c1, c2;
      if (dist < 4) {
        const h = hub.getBoundingClientRect(), k = Math.min(h.width, h.height) * .42, m = k * .6;
        const nx = a.ty, ny = -a.tx;
        c1 = [a.x + a.tx * k - nx * m, a.y + a.ty * k - ny * m];
        c2 = [b.x - b.tx * k + nx * m, b.y - b.ty * k + ny * m];
      } else {
        const k = dist * .38, l = Math.min(16, dist * .12), nx = dy / dist, ny = -dx / dist;
        c1 = [a.x + a.tx * k + nx * l, a.y + a.ty * k + ny * l];
        c2 = [b.x - b.tx * k + nx * l, b.y - b.ty * k + ny * l];
      }
      p.setAttribute("d", `M${a.x} ${a.y} C${c1[0]} ${c1[1]} ${c2[0]} ${c2[1]} ${b.x} ${b.y}`);
    };
    j();
    return { p, j };
  }
  const until = (f) => f() ? Promise.resolve() : new Promise((res) => waiters.push({ f, res }));
  const wake = () => { const w = waiters; waiters = []; for (const x of w) if (x.f()) x.res(); else waiters.push(x); };
  function say(s, lo) {
    if (!s) return;
    if (!shown()) { capQ = []; show({ s, lo }); return; }
    if (lo && capQ.length) return;
    if (!lo) capQ = capQ.filter((c) => !c.lo);
    capQ.push({ s, lo });
    if (capQ.length > 3) capQ.shift();
  }
  function show(c) { capLo = !!c.lo; cap.textContent = c.s; cap.classList.remove("in"); void cap.offsetWidth; cap.classList.add("in"); capAt = performance.now(); }

  function layout() {
    const r = stage.getBoundingClientRect();
    if (!r.width) return;
    const b = (e) => { const x = e.getBoundingClientRect(); return { l: x.left - r.left, r: x.right - r.left, t: x.top - r.top, b: x.bottom - r.top, cx: (x.left + x.right) / 2 - r.left, cy: (x.top + x.bottom) / 2 - r.top }; };
    wires.setAttribute("viewBox", `0 0 ${r.width} ${r.height}`);
    sky.setAttribute("viewBox", `0 0 ${r.width} ${r.height}`);
    const h = b(hub), low = b(srcs).b;
    for (const a of agents.values()) {
      const s = b(a.node), mx = (s.r + h.l) / 2;
      a.wire.setAttribute("d", h.l > s.r ? `M${s.r} ${s.cy} C${mx} ${s.cy} ${mx} ${h.cy} ${h.l} ${h.cy}` : `M${s.cx} ${s.b} L${h.cx} ${h.t}`);
    }
    const fromHub = (a) => {
      if (a.l > h.r) {
        const mx = (h.r + a.l) / 2;
        return `M${h.r} ${h.cy} C${mx} ${h.cy} ${mx} ${a.cy} ${a.l} ${a.cy}`;
      }
      // the accounts sit under magpie: a lane down their left, from below the agents too
      const x = a.l - 12, y = Math.max(h.b, low - 8);
      return `M${h.cx} ${h.b} L${h.cx} ${y} C${h.cx} ${y + 22} ${x} ${y + 2} ${x} ${y + 24} L${x} ${a.cy - 10} Q${x} ${a.cy} ${a.l} ${a.cy}`;
    };
    // a group in the group: its models hang from its heading, a lane down
    // from where the wire to it ends (under the heading) to each
    const fromSub = (s, a) => {
      const p = b(s.li), x = p.l + 9;
      return `M${p.l} ${p.cy} Q${x} ${p.cy} ${x} ${p.cy + 9} L${x} ${a.cy - 9} Q${x} ${a.cy} ${a.l} ${a.cy}`;
    };
    for (const s of subs.values()) {
      const up = s.up && subs.get(s.up);
      s.wire.setAttribute("d", up ? fromSub(up, b(s.li)) : fromHub(b(s.li)));
    }
    for (const row of rows.values()) {
      const up = row.up && subs.get(row.up), a = b(row.li);
      row.wire.setAttribute("d", up ? fromSub(up, a) : fromHub(a));
    }
  }

  // seated: a route's accounts and keys each in a place of its own, not in
  // the order routing weighed them this time — the group's members in the
  // group's order, a provider's fallbacks after its own, then by name — so
  // the column holds still while the one put first moves.
  // One whose vendor doesn't list the model to it is never asked, so it
  // isn't drawn — only told of, among why it went where it did.
  function seated(r) {
    const members = r.group?.members || [];
    const key = (w) => {
      let m = members.indexOf(w.provider + "/" + w.model);
      if (m < 0) m = members.findIndex((x) => x.startsWith(w.provider + "/"));
      return [w.fallback ? 1 : 0, m < 0 ? members.length : m, w.name || w.provider, w.aside ? 1 : 0, w.who || "", w.id];
    };
    const cmp = (a, b) => {
      const x = key(a), y = key(b);
      for (let i = 0; i < x.length; i++) if (x[i] !== y[i]) return typeof x[i] === "number" ? x[i] - y[i] : String(x[i]).localeCompare(String(y[i]));
      return 0;
    };
    return [...r.order].sort(cmp);
  }

  // a seat is one of a route's keys or accounts for one model: two of a
  // group's models on one provider go over the same keys, and are two seats
  const seat = (x) => x.id + "\u0000" + (x.model || "");
  // tried is the seat a try went to
  const tried = (r, tr) => r.order.find((x) => seat(x) === seat(tr)) || r.order.find((x) => x.id === tr.id);
  const setOf = (r) => r.order.map(seat).sort().join("\n");

  // staged: the routes the stage shows — a picked one alone; else those
  // playing, and each agent's latest while it lingers, a few agents at most
  function staged() {
    if (pinned) return [pinned];
    const n = now(), last = new Map();
    const rs = [...routes.values()].sort((a, b) => a.id - b.id);
    for (const r of rs) last.set(r.agent, r);
    const out = rs.filter((r) => playing.has(r.id) || (last.get(r.agent) === r && (!r.done || at(r.time) + (r.ms || 0) > n - LINGER)));
    const c = cur && routes.get(cur.id);
    if (c && !out.includes(c)) out.push(c);
    const ags = [...new Set(out.map((r) => r.agent))].slice(-4);
    return out.filter((r) => ags.includes(r.agent)).sort((a, b) => a.id - b.id);
  }

  // hueOf is the colour an agent's requests and answers fly in, and it is
  // marked with: its maker's own where it has one, else one of the rest
  // picked by its id, so it stays the same from one request to the next.
  const HUES = {
    claude: "#d97757", codex: "#6366f1", gemini: "#0ea5e9", copilot: "#a855f7", cursor: "#14b8a6", opencode: "#eab308",
    crush: "#ec4899", goose: "#84cc16", pi: "#10b981", omp: "#f43f5e", dsh: "#06b6d4", commandcode: "#f97316",
  };
  const SPARE = ["#8b5cf6", "#22c55e", "#e11d48", "#0891b2", "#ca8a04", "#db2777", "#2563eb", "#65a30d"];
  function hueOf(id) {
    if (HUES[id]) return HUES[id];
    let h = 0;
    for (const c of String(id || "")) h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return SPARE[h % SPARE.length];
  }

  function agentNode(id) {
    let a = agents.get(id);
    if (a) return a;
    const node = el("div", "rt-node rt-src"), ic = el("span", "rt-ic"), name = el("b"), sub = el("small");
    node.append(ic, name, sub);
    a = { node, ic, name, sub, wire: path() };
    node.style.setProperty("--agent", hueOf(id));
    a.wire.style.setProperty("--agent", hueOf(id));
    agents.set(id, a);
    return a;
  }

  // subNode is the heading of a group in the group, key its way down
  // ("fast>cheap"), at depth d
  function subNode(key, info, d) {
    let s = subs.get(key);
    if (!s) {
      const li = el("li", "rt-sub"), name = el("b"), id = el("code", "mdl"), mode = el("i");
      li.append(svg(FAN, 14, 1.5), name, id, mode);
      s = { li, name, id, mode, wire: path(), key };
      subs.set(key, s);
    }
    s.up = d ? key.slice(0, key.lastIndexOf(">")) : null;
    s.li.style.setProperty("--depth", d);
    s.name.textContent = info.name || info.id;
    s.id.textContent = "group/" + info.id;
    s.mode.textContent = t((MODES[info.routing || ""] || MODES[""])[0]);
    s.li.title = info.rules ? t(info.rules === 1 ? "1 rule" : "{n} rules", { n: info.rules }) : "";
    return s;
  }
  // wiresTo is the way from magpie to a row: through the headings of the
  // groups it is in, then its own wire
  function wiresTo(row) {
    const via = row.w.via || [], out = [];
    for (let d = 1; d <= via.length; d++) { const s = subs.get(via.slice(0, d).join(">")); if (s) out.push(s.wire); }
    return [...out, row.wire];
  }

  function makeRow(w) {
    const li = el("li"), b = el("b");
    b.append(icon(w.icon || (w.preset ? w.preset : "generic")));
    const name = el("span", "who", who(w));
    // the provider's name heads the card; a row names what differs
    const sub = el("span", "", w.fallback ? w.name : w.kind === "provider" ? "" : w.plan || "");
    b.append(name, " ", sub, el("code", "mdl", w.model));
    if (w.fallback) b.append(el("small", "fb", t("fallback")));
    const st = el("em"), bar = el("div", "bar"), bi = el("i"), tg = el("span", "tag");
    bar.append(bi);
    li.append(el("i", "dot"), b, st, bar, tg);
    li.title = w.id;
    return { li, wire: path(), st, bi, tg, w };
  }

  // sync puts the staged routes on the stage: each agent on the left, and
  // the accounts they weighed, each route's in the order it weighed them —
  // a new order of the same accounts moves them to their places, so it
  // shows. Accounts and agents stay where they are while they stay.
  function sync(force) {
    const rs = staged();
    if (!rs.length) return;
    if (force) return steady(() => rebuild(rs, true));
    rebuild(rs, false);
  }
  function rebuild(rs, force) {
    cur = routes.get((pinned || rs[rs.length - 1]).id) || pinned || rs[rs.length - 1];
    if (force) {
      for (const row of rows.values()) row.wire.remove();
      rows = new Map();
      for (const s of subs.values()) s.wire.remove();
      subs.clear();
      list.replaceChildren();
      for (const a of agents.values()) a.wire.remove();
      agents.clear();
      srcs.replaceChildren();
    }
    // agents
    const ags = [...new Set(rs.map((r) => r.agent))];
    for (const [id, a] of agents) if (!ags.includes(id)) { a.node.remove(); a.wire.remove(); agents.delete(id); }
    const keep = [...agents.keys()];
    const nodes = [...keep, ...ags.filter((a) => !keep.includes(a))].map((id) => agentNode(id).node);
    if (nodes.some((x, i) => srcs.children[i] !== x) || srcs.children.length !== nodes.length) srcs.replaceChildren(...nodes);
    srcs.classList.toggle("many", ags.length > 1);
    for (const id of ags) {
      const a = agents.get(id), r = rs.filter((x) => x.agent === id).pop(), ag = agentOf(id);
      if (a.icon !== (ag?.icon || "generic")) { a.icon = ag?.icon || "generic"; a.ic.replaceChildren(icon(a.icon)); }
      a.name.textContent = agentName(id);
      a.sub.textContent = r.model;
    }
    // the account sets, the latest of each
    const latest = new Map();
    for (const r of rs) latest.set(setOf(r), r);
    sets = [...sets.filter((k) => latest.has(k)), ...[...latest.keys()].filter((k) => !sets.includes(k))];
    const ids = [], wOf = new Map(), rOf = new Map();
    for (const k of sets) {
      const r = latest.get(k);
      for (const w of seated(r)) if (!ids.includes(seat(w))) ids.push(seat(w));
    }
    for (const r of rs) for (const w of [...r.order, ...(r.left || [])]) { wOf.set(seat(w), w); rOf.set(seat(w), r.id); }
    const before = new Map([...rows].map(([id, row]) => [id, row.li.getBoundingClientRect().top]));
    for (const [id, row] of rows) if (!ids.includes(id)) { row.li.remove(); row.wire.remove(); rows.delete(id); }
    if (list.querySelector(".idle")) list.replaceChildren();
    for (const id of ids) {
      let row = rows.get(id);
      if (!row) {
        row = makeRow(wOf.get(id));
        rows.set(id, row);
        if (before.size && !still()) row.li.classList.add("new");
      }
      row.w = wOf.get(id);
      row.rid = rOf.get(id);
    }
    // a group in the group heads its models, which it routes by its own
    // routing, set in under it
    const els = [], want = new Set(), infos = rs.flatMap((r) => r.group?.subs || []);
    let prev = [];
    for (const id of ids) {
      const row = rows.get(id), via = row.w.via || [];
      row.li.style.setProperty("--depth", via.length);
      row.up = via.length ? via.join(">") : null;
      via.forEach((g, d) => {
        const key = via.slice(0, d + 1).join(">");
        if (prev.slice(0, d + 1).join(">") === key || want.has(key)) return;
        want.add(key);
        els.push(subNode(key, infos.find((x) => x.id === g) || { id: g, name: g }, d).li);
      });
      els.push(row.li);
      prev = via;
    }
    for (const [k, s] of subs) if (!want.has(k)) { s.li.remove(); s.wire.remove(); subs.delete(k); }
    const mine = new Set(els);
    for (const li of [...list.children]) if (!mine.has(li)) li.remove();
    // moved only when the order changed: moving a node restarts what it plays
    if (els.some((e, i) => list.children[i] !== e) || list.children.length !== els.length) {
      for (const e of els) list.append(e);
    }
    // FLIP: from where each was to its new place
    let moved = false;
    for (const [id, row] of rows) {
      const dy = before.has(id) ? before.get(id) - row.li.getBoundingClientRect().top : 0;
      if (!dy || still()) continue;
      moved = true;
      row.li.style.transition = "none";
      row.li.style.transform = `translateY(${dy}px)`;
    }
    if (moved) {
      void list.offsetWidth;
      for (const row of rows.values()) { row.li.style.transition = ""; row.li.style.transform = ""; }
      flipUntil = performance.now() + 520;
    }
    header(cur, ags.length);
    layout();
  }

  // header tells of the route the log tells of: its provider or group,
  // and how it routes
  function header(r, many) {
    const f = r.order.find((x) => !x.fallback) || r.order[0];
    const g = r.group;
    const routing = g ? g.routing || "" : f?.routing || "";
    const m = MODES[routing] || MODES[""];
    chip.textContent = t(m[0]);
    chip.hidden = false;
    hubText();
    const on = r.order.filter((x) => !x.fallback).length;
    what.replaceChildren(el("b", "", g ? g.name : f?.name || r.provider),
      el("span", "", (g ? " · " + t("routing group") : "") + " · " + t(on === 1 ? "one on" : "{n} on", { n: on })
        + (many > 1 ? " · " + t("{n} agents at once", { n: many }) : "")));
    mode.textContent = g && routing === "order" ? t(GROUP_ORDER) : t(!routing && f?.kind === "key" && !g ? KEYS_SMART : m[1]);
  }

  // what a row says now: resting, answering, or what routing weighed it by
  function render() {
    if (!cur) return;
    hubText();
    const n = now(), rs = staged();
    const trying = new Set(), busy = new Set();
    for (const r of rs) for (const tr of r.tries) if (!tr.done) { trying.add(seat(tr)); busy.add(r.agent); }
    const onWire = new Set();
    for (const f of flying.values()) { onWire.add(f.id); busy.add(f.agent); }
    for (const [id, row] of rows) {
      // each row as the latest staged request that weighed it found it
      const r = routes.get(row.rid) || pinned || cur, answered = new Set(), rests = new Map(), gave = new Map();
      for (const w of r.order) if (w.rest) rests.set(w.id, w.rest);
      for (const tr of r.tries) {
        if (tr.done && tr.status < 400) answered.add(seat(tr));
        else if (tr.done && !tr.rest && !tr.again) gave.set(seat(tr), tr); // the error the agent got
        if (tr.rest) rests.set(tr.id, tr.rest);
      }
      for (const tr of r.tries) if (tr.done && tr.status < 400) rests.delete(tr.id); // it answered: whatever rest it began in is over
      const w = row.w, rest = rests.get(w.id), resting = rest && at(rest.until) > n;
      let s;
      if (w.unlisted) s = unlistedWord(w);
      else if (resting) s = `${failWord(rest.why)} · ${restWhen(rest)}`;
      else if (trying.has(id)) s = t("answering…");
      else if (answered.has(id)) s = agents.size > 1 ? t("answered {agent}", { agent: agentName(r.agent) }) : t("answered this request");
      else if (gave.has(id)) s = t("{status} · {fail} · passed to {agent}", { status: gave.get(id).status, fail: failWord(gave.get(id).fail), agent: agentName(r.agent) });
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        s = !w.routing && w.used >= 98 ? t("{n} used · all but used up", { n: pct(w.used) })
          : !w.routing && w.used >= 90 ? t("{n} used · kept for last", { n: pct(w.used) })
          : soon ? t("{n} used · renews in {d}", { n: pct(w.used), d: dur(soon - n) }) : t("{n} used", { n: pct(w.used) });
      } else if (w.kind === "account") s = t("what's left not known yet");
      else if (w.routing === "usage") s = t("{n} tokens lately", { n: tokens(w.tokens || 0) });
      else if (w.aside) s = t("{api} only · after the others", { api: API[w.speaks] || w.speaks || t("any API") });
      else if (w.speaks) s = t("{api} only", { api: API[w.speaks] || w.speaks });
      else s = w.kind === "key" ? t("API key") : t("one key");
      if (row.st.textContent !== s) row.st.textContent = s;
      const bar = w.kind === "account" && w.known;
      row.li.classList.toggle("nobar", !bar);
      row.bi.style.width = bar ? Math.min(100, w.used) + "%" : "0";
      const on = !resting && (trying.has(id) || answered.has(id) || onWire.has(id));
      row.li.classList.toggle("on", on);
      row.li.classList.toggle("low", !!(!w.routing && w.known && w.used >= 90));
      row.li.classList.toggle("rest", !!resting || gave.has(id));
      row.li.classList.toggle("left", !!w.unlisted);
      row.wire.classList.toggle("live", on);
      if (on) row.wire.style.setProperty("--agent", hueOf(r.agent));
      row.wire.classList.toggle("rest", !!resting || !!w.unlisted);
    }
    for (const s of subs.values()) {
      const lit = [...rows.values()].find((row) => row.li.classList.contains("on") && (row.up === s.key || row.up?.startsWith(s.key + ">")));
      s.li.classList.toggle("on", !!lit);
      s.wire.classList.toggle("live", !!lit);
      if (lit) s.wire.style.setProperty("--agent", lit.wire.style.getPropertyValue("--agent"));
    }
    for (const [id, a] of agents) a.wire.classList.toggle("live", busy.has(id));
  }

  // ---------- the log: how one request was routed ----------

  function renderLog() {
    const r = pinned || cur;
    log.hidden = !r;
    if (!r) return;
    logHead.replaceChildren(
      el("span", "", pinned ? t("How the request at {time} was routed", { time: clock(r.time) }) : t("How the last request was routed")),
      el("span", "grow"));
    if (pinned) {
      const live = el("button", "text", t("Back to live"));
      live.onclick = () => { pinned = null; cur = newest(); sync(true); renderAll(); };
      logHead.append(live);
    }
    const items = [];
    const main = r.order.find((x) => !x.fallback);
    items.push([r.group
      ? t("{agent} asked for the routing group {name}: {members}", { agent: agentName(r.agent), name: r.group.name, members: treeText(r.group) })
      : main && main.model !== r.model
      ? t("{agent} asked for {model}: {name} serves it, and the vendor is asked for {sent}", { agent: agentName(r.agent), model: r.model, name: main.name, sent: main.model })
      : t("{agent} asked for {model}", { agent: agentName(r.agent), model: r.model }) + " → " + (main?.name || r.provider), ""]);
    items.push([affWhy(r, true) || ruleWhy(r, true) || firstWhy(r), "why"]);
    for (const s of nestedWhy(r)) items.push([s, "why"]);
    for (const a of asides(r)) items.push([a, "aside"]);
    r.tries.forEach((_, i) => items.push([tryWhy(r, i), r.tries[i].done ? (r.tries[i].status < 400 ? "ok" : "bad") : "wait"]));
    if (r.done && !r.tries.length) items.push([t("Nothing was tried: {error}", { error: r.error || r.status }), "bad"]);
    steps.replaceChildren(...items.map(([s, c]) => el("li", c, s)));
  }

  // pick sets the stage to a past request, or back to live with the newest
  function pick(r) {
    pinned = r.id === newest()?.id ? null : r;
    gen++; trips = []; flying.clear(); playing.clear();
    for (const p of sky.querySelectorAll(".pkt, .rt-flier")) p.remove();
    cur = r;
    sync(true); renderAll();
    say(affWhy(r, true) || ruleWhy(r, true) || firstWhy(r));
    if (pinned) { scrollOnPurpose(); box.scrollIntoView({ block: "nearest", behavior: still() ? "auto" : "smooth" }); }
  }

  // who answered a request, or what its agent got
  function outcome(r) {
    if (!r.done) {
      const tr = r.tries[r.tries.length - 1], w = tr && tried(r, tr);
      return [w ? t("{who} is answering…", { who: `${who(w)} · ${w.model}` }) : t("routing…"), "wait"];
    }
    const ok = r.tries.find((tr) => tr.done && tr.status < 400), w = ok && tried(r, ok);
    if (r.status < 400) return [w ? `${who(w)} · ${w.model}` : r.provider, r.tries.length > 1 ? "moved" : "ok"];
    const last = r.tries[r.tries.length - 1];
    return [last ? `${r.status} · ${failWord(last.fail)}` : `${r.status || ""} ${r.error || ""}`.trim(), "bad"];
  }

  // the requests the gateway keeps, newest first: pick one to see how it was routed
  function renderHist() {
    const rs = [...routes.values()].sort((a, b) => b.id - a.id);
    hist.hidden = !rs.length;
    reqHead.replaceChildren(el("span", "label", t("Requests")), el("span", "grow"), reqNote);
    reqNote.textContent = t("the last {n} the gateway keeps", { n: rs.length });
    reqs.replaceChildren(...rs.map((r) => {
      const [said, how] = outcome(r);
      const b = el("button", "rt-req " + how);
      const sel = pinned ? pinned.id === r.id : cur?.id === r.id;
      b.setAttribute("aria-pressed", String(sel));
      const ag = agentOf(r.agent);
      const when = el("span", "at", new Date(r.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
      const asked = el("span", "asked");
      const sw = el("i", "ag");
      sw.style.setProperty("--agent", hueOf(r.agent));
      asked.append(sw, icon(ag?.icon || "generic"), el("span", "m", r.model));
      const to = el("span", "to");
      to.append(el("i"), el("span", "", said));
      const meta = [];
      if (r.tries.length > 1) meta.push(t("{n} tries", { n: r.tries.length }));
      if (r.done && r.ms) meta.push(took(r.ms));
      if (r.tokens) meta.push(t("{n} tokens", { n: tokens(r.tokens) }));
      b.append(when, asked, to, el("span", "meta", meta.join(" · ")));
      b.title = `${agentName(r.agent)} · ${r.model} → ${r.provider}`;
      b.onclick = () => pick(r);
      return b;
    }));
    renderActs(rs);
  }

  // each account or key the kept requests weighed: how often it was
  // tried, answered and failed in them, and how the latest found it
  function renderActs(rs) {
    const by = new Map();
    for (const r of [...rs].reverse()) { // oldest first, so the latest wins
      r.order.forEach((w, i) => {
        const a = by.get(w.id) || { w, tried: 0, ok: 0, fails: {}, last: 0, rest: null, restAt: 0, seen: 0, pos: 0, models: new Set() };
        a.w = w; a.seen++; a.pos = i; a.at = r.time;
        // a later request found it resting, or not
        if (at(r.time) >= a.restAt) { a.rest = w.rest || null; a.restAt = at(r.time); }
        by.set(w.id, a);
      });
      for (const tr of r.tries) {
        const a = by.get(tr.id);
        if (!a || !tr.done || tr.fail === "canceled") continue; // the agent's doing, not its
        a.tried++;
        const end = at(tr.start) + (tr.ms || 0);
        if (tr.status < 400) { a.ok++; a.last = Math.max(a.last, end); const m = tr.model || r.order.find((x) => x.id === tr.id)?.model; if (m) a.models.add(m); }
        else a.fails[tr.fail || "other"] = (a.fails[tr.fail || "other"] || 0) + 1;
        if (tr.rest && end >= a.restAt) { a.rest = tr.rest; a.restAt = end; }
      }
    }
    const list = [...by.values()].sort((x, y) => (x.w.name || "").localeCompare(y.w.name || "") || x.w.provider.localeCompare(y.w.provider) || x.pos - y.pos);
    actHead.replaceChildren(el("span", "label", t("Accounts and keys")), el("span", "grow"), actNote);
    actNote.textContent = t("over those requests");
    const n = now();
    let prov = "";
    const out = [];
    for (const a of list) {
      const w = a.w;
      if (w.provider !== prov) {
        prov = w.provider;
        const h = el("div", "rt-prov");
        h.append(icon(w.icon || w.preset || "generic"), el("b", "", w.name || w.provider));
        const m = MODES[list.find((x) => x.w.provider === prov && !x.w.fallback)?.w.routing || ""] || MODES[""];
        h.append(el("span", "", t(w.kind === "key" && !w.routing ? "Smart" : m[0])));
        out.push(h);
      }
      const row = el("div", "rt-act");
      const name = el("div", "nm");
      name.append(el("b", "", who(w)), el("span", "", w.kind === "provider" ? w.model : w.plan || (w.kind === "key" ? t("API key") : "")));
      let st, cls = "";
      const resting = a.rest && at(a.rest.until) > n;
      if (resting) { st = `${failWord(a.rest.why)} · ${restWhen(a.rest)}`; cls = "rest"; }
      else if (w.unlisted) { st = unlistedWord(w); cls = "left"; }
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        st = soon && soon <= n ? t("{n} used at {time}; it has renewed since", { n: pct(w.used), time: clock(a.at) })
          : (soon ? t("{n} used · renews in {d}", { n: pct(w.used), d: dur(soon - n) }) : t("{n} used", { n: pct(w.used) })) + " · " + t("as of {time}", { time: clock(a.at) });
      } else if (w.kind === "account") st = t("what's left not known yet");
      else st = "";
      const tally = el("div", "tally");
      const fails = Object.entries(a.fails).map(([k, v]) => `${v} ${failWord(k)}`);
      tally.append(
        el("span", "", t("tried {n}", { n: a.tried })),
        el("span", "ok", t("answered {n}", { n: a.ok })),
        ...(fails.length ? [el("span", "bad", fails.join(", "))] : []),
        ...(a.last ? [el("span", "", t("last answered {time}", { time: clock(a.last) }))] : []),
        ...[...a.models].map((m) => el("code", "mdl", m)));
      row.append(name, el("div", "st " + cls, st), tally);
      if (w.kind === "account" && w.known) {
        const bar = el("div", "bar"), bi = el("i");
        bi.style.width = Math.min(100, w.used) + "%";
        bar.append(bi);
        row.append(bar);
      }
      row.title = w.id;
      out.push(row);
    }
    acts.replaceChildren(...out);
  }

  function renderAll() { render(); renderLog(); renderHist(); }
  const newest = () => [...routes.values()].reduce((a, b) => (!a || b.id > a.id ? b : a), null);

  // ---------- playing a request ----------

  const flying = new Map(); // packet → { id: the row it is at, agent }

  // A magpie, drawn to fly: facing right, the dot it carries at the tip
  // of its beak where the flight puts it; its wings beat from the shoulder.
  const FLIER = '<g class="rt-lift"><g transform="scale(1.2) translate(-6.5 3.2)">'
    + '<path class="wing far" d="M-14.5 -3.4C-16.5 -9.5 -14.2 -15 -8.6 -18.6C-9.4 -12.6 -9.6 -7.4 -9.2 -3.2Z"/>'
    + '<path class="tail" d="M-31.5 3.1L-18.6 -1.8L-17.4 1.2L-30.8 4.6Z"/>'
    + '<ellipse class="body" cx="-12.2" cy="-1.4" rx="7.6" ry="3.9"/>'
    + '<ellipse class="belly" cx="-12.8" cy="0.4" rx="4.3" ry="1.6"/>'
    + '<circle class="body" cx="-4.9" cy="-3.7" r="3.1"/>'
    + '<path class="body" d="M-2.3 -4.8L1.4 -3.3L-2.3 -2.2Z"/>'
    + '<g class="wing near"><path d="M-15.6 -3.2C-17.8 -10.2 -15.2 -16.4 -8.4 -20.4C-9.3 -13.6 -9.4 -7.8 -8.8 -2.8Z"/>'
    + '<path class="bar" d="M-14.6 -5.6C-15.4 -10.4 -13.8 -14.4 -10.4 -17.2"/></g>'
    + "</g></g>";

  function bird(kind) {
    if (still() || !shown()) return null;
    const g = document.createElementNS(NS, "g");
    g.setAttribute("class", "rt-flier " + kind);
    g.innerHTML = FLIER;
    sky.appendChild(g);
    return g;
  }
  // off it goes, up and away, once it has let go of the dot
  function away(b) {
    if (!b) return;
    b.classList.add("away");
    setTimeout(() => b.remove(), 450);
  }
  function packet() {
    const dot = document.createElementNS(NS, "circle");
    dot.setAttribute("r", 4.5);
    dot.setAttribute("class", "pkt");
    dot.setAttribute("visibility", "hidden"); // until a flight puts it somewhere
    sky.appendChild(dot);
    return dot;
  }

  // play: one magpie carries the request from the agent through magpie to
  // who routing put first, and lets it go there while it answers; another
  // picks up the answer and brings it back — a failure only as far as
  // magpie, where the first takes the request on to the next.
  async function play(id) {
    let r = routes.get(id);
    const g = gen;
    playing.add(id);
    cur = r;
    sync();
    say(affWhy(r, true) || ruleWhy(r, true) || firstWhy(r));
    const aside = asides(r).find((s) => s);
    if (aside) say(aside, true);
    renderAll();
    const A = agents.get(r.agent);
    const dot = packet();
    dot.style.setProperty("--agent", hueOf(r.agent));
    // from: where in magpie the dot is, once it is — the way it came in
    let carrier = bird("req"), from = null, back = null;
    // rt: the route as the gateway has it now — or, dropped from what it
    // keeps, as it was last, over
    const rt = () => routes.get(id) || { ...r, done: true, tries: r.tries.map((x) => ({ ...x, done: true })) };
    try {
      tick(A.node);
      if (!r.tries.length) { // routing hasn't picked yet: to magpie, to wait there
        await fly(dot, carrier, [{ p: A.wire }], 620);
        from = tip(A.wire, true);
        tick(hub);
      }
      for (let i = 0; g === gen; ) {
        r = rt();
        if (!routes.has(id)) break;
        if (i >= r.tries.length) {
          if (r.done) break;
          await until(() => g !== gen || rt().tries.length > i || rt().done);
          continue;
        }
        const row = rows.get(seat(r.tries[i]));
        if (!row) { i++; continue; }
        flying.set(dot, { id: seat(r.tries[i]), agent: r.agent });
        if (!carrier) carrier = bird("req");
        if (from) tick(hub);
        const ws = wiresTo(row);
        const out = [via(from || tip(A.wire, true), tip(ws[0], false)), ...ws.map((p) => ({ p }))];
        await fly(dot, carrier, from ? out : [{ p: A.wire }, ...out], from ? 900 : 1400);
        if (g !== gen) break;
        // let go at the account: it waits there while it answers
        away(carrier);
        carrier = null;
        dot.classList.add("held");
        await until(() => g !== gen || rt().tries[i].done);
        if (g !== gen) break;
        r = rt();
        if (!routes.has(id)) break;
        const tr = r.tries[i];
        dot.classList.remove("held");
        if (tr.status < 400) {
          // the reply's tokens are counted once the route is done
          await until(() => g !== gen || rt().done);
          if (g !== gen) break;
          r = rt();
          say(tryWhy(r, i), true);
          renderAll();
          dot.classList.add("back");
          dot.setAttribute("r", 4);
          back = bird("res");
          await fly(dot, back, home(row, A), 1400);
          flying.delete(dot);
          away(back);
          tick(A.node);
          break;
        }
        row.li.classList.remove("hit"); void row.li.offsetWidth; row.li.classList.add("hit", "tagged");
        row.tg.textContent = `${tr.status} · ${failWord(tr.fail)}`;
        setTimeout(() => row.li.classList.remove("tagged"), 1800);
        say(tryWhy(r, i));
        renderAll();
        dot.classList.add("back", "err");
        back = bird("res");
        if (!tr.rest && !tr.again) { // that was the answer: the agent gets the error
          await fly(dot, back, home(row, A), 1350);
          flying.delete(dot);
          away(back);
          tick(A.node);
          break;
        }
        // back to magpie, which hands the request on to the next
        await fly(dot, back, [...ws].reverse().map((p) => ({ p, rev: true })), 620);
        flying.delete(dot);
        away(back);
        dot.classList.remove("back", "err");
        from = tip(ws[0], false, true);
        i++;
      }
    } finally { // however it ended, nothing of it stays behind
      away(carrier);
      away(back);
      flying.delete(dot);
      dot.remove();
      playing.delete(id);
      renderAll();
    }
  }

  // home: from an account back through magpie to the agent
  const home = (row, A) => {
    const ws = wiresTo(row);
    return [...[...ws].reverse().map((p) => ({ p, rev: true })), via(tip(ws[0], false, true), tip(A.wire, true, true)), { p: A.wire, rev: true }];
  };

  // ---------- the loop ----------

  // pose puts a flight's dot e of the way along, and its magpie with it,
  // heading the way it flies — turned about to fly left, tilted no more
  // than a bird banks
  function pose(tr, e) {
    for (const l of tr.legs) if (l.j) l.j();
    const lens = tr.legs.map((l) => l.p.isConnected && l.p.getAttribute("d") && l.p.getTotalLength?.() || 0);
    const total = lens.reduce((a, b) => a + b, 0);
    const point = (d) => {
      d = Math.max(0, Math.min(total, d));
      let i = 0;
      // a leg not laid out has nowhere to be on: passed over
      while (i < lens.length - 1 && (d > lens[i] || !lens[i])) { d -= lens[i]; i++; }
      const l = tr.legs[i];
      return l.p.getPointAtLength(l.rev ? lens[i] - d : d);
    };
    if (!total) return;
    const d = e * total, pt = point(d);
    tr.dot.setAttribute("cx", pt.x);
    tr.dot.setAttribute("cy", pt.y);
    tr.dot.removeAttribute("visibility");
    const b = tr.bird;
    if (!b) return;
    const p0 = point(d - 3), p1 = point(d + 3);
    let vx = p1.x - p0.x, vy = p1.y - p0.y;
    if (Math.abs(vx) + Math.abs(vy) < .01) { vx = b._vx ?? (tr.legs[0].rev ? -1 : 1); vy = 0; }
    b._vx = vx;
    const aim = Math.max(-24, Math.min(24, Math.atan2(vy, Math.abs(vx)) * 180 / Math.PI));
    b._a = b._a === undefined || b._flip !== (vx < 0) ? aim : b._a + (aim - b._a) * .12;
    b._flip = vx < 0;
    b.setAttribute("transform", `translate(${pt.x.toFixed(1)} ${pt.y.toFixed(1)}) scale(${b._flip ? -1 : 1} 1) rotate(${b._a.toFixed(1)})`);
  }

  function frame(ts) {
    if (shown()) {
      const now_ = trips; trips = [];
      for (const tr of now_) {
        if (tr.g !== gen) { tr.res(); continue; }
        const k = tr.ms ? Math.min(1, Math.max(0, (ts - tr.t0) / tr.ms)) : 1;
        try {
          pose(tr, (1 - Math.cos(Math.PI * k)) / 2); // eased in and out, as a bird glides to land
        } catch { tr.res(); continue; } // one that can't be flown ends, and the rest fly on
        if (k >= 1) tr.res(); else trips.push(tr);
      }
      if (ts < flipUntil) layout();
      if (capQ.length && ts - capAt > (capLo && !capQ[0].lo ? 500 : 1700)) show(capQ.shift());
    } else {
      for (const tr of trips) tr.res();
      trips = [];
      if (capQ.length) { show(capQ[capQ.length - 1]); capQ = []; }
    }
    requestAnimationFrame(frame);
  }
  // countdowns tick once a second
  setInterval(() => { if (shown()) { if (!pinned && loaded && cur) sync(); render(); renderActs([...routes.values()].sort((a, b) => b.id - a.id)); } }, 1000);

  function offline(msg) {
    off.textContent = msg;
    off.hidden = !msg;
    for (const e of [top, stage, foot, log]) e.hidden = !!msg;
    more.hidden = !!msg;
  }

  function empty() {
    offline("");
    what.replaceChildren(el("b", "", t("Waiting for a request")));
    mode.textContent = t("Send one from any agent routed through magpie and it plays here as it happens: who routing put first and why, each try, and what each answered.");
    for (const a of agents.values()) a.wire.remove();
    agents.clear();
    const a = agentNode("");
    a.ic.replaceChildren(icon("generic"));
    a.name.textContent = t("your agent");
    a.sub.textContent = "";
    srcs.replaceChildren(a.node);
    for (const row of rows.values()) row.wire.remove();
    rows = new Map();
    for (const s of subs.values()) s.wire.remove();
    subs.clear();
    chip.hidden = true;
    hubText();
    list.replaceChildren(el("li", "idle", t("No request yet")));
    say(t("Every request an agent sends to magpie shows up here, routed for real."));
    log.hidden = hist.hidden = true;
    layout();
  }

  async function poll() {
    for (;;) {
      try {
        const res = await fetch(`/api/gateway/trace?after=${seq}${loaded ? "&wait=1" : ""}`);
        const d = await res.json();
        skew = at(d.now) - Date.now();
        mine = d.mine;
        hubText();
        statB[0].textContent = d.totals.requests;
        statB[1].textContent = d.totals.rerouted;
        statB[2].textContent = d.totals.errors;
        if (!mine) {
          offline(t(providers?.gateway?.running ? "Another magpie serves the gateway; its routing plays live in that magpie's window." : "The gateway isn't running, so nothing is routed."));
          loaded = false;
          await new Promise((r) => setTimeout(r, 5000));
          continue;
        }
        if (d.seq < seq) routes.clear(); // the gateway started over
        seq = d.seq;
        const first = !loaded;
        const fresh = [];
        for (const r of d.routes) {
          if (!routes.has(r.id) && !first) fresh.push(r.id);
          routes.set(r.id, r);
        }
        for (const id of [...routes.keys()].sort((a, b) => a - b).slice(0, -60)) routes.delete(id);
        loaded = true;
        if (first) {
          offline("");
          // the agents' names come with the app's state, which may not be here yet
          for (let i = 0; i < 30 && !state.agents.length; i++) await new Promise((res) => setTimeout(res, 100));
          const r = newest();
          if (r) { cur = r; sync(true); say(affWhy(r, true) || ruleWhy(r, true) || firstWhy(r)); renderAll(); } else empty();
        } else {
          if (cur && routes.has(cur.id)) cur = routes.get(cur.id);
          if (pinned && routes.has(pinned.id)) pinned = routes.get(pinned.id);
          for (const id of fresh) if (!pinned) play(id);
          wake();
          renderAll();
        }
      } catch {
        await new Promise((r) => setTimeout(r, 3000));
      }
    }
  }

  function hubText() { hubSub.textContent = (providers?.gateway?.url || "").replace(/^https?:\/\//, "") || t("gateway"); }

  // labels in the page's language, and again when it changes
  function words() {
    for (const s of stats.children) s.lastChild.textContent = t(s.dataset.label);
    hubText();
    // the caption said before, said again in these words: it was set as text
    if (loaded) { if (cur) { sync(true); renderAll(); capQ = []; say(affWhy(cur, true) || ruleWhy(cur, true) || firstWhy(cur)); } else empty(); }
    renderGroups();
  }
  new MutationObserver(words).observe(document.documentElement, { attributes: true, attributeFilter: ["lang"] });

  // ---------- routing groups ----------
  // The groups agents can pick as one model (group/<id>): the user's, and
  // those magpie found — one model several providers serve. Each is made,
  // changed or removed here; changing one magpie found makes it the user's.
  // Below them, each provider with several keys or accounts on, which
  // routes over them already: how, and how long a conversation stays.
  const gsec = el("div", "rt-gsec");
  const gHead = el("div", "row-head"), gList = el("div", "list rt-groups");
  const pHead = el("div", "row-head"), pList = el("div", "list rt-pools");
  gsec.append(gHead, gList, pHead, pList);
  more.prepend(gsec);
  const ROUTE_OPTS = [["", "Smart"], ["order", "In order"], ["rotate", "In turn"], ["usage", "Least used"]];
  const AFF_OPTS = [["", "Auto"], ["session", "Session"], ["turn", "Within a turn"], ["off", "Off"]];
  const AFF_HINT = {
    "": "A conversation stays with the account or key that answered it while what the vendor cached of it is worth keeping — within a turn always, across turns while it's fresh.",
    session: "A conversation stays with the account or key that answered it for the whole session, while it can answer.",
    turn: "A conversation stays put within a turn, while the agent sends tool results back; when you speak again, routing decides afresh.",
    off: "Every request is routed afresh, whoever answered its conversation before.",
  };
  const GROUP_HINT = {
    "": "Smart, over every member's accounts and keys together: of the subscriptions with quota to spare, the one whose allowance renews soonest goes first; one resting after a failure goes last.",
    order: "In order: the first model until it can't answer, then the next — each over its own accounts or keys as its provider routes them.",
    rotate: "In turn: each conversation's next turn goes to the next member's account or key, spreading the load.",
    usage: "Least used first: the account or key with the most of its allowance left goes first.",
  };
  const EFFORTS = ["low", "medium", "high", "xhigh", "max"]; // provider.Efforts
  // what a rule matches, in words
  function ruleText(r) {
    const bits = [];
    if (r.tokens) bits.push(t("≥ {n} tokens", { n: r.tokens.toLocaleString() }));
    if (r.images) bits.push(t("has an image"));
    if (r.effort) bits.push(r.effort === "on" ? t("reasoning on") : t("reasoning ≥ {level}", { level: r.effort }));
    if (r.agents?.length) bits.push(r.agents.map((id) => (state.clients || state.agents || []).find((a) => a.id === id)?.name || id).join(" / "));
    if (r.intent) bits.push(t("asks for “{intent}”", { intent: r.intent }));
    return bits.join(" · ");
  }
  const slug = (s) => String(s || "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  let groups = null, gEdit = null; // gEdit: { id: "" for a new one, draft }
  async function loadGroups() {
    try { groups = await api("groups"); } catch { return; }
    if (!gEdit && !gsec.contains(document.activeElement)) renderGroups(); // not under someone's hands
  }
  async function groupAction(action, body, ok) {
    try {
      groups = await api("groups/" + action, body);
      gEdit = null;
      renderGroups();
      if (ok) status(ok, "ok");
      load(); // the gateway's model list, the agents' pickers
    } catch (e) { status(e.message, "err"); }
  }
  const modelOf = (id) => groups?.models.find((m) => m.id === id);
  // a routing group among a group's members: group/<id>
  const subOf = (id) => id?.startsWith("group/") ? groups?.groups.find((x) => "group/" + x.id === id && !x.hidden) : null;
  const groupIcons = (g) => [...new Map((g.memberInfo || []).filter((i) => i.icon).map((i) => [i.provider || i.icon, i.icon])).values()];
  const memberIcon = (id) => { const s = subOf(id); return s ? stackIcon(groupIcons(s)) : icon(modelOf(id)?.icon || "generic"); };
  const memberName = (id) => { const s = subOf(id), m = modelOf(id); return s ? s.name : m ? m.name || m.id : id; };
  const memberNote = (id) => subOf(id) ? t("routing group") : modelOf(id)?.providerName;
  function memberLabel(g, id) {
    const i = g.memberInfo?.find((x) => x.id === id), m = modelOf(id), s = subOf(id);
    if (s) return `${t("routing group")} · ${s.name}`;
    if (m) return `${m.providerName} · ${m.name || m.id}`;
    return i?.name ? `${i.name} · ${i.model}` : id;
  }
  function renderGroups() {
    if (groups) steady(drawGroups);
  }
  function drawGroups() {
    const newBtn = el("button", "text", t("New group"));
    newBtn.onclick = () => { gEdit = { id: "", draft: { name: "", members: [], routing: "", affinity: "", rules: [] } }; renderGroups(); };
    gHead.replaceChildren(el("span", "label", t("Routing groups")), el("span", "grow"), el("span", "note", t("models agents pick as one")), newBtn);
    const rows = [];
    if (gEdit && !gEdit.id) rows.push(groupEditor(null));
    const shown = groups.groups.filter((g) => !g.hidden), hidden = groups.groups.filter((g) => g.hidden);
    for (const g of shown) rows.push(gEdit?.id === g.id ? groupEditor(g) : groupRow(g));
    if (!rows.length) rows.push(el("div", "none rt-gnone", t("No group yet. A model two of your providers serve becomes one on its own; New group makes one of any models you like.")));
    if (hidden.length) {
      const h = el("div", "rt-ghidden");
      h.append(el("span", "", t("Removed:")));
      for (const g of hidden) {
        const b = el("button", "text", g.id.replace(/^auto-/, ""));
        b.title = t("Bring it back");
        b.onclick = () => groupAction("show", { id: g.id }, t("{name} is back", { name: g.id }));
        h.append(b);
      }
      rows.push(h);
    }
    gList.replaceChildren(...rows);
    renderPools();
  }
  function groupRow(g) {
    const row = el("div", "rt-group" + (g.ready ? "" : " off"));
    const ics = el("span", "ics");
    ics.append(stackIcon(groupIcons(g)));
    const main = el("div", "main");
    const nm = el("div", "nm");
    nm.append(el("b", "", g.name), el("code", "mdl", "group/" + g.id));
    if (g.auto) nm.append(el("small", "auto", t("found by magpie")));
    const sep = g.routing === "order" ? " → " : " · ";
    const mem = el("div", "mem", g.members.map((id) => memberLabel(g, id)).join(sep));
    main.append(nm, mem);
    const m = ROUTE_OPTS.find(([id]) => id === (g.routing || "")) || ROUTE_OPTS[0];
    const tags = el("span", "tags");
    tags.append(el("span", "tag", t(m[1])));
    if (g.affinity) tags.append(el("span", "tag", t(AFF_OPTS.find(([id]) => id === g.affinity)?.[1] || "")));
    if (g.rules?.length) {
      const r = el("span", "tag", t(g.rules.length === 1 ? "1 rule" : "{n} rules", { n: g.rules.length }));
      r.title = g.rules.map((x, i) => `${i + 1}. ${ruleText(x)} → ${memberLabel(g, x.use)}`).join("\n");
      tags.append(r);
    }
    if (!g.ready) tags.append(el("span", "tag bad", t("no member ready")));
    const edit = el("button", "text", t("Edit"));
    edit.onclick = (e) => { e.stopPropagation(); open(); };
    const open = () => { gEdit = { id: g.id, draft: { name: g.name, members: [...g.members], routing: g.routing || "", affinity: g.affinity || "", classifier: g.classifier || "", effort: g.effort || "", rules: (g.rules || []).map((r) => ({ ...r, intent: r.intent || "", agents: [...(r.agents || [])] })) } }; renderGroups(); };
    row.onclick = open;
    row.append(ics, main, tags, edit);
    return row;
  }
  function groupEditor(g) {
    const d = gEdit.draft;
    const ed = el("div", "editor rt-gedit");
    const h = el("div", "ehead");
    h.append(el("b", "", g ? g.name : t("New group")));
    if (g?.auto) h.append(el("span", "note", t("found by magpie — saving a change makes it yours")));
    ed.append(h);
    const keys = (i) => { i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") { gEdit = null; renderGroups(); } else if (e.key === "Enter" && i === name) save(); }; return i; };
    const name = keys(input(d.name, t("e.g. Opus anywhere")));
    const idHint = el("div", "hint");
    // an existing group's id can change (an auto- one found by magpie too);
    // a new one's is made from its name
    if (g && d.id === undefined) d.id = g.id;
    const idIn = g ? keys(input(d.id, g.id)) : null;
    const idOf = () => {
      if (g) return slug(d.id) || g.id;
      let id = slug(d.name) || "group", n = 1;
      const base = id;
      while (groups.groups.some((x) => x.id === id)) id = `${base}-${++n}`;
      return id;
    };
    const showId = () => {
      idHint.textContent = t("Agents pick it as {id}", { id: "group/" + idOf() }) +
        (g && idOf() !== g.id ? " · " + t("an agent set to {id} needs setting again", { id: "group/" + g.id }) : "");
    };
    name.oninput = () => { d.name = name.value; showId(); };
    const nw = el("div");
    nw.append(name);
    if (!g) nw.append(idHint);
    ed.append(el("label", "", t("Name")), nw);
    if (idIn) {
      idIn.oninput = () => { d.id = idIn.value; showId(); };
      idIn.onblur = () => { d.id = idIn.value = idOf(); showId(); };
      const iw = el("div");
      iw.append(idIn, idHint);
      ed.append(el("label", "", t("ID")), iw);
    }
    showId();

    // members, in order: the first is what an agent is told the model can do.
    // More are picked with the model picker the agents use.
    const box = el("div", "fallback");
    const list = el("div", "fbl");
    const addBtn = el("button", "rt-gadd");
    addBtn.append(svg(PLUS, 11, 1.8), el("span", "", t("Add a model")));
    const draw = () => {
      list.replaceChildren();
      d.members.forEach((id, i) => {
        const m = modelOf(id), s = subOf(id);
        const row = el("div", "fbrow");
        const n = el("span", "n");
        n.append(el("span", "", memberName(id)));
        if (m || s) n.append(el("small", "", memberNote(id)));
        row.append(el("span", "i", String(i + 1)), memberIcon(id), n, el("span", "grow"));
        if (s) row.title = s.members.map((x) => memberLabel(s, x)).join(s.routing === "order" ? " → " : " · ");
        if (!m && !s) { row.classList.add("off"); row.title = t("No provider serves {id} now; it is skipped", { id }); }
        if (i) { const up = el("button", "text", t("Up")); up.onclick = () => { d.members.splice(i - 1, 0, d.members.splice(i, 1)[0]); draw(); }; row.append(up); }
        const rm = el("button", "text", t("Remove"));
        rm.onclick = () => { d.members.splice(i, 1); d.rules = d.rules.filter((r) => d.members.includes(r.use)); draw(); drawRules(); };
        row.append(rm);
        list.append(row);
      });
      addBtn.querySelector("span").textContent = t(d.members.length ? "Add another model" : "Add a model");
    };
    addBtn.onclick = (ev) => {
      // a group in it may be any other, but never one it is in already:
      // that would put it in itself
      const subs = groups.groups.filter((x) => !x.hidden && x.id !== g?.id && !(g && x.holds?.includes(g.id)) && !d.members.includes("group/" + x.id))
        .map((x) => ({ value: "group/" + x.id, label: x.name, note: "group/" + x.id, icons: groupIcons(x), group: ROUTING_GROUPS, ref: "group/" + x.id }));
      const options = [...subs, ...groups.models.filter((x) => !d.members.includes(x.id))
        .map((x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.providerName, ref: x.id }))];
      openPicker({ id: "", name: "", fields: [] }, { key: "member", label: "model", value: "", options, onPick: (id) => {
        if (id && !d.members.includes(id)) d.members.push(id);
        draw(); drawRules();
      } }, addBtn, ev);
    };
    box.append(list, addBtn);
    draw();
    const mw = el("div");
    mw.append(box, el("div", "hint", t("The first answers for what the model can do. In order, they are tried top first.")));
    ed.append(el("label", "", t("Models")), mw);

    const rHint = el("div", "hint", t(GROUP_HINT[d.routing] || GROUP_HINT[""]));
    const rw = el("div");
    rw.append(segs(ROUTE_OPTS.map(([id, n]) => [id, t(n)]), d.routing, (v) => { d.routing = v; rHint.textContent = t(GROUP_HINT[v] || GROUP_HINT[""]); }), rHint);
    ed.append(el("label", "", t("Routing")), rw);
    const aHint = el("div", "hint", t(AFF_HINT[d.affinity] || AFF_HINT[""]));
    const aw = el("div");
    aw.append(segs(AFF_OPTS.map(([id, n]) => [id, t(n)]), d.affinity, (v) => { d.affinity = v; aHint.textContent = t(AFF_HINT[v] || AFF_HINT[""]); }), aHint);
    ed.append(el("label", "", t("Stays")), aw);

    // rules: which member a turn goes to first, by what the request shows
    const rbox = el("div", "fallback rt-rules");
    const rlist = el("div", "fbl");
    const rAdd = el("button", "rt-gadd");
    rAdd.append(svg(PLUS, 11, 1.8), el("span", "", t("Add a rule")));
    const infoOf = (id) => g?.memberInfo?.find((x) => x.id === id);
    const pickFrom = (label, anchor, ev, options, onPick) => openPicker({ id: "", name: "", fields: [] }, { key: "rule", label, value: "", menu: true, options, onPick }, anchor, ev);
    const rHint2 = el("div", "hint");
    const drawRules = () => {
      rlist.replaceChildren();
      rAdd.hidden = d.members.length < 2;
      rHint2.textContent = t(d.members.length < 2 ? "With two models or more, a rule can send some turns to one of them first." : "Checked top first when you send a message: the first that matches sends that turn to its model first; the rest stay behind it if it fails. A turn under way is never moved.");
      d.rules.forEach((r, i) => {
        const row = el("div", "rt-rule");
        const when = el("div", "when");
        when.append(el("span", "w", t("When")));
        // tokens: at least this long
        const tk = el("span", "rt-cond tk" + (r.tokens ? " on" : ""));
        tk.onclick = () => ti.focus();
        const ti = input(r.tokens ? String(r.tokens) : "", "", "number");
        ti.min = "0"; ti.step = "1000";
        ti.oninput = () => { r.tokens = Math.max(0, parseInt(ti.value, 10) || 0); tk.classList.toggle("on", !!r.tokens); warn(); };
        ti.onkeydown = (e) => e.stopPropagation();
        tk.append(el("span", "", "≥"), ti, el("span", "", t("tokens")));
        // images
        const im = el("button", "rt-cond" + (r.images ? " on" : ""), t("has an image"));
        im.onclick = () => { r.images = !r.images; im.classList.toggle("on", r.images); warn(); };
        // reasoning
        const effortName = (v) => v === "on" ? t("reasoning on") : v ? t("reasoning ≥ {level}", { level: v }) : t("any reasoning");
        const ef = el("button", "rt-cond" + (r.effort ? " on" : ""), effortName(r.effort));
        ef.onclick = (ev) => pickFrom("reasoning", ef, ev, [
          { value: "", label: t("any reasoning"), note: t("not a condition") },
          { value: "on", label: t("reasoning on"), note: t("thinking or any effort level") },
          ...EFFORTS.map((v) => ({ value: v, label: t("reasoning ≥ {level}", { level: v }) })),
        ], (v) => { r.effort = v; ef.textContent = effortName(v); ef.classList.toggle("on", !!v); });
        // agents
        const clients = state.clients || state.agents || [];
        const agentsName = () => r.agents.length ? r.agents.map((id) => clients.find((a) => a.id === id)?.name || id).join(", ") : t("any agent");
        const ag = el("button", "rt-cond" + (r.agents.length ? " on" : ""), agentsName());
        ag.onclick = (ev) => pickFrom("agent", ag, ev, [
          { value: "", label: t("any agent"), note: t("not a condition") },
          ...clients.map((a) => ({ value: a.id, label: a.name, icon: a.icon, note: r.agents.includes(a.id) ? "✓" : "" })),
        ], (v) => {
          r.agents = !v ? [] : r.agents.includes(v) ? r.agents.filter((x) => x !== v) : [...r.agents, v];
          ag.textContent = agentsName(); ag.classList.toggle("on", r.agents.length > 0);
        });
        // intent: what the message asks for, as the classifier judges it
        const it = el("span", "rt-cond in" + (r.intent ? " on" : ""));
        it.onclick = () => ii.focus();
        const ii = input(r.intent || "", t("what it asks for, e.g. writing tests"));
        ii.maxLength = 200;
        ii.oninput = () => { r.intent = ii.value; it.classList.toggle("on", !!r.intent.trim()); drawClassifier(); };
        ii.onkeydown = (e) => e.stopPropagation();
        it.append(el("span", "", t("asks for")), ii);
        when.append(tk, im, ef, ag, it);
        // the member it sends to
        const use = el("div", "rt-use");
        const ub = el("button", "rt-cond on");
        const drawUse = () => { const note = memberNote(r.use); ub.replaceChildren(memberIcon(r.use), el("span", "", note ? `${memberName(r.use)} · ${note}` : r.use)); };
        drawUse();
        ub.onclick = (ev) => pickFrom("member", ub, ev, d.members.map((id) => { const s = subOf(id); return { value: id, label: memberName(id), note: memberNote(id), icon: s ? undefined : modelOf(id)?.icon, icons: s ? groupIcons(s) : undefined }; }),
          (v) => { r.use = v; drawUse(); warn(); });
        const hint = el("span", "rt-rwarn");
        const warn = () => {
          const m = infoOf(r.use), bits = [];
          if (r.images && m && m.ready && !m.images) bits.push(t("it doesn't take images"));
          if (r.tokens && m?.context && m.context < r.tokens) bits.push(t("it takes {n} tokens", { n: m.context.toLocaleString() }));
          hint.textContent = bits.join(" · ");
        };
        warn();
        use.append(el("span", "w", t("send to")), ub, hint);
        const ctl = el("span", "ctl");
        if (i) { const up = el("button", "text", t("Up")); up.onclick = () => { d.rules.splice(i - 1, 0, d.rules.splice(i, 1)[0]); drawRules(); }; ctl.append(up); }
        const rm = el("button", "text", t("Remove"));
        rm.onclick = () => { d.rules.splice(i, 1); drawRules(); };
        ctl.append(rm);
        row.append(el("span", "i", String(i + 1)), when, use, ctl);
        rlist.append(row);
      });
      drawClassifier();
    };
    rAdd.onclick = () => { d.rules.push({ use: d.members[d.members.length - 1], tokens: 0, images: false, effort: "", agents: [], intent: "" }); drawRules(); };
    // the classifier, once a rule has an intent or Jev picks the effort:
    // the model asked which intent a turn's message is. Jev (a decision
    // provider's model) answers both in one call.
    const cls = el("div", "rt-classifier");
    const clabel = el("label", "");
    const deciders = groups.deciders || [];
    const isJev = (id) => deciders.some((x) => x.id === id);
    const drawClassifier = () => {
      const auto = d.effort === "auto";
      const on = auto || d.rules.some((r) => r.intent?.trim());
      cls.hidden = clabel.hidden = !on;
      if (!on) return;
      const intents = d.rules.some((r) => r.intent?.trim());
      clabel.textContent = t(auto && !intents ? "Decided by" : "Intent told by");
      const m = [...deciders, ...groups.models].find((x) => x.id === d.classifier);
      const cb = el("button", "rt-cond" + (d.classifier ? " on" : ""));
      if (d.classifier) cb.append(icon(m?.icon || "generic"), el("span", "", m ? `${m.name || m.id} · ${m.providerName}` : d.classifier));
      else cb.append(el("span", "", t("choose a model")));
      const opt = (x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.providerName, ref: x.id });
      cb.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "classifier", label: "model", value: d.classifier, options: [...deciders.map(opt), ...(auto ? [] : groups.models.map(opt))],
        onPick: (id) => { if (id) d.classifier = id; drawClassifier(); } }, cb, ev);
      cls.replaceChildren(cb,
        el("div", "hint", t(isJev(d.classifier) && !intents
          ? "Jev is asked once as each turn begins, with the message and what it said of the turn before. Its calls show in the usage as magpie’s own."
          : isJev(d.classifier)
          ? "As a turn begins, Jev is asked once which of the intents the message is, and how hard the turn is when it picks the effort. An intent it isn't sure of matches no rule. Its calls show in the usage as magpie’s own."
          : "As a turn begins, this model is asked which of the intents the message is — once; a small, fast one without reasoning is best. If it fails or can't say, no intent matches. Its calls show in the usage as magpie’s own.")));
    };
    rbox.append(rlist, rAdd);
    const rw2 = el("div");
    rw2.append(rbox, rHint2);
    ed.append(el("label", "", t("Rules")), rw2);
    // the effort: the agent's, or Jev's pick for each turn
    const eHint = el("div", "hint");
    const ew = el("div");
    const drawEffort = () => {
      eHint.textContent = t(d.effort === "auto"
        ? "As a turn begins, Jev rates how hard it is and the turn's requests ask their model for low, medium, high or xhigh reasoning — the level each model has nearest. Only where the agent asked for reasoning: a request without any (a session title) stays without."
        : deciders.length ? "Each request reasons as much as the agent asked." : "Each request reasons as much as the agent asked. Add TypeSafe Jev in Providers to have it pick each turn's effort.");
    };
    ew.append(segs([["", t("Agent's")], ["auto", t("Jev picks")]], d.effort, (v) => {
      if (v === "auto" && !deciders.length) { status(t("Add TypeSafe Jev in Providers first"), "warn"); v = ""; }
      d.effort = v;
      if (v === "auto" && !isJev(d.classifier)) d.classifier = deciders[0].id;
      drawEffort(); drawClassifier();
    }), eHint);
    drawEffort();
    ed.append(el("label", "", t("Effort")), ew);
    const cw = el("div");
    cw.append(cls);
    ed.append(clabel, cw);
    drawRules();

    const bar = el("div", "bar");
    if (g) {
      const del = el("button", "text danger", t("Remove"));
      del.onclick = () => groupAction("delete", { id: g.id }, t("{name} removed", { name: g.name }));
      bar.append(del);
    }
    bar.append(el("span", "grow"));
    const cancel = el("button", "text", t("Cancel"));
    cancel.onclick = () => { gEdit = null; renderGroups(); };
    const saveBtn = el("button", "text primary", t(g ? "Save" : "Add"));
    const save = () => {
      if (!d.members.length) { addBtn.focus({ preventScroll: true }); return status(t("A group needs a model in it"), "warn"); }
      d.rules.forEach((r) => { r.intent = (r.intent || "").trim(); });
      const bare = d.rules.findIndex((r) => !r.tokens && !r.images && !r.effort && !r.agents.length && !r.intent);
      if (bare >= 0) return status(t("Rule {n} needs a condition", { n: bare + 1 }), "warn");
      if (d.rules.some((r) => r.intent) && !d.classifier) return status(t("Choose the model that tells which intent a message is"), "warn");
      if (d.effort === "auto" && !isJev(d.classifier)) return status(t("Only Jev picks the effort: choose it as the group's classifier"), "warn");
      saveBtn.classList.add("busy");
      groupAction("save", { id: idOf(), from: g?.id, name: d.name.trim() || idOf(), members: d.members, routing: d.routing, affinity: d.affinity, rules: d.rules, effort: d.effort, classifier: d.rules.some((r) => r.intent) || d.effort === "auto" ? d.classifier : "", context: g?.context || 0 }, t(g ? "{name} saved" : "{name} added", { name: d.name.trim() || idOf() }));
    };
    saveBtn.onclick = save;
    bar.append(cancel, saveBtn);
    ed.append(bar);
    if (!g) setTimeout(() => name.focus({ preventScroll: true }), 0); // WebKit would scroll the page to put it mid-view
    return ed;
  }
  // the providers that route over several accounts or keys of their own
  function renderPools() {
    const ps = groups?.pools || [];
    pHead.hidden = pList.hidden = !ps.length;
    pHead.replaceChildren(el("span", "label", t("Several accounts or keys")), el("span", "grow"), el("span", "note", t("each provider routes over its own")));
    pList.replaceChildren(...ps.map((p) => {
      const row = el("div", "rt-pool");
      const nm = el("div", "nm");
      nm.append(icon(p.icon || "generic"), el("b", "", p.name), el("span", "", p.protocol
        ? t("{n} keys for {api}", { n: p.who.length, api: API[p.protocol] || p.protocol })
        : t(p.kind === "account" ? "{n} accounts" : "{n} keys", { n: p.who.length })));
      const ctl = el("div", "ctl");
      const set = async (what, body, ok) => {
        try { await api("provider/" + what, body); status(ok, "ok"); groups = await api("groups"); load(); } catch (e) { status(e.message, "err"); }
      };
      const lab = (s, c) => { const w = el("span", "lab"); w.append(el("small", "", s), c); return w; };
      ctl.append(
        lab(t("Routing"), segs(ROUTE_OPTS.map(([id, n]) => [id, t(n)]), p.routing || "", (v) => set("route", { id: p.provider, routing: v }, t("{name}: {routing}", { name: p.name, routing: t(ROUTE_OPTS.find(([id]) => id === v)[1]) })))),
        lab(t("Stays"), segs(AFF_OPTS.map(([id, n]) => [id, t(n)]), p.affinity || "", (v) => set("affinity", { id: p.provider, affinity: v }, t("{name}: {routing}", { name: p.name, routing: t(AFF_OPTS.find(([id]) => id === v)[1]) })))));
      row.append(nm, ctl);
      row.title = p.who.join(", ");
      return row;
    }));
  }
  // loaded when the view is shown, and again when the window comes back
  new MutationObserver(() => { if (!$("#view-routing").hidden) loadGroups(); }).observe($("#view-routing"), { attributes: true, attributeFilter: ["hidden"] });
  window.addEventListener("focus", () => { if (shown()) loadGroups(); });
  loadGroups();

  // ---------- hiding emails, for a screenshot to share ----------
  // Each email address on the page — an account's, in a row, a sentence,
  // a tooltip — is swapped for blurred stand-in letters while it's on, as the
  // page redraws too; the address itself is kept aside to put back.
  const view = $("#view-routing"), maskBtn = $("#rtMask");
  const EMAIL = /[\w.+-]+@[\w-]+(?:\.[\w-]+)+/g, IS_EMAIL = new RegExp(EMAIL.source);
  // stand-in letters of the address's shape, the same each time it's drawn:
  // blurred, they read as a name without being one
  const dots = (s) => { let h = 7; return s.replace(/[^@.]/g, (c) => (h = (h * 31 + c.charCodeAt(0)) >>> 0, "aeiounrstlcmdh"[h % 14])); };
  let masked = false;
  try { masked = localStorage.getItem("magpie.maskEmails") === "1"; } catch {}
  function mask() {
    const walk = document.createTreeWalker(view, NodeFilter.SHOW_TEXT), found = [];
    for (let n; (n = walk.nextNode());) if (n.data.includes("@") && IS_EMAIL.test(n.data) && !n.parentElement?.closest(".pii")) found.push(n);
    for (const n of found) {
      const bits = [];
      let last = 0;
      for (const m of n.data.matchAll(EMAIL)) {
        if (m.index > last) bits.push(n.data.slice(last, m.index));
        const s = el("span", "pii", dots(m[0]));
        s.dataset.raw = m[0];
        bits.push(s);
        last = m.index + m[0].length;
      }
      if (!last) continue;
      if (last < n.data.length) bits.push(n.data.slice(last));
      // one piece still, where the text was: in a flex row each would
      // otherwise stand as an item of its own
      if (bits.length > 1) { const run = el("span", "pii-run"); run.append(...bits); n.replaceWith(run); }
      else n.replaceWith(...bits);
    }
    for (const e of view.querySelectorAll("[title]")) {
      if (!e.title.includes("@") || !IS_EMAIL.test(e.title)) continue;
      e.dataset.piiTitle = e.title;
      e.title = e.title.replace(EMAIL, (m) => m.replace(/[^@.]/g, "•")); // a tooltip can't blur
    }
  }
  function unmask() {
    for (const s of view.querySelectorAll(".pii")) s.replaceWith(s.dataset.raw);
    for (const r of view.querySelectorAll(".pii-run")) r.replaceWith(r.textContent);
    view.normalize();
    for (const e of view.querySelectorAll("[data-pii-title]")) { e.title = e.dataset.piiTitle; delete e.dataset.piiTitle; }
  }
  // what the page redraws is masked before it's painted; masking isn't
  // itself watched, so it can't set itself off again
  const OBS = { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["title"] };
  const watch = new MutationObserver(() => {
    if (!masked) return;
    watch.disconnect();
    mask();
    watch.observe(view, OBS);
  });
  function setMasked(on) {
    masked = on;
    try { localStorage.setItem("magpie.maskEmails", on ? "1" : "0"); } catch {}
    maskBtn.setAttribute("aria-pressed", String(on));
    view.classList.toggle("masked", on);
    if (on) { mask(); watch.observe(view, OBS); }
    else { watch.disconnect(); unmask(); }
  }
  maskBtn.onclick = () => {
    setMasked(!masked);
    // pixelated in when asked for, not again each time the page redraws
    view.classList.add("masking");
    clearTimeout(maskBtn._t);
    maskBtn._t = setTimeout(() => view.classList.remove("masking"), 450);
  };
  setMasked(masked);
  new ResizeObserver(() => layout()).observe(stage);
  words();
  requestAnimationFrame(frame);
  poll();
})();
