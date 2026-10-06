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
    if (k === "errors your agent saw") {
      // the newest request whose agent got an error, to see what it got
      s.classList.add("rt-errs");
      s.title = t("Show the latest request that failed");
      s.addEventListener("click", () => {
        const r = listed().find(failedRoute);
        if (r) pick(r);
      });
    }
    statB.push(b);
    stats.append(s);
  }
  foot.append(cap, stats);
  const log = el("div", "rt-log");
  const logHead = el("div", "rt-log-head");
  const steps = el("ol", "rt-steps");
  log.append(logHead, steps);
  const off = el("div", "none rt-off");
  // over the stage while a replay plays: the time it is replaying, which
  // requests are in flight then, and where it is among them
  const rbar = el("div", "rt-replay");
  const rTop = el("div", "rp-top"), rClock = el("b", "rp-clock"), rDay = el("small", "rp-day"), rSkip = el("span", "rp-skip");
  const rSpeed = el("button", "text"), rStop = el("button", "text");
  rTop.append(el("span", "rp-tag"), rClock, rDay, rSkip, el("span", "grow"), rSpeed, rStop);
  const rWhat = el("div", "rp-what");
  const rTrack = el("div", "rp-track"), rHead = el("i", "rp-head");
  rTrack.append(rHead);
  rbar.append(rTop, rWhat, rTrack);
  rbar.hidden = true;
  box.append(top, rbar, stage, foot, log, off);

  // under the stage: every request the gateway keeps, and each account or
  // key as those requests found it
  const more = $("#rtMore");
  const reqHead = el("div", "row-head rt-req-head"), reqNote = el("span", "note");
  const reqs = el("div", "list rt-reqs");
  // the days the history keeps on disk, to look back at one: see listed
  const dayBar = el("div", "rt-days");
  // the days past the bar's width go under one more pill, whose menu lists
  // them (#961): the bar scrolled with its scrollbar hidden, so they were cut
  // off and out of reach of a mouse wheel. The day looked at stays in
  // sight: when it is one of them, the pill is named by it
  const moreDays = el("button", "rt-day rt-day-more");
  moreDays.type = "button";
  moreDays.setAttribute("aria-haspopup", "menu");
  moreDays.setAttribute("aria-expanded", "false");
  let unseenDays = [];
  moreDays.onclick = (e) => {
    e.stopPropagation();
    if (moreDays.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(moreDays, unseenDays.map((d) => ({ v: d.day, name: dayName(d.day), literalName: true, note: String(d.requests) })),
      day, (v) => { if (v !== day) lookAt(v); }, "Earlier days", "rt-days-menu");
  };
  function nameMoreDays() {
    const picked = unseenDays.find((d) => d.day === day);
    moreDays.classList.toggle("on", !!picked);
    moreDays.setAttribute("aria-pressed", String(!!picked));
    moreDays.replaceChildren(el("span", "", picked ? dayName(picked.day) : t("Earlier days")),
      ...(picked ? [el("small", "", String(picked.requests))] : []), svg(CHEV, 10, 1.6));
  }
  function fitDays() {
    if (!dayBar.clientWidth) return; // out of sight: fitted when it shows
    const pills = [...dayBar.children].filter((x) => x !== moreDays);
    for (const x of pills) x.hidden = false;
    unseenDays = [];
    if (dayBar.scrollWidth <= dayBar.clientWidth + 1) { moreDays.remove(); return; }
    dayBar.append(moreDays);
    // pills[0] is Live, pills[i] days[i - 1], newest first: the oldest go
    for (let i = pills.length - 1; i >= 1; i--) {
      pills[i].hidden = true;
      unseenDays.unshift(days[i - 1]);
      nameMoreDays();
      if (dayBar.scrollWidth <= dayBar.clientWidth + 1) break;
    }
  }
  new ResizeObserver(() => fitDays()).observe(dayBar);
  const groupBar = el("div", "rt-group-by");
  let bySession = false;
  try { bySession = localStorage.getItem("magpie.routingBySession") === "1"; } catch {}
  const groupButtons = [[false, "By request"], [true, "By session"]].map(([on, label]) => {
    const b = el("button", "rt-day");
    b.dataset.label = label;
    b.onclick = () => {
      bySession = on;
      try { localStorage.setItem("magpie.routingBySession", on ? "1" : "0"); } catch {}
      steady(renderHist);
    };
    groupBar.append(b);
    return b;
  });
  const actHead = el("div", "row-head"), actNote = el("span", "note");
  const acts = el("div", "list rt-acts");
  const actLabel = el("span", "label");
  actHead.append(actLabel, el("span", "grow"), actNote);
  const hist = el("div", "rt-cols");
  const colA = el("div", "rt-col"), colB = el("div", "rt-col");
  const filters = el("div", "rt-filters");
  const purposeTools = el("div", "rt-purpose-tools");
  const purposePick = el("button", "text rt-purpose"), purposeLabel = el("span");
  purposePick.type = "button";
  purposePick.id = "rtPurpose";
  purposePick.setAttribute("aria-haspopup", "menu");
  purposePick.setAttribute("aria-expanded", "false");
  purposePick.append(purposeLabel, svg(CHEV, 11, 1.6));
  const purposeClear = el("button", "text rt-purpose-clear");
  purposeClear.type = "button";
  purposeClear.id = "rtPurposeClear";
  purposeClear.append(svg(CROSS, 11, 1.6));
  purposeTools.append(purposePick, purposeClear);
  const metricOptions = [["duration", "Duration"], ["ttft", "First token"], ["tokens", "Tokens"],
    ["cache", "Cache hit rate"], ["speed", "Output speed"], ["cost", "Cost"]];
  let visibleMetrics = metricOptions.map(([key]) => key);
  try {
    const saved = JSON.parse(localStorage.getItem("magpie.routingMetrics"));
    if (Array.isArray(saved)) visibleMetrics = metricOptions.map(([key]) => key).filter((key) => saved.includes(key));
  } catch {}
  const metricPick = el("button", "text rt-metric-pick"), metricLabel = el("span");
  metricPick.type = "button";
  metricPick.id = "rtMetrics";
  metricPick.setAttribute("aria-haspopup", "menu");
  metricPick.setAttribute("aria-expanded", "false");
  metricPick.append(metricLabel, svg(CHEV, 11, 1.6));
  metricPick.onclick = (e) => {
    e.stopPropagation();
    if (metricPick.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(metricPick, metricOptions.map(([v, name]) => ({ v, name, note: "" })), visibleMetrics, (keys) => {
      visibleMetrics = keys;
      try { localStorage.setItem("magpie.routingMetrics", JSON.stringify(keys)); } catch {}
      steady(renderHist);
    }, "Metrics to show", "rt-metric-menu", "right", true);
  };
  let purpose = "";
  purposeClear.onclick = () => {
    closeProtoMenu();
    purpose = "";
    steady(followListed);
    purposePick.focus({ preventScroll: true });
  };
  filters.append(dayBar, groupBar);
  colA.append(reqHead, filters, reqs);
  colB.append(actHead, acts);
  hist.append(colA, colB);
  more.append(hist);

  const path = () => { const p = document.createElementNS(NS, "path"); wires.appendChild(p); return p; };
  const setText = (e, s) => { if (e.textContent !== s) e.textContent = s; };
  const tick = (e) => { e.classList.remove("tick"); void e.offsetWidth; e.classList.add("tick"); };

  // ---------- words ----------

  let skew = 0; // the gateway's clock less this page's
  // now is the gateway's time — or, in a replay, the time it is replaying
  const now = () => rp ? rp.real : Date.now() + skew;
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
  // (the formats made once: made for each call, they were much of what the
  // accounts' countdowns cost)
  const HM = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit" }), WD = new Intl.DateTimeFormat([], { weekday: "short" });
  function clock(s) {
    const d = new Date(s), n = new Date();
    const hm = HM.format(d);
    return d.toDateString() === n.toDateString() ? hm : WD.format(d) + " " + hm;
  }
  // how long a reply took to begin, and how fast it wrote after (#196):
  // none for one whose window from its first content to its end is under
  // 100 ms or would have it write over 10,000 tok/s, as it came in one
  // burst at its end (usage.DecodeWindow, #731: a whole Gemini tool call,
  // 8264 tokens 1 ms before the end, read 8,264,000 tok/s)
  const speedOf = (out, ms, ttft) => out > 0 && ttft > 0 && ms - ttft >= 100 && out * 1000 <= 10000 * (ms - ttft) ? out / ((ms - ttft) / 1000) : 0;
  function promptOf(r) {
    let prompt = 0, read = 0;
    // Input excludes both cache tiers. Include writes in the denominator,
    // and every billable try, as the request's cost does. Older history
    // without the tiers cannot tell a hit rate, even when it has tokens.
    for (const u of r.usage || []) {
      read += u.cache_read || 0;
      prompt += (u.in || 0) + (u.cache_read || 0) + (u.cache_write || 0);
    }
    return { prompt, read };
  }
  function tokenBreakdown(rs) {
    let input = 0, output = 0, read = 0, write = 0, recorded = 0;
    for (const r of rs) {
      if (!r.usage?.length) continue;
      recorded++;
      for (const u of r.usage) {
        input += u.in || 0; output += u.out || 0;
        read += u.cache_read || 0; write += u.cache_write || 0;
      }
    }
    if (!recorded) return t("No token breakdown was recorded");
    // The recorded tiers are disjoint: cached input is not counted again
    // as uncached input, nor is reasoning counted again outside output.
    const lines = [t("Token breakdown"), ...[["Input (uncached)", input], ["Output", output], ["Cache read", read], ["Cache write", write]]
      .map(([label, n]) => t(label) + ": " + ledNum(n))];
    if (rs.some((r) => r.tries.length > 1)) lines.push(t("Includes token usage from billable retries; the row totals show the final attempts"));
    if (recorded < rs.length) lines.push(t("Breakdown available for {n} of {total} requests", { n: recorded, total: rs.length }));
    return lines.join("\n");
  }
  function requestMetrics(r, how) {
    const { prompt, read } = r.done ? promptOf(r) : { prompt: 0, read: 0 };
    // Both times start at the request, so subtracting them leaves the
    // reply's decode window even after a retry. Output is the reply's,
    // not the prompt or the output of earlier billable tries.
    const speed = r.done && how !== "bad" ? speedOf(r.out, r.ms, r.ttft) : 0;
    const values = {
      duration: ["duration", "Duration", r.done && r.ms ? took(r.ms) : "—", ""],
      ttft: ["ttft", "First token", r.done && r.ttft ? took(r.ttft) : "—", t("First token")],
      tokens: ["tokens", "Tokens", r.tokens ? tokens(r.tokens) : "—", tokenBreakdown([r])],
      cache: ["cache-hit", "Cache", prompt ? pct(100 * read / prompt) : "—",
        t("Cache hit rate") + ": " + t("The share of the prompt read from the cache")],
      speed: ["speed", "Speed", speed ? t("{n} tok/s", { n: Math.round(speed) }) : "—",
        t("Output tokens a second after the first, over the streamed replies")],
      cost: ["cost", "Cost", routeCost(r), r.priced ? costNote() : t("No known price or token counts for this request")],
    };
    return visibleMetrics.map((key) => values[key]).filter((metric) => metric[2] !== "—");
  }
  function metricElement([cls, label, value, help], compact = false) {
    const metric = el("span", "rt-metric " + (cls === "cost" ? "cost-metric" : cls));
    if (!compact || cls === "ttft" || cls === "cache-hit") metric.append(el("span", "k", t(label)));
    metric.append(el("span", "v" + (cls === "cost" ? " cost" : ""), value));
    if (compact && cls === "tokens") metric.append(el("span", "k", t("{n} tokens", { n: "" }).trim()));
    metric.title = help || t(label);
    return metric;
  }
  function firstNote(r, tr) {
    let s = tr.ttft ? " · " + t("first token in {ms}", { ms: took(tr.ttft) }) : "";
    if (tr.ttft && tr.firstText > tr.ttft) s += " · " + t("first text in {ms}", { ms: took(tr.firstText) });
    const v = speedOf(r.out, tr.ms, tr.ttft);
    if (v) s += " · " + t("{n} tok/s", { n: Math.round(v) });
    const { prompt, read } = promptOf(r);
    if (prompt) s += " · " + t("request cache hit rate {p}", { p: pct(100 * read / prompt) });
    return s;
  }
  const tokens = (n) => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(Math.round(n));
  const pct = (n) => Math.round(n) + "%";
  // an account's window as Settings' allowance display has it, how much is
  // used or how much is left, the bar filling with the same (#602)
  const share = (w) => quotaLeft ? 100 - Math.max(0, Math.min(100, w.used)) : w.used;
  // the account's own count before it, when its vendor counts in amounts
  // (WorkBuddy's credits, #659): "355 / 500 credits · 71% used"
  const quota = (w, used, left, vars) => (w.limit > 0 ? quotaCount(w) + " · " : "") + t(quotaLeft ? left : used, { n: pct(share(w)), ...vars });
  const fill = (w) => Math.max(0, Math.min(100, share(w))) + "%";
  const FAIL = { rate: "rate limited", credit: "out of credit", quota: "quota used up", other: "failed", auth: "sign-in required", canceled: "canceled", foreign: "another account's reasoning", floor: "reply too short", verify: "needs verification", refused: "refused (safety filter)", shape: "request not understood", proxy: "proxy not reachable", effort: "reasoning effort not in its plan", overflow: "too long for its model", slow: "slow to start", prompt: "agent's prompt turned away" };
  const failWord = (why) => t(FAIL[why] || "failed");
  // a try that answered: one whose reply broke off after it began (its 200
  // sent, then the vendor's error, #733) didn't, and has a fail
  const tryOk = (tr) => tr.status < 400 && !tr.fail;
  const API = { anthropic: "Anthropic", chat: "OpenAI", responses: "OpenAI Responses", gemini: "Gemini" };
  const MODES = {
    "": ["Smart", "Smart: of the accounts with quota to spare, the one whose allowance renews soonest goes first — what it has left would be lost at the reset. The week decides; an account with five hours and no week goes by its five hours. One at 90% or more waits until the others can't answer; one resting after a failure goes last."],
    order: ["In order", "In order: the first answers everything until it can't; then the next."],
    rotate: ["In turn", "In turn: each conversation's next turn goes to the account after the one that answered its last, and a new conversation starts one further along; the requests within a turn stay put, keeping the prompt cache."],
    usage: ["Least used", "Least used first: the account with the most of its allowance left goes first; a key by the tokens magpie sent it lately."],
    pace: ["Weekly pace", "Weekly pace: the account with the most of its week left per hour until it renews goes first — the one with the most to lose at its reset; an account with five hours and no week by what its five hours have left per hour. One at 90% or more waits until the others can't answer; a key by the tokens magpie sent it lately."],
    manual: ["Manual", "Manual: every request goes to the model picked on the group's card, over its own accounts or keys."],
  };
  const GROUP_ORDER = "In order: member by member, the first model the group names until it can't answer, each over its own accounts or keys as its provider routes them.";
  const KEYS_SMART = "Smart: keys that suit the request go first — one made for the model's own API — then in their order. One resting after a failure goes last.";

  const agentOf = (id) => (state.clients || state.agents).find((a) => a.id === id);
  const agentName = (id) => agentOf(id)?.name || (id && id !== "other" ? id : t("your agent"));
  // who names an account or key in a sentence
  const who = (w) => w.kind === "provider" ? w.name : w.who;
  // where names one as a place a request went: the provider, and the
  // account or key when it has one
  const where = (w) => w.kind === "provider" || !w.who ? w.name || w.provider : `${w.name || w.provider} · ${w.who}`;
  // why one is left out: an account's plan lacks the model; a key's list
  // from its vendor does — relays list each key its own group's models
  // or the user set the account or key to serve other models only (#474)
  // or it is held at the usage cap the user set on the account
  const unlistedWord = (w) => w.held ? t("not an account the gateway key may use") : w.noCredits ? t("held: its allowance used up, set not to spend credits") : w.capped ? t("held at its {cap}% usage cap", { cap: w.capped }) : w.barred ? t("set to serve other models, not {model}", { model: w.model }) : w.kind === "key" ? t("{name}'s list for this key has no {model}", { name: w.name, model: w.model }) : t("its plan doesn't list {model}", { model: w.model });
  const group = (w) => w.used >= 98 ? "spent" : w.used >= 90 ? "low" : "fine";
  const renews = (w) => (w.renews || []).map((s) => known0(s) ? at(s) : 0);
  // renewsBy is renews as Smart ranks them: an auto-used Codex reset that
  // starts the windows again sooner (restarts) is when they renew (#718)
  const renewsBy = (w) => {
    const rs = known0(w.restarts) ? at(w.restarts) : 0;
    return renews(w).map((x) => rs && (!x || rs < x) ? rs : x);
  };

  // cmpRenews orders two accounts' windows as the gateway does: the
  // biggest first, to the hour, one not known after those known. It says
  // which window decided, too.
  function cmpRenews(a, b) {
    const ra = renewsBy(a), rb = renewsBy(b), H = 3600e3;
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
    return left < 3600e3 ? t("back in {d}", { d: dur(left) }) : t("back at {time} · in {d}", { time: clock(rest.until), d: dur(left) });
  }
  // restHow says how long a failed account sits out, and what said so.
  function restHow(rest, from) {
    const d = dur(at(rest.until) - from), time = clock(rest.until);
    switch (rest.by) {
      case "retry-after": return t("It rests {d}, as the vendor's Retry-After says", { d });
      case "credit": return t("It sits out half an hour, until someone tops it up");
      case "window": return t("Its allowance is used up: it rests until that renews, at {time}", { time });
      case "resets": return t("It rests until {time}, when the vendor says the limit resets", { time });
      case "quota": return t("It rests 15 minutes: out of quota, with no word of when it resets");
      case "verify": return t("The vendor wants the account verified first: it rests half an hour, or until you say it's verified");
      case "backoff": return rest.why === "rate"
        ? t("It was rate limited again as soon as it was back ({n} times in a row): it rests {d}, longer each time until it answers", { n: rest.failures, d })
        : rest.failures > 1
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
    if (r.sealedTask && r.leadAccount === f.id) return t("{who} goes first: it answered the parent agent and may be needed to read this encrypted task.", { who: w });
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
      case "weight": return t("By weight: it's {who}'s share — each key takes requests as its weight says.", { who: w });
      case "usage":
        if (f.kind === "account" && f.known) return t("Least used first: {who} has the most of its allowance left — {n} used.", { who: w, n: pct(f.used) });
        if (f.kind === "key") return t("Least used first: {who} served the fewest tokens lately — {n}.", { who: w, n: tokens(f.tokens || 0) });
        return t("Least used first: {who} goes first.", { who: w });
      case "pace":
        if (f.kind === "account" && f.known) {
          // The allowance window used for pace, which may be shorter than a week.
          const left = known0(f.due) ? Math.min(100, Math.round((f.pace || 0) * Math.max(1, (at(f.due) - at(r.time)) / 36e5))) : null;
          if (left !== null && f.dueBy === "reset") return t("Weekly pace: {who} has the most remaining allowance per hour until one of its Codex resets about to run out is used by itself — {n} left, used in {d}.", { who: w, n: pct(left), d: dur(at(f.due) - at(r.time)) });
          return left !== null
            ? t("Weekly pace: {who} has the most remaining allowance per hour until reset — {n} left, resets in {d}.", { who: w, n: pct(left), d: dur(at(f.due) - at(r.time)) })
            : t("Weekly pace: {who} has the most remaining allowance per hour until reset — {n} used.", { who: w, n: pct(f.used) });
        }
        if (f.kind === "key") return t("Weekly pace: {who} served the fewest tokens lately — {n}.", { who: w, n: tokens(f.tokens || 0) });
        return t("Weekly pace: {who} goes first.", { who: w });
    }
    if (f.kind === "key") {
      if (!f.fit && peers.some((p) => p.fit > 0)) return t("{who} goes first: it's made for {api}, the API {model} is at home in, so nothing is translated.", { who: w, api: API[f.speaks] || f.speaks, model: f.model });
      return restedTo(t("the others go in their order.")) || t("{who} goes first: keys go in their order, those that suit the request first.", { who: w });
    }
    if (f.kind !== "account") return t("{who} goes first.", { who: w });
    if (f.learns && peers.some((p) => p.known)) return t("{who} goes first: what it has left isn't known yet, and its answer tells — kept behind those known, it would never answer and never be known.", { who: w });
    if (!f.known && !peers.some((p) => p.known)) return t("The vendor hasn't said yet what these accounts have left, so they go in their order: {who} first.", { who: w });
    if (group(f) !== "fine") return t("Every account is at 90% or more of its allowance, so the one with the most left goes first: {who}, at {n}.", { who: w, n: pct(f.used) });
    const next = peers.find((p) => p.known && group(p) === "fine");
    const soon = renewsBy(f).find(Boolean);
    if (!next) return t("{who} goes first: it has quota to spare, and the others are kept for last.", { who: w });
    const { c, k } = cmpRenews(f, next);
    if (c < 0 && k === 0 && known0(f.restarts)) return t("{who} goes first: of those with quota to spare, its allowance starts again soonest — one of its Codex resets about to run out is used by itself in {d}, and what it has left then is lost. {other} renews later and keeps its own.", { who: w, d: dur(at(f.restarts) - at(r.time)), other: who(next) });
    if (c < 0 && k === 0) return t("{who} goes first: of those with quota to spare, its allowance renews soonest — in {d} — and what it has left then is lost. {other} renews later and keeps its own.", { who: w, d: dur(renews(f)[0] - at(r.time)), other: who(next) });
    if (c < 0) return t("{who} goes first: its allowance renews in the same hour as {other}'s, and its shorter one sooner — in {d}.", { who: w, other: who(next), d: dur(renewsBy(f)[k] - at(r.time)) });
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
      if (x.sunk && !x.rest) out.push(t("{who} was rate limited at {time} while it had quota left, so it went to the back: it comes round again once those ahead of it are rate limited in turn.", { who: who(x), time: clock(x.sunk) }));
      else if (x.rest) out.push(t("{who} is resting — {why}, {when} — so it waits at the back.", { who: who(x), why: `${x.rest.status} · ${failWord(x.rest.why)}`, when: restWhen(x.rest, at(r.time)) }));
      else if (smart(x) && x.known && group(x) === "spent") out.push(t("{who} is at {n} — all but used up, it answers only when nothing else can.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && x.known && group(x) === "low") out.push(t("{who} is at {n} — kept for when the others can't.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && x.learns && someKnown) out.push(t("{who}: what it has left isn't known yet, and its answer tells, so it goes before those known.", { who: who(x) }));
      else if (smart(x) && !x.known && someKnown) out.push(t("{who}: what it has left isn't known yet, so it goes after those known.", { who: who(x) }));
    }
    const pooled = r.order.find((x) => !x.aside && x.kind === "key");
    for (const x of r.order.filter((x) => x.aside)) out.push(t("{who} is made for {api}, not {other} as the keys routed over are, so it isn't one of them: it's tried after them.", { who: who(x), api: API[x.speaks] || x.speaks || t("any API"), other: API[pooled?.speaks] || pooled?.speaks || t("any API") }));
    for (const x of r.left || []) out.push(x.held
      ? t("{who} is left out: the gateway key asking may not use its account.", { who: who(x) })
      : x.noCredits
      ? t("{who} is left out: a usage window is used up, and the account is set not to spend its credits, so it counts as used up until that window renews.", { who: who(x) })
      : x.capped
      ? t("{who} is left out: a usage window is at {n}, past the {cap}% cap set on the account, so it counts as used up until that window renews.", { who: who(x), n: pct(x.used), cap: x.capped })
      : x.barred
      ? t("{who} is left out: it is set to serve other models, not {model}.", { who: who(x), model: x.model })
      : x.kind === "key"
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
      const held = t("Rule {n} ({when}) sent turn {turn} to {use} as it began; the turn stays with whoever took it then.", { n: x.n, when, turn: x.turn, use: x.use });
      if (x.compact) return lead ? t("The agent is compacting the conversation, so rule {n} sends the summary to {use}; the conversation stays on the model it was on.", { n: x.n, use: x.use }) + (x.small?.length ? " " + t("Passed over, as the conversation is longer than they take: {models}.", { models: x.small.map((id) => useName(r, id)).join(", ") }) : "") : null;
      if (x.held) return lead || affWhy(r, true) ? held : null;
      if (x.grown) return lead ? t("Within turn {turn} the conversation grew to about {tokens} tokens, more than the model it was on takes, so rule {n} ({when}) moves it to {use}.", { turn: x.turn, tokens: tokens(x.tokens), n: x.n, when, use: x.use }) : null;
      if (!lead) return null;
      if (x.then?.length) return t("Turn {turn} begins and rule {n} matches — {when} — so {use} goes first; if it fails, {then}, which the rules after it that match too name, then the group's others.", { turn: x.turn, n: x.n, when, use: x.use, then: x.then.map((id) => useName(r, id)).join(", ") });
      return t("Turn {turn} begins and rule {n} matches — {when} — so {use} goes first; the group's others stay behind it if it fails.", { turn: x.turn, n: x.n, when, use: x.use });
    }
    if (x.use && x.instead) return lead ? t("Rule {n} matches, but {use} has no account or key that can take this request, so a later matching rule puts {instead} first.", { n: x.n, use: x.use, instead: useName(r, x.instead) }) : null;
    if (lead) return null;
    if (x.use) return t("Rule {n} matches, but {use} has no account or key that can take this request, so the group's order stands.", { n: x.n, use: x.use });
    if (x.waits) return t("This turn began before magpie saw it, so the rules wait for the next one.");
    if (x.held) return null;
    return t("No rule matches turn {turn} (about {n} tokens{img}).", { turn: x.turn, n: tokens(x.tokens), img: x.images ? t(", with an image") : "" });
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
      if (x.use && x.instead) out.push(t("In {group}, rule {n} matches, but {use} has no account or key that can take this request, so a later matching rule puts {instead} first.", { group: g, n: x.n, use: x.use, instead: x.instead }));
      else if (x.use && x.unready) out.push(t("In {group}, rule {n} matches, but {use} has no account or key that can take this request, so {group}'s order stands.", { group: g, n: x.n, use: x.use }));
      else if (x.use && x.held) out.push(t("In {group}, rule {n} ({when}) sent turn {turn} to {use} as it began; the turn stays with whoever took it then.", { group: g, n: x.n, when, turn: x.turn, use: x.use }));
      else if (x.use) out.push(t("In {group}, rule {n} matches — {when} — so {use} goes first there.", { group: g, n: x.n, when, use: x.use }));
      else if (!x.waits) out.push(t("In {group}, no rule matches.", { group: g }));
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
    if (!c) return x?.pick ? t("Turn {turn} reasons at {level}, as the classifier picked when it began.", { turn: x.turn, level: x.pick }) : null;
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
    if (c === "compacting") return t("compacting");
    if ((m = /^effort ≥ (\w+)$/.exec(c))) return t("reasoning ≥ {level}", { level: m[1] });
    if ((m = /^agent (.+)$/.exec(c))) return m[1].split("|").map(agentName).join(" / ");
    if ((m = /^time (all day|(\d\d:\d\d)–(\d\d:\d\d))(?: (.+))?$/.exec(c))) {
      // the days as the gateway writes them: Mon–Fri, Sat,Sun, Mon,Wed–Fri
      const days = [];
      for (const part of (m[4] || "").split(",").filter(Boolean)) {
        const [a, b] = part.split("–").map((d) => WEEK.indexOf(d.toLowerCase()));
        for (let i = a; i >= 0 && i <= (b >= 0 ? b : a); i++) days.push(WEEK[i]);
      }
      return timeText({ from: m[2] || "00:00", to: m[3] || "00:00", days });
    }
    return c;
  }
  // a rule's hours (provider.TimeWindow): local time, past midnight when
  // they end before they begin, on the days named or every day
  const WEEK = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
  const DAY_NAMES = { mon: "Mon", tue: "Tue", wed: "Wed", thu: "Thu", fri: "Fri", sat: "Sat", sun: "Sun" };
  function daysText(days) {
    const on = WEEK.map((d) => (days || []).includes(d)), out = [];
    for (let i = 0; i < 7; i++) {
      if (!on[i]) continue;
      let j = i;
      while (j + 1 < 7 && on[j + 1]) j++;
      if (j - i >= 2) out.push(t("{from}–{to}", { from: t(DAY_NAMES[WEEK[i]]), to: t(DAY_NAMES[WEEK[j]]) }));
      else for (let k = i; k <= j; k++) out.push(t(DAY_NAMES[WEEK[k]]));
      i = j;
    }
    return out.length === 7 || !out.length ? "" : out.join(t(", "));
  }
  function timeText(w) {
    const hours = w.from === w.to ? t("all day") : t("{from}–{to}", { from: w.from, to: w.to });
    const days = daysText(w.days);
    return days ? t("{hours} {days}", { hours, days }) : hours;
  }
  // "9:5" is no time; "9:05" is 09:05
  const clockOf = (s) => {
    const m = /^\s*(\d{1,2}):(\d{2})\s*$/.exec(s || "");
    return m && +m[1] < 24 && +m[2] < 60 ? m[1].padStart(2, "0") + ":" + m[2] : "";
  };

  // how the reasoning a try was sent at came to be
  function effortNote(r, tr) {
    const agent = agentName(r.agent);
    // a group's member fixed at an effort is sent it whatever was asked
    if (tr.fixed) return r.effort && r.effort !== tr.effort
      ? t("{level} reasoning, fixed on this model in the group; {agent} asked for {asked}", { level: tr.effort, agent, asked: r.effort })
      : t("{level} reasoning, fixed on this model in the group", { level: tr.effort });
    if (tr.picked) return r.effort && r.effort !== tr.effort
      ? t("{level} reasoning, picked for the turn; {agent} asked for {asked}", { level: tr.effort, agent, asked: r.effort })
      : t("{level} reasoning, picked for the turn", { level: tr.effort });
    return r.effort && r.effort !== tr.effort
      ? t("{level} reasoning: the model's nearest to the {asked} {agent} asked for", { level: tr.effort, agent, asked: r.effort })
      : t("{level} reasoning, as {agent} asked", { level: tr.effort, agent });
  }

  // a try in words, and how its reasoning came to differ from the agent's
  function tryWhy(r, i) {
    const tr = r.tries[i], said = trySaid(r, i);
    // a prompt turned away is turned away at every level: the level is no part of it
    if (!tr.effort || !r.effort || r.effort === tr.effort || tr.fail === "prompt") return said;
    const agent = agentName(r.agent);
    return said + (/[。！？]$/.test(said) ? "" : " ") + (tr.fixed
      ? t("The group fixes this model at {fixed} reasoning, in place of the {asked} {agent} asked for.", { fixed: tr.fixed, asked: r.effort, agent })
      : tr.picked
      ? t("The turn's pick replaced the {asked} {agent} asked for.", { asked: r.effort, agent })
      : t("{level} is the model's nearest to the {asked} {agent} asked for.", { level: tr.effort, asked: r.effort, agent }));
  }

  // what the gateway adds to WorkBuddy's "unapproved channel" refusal
  // (provider.WBRefusedHint, #182): the agent gets it in English, the page
  // says it apart from the vendor's words, in its own language
  const WB_REFUSED = "WorkBuddy refuses chats from Codex and Claude Code (their system prompt); use it from Hermes, OpenCode or Pi, or add another provider to this group";
  // what it adds to a vendor's edge firewall's block page (provider.BlockedHint)
  const BLOCKED = "the provider's network firewall blocked requests from this IP; wait a while, or switch to another network or proxy";
  // what it says of ZCode's Start Plan turning a request away (#425,
  // provider.ZCodeStartBlockedHint), in place of BLOCKED
  const ZCODE_BLOCKED = "ZCode's Start Plan still turned this request away, though magpie sends it as the ZCode app does; it can be a network block of this IP, or ZCode checking for something new. Use an account with a GLM Coding Plan, or add another provider to this group";
  const HINTS = [WB_REFUSED, BLOCKED, ZCODE_BLOCKED];

  function trySaid(r, i) {
    const tr = r.tries[i], w = tried(r, tr), agent = agentName(r.agent);
    let name = w ? `${who(w)} (${w.model})` : tr.id;
    // Copilot's Auto: the model it picked last, which this try went as
    if (w && tr.auto?.length) name = `${who(w)} (${w.model} → ${tr.auto[tr.auto.length - 1].model})`;
    if (tr.effort && tr.fail !== "prompt") name += " " + t("at {level} reasoning", { level: tr.effort });
    if (!tr.done) return t("{who} is answering…", { who: name });
    // a Codex reset spent by itself: with Codex's own sign-in the one try
    // it was spent for is the one that then answered
    const spent = tr.reset && tr.status < 400
      ? t("Its week was used up, so one of {account}'s Codex resets was used by itself first.", { account: tr.reset.who }) + " "
      : "";
    if (tr.status < 400 && tr.fail)
      return tr.rest
        ? t("{who} began answering, then broke off · {fail}. {agent} got what was said before the error; {how}, so {agent}'s next request goes to another first.",
          { who: name, fail: failWord(tr.fail), how: restHow(tr.rest, at(tr.start) + (tr.ms || 0)), agent })
        : t("{who} began answering, then broke off. {agent} got what was said before the error; the conversation isn't kept on {who} for its next request.", { who: name, agent });
    if (tr.status < 400) {
      const tk = (r.tokens ? " · " + t("{n} tokens", { n: tokens(r.tokens) }) : "") + firstNote(r, tr);
      return spent + (i > 0
        ? t("{who} answered in {ms}{tk}. {agent} got one clean reply and never saw the {n} that failed first.", { who: name, ms: took(tr.ms), tk, agent, n: i })
        : t("{who} answered in {ms}{tk}.", { who: name, ms: took(tr.ms), tk }));
    }
    if (tr.reset)
      return t("{who} answered {status}: its week is used up and nobody else could take the request, so one of {account}'s Codex resets was used by itself and the request is asked again, before any of the reply reaches {agent}.",
        { who: name, status: tr.status, account: tr.reset.who, agent });
    if (tr.fail === "canceled")
      return t("{agent} canceled the request while {who} was answering: nobody failed, so nobody rests and nobody else is asked.", { who: name, agent });
    if (tr.fail === "auth") {
      const next = r.tries[i + 1], nw = next && tried(r, next);
      return next
        ? t("{who} could not authenticate. Sign in again in magpie; this login is not retried. The request went to {next}.", { who: name, next: nw ? who(nw) : t("the next") })
        : t("{who} could not authenticate. Sign in again in magpie; this login is not retried. No other account could answer.", { who: name });
    }
    if (tr.fail === "foreign")
      return t("{who} couldn't read the reasoning another account wrote earlier in this conversation, so it is asked again without it, before any of the reply reaches {agent}.", { who: name, agent });
    if (tr.fail === "floor")
      return t("{who} takes no request for a reply as short as this one asked for, so it is asked again for the shortest it gives, before any of the reply reaches {agent}.", { who: name, agent });
    if (tr.fail === "update")
      return t("{who} turned away the reasoning effort changed mid-conversation as an update that keeps its cache, so it is asked again at the new effort the usual way, before any of the reply reaches {agent}.", { who: name, agent });
    if (tr.fail === "verify" && !r.tries[i + 1])
      return t("{who} answered {status}: the vendor wants the account verified before it serves it again, and nobody is left to try, so {agent} gets the error with how to verify it. For a minute {agent}'s retries get the same answer without asking the vendor.", { who: name, status: tr.status, agent });
    if (tr.fail === "refused")
      return r.tries[i + 1]
        ? t("{who}'s safety filter refused the request before saying anything, so it goes on to the next before any of the reply reaches {agent}. Nothing is wrong with {who}, so it doesn't rest.", { who: name, agent })
        : t("{who}'s safety filter refused the request before saying anything, and nobody is left to try, so {agent} gets an error saying so, not an empty reply to ask again for.", { who: name, agent });
    if (tr.fail === "prompt")
      return r.tries[i + 1]
        ? t("{who} answered {status}: the vendor turns away {agent}'s system prompt whichever account it goes to, so it goes on to the next before any of the reply reaches {agent}. Nothing is wrong with {who}, so it doesn't rest.", { who: name, status: tr.status, agent })
        : t("{who} answered {status}: the vendor turns away {agent}'s system prompt whichever account it goes to, and nobody else is left to try, so {agent} gets the error. Nothing is wrong with {who}, so it doesn't rest.", { who: name, status: tr.status, agent });
    if (tr.fail === "shape")
      return t("{who} answered {status}: its API couldn't read something in the request that another's may, so it goes on to the next before any of the reply reaches {agent}. Nothing is wrong with {who}, so it doesn't rest.", { who: name, status: tr.status, agent });
    if (tr.fail === "effort")
      return r.tries[i + 1]
        ? t("{who} answered {status}: its plan doesn't take the reasoning effort asked for, so another account goes on with it before any of the reply reaches {agent}. {who} serves other efforts, so it doesn't rest.", { who: name, status: tr.status, agent })
        : t("{who} answered {status}: its plan doesn't take the reasoning effort asked for, and no account left that does could answer, so {agent} gets an error saying so.", { who: name, status: tr.status, agent });
    if (tr.fail === "slow")
      return t("{who} hadn't begun answering after {ms}, so the request went on to the next before any of it reached {agent}. Nothing is wrong with {who}, so it doesn't rest.", { who: name, ms: took(tr.ms), agent });
    if (tr.fail === "proxy")
      return r.tries[i + 1]
        ? t("{who}: the proxy magpie goes through didn't take the connection, so the request never reached the vendor and goes on to the next. Nothing is wrong with {who}, so it doesn't rest: once the proxy is up it is asked first again.", { who: name })
        : t("{who}: the proxy magpie goes through didn't take the connection, so the request never reached the vendor, and nobody is left to try: {agent} gets the error. Start the proxy, or change it in Settings.", { who: name, agent });
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
  let seq = 0, mine = true, loaded = false, daysAt = 0;
  let traceTotals = { requests: 0, rerouted: 0, errors: 0 };
  let offMsg = ""; // why the trace can't be watched here, when it can't
  // the request the window was opened on (?req=), from the tray panel
  let wanted = document.body.classList.contains("window") && Number(params.get("req")) || 0;
  let cur = null;           // the route the header and the log tell of: the newest played
  let pinned = null;        // a past route picked from the strip
  let rows = new Map();     // id → { li, wire, st, bi, tg, w, rid, up }
  const subs = new Map();   // a group in the group's way down → its heading { li, wire, key, up }
  const agents = new Map(); // agent → { node, ic, name, sub, wire }
  let sets = [];            // the account sets on the stage, in the order they came
  const playing = new Map(); // the routes being played → the gen playing each
  let rp = null;            // a replay playing: see replay
  let day = "", days = [], past = [], pastCut = false; // a kept day looked at ("" for live), the days kept, its routes
  const src = () => rp ? rp.routes : routes; // the routes the stage plays from
  const LINGER = 12e3;      // how long an agent's last request stays on the stage
  let gen = 0, trips = [], waiters = [];
  let capQ = [], capAt = -1e9, capLo = false, flipUntil = 0;

  // fly carries a dot along paths one after another, as one flight — and
  // the magpie holding it in its beak, if there is one
  const fly = (dot, bird, legs, ms) => new Promise((res) => {
    if (!shown()) { res(); return; } // no hidden frame is needed to finish it
    const tr = { dot, bird, legs, t0: performance.now(), ms: still() ? 0 : ms, res, g: gen };
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

  // rankOf is an account's or key's place in its provider's own list, the
  // order its page shows and a drag sets (#217): the list as it is now, so
  // a drag moves it at once; else as the request found it
  function rankOf(w) {
    const p = providers?.providers?.find((x) => x.id === w.provider);
    let i = -1;
    if (p?.account && w.kind === "account") {
      const user = (w.who || "").toLowerCase();
      i = loginsInOrder(p.account, p).findIndex((l) => (l.user || "").toLowerCase() === user);
    } else if (p && w.kind === "key") i = (p.keyList || []).findIndex((k) => w.id === p.id + "#" + k.id);
    return i >= 0 ? i : (p ? 1000 : 0) + (w.rank || 0);
  }

  // seated: a route's accounts and keys each in a place of its own, not in
  // the order routing weighed them this time — the group's members in the
  // group's order, a provider's fallbacks after its own, then each
  // provider's in its own order — so the column holds still while the one
  // put first moves.
  // One whose vendor doesn't list the model to it is never asked, so it
  // isn't drawn — only told of, among why it went where it did.
  function seated(r) {
    const members = r.group?.members || [];
    const key = (w) => {
      let m = members.indexOf(w.provider + "/" + w.model + (w.fixed ? ":" + w.fixed : ""));
      if (m < 0) m = members.findIndex((x) => x.startsWith(w.provider + "/"));
      return [w.fallback ? 1 : 0, m < 0 ? members.length : m, w.name || w.provider, w.aside ? 1 : 0, rankOf(w), w.who || "", w.id];
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
  // — as is one model a group has twice, each at an effort of its own
  const seat = (x) => x.id + "\u0000" + (x.model || "") + (x.fixed ? ":" + x.fixed : "");
  // tried is the seat a try went to. A try's fixed is the effort it was
  // sent at by force — the member's own, or one its agent asked in the
  // model's id (a Claude Code tier at high) — where its seat's is the
  // member's own only, so a member following the group's effort is found by
  // its key and model (#865)
  const tried = (r, tr) => r.order.find((x) => seat(x) === seat(tr))
    || r.order.find((x) => x.id === tr.id && (x.model || "") === (tr.model || ""))
    || r.order.find((x) => x.id === tr.id);
  // tryseat is the seat of the row a try went to: its row is keyed by it
  const tryseat = (r, tr) => seat(tried(r, tr) || tr);
  const setOf = (r) => r.order.map(seat).sort().join("\n");
  // whole fills in the lists a route's JSON can leave null: a route the
  // gateway weighed no seats for has "order": null, and the page reads
  // r.order, r.tries and r.left as lists everywhere — one null threw in
  // pick, and clicking such a request (a refused one) opened nothing
  const whole = (r) => {
    if (r) { r.order ||= []; r.tries ||= []; r.left ||= []; }
    return r;
  };

  // staged: the routes the stage shows — a picked one alone; else those
  // playing, and each agent's latest while it lingers, a few agents at most
  function staged() {
    if (pinned) return [pinned];
    if (day && !rp) return [];
    const n = now(), last = new Map(), map = src();
    const rs = [...map.values()].filter(matchesPurpose).sort((a, b) => a.id - b.id);
    for (const r of rs) last.set(r.agent, r);
    const out = rs.filter((r) => playing.has(r.id) || (last.get(r.agent) === r && (!r.done || at(r.time) + (r.ms || 0) > n - LINGER)));
    const c = cur && map.get(cur.id);
    if (c && !out.includes(c)) out.push(c);
    const ags = [...new Set(out.map((r) => r.agent))].slice(-4);
    return out.filter((r) => ags.includes(r.agent)).sort((a, b) => a.id - b.id);
  }

  // hueOf is the colour an agent's requests and answers fly in, and it is
  // marked with: its maker's own where it has one, else one of the rest
  // picked by its id, so it stays the same from one request to the next.
  const HUES = {
    claude: "#d97757", codex: "#6366f1", gemini: "#0ea5e9", copilot: "#a855f7", cursor: "#14b8a6", opencode: "#eab308",
    crush: "#ec4899", goose: "#84cc16", pi: "#10b981", omp: "#f43f5e", omo: "#7c3aed", dsh: "#06b6d4", commandcode: "#f97316",
    mimocode: "#3b82f6",
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
    b.append(name, " ", sub, el("code", "mdl", w.fixed ? `${w.model}:${w.fixed}` : w.model));
    if (w.fixed) b.title = t("{level} reasoning, fixed on this model in the group", { level: w.fixed });
    if (w.fast) {
      // the group sends this member in its vendor's fast mode
      const f = el("small", "fb", t("fast"));
      f.title = t("Sent in its vendor's fast mode, as the group says");
      b.append(f);
    }
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
    cur = src().get((pinned || rs[rs.length - 1]).id) || pinned || rs[rs.length - 1];
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
    for (const r of rs) for (const tr of r.tries) if (!tr.done) { trying.add(tryseat(r, tr)); busy.add(r.agent); }
    const onWire = new Set();
    for (const f of flying.values()) { onWire.add(f.id); busy.add(f.agent); }
    for (const [id, row] of rows) {
      // each row as the latest staged request that weighed it found it
      const r = src().get(row.rid) || pinned || cur, answered = new Set(), rests = new Map(), gave = new Map();
      for (const w of r.order) if (w.rest) rests.set(w.id, w.rest);
      for (const tr of r.tries) {
        if (tr.done && tryOk(tr)) answered.add(tryseat(r, tr));
        else if (tr.done && !tr.rest && !tr.again) gave.set(tryseat(r, tr), tr); // the error the agent got
        if (tr.rest) rests.set(tr.id, tr.rest);
      }
      for (const tr of r.tries) if (tr.done && tryOk(tr)) rests.delete(tr.id); // it answered: whatever rest it began in is over
      const w = row.w, rest = rests.get(w.id), resting = rest && at(rest.until) > n;
      let s;
      if (w.unlisted) s = unlistedWord(w);
      else if (resting) s = `${failWord(rest.why)} · ${restWhen(rest)}`;
      // a row another request weighed, answering that one: the header
      // tells of this request, so it says the answer is not this one's
      else if (trying.has(id)) s = r.id !== cur.id && agents.size <= 1 ? t("answering another request…") : t("answering…");
      else if (answered.has(id)) s = agents.size > 1 ? t("answered {agent}", { agent: agentName(r.agent) }) : t("answered this request");
      else if (gave.has(id)) s = t("{status} · {fail} · passed to {agent}", { status: gave.get(id).status, fail: failWord(gave.get(id).fail), agent: agentName(r.agent) });
      else if (w.sunk) s = t("rate limited at {time} · at the back", { time: clock(w.sunk) });
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        s = !w.routing && w.used >= 98 ? quota(w, "{n} used · all but used up", "{n} left · all but used up")
          : !w.routing && w.used >= 90 ? quota(w, "{n} used · kept for last", "{n} left · kept for last")
          : soon ? quota(w, "{n} used · renews in {d}", "{n} left · renews in {d}", { d: dur(soon - n) }) : quota(w, "{n} used", "{n} left");
      } else if (w.kind === "account") s = t("what's left not known yet");
      else if (w.routing === "usage") s = t("{n} tokens lately", { n: tokens(w.tokens || 0) });
      else if (w.aside) s = t("{api} only · after the others", { api: API[w.speaks] || w.speaks || t("any API") });
      else if (w.speaks) s = t("{api} only", { api: API[w.speaks] || w.speaks });
      else s = w.kind === "key" ? t("API key") : t("one key");
      if (row.st.textContent !== s) row.st.textContent = s;
      const bar = w.kind === "account" && w.known;
      row.li.classList.toggle("nobar", !bar);
      row.bi.style.width = bar ? fill(w) : "0";
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

  // the log is drawn again only when what it says changes: it is asked to
  // on every trace update (#308)
  let logR = null, headKey = "", stepsKey = "";
  function renderLog() {
    const r = logR = pinned || cur;
    log.hidden = !r;
    if (!r) return;
    const on = () => listed().filter((x) => x.id >= logR.id);
    const head = JSON.stringify([document.documentElement.lang, !!rp, pinned?.id, r.id, r.time, r.done, !!pinned && on().length > 1]);
    if (headKey !== head) {
      headKey = head;
      logHead.replaceChildren(
        el("span", "", rp ? t("How the request at {time} was routed", { time: clock(r.time) })
          : pinned ? t("How the request at {time} was routed", { time: clock(r.time) }) : t("How the last request was routed")),
        el("span", "grow"));
      if (!rp && r.done) {
        const usage = el("button", "text", t("View usage"));
        usage.onclick = () => window.openUsageRoute(logR);
        logHead.append(usage);
        const again = el("button", "text", t("Replay"));
        again.onclick = () => replay([logR], pinned);
        logHead.append(again);
        // and on from it: the requests listed after it, as they came
        if (pinned && on().length > 1) {
          const from = el("button", "text", t("Replay from here"));
          from.onclick = () => replay(on(), pinned);
          logHead.append(from);
        }
      }
      if (pinned && !rp) {
        const live = el("button", "text", t("Back to live"));
        live.onclick = () => { if (day) lookAt(""); else { pinned = null; followListed(); } };
        logHead.append(live);
      }
    }
    renderSteps(r);
  }
  // logoed puts the provider's logo before the first account, key or
  // provider a line of the story names, so who it is about reads at a
  // glance (the owner: 这里在前面显示对应的 provider 图标会不会更直观一点)
  const logoOf = (w) => w.icon || w.preset || "generic";
  function logoed(s, r) {
    const word = /[A-Za-z0-9_]/;
    let hit = null;
    for (const w of r.order) {
      for (const n of new Set([who(w), w.name].filter(Boolean))) {
        for (let i = s.indexOf(n); i >= 0; i = s.indexOf(n, i + 1)) {
          if (word.test(s[i - 1] || "") || word.test(s[i + n.length] || "")) continue;
          if (!hit || i < hit.i || (i === hit.i && n.length > hit.n.length)) hit = { i, n, w };
          break;
        }
      }
    }
    if (!hit) return [s];
    const name = el("span", "rt-named");
    name.append(icon(logoOf(hit.w)), hit.n);
    return [s.slice(0, hit.i), name, s.slice(hit.i + hit.n.length)].filter((x) => x !== "");
  }

  function renderSteps(r) {
    const items = [];
    const main = r.order.find((x) => !x.fallback);
    items.push([r.group
      ? t("{agent} asked for the routing group {name}: {members}", { agent: agentName(r.agent), name: r.group.name, members: treeText(r.group) })
      : main && main.model !== r.model
      ? t("{agent} asked for {model}: {name} serves it, and the vendor is asked for {sent}", { agent: agentName(r.agent), model: r.model, name: main.name, sent: main.model })
      : t("{agent} asked for {model}", { agent: agentName(r.agent), model: r.model }) + " → " + (main?.name || r.provider), ""]);
    if (r.kind) items.push([kindWhy(r), "aside kind"]);
    if (r.sealedTask) items.push([t(r.group
      ? "This subagent task is encrypted. Only ChatGPT accounts can read it; other providers (such as Claude) are excluded regardless of quota."
      : "This subagent task is encrypted. Only ChatGPT accounts can read it."), "aside"]);
    items.push([affWhy(r, true) || ruleWhy(r, true) || firstWhy(r), "why"]);
    for (const s of nestedWhy(r)) items.push([s, "why"]);
    for (const a of asides(r)) items.push([a, "aside"]);
    r.tries.forEach((tr, i) => {
      items.push([tryWhy(r, i), tr.done ? (tryOk(tr) ? "ok" : "bad") : "wait"]);
      // each model Copilot's Auto picked for it, where from, and whether
      // Copilot refused it, on which API and with Auto's session token or
      // without (#256)
      for (const p of tr.auto || []) {
        let s = t("Copilot's Auto picked {model} ({via})", { model: p.model, via: p.via === "fallback" ? t("the model the account may pick by hand") : p.via });
        if (p.skipped) s += " · " + t("not from /auto: {why}", { why: p.skipped });
        if (p.api) s += " · " + t(p.session ? "sent to {api} with Auto's session token" : "sent to {api}", { api: p.api });
        if (p.refused) s += " · " + t("Copilot refused it: {error}", { error: p.refused });
        items.push([s, "aside"]);
      }
      // what the vendor said, word for word: the why above is magpie's reading of it
      if (tr.done && !tryOk(tr) && tr.error) {
        const hint = HINTS.find((h) => tr.error.endsWith(" — " + h));
        const said = hint ? tr.error.slice(0, -(hint.length + 3)) : tr.error;
        items.push([t("It said: {error}", { error: said.length > 600 ? said.slice(0, 600) + "…" : said }), "aside said"]);
        if (hint) items.push([t(hint), "aside"]);
      }
      // the reply said another model answered it
      if (tr.done && tryOk(tr) && tr.swapped) items.push([swapWhy(tr), "swap", tr]);
      // another magpie's routing group named the member it routed to
      else if (tr.done && tryOk(tr) && tr.routed) items.push([routedWhy(tr), "aside", tr]);
      if (tr.done && tryOk(tr) && tr.upstream) items.push([upstreamWhy(tr), "aside upstream-said", tr]);
    });
    if (r.done && !r.tries.length) items.push([t("Nothing was tried: {error}", { error: r.error || r.status }), "bad"]);
    const key = JSON.stringify([r.kind, items.map(([s, c, tr]) => [s, c, tr?.model, tr?.served]), r.order.map(logoOf)]);
    if (stepsKey === key) return;
    stepsKey = key;
    steps.replaceChildren(...items.map(([s, c, tr]) => {
      const li = el("li", c);
      li.append(...(/^(why|ok|bad|wait|aside)$/.test(c) ? logoed(s, r) : [s]));
      if (c === "aside kind") li.prepend(kindTag(r), " ");
      if (c === "swap") li.prepend(swapTag(tr), " ");
      return li;
    }));
  }

  // pick sets the stage to a past request, or back to live with the newest.
  // The page stays where it is: a request clicked in the list stays under
  // the pointer, and the stage and its story change above it (it used to
  // go up to the stage, which read as the page jumping to its top)
  function pick(r) {
    pinned = !day && r.id === newest()?.id ? null : r;
    if (rp) { rp = null; rbar.hidden = true; }
    stopPlays();
    cur = r;
    sync(true); renderAll();
    say(affWhy(r, true) || ruleWhy(r, true) || firstWhy(r));
  }

  window.openRoute = async (id, time) => {
    let r = routes.get(id);
    if (!r) {
      const res = await fetch("/api/gateway/route?id=" + encodeURIComponent(id) + "&day=" + encodeURIComponent(time.slice(0, 10)));
      if (res.status === 404) throw new Error(t("Routing history for this request is no longer available."));
      if (!res.ok) throw new Error(await res.text());
      r = whole(await res.json());
      noteAccounts(r);
    }
    day = routes.has(id) ? "" : r.time.slice(0, 10);
    if (day) {
      await loadDays(day);
      if (!past.some((x) => x.id === id)) past.push(r);
    }
    if (purpose && purposeOf(r.kind) !== purpose) purpose = "";
    offline("");
    window.show("routing");
    pick(r);
  };

  // what a call was for when it isn't a turn of the conversation, as
  // Codex names it (x-openai-subagent): its own guardian review of an
  // approval, a thread's title, memories… — each on the model Codex picks
  // for it, so a list of Luna calls under a Sol composer reads as it is.
  // A web search is magpie's own, run for a model that can't search on the
  // model it searches with (a DeepSeek chat showing GPT calls, #314)
  const knownKind = (k) => Object.hasOwn(REQUEST_KINDS, k) ? REQUEST_KINDS[k] : null;
  const kindName = (k) => knownKind(k) ? t(knownKind(k).name) : k;
  window.kindName = kindName; // the Usage page's Requests say it too (#714)
  // The catalog is generated from usage.PurposeKinds; unknown names stay literal.
  const purposeOf = (kind) => !kind ? "unmarked" : knownKind(kind)?.purpose || "kind:" + kind;
  const purposeOptions = (ids, selected) => [...new Set([...ids, ...(selected ? [selected] : [])])].sort().map((id) => {
    const kind = id.slice(5);
    return { v: id, name: id === "unmarked" ? t("Unmarked") : kindName(kind), note: id === "unmarked"
      ? t("No purpose was recorded; this may be a conversation turn or an older record.")
      : knownKind(kind) ? "" : t("Unrecognized request purpose") };
  });
  window.purposeOf = purposeOf;
  window.purposeOptions = purposeOptions;
  function kindTag(r) {
    const k = el("span", "kind", kindName(r.kind));
    k.title = kindWhy(r);
    return k;
  }
  // a try whose reply said another model answered than the one it asked
  // for: a vendor serving a cheaper model in its place, which only the
  // reply's model field tells (its dated name is the same model)
  function swapTag(tr, short) {
    const k = el("span", "swap", short ? t("served {served}", { served: tr.served }) : t("requested {sent} · served {served}", { sent: tr.model, served: tr.served }));
    k.title = swapWhy(tr);
    return k;
  }
  const swapWhy = (tr) => t("The vendor was asked for {sent}, but its reply says {served} answered: likely another model. The same model under a dated name or spelled otherwise isn't marked.", { sent: tr.model, served: tr.served });
  window.swapWhy = swapWhy; // the Usage page's Requests say it too
  // a try that asked a remote magpie for one of its routing groups: the
  // reply names the member the group routed to, which is the group
  // picking, not a swap — shown plain, as a model that answered
  function routedTag(tr) {
    const k = el("span", "routed", t("served {served}", { served: tr.served }));
    k.title = routedWhy(tr);
    return k;
  }
  const routedWhy = (tr) => t("{sent} is a routing group of the remote magpie, and it routed the request to {served}: the group picking one of its models, not the vendor swapping the model.", { sent: tr.model, served: tr.served });
  window.routedWhy = routedWhy;
  // the provider an aggregator (OpenRouter …) said answered behind it:
  // DeepInfra, Novita… (leslie_luo on Discord)
  function upstreamTag(tr) {
    const k = el("span", "upstream", t("Upstream: {upstream}", { upstream: tr.upstream }));
    k.title = upstreamWhy(tr);
    return k;
  }
  const upstreamWhy = (tr) => t("The aggregator passed the request on to {upstream}, as its reply says: the provider that actually answered it.", { upstream: tr.upstream });
  function kindWhy(r) {
    const agent = agentName(r.agent);
    if (purposeOf(r.kind) === "kind:collab_spawn") return r.group
      ? t("{agent} requested a subagent; magpie selects its model within this routing group.", { agent })
      : t("{agent} requested a subagent on {model}.", { agent, model: r.model });
    if (purposeOf(r.kind) === "kind:ambient_suggestions") return t("{agent} drafted the suggested prompts on its home page by itself, in the background, searching the project's files and connected apps, and checked them for safety. Not a turn of the conversation; Codex's Settings › Configuration › Suggested prompts turns it off.", { agent });
    if (r.kind === "luna_reserve") return t("{agent} sent this turn on Luna Reserve, which it turns to once the plan's own allowance is used up; it picks the model itself.", { agent });
    if (r.kind === "web_search") return r.for
      ? t("magpie ran this web search for {agent}'s {model}, which can't search the web by itself: {searcher} searched, and {model} goes on answering once it has what was found. Not a turn of the conversation.", { agent: agentName(r.for.agent), model: r.for.model, searcher: r.model })
      : t("magpie ran this web search for a model that can't search the web by itself: {searcher} searched, and that model goes on answering once it has what was found. Not a turn of the conversation.", { searcher: r.model });
    if (r.kind === "vision") return r.for
      ? t("magpie had {describer} describe an image for {agent}'s {model}, which can't see images: {model} is given the description in the image's place. Not a turn of the conversation.", { agent: agentName(r.for.agent), model: r.for.model, describer: r.model })
      : t("magpie had {describer} describe an image for a model that can't see images, which is given the description in the image's place. Not a turn of the conversation.", { describer: r.model });
    return t("{agent} made this call itself ({kind}), not as a turn of the conversation, and picks its model itself.", { agent, kind: kindName(r.kind) });
  }

  // who answered a request, or what its agent got
  function outcome(r) {
    if (!r.done) {
      const tr = r.tries[r.tries.length - 1], w = tr && tried(r, tr);
      return [w ? t("{who} is answering…", { who: `${who(w)} · ${w.model}` }) : t("routing…"), "wait", tr];
    }
    const ok = r.tries.find((tr) => tr.done && tryOk(tr)), w = ok && tried(r, ok);
    // a 200 whose error is a note of its own — Codex's titles off in
    // Settings, a reply with no title in it — answered: its try has no
    // fail. Bad is a reply that broke off, or every try failing
    if (r.status < 400 && (ok || !r.tries.length)) return [w ? `${who(w)} · ${w.model}` : r.provider, r.tries.length > 1 ? "moved" : "ok", ok];
    const last = r.tries[r.tries.length - 1];
    return [last ? `${r.status} · ${failWord(last.fail)}` : `${r.status || ""} ${r.error || ""}`.trim(), "bad"];
  }

  // a request's row's title: the way it went, to the one that took it
  // once it was rerouted — not the provider it resolved to first, which
  // the row no longer says (#337)
  function reqTitle(r, how, tr) {
    const head = `${agentName(r.agent)} · ${r.model} → `;
    const w = how !== "bad" && r.tries.length > 1 && tr && tried(r, tr);
    if (!w) return head + r.provider;
    const first = r.tries[0], f = tried(r, first);
    const from = f && seat(f) !== seat(w) ? "\n" + t("rerouted from {from}", { from: `${where(f)} · ${first.model || f.model}` }) : "";
    return head + `${where(w)} · ${tr.model || w.model}` + from;
  }

  // the requests the gateway keeps, newest first: pick one to see how it was routed
  // listed: the requests the list shows, newest first — the gateway's last
  // few, or a day the history keeps
  const allListed = () => (day ? past : [...routes.values()]).slice().sort((a, b) => b.id - a.id);
  const matchesPurpose = (r) => !purpose || purposeOf(r.kind) === purpose;
  const listed = () => allListed().filter(matchesPurpose);
  // Match the rows and story: a broken-off 200 fails, an informational note
  // on an answered request (such as Codex titles being off) does not.
  const failedRoute = (r) => r.done && outcome(r)[1] === "bad";
  function renderStats(rs) {
    const scoped = !!(day || purpose), done = rs.filter((r) => r.done);
    const counts = scoped ? {
      requests: done.length,
      rerouted: done.reduce((n, r) => n + r.tries.filter((tr, i) => tr.rest && i < r.tries.length - 1).length, 0),
      errors: done.filter(failedRoute).length,
    } : traceTotals;
    [counts.requests, counts.rerouted, counts.errors].forEach((n, i) => setText(statB[i], String(n)));
    stats.title = t(scoped ? "Counts for the requests in this list" : "Counts since the gateway started");
    const failed = rs.some(failedRoute), btn = statB[2].parentElement;
    btn.setAttribute("aria-disabled", String(!failed));
    btn.title = t(failed ? "Show the latest request that failed" : "No failed request in this list");
  }
  // Follow the newest matching request unless the reader picked one still in
  // this list. Stop old flights before the scope changes, including replays.
  function followListed() {
    if (rp) endReplay(true);
    stopPlays();
    if (pinned && !listed().some((r) => r.id === pinned.id)) pinned = null;
    cur = pinned || newest();
    if (day) pinned = cur;
    capQ = [];
    if (cur) { sync(true); say(affWhy(cur, true) || ruleWhy(cur, true) || firstWhy(cur)); }
    else empty();
    renderAll();
  }
  const dayName = (d) => {
    const x = new Date(d + "T12:00:00"), n = new Date(), y = new Date(n.getTime() - 864e5);
    return x.toDateString() === n.toDateString() ? t("today") : x.toDateString() === y.toDateString() ? t("yesterday")
      : x.toLocaleDateString([], { month: "short", day: "numeric", weekday: "short" });
  };
  async function loadDays(d) {
    try {
      const res = await (await fetch("/api/gateway/history?day=" + encodeURIComponent(d || ""))).json();
      days = res.days || [];
      noteAccounts(res.routes);
      if (d && d === day) { past = (res.routes || []).map(whole); pastCut = !!res.cut; }
    } catch {}
    renderHist(); // shown once there are days, though none are live
  }
  async function lookAt(d) {
    if (rp) endReplay(true);
    day = d;
    past = [];
    if (d) await loadDays(d);
    pinned = null;
    followListed();
  }
  function renderDays() {
    const b = (d, label, n) => {
      const x = el("button", "rt-day" + (d === day ? " on" : ""));
      x.append(el("span", "", label), ...(n != null ? [el("small", "", String(n))] : []));
      x.setAttribute("aria-pressed", String(d === day));
      x.onclick = () => { if (d !== day) lookAt(d); };
      return x;
    };
    const key = day + "|" + days.map((d) => d.day + ":" + d.requests).join(",");
    if (dayBar.dataset.key === key) return;
    dayBar.dataset.key = key;
    dayBar.replaceChildren(b("", t("Live")), ...days.map((d) => b(d.day, dayName(d.day), d.requests)));
    dayBar.hidden = !days.length && !day;
    fitDays();
  }
  // the list's head, made once: a trace update redraws the list, and a
  // button made again each time is one WebKit may drop a click on
  const reqLabel = el("span", "label"), replayAll = el("button", "text");
  replayAll.onclick = () => replay(listed(), pinned);
  reqHead.append(reqLabel, el("span", "grow"), reqNote, purposeTools, metricPick, replayAll);
  // each request's row, kept while what it says is the same: the list
  // is redrawn on every trace update, and made again whole each time it
  // was most of what a busy gateway cost the page (#308)
  const reqRows = new Map(); // id → { b, sig, r }
  const sessionRows = new Map(); // agent + session → persistent heading
  const closedSessions = new Set();
  const groupSession = (r) => r.parentSession || r.session || "";
  const sessionKey = (r) => groupSession(r) ? JSON.stringify([r.agent || "other", groupSession(r)]) : "";
  let namesBusy = false;
  async function refreshSessionNames() {
    if (namesBusy || !shown()) return;
    const rs = listed().filter((r) => r.agent === "codex"), sourceDay = day, routeIDs = new Set(rs.map((r) => r.id));
    if (!rs.some(groupSession)) return;
    namesBusy = true;
    try {
      const result = { names: {}, parents: {}, matched: {}, resetInferred: false };
      // A full history day plus an individually opened older request can exceed
      // the API's 2,000-row limit. Only Codex rows need names or associations.
      for (let i = 0; i < rs.length; i += 2000) {
        const batch = rs.slice(i, i + 2000), ids = [...new Set(batch.map(groupSession).filter(Boolean))];
        const res = await fetch("/api/gateway/session-titles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids, groups: true, routeIds: batch.map((r) => r.id), day: sourceDay }) });
        if (!res.ok) return;
        const part = await res.json();
        if (day !== sourceDay) return;
        Object.assign(result.names, part.names || part);
        Object.assign(result.parents, part.parents);
        Object.assign(result.matched, part.matched);
        result.resetInferred ||= !!part.resetInferred;
      }
      const names = result.names;
      if (day !== sourceDay) return;
      let changed = false;
      for (const r of listed()) {
        if (r.agent !== "codex" || !routeIDs.has(r.id)) continue;
        if (result.resetInferred && r.parentMatched) {
          r.parentSession = ""; r.parentMatched = false; changed = true;
        }
        if (result.parents && Object.hasOwn(result.parents, r.id)) {
          const parent = result.parents[r.id] || "", matched = !!result.matched?.[r.id];
          if ((r.parentSession || "") !== parent || !!r.parentMatched !== matched) {
            r.parentSession = parent; r.parentMatched = matched; changed = true;
          }
        }
        const id = groupSession(r);
        const name = typeof names[id] === "string" ? names[id] : "";
        if ((r.sessionTitle || "") !== name) { r.sessionTitle = name; changed = true; }
      }
      if (changed) steady(renderHist);
    } catch {} finally { namesBusy = false; }
  }
  setInterval(refreshSessionNames, 15000);
  window.addEventListener("focus", refreshSessionNames);
  const costNote = () => t("Estimated at effective model prices, including cache reads and writes; subscription billing may differ. Totals cover only the requests listed here.");
  function routeCost(r) {
    if (!r.priced) return "—";
    return "≈" + fmtCost({ cost: r.cost || 0, unpriced: 0 }) + (r.unpriced ? "+" : "");
  }
  function groupedRows(rs, rowEls) {
    const groups = new Map();
    rs.forEach((r, i) => {
      const key = sessionKey(r);
      if (!groups.has(key)) groups.set(key, { key, r, rows: [], els: [] });
      const g = groups.get(key);
      g.rows.push(r); g.els.push(rowEls[i]);
    });
    const els = [];
    for (const g of groups.values()) {
      let x = sessionRows.get(g.key);
      if (!x) {
        const b = el(g.key ? "button" : "div", "rt-session"), name = el("span", "nm"), meta = el("span", "summary"), cost = el("span", "cost"), arrow = el("span", "arrow");
        b.append(arrow, name, meta, cost);
        x = { b, name, meta, cost, arrow };
        if (g.key) b.onclick = () => {
          if (closedSessions.has(g.key)) closedSessions.delete(g.key); else closedSessions.add(g.key);
          steady(renderHist);
        };
        sessionRows.set(g.key, x);
      }
      const open = !closedSessions.has(g.key);
      const total = g.rows.reduce((s, r) => {
        s.tokens += r.tokens || 0;
        if (r.priced) { s.cost += r.cost || 0; s.priced++; }
        if (!r.priced || r.unpriced) s.unpriced++;
        if (!r.done) s.running++;
        else {
          s.completed++;
          const { prompt, read } = promptOf(r);
          if (prompt) { s.prompt += prompt; s.read += read; s.cached++; }
          if (outcome(r)[1] !== "bad" && speedOf(r.out, r.ms, r.ttft)) {
            s.decodeMs += r.ms - r.ttft; s.decodeOut += r.out; s.timed++;
          }
        }
        return s;
      }, { cost: 0, tokens: 0, priced: 0, unpriced: 0, running: 0, completed: 0, prompt: 0, read: 0, cached: 0, decodeMs: 0, decodeOut: 0, timed: 0 });
      setText(x.arrow, g.key ? open ? "▾" : "▸" : "");
      // Background workers have their own IDs. Label groups made entirely
      // for one purpose without renaming a chat that also made helper calls.
      const memory = g.r.agent === "codex" && g.rows.every((r) => purposeOf(r.kind) === "kind:memory_consolidation");
      const suggestions = g.r.agent === "codex" && g.rows.every((r) => purposeOf(r.kind) === "kind:ambient_suggestions");
      const name = g.rows.find((r) => r.sessionTitle)?.sessionTitle || (memory ? t("Background memory task") : suggestions ? t("Background prompt suggestions") : "");
      setText(x.name, g.key ? agentName(g.r.agent) + " · " + (name || groupSession(g.r)) : t("No session ID"));
      const purpose = memory ? t("Codex is organizing memories from earlier chats in the background. This can continue after a chat finishes.") + "\n"
        : suggestions ? kindWhy(g.r) + "\n"
        : g.rows.some((r) => r.parentMatched) ? t("Title requests were automatically matched using the prompt and the applied chat title.") + "\n" : "";
      x.name.title = g.key ? (name ? name + "\n" : "") + purpose + t("Session id") + ": " + groupSession(g.r) : t("These requests did not provide a session ID; they are not treated as one conversation.");
      const bits = [t(g.rows.length === 1 ? "{n} request" : "{n} requests", { n: g.rows.length })];
      if (visibleMetrics.includes("tokens") && total.tokens) bits.push(t("{n} tokens", { n: tokens(total.tokens) }));
      if (total.running) bits.push(t("{n} in progress", { n: total.running }));
      const count = el("span", "session-count", bits[0]), tokenHelp = visibleMetrics.includes("tokens") && total.tokens ? tokenBreakdown(g.rows) : "";
      if (tokenHelp) {
        const amount = el("span", "rt-token-total", bits[1]);
        amount.title = tokenHelp;
        count.append(" · ", amount);
      }
      if (total.running) count.append(" · " + bits[bits.length - 1]);
      const meta = [count];
      const coverage = (n) => t("Based on {n} of {total} completed requests", { n, total: total.completed });
      if (g.key && visibleMetrics.includes("cache") && total.prompt) meta.push(metricElement(["cache-hit", "Avg. cache", pct(100 * total.read / total.prompt),
        t("Total cache reads divided by total prompt tokens; larger prompts carry more weight") + "\n" + coverage(total.cached)]));
      if (g.key && visibleMetrics.includes("speed") && total.decodeMs) meta.push(metricElement(["speed", "Avg. speed", t("{n} tok/s", { n: Math.round(total.decodeOut * 1000 / total.decodeMs) }),
        t("Total output tokens divided by total decode time; excludes waiting and replies without usable timing") + "\n" + coverage(total.timed)]));
      const metaSig = JSON.stringify([bits, tokenHelp, meta.map((e) => [e.textContent, e.title])]);
      if (x.meta.dataset.sig !== metaSig) { x.meta.replaceChildren(...meta); x.meta.dataset.sig = metaSig; }
      setText(x.cost, total.priced ? "≈" + fmtCost({ cost: total.cost, unpriced: 0 }) + (total.unpriced ? "+" : "") : "");
      x.cost.hidden = !visibleMetrics.includes("cost") || !total.priced;
      x.cost.title = costNote();
      if (g.key) x.b.setAttribute("aria-expanded", String(open));
      els.push(x.b);
      if (open) els.push(...g.els);
    }
    for (const key of sessionRows.keys()) if (!groups.has(key)) { sessionRows.delete(key); closedSessions.delete(key); }
    return els;
  }
  function renderHist() {
    const rs = listed();
    renderStats(rs);
    const all = allListed();
    hist.hidden = !all.length && !day && !days.length && !purpose;
    const opts = purposeOptions(all.map((r) => purposeOf(r.kind)), purpose);
    const selected = opts.find((o) => o.v === purpose);
    purposeTools.hidden = opts.length < 2 && !purpose;
    purposeTools.classList.toggle("set", !!purpose);
    purposeClear.hidden = !purpose;
    purposeClear.title = t("Clear filter");
    purposeClear.setAttribute("aria-label", t("Clear filter"));
    const label = purpose ? t("Purpose: {name}", { name: selected?.name || purpose }) : t("Purpose filter");
    setText(purposeLabel, label);
    purposePick.setAttribute("aria-label", label);
    purposePick.title = t("Filter routing by purpose") + (purpose ? "\n" + label : "");
    setText(metricLabel, t("Metrics"));
    metricPick.title = t("Metrics to show");
    purposePick.onclick = (e) => {
      e.stopPropagation();
      if (purposePick.classList.contains("open")) return closeProtoMenu();
      // A handful of purposes needs a small menu; explanations stay in tooltips.
      openProtoMenu(purposePick, [{ v: "", name: t("All purposes"), note: "" }, ...opts].map((o) => ({
        ...o, literalName: true, title: o.note || o.v, note: "",
      })), purpose, (v) => {
        purpose = v;
        steady(followListed);
        purposePick.focus({ preventScroll: true });
      }, "Purpose", "rt-purpose-menu", "right");
    };
    setText(reqLabel, t("Requests"));
    setText(replayAll, t("Replay them all"));
    replayAll.hidden = !(rs.filter((r) => r.done).length > 1 && !rp);
    setText(reqNote, day ? t(pastCut ? "the last {n} of {day}" : "{n} on {day}", { n: rs.length, day: dayName(day) })
      : t("the last {n} the gateway keeps", { n: rs.length }));
    renderDays();
    groupButtons.forEach((b, i) => {
      setText(b, t(b.dataset.label));
      b.classList.toggle("on", bySession === !!i);
      b.setAttribute("aria-pressed", String(bySession === !!i));
    });
    groupBar.hidden = !rs.length;
    if (!reqs.style.maxHeight) requestAnimationFrame(fitReqs); // first shown
    // none yet: what the list is for in its place, and no accounts column
    // to tally nothing
    const none = !rs.length;
    reqNote.hidden = none;
    reqs.classList.toggle("none", none);
    colB.hidden = none;
    hist.classList.toggle("solo", none);
    if (none) {
      const p = el("div", "empty-state");
      p.append(el("b", "", purpose ? t("No requests match these filters.") : day ? t("Nothing on {day}", { day: dayName(day) }) : t("No requests since magpie started")),
        t("Each request an agent sends through magpie shows up here: who answered it, why, and each try."));
      if (!day && days.length) p.append(" " + t("Earlier ones are kept by day, in the bar above."));
      reqs.replaceChildren(p);
      reqRows.clear();
      sessionRows.clear();
      closedSessions.clear();
      renderActs(rs);
      return;
    }
    const lang = document.documentElement.lang, ids = new Set();
    const rowEls = rs.map((r) => {
      const [said, how, tr] = outcome(r);
      const sel = String(pinned ? pinned.id === r.id : cur?.id === r.id);
      const ag = agentOf(r.agent);
      const meta = [];
      if (r.tries.length > 1) meta.push(t("{n} tries", { n: r.tries.length }));
      const metrics = requestMetrics(r, how);
      // all the row says, and its titles
      const title = reqTitle(r, how, tr);
      const sig = JSON.stringify([lang, said, how, title, r.time, r.agent, agentName(r.agent), ag?.icon, r.model, r.provider, r.kind, r.effort,
        tr?.effort, tr?.picked, tr?.fixed, tr?.fast, tr?.swapped && tr.done && tryOk(tr) ? [tr.model, tr.served] : 0, tr?.routed && tr.done && tryOk(tr) ? tr.served : 0, tr?.done && tryOk(tr) ? tr.upstream : 0, meta, metrics, routeCost(r)]);
      ids.add(r.id);
      let x = reqRows.get(r.id);
      if (!x || x.sig !== sig) {
        const row = x = { sig, b: reqRow(r, said, how, tr, ag, meta, metrics, title) };
        row.b.onclick = () => pick(row.r); // the request as it is when clicked
        reqRows.set(r.id, x);
      }
      x.r = r;
      if (x.b.getAttribute("aria-pressed") !== sel) x.b.setAttribute("aria-pressed", sel);
      return x.b;
    });
    for (const id of reqRows.keys()) if (!ids.has(id)) reqRows.delete(id);
    const els = bySession && rs.some((r) => r.session) ? groupedRows(rs, rowEls) : rowEls;
    // the rows moved only where they changed; the list scrolls on its own,
    // and WebKit, a row taken out for a moment, would send it back to its
    // top from under the row just picked
    const kids = reqs.children;
    if (kids.length !== els.length || els.some((e, i) => kids[i] !== e)) {
      const listTop = reqs.scrollTop;
      els.forEach((e, i) => { if (kids[i] !== e) reqs.insertBefore(e, kids[i] || null); });
      while (kids.length > els.length) reqs.lastElementChild.remove();
      if (reqs.scrollTop !== listTop) reqs.scrollTop = listTop;
    }
    renderActs(rs);
  }
  function reqRow(r, said, how, tr, ag, meta, metrics, title) {
    const b = el("button", "rt-req " + how);
    const when = el("span", "at", new Date(r.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
    const asked = el("span", "asked");
    const sw = el("i", "ag");
    sw.style.setProperty("--agent", hueOf(r.agent));
    asked.append(sw, icon(ag?.icon || "generic"), el("span", "m", r.model));
    if (r.kind) { asked.classList.add("kinded"); asked.append(kindTag(r)); }
    // the reasoning the model was sent at — the turn's pick, or the
    // agent's fitted to the model's levels — after the one the agent
    // asked for when that was another (xhigh → max), so a level the
    // agent didn't pick reads as the agent's or as magpie's at a glance
    // (呆滞 on X: Pi 里面选择是 xhigh 但是 magpie 里面显示的是 max);
    // how it came to be is in its title and the request's story. Short of
    // room, where it went gives way first, then the level asked for, then
    // the one sent, each cut with an ellipsis in its own box (#435,
    // azir12345: 文字重叠 — "medium → low" was drawn over the numbers)
    const to = el("span", "to");
    to.append(el("i"), el("span", "said", said));
    if (tr?.effort) {
      const ef = el("span", "ef" + (tr.picked ? " picked" : ""));
      const was = r.effort && r.effort !== tr.effort;
      if (was) ef.append(el("span", "was", r.effort));
      ef.append(el("span", "now", (was ? " → " : "") + tr.effort));
      ef.title = effortNote(r, tr);
      to.append(ef);
    }
    if (tr?.fast) {
      // sent in its vendor's fast mode, as the group's member is
      const ft = el("span", "ef", t("fast"));
      ft.title = t("Sent in its vendor's fast mode, as the group says");
      to.append(ft);
    }
    if (tr?.swapped && tr.done && tryOk(tr)) to.append(swapTag(tr, true)); // beside the model asked for
    else if (tr?.routed && tr.done && tryOk(tr)) to.append(routedTag(tr));
    if (tr?.upstream && tr.done && tryOk(tr)) { to.append(upstreamTag(tr)); to.classList.add("upstreamed"); }
    const info = el("span", "meta");
    if (meta.length) info.append(el("span", "retry-count", meta.join(" · ")));
    info.append(...metrics.map((m) => metricElement(m, true)));
    info.hidden = !metrics.length && !meta.length;
    b.append(when, asked, to, info);

    b.title = title;
    return b;
  }

  // each account or key the kept requests weighed: how often it was
  // tried, answered and failed in them, and how the latest found it
  function renderActs(rs) {
    const by = new Map();
    // The agent's current sign-in uses its provider's ID; after switching,
    // that same account uses provider@user and the base ID names another.
    // Group accounts by their identity, resolving each try in its own route.
    const keyOf = (w) => w.kind === "account" && w.who
      ? JSON.stringify([w.provider, w.who.trim().toLowerCase()]) : w.id;
    for (const r of [...rs].reverse()) { // oldest first, so the latest wins
      const candidates = new Map();
      r.order.forEach((w, i) => {
        const key = keyOf(w);
        const a = by.get(key) || { w, tried: 0, ok: 0, fails: {}, last: 0, rest: null, restAt: 0, seen: 0, pos: 0, models: new Set() };
        a.w = w; a.seen++; a.pos = i; a.at = r.time;
        // a later request found it resting, or not
        if (at(r.time) >= a.restAt) { a.rest = w.rest || null; a.restAt = at(r.time); }
        by.set(key, a);
        candidates.set(w.id, a);
      });
      for (const tr of r.tries) {
        const a = candidates.get(tr.id);
        if (!a || !tr.done || tr.fail === "canceled") continue; // the agent's doing, not its
        a.tried++;
        const end = at(tr.start) + (tr.ms || 0);
        if (tryOk(tr)) { a.ok++; a.last = Math.max(a.last, end); const m = tr.model || r.order.find((x) => x.id === tr.id)?.model; if (m) a.models.add(m); }
        else a.fails[tr.fail || "other"] = (a.fails[tr.fail || "other"] || 0) + 1;
        if (tr.rest && end >= a.restAt) { a.rest = tr.rest; a.restAt = end; }
      }
    }
    const list = [...by.values()].sort((x, y) => (x.w.name || "").localeCompare(y.w.name || "") || x.w.provider.localeCompare(y.w.provider) || rankOf(x.w) - rankOf(y.w) || x.pos - y.pos);
    setText(actLabel, t("Accounts and keys"));
    setText(actNote, t("over those requests"));
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
      // a provider's own row: its model, the provider's name is the heading
      if (w.kind === "provider") name.append(el("b", "", w.model || w.name));
      else name.append(el("b", "", who(w)), el("span", "", w.plan || (w.kind === "key" ? t("API key") : "")));
      let st, cls = "";
      const resting = a.rest && at(a.rest.until) > n;
      if (resting) { st = `${failWord(a.rest.why)} · ${restWhen(a.rest)}`; cls = "rest"; }
      else if (w.unlisted) { st = unlistedWord(w); cls = "left"; }
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        st = soon && soon <= n ? quota(w, "{n} used at {time}; it has renewed since", "{n} left at {time}; it has renewed since", { time: clock(a.at) })
          : (soon ? quota(w, "{n} used · renews in {d}", "{n} left · renews in {d}", { d: dur(soon - n) }) : quota(w, "{n} used", "{n} left")) + " · " + t("as of {time}", { time: clock(a.at) });
      } else if (w.kind === "account") st = t("what's left not known yet");
      else st = "";
      const tally = el("div", "tally");
      const fails = Object.entries(a.fails).map(([k, v]) => `${v} ${failWord(k)}`);
      tally.append(
        el("span", "", t("tried {n}", { n: a.tried })),
        el("span", "ok", t("answered {n}", { n: a.ok })),
        ...(fails.length ? [el("span", "bad", fails.join(", "))] : []),
        ...(a.last ? [el("span", "", t("last answered {time}", { time: clock(a.last) }))] : []));
      row.append(name, el("div", "st " + cls, st), tally);
      // the models it answered, on a line of their own, not wrapped in
      // among the numbers; a provider's one model is already its name
      const mdls = [...a.models].filter((m) => !(w.kind === "provider" && m === w.model));
      if (mdls.length) {
        const ms = el("div", "mdls");
        ms.append(...mdls.map((m) => el("code", "mdl", m)));
        row.append(ms);
      }
      if (resting && a.rest.why === "verify") {
        // the vendor wants the account verified (#152): where, and a way to
        // stop its rest once it is
        const fix = el("div", "fix"), rest = a.rest;
        if (rest.link) { const b = el("button", "link", t("Verify the account ↗")); b.onclick = () => api("open", { url: rest.link }).catch(() => {}); fix.append(b); }
        const go = el("button", "link", t("It's verified — try it again"));
        go.onclick = async () => {
          try { await api("gateway/unrest", { key: rest.key }); rest.until = new Date().toISOString(); render(); } catch (e) { go.textContent = e.message; }
        };
        fix.append(go);
        row.append(fix);
      }
      if (w.kind === "account" && w.known) {
        const bar = el("div", "bar"), bi = el("i");
        bi.style.width = fill(w);
        bar.append(bi);
        row.append(bar);
      }
      row.title = w.id;
      out.push(row);
    }
    patch(acts, out);
  }

  // patch puts a list's new rows in, keeping each old one that is the same
  // (and has no button, whose handler is the new one's): the accounts list
  // emptied and filled again each second (its countdowns) had WebKit scroll
  // the page, a frame at a time, as if the list had been shorter
  function patch(box, rows) {
    const old = [...box.children];
    const same = (r, o) => o && !r.querySelector("button") && r.isEqualNode(o);
    if (old.length === rows.length && rows.every((r, i) => same(r, old[i]))) return;
    rows = rows.map((r, i) => (same(r, old[i]) ? old[i] : r));
    rows.forEach((r, i) => { if (box.children[i] !== r) box.insertBefore(r, box.children[i] || null); });
    while (box.children.length > rows.length) box.lastElementChild.remove();
  }

  // renderAll redraws once a frame, however many trace updates and plays
  // asked for it in between (#308), and not at all while nobody sees the
  // page: coming back into sight redraws it (see resume)
  let drawing = 0;
  function renderAll() { if (!drawing) drawing = requestAnimationFrame(drawAll); }
  function drawAll() {
    drawing = 0;
    if (!shown()) return;
    render(); renderLog(); renderHist();
  }
  const newest = () => (day ? past : [...routes.values()]).filter(matchesPurpose).reduce((a, b) => (!a || b.id > a.id ? b : a), null);

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
  async function play(id, synced) {
    const routes = src(); // a replay's, if it is one
    let r = routes.get(id);
    const g = gen;
    playing.set(id, g);
    cur = r;
    if (!synced) sync();
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
        const row = rows.get(tryseat(r, r.tries[i]));
        if (!row) { i++; continue; }
        flying.set(dot, { id: tryseat(r, r.tries[i]), agent: r.agent });
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
        if (tryOk(tr)) {
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
      if (playing.get(id) === g) playing.delete(id);
      renderAll();
    }
  }

  // home: from an account back through magpie to the agent
  const home = (row, A) => {
    const ws = wiresTo(row);
    return [...[...ws].reverse().map((p) => ({ p, rev: true })), via(tip(ws[0], false, true), tip(A.wire, true, true)), { p: A.wire, rev: true }];
  };

  // stopPlays ends whatever is flying: each play sees its gen gone and
  // clears up after itself
  function stopPlays() {
    gen++; flying.clear(); playing.clear();
    for (const p of sky.querySelectorAll(".pkt, .rt-flier")) p.remove();
    wake();
  }

  // ---------- replay ----------

  // replay plays kept requests again as they happened, from the trace the
  // gateway kept: each at its time, each try as long as it took, and the
  // stage — every agent, account, rest and sentence — as it was then. A
  // wait longer than GAP is cut to GAP, so a minute's answer or a quiet
  // hour doesn't hold it up; the clock over the stage still tells the time
  // it is replaying, running fast through what was cut.
  const GAP = 1800, LEAD = 500;
  const ends = (r) => Math.max(at(r.time) + (r.ms || 0), ...r.tries.map((tr) => at(tr.start) + (tr.ms || 0)));
  function replay(list, back) {
    list = list.filter((r) => r.done).sort((a, b) => a.id - b.id);
    if (!list.length) return;
    const all = [];
    for (const r of list) { all.push(at(r.time), ends(r)); for (const tr of r.tries) all.push(at(tr.start), at(tr.start) + (tr.ms || 0)); }
    const ts = [...new Set(all)].sort((a, b) => a - b), vs = [];
    let v = 0;
    ts.forEach((x, i) => { if (i) v += Math.min(x - ts[i - 1], GAP); vs.push(v); });
    const vOf = (x) => vs[ts.indexOf(x)];
    const plan = list.map((r, i) => ({ r, n: i + 1, s: vOf(at(r.time)), e: vOf(ends(r)),
      tries: r.tries.map((tr) => ({ s: vOf(at(tr.start)), e: vOf(at(tr.start) + (tr.ms || 0)) })) }));
    const speed = rp?.speed || 1;
    rp = null; // the old one, if one was playing, stops here
    stopPlays();
    pinned = null;
    rp = { routes: new Map(), plan, ts, vs, v: -LEAD, total: v, speed, back: back || null, t: performance.now(), real: ts[0] - LEAD };
    rTrack.replaceChildren(rHead, ...plan.map((g) => {
      const m = el("i", "rp-mark");
      m.style.left = (g.s / (v || 1)) * 100 + "%";
      m.style.setProperty("--agent", hueOf(g.r.agent));
      m.title = `${clock(g.r.time)} · ${agentName(g.r.agent)} · ${g.r.model}`;
      g.mark = m;
      return m;
    }));
    rbar.hidden = false;
    renderAll();
    requestAnimationFrame(step);
  }
  // ghost is a request as it was v into the replay
  function ghost(g, v) {
    const tries = [];
    g.r.tries.forEach((tr, i) => {
      const x = g.tries[i];
      if (x.s <= v) tries.push(x.e <= v ? tr : { ...tr, done: false, status: 0, rest: null, again: false });
    });
    return { ...g.r, tries, done: g.e <= v };
  }
  // realAt is the time it was v into the replay, and how much faster than
  // time it runs there
  function realAt(p, v) {
    const { ts, vs } = p;
    if (v <= 0) return [ts[0] + v, 1];
    for (let k = 1; k < ts.length; k++) {
      if (v > vs[k]) continue;
      const dv = vs[k] - vs[k - 1], dt = ts[k] - ts[k - 1];
      return [dv ? ts[k - 1] + (v - vs[k - 1]) * dt / dv : ts[k], dv ? dt / dv : 1];
    }
    return [ts[ts.length - 1], 1];
  }
  const same = (a, b) => a.done === b.done && a.tries.length === b.tries.length && a.tries.every((x, i) => x.done === b.tries[i].done);
  function step(ts) {
    const p = rp;
    if (!p) return;
    p.v += Math.min(ts - p.t, 250) * p.speed; // a replay out of sight waits
    p.t = ts;
    let fast;
    [p.real, fast] = realAt(p, p.v);
    let moved = false;
    for (const g of p.plan) {
      if (g.s > p.v) continue;
      const was = p.routes.get(g.r.id), is = ghost(g, p.v);
      if (was && same(was, is)) continue;
      p.routes.set(g.r.id, is);
      moved = true;
      if (!was) play(g.r.id);
    }
    wake();
    // as the gateway's own trace would: a try begun or ended shows at once
    if (moved) { if (cur && p.routes.has(cur.id)) cur = p.routes.get(cur.id); renderAll(); }
    replayBar(p, fast);
    if (p.v > p.total && !playing.size) { endReplay(); return; }
    requestAnimationFrame(step);
  }
  function replayBar(p, fast) {
    const d = new Date(p.real);
    rClock.textContent = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) + "." + Math.floor(d.getMilliseconds() / 100);
    const day = d.toDateString() === new Date().toDateString() ? t("today") : d.toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" });
    setText(rDay, day);
    // a quiet stretch cut short: the clock runs fast through it, whatever
    // the speed picked
    const skip = fast > 1.5 ? t("skipping a quiet stretch · clock {n}×", { n: Math.round(fast) }) : "";
    // written only when it changes: WebKit sends no click to a button whose
    // text was replaced between the press and the release, and this runs
    // every frame, so Stop and the speed did nothing when clicked
    setText(rSkip, skip);
    setText(rSpeed, t("{n}× speed", { n: p.speed }));
    setText(rStop, t("Stop replay"));
    setText(rTop.firstChild, t("Replaying"));
    rHead.style.left = Math.max(0, Math.min(100, (p.v / (p.total || 1)) * 100)) + "%";
    // the requests in flight at this moment of the replay, else the last one
    const on = p.plan.filter((g) => playing.has(g.r.id) || (p.routes.has(g.r.id) && !p.routes.get(g.r.id).done));
    const shown_ = on.length ? on : p.plan.filter((g) => p.routes.has(g.r.id)).slice(-1);
    for (const g of p.plan) g.mark.classList.toggle("on", on.includes(g));
    const key = shown_.map((g) => g.r.id + ":" + p.routes.get(g.r.id)?.tries.length + ":" + p.routes.get(g.r.id)?.done).join(",") + (shown_.length ? "" : "-");
    if (rWhat.dataset.key === key) return;
    rWhat.dataset.key = key;
    rWhat.replaceChildren(...(shown_.length ? shown_.map((g) => {
      const r = p.routes.get(g.r.id) || g.r, row = el("div", "rp-req" + (on.includes(g) ? " on" : ""));
      const sw = el("i", "ag");
      sw.style.setProperty("--agent", hueOf(r.agent));
      const [said] = outcome(r);
      row.append(el("span", "rp-n", t("request {i} of {n}", { i: g.n, n: p.plan.length })), sw,
        el("b", "", agentName(r.agent)), el("code", "mdl", r.model), ...(r.kind ? [kindTag(r)] : []), el("span", "rp-to", "→ " + said),
        el("span", "rp-at", t("sent {time}", { time: new Date(r.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) })));
      row.onclick = () => { cur = r; renderLog(); };
      return row;
    }) : [el("span", "rp-wait", t("starting…"))]));
  }
  // seek plays the replay on from where on its track was clicked
  function seek(frac) {
    const p = rp;
    if (!p) return;
    const v = Math.max(0, Math.min(1, frac)) * p.total;
    stopPlays(); // the flights end; the replay goes on
    p.v = v; p.t = performance.now(); p.routes.clear();
    for (const g of p.plan) if (g.s <= v) p.routes.set(g.r.id, ghost(g, v));
    // one in flight there flies on from where it was; the rest are done
    for (const g of p.plan) if (p.routes.has(g.r.id) && !p.routes.get(g.r.id).done) play(g.r.id);
    const last = [...p.routes.values()].pop();
    if (last) { cur = last; sync(true); }
    rWhat.dataset.key = "";
    renderAll();
  }
  function endReplay(stop) {
    const p = rp;
    if (!p) return;
    rp = null;
    rbar.hidden = true;
    if (stop) stopPlays();
    const b = p.back && (routes.get(p.back.id) || (day && past.find((x) => x.id === p.back.id)));
    pinned = b || null;
    cur = b || newest();
    if (cur) { sync(true); renderAll(); }
    else empty();
  }
  rSpeed.onclick = () => { if (rp) { rp.speed = rp.speed >= 8 ? 1 : rp.speed * 2; replayBar(rp, 1); } };
  rStop.onclick = () => endReplay(true);
  rTrack.onclick = (e) => { const b = rTrack.getBoundingClientRect(); seek((e.clientX - b.left) / b.width); };

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

  // resume: the page back in sight — another tab left, the window shown
  // again, or frames not drawn for a while (a hidden or covered window
  // draws none, and may not say it is hidden). What was flying then is
  // stale: it ends, the stage is drawn as it is now, and only the requests
  // still under way fly (#302).
  const LIVE = 4;
  const underWay = () => pinned || day ? [] : [...src().values()].filter((r) => !r.done && matchesPurpose(r)).sort((a, b) => a.id - b.id).slice(-LIVE);
  function resume() {
    stopPlays();
    if (!loaded) return;
    if (!pinned && !rp) cur = newest();
    if (cur) sync(true);
    else empty();
    for (const r of underWay()) play(r.id);
    renderAll();
  }
  let seen = false, lastFrame = 0, ticking = 0;
  // the loop runs only while the page is the one in sight: out of sight it
  // is not scheduled at all, so a hidden Routing page — the window hidden,
  // another view picked, another tab open — asks for no frames forever
  // (#302's other half). Shown again, start() resumes it, and resume()
  // draws the page as it is now. The requests that came meanwhile are
  // listed by the poll, which never stops.
  function frame(ts) {
    ticking = 0;
    const vis = shown();
    if (vis && (!seen || ts - lastFrame > 1000)) resume();
    seen = vis;
    lastFrame = ts;
    if (vis) {
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
    } else pause();
    if (vis) ticking = requestAnimationFrame(frame);
  }
  function pause() {
    if (ticking) cancelAnimationFrame(ticking);
    ticking = 0;
    seen = false;
    endReplay(true);
    for (const tr of trips) tr.res();
    trips = [];
    wake();
    if (capQ.length) { show(capQ[capQ.length - 1]); capQ = []; }
  }
  // Visibility events can arrive after the browser has suspended frames, so
  // finish hidden work here too. The first visible frame alone owns resume.
  function start() {
    if (!shown()) { pause(); return; }
    if (!ticking) ticking = requestAnimationFrame(frame);
  }
  // in sight again: the view picked, the window shown, another tab left,
  // the window covered and drawing frames once more
  new MutationObserver(start).observe($("#view-routing"), { attributes: true, attributeFilter: ["hidden"] });
  document.addEventListener("visibilitychange", start);
  window.addEventListener("focus", start);
  // countdowns tick once a second
  setInterval(() => { if (shown()) { if (!pinned && loaded && cur) sync(); render(); renderActs(listed()); } }, 1000);

  function offline(msg) {
    off.textContent = msg;
    off.hidden = !msg;
    for (const e of [top, stage, foot, log]) e.hidden = !!msg;
    more.hidden = !!msg;
  }

  function empty() {
    offline("");
    what.replaceChildren(el("b", "", t(purpose || day ? "No requests match these filters." : "Waiting for a request")));
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
    list.replaceChildren(el("li", "idle", t(purpose || day ? "No requests match these filters." : "No request yet")));
    say(t("Every request an agent sends to magpie shows up here, routed for real."));
    log.hidden = true;
    renderHist(); // none live, but the days the history keeps are still there to look at
    layout();
  }

  async function poll() {
    for (;;) {
      try {
        const res = await fetch(`/api/gateway/trace?after=${seq}${loaded ? "&wait=1" : ""}`);
        const d = await res.json();
        noteAccounts(d);
        skew = at(d.now) - Date.now();
        mine = d.mine;
        hubText();
        traceTotals = d.totals || traceTotals;
        if (!mine) {
          const gw = providers?.gateway;
          offMsg = !gw?.running ? "The gateway isn't running, so nothing is routed."
            : gw.window ? "Another magpie serves the gateway; its routing plays live in that magpie's window."
            // magpie serve: its routing isn't shown anywhere
            : "The gateway is served by a magpie without a window (magpie serve), so its routing can't be watched. Stop it and let this magpie serve the gateway to see routing live.";
          offline(pinned ? "" : t(offMsg));
          loaded = false;
          renderPanel();
          await new Promise((r) => setTimeout(r, 5000));
          continue;
        }
        if (d.seq < seq) routes.clear(); // the gateway started over
        seq = d.seq;
        const first = !loaded;
        const fresh = [];
        for (const r of d.routes) {
          if (!routes.has(r.id) && !first) fresh.push(r.id);
          routes.set(r.id, whole(r));
        }
        for (const id of [...routes.keys()].sort((a, b) => a - b).slice(0, -60)) routes.delete(id);
        loaded = true;
        offMsg = "";
        renderPanel(first || d.routes.some((r) => r.done));
        // a request done is a day's count grown: heard of now and then
        if (first || (d.routes.some((r) => r.done) && performance.now() - daysAt > 15e3)) { daysAt = performance.now(); loadDays(); }
        if (first) {
          offline("");
          // the agents' names come with the app's state, which may not be here yet
          for (let i = 0; i < 30 && !state.agents.length; i++) await new Promise((res) => setTimeout(res, 100));
          renderPanel();
          // the window opened from the tray panel's Routing tab on a request
          const asked = wanted && routes.get(wanted), r = pinned || asked || newest();
          if (wanted) { wanted = 0; params.delete("req"); history.replaceState(null, "", params.size ? "?" + params : location.pathname); }
          if (asked && asked.id !== newest().id) pinned = asked;
          if (r) { cur = r; sync(true); say(affWhy(r, true) || ruleWhy(r, true) || firstWhy(r)); renderAll(); } else empty();
          // the page's first frame came before the trace did, and found
          // nothing to fly: the requests under way fly now, not never
          if (seen && shown()) for (const u of underWay()) if (!playing.has(u.id)) play(u.id);
        } else {
          if (cur && routes.has(cur.id) && !rp) cur = routes.get(cur.id);
          if (pinned && routes.has(pinned.id)) pinned = routes.get(pinned.id);
          if (!pinned && !rp) {
            cur = newest();
            if (cur) sync();
            else empty();
          }
          // played only in sight: coming back into it plays those still
          // under way, not all that came meanwhile (#302)
          const go = !pinned && !rp && !day && shown() ? fresh.filter((id) => matchesPurpose(routes.get(id))) : [];
          for (const id of go) playing.set(id, gen);
          if (go.length) sync(); // the stage once for them all (#308)
          for (const id of go) play(id, true);
          for (const id of fresh.slice(-4)) pPlay(id);
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
    renderPanel();
  }
  new MutationObserver(words).observe(document.documentElement, { attributes: true, attributeFilter: ["lang"] });
  // a count follows Settings' number units, the panel's "today" with it
  document.addEventListener("magpie-costs-changed", () => { steady(renderHist); renderPanel(); });

  // ---------- routing groups ----------
  // The groups agents can pick as one model (group/<id>): the user's, and
  // those magpie found — one model several providers serve. Each is made,
  // changed or removed here; changing one magpie found makes it the user's.
  // Below them, each provider with several keys or accounts on, which
  // routes over them already: how, and how long a conversation stays.
  const gsec = el("div", "rt-gsec");
  const gHead = el("div", "row-head"), gList = el("div", "list rt-groups");
  const pHead = el("div", "row-head"), pList = el("div", "list rt-pools");
  // whether magpie finds groups on its own, by the list it fills (蓝猫 on
  // Discord: they could only be removed one at a time)
  const gFound = el("div", "rt-gfound");
  // how the agents' lists name every group, one setting for them all (and
  // for the providers' models), so it sits over the groups rather than in
  // one group's editor (PAMI on Discord)
  const gNames = el("div", "rt-gnames");
  gsec.append(gHead, gFound, gNames, gList, pHead, pList);
  // after the requests: a request picked in the list plays on the stage,
  // so the list sits right under it
  more.append(gsec);
  const ROUTE_OPTS = [["", "Smart"], ["order", "In order"], ["rotate", "In turn"], ["usage", "Least used"], ["pace", "Weekly pace"]];
  // a group may also be routed by hand: every request to the member the
  // user picks on its card (provider.Manual, #317) — a provider's keys can't
  const GROUP_ROUTE_OPTS = [...ROUTE_OPTS, ["manual", "Manual"]];
  const AFF_OPTS = [["", "Auto"], ["session", "Session"], ["turn", "Within a turn"], ["off", "Off"]];
  // what one rate limited with quota left does then (provider.Sink), as in
  // a provider's editor (app.js SINKS)
  const SINK_OPTS = [["", "Keeps its place"], ["sink", "Goes to the back"]];
  const SINK_HINT = {
    "": "One rate limited while it still has quota rests as long as the vendor asks, then takes its place in the order again.",
    sink: "One rate limited (429) while it still has quota goes to the back of the order, behind every one not rate limited since, and comes round again once those ahead of it are rate limited in turn — so the load goes round rather than back to the first each time. Out of quota, it rests as usual. Kept until magpie restarts.",
  };
  // in turn goes round already, and a manual group sends to one member
  // how long a group's member may take to its first token (provider.Group.FirstToken)
  const FIRST_OPTS = [[0, "Wait"], [30, "30 s"], [60, "1 min"], [120, "2 min"]];
  const sinkable = (routing) => routing !== "rotate" && routing !== "weight" && routing !== "manual";
  const AFF_HINT = {
    "": "A conversation stays with the account or key that answered it while what the vendor cached of it is worth keeping — within a turn always, across turns while it's fresh.",
    session: "A conversation stays with the account or key that answered it for the whole session, while it can answer.",
    turn: "A conversation stays put within a turn, while the agent sends tool results back; when you speak again, routing decides afresh.",
    off: "Every request is routed afresh, whoever answered its conversation before.",
  };
  const GROUP_HINT = {
    "": "Smart, over the members' accounts and keys together: of the subscriptions with quota to spare, the one whose allowance renews soonest goes first — the week decides, and an account with five hours and no week (Claude Enterprise) goes by its five hours, so ahead of every week renewing later; one resting after a failure goes last.",
    order: "In order: the first model until it can't answer, then the next — each over its own accounts or keys as its provider routes them.",
    rotate: "In turn: each conversation's next turn goes to the next member's account or key, spreading the load.",
    usage: "Least used first: the account or key with the most of its allowance left goes first.",
    pace: "Weekly pace: the account with the most of its week left per hour until it renews goes first, so less of each member's week is lost at its reset — an account with five hours and no week (Claude Enterprise) by what its five hours have left per hour until they renew, so almost always first; a key by the tokens magpie sent it lately.",
    manual: "Manual: every request goes to the model you pick on the group's card, over its own accounts or keys; the others, and the rules, wait until you pick another — none takes over when it fails.",
  };
  // a group in the group is routed by its own routing, whatever this one's
  // (planLevel, #576)
  const GROUP_NEST = "A group among the models keeps its own routing: this group only picks between it and the others, weighing it by the account or key it would try first, then tries it whole in its own order.";
  const EFFORTS = ["low", "medium", "high", "xhigh", "max"]; // provider.Efforts
  // what a rule matches, in words
  function ruleText(r) {
    const bits = [];
    if (r.tokens) bits.push(t("≥ {n} tokens", { n: r.tokens.toLocaleString() }));
    if (r.images) bits.push(t("has an image"));
    if (r.effort) bits.push(r.effort === "on" ? t("reasoning on") : t("reasoning ≥ {level}", { level: r.effort }));
    if (r.agents?.length) bits.push(r.agents.map((id) => (state.clients || state.agents || []).find((a) => a.id === id)?.name || id).join(" / "));
    if (r.intent) bits.push(t("asks for “{intent}”", { intent: r.intent }));
    if (r.compact) bits.push(t("compacting"));
    if (r.time) bits.push(timeText(r.time));
    return bits.join(" · ");
  }
  const slug = (s) => String(s || "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  let groups = null, gEdit = null; // gEdit: { id: "" for a new one, draft }
  async function loadGroups() {
    try { groups = await api("groups"); } catch { return; }
    if (!gEdit && !gsec.contains(document.activeElement)) renderGroups(); // not under someone's hands
  }
  const groupDirty = () => !!gEdit && gEdit.was !== undefined &&
    (JSON.stringify(gEdit.draft) !== gEdit.was || !!gsec.querySelector(".rt-patadd input")?.value.trim());
  async function cancelGroup() {
    if (groupDirty() && !(await confirmDiscard())) return false;
    gEdit = null;
    renderGroups();
    return true;
  }
  window.routingDirty = groupDirty;
  window.discardRouting = () => { gEdit = null; renderGroups(); };
  async function groupAction(action, body, ok) {
    if (action === "delete" && !await confirmRemoval(groups?.groups.find((g) => g.id === body.id)?.name || body.id,
      "This routing group will no longer be available to agents.")) return;
    try {
      groups = await api("groups/" + action, body);
      gEdit = null;
      renderGroups();
      if (ok) status(ok, "ok");
      load(); // the gateway's model list, the agents' pickers
    } catch (e) { status(e.message, "err"); }
  }
  // a member may be a model at an effort of its own, "provider/model:low" —
  // unless the whole is a model's own id (a :free, a :7b); see
  // provider.MemberEffort
  const LEVELS = ["none", "minimal", "low", "medium", "high", "xhigh", "max"]; // provider.MemberEfforts
  function splitMember(id) {
    id = id || "";
    if (id.startsWith("group/") || groups?.models.some((m) => m.id === id)) return [id, ""];
    const m = /^(.+):(none|minimal|low|medium|high|xhigh|max)$/i.exec(id);
    return m ? [m[1], m[2].toLowerCase()] : [id, ""];
  }
  const modelOf = (id) => groups?.models.find((m) => m.id === id) || groups?.models.find((m) => m.id === splitMember(id)[0]);
  // a group's patterns (#766, provider.IsPattern): a glob, * any run of
  // characters over the provider/model id in any case, or re:<regexp> over
  // the whole id. The group keeps them, and the models they match now
  // follow those it names, in the catalog's order (Group.Matched)
  const isPattern = (s) => s.startsWith("re:") || s.includes("*");
  function patternRe(p) {
    try {
      if (p.startsWith("re:")) return p.length > 3 ? new RegExp("^(?:" + p.slice(3) + ")$") : null;
      return new RegExp("^" + p.split("*").map((s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$", "i");
    } catch { return null; }
  }
  // the models served now the patterns match, but those the group names
  // (at any reasoning): as provider.matchesIn finds them
  function matchedBy(patterns, named) {
    const res = patterns.map(patternRe).filter(Boolean);
    const own = new Set(named.map((id) => splitMember(id)[0]));
    return (groups?.models || []).filter((m) => !own.has(m.id) && res.some((r) => r.test(m.id))).map((m) => m.id);
  }
  const patternCount = (p) => { const r = patternRe(p); return r ? (groups?.models || []).filter((m) => r.test(m.id)).length : 0; };
  const patternWords = (n) => n ? t(n === 1 ? "1 model" : "{n} models", { n }) : t("matches nothing now");
  // what a group keeps of its members: those it names, not those its
  // patterns matched when it was read — they are found again each time
  const namedOf = (g) => (g.members || []).filter((x) => !(g.matched || []).includes(x));
  const fixedOf = (id) => subOf(id) ? "" : splitMember(id)[1];
  const fixedWords = (level) => t("{level} reasoning", { level });
  // a routing group among a group's members: group/<id>
  const subOf = (id) => id?.startsWith("group/") ? groups?.groups.find((x) => "group/" + x.id === id && !x.hidden) : null;
  // a group's editor, its draft being the group as it is now. The group's own
  // card opens it, and so does a group that is a member of another: such a
  // member takes no reasoning of its own (provider.SaveGroup refuses
  // "group/x:high"), so its row says where its reasoning is set and goes
  // there, rather than leaving the slot empty.
  const openGroupEditor = async (g) => {
    if (groupDirty() && !(await confirmDiscard())) return false;
    gEdit = { id: g.id, draft: { name: g.name, members: [...g.members], match: [...(g.match || [])], matched: [...(g.matched || [])], fast: [...(g.fast || [])], off: [...(g.off || [])], routing: g.routing || "", pick: g.pick || "", affinity: g.affinity || "", sink: !!g.sink, firstToken: g.firstToken || 0, context: g.context || 0, classifier: g.classifier || "", effort: g.effort || "", levels: [...(g.levels || [])], rules: (g.rules || []).map((r) => ({ ...r, intent: r.intent || "", agents: [...(r.agents || [])], time: r.time ? { ...r.time, days: [...(r.time.days || [])] } : null })) } };
    renderGroups();
    return true;
  };
  const groupIcons = (g) => [...new Map((g.memberInfo || []).filter((i) => i.icon).map((i) => [i.provider || i.icon, i.icon])).values()];
  const memberIcon = (id) => { const s = subOf(id); return s ? stackIcon(groupIcons(s)) : icon(modelOf(id)?.icon || "generic"); };
  const memberName = (id) => { const s = subOf(id), m = modelOf(id); return s ? s.name : m ? m.name || m.id : id; };
  const memberNote = (id) => subOf(id) ? t("routing group") : [modelOf(id)?.providerName, fixedOf(id) && fixedWords(fixedOf(id))].filter(Boolean).join(" · ");
  // a member the group sends in its vendor's fast mode (Group.Fast)
  const fastIn = (g, id) => !!g?.fast?.includes(id) && !subOf(id);
  function memberLabel(g, id) {
    const i = g.memberInfo?.find((x) => x.id === id), m = modelOf(id), s = subOf(id), f = fixedOf(id);
    if (s) return `${t("routing group")} · ${s.name}`;
    const at = (f ? ` · ${fixedWords(f)}` : "") + (fastIn(g, id) ? ` · ${t("fast")}` : "");
    if (m) return `${m.providerName} · ${m.name || m.id}${at}`;
    return i?.name ? `${i.name} · ${i.model}${at}` : id;
  }
  function renderGroups() {
    if (!groups) return;
    // not under a row being dragged: it is drawn when let go
    if (groupArranging) { groupRenderPending = true; return; }
    steady(drawGroups);
  }
  // gSel: the groups picked to be removed together, while picking (lc on
  // Discord: they could only be removed one at a time); null otherwise
  let gSel = null;
  // gQ: the groups filtered by name and by the models in them, as many
  // as there may be (PAMI on Discord); kept across redraws, and focused
  // again when one comes while typing
  const gQ = el("input", "sess-filter rt-gfilter");
  gQ.type = "search";
  gQ.spellcheck = false;
  gQ.autocomplete = "off";
  gQ.oninput = () => renderGroups();
  gQ.onkeydown = (e) => { if (e.key === "Escape" && gQ.value) { e.stopPropagation(); gQ.value = ""; renderGroups(); } };
  // groupMatches: every word of the filter in the group's name, id, or a
  // member's id or label (its provider and model names)
  function groupMatches(g, words) {
    const hay = [g.name, g.id, "group/" + g.id, ...g.members.flatMap((id) => [id, memberLabel(g, id)]), ...(g.patterns || []).map((p) => p.pattern)].join("\n").toLowerCase();
    return words.every((w) => hay.includes(w));
  }
  function drawGroups() {
    const all = groups.groups.filter((g) => !g.hidden), hidden = groups.groups.filter((g) => g.hidden);
    const words = gQ.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    // the group being changed stays, whatever the filter
    const shown = words.length ? all.filter((g) => gEdit?.id === g.id || groupMatches(g, words)) : all;
    if (gSel) gSel = new Set([...gSel].filter((id) => shown.some((g) => g.id === id)));
    if (gSel && !all.length) gSel = null;
    const newBtn = el("button", "text rt-gnew");
    newBtn.type = "button";
    newBtn.append(svg(PLUS, 11, 1.8), el("span", "", t("New group")));
    newBtn.onclick = () => newGroup();
    const head = [el("span", "label", t("Routing groups")), el("span", "grow"), el("span", "note", t("models agents pick as one"))];
    const typing = document.activeElement === gQ, [a, b] = [gQ.selectionStart, gQ.selectionEnd];
    if (all.length > 1 || gQ.value) {
      gQ.placeholder = t("Filter groups and models");
      gQ.setAttribute("aria-label", gQ.placeholder);
      head.push(gQ);
    }
    if (!gSel) {
      // several removed at once: pick them, then Remove
      if (all.length > 1) {
        const pick = el("button", "text rt-gselect", t("Select"));
        pick.title = t("Pick several groups to remove together");
        pick.onclick = async () => {
          if (groupDirty() && !(await confirmDiscard())) return;
          gEdit = null; gSel = new Set(); renderGroups();
        };
        head.push(pick);
      }
      head.push(newBtn);
    }
    gHead.replaceChildren(...head);
    if (typing && gQ.isConnected) { gQ.focus({ preventScroll: true }); try { gQ.setSelectionRange(a, b); } catch {} }
    drawFound();
    drawNames();
    const rows = [];
    if (gSel) rows.push(selectBar(shown));
    if (gEdit && !gEdit.id) rows.push(groupEditor(null));
    for (const g of shown) rows.push(gSel ? pickedRow(g) : gEdit?.id === g.id ? groupEditor(g) : groupRow(g));
    if (!rows.length && all.length) rows.push(el("div", "none rt-gnone", t("No group or model matches “{q}”", { q: gQ.value.trim() })));
    if (!rows.length) rows.push(el("div", "none rt-gnone", groups.found === false
      ? t("No group yet. New group makes one of any models you like.")
      : t("No group yet. A model two of your providers serve becomes one on its own; New group makes one of any models you like.")));
    // the found groups removed stay removed, magpie doesn't make them
    // again: not listed, one line under the list says how many, and its
    // menu brings one back. With found groups off, none would be anyway.
    if (hidden.length && groups.found !== false) {
      const h = el("div", "rt-ghidden");
      const b = el("button", "text rt-gremoved");
      b.type = "button";
      b.setAttribute("aria-haspopup", "menu");
      b.append(el("span", "", t(hidden.length === 1 ? "1 found group removed" : "{n} found groups removed", { n: hidden.length })), svg(CHEV, 11, 1.6));
      b.title = t("magpie doesn't make them again. Click to bring one back.");
      b.onclick = (e) => {
        e.stopPropagation();
        const again = agentMenu?.anchor === b;
        closeAgentMenu();
        if (again) return;
        openRowMenu(b, hidden.map((g) => ({ name: g.id.replace(/^auto-/, ""), icon: REAPPLY, tip: t("Bring it back"),
          run: () => groupAction("show", { id: g.id }, t("{name} is back", { name: g.id })) })));
      };
      h.append(b);
      rows.push(h);
    }
    gList.replaceChildren(...rows);
    renderPools();
  }
  // selectBar: over the groups while picking: all or none, how many are
  // picked, Remove them after confirmation, and Done
  function selectBar(shown) {
    const bar = el("div", "rt-gsel");
    const all = el("input");
    all.type = "checkbox";
    all.setAttribute("aria-label", t("Select every group"));
    const n = el("span", "note");
    const rm = el("button", "text danger rt-gremove");
    rm.type = "button";
    const done = el("button", "text", t("Done"));
    done.type = "button";
    done.onclick = () => { gSel = null; renderGroups(); };
    const sync = () => {
      const k = gSel.size;
      all.checked = k > 0 && k === shown.length;
      all.indeterminate = k > 0 && k < shown.length;
      n.textContent = k ? t("{n} selected", { n: k }) : t("Pick the groups to remove");
      rm.disabled = !k;
      rm.textContent = t("Remove");
      for (const r of gList.querySelectorAll(".rt-gpick")) {
        const on = gSel.has(r.dataset.id);
        r.classList.toggle("on", on);
        r.querySelector("input").checked = on;
      }
    };
    bar.sync = sync;
    all.onchange = () => { gSel = new Set(all.checked ? shown.map((g) => g.id) : []); sync(); };
    rm.onclick = async () => {
      if (!gSel.size) return;
      const ids = shown.map((g) => g.id).filter((id) => gSel.has(id));
      if (!await confirmRemoval(shown.filter((g) => ids.includes(g.id)).map((g) => g.name).join(t(", ")), "These routing groups will no longer be available to agents.")) return;
      rm.disabled = true;
      try {
        groups = await api("groups/delete", { ids });
        gSel = null;
        renderGroups();
        status(ids.length === 1 ? t("{name} removed", { name: ids[0] }) : t("{n} groups removed", { n: ids.length }), "ok");
        load(); // the gateway's model list, the agents' pickers
      } catch (e) {
        sync();
        status(e.message, "err");
      }
    };
    bar.append(all, n, el("span", "grow"), rm, done);
    queueMicrotask(sync);
    return bar;
  }
  // pickedRow: a group while picking: its box, logos and name; a click
  // anywhere on it picks it or lets it go
  function pickedRow(g) {
    const row = el("label", "rt-group rt-gpick" + (g.ready ? "" : " off"));
    row.dataset.id = g.id;
    const cb = el("input");
    cb.type = "checkbox";
    cb.checked = gSel.has(g.id);
    cb.setAttribute("aria-label", g.name);
    cb.onchange = () => {
      cb.checked ? gSel.add(g.id) : gSel.delete(g.id);
      gList.querySelector(".rt-gsel")?.sync();
    };
    const ics = el("span", "ics");
    ics.append(stackIcon(groupIcons(g)));
    const main = el("div", "main"), nm = el("div", "nm");
    nm.append(el("b", "", g.name), el("code", "mdl", "group/" + g.id));
    if (g.auto) nm.append(el("small", "auto", t("found by magpie")));
    main.append(nm, el("div", "mem", g.members.map((id) => memberLabel(g, id)).join(g.routing === "order" ? " → " : " · ")));
    row.append(cb, ics, main);
    return row;
  }
  // drawFound: the switch for the groups magpie finds on its own — a
  // model two or more providers serve, as auto-<model> — all at once.
  // Off, none is listed or served: the groups the user made or changed
  // stay, and an agent set to a found one is moved to its model from one
  // provider (agent.Reseat), as a request still naming one goes there.
  function drawFound() {
    const on = groups.found !== false;
    const s = el("button", "lib-switch" + (on ? " on" : ""));
    s.type = "button";
    s.setAttribute("role", "switch");
    s.setAttribute("aria-checked", String(on));
    s.setAttribute("aria-label", t("Find groups on their own"));
    s.append(el("i"));
    s.onclick = (e) => { e.stopPropagation(); setFound(!on, s); };
    const txt = el("div", "txt");
    txt.append(el("b", "", t("Find groups on their own")), el("small", "", on
      ? t("A model two or more of your providers serve becomes a group of them (auto-…). Switch it off to list and serve only the groups you made or changed.")
      : t("Off: only the groups you made or changed are listed and served. An agent set to a found group is moved to its model from one provider, and a request still naming one goes there too.")));
    gFound.replaceChildren(txt, s);
  }
  function drawNames() {
    const txt = el("div", "txt");
    txt.append(el("b", "", t("Names in agents’ lists")), el("small", "", t("Whether “· routing group” follows a group’s name in the agents’ lists: one setting for every group, and for the provider after each model’s name, as on Settings.")));
    // an open editor says what its group will be called: drawn again with it
    gNames.replaceChildren(txt, suffixSegs(() => renderGroups()));
  }
  async function setFound(on, s) {
    if (groupDirty() && !(await confirmDiscard())) return;
    s.classList.toggle("on", on);
    s.setAttribute("aria-checked", String(on));
    try {
      groups = await api("groups/found", { on });
      gEdit = null;
      renderGroups();
      saidMoved(on ? t("Found groups are on") : t("Found groups are off: only yours are listed and served"), groups.moved);
      load(); // the gateway's model list, the agents' pickers
    } catch (e) {
      s.classList.toggle("on", !on);
      s.setAttribute("aria-checked", String(!on));
      status(e.message, "err");
    }
  }
  function groupRow(g) {
    const row = el("div", "rt-group" + (g.ready ? "" : " off") + (g.disabled ? " disabled" : ""));
    row.dataset.id = g.id;
    const ics = groupHandle(g, row);
    const main = el("div", "main");
    const nm = el("div", "nm");
    nm.append(el("b", "", g.name), el("code", "mdl", "group/" + g.id));
    if (g.auto) nm.append(el("small", "auto", t("found by magpie")));
    const manual = g.routing === "manual";
    const sep = g.routing === "order" ? " → " : " · ";
    const mem = manual ? pickRow(g) : el("div", "mem", g.members.map((id) => memberLabel(g, id) + (g.off?.includes(id) ? ` (${t("off")})` : "")).join(sep));
    main.append(nm, mem);
    for (const p of g.patterns || []) {
      const line = el("div", "mem pat" + (p.models ? "" : " none"));
      line.append(el("code", "", p.pattern), document.createTextNode(" · " + patternWords(p.models)));
      line.title = p.models ? t("Every model this pattern matches is in the group, as providers list them") : t("No model magpie serves matches this pattern now");
      main.append(line);
    }
    const m = GROUP_ROUTE_OPTS.find(([id]) => id === (g.routing || "")) || ROUTE_OPTS[0];
    const tags = el("span", "tags");
    tags.append(el("span", "tag", t(m[1])));
    if (g.affinity) tags.append(el("span", "tag", t(AFF_OPTS.find(([id]) => id === g.affinity)?.[1] || "")));
    if (g.sink && sinkable(g.routing || "")) {
      const k = el("span", "tag sink", t("Rate limited to the back"));
      k.title = t(SINK_HINT.sink);
      tags.append(k);
    }
    if (g.firstToken > 0 && !manual) tags.append(el("span", "tag", t("Next after {n} without a first token", { n: took(g.firstToken * 1000) })));
    if (g.rules?.length) {
      const r = el("span", "tag" + (manual ? " idle" : ""), t(g.rules.length === 1 ? "1 rule" : "{n} rules", { n: g.rules.length }));
      r.title = (manual ? t("The rules wait while you pick the model by hand.") + "\n" : "") + g.rules.map((x, i) => `${i + 1}. ${ruleText(x)} → ${memberLabel(g, x.use)}`).join("\n");
      tags.append(r);
    }
    if (g.disabled) tags.append(el("span", "tag idle", t("switched off")));
    else if (!g.ready) tags.append(el("span", "tag bad", t("no member ready")));
    const edit = el("button", "text", t("Edit"));
    edit.onclick = (e) => { e.stopPropagation(); open(); };
    const open = () => openGroupEditor(g);
    row.onclick = open;
    row.oncontextmenu = (e) => { e.preventDefault(); groupMenu(ics, g); };
    row.append(ics, main, tags, edit);
    // a group of the user's switches off as a whole, kept as it is (PAMI
    // on Discord); one magpie found is removed rather (provider.SwitchGroup)
    if (!g.auto) {
      const sw = el("button", "lib-switch rt-gon" + (g.disabled ? "" : " on"));
      sw.type = "button";
      sw.setAttribute("role", "switch");
      sw.setAttribute("aria-checked", String(!g.disabled));
      sw.setAttribute("aria-label", g.name);
      sw.title = g.disabled ? t("Off: agents aren't offered {name} and a request to it is turned away. Click to switch it on", { name: g.name })
        : t("On: agents may pick {name}. Click to switch it off and keep it as it is", { name: g.name });
      sw.append(el("i"));
      sw.onclick = async (e) => {
        e.stopPropagation(); // the card opens the editor; this switches
        const held = document.activeElement === sw;
        await groupAction("switch", { id: g.id, on: !!g.disabled }, t(g.disabled ? "{name} switched on" : "{name} switched off", { name: g.name }));
        if (held) gList.querySelector(`.rt-group[data-id="${CSS.escape(g.id)}"] .rt-gon`)?.focus({ preventScroll: true });
      };
      row.append(sw);
    }
    return row;
  }
  // groupHandle is a group row's logos, which are also its handle, as an
  // agent row's logo is (#779): drag it to move the row, click it (or
  // right-click the row) for Move up and Move down, and Alt+↑/↓ moves it
  // from the keyboard. The grip that says so is drawn in the row's margin
  // only on hover (routing.css), as on the Agents page. The order is the
  // one /v1/models lists the groups in too, which agents' pickers show.
  let groupArranging = false, groupRenderPending = false, groupArrangeSeq = 0;
  const shownGroups = () => groups.groups.filter((g) => !g.hidden).map((g) => g.id);
  function groupHandle(g, row) {
    const b = el("button", "ics ag-handle rt-ghandle");
    b.type = "button";
    b.setAttribute("aria-label", t("Arrange {agent}", { agent: g.name }));
    b.setAttribute("aria-haspopup", "menu");
    b.title = t("Drag to reorder — agents' model lists show the groups in this order · Alt+↑/↓ to move");
    b.append(stackIcon(groupIcons(g)));
    b.onkeydown = (e) => {
      if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
      e.preventDefault();
      e.stopPropagation();
      moveGroup(g.id, nextTo(g.id, e.key === "ArrowUp" ? -1 : 1), true);
    };
    // the card opens the editor; the handle its menu, but not for the
    // click that ends a drag
    b.onclick = (e) => { e.stopPropagation(); if (!b.dataset.dragged) groupMenu(b, g); delete b.dataset.dragged; };
    b.onpointerdown = (e) => {
      if (groupArranging) return;
      // an editor open in the list is no row to move past: the rows
      // dragged among are the cards, and where one is let go is named by
      // the card it lands on
      const rows = [...gList.children].filter((r) => r.classList.contains("rt-group"));
      groupArranging = dragRows(e, b, row, gList, rows, (to) => moveGroup(g.id, shownGroups().indexOf(rows[to].dataset.id)), closeAgentMenu, () => {
        groupArranging = false;
        if (groupRenderPending) { groupRenderPending = false; renderGroups(); }
      });
    };
    return b;
  }
  function groupMenu(anchor, g) {
    const again = agentMenu?.anchor === anchor;
    closeAgentMenu();
    if (again) return;
    const up = nextTo(g.id, -1), down = nextTo(g.id, 1);
    openRowMenu(anchor, [
      { name: "Move up", icon: MOVE_UP, key: ALT + "↑", off: up < 0, run: () => moveGroup(g.id, up, true) },
      { name: "Move down", icon: MOVE_DOWN, key: ALT + "↓", off: down < 0, run: () => moveGroup(g.id, down, true) },
    ]);
  }
  // nextTo: where a group moved one up (-1) or down goes, among them all:
  // the place of the card listed next to it, so a filtered list moves it
  // past the one the reader sees; -1 at either end
  function nextTo(id, by) {
    const seen = [...gList.querySelectorAll(".rt-group[data-id]")].map((r) => r.dataset.id);
    const n = seen[seen.indexOf(id) + by];
    return n === undefined || !seen.includes(id) ? -1 : shownGroups().indexOf(n);
  }
  // moveGroup puts the group at index `to` among the ones listed; the
  // removed ones keep their places after them. The list is drawn in its new
  // order at once and put back if the save fails.
  async function moveGroup(id, to, keep) {
    const prev = groups;
    const ids = shownGroups();
    const from = ids.indexOf(id);
    if (from < 0 || to < 0 || to >= ids.length || to === from) return;
    ids.splice(to, 0, ...ids.splice(from, 1));
    const order = [...ids, ...prev.groups.filter((g) => g.hidden).map((g) => g.id)];
    const byId = new Map(prev.groups.map((g) => [g.id, g]));
    const handle = () => gList.querySelector(`.rt-group[data-id="${CSS.escape(id)}"] .rt-ghandle`);
    groups = { ...prev, groups: order.map((x) => byId.get(x)) };
    renderGroups();
    // the rows were drawn anew: keep the keyboard on this one
    if (keep) handle()?.focus({ preventScroll: true });
    const seq = ++groupArrangeSeq;
    let next;
    try {
      next = await api("groups/arrange", { order });
      status(t("Group order saved"), "ok");
    } catch (e) {
      next = prev;
      status(e.message, "err");
    }
    // a later move, made while this one was saved, has the last word
    if (seq !== groupArrangeSeq) return;
    const held = document.activeElement === handle();
    groups = next;
    renderGroups();
    if (held) handle()?.focus({ preventScroll: true });
    load(); // the gateway's model list, the agents' pickers
  }
  // pickRow: a manual group's members on its card, the one every request
  // goes to marked; clicking another sends them there from the next
  // request on (#317), as CC Switch switches a provider
  function pickRow(g) {
    const picked = g.picked || (g.members.includes(g.pick) ? g.pick : g.members[0]);
    const box = el("div", "rt-picks");
    box.setAttribute("role", "radiogroup");
    box.setAttribute("aria-label", t("Model every request goes to"));
    for (const id of g.members) {
      const info = g.memberInfo?.find((x) => x.id === id);
      const on = id === picked;
      const b = el("button", "rt-pick" + (on ? " on" : "") + (info && !info.ready ? " off" : ""));
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", String(on));
      b.dataset.member = id;
      b.append(el("span", "dot"), memberIcon(id), el("span", "n", memberName(id)));
      const note = [memberNote(id), fastIn(g, id) && t("fast")].filter(Boolean).join(" · ");
      if (note) b.append(el("small", "", note));
      b.title = on ? t("Every request goes to {name}", { name: memberLabel(g, id) })
        : info && !info.ready ? t("No provider serves {id} now; it is skipped", { id })
        : t("Send every request to {name}", { name: memberLabel(g, id) });
      b.onclick = (e) => {
        e.stopPropagation(); // the card opens the editor; this picks
        if (on) return;
        groupAction("save", { id: g.id, name: g.name, members: namedOf(g), match: g.match || [], routing: "manual", pick: id, affinity: g.affinity || "", sink: !!g.sink, firstToken: g.firstToken || 0, rules: g.rules || [], effort: g.effort || "", classifier: g.classifier || "", context: g.context || 0, levels: g.levels || [], family: g.family || "", fast: g.fast || [], off: g.off || [] },
          t("{name}: every request to {model}", { name: g.name, model: memberName(id) }));
      };
      box.append(b);
    }
    return box;
  }
  function groupEditor(g) {
    const d = gEdit.draft;
    // whoever opened it, a draft has what the editor and Add read: a group
    // made from a model (newGroupWith) had no fast, and Add threw on it
    // and did nothing (悠悠哥 on Discord)
    for (const k of ["members", "match", "matched", "fast", "off", "rules"]) if (!Array.isArray(d[k])) d[k] = [];
    const ed = el("div", "editor rt-gedit");
    const h = el("div", "ehead");
    h.append(el("b", "", g ? g.name : t("New group")));
    if (g?.auto) h.append(el("span", "note", t("found by magpie — saving a change makes it yours")));
    ed.append(h);
    const keys = (i) => { i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") { cancelGroup(); } else if (e.key === "Enter" && i === name) saveBtn.onclick(); }; return i; };
    const name = keys(input(d.name, t("e.g. Opus anywhere")));
    const idHint = el("div", "hint");
    // an existing group's id can change (an auto- one found by magpie too);
    // a new one's is made from its name
    if (g && d.id === undefined) d.id = g.id;
    // an open group folds back into its row from its heading, as it opened
    // from the row (ARNO on Discord) — only while nothing in it has
    // changed, so a stray click never throws an edit away; Cancel does that
    if (g) {
      h.classList.add("fold");
      h.setAttribute("role", "button");
      h.tabIndex = 0;
      h.setAttribute("aria-expanded", "true");
      h.title = t("Click to fold it");
      const fold = () => {
        if (JSON.stringify(d) !== gEdit.was) return status(t("Save or Cancel your changes first"), "warn");
        gEdit = null;
        renderGroups();
      };
      h.onclick = fold;
      h.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); fold(); } };
    }
    const idIn = g ? keys(input(d.id, g.id)) : null;
    const idOf = () => {
      if (g) return groupSlug(d.id) || g.id;
      let id = groupSlug(d.name) || "group", n = 1;
      const base = id;
      while (groups.groups.some((x) => x.id === id)) id = `${base}-${++n}`;
      return id;
    };
    const showId = () => {
      idHint.textContent = t("Agents pick it as {id}", { id: "group/" + idOf() }) +
        (g && idOf() !== g.id ? " · " + t("an agent set to {id} needs setting again", { id: "group/" + g.id }) : "");
    };
    // what the agents' lists will call it (#868: an agent's narrow menu
    // cut it to "· rou…"); whether "· routing group" follows the name is
    // one setting for every group, over the list (drawNames), not one in
    // here (PAMI on Discord); a group saved here is one the user made
    const sfxSaid = el("span", "hint");
    const sayLabel = () => {
      sfxSaid.textContent = t("Agents’ lists show “{label}”", { label: (d.name.trim() || t("New group")) + (suffixMode() === "on" ? " · routing group" : "") }) +
        " · " + t("“· routing group” is set for every group above");
    };
    name.oninput = () => { d.name = name.value; showId(); sayLabel(); };
    const nw = el("div");
    nw.append(name);
    if (!g) nw.append(idHint);
    ed.append(el("label", "", t("Name")), nw);
    const sw = el("div", "gsuffix");
    sw.append(sfxSaid);
    sayLabel();
    ed.append(el("label", "", t("In agents’ lists")), sw);
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
    const infoOf = (id) => g?.memberInfo?.find((x) => x.id === id);
    const pickFrom = (label, anchor, ev, options, onPick, value = "") => openPicker({ id: "", name: "", fields: [] }, { key: "rule", label, value, menu: true, options, onPick }, anchor, ev);
    const box = el("div", "fallback");
    const list = el("div", "fbl");
    const addBtn = el("button", "rt-gadd");
    addBtn.append(svg(PLUS, 11, 1.8), el("span", "", t("Add a model")));
    // moveMember puts the member at `from` at `to` among those named — a
    // pattern's follow them, in the catalog's order — and keeps the
    // keyboard on its handle when it was there
    const named = () => d.members.filter((x) => !d.matched.includes(x)).length;
    const moveMember = (from, to, keep) => {
      if (to < 0 || to >= named() || to === from) return;
      d.members.splice(to, 0, d.members.splice(from, 1)[0]);
      draw();
      if (keep) list.querySelector(`.rt-mhandle[data-at="${to}"]`)?.focus({ preventScroll: true });
    };
    // memberHandle is a named member's place in the order, which is also
    // its handle (PAMI on Discord: only Up, one step a click): drag it to
    // move the model, or Alt+↑/↓ from the keyboard; Up and Down stay
    // beside it. The grip that says so shows only on hover, as on the
    // groups' cards.
    const memberHandle = (id, i, row) => {
      const b = el("button", "i ag-handle rt-mhandle", String(i + 1));
      b.type = "button";
      b.dataset.at = i;
      b.setAttribute("aria-label", t("Move {name}", { name: memberName(id) }));
      b.title = t("Drag to reorder · Alt+↑/↓ to move");
      b.onkeydown = (e) => {
        if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
        e.preventDefault();
        e.stopPropagation();
        moveMember(i, i + (e.key === "ArrowUp" ? -1 : 1), true);
      };
      b.onclick = (e) => { e.stopPropagation(); delete b.dataset.dragged; };
      b.onpointerdown = (e) => {
        const rows = [...list.children].filter((r) => r.querySelector(".rt-mhandle"));
        dragRows(e, b, row, list, rows, (to) => moveMember(i, to));
      };
      return b;
    };
    const draw = () => {
      list.replaceChildren();
      d.members.forEach((id, i) => {
        const m = modelOf(id), s = subOf(id);
        // a member that is itself a group is in none of groups.models
        // (groupsState leaves groups out of it), so it is read from its own
        // memberInfo instead: without this its row carried no chips at all,
        // and a group inside a group said nothing of what it takes
        const info = infoOf(id);
        const chips = m || (s ? info : undefined);
        const row = el("div", "fbrow");
        const n = el("span", "n");
        n.append(el("span", "", memberName(id)));
        if (m || s) n.append(el("small", "", subOf(id) ? memberNote(id) : m.providerName));
        const matched = d.matched.includes(id);
        if (matched) n.append(el("small", "", t("by pattern")));
        // switched off, it keeps its place and its rules but is sent
        // nothing: trying the group without it takes no removing and
        // adding back (Group.Off)
        const off = d.off.includes(id);
        const sw = el("button", "lib-switch rt-mon" + (off ? "" : " on"));
        sw.type = "button";
        sw.setAttribute("role", "switch");
        sw.setAttribute("aria-checked", String(!off));
        sw.setAttribute("aria-label", memberName(id));
        sw.title = off ? t("Off: kept in its place, sent nothing. Click to switch it on") : t("On: requests may go to it. Click to switch it off and keep its place");
        sw.append(el("i"));
        sw.onclick = () => { d.off = off ? d.off.filter((x) => x !== id) : [...d.off, id]; draw(); };
        if (off) row.classList.add("muted");
        row.append(sw, matched ? el("span", "i", String(i + 1)) : memberHandle(id, i, row), memberIcon(id), n, el("span", "grow"));
        // what the model is, in the same chips the Gateway page's list says it
        // in (modelInfo): its reasoning levels, whether it sees images and the
        // window it holds. A group mixing members that see images with ones
        // that don't had nothing here to tell one from another, and which
        // member a picture would reach was only found out by sending one.
        // A member whose list says nothing of images is marked unknown rather
        // than text-only: magpie counts it text-only for a describer
        // (gateway.blindTo), which is not the same as its list saying so.
        if (chips) row.append(modelInfo({ ...chips, group: !!s, images: info?.images ?? chips.images, imagesUnknown: info?.imagesUnknown ?? chips.imagesUnknown }));
        // the reasoning the model is sent at in this group: the group's
        // (blank), or one of its own whatever the agent asks. A group in
        // it reasons as it says.
        if (!s) {
          const [base, fixed] = splitMember(id);
          const fx = el("button", "rt-cond rt-fixed" + (fixed ? " on" : ""), fixed ? fixedWords(fixed) : t("Group's reasoning"));
          fx.title = fixed ? t("Sent at {level} reasoning whatever the agent asks, at the model's nearest level", { level: fixed }) : t("Reasons as the group's effort says");
          const levels = m?.efforts?.length ? m.efforts.filter((v) => LEVELS.includes(v)) : LEVELS.filter((v) => v !== "none" && v !== "minimal");
          fx.onclick = (ev) => pickFrom("reasoning", fx, ev, [
            { value: "", label: t("Follow the group"), note: t("as the group's effort says") },
            ...levels.map((v) => ({ value: v, label: fixedWords(v), note: t("whatever the agent asks") })),
          ], (v) => {
            const to = v ? `${base}:${v}` : base;
            if (to === id) return;
            if (d.members.includes(to)) { status(t("{name} at that reasoning is in the group already", { name: memberName(id) }), "err"); return; }
            d.members[i] = to;
            d.matched = d.matched.filter((x) => x !== id); // named now, at its own reasoning
            for (const r of d.rules) if (r.use === id) r.use = to;
            d.fast = d.fast.map((x) => x === id ? to : x);
            d.off = d.off.map((x) => x === id ? to : x);
            if (d.pick === id) d.pick = to;
            draw(); drawRules();
          }, fixed);
          row.append(fx);
          // its vendor's fast mode, where the model has one: priority
          // processing, Claude's fast mode (provider.CanFast)
          if (m?.canFast) {
            const on = d.fast.includes(id);
            const fb = el("button", "rt-cond rt-fixed rt-fast" + (on ? " on" : ""), t(on ? "Fast" : "Standard speed"));
            fb.type = "button";
            fb.setAttribute("aria-pressed", String(on));
            fb.title = on ? t("Sent in its vendor's fast mode whatever the agent asks: quicker, at a higher price") : t("Sent at its vendor's usual speed; click to send it in fast mode");
            fb.onclick = () => { d.fast = on ? d.fast.filter((x) => x !== id) : [...d.fast, id]; draw(); };
            row.append(fb);
          }
        } else {
          // a group in the group takes no reasoning of its own: its models
          // reason as it says (provider.SaveGroup refuses "group/x:high", and
          // the gateway has nowhere to put one). So the slot a model's own
          // picker sits in says where it is set instead, and goes there.
          const hx = el("button", "rt-cond rt-fixed rt-inner");
          hx.type = "button";
          // in a span, so a long group's name ellipsizes instead of widening
          // the row: the desktop app's member row doesn't wrap
          hx.append(el("span", "", t("Follows {name}", { name: s.name })));
          hx.title = t("Its models reason as {name} says: set that up on {name}'s own card, not here", { name: s.name });
          hx.onclick = async (ev) => {
            if (!(await openGroupEditor(s))) return;
            const ed = gList.querySelector(".rt-gedit");
            if (ed && window.scrollOnPurpose?.(ev)) ed.scrollIntoView({ block: "center", behavior: "smooth" });
          };
          row.append(hx);
        }
        if (s) row.title = s.members.map((x) => memberLabel(s, x)).join(s.routing === "order" ? " → " : " · ");
        if (!m && !s) { row.classList.add("off"); row.title = t("No provider serves {id} now; it is skipped", { id }); }
        if (matched) {
          // a pattern's: it follows the catalog, so it is switched off
          // rather than taken out — the pattern would find it again
          row.title = t("In the group by a pattern: switch it off to send it nothing");
          list.append(row);
          return;
        }
        if (i) { const up = el("button", "text", t("Up")); up.onclick = () => moveMember(i, i - 1); row.append(up); }
        if (i < named() - 1) { const down = el("button", "text", t("Down")); down.onclick = () => moveMember(i, i + 1); row.append(down); }
        const rm = el("button", "text", t("Remove"));
        rm.onclick = () => { d.members.splice(i, 1); rematch(); draw(); drawRules(); };
        row.append(rm);
        list.append(row);
      });
      addBtn.querySelector("span").textContent = t(d.members.length ? "Add another model" : "Add a model");
    };
    // the picker stays open for as many models as are wanted, each ticked
    // once in, and says which other groups a model is in already (PAMI on
    // Discord: a group was made one model at a time, blind)
    const inGroups = (id) => groups.groups.filter((x) => !x.hidden && x.id !== g?.id && x.members.some((m) => m === id || splitMember(m)[0] === id)).map((x) => x.name);
    const noted = (note, id) => {
      const gs = inGroups(id);
      return gs.length ? [note, t("in {groups}", { groups: gs.join(", ") })].filter(Boolean).join(" · ") : note;
    };
    addBtn.onclick = (ev) => {
      // a group in it may be any other, but never one it is in already:
      // that would put it in itself
      const subs = groups.groups.filter((x) => !x.hidden && x.id !== g?.id && !(g && x.holds?.includes(g.id)))
        .map((x) => ({ value: "group/" + x.id, label: x.name, note: noted("group/" + x.id, "group/" + x.id), icons: groupIcons(x), group: ROUTING_GROUPS, ref: "group/" + x.id }));
      const options = [...subs, ...groups.models
        .map((x) => ({ value: x.id, label: x.name || x.id, note: noted(x.providerName, x.id), icon: x.icon, group: x.providerName, ref: x.id, context: x.context }))];
      // the member a model is in the group as: itself, or with its
      // reasoning fixed (deepseek/x:max), which is ticked as it and taken
      // out by it rather than added twice (#907)
      const holding = (id) => d.members.includes(id) ? id : d.members.find((m) => splitMember(m)[0] === id);
      openPicker({ id: "", name: "", fields: [] }, { key: "member", label: "model", value: "", options, multi: true, picked: (id) => !!holding(id), onPick: (id) => {
        if (!id) return;
        const m = holding(id);
        if (!m) d.members.splice(named(), 0, id); // after those named, before a pattern's
        else if (d.matched.includes(m)) return status(t("In the group by a pattern: switch it off to send it nothing"), "warn");
        else { d.members.splice(d.members.indexOf(m), 1); rematch(); }
        draw(); drawRules();
      } }, addBtn, ev);
    };
    box.append(list, addBtn);
    draw();
    const mw = el("div");
    mw.append(box, el("div", "hint", t("The first answers for what the model can do. In order, they are tried top first.")));
    ed.append(el("label", "", t("Models")), mw);

    // patterns: models found by their ids, now and as providers list new
    // ones (#766); what they match follows the models named above
    function rematch() {
      const named = d.members.filter((x) => !d.matched.includes(x));
      d.matched = matchedBy(d.match, named);
      d.members = [...named, ...d.matched];
      d.rules = d.rules.filter((r) => d.members.includes(r.use));
      d.fast = d.fast.filter((x) => d.members.includes(x));
      d.off = d.off.filter((x) => d.members.includes(x));
    }
    const pbox = el("div", "fallback rt-pats");
    const plist = el("div", "fbl");
    const pin = keys(input("", t("e.g. openrouter/*:free or re:…")));
    const pAdd = el("button", "text", t("Add pattern"));
    const drawPats = () => {
      plist.replaceChildren(...d.match.map((p) => {
        const row = el("div", "fbrow"), n = patternCount(p);
        const c = el("span", "n");
        c.append(el("code", "", p), el("small", "", patternWords(n)));
        if (!n) row.classList.add("none");
        const rm = el("button", "text", t("Remove"));
        rm.onclick = () => { d.match = d.match.filter((x) => x !== p); rematch(); drawPats(); draw(); drawRules(); };
        row.append(c, el("span", "grow"), rm);
        return row;
      }));
    };
    // what was typed, as a pattern the group keeps; "" when it was taken
    // or there was none, an error said where it is seen otherwise
    const addPattern = () => {
      const p = pin.value.trim();
      if (!p) return "";
      if (!isPattern(p)) return status(t("A pattern has a * in it, or starts with re:"), "warn"), null;
      if (!patternRe(p)) return status(t("{pattern} is not a regular expression magpie can read", { pattern: p }), "warn"), null;
      if (!d.match.includes(p)) d.match.push(p);
      pin.value = "";
      rematch(); drawPats(); draw(); drawRules();
      return "";
    };
    pAdd.onclick = () => { addPattern(); };
    pin.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") addPattern(); else if (e.key === "Escape") cancelGroup(); };
    const padd = el("div", "rt-patadd");
    padd.append(pin, pAdd);
    pbox.append(plist, padd);
    drawPats();
    const pw = el("div");
    pw.append(pbox, el("div", "hint", t("Every model a pattern matches is in the group, now and as providers list new ones, after the models above: * is any run of characters in provider/model, re: starts a regular expression.")));
    ed.append(el("label", "", t("Patterns")), pw);

    const rHint = el("div", "hint", t(GROUP_HINT[d.routing] || GROUP_HINT[""]));
    const rw = el("div");
    rw.append(segs(GROUP_ROUTE_OPTS.map(([id, n]) => [id, t(n)]), d.routing, (v) => { d.routing = v; rHint.textContent = t(GROUP_HINT[v] || GROUP_HINT[""]); drawRules(); drawSink(); }), rHint);
    rw.append(el("div", "hint", t(GROUP_NEST)));
    ed.append(el("label", "", t("Routing")), rw);
    // one rate limited with quota left: its place, or the back
    const kHint = el("div", "hint"), kw = el("div", "sink-wrap"), kLab = el("label", "", t("Rate limited"));
    const drawSink = () => {
      kHint.textContent = t(SINK_HINT[d.sink ? "sink" : ""]);
      kLab.hidden = kw.hidden = !sinkable(d.routing);
    };
    const kPick = segs(SINK_OPTS.map(([id, n]) => [id, t(n)]), d.sink ? "sink" : "", (v) => { d.sink = v === "sink"; drawSink(); });
    kPick.classList.add("sink-pick");
    kw.append(kPick, kHint);
    ed.append(kLab, kw);
    drawSink();
    // a member slow to begin its answer: waited for, or the next asked
    const fHint = el("div", "hint"), fw = el("div");
    const drawFirst = () => fHint.textContent = d.firstToken
      ? t("A member that hasn't begun answering after {n} — no text, reasoning or tool call yet — is let go and the next one asked, before any of it reaches the agent. It doesn't rest; the last one left is always waited for. Only for streamed requests.", { n: took(d.firstToken * 1000) })
      : t("Every member is waited for, however long it takes to begin answering.");
    fw.append(segs(FIRST_OPTS.map(([n, l]) => [n, t(l)]), d.firstToken || 0, (v) => { d.firstToken = +v; drawFirst(); }), fHint);
    ed.append(el("label", "", t("Slow to start")), fw);
    drawFirst();
    // the window agents are told: the largest member's, the smallest's, or
    // one the user names (Mikan on Discord)
    const ctxHint = el("div", "hint"), ctxW = el("div", "ctx-wrap");
    const ctxIn = keys(input(d.context > 0 ? ctxShort(d.context).toLowerCase() : "", t("e.g. 200k")));
    ctxIn.classList.add("ctx-in");
    ctxIn.setAttribute("aria-label", t("Context"));
    ctxHint.id = "groupContextHint";
    ctxIn.setAttribute("aria-describedby", ctxHint.id);
    const ctxs = () => d.members.map((id) => groups.models.find((x) => x.id === id)?.context || infoOf(id)?.context || 0).filter((n) => n > 0);
    const ctxMode = () => d.context === -1 ? "smallest" : d.context > 0 ? "custom" : "largest";
    let ctxErr = "";
    const drawCtx = () => {
      const ns = ctxs(), mode = ctxMode();
      ctxIn.hidden = mode !== "custom";
      ctxIn.setAttribute("aria-invalid", String(!!ctxErr));
      const big = ns.length ? Math.max(...ns) : 0, small = ns.length ? Math.min(...ns) : 0;
      ctxHint.textContent = ctxErr || (mode === "custom"
        ? t("Agents are told the group takes {n} tokens; a longer conversation still goes on to a member with room for it.", { n: (d.context || 0).toLocaleString() })
        : mode === "smallest"
          ? (small ? t("Agents are told {n} tokens, its smallest model's, so they compact before any member would turn the conversation away.", { n: small.toLocaleString() }) : t("Agents are told its smallest model's window, so they compact before any member would turn the conversation away."))
          : (big ? t("Agents are told {n} tokens, its largest model's; a conversation too long for one member goes on to one with room for it.", { n: big.toLocaleString() }) : t("Agents are told its largest model's window; a conversation too long for one member goes on to one with room for it.")));
    };
    const CTX_OPTS = [["largest", "Largest model's"], ["smallest", "Smallest model's"], ["custom", "Custom"]];
    ctxW.append(segs(CTX_OPTS.map(([id, n]) => [id, t(n)]), ctxMode(), (v) => {
      ctxErr = "";
      if (v === "largest") d.context = 0;
      else if (v === "smallest") d.context = -1;
      else { const ns = ctxs(); d.context = d.context > 0 ? d.context : (ns.length ? Math.min(...ns) : 200000); ctxIn.value = ctxShort(d.context).toLowerCase(); }
      drawCtx();
      if (v === "custom") ctxIn.focus();
    }), ctxIn, ctxHint);
    ctxIn.oninput = () => {
      const m = ctxIn.value.trim().toLowerCase().replace(/_/g, "").match(/^(\d+(?:\.\d+)?)\s*([km]?)$/);
      const n = m ? Math.round(+m[1] * (m[2] === "k" ? 1e3 : m[2] === "m" ? 1e6 : 1)) : 0;
      ctxErr = n > 0 ? "" : t("{v} is not a length of tokens like 200k or 1m", { v: ctxIn.value.trim() });
      if (n > 0) d.context = n;
      drawCtx();
    };
    ed.append(el("label", "", t("Context")), ctxW);
    drawCtx();
    const aHint = el("div", "hint", t(AFF_HINT[d.affinity] || AFF_HINT[""]));
    const aw = el("div");
    aw.append(segs(AFF_OPTS.map(([id, n]) => [id, t(n)]), d.affinity, (v) => { d.affinity = v; aHint.textContent = t(AFF_HINT[v] || AFF_HINT[""]); }), aHint);
    ed.append(el("label", "", t("Stays")), aw);

    // rules: which member a turn goes to first, by what the request shows
    const rbox = el("div", "fallback rt-rules");
    const rlist = el("div", "fbl");
    const rAdd = el("button", "rt-gadd");
    rAdd.append(svg(PLUS, 11, 1.8), el("span", "", t("Add a rule")));
    const rHint2 = el("div", "hint");
    const drawRules = () => {
      drawCtx(); // the members' windows changed with them
      rlist.replaceChildren();
      rAdd.hidden = d.members.length < 2;
      rHint2.textContent = t(d.members.length < 2 ? "With two models or more, a rule can send some turns to one of them first."
        : d.routing === "manual" ? "The rules wait while you pick the model by hand."
        : "Checked top first when you send a message: the first that matches sends that turn to its model first; the rest stay behind it if it fails. A turn under way is never moved.");
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
        // compacting: the agent summarizing its conversation (/compact),
        // which a cheaper, faster model can do
        const cp = el("button", "rt-cond" + (r.compact ? " on" : ""), t("compacting"));
        cp.title = t("The agent summarizes the conversation to go on in less room (Claude Code's /compact, Codex, OpenCode…): a cheaper, faster model can do it");
        cp.onclick = () => { r.compact = !r.compact; cp.classList.toggle("on", r.compact); warn(); };
        // hours of the day, on this computer's clock (a vendor's peak
        // hours, say): typed as 09:00 and 18:00, past midnight when the
        // second comes first; the days are picked like the agents
        const tm = el("span", "rt-cond tm");
        const tw = () => r.time || (r.time = { from: "", to: "", days: [] });
        const tFrom = input(r.time?.from || "", "09:00"), tTo = input(r.time?.to || "", "18:00");
        tm.title = t("The turn begins within these hours, on this computer's clock — a provider's peak-price hours sent to another, say");
        tm.onclick = (e) => { if (e.target === tm || e.target.tagName === "SPAN") (tFrom.value ? tTo : tFrom).focus({ preventScroll: true }); };
        const dayBtn = el("button", "rt-cond");
        const drawTime = () => {
          const w = r.time, from = clockOf(w?.from), to = clockOf(w?.to);
          const typed = !!(w && (w.from || w.to));
          tm.classList.toggle("on", !!(from && to));
          // a time typed that isn't one, once its field is left
          tm.classList.toggle("bad", [[tFrom, w?.from], [tTo, w?.to]].some(([b, v]) => v && !clockOf(v) && document.activeElement !== b));
          dayBtn.hidden = !typed && !w?.days?.length;
          dayBtn.textContent = daysText(w?.days) || t("every day");
          dayBtn.classList.toggle("on", !!daysText(w?.days));
        };
        for (const [box, key] of [[tFrom, "from"], [tTo, "to"]]) {
          box.className = "clock";
          box.maxLength = 5;
          box.inputMode = "numeric";
          box.oninput = () => { tw()[key] = box.value.trim(); drawTime(); warn(); };
          box.onblur = () => { const v = clockOf(box.value); if (v) { box.value = v; tw()[key] = v; } drawTime(); warn(); };
          box.onkeydown = (e) => e.stopPropagation();
        }
        tm.append(el("span", "", t("from")), tFrom, el("span", "", t("to")), tTo);
        const presets = [["", "every day", []], ["weekdays", "weekdays", WEEK.slice(0, 5)], ["weekends", "weekends", WEEK.slice(5)]];
        dayBtn.onclick = (ev) => pickFrom("days", dayBtn, ev, [
          ...presets.map(([v, label, ds]) => ({ value: "=" + v, label: t(label), note: ds.length ? daysText(ds) : t("not a condition") })),
          ...WEEK.map((d) => ({ value: d, label: t(DAY_NAMES[d]), note: r.time?.days?.includes(d) ? "✓" : "" })),
        ], (v) => {
          const w = tw();
          if (v.startsWith("=")) w.days = [...presets.find(([id]) => "=" + id === v)[2]];
          else w.days = w.days.includes(v) ? w.days.filter((x) => x !== v) : WEEK.filter((x) => x === v || w.days.includes(x));
          if (w.days.length === 7) w.days = [];
          drawTime(); warn();
        });
        drawTime();
        when.append(tk, im, ef, ag, it, cp, tm, dayBtn);
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
          // a summary longer than it takes goes by the next rule, or the group
          if (r.compact && m?.context && d.members.some((id) => (infoOf(id)?.context || 0) > m.context)) bits.push(t("longer conversations skip it: it takes {n} tokens", { n: m.context.toLocaleString() }));
          const from = clockOf(r.time?.from), to = clockOf(r.time?.to);
          if (from && to && to < from) bits.push(t("runs past midnight, into the next day"));
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
    rAdd.onclick = () => { d.rules.push({ use: d.members[d.members.length - 1], tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, time: null }); drawRules(); };
    // the classifier, once a rule has an intent or the effort is picked
    // per turn: the model asked which intent a turn's message is and how
    // hard it is. Jev (a decision provider's model) answers both in one
    // call; any other model is asked each in words.
    const cls = el("div", "rt-classifier");
    const clabel = el("label", "");
    // its cell in the editor's grid: hidden with the label, or the rows
    // after it would each slip one cell (Levels' label at the right, its
    // choices under the labels)
    const cw = el("div");
    cw.append(cls);
    const deciders = groups.deciders || [];
    const isJev = (id) => deciders.some((x) => x.id === id);
    const drawClassifier = () => {
      const auto = d.effort === "auto";
      const on = auto || d.rules.some((r) => r.intent?.trim());
      cls.hidden = cw.hidden = clabel.hidden = !on;
      if (!on) return;
      const intents = d.rules.some((r) => r.intent?.trim());
      clabel.textContent = t(auto && !intents ? "Decided by" : "Intent told by");
      // a model, or another group: its models are asked in turn, failing
      // over as any request to it does (never this group: it would ask
      // itself)
      const m = [...deciders, ...groups.models].find((x) => x.id === d.classifier), sg = subOf(d.classifier);
      const cb = el("button", "rt-cond" + (d.classifier ? " on" : ""));
      if (sg) cb.append(stackIcon(groupIcons(sg)), el("span", "", `${sg.name} · ${t("routing group")}`));
      else if (d.classifier) cb.append(icon(m?.icon || "generic"), el("span", "", m ? `${m.name || m.id} · ${m.providerName}` : d.classifier));
      else cb.append(el("span", "", t("choose a model")));
      const opt = (x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.providerName, ref: x.id, context: x.context });
      const subs = groups.groups.filter((x) => !x.hidden && x.id !== g?.id)
        .map((x) => ({ value: "group/" + x.id, label: x.name, note: "group/" + x.id, icons: groupIcons(x), group: ROUTING_GROUPS, ref: "group/" + x.id }));
      cb.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "classifier", label: "model", value: d.classifier, options: [...deciders.map(opt), ...subs, ...groups.models.map(opt)],
        onPick: (id) => { if (id) d.classifier = id; drawClassifier(); } }, cb, ev);
      cls.replaceChildren(cb,
        ...(sg ? [el("div", "hint", t("A routing group classifies as any request to it goes: if its first model fails, the next is asked."))] : []),
        el("div", "hint", t(isJev(d.classifier) && !intents
          ? "Jev is asked once as each turn begins, with the message and what it said of the turn before. Its calls show in the usage as magpie’s own."
          : isJev(d.classifier)
          ? "As a turn begins, Jev is asked once which of the intents the message is, and how hard the turn is when it picks the effort. An intent it isn't sure of matches no rule. Its calls show in the usage as magpie’s own."
          : auto && !intents
          ? "As a turn begins, this model is asked how hard the turn is — once; a small, fast one without reasoning is best. If it fails or can't say, the turn reasons as the agent asked. Its calls show in the usage as magpie’s own."
          : auto
          ? "As a turn begins, this model is asked which of the intents the message is and how hard the turn is, each once; a small, fast one without reasoning is best. If it can't say, no intent matches and the turn reasons as the agent asked. Its calls show in the usage as magpie’s own."
          : "As a turn begins, this model is asked which of the intents the message is — once; a small, fast one without reasoning is best. If it fails or can't say, no intent matches. Its calls show in the usage as magpie’s own.")));
    };
    rbox.append(rlist, rAdd);
    const rw2 = el("div");
    rw2.append(rbox, rHint2);
    ed.append(el("label", "", t("Rules")), rw2);
    // the effort: the agent's, or the classifier's pick for each turn
    const eHint = el("div", "hint");
    const ew = el("div");
    const drawEffort = () => {
      eHint.textContent = t(d.effort === "auto"
        ? "As a turn begins, the classifier rates how hard it is and the turn's requests ask their model for low, medium, high or xhigh reasoning — the level each model has nearest. Only where the agent asked for reasoning: a request without any (a session title) stays without."
        : "Each request reasons as much as the agent asked.");
    };
    ew.append(segs([["", t("Agent's")], ["auto", t("Picked per turn")]], d.effort, (v) => {
      d.effort = v;
      if (v === "auto" && !d.classifier && deciders.length) d.classifier = deciders[0].id;
      drawEffort(); drawClassifier();
    }), eHint);
    drawEffort();
    ed.append(el("label", "", t("Effort")), ew);
    ed.append(clabel, cw);
    drawRules();
    // the levels agents are offered: those every model has, or ones the
    // group names (#295) — a model without the one asked is sent its
    // nearest, so one with few needn't take the rest from the others
    if (!d.levels) d.levels = [];
    let own = d.levels.length > 0;
    const shared = g && d.members.join() === g.members.join() ? g.shared || [] : null; // as saved
    const lHint = el("div", "hint");
    const chips = el("div", "rt-levels");
    const drawLevels = () => {
      chips.hidden = !own;
      lHint.textContent = own
        ? t("Agents are offered these. A model without the level asked is sent the one it has nearest.")
        : shared?.length ? t("Agents are offered the levels every model has: {levels}.", { levels: shared.join(", ") })
        : t("Agents are offered the levels every model has.");
    };
    for (const v of LEVELS) {
      const c = el("button", "rt-cond" + (d.levels.includes(v) ? " on" : ""), v);
      c.onclick = () => {
        d.levels = d.levels.includes(v) ? d.levels.filter((x) => x !== v) : LEVELS.filter((x) => x === v || d.levels.includes(x));
        c.classList.toggle("on", d.levels.includes(v));
      };
      chips.append(c);
    }
    const lw = el("div");
    lw.append(segs([["", t("Its models' shared")], ["own", t("Named")]], own ? "own" : "", (v) => {
      own = v === "own";
      if (own && !d.levels.length) {
        d.levels = LEVELS.filter((x) => (shared?.length ? shared : ["low", "medium", "high"]).includes(x));
        for (const c of chips.children) c.classList.toggle("on", d.levels.includes(c.textContent));
      }
      drawLevels();
    }), chips, lHint);
    drawLevels();
    ed.append(el("label", "", t("Levels")), lw);

    const bar = el("div", "bar");
    if (g) {
      const del = el("button", "text danger", t("Remove"));
      del.onclick = () => groupAction("delete", { id: g.id }, t("{name} removed", { name: g.name }));
      bar.append(del);
    }
    bar.append(el("span", "grow"));
    const cancel = el("button", "text", t("Cancel"));
    cancel.onclick = cancelGroup;
    const saveBtn = el("button", "text primary", t(g ? "Save" : "Add"));
    const save = () => {
      if (addPattern() === null) return pin.focus({ preventScroll: true }); // typed, not added: it is meant
      if (!d.members.length && !d.match.length) { addBtn.focus({ preventScroll: true }); return status(t("A group needs a model in it"), "warn"); }
      d.rules.forEach((r) => { r.intent = (r.intent || "").trim(); });
      // hours: both times, or none and no days; days alone are all day
      const badTime = d.rules.findIndex((r) => {
        const w = r.time;
        if (!w || !w.from && !w.to && !w.days?.length) return r.time = null, false;
        if (!w.from && !w.to) { w.from = w.to = "00:00"; return false; }
        const from = clockOf(w.from), to = clockOf(w.to);
        if (!from || !to) return true;
        w.from = from; w.to = to;
        return from === to && !w.days?.length;
      });
      if (badTime >= 0) {
        const w = d.rules[badTime].time;
        return status(clockOf(w.from) && clockOf(w.from) === clockOf(w.to)
          ? t("Rule {n}: its hours are the whole day — pick days, or other hours", { n: badTime + 1 })
          : t("Rule {n}: the hours are two times like 09:00 and 18:00", { n: badTime + 1 }), "warn");
      }
      const bare = d.rules.findIndex((r) => !r.tokens && !r.images && !r.effort && !r.agents.length && !r.intent && !r.compact && !r.time);
      if (bare >= 0) return status(t("Rule {n} needs a condition", { n: bare + 1 }), "warn");
      if (d.rules.some((r) => r.intent) && !d.classifier) return status(t("Choose the model that tells which intent a message is"), "warn");
      if (d.effort === "auto" && !d.classifier) return status(t("Choose the model that rates how hard a turn is"), "warn");
      if (own && !d.levels.length) return status(t("Pick a level to offer, or leave them to its models"), "warn");
      saveBtn.classList.add("busy");
      // refused, Add can be pressed again (busy, it takes no clicks)
      groupAction("save", { id: idOf(), from: g?.id, name: d.name.trim() || idOf(), members: namedOf(d), match: d.match, routing: d.routing, pick: d.pick || "", affinity: d.affinity, sink: !!d.sink && sinkable(d.routing), firstToken: d.firstToken || 0, rules: d.rules, effort: d.effort, classifier: d.rules.some((r) => r.intent) || d.effort === "auto" ? d.classifier : "", context: d.context || 0, levels: own ? d.levels : [], family: g?.family || "", fast: d.fast.filter((x) => d.members.includes(x)), off: d.off.filter((x) => d.members.includes(x)) }, t(g ? "{name} saved" : "{name} added", { name: d.name.trim() || idOf() }))
        .then(() => saveBtn.classList.remove("busy"));
    };
    // what goes wrong is said where it is seen, never a click that does nothing
    saveBtn.onclick = () => {
      try { save(); } catch (e) { saveBtn.classList.remove("busy"); status(t("Couldn't save the group: {error}", { error: e.message }), "err"); }
    };
    bar.append(cancel, saveBtn);
    ed.append(bar);
    if (!g) setTimeout(() => name.focus({ preventScroll: true }), 0); // WebKit would scroll the page to put it mid-view
    if (gEdit.was == null) gEdit.was = JSON.stringify(d);
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
      // by weight is a key's share (#841), set beside each key in the provider's editor
      const opts = p.kind === "account" ? ROUTE_OPTS : [...ROUTE_OPTS, ["weight", "By weight"]];
      ctl.append(
        lab(t("Routing"), segs(opts.map(([id, n]) => [id, t(n)]), p.routing || "", (v) => set("route", { id: p.provider, routing: v }, t("{name}: {routing}", { name: p.name, routing: t(opts.find(([id]) => id === v)[1]) })))),
        lab(t("Stays"), segs(AFF_OPTS.map(([id, n]) => [id, t(n)]), p.affinity || "", (v) => set("affinity", { id: p.provider, affinity: v }, t("{name}: {routing}", { name: p.name, routing: t(AFF_OPTS.find(([id]) => id === v)[1]) })))));
      if (sinkable(p.routing || "")) {
        const k = lab(t("Rate limited"), segs(SINK_OPTS.map(([id, n]) => [id, t(n)]), p.sink ? "sink" : "", (v) => set("sink", { id: p.provider, sink: v === "sink" }, t("{name}: {routing}", { name: p.name, routing: t(SINK_OPTS.find(([id]) => id === v)[1]) }))));
        k.classList.add("sink-pick");
        k.title = t(SINK_HINT[p.sink ? "sink" : ""]);
        ctl.append(k);
      }
      row.append(nm, ctl);
      row.title = p.who.join(", ");
      return row;
    }));
  }
  // loaded when the view is shown, and again when the window comes back
  new MutationObserver(() => { if (!$("#view-routing").hidden) loadGroups(); }).observe($("#view-routing"), { attributes: true, attributeFilter: ["hidden"] });
  window.addEventListener("focus", () => { if (shown()) loadGroups(); });
  // newGroupWith: a new group's editor, opened with the model in it — a
  // model of a provider kept for routing groups that no group has, which
  // agents can reach no other way. The picker and the provider's editor
  // ask it (and the tray panel, by ?newgroup= on the window it opens).
  window.newGroupWith = async (id, name, ev) => {
    // app.js's show, the page's: this one's own show is the stage's caption
    if (document.body.classList.contains("window") && $("#view-routing").hidden && !(await window.show("routing"))) return;
    if (!groups) await loadGroups();
    if (!groups) return;
    if (groupDirty() && !(await confirmDiscard())) return;
    gEdit = { id: "", draft: { name: name || modelOf(id)?.name || id.split("/").pop(), members: [id], fast: [], routing: "", affinity: "", rules: [] } };
    renderGroups();
    const ed = gList.querySelector(".rt-gedit");
    if (ed && window.scrollOnPurpose?.(ev)) ed.scrollIntoView({ block: "center", behavior: "smooth" });
    ed?.querySelector("input")?.focus({ preventScroll: true });
  };
  // newGroup: an empty new group's editor. The groups sit below the
  // requests, out of sight on a first look, so the page's head has a New
  // group too (mintonight, #944), which brings the editor into view.
  async function newGroup(ev) {
    if (groupDirty() && !(await confirmDiscard())) return;
    gSel = null;
    gEdit = { id: "", draft: { name: "", members: [], match: [], matched: [], fast: [], off: [], routing: "", affinity: "", rules: [] } };
    renderGroups();
    if (!ev) return;
    const ed = gList.querySelector(".rt-gedit");
    if (ed && window.scrollOnPurpose?.(ev)) ed.scrollIntoView({ block: "center", behavior: "smooth" });
    ed?.querySelector("input")?.focus({ preventScroll: true });
  }
  const headNew = $("#rtNewGroup");
  if (headNew) headNew.onclick = async (ev) => {
    if (!groups) await loadGroups();
    if (groups) newGroup(ev);
  };
  const askedGroup = document.body.classList.contains("window") && params.get("newgroup");
  if (askedGroup) loadGroups().then(() => window.newGroupWith(askedGroup, ""));
  else loadGroups();

  // ---------- where the reader is, as requests come ----------
  // Every request redraws what is above the routing groups: the stage gains
  // or loses a row, the story a line, the lists theirs. The view kept its
  // scrollTop, so all of it pushed the groups down or pulled them up under
  // the reader, a group being edited too (Jerell.OvO on Discord). The view
  // keeps a part of itself where it is on the screen instead (keepInView in
  // app.js): the groups, while they are in the upper half of the view or a
  // field in them has the focus, else the lists, while they are; with
  // neither (the stage in sight at the top), the view stays as it is.
  const rv = $("#view-routing");
  keepInView(rv, () => {
    const mid = rv.getBoundingClientRect().top + rv.clientHeight / 2;
    if (gsec.contains(document.activeElement) && gsec.offsetParent) return gsec;
    return [gsec, hist].find((p) => p.offsetParent && p.getBoundingClientRect().top <= mid) || null;
  });

  // ---------- the tray panel's Routing tab ----------
  // The gateway's latest requests, as they come, from the same trace the
  // page above plays: who sent each, the model asked for, the provider and
  // account it went to, the model that answered (marked when the reply
  // names another), how it ended and when; today's calls and tokens over
  // them, from the Usage page's count, and between the two a small stage:
  // the agents that asked lately, magpie, and where their requests went,
  // each new request a dot flying there and back. A click opens the
  // window's Routing page on that request; nothing here moves the panel's
  // scroll.
  const pBox = document.body.classList.contains("panel") ? $("#panelRouting") : null;
  if (pBox) pBox.hidden = false;
  const P_ROWS = 6;
  let today = null, todayAt = 0;
  const pShown = () => pBox && document.body.dataset.ptab === "routing";
  async function loadToday() {
    todayAt = performance.now();
    try { today = await api("usage?period=today"); } catch { return; }
    renderPanel();
  }
  window.panelRoutingShown = () => { if (pShown() && performance.now() - todayAt > 5e3) loadToday(); pDraw(); };

  // ---- the stage: agents → magpie → providers ----
  // Three a side at most, each kept where it is while it stays: one that
  // comes takes the place of the one heard of longest ago. A request plays
  // as a dot in its agent's colour: to magpie, on to the account it was
  // routed to, waiting there while that answers; a try that failed turns
  // red and comes back for the next, and the answer flies home green (red
  // when nobody could answer). Still, with reduced motion or the tab hidden.
  const P_SIDE = 3;
  const pStage = el("div", "pr-stage");
  const pWires = document.createElementNS(NS, "svg"), pSky = document.createElementNS(NS, "svg");
  pWires.setAttribute("class", "ps-wires");
  pSky.setAttribute("class", "ps-sky");
  const pFrom = el("div", "ps-col ps-from"), pTo = el("div", "ps-col ps-to"), pHub = el("div", "ps-hub");
  pHub.innerHTML = '<svg viewBox="0 0 44 44" aria-hidden="true"><use href="#bird"/></svg>';
  pStage.append(pWires, pFrom, pHub, pTo, pSky);
  pStage.setAttribute("aria-hidden", "true");
  let pAg = [], pDst = [];          // the keys on the stage, in their places
  const pNodes = new Map();         // "a:<agent>" / "d:<seat>" → { node, wire }
  const pPlays = new Set();         // the requests flying
  let pTrips = [], pWait = [], pRaf = 0;
  // pSeat is where a try went: a provider, and the account or key there
  function pSeat(r, tr) {
    const w = tr && tried(r, tr);
    if (!w) return { key: r.provider || "?", name: r.provider || "?", sub: "" };
    const sub = w.kind === "provider" ? "" : w.who || "";
    return { key: `${w.provider}|${sub}`, name: w.name || w.provider, sub };
  }
  // pPlace keeps those on the stage that are still wanted where they are,
  // the newcomers taking the free places
  function pPlace(had, want) {
    const out = had.map((k) => want.includes(k) ? k : null);
    for (const k of want) if (!out.includes(k)) { const i = out.indexOf(null); if (i >= 0) out[i] = k; else out.push(k); }
    return out.filter(Boolean).slice(0, P_SIDE);
  }
  function pDraw() {
    if (!pBox || !pStage.isConnected) return;
    const recent = [...routes.values()].sort((a, b) => b.id - a.id);
    const flying = recent.filter((r) => pPlays.has(r.id));
    const ags = [], dst = new Map(); // seat key → { name, sub, how }
    for (const r of flying) {
      if (!ags.includes(r.agent)) ags.push(r.agent);
      for (const tr of r.tries) { const s = pSeat(r, tr); if (!dst.has(s.key)) dst.set(s.key, s); }
    }
    for (const r of recent) {
      if (ags.length < P_SIDE && !ags.includes(r.agent)) ags.push(r.agent);
      const tr = r.tries[r.tries.length - 1];
      if (tr) { const s = pSeat(r, tr); if (!dst.has(s.key) && dst.size < P_SIDE) dst.set(s.key, s); }
    }
    // each account on the stage says how its latest try there went
    for (const r of recent) {
      for (const x of r.tries.slice().reverse()) {
        const d = dst.get(pSeat(r, x).key);
        if (d && !d.how) d.how = !x.done ? "wait" : tryOk(x) ? "ok" : "bad";
      }
    }
    pAg = pPlace(pAg, ags.slice(0, P_SIDE));
    pDst = pPlace(pDst, [...dst.keys()].slice(0, P_SIDE));
    const keep = new Set();
    pFrom.replaceChildren(...pAg.map((id) => {
      const n = pNode("a:" + id);
      keep.add("a:" + id);
      const ag = agentOf(id);
      n.node.className = "ps-node ps-ag";
      n.node.style.setProperty("--agent", hueOf(id));
      n.wire.style.setProperty("--agent", hueOf(id));
      n.node.replaceChildren(icon(ag?.icon || "generic"), el("span", "ps-name", agentName(id)));
      n.node.title = agentName(id);
      return n.node;
    }));
    pTo.replaceChildren(...pDst.map((k) => {
      const n = pNode("d:" + k), d = dst.get(k);
      keep.add("d:" + k);
      n.node.className = "ps-node ps-dst " + (d.how || "ok");
      const name = el("span", "ps-name", d.name);
      if (d.sub) name.append(el("small", "", d.sub));
      n.node.replaceChildren(el("i"), name);
      n.node.title = d.sub ? `${d.name} · ${d.sub}` : d.name;
      return n.node;
    }));
    for (const [k, n] of pNodes) if (!keep.has(k)) { n.wire.remove(); pNodes.delete(k); }
    pHub.classList.toggle("busy", pPlays.size > 0);
    pLayout();
  }
  function pNode(k) {
    let n = pNodes.get(k);
    if (!n) {
      const wire = document.createElementNS(NS, "path");
      wire.setAttribute("class", k[0] === "a" ? "ps-wire" : "ps-wire ps-out");
      pWires.appendChild(wire);
      n = { node: el("div"), wire, lit: 0 };
      pNodes.set(k, n);
    }
    return n;
  }
  // the wires from each agent into magpie, and out of it to each account
  function pLayout() {
    const r = pStage.getBoundingClientRect();
    if (!r.width) return;
    for (const svg of [pWires, pSky]) svg.setAttribute("viewBox", `0 0 ${r.width} ${r.height}`);
    const h = pHub.getBoundingClientRect(), hy = (h.top + h.bottom) / 2 - r.top;
    for (const [k, n] of pNodes) {
      const b = n.node.getBoundingClientRect();
      if (!b.width) continue;
      const y = (b.top + b.bottom) / 2 - r.top;
      const [x1, y1, x2, y2] = k[0] === "a" ? [b.right - r.left + 3, y, h.left - r.left - 2, hy] : [h.right - r.left + 2, hy, b.left - r.left - 3, y];
      const mx = (x1 + x2) / 2;
      n.wire.setAttribute("d", `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`);
    }
  }
  new ResizeObserver(pLayout).observe(pStage);
  // pFly carries a dot along a wire (back: from its end), lighting the wire
  function pFly(dot, n, back, ms) {
    if (!n) return Promise.resolve();
    n.lit++;
    if (n.wire.classList.contains("ps-out")) n.wire.style.setProperty("--agent", dot.style.getPropertyValue("--agent"));
    n.wire.classList.add("on");
    return new Promise((res) => {
      pTrips.push({ dot, n, back, t0: performance.now(), ms, res });
      if (!pRaf) pRaf = requestAnimationFrame(pTick);
    }).finally(() => { if (!--n.lit) n.wire.classList.remove("on"); });
  }
  function pTick(ts) {
    pRaf = 0;
    const going = [];
    for (const tr of pTrips) {
      const k = pShown() && !still() ? Math.min(1, Math.max(0, (ts - tr.t0) / tr.ms)) : 1;
      const e = k < .5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2;
      const L = tr.n.wire.getTotalLength?.() || 0;
      if (L) {
        const pt = tr.n.wire.getPointAtLength((tr.back ? 1 - e : e) * L);
        tr.dot.setAttribute("cx", pt.x);
        tr.dot.setAttribute("cy", pt.y);
        tr.dot.removeAttribute("visibility");
      }
      if (k >= 1) tr.res(); else going.push(tr);
    }
    pTrips = going;
    if (pTrips.length) pRaf = requestAnimationFrame(pTick);
  }
  // pUntil waits for the trace to say so (checked at each of its updates)
  const pUntil = (f) => f() ? Promise.resolve() : new Promise((res) => pWait.push({ f, res, by: performance.now() + 180e3 }));
  function pWake() {
    const w = pWait;
    pWait = [];
    for (const x of w) if (x.f() || performance.now() > x.by) x.res(); else pWait.push(x);
  }
  const pRest = (ms) => new Promise((res) => setTimeout(res, pShown() && !still() ? ms : 0));
  async function pPlay(id) {
    if (!pBox || !pShown() || still() || pPlays.has(id) || pPlays.size >= 4) return;
    let r = routes.get(id);
    if (!r) return;
    pPlays.add(id);
    pDraw();
    const dot = document.createElementNS(NS, "circle");
    dot.setAttribute("r", 3.5);
    dot.setAttribute("class", "ps-dot");
    dot.setAttribute("visibility", "hidden");
    dot.style.setProperty("--agent", hueOf(r.agent));
    pSky.appendChild(dot);
    const home = () => pNodes.get("a:" + r.agent);
    try {
      await pFly(dot, home(), false, 420);
      for (let i = 0; ; i++) {
        await pUntil(() => { r = routes.get(id) || r; return r.tries.length > i || r.done; });
        const tr = r.tries[i];
        if (!tr) break;
        const k = "d:" + pSeat(r, tr).key;
        pDraw();
        const n = pNodes.get(k);
        await pFly(dot, n, false, 460);
        // it waits at the account while that answers
        if (n) n.lit++;
        dot.classList.add("wait");
        await pUntil(() => { r = routes.get(id) || r; return r.tries[i]?.done || r.done; });
        dot.classList.remove("wait");
        if (n && !--n.lit) n.wire.classList.remove("on");
        const t2 = r.tries[i];
        if (t2 && !tryOk(t2)) {
          dot.classList.add("bad");
          await pRest(260);
          await pFly(dot, n, true, 360);
          await pUntil(() => { r = routes.get(id) || r; return r.tries.length > i + 1 || r.done; });
          if (r.tries.length > i + 1) { dot.classList.remove("bad"); continue; }
          await pFly(dot, home(), true, 420); // nobody left: the error goes home
          break;
        }
        dot.classList.add("ok");
        await pFly(dot, n, true, 420);
        await pFly(dot, home(), true, 420);
        break;
      }
    } finally {
      dot.classList.add("gone");
      setTimeout(() => dot.remove(), 300);
      pPlays.delete(id);
      pDraw();
    }
  }
  // renderPanel draws the tab; counted is a request just done, for today's
  // totals to be read again (now and then)
  function renderPanel(counted) {
    if (!pBox) return;
    if (counted && (!todayAt || performance.now() - todayAt > 5e3)) loadToday();
    const v = $("#view-agents"), keep = v.scrollTop;
    const head = el("div", "pr-head");
    const sum = el("span", "pr-today");
    if (today) {
      sum.append(el("span", "", t("today")), " ",
        el("b", "", String(today.calls || 0)), " ", t(today.calls === 1 ? "call" : "calls"), " · ",
        el("b", "", fmtN(tokensOf(today) || 0)), " ", t("tokens"));
    } else sum.append(el("span", "skeleton pr-sk"));
    const open = el("button", "text", t("Open Routing"));
    open.type = "button";
    open.onclick = (e) => { api("window/main?view=routing", {}); e.currentTarget.blur(); };
    head.append(el("span", "rt-livedot"), sum, el("span", "grow"), open);
    const out = [head];
    const rs = [...routes.values()].sort((a, b) => b.id - a.id).slice(0, P_ROWS);
    if (!offMsg && loaded && rs.length) out.push(pStage);
    if (offMsg) out.push(el("p", "pr-none", t(offMsg)));
    else if (!loaded) for (let i = 0; i < 3; i++) out.push(el("span", "skeleton pr-sk-row"));
    else if (!rs.length) {
      const p = el("div", "pr-none");
      p.append(el("b", "", t("No request yet")), t("Every request an agent sends to magpie shows up here, routed for real."));
      out.push(p);
    } else {
      const list = el("div", "pr-list");
      for (const r of rs) list.append(panelRow(r));
      out.push(list);
    }
    pBox.replaceChildren(...out);
    if (v.scrollTop !== keep) v.scrollTop = keep;
    pDraw();
    pWake();
    fit();
  }
  function panelRow(r) {
    const [, how, tr] = outcome(r);
    const w = tr && tried(r, tr);
    const b = el("button", "pr-req " + how);
    b.type = "button";
    const ag = agentOf(r.agent);
    const asked = el("span", "pr-a");
    asked.append(icon(ag?.icon || "generic"), el("span", "pr-who", agentName(r.agent)), el("code", "m", r.model));
    if (r.kind) asked.append(kindTag(r));
    const when = el("span", "at", new Date(r.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }));
    // where it went: the provider, the account or key, and the model that
    // answered — marked when the reply names another than the one sent
    const to = el("span", "pr-to");
    to.append(el("i", "", "→"));
    const place = w ? where(w) : r.provider;
    if (!r.done) to.append(el("span", "pr-where", w ? t("{who} is answering…", { who: place }) : t("routing…")));
    else if (how === "bad") {
      const last = r.tries[r.tries.length - 1];
      to.append(el("span", "pr-where", last ? `${r.status} · ${failWord(last.fail)}` : `${r.status || ""} ${r.error || ""}`.trim()));
    } else {
      to.append(el("span", "pr-where", place));
      const model = tr?.model || w?.model;
      if (model) to.append(el("span", "pr-m", model));
      if (tr?.swapped && tr.done) to.append(swapTag(tr, true));
      else if (tr?.routed && tr.done) to.append(routedTag(tr));
      if (tr?.upstream && tr.done) to.append(upstreamTag(tr));
    }
    const meta = [];
    if (r.tries.length > 1) meta.push(t("{n} tries", { n: r.tries.length }));
    if (r.tokens) meta.push(t("{n} tokens", { n: tokens(r.tokens) }));
    if (r.done && r.ms) meta.push(took(r.ms));
    b.append(asked, when, to, el("span", "meta", meta.join(" · ")));
    b.title = reqTitle(r, how, tr);
    b.onclick = (e) => { api("window/main?view=routing&req=" + r.id, {}); e.currentTarget.blur(); };
    return b;
  }

  new ResizeObserver(() => layout()).observe(stage);
  // the list is as tall as leaves the stage in sight above it: the reader
  // scrolls the list, not the page, and a request picked plays in view.
  // Its top stays where it is as it is sized, so a row just clicked does too
  function fitReqs() {
    const v = $("#view-routing");
    if (v.hidden || !reqs.offsetParent) return;
    const above = reqs.getBoundingClientRect().top - box.getBoundingClientRect().top;
    const room = v.clientHeight - above - 28;
    const h = Math.round(Math.max(216, Math.min(420, room))) + "px";
    if (reqs.style.maxHeight !== h) reqs.style.maxHeight = h;
  }
  // the accounts beside the requests end where they do, so a new height
  // resizes what the other observers have just been told of: size the
  // list in the next frame, not inside this round of them
  let fitting = 0;
  const fitSoon = () => { if (!fitting) fitting = requestAnimationFrame(() => { fitting = 0; fitReqs(); }); };
  new ResizeObserver(fitSoon).observe($("#view-routing"));
  new ResizeObserver(fitSoon).observe(box);
  words();
  start();
  poll();
})();
