// The Usage page's code (client.js has its markup and hooks): run after
// app.js, whose helpers and state it shares, and before start.js shows the
// first page.
const PERIODS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"], ["all", "All"]];

// asked: the reader opened the page, so a Claude account's usage is read
// at once, by running Claude Code's own /usage, rather than when its last
// reading is due (every 5 to 15 minutes, at random, once Claude Code was used).
// The Overview's reads share one count: only the newest draws, and only for the
// period and tab the page is on, whether a pick or the timer asked for it.
let usageRead = 0;
async function loadUsage(asked) {
  renderUsageTab();
  renderUsageEvery(); // the refresh's tooltip says what it reads on this tab
  if (asked) quotasOpened();
  if (usageTab === "sessions") return loadSessions();
  if (usageTab === "requests") return loadLedger();
  if (usageTab === "context") return window.loadContext?.();
  renderUsageLoading();
  if (!asked) loadQuotas();
  const p = period, read = ++usageRead;
  const next = await api("usage?period=" + encodeURIComponent(p));
  if (read !== usageRead || view !== "usage" || usageTab !== "usage" || period !== p) return;
  usage = next;
  renderUsage();
}

// the period picker, in the page's head, for the Overview and Requests
function renderPeriod(loading) {
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.disabled = !!loading;
    b.onclick = () => { for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "period"); pickPeriod(id); };
    seg.append(b);
  }
  // days of the reader's own (#1492): the last opt names them once picked,
  // and opens the range menu to pick them, or others
  const range = periodDays(period);
  const b = el("button", "opt range-opt" + (range ? " on" : ""));
  b.type = "button";
  b.disabled = !!loading;
  b.setAttribute("aria-haspopup", "dialog");
  b.setAttribute("aria-expanded", "false");
  b.setAttribute("aria-label", range ? t("Days picked: {days}", { days: rangeName(range) }) : t("Pick days"));
  b.append(svg("M3 4.5h10v8.5H3zM3 7h10M5.5 3v3M10.5 3v3", 12, 1.4), el("span", "", range ? rangeName(range) : t("Custom")));
  if (range) b.title = t("Days picked: {days}", { days: rangeName(range) });
  b.onclick = () => {
    if (b.classList.contains("open")) return closeProtoMenu();
    openRangeMenu(b, range, (from, to) => {
      for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
      b.lastChild.textContent = rangeName({ from, to });
      b.title = t("Days picked: {days}", { days: rangeName({ from, to }) });
      b.setAttribute("aria-label", b.title);
      slide(seg, "period");
      pickPeriod(from + ".." + to);
    });
  };
  seg.append(b);
  slide(seg, "period");
}

function pickPeriod(id) {
  period = id; ledDay = ""; ledOffset = 0;
  loadUsage().catch((e) => status(e.message, "err"));
}

const dayKeyOf = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
// rangeName says picked days short: "Oct 1 – Oct 7", the year only when it
// isn't this one, one day alone as itself
function rangeName({ from, to }) {
  const year = new Date().getFullYear();
  const name = (k) => dayOfKey(k).toLocaleDateString(intlLang(), { month: "short", day: "numeric", ...(dayOfKey(k).getFullYear() !== year ? { year: "numeric" } : {}) });
  return from === to ? name(from) : name(from) + " – " + name(to);
}
// noCallsIn is what an empty page says of the period it is on
function noCallsIn(p) {
  if (periodDays(p)) return "No calls on the days picked.";
  return { today: "No calls today.", "7d": "No calls in the last 7 days.", "30d": "No calls in the last 30 days.", all: "No calls yet." }[p] || "No calls yet.";
}

// openRangeMenu is the app's own menu for picking days (#1492): a few
// spans a click away, and a month to pick the first day on, then the last.
// Days after today can't be picked. choose gets the two days, in order.
function openRangeMenu(anchor, current, choose) {
  closeProtoMenu();
  const today = dayKeyOf(new Date());
  let start = null; // the first day clicked, waiting for the last
  let shown = dayOfKey(current?.to || today);
  shown = new Date(shown.getFullYear(), shown.getMonth(), 1, 12);
  const box = el("div", "pop proto-menu range-menu");
  box.setAttribute("role", "dialog");
  box.setAttribute("aria-label", t("Pick days"));
  const head = el("div", "pm-head");
  const quick = el("div", "rm-quick");
  const span = (from, to) => ({ from: dayKeyOf(from), to: dayKeyOf(to) });
  const now = new Date();
  now.setHours(12, 0, 0, 0);
  const back = (n) => { const d = new Date(now); d.setDate(d.getDate() - n); return d; };
  const quicks = [
    ["Yesterday", span(back(1), back(1))],
    ["This month", span(new Date(now.getFullYear(), now.getMonth(), 1, 12), now)],
    ["Last month", span(new Date(now.getFullYear(), now.getMonth() - 1, 1, 12), new Date(now.getFullYear(), now.getMonth(), 0, 12))],
    ["90 days", span(back(89), now)],
  ];
  for (const [name, r] of quicks) {
    const b = el("button", "rm-span" + (current && current.from === r.from && current.to === r.to ? " on" : ""), t(name));
    b.type = "button";
    b.title = rangeName(r);
    b.onclick = (e) => { e.stopPropagation(); closeProtoMenu(); choose(r.from, r.to); };
    quick.append(b);
  }
  const nav = el("div", "rm-nav");
  const prev = el("button", "rm-step"), next = el("button", "rm-step"), month = el("span", "rm-month");
  prev.type = next.type = "button";
  prev.setAttribute("aria-label", t("Previous month"));
  next.setAttribute("aria-label", t("Next month"));
  prev.append(svg("M10 4 6 8l4 4", 12, 1.6));
  next.append(svg("m6 4 4 4-4 4", 12, 1.6));
  nav.append(prev, month, next);
  const grid = el("div", "rm-grid");
  grid.setAttribute("role", "grid");
  box.append(head, quick, nav, grid);
  const draw = (focusDay) => {
    head.textContent = start ? t("Now the last day") : t("Pick the first day, then the last");
    month.textContent = shown.toLocaleDateString(intlLang(), { year: "numeric", month: "long" });
    next.disabled = dayKeyOf(new Date(shown.getFullYear(), shown.getMonth() + 1, 1, 12)) > today;
    const cells = [];
    // the weekdays, Monday first, as the app's weeks are
    for (let i = 0; i < 7; i++) cells.push(el("span", "rm-wd", new Date(2024, 0, 1 + i, 12).toLocaleDateString(intlLang(), { weekday: "narrow" })));
    const first = new Date(shown);
    const lead = (first.getDay() + 6) % 7;
    for (let i = 0; i < lead; i++) cells.push(el("span", "rm-pad"));
    const lo = start || current?.from, hi = start ? null : current?.to;
    for (let d = new Date(first); d.getMonth() === shown.getMonth(); d.setDate(d.getDate() + 1)) {
      const k = dayKeyOf(d);
      const b = el("button", "rm-day", String(d.getDate()));
      b.type = "button";
      b.dataset.day = k;
      b.setAttribute("aria-label", d.toLocaleDateString(intlLang(), { year: "numeric", month: "long", day: "numeric" }));
      if (k === today) b.classList.add("today");
      if (lo && (k === lo || k === hi)) b.classList.add("end");
      if (lo && hi && k > lo && k < hi) b.classList.add("in");
      b.disabled = k > today;
      b.onclick = (e) => {
        e.stopPropagation();
        if (!start) { start = k; draw(k); return; }
        const [from, to] = start <= k ? [start, k] : [k, start];
        closeProtoMenu();
        choose(from, to);
      };
      cells.push(b);
    }
    grid.replaceChildren(...cells);
    (focusDay && grid.querySelector(`[data-day="${focusDay}"]`))?.focus({ preventScroll: true });
  };
  const turn = (by) => (e) => { e.stopPropagation(); shown = new Date(shown.getFullYear(), shown.getMonth() + by, 1, 12); draw(); };
  prev.onclick = turn(-1);
  next.onclick = turn(1);
  draw();
  document.body.append(box);
  placeMenu(box, anchor);
  anchor.classList.add("open");
  anchor.setAttribute("aria-expanded", "true");
  const outside = (e) => { if (!box.contains(e.target) && !anchor.contains(e.target)) closeProtoMenu(); };
  const scroll = (e) => { if (scrollCloses(box, e)) closeProtoMenu(); };
  // Escape closes; arrows walk the days, a week up or down
  const keys = (e) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeProtoMenu(); anchor.focus({ preventScroll: true }); return; }
    const at = document.activeElement?.dataset?.day;
    const by = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }[e.key];
    if (!at || !by || !box.contains(document.activeElement)) return;
    e.preventDefault(); e.stopPropagation();
    const d = dayOfKey(at);
    d.setDate(d.getDate() + by);
    const k = dayKeyOf(d);
    if (k > today) return;
    if (d.getMonth() !== shown.getMonth() || d.getFullYear() !== shown.getFullYear()) shown = new Date(d.getFullYear(), d.getMonth(), 1, 12);
    draw(k);
  };
  document.addEventListener("mousedown", outside, true);
  document.addEventListener("keydown", keys, true);
  document.addEventListener("scroll", scroll, true);
  addEventListener("resize", closeProtoMenu);
  protoMenu = { box, anchor, outside, keys, scroll, done: null };
  (grid.querySelector(".rm-day.end") || grid.querySelector(".rm-day.today") || quick.firstChild).focus({ preventScroll: true });
}

function renderUsageLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  renderPeriod(true);
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  renderQuotas();
  const stats = $("#stats");
  stats.classList.remove("empty");
  stats.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const tile = el("div", "kpi loading-kpi");
    tile.append(el("span", "skeleton sk-number"), el("span", "skeleton sk-label"));
    stats.append(tile);
  }
  $("#chart").hidden = true;
  for (const id of ["usageAgents", "usageAgentsHid", "usageModels", "usageKeys"]) $("#" + id).hidden = true;
  // the allowances' head is renderQuotas' alone: the quotas it heads are
  // still there while the period loads (#809, Hu9956: 用量界面红框这行会延迟几秒才会出现)
  for (const h of $$("#view-usage .row-head:not(#quotaHead)")) h.hidden = true;
  $("#usageNote").textContent = "";
}

// costHead is the Usage head's sum: the cost at effective prices, or, when
// some of what it sums has no known price, how many beside it (#1856) — a
// total never reads as if those cost nothing — and with none priced, the
// count alone. note is that count in words ("" when everything was
// priced), title the tooltip.
function costHead(c, note, title) {
  const cost = $("#usageCost");
  cost.replaceChildren();
  cost.title = "";
  if (!c && !note) return;
  if (c) cost.append(el("b", "", "≈" + c));
  cost.append(el("span", note ? "unpriced" : "", note || t("effective prices")));
  cost.title = title;
}
const unpricedCalls = (n) => t(n === 1 ? "{n} call unpriced" : "{n} calls unpriced", { n: ledNum(n) });
const unpricedCallsWhy = (n) => n ? t(n === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n }) : t("Estimated using effective model prices, including custom prices");
// renderCosts redraws whatever on the Usage page shows a cost, once the
// currency changes — the numbers alone, not the page around them, so a
// click on the setting never moves anything it isn't showing (#212)
function renderCosts() {
  if (usage) renderUsage();
  if (ledger && usageTab === "requests") renderLedger();
  if (sessions) renderSessions();
  document.dispatchEvent(new Event("magpie-costs-changed"));
}
// quotaWho is how an account's card names it (#1515): by the name the user
// gave it, the account beside it, else the account; and the key plan it
// stands in for (seat) when that isn't already its name. A team's GLM
// seats read as the user tells them apart, not as 31****36@qq.com.
function quotaWho(q) {
  const out = [];
  if (q.alias) {
    const a = el("span", "user quota-alias", q.alias);
    a.title = q.user ? q.alias + " · " + q.user : q.alias;
    out.push(a);
    if (q.user) {
      const u = el("span", "quota-of", q.user);
      u.title = q.user;
      out.push(u);
    }
  } else if (q.user) {
    const u = el("span", "user", q.user);
    u.title = q.user;
    out.push(u);
  }
  if (q.seat && q.seat !== q.alias) {
    const s = el("span", "quota-seat", "≡ " + q.seat);
    s.title = t("The same seat as the key plan {name}, shown once, on this account's card", { name: q.seat });
    out.push(s);
  } else if (q.seat) out[0].title += "\n" + t("The same seat as the key plan {name}, shown once, on this account's card", { name: q.seat });
  return out;
}

function ownCalls(n) {
  return t("{n} by magpie itself", { n: fmtN(n) });
}
// quotaReadings: a line's last eight readings, newest first, each what was
// left and when it was read (#802), a line each, ↻ where the window
// started again; for its legend key's title
function quotaReadings(l, now) {
  const pts = l.points.slice(-8);
  return pts.map((p, i) => (i > 0 && quotaCycleBreak(pts[i - 1], p) ? "↻ " : "") + Math.round(p.left) + "% · " + quotaTimeText(p.at, now)).reverse();
}
// Keep the last date and as many earlier dates as fit, without clipping their
// text. Visibility keeps each label aligned to its bar; resizing restores them.
// This ran for the browser's phones alone; a native window is narrow too, and
// nothing there says so (a user at 950px: the dates ran one into the next), so
// a label is now hidden wherever it would collide with its neighbour.
function fitChartLabels() {
  const fits = !web || matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").matches;
  for (const labels of document.querySelectorAll(".chart .labels")) {
    const spans = [...labels.children];
    for (const span of spans) span.style.visibility = "";
    if (!fits || !labels.offsetWidth) continue;
    const box = labels.getBoundingClientRect();
    let right = box.right + 8;
    for (const span of spans.reverse()) {
      if (!span.textContent) continue;
      const range = document.createRange();
      range.selectNodeContents(span);
      const r = range.getBoundingClientRect();
      if (r.right + 8 > right + 1 || r.left < box.left - 1) span.style.visibility = "hidden";
      else right = r.left;
    }
  }
}
new ResizeObserver(fitChartLabels).observe($("#view-usage"));
// the dates are measured in the page's font, which can land after the first draw
document.fonts?.ready.then(fitChartLabels);

// The Overview's lists — Agents, Models, Accounts, Gateway keys — each fold
// away under their heading (#2378: 日常看账号维度较多，都要拉到最底下), and
// stay folded across a reload (magpie.usageFolded, the folded ones' names).
let usageFolded = new Set();
try { usageFolded = new Set(JSON.parse(localStorage.getItem("magpie.usageFolded") || "[]")); } catch {}
const USAGE_FOLDS = { agents: ["usageAgents", "usageAgentsHid"], models: ["usageModels"], accounts: ["usageAccounts"], keys: ["usageKeys"] };
const USAGE_FOLD_TITLES = {
  agents: ["Show the agents", "Fold the agents away"],
  models: ["Show the models", "Fold the models away"],
  accounts: ["Show the accounts", "Fold the accounts away"],
  keys: ["Show the gateway keys", "Fold the gateway keys away"],
};
function mountUsageFolds() {
  for (const b of $$("#usagePane .row-head .fold[data-fold]")) {
    b.prepend(svg(CHEV_R, 11, 1.6));
    b.onclick = () => {
      const id = b.dataset.fold;
      if (usageFolded.has(id)) usageFolded.delete(id); else usageFolded.add(id);
      try { localStorage.setItem("magpie.usageFolded", JSON.stringify([...usageFolded])); } catch {}
      showUsageFolds();
      backToReader($("#view-usage"));
    };
  }
  showUsageFolds();
}
// a folded list is out of sight by its class, so the page's own hidden
// (no calls, no accounts) is left as it is
function showUsageFolds() {
  for (const b of $$("#usagePane .row-head .fold[data-fold]")) {
    const id = b.dataset.fold, folded = usageFolded.has(id);
    b.setAttribute("aria-expanded", String(!folded));
    b.title = t(USAGE_FOLD_TITLES[id][folded ? 0 : 1]);
    for (const box of USAGE_FOLDS[id]) $("#" + box)?.classList.toggle("folded", folded);
  }
}

// usageParts: a line of counts, each kept whole in a span of its own and
// " · " between them, where the line may wrap ("{a} in · {b} out" is two)
function usageParts(texts) {
  const out = [];
  for (const x of texts.flatMap((s) => s.split(" · "))) out.push(...(out.length ? [" · "] : []), el("span", "", x));
  return out;
}

// The Overview's token totals count what went in and out; the cache's
// tokens, read and written, can be counted in them too (#2378), as the
// Requests tab's total always has them. Kept as the reader left it
// (magpie.usageWithCache).
let usageWithCache = false;
try { usageWithCache = localStorage.getItem("magpie.usageWithCache") === "1"; } catch {}
const cacheOf = (x) => (x.cache_read || 0) + (x.cache_write || 0);
const usageTokens = (x) => tokensOf(x) + (usageWithCache ? cacheOf(x) : 0);
// the tokens tile's label: a pop-up of the two ways to count
function usageTokensPick() {
  const b = el("button", "kpi-pick");
  b.type = "button";
  b.setAttribute("aria-haspopup", "menu");
  b.append(el("span", "", t(usageWithCache ? "tokens with cache" : "tokens")), svg(CHEV, 10, 1.6));
  b.title = t("Count the cache's tokens in the totals, or leave them out");
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, [
      { v: "", name: "Without cache", note: "Input and output" },
      { v: "1", name: "With cache", note: "Cache read and written counted too" },
    ], usageWithCache ? "1" : "", (v) => {
      usageWithCache = v === "1";
      try { localStorage.setItem("magpie.usageWithCache", usageWithCache ? "1" : "0"); } catch {}
      renderUsage();
    }, "Token totals", "sess-menu");
  };
  return b;
}

function renderUsage() {
  const u = usage;
  const view = $("#view-usage");
  showUsageFolds();
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderPeriod();
  costHead(fmtCost(u), u.unpriced ? unpricedCalls(u.unpriced) : "", unpricedCallsWhy(u.unpriced));

  const stats = $("#stats");
  stats.replaceChildren();
  const empty = !u.calls;
  $("#chart").hidden = empty;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = empty;
  if (empty) $("#usageAgentsHid").hidden = true;
  for (const h of $$("#view-usage .row-head:not(#quotaHead)")) h.hidden = empty;
  $("#usageKeysHead").hidden = $("#usageKeys").hidden = empty || !u.callerKeys?.length;
  // each subscription account's share, by the account that answered (#557)
  $("#usageAccountsHead").hidden = $("#usageAccounts").hidden = empty || !u.accounts?.length;
  if (empty) {
    stats.classList.add("empty");
    const none = noCallsIn(period);
    stats.append(el("div", "none", t(none) + " " + t("Point an agent at a catalog model and use it; every call through the gateway is counted here.")));
    $("#usageNote").textContent = "";
    return;
  }
  stats.classList.remove("empty");
  const tile = (n, label, sub, title) => {
    const t = el("div", "kpi");
    if (title) t.title = title;
    t.append(el("b", "", n), typeof label === "string" ? el("span", "", label) : label);
    // a line in parts keeps each whole, wrapping between them rather than
    // cutting the last off
    if (Array.isArray(sub)) {
      const s = el("small", "parts");
      s.append(...usageParts(sub));
      t.append(s);
    } else if (sub) t.append(el("small", "", sub));
    stats.append(t);
  };
  tile(fmtN(usageTokens(u)), usageTokensPick(), [t("{a} in · {b} out", { a: fmtN(u.input), b: fmtN(u.output) }), ...(usageWithCache && cacheOf(u) ? [t("{a} cache", { a: fmtN(cacheOf(u)) })] : [])]);
  // cache reads are billed at a fraction of input, so how much of the prompt
  // came from cache is the number that explains the bill; input here already
  // excludes the cached tokens, read and written (the gateway subtracts
  // them), so what was written counts in the prompt too: left out, a prompt
  // written to the cache again at every turn showed a rate near 100%. The
  // written count is secondary and only fits in the tooltip.
  const promptTokens = u.input + u.cache_read + u.cache_write;
  const hit = u.cache_read && promptTokens ? t("hit rate {p}", { p: Math.round(100 * u.cache_read / promptTokens) + "%" }) : "";
  tile(fmtN(u.cache_read), t("cache read"), hit, u.cache_write ? t("{n} written", { n: fmtN(u.cache_write) }) + cacheTTLs(u, fmtN) : "");
  tile(fmtN(u.reasoning), t("reasoning"), t("inside output"));
  tile(String(u.calls), t(u.calls === 1 ? "call" : "calls"), [u.errors ? t("{n} failed", { n: u.errors }) : "", u.retried ? t("{n} answered on a retry", { n: u.retried }) : "", u.unpriced ? t("{n} unpriced", { n: u.unpriced }) : ""].filter(Boolean).join(" · "), u.unpriced ? unpricedCallsWhy(u.unpriced) : "");

  // the timeline: one bar per hour, day or week; output sits on top of input
  const chart = $("#chart");
  chart.replaceChildren();
  const bars = el("div", "bars");
  const peak = Math.max(1, ...u.series.map(usageTokens));
  const labels = el("div", "labels");
  const n = u.series.length;
  const every = n <= 8 ? 1 : n <= 31 ? Math.ceil(n / 6) : Math.ceil(n / 5);
  // the day in the reader's language (the label comes as "Sep 5" in every one)
  const dayOf = (p) => u.bucket === "hour" || !p.time ? p.label : new Date(p.time).toLocaleDateString(intlLang(), { month: "short", day: "numeric" });
  u.series.forEach((p, i) => {
    const b = el("div", "bar");
    const inp = el("i", "in"), out = el("i", "out");
    // counted with the cache, its tokens are in the prompt's part
    inp.style.height = (100 * (usageTokens(p) - p.output) / peak).toFixed(1) + "%";
    out.style.height = (100 * p.output / peak).toFixed(1) + "%";
    b.append(out, inp);
    const when = u.bucket === "hour" ? `${p.label}:00` : u.bucket === "week" ? t("week of {label}", { label: dayOf(p) }) : dayOf(p);
    b.title = p.calls ? t(p.calls === 1 ? "{when} · {tokens} tokens · {n} call" : "{when} · {tokens} tokens · {n} calls", { when, tokens: fmtN(usageTokens(p)), n: p.calls }) + (fmtCost(p) ? " · ≈" + fmtCost(p) : "") : t("{when} · nothing", { when });
    bars.append(b);
    const last = i === n - 1 && (n - 1) % every >= every / 2;
    labels.append(el("span", "", i % every === 0 || last ? dayOf(p) : ""));
  });
  chart.append(el("div", "peak", fmtN(peak)), bars, labels);
  fitChartLabels();

  const total = Math.max(1, usageTokens(u));
  const list = (id, groups, act) => {
    const box = typeof id === "string" ? $("#" + id) : id;
    box.replaceChildren();
    for (const g of groups) {
      const r = el("div", "row stat");
      r.dataset.id = g.id || "";
      r.append(icon(g.icon || "generic"));
      const who = el("div", "who");
      who.append(el("div", "name" + (g.name ? "" : " faint"), g.name || t("account not recorded")));
      const sub = [];
      if (g.sub) sub.push(g.sub);
      sub.push(t(g.calls === 1 ? "{n} call" : "{n} calls", { n: g.calls }));
      // magpie's own calls apart from the agents' (#1839): magpie's row
      // says it is, and a model's, a key's or an account's how many of
      // its calls were
      if (id === "usageAgents" && g.id === "magpie") who.firstChild.append(ownBadge());
      else if (g.own?.calls) sub.push(ownCalls(g.own.calls));
      if (g.errors) sub.push(t("{n} failed", { n: g.errors }));
      // of those, the replies magpie ended stuck in a loop (#1858)
      if (g.loops) sub.push(t("{n} looped", { n: g.loops }));
      if (g.retried) sub.push(t("{n} answered on a retry", { n: g.retried }));
      // how long the streamed replies took to begin, and how fast they
      // wrote after (#196)
      if (g.timed) {
        sub.push(t("TTFT {ms}", { ms: ledTook(Math.round(g.ttft_ms / g.timed)) }));
      }
      const gs = ledGroupSpeed(g);
      if (gs) sub.push(gs.text);
      const subLine = el("div", "sub", sub.join(" · "));
      if (g.own?.calls) subLine.title = ownWhy();
      who.append(subLine);
      r.append(who);
      const share = el("div", "share");
      const fill = el("i");
      fill.style.width = Math.max(1.5, 100 * usageTokens(g) / total).toFixed(1) + "%";
      share.append(fill);
      share.title = t("{n}% of tokens", { n: Math.round(100 * usageTokens(g) / total) });
      r.append(share);
      const num = el("div", "num");
      // how much of its prompt came from the cache, counted as the cache
      // read tile counts it (#925)
      const prompt = g.input + g.cache_read + (g.cache_write || 0);
      const hit = g.cache_read && prompt ? " · " + t("hit rate {p}", { p: Math.round(100 * g.cache_read / prompt) + "%" }) : "";
      num.append(el("b", "", fmtN(usageTokens(g))), el("small", "", t("{a} in · {b} out", { a: fmtN(g.input), b: fmtN(g.output) }) + (g.cache_read ? " · " + t("{n} cached", { n: fmtN(g.cache_read) }) : "") + hit));
      r.append(num);
      r.append(el("div", "cost", fmtCost(g) ? "≈" + fmtCost(g) : ""));
      if (act) act(r, g);
      box.append(r);
    }
  };
  // a client hidden from the Agents list (ikunbots on X: curl,
  // Go-http-client, python-httpx beside the agents in use) is left out of
  // it, and only of it: the totals, the chart and every share above still
  // count its calls. One line under the list says how many, and opens them.
  const hid = new Set(state.settings?.usageAgentsHidden || []);
  const hidden = u.agents.filter((g) => hid.has(g.id));
  list("usageAgents", u.agents.filter((g) => !hid.has(g.id)), (r, g) => {
    r.classList.add("acts");
    const b = el("button", "icon us-more");
    b.type = "button";
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-label", t("Actions for {name}", { name: g.name || g.id }));
    b.append(svg(DOTS, 14, 2.2));
    // a client magpie doesn't know can be counted as one of the agents
    // it does (#1958, NorethSea): a bridge that starts Codex under a name
    // of its own; the agent counting one has it put back on its own
    const counted = Object.entries(state.settings?.clientAgents || {}).filter(([, as]) => as === g.id).map(([c]) => c);
    const menu = () => {
      const again = agentMenu?.anchor === b;
      closeAgentMenu();
      if (again) return;
      openRowMenu(b, [
        ...(countable(g.id) ? [{ name: "Count as an agent…", icon: COUNT_AS, run: () => pickCountAs(b, g) }] : []),
        ...counted.map((c) => ({ name: t("Count {client} on its own", { client: c }), icon: OUT, run: () => countClientAs(c, "") })),
        { name: "Hide from this list", icon: EYE_OFF, sep: countable(g.id) || counted.length > 0, run: () => hideUsageAgent(g.id, true) },
      ]);
    };
    if (counted.length) {
      const sub = r.querySelector(".who .sub");
      sub.textContent = t("with {clients}", { clients: counted.join(", ") }) + " · " + sub.textContent;
    }
    b.onclick = (e) => { e.stopPropagation(); menu(); };
    r.oncontextmenu = (e) => { e.preventDefault(); menu(); };
    r.querySelector(".share").after(b);
  });
  const hb = $("#usageAgentsHid");
  hb.hidden = !hidden.length;
  if (!hidden.length) usageHidOpen = false;
  const line = hb.querySelector(".us-hidline"), hl = hb.querySelector(".list");
  line.replaceChildren(el("span", "", t("{n} hidden", { n: hidden.length })), svg(CHEV, 11, 1.6));
  line.title = t("Hidden from this list only: their calls still count in the totals and the chart. Click to see them.");
  line.setAttribute("aria-expanded", String(usageHidOpen));
  line.onclick = () => { usageHidOpen = !usageHidOpen; renderUsage(); };
  hl.hidden = !usageHidOpen;
  list(hl, usageHidOpen ? hidden : [], (r, g) => {
    r.classList.add("hid", "acts");
    const b = el("button", "text us-unhide", t("Unhide"));
    b.type = "button";
    b.onclick = (e) => { e.stopPropagation(); hideUsageAgent(g.id, false); };
    r.append(b);
  });
  list("usageModels", u.models);
  list("usageAccounts", u.accounts || []);
  list("usageKeys", u.callerKeys || []);
  $("#usageNote").textContent = t("Counted from the providers' own usage reports on every call through the gateway · {path}", { path: u.path });
  // the account cards say how many of this period's requests were
  // magpie's own (ownLine), which they could only once the summary came
  if (quotas && (u.accounts?.some((g) => g.own?.calls) || $("#subscriptionUsage .quota-own"))) renderQuotas();
}

// countable is whether the Usage page's Agents list offers to count a
// client as an agent: one magpie doesn't know, not magpie's own (#1839)
// nor the calls that named no client.
const COUNT_AS = "M3 3v3.5A3.5 3.5 0 0 0 6.5 10H13M10 7l3 3-3 3";
function countable(id) {
  return !!id && id !== "other" && !/^magpie/i.test(id) && !(state.clients || []).some((a) => a.id === id);
}
// pickCountAs opens the agents a client can be counted as, under its menu
// button.
function pickCountAs(anchor, g) {
  const opts = (state.clients || []).filter((a) => a.id !== "magpie").map((a) => ({ v: a.id, name: a.name, literalName: true, note: "" }));
  openProtoMenu(anchor, opts, "", (v) => countClientAs(g.id, v), "Count this client's calls as", "", "left", false, opts.length > 8 ? "Search agents…" : "");
}
// countClientAs counts a client's calls as an agent's on the Usage and
// Sessions pages, or on its own again (as ""), and draws the page again
// from the ledger read anew: the history is counted the new way too.
async function countClientAs(client, as) {
  try {
    const s = await api("usage/arrange", { count: { client, as } });
    state.settings = { ...state.settings, clientAgents: s.clientAgents || {} };
    status(as ? t("{client} is counted as {agent}", { client, agent: (state.clients || []).find((a) => a.id === as)?.name || as }) : t("{client} is counted on its own", { client }), "ok");
    await loadUsage();
  } catch (e) {
    status(e.message, "err");
  }
}

// hideUsageAgent takes a client out of the Usage page's Agents list, or
// puts it back: drawn at once, put back if the save fails. Its calls stay
// in the ledger and in every total.
async function hideUsageAgent(id, hide) {
  const prev = state.settings || {};
  const cur = (prev.usageAgentsHidden || []).filter((k) => k !== id);
  const agentsHidden = hide ? [...cur, id] : cur;
  state.settings = { ...prev, usageAgentsHidden: agentsHidden };
  const again = () => {
    if (!usage) return;
    renderUsage();
    // the focus stays on the list: the row put back, else the hidden line
    const to = hide ? $("#usageAgentsHid .us-hidline") : $(`#usageAgents .row.stat[data-id="${CSS.escape(id)}"] .us-more`);
    (to && !to.closest("[hidden]") ? to : null)?.focus({ preventScroll: true });
  };
  again();
  try {
    const s = await api("usage/arrange", { agentsHidden });
    state.settings = { ...state.settings, usageAgentsHidden: s.usageAgentsHidden || [] };
  } catch (e) {
    state.settings = prev;
    again();
    status(e.message, "err");
  }
}

// ---------- requests ----------
//
// The ledger: every request of the period, one row each, newest first —
// what the agent asked for, where it went, the model sent and the one the
// reply says answered, the tokens, what they cost at effective prices, how long
// it took and how it ended — to set beside a vendor's own bill. The
// server pages it (/api/usage/requests) and saves it whole as CSV.

let ledger = null; // the page shown: { rows, offset, total, agents, …totals }
let ledOffset = 0, ledPurpose = "", ledAgent = "", ledProvider = "", ledAccount = "", ledCallerKey = "", ledFailed = false, ledRetried = false, ledQuery = "", ledModel = "", ledRoute = 0, ledComputer = "", ledDay = "", ledVia = "";

const LED_PAGE = 100;
// Remember the chart metric; start each app load split by model.
let ledMetric = "tokens", ledSplit = "model";
try {
  const m = localStorage.getItem("magpie.ledMetric");
  if (["tokens", "cost", "calls", "speed"].includes(m)) ledMetric = m;
} catch {}

let ledRouteInfo = null, ledBeforeRoute = null;
window.openUsageRoute = (route) => {
  if (!ledBeforeRoute) ledBeforeRoute = { period, ledOffset, ledPurpose, ledAgent, ledProvider, ledAccount, ledCallerKey, ledFailed, ledRetried, ledQuery, ledModel, ledComputer, ledDay, ledVia };
  ledRoute = route.id;
  ledRouteInfo = route;
  ledOffset = 0; ledPurpose = ""; ledAgent = ""; ledProvider = ""; ledAccount = ""; ledCallerKey = ""; ledFailed = false; ledRetried = false; ledQuery = ""; ledModel = ""; ledComputer = ""; ledVia = "";
  ledDay = "";
  $("#ledQ").value = "";
  period = "all";
  usageTab = "requests";
  ledger = null;
  show("usage");
};

function ledParams(extra) {
  const q = new URLSearchParams({ period });
  if (ledDay) q.set("day", ledDay);
  if (ledAgent) q.set("agent", ledAgent);
  if (ledPurpose) q.set("purpose", ledPurpose);
  if (ledProvider) q.set("provider", ledProvider);
  if (ledAccount) q.set("account", ledAccount);
  if (ledComputer) q.set("computer", ledComputer);
  if (ledCallerKey) q.set("callerKey", ledCallerKey);
  if (ledVia) q.set("via", ledVia);
  if (ledRoute) q.set("route", ledRoute);
  if (ledFailed) q.set("failed", "1");
  if (ledRetried) q.set("retried", "1");
  if (ledModel) q.set("model", ledModel);
  else if (ledQuery.trim()) q.set("q", ledQuery.trim());
  if (extra) for (const k in extra) q.set(k, extra[k]);
  return q.toString();
}

// One read of the ledger at a time (Vantiboo1ean, ~750k requests): the page
// asked again while it is read (the refresh button, the timer) waits for
// that read, and another page or filter cancels it. Each pick and press
// used to send one more, and a long history's reads, all still running,
// took the browser's few connections to magpie, so the page never came.
// flight: { want, ctl, p }
const ledFlights = { page: null, heat: null };
function ledRead(kind, path, want) {
  const was = ledFlights[kind];
  if (was?.want === want) return was.p;
  was?.ctl.abort();
  const ctl = new AbortController();
  const f = { want, ctl, p: api(path + want, undefined, undefined, ctl.signal) };
  ledFlights[kind] = f;
  const done = () => { if (ledFlights[kind] === f) ledFlights[kind] = null; };
  f.p.then(done, done);
  return f.p;
}
const ledAborted = (e) => e?.name === "AbortError";

// quiet: a refresh while it's looked at, redrawn only on a change
async function loadLedger(quiet) {
  if (!ledger && !quiet) renderLedgerLoading();
  // a pick over rows shown: they stay, dimmed, till its answer comes, so
  // the pick is seen taken (#1990, overstarry: over a long history the
  // read takes seconds, and the table sat as it was, as if the pick
  // hadn't been)
  else if (!quiet) ledPending(true);
  loadLedHeat().catch(() => {});
  const want = ledParams({ offset: ledOffset, limit: LED_PAGE });
  let l;
  try { l = await ledRead("page", "usage/requests?", want); } catch (e) {
    if (ledAborted(e)) return; // another page or filter was picked, and is read instead
    ledPending(false);
    throw e;
  }
  if (want !== ledParams({ offset: ledOffset, limit: LED_PAGE })) return; // another page or filter was picked meanwhile
  ledPending(false);
  if (quiet && JSON.stringify(l) === JSON.stringify(ledger)) return;
  ledger = l;
  if (view === "usage" && usageTab === "requests") renderLedger();
}

function ledPending(on) {
  const wrap = $("#ledWrap");
  wrap.classList.toggle("led-pending", on);
  if (on) wrap.setAttribute("aria-busy", "true");
  else wrap.removeAttribute("aria-busy");
}

function renderLedgerLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  renderPeriod(true);
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  $("#ledSum").textContent = "";
  $("#ledPager").hidden = true;
  const wrap = $("#ledWrap");
  wrap.classList.remove("none");
  wrap.replaceChildren();
  $("#ledHScroll").hidden = true;
  for (let i = 0; i < 5; i++) {
    const r = el("div", "led-sk");
    r.append(el("span", "skeleton sk-line"));
    wrap.append(r);
  }
}

function ledTime(when) {
  const d = new Date(when), now = new Date();
  const opts = { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false };
  if (d.toDateString() !== now.toDateString()) Object.assign(opts, { month: "short", day: "numeric" });
  return d.toLocaleString(intlLang(), opts);
}
// the model the reply named: amber, as the Routing page's tag, when it is
// another than the one sent; plain when it is that one under a dated name,
// or the member a remote magpie's routing group sent it to, said in its title
function ledServed(r) {
  if (!r.served) return el("span", "faint", "—");
  if (r.routed && !r.swapped) {
    const k = el("span", "muted model-value routed", r.served);
    k.title = window.routedWhy ? window.routedWhy({ model: r.model, served: r.served }) : "";
    return k;
  }
  if (!r.swapped) return el("span", "model-value", r.served);
  const k = el("span", "swap", r.served);
  k.title = window.swapWhy ? window.swapWhy({ model: r.model, served: r.served }) : "";
  return k;
}

// the protocol a request was sent upstream in, told by the path it went
// out on, and the one its agent spoke when that was another: the
// endpoint reads "/v1/chat/completions → /v1/messages" for a request
// translated, the agent's own path alone for one sent as it came (蓝猫 on
// Discord). null for a request with no path kept, a session file's. Gemini's
// is generateContent, or streamGenerateContent streamed (Gemini CLI's,
// Vertex AI's).
const ledProtoOf = (path) => /\/messages\b/.test(path) ? "Anthropic" : /\/responses\b/.test(path) ? "Responses"
  : /\/chat\/completions\b/.test(path) ? "Chat" : /[gG]enerateContent|\/generate\b/.test(path) ? "Gemini" : "";
function ledProtos(r) {
  if (!r.ep || r.source === "log") return null;
  const [a, b] = String(r.ep).split(" → ");
  const from = ledProtoOf(a), to = b ? ledProtoOf(b) : from;
  if (!to) return null;
  return { from: from || to, to };
}
function ledProtoBadge(r) {
  const p = ledProtos(r);
  if (!p) return null;
  const k = el("span", "src proto proto-" + p.to.toLowerCase(), p.from !== p.to ? p.from + " → " + p.to : p.to);
  k.title = p.from !== p.to ? t("The agent spoke {from}; sent upstream as {to}", p) : t("Sent upstream as {to}, as the agent spoke it", p);
  return k;
}

// a request that failed: told by its status, or, for one read from a
// session file, which records none, by the error that ended it
const ledFailed_ = (r) => r.status >= 400 || !!r.err;
const ledKey = (r) => [r.t, r.agent, r.model, r.rid, r.session, r.computer || ""].join("|");
const ledOpen = new Set(); // the rows opened, kept across a refresh

// what is known of one request beyond its row: the id its vendor gave it,
// the path it went to, and, if it failed, what the vendor said
function ledDetail(r, cols) {
  const dl = el("dl", "led-dl");
  const add = (name, value, cls) => {
    if (value === "" || value == null) return;
    dl.append(el("dt", "", t(name)), el("dd", cls || "", value));
  };
  if (ledFailed_(r)) {
    add("Status", r.status ? String(r.status) : "—", "bad");
    add("Error type", r.err_type, "bad");
    add(r.source === "log" ? "Error" : "Upstream said", r.err, "said");
  } else {
    // how it went, here as well as in the row's last column, which a narrow
    // window leaves out of sight (#799)
    add("Status", r.status ? String(r.status) : r.source === "log" ? t("Succeeded") : "");
  }
  if (Number.isFinite(r.ms) && r.ms > 0) add("Duration", (r.source === "log" ? "≈" : "") + ledTook(r.ms));
  // the way it was routed: the row's time links there too, which reads as
  // a time rather than a link (#799)
  // (with the Routing page switched off there is nowhere to see it)
  if (r.route_id && !featuresOff.has("routing")) {
    const go = el("button", "text led-route-link", t("View routing"));
    go.onclick = (e) => { e.stopPropagation(); window.openRoute(r.route_id, r.t).catch((err) => status(err.message, "err")); };
    const dd = el("dd");
    dd.append(go);
    dl.append(el("dt", "", t("Routing")), dd);
  }
  if (r.computerName) add("Computer", r.computerName);
  // the client's own name, for one the user counts as an agent (#1958)
  add("Client", r.client);
  // the columns a narrow window leaves out, shown only while it does (#860)
  const opt = (name, value) => {
    if (!value) return;
    add(name, value);
    dl.lastChild.classList.add("lc-opt");
    dl.lastChild.previousSibling.classList.add("lc-opt");
  };
  opt("Sent", r.model);
  opt("Effort", r.effort);
  // its tokens at every width, the cache's beside in and out: a prompt read
  // from the cache is told from one that missed it, whose input is the
  // whole of it, where the row's cache columns are out of sight (#1839).
  // In leaves the cache out, as Anthropic counts it.
  if (r.in || r.out || r.cache_read || r.cache_write || !ledFailed_(r)) {
    add("Tokens", t("{i} in · {o} out · {r} cache read · {w} cache creation", { i: ledNum(r.in || 0), o: ledNum(r.out || 0), r: ledNum(r.cache_read || 0), w: ledNum(r.cache_write || 0) }) + cacheTTLs(r, ledNum), "led-tokens");
  }
  if (ledRowSpeed(r)) add("Speed", t("{n} tok/s", { n: ledNum(Math.round(ledRowSpeed(r))) }));
  else if (ledFloorSpeed(r)) add("Speed", "≥" + t("{n} tok/s", { n: ledNum(Math.round(ledFloorSpeed(r))) }));
  add("Request ID", r.rid);
  add("Endpoint", r.ep);
  const protos = ledProtos(r);
  if (protos) add("Protocol", protos.from !== protos.to ? protos.from + " → " + protos.to : protos.to);
  // why the upstream said its reply ended, in its own words: a reply that
  // ended too soon is told apart by it (蓝猫 on Discord)
  add("Upstream stop reason", r.stop);
  // the provider an aggregator (OpenRouter …) said answered behind it
  add("Upstream provider", r.upstream);
  add("Session ID", r.session);
  // a call that isn't a turn of the conversation says what it was for, as
  // the agent named it: a subagent's, a title, an approval check… — a
  // model the conversation never picked reads as what it is (#714)
  if (r.kind) add("Called for", window.kindName ? window.kindName(r.kind) : r.kind);
  if (r.ttft_ms) add("First token", ledTook(r.ttft_ms));
  // of it, what came before the request went to the vendor, and the
  // vendor's wait from then to its first token. Before it, magpie's own
  // time is told apart from the tries that failed first, a wait for a
  // slot and connecting and sending the body (#1532: a vendor that held a
  // try ~100 s before its 503 read as 104 s in magpie); a record from
  // before they were kept has only the whole
  if (r.ttft_ms && r.sent_ms) {
    const split = r.own_ms != null;
    add("In magpie", ledTook(split ? r.own_ms : r.sent_ms));
    if (split) {
      if (r.retried_ms > 0) add("Tries that failed first", ledTook(r.retried_ms));
      if (r.queued_ms > 0) add("Waiting for a slot", ledTook(r.queued_ms));
      const sending = r.sent_ms - r.own_ms - (r.retried_ms || 0) - (r.queued_ms || 0);
      if (sending > 0) add("Connecting and sending", ledTook(sending));
    }
    add("Vendor's first token", ledTook(Math.max(0, r.ttft_ms - r.sent_ms)));
  }
  if (r.reasoning) add("Reasoning tokens", ledNum(r.reasoning));
  if (r.session_provider) add("Recorded provider ID", r.session_provider);
  if (r.session_account) add("Session account", r.session_account);
  if (r.source === "log" && r.session_account && r.session_official_login) add("Login method", t("Official login"));
  if (r.pricing_model) add("API price reference", r.pricing_model);
  // with the archive on, a request it has no copy of says so: from before
  // it was on, or not through the gateway
  if (!r.archive && !r.computer && (providers?.gateway?.archive?.on ?? state.settings?.requestArchive)) add("Request archive", t("Not archived"), "muted");
  if (r.source === "log") add("Data source", t("Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred."), "muted");
  const tr = el("tr", "led-detail");
  const td = el("td");
  td.colSpan = cols;
  const box = el("div", "led-box");
  box.append(dl.childElementCount ? dl : el("span", "faint", t("Nothing more was kept of this request")));
  // the call as the request archive kept it, when it was on (#447)
  if (r.archive) {
    const ab = el("div", "led-archive");
    const draw = () => ab.replaceChildren(archivePanel(r.archive, "led|" + ledKey(r), draw));
    draw();
    box.append(ab);
  }
  // what was said: read from the agent's session file, when the row is
  // opened; one made on another computer has its files there, and sync
  // brings none of what was said
  const cx = el("div", "led-cx");
  if (r.computer) {
    cx.append(el("p", "cx-none", t("What was said stays on {computer}: sync shares the usage alone.", { computer: r.computerName || r.computer })));
  } else {
    cx.append(el("p", "cx-none", t("Loading…")));
    ledLoadContent(r).then((c) => { if (cx.isConnected) cx.replaceChildren(ledContentBox(c)); });
  }
  box.append(cx);
  td.append(box);
  tr.append(td);
  return tr;
}

// What was said in a request — what its agent was given, and what came back —
// is in the agent's own session file, and read from it when a row is opened
// (the server finds the call by its session and time); magpie keeps no copy.
// What came is kept here while the list is looked at, so a row opened again, or
// drawn again as the list is read anew, doesn't ask again.
const ledContent = new Map();
function ledLoadContent(r) {
  const key = ledKey(r);
  if (!ledContent.has(key)) {
    if (ledContent.size > 60) ledContent.delete(ledContent.keys().next().value);
    const t0 = new Date(r.t).getTime();
    // a call of a session file is at its own time; a request the gateway logged began
    // a little before the file wrote it, and the call is in the span it took
    const [a, b] = r.source === "log" ? [t0 - 1, t0 + 1] : [t0 - 5e3, t0 + (r.ms || 0) + 30e3];
    // the window can hold the next calls of a quick tool loop too: the one that
    // ended nearest the request's own end is its call
    const at = r.source === "log" ? t0 : t0 + (r.ms || 0);
    const q = new URLSearchParams({ agent: r.agent, session: r.native_session || r.session || "", from: new Date(a).toISOString(), to: new Date(b).toISOString(), at: new Date(at).toISOString() });
    const pending = api("usage/requests/content?" + q).catch(() => ({ found: false, why: "read" })).then((c) => {
      if (!c.found && ledContent.get(key) === pending) ledContent.delete(key);
      return c;
    });
    ledContent.set(key, pending);
  }
  return ledContent.get(key);
}
const LED_WHY = {
  session: "No session was named with this request, so its session file can't be found",
  agent: "magpie reads the session files of Claude Code, Claude Desktop and Codex only",
  missing: "This request isn't in the agent's session files: they may be deleted, moved, or not written yet",
  read: "The session file couldn't be read",
};
function ledContentBox(c) {
  const box = el("div", "cx");
  if (!c.found) {
    box.append(el("p", "cx-none", t(LED_WHY[c.why] || LED_WHY.read)));
    return box;
  }
  for (const [name, parts] of [["Input", c.input || []], ["Output", c.output || []]]) {
    const sec = el("section", "cx-sec");
    sec.append(el("h4", "", t(name)));
    if (!parts.length) sec.append(el("p", "cx-none", t("Nothing was said here")));
    for (const p of parts) sec.append(ledSaid(p));
    box.append(sec);
  }
  if (c.cut) box.append(el("p", "cx-none", t("There was more than is shown here")));
  box.append(el("p", "cx-src", t("Read from the agent's session file; magpie keeps no copy")));
  return box;
}

// ---- the totals and the trend over the requests listed
//
// A strip of four totals, then the trend of one metric — tokens, cost or
// requests — as columns by the hour, day or week, each told apart by provider,
// agent or model, beside a ranking of the same: which is the chart's legend
// and a way in (a click on a provider or agent lists only its requests). The
// server sums it all (the ledger's "series" and "by"); the tray panel's Usage
// tab draws the same from the same answer.

// the window's chart can also say how fast the replies came (#860)
const LED_TREND = [...LED_METRICS, ["speed", "Speed"]];
// "modelAt" is a model at the provider it went to, so one model's speed at
// each provider is ranked side by side (inaction on Discord); "callerKey"
// the gateway key that made it, "none" for calls with none, offered while
// the period has a key's calls (#2521)
const LED_SPLITS = [["model", "Model"], ["modelAt", "Model · provider"], ["provider", "Provider"], ["agent", "Agent"], ["callerKey", "Gateway key"]];
// cacheTTLs: a call's cache writes split by how long they are kept, where
// some were for an hour (Anthropic bills those at 2× input, 5 minutes'
// at 1.25×); nothing when none were
function cacheTTLs(x, fmt) {
  const h = Math.min(x.cache_write_1h || 0, x.cache_write || 0);
  return h ? " · " + t("{m} for 5 minutes, {h} for an hour", { m: fmt(x.cache_write - h), h: fmt(h) }) : "";
}
// the rail's thumb drags the ranking; a click on the rail takes it there
function ledRail(rail, box) {
  box.rail = rail;
  rail.onpointerdown = (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const end = box.scrollHeight - box.clientHeight, thumb = rail.firstChild.offsetHeight, room = rail.clientHeight - thumb;
    if (end <= 0 || room <= 0) return;
    if (e.target !== rail.firstChild) {
      // the thumb's middle to where the rail was clicked
      const y = e.clientY - rail.getBoundingClientRect().top - thumb / 2;
      box.scrollTop = (Math.max(0, Math.min(room, y)) / room) * end;
    }
    const y0 = e.clientY, top0 = box.scrollTop;
    rail.setPointerCapture(e.pointerId);
    rail.classList.add("drag");
    rail.onpointermove = (m) => { box.scrollTop = top0 + ((m.clientY - y0) / room) * end; };
    rail.onpointerup = rail.onpointercancel = () => {
      rail.classList.remove("drag");
      rail.onpointermove = rail.onpointerup = rail.onpointercancel = null;
    };
  };
}

// the request list's status filter: "" for all, "failed" or "retried"
const ledStatusOf = () => ledFailed ? "failed" : ledRetried ? "retried" : "";
function ledShowStatus(to) {
  if (to === ledStatusOf()) return;
  ledFailed = to === "failed";
  ledRetried = to === "retried";
  ledOffset = 0;
  loadLedger().catch((e) => status(e.message, "err"));
}

function renderLedgerDash(l) {
  const dash = $("#ledDash");
  dash.hidden = !l.total && !l.day;
  if (dash.hidden) return;
  const total = allTokens(l);
  const prompt = l.input + l.cache_write + l.cache_read;
  const rate = prompt ? l.cache_read / prompt : 0;
  const strip = $("#ledKpi");
  strip.replaceChildren();
  const block = (label, value, sub, cls, title) => {
    const b = el("div", "blk");
    if (title) b.title = title;
    // a value in parts (an array) keeps each whole, wrapping between them
    const v = el("span", "v" + (cls ? " " + cls : "") + (Array.isArray(value) ? " parts" : ""), Array.isArray(value) ? undefined : value);
    // (a space between two stays out of them: one leading a part is dropped)
    if (Array.isArray(value)) value.forEach((x, i) => v.append(...(i && /^\s/.test(x) ? [" "] : []), el("span", "", x.trim())));
    b.append(el("span", "k", t(label)), v);
    if (sub) b.append(sub);
    strip.append(b);
    return b;
  };
  const line = (...parts) => { const s = el("span", "sub"); s.append(...parts); return s; };
  // a line holding a count's button wraps rather than cut the button off
  // (German's "nach erneutem Versuch beantwortet" runs past a narrow card)
  const jumps = (...parts) => { const s = line(...parts); s.classList.add("jumps"); return s; };
  // its parts kept whole, the cache's going under in a card too narrow for
  // all three rather than cut (#2378: 缓存 879… in a wide window's five cards)
  const tokParts = line(...usageParts([t("{a} in · {b} out", { a: fmtN(l.input), b: fmtN(l.output) }), t("{a} cache", { a: fmtN(l.cache_read + l.cache_write) })]));
  tokParts.classList.add("parts");
  block("Tokens", fmtN(total), tokParts, "", ledNum(total));
  // a request is counted once, by how it ended: one a later try answered
  // is not a failure, and is counted beside them as answered on a retry
  // (小一 on Discord). Each count is a button for its requests in the list.
  const jump = (text, to, title) => {
    const b = el("button", "led-jump" + (to === "failed" ? " bad" : "") + (ledStatusOf() === to ? " on" : ""), text);
    b.type = "button";
    b.title = title;
    b.setAttribute("aria-pressed", ledStatusOf() === to ? "true" : "false");
    b.onclick = () => ledShowStatus(ledStatusOf() === to ? "" : to);
    return b;
  };
  block("Requests", ledNum(l.calls), l.errors
    ? jumps(jump(t("{n} failed", { n: ledNum(l.errors) }), "failed", t("Show only the failed requests")), " · " + t("{p} succeeded", { p: (100 * (1 - l.errors / l.calls)).toFixed(1) + "%" }))
    : line(t("none failed")));
  if (l.retried) strip.lastChild.append(jumps(jump(t("{n} answered on a retry", { n: ledNum(l.retried) }), "retried", t("Show only the requests answered on a retry"))));
  const c = fmtCost(l);
  block("Cost", c ? "≈" + c : "—", line(l.unpriced ? t(l.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: l.unpriced }) : t("effective prices")), c ? "cost" : "");
  const hit = block("Cache hit rate", (100 * rate).toFixed(1) + "%", null, "", t("The share of the prompt read from the cache"));
  const meter = el("div", "meter"), fill = el("i");
  fill.style.width = (100 * rate).toFixed(1) + "%";
  meter.append(fill);
  hit.append(meter);
  // how fast the streamed replies wrote, and how long they took to begin (#860):
  // the figure of more replies, its count beside it; the other under it; the
  // wait for the first token on a line of its own, with how many it is of
  // (Fallmonkey on Discord)
  const gs = ledGroupSpeed(l);
  const ttft = ledTTFT(l) ? t("first token in {ms} on average", { ms: ledTook(ledTTFT(l)) }) + " · " + t(l.timed === 1 ? "{k} timed reply" : "{k} timed replies", { k: ledNum(l.timed) }) : t("no streamed replies timed");
  const speedCard = block("Output speed", gs ? gs.mainParts : "—", gs?.other ? line(gs.other) : null,
    "", [gs?.title, t("Output tokens a second after the first, over the streamed replies") + ". " + t("A reply that reasoned counts only its answer, from its first text: the reasoning was written before the stream showed it") + ". " + t("No answer text: speed includes reasoning only when visible reasoning streamed; hidden reasoning has no speed")].filter(Boolean).join("\n"));
  speedCard.append(Object.assign(line(ttft), { className: "sub ttft" }));
  drawLedTrend();
}

function drawLedTrend() {
  const l = ledger;
  if (!l || $("#ledDash").hidden) return;
  // the two switches
  const pill = (host, key, opts, cur, pick) => {
    host.replaceChildren();
    for (const [id, name] of opts) {
      const b = el("button", "opt" + (id === cur ? " on" : ""), t(name));
      b.onclick = () => { if (id !== cur) pick(id); };
      host.append(b);
    }
    slide(host, key);
  };
  pill($("#ledMetric"), "ledMetric", LED_TREND, ledMetric, (id) => { ledMetric = id; try { localStorage.setItem("magpie.ledMetric", id); } catch {} drawLedTrend(); });
  const keyed = (l.callerKeys || []).length > 0;
  if (ledSplit === "callerKey" && !keyed) ledSplit = "model";
  pill($("#ledSplit"), "ledSplit", LED_SPLITS.filter(([id]) => id !== "callerKey" || keyed), ledSplit, (id) => { ledSplit = id; drawLedTrend(); });
  const chart = $("#ledChart"), rank = $("#ledRank");
  drawLedColumns(chart, l, ledSplit, ledMetric, false, (day) => {
    ledDay = ledDay === day ? "" : day;
    ledOffset = 0;
    loadLedger().catch((e) => status(e.message, "err"));
  });
  rank.chart = chart;
  if (!rank.rail) ledRail($("#ledRail"), rank);
  const picked = ledSplit === "provider" ? ledProvider : ledSplit === "agent" ? ledAgent : ledSplit === "callerKey" ? ledCallerKey
    : ledSplit === "modelAt" ? (ledProvider && ledModel ? ledProvider + "/" + ledModel : "") : ledModel;
  drawLedRank(rank, l, ledSplit, ledMetric, picked, (x) => {
    // a click lists only that one's requests; on the one listed, all again
    if (ledSplit === "modelAt") {
      // provider/model: both filters, the provider's id has no "/"
      const q = $("#ledQ"), cut = x.id.indexOf("/"), off = picked === x.id;
      ledProvider = off ? "" : x.id.slice(0, cut);
      ledModel = off ? "" : x.id.slice(cut + 1);
      ledQuery = ledModel; q.value = ledQuery;
    } else if (ledSplit === "provider") ledProvider = ledProvider === x.id ? "" : x.id;
    else if (ledSplit === "agent") ledAgent = ledAgent === x.id ? "" : x.id;
    else if (ledSplit === "callerKey") ledCallerKey = ledCallerKey === x.id ? "" : x.id;
    else { const q = $("#ledQ"); ledModel = ledModel === x.id ? "" : x.id; ledQuery = ledModel; q.value = ledQuery; }
    ledOffset = 0;
    loadLedger().catch((e) => status(e.message, "err"));
  }, false);
}
// ---------- the heatmap (#1369) ----------
//
// The last 53 weeks a day each, Monday on top, as a calendar of
// contributions lays them out: each day as dark as it is busy among the
// others, by tokens, requests or cost. It is of the requests the filters
// above keep, whatever the period (the server's /api/usage/heatmap, read
// from the same index as the page), its days in the gateway's time zone. A
// narrow window scrolls it sideways inside its card, the newest week in
// sight; the pointer on a day says its date and what it had.
let ledHeat = null, ledHeatWant = "", ledHeatMetric = "tokens";
try {
  const m = localStorage.getItem("magpie.ledHeatMetric");
  if (["tokens", "calls", "cost"].includes(m)) ledHeatMetric = m;
} catch {}
const LED_HEAT = [["tokens", "Tokens"], ["calls", "Requests"], ["cost", "Cost"]];
function ledHeatParams() {
  const q = new URLSearchParams(ledParams());
  q.delete("period");
  q.delete("day");
  return q.toString();
}
async function loadLedHeat() {
  const want = ledHeatParams();
  ledHeatWant = want;
  const h = await ledRead("heat", "usage/heatmap?", want).catch((e) => { if (ledAborted(e)) return ledAborted; throw e; });
  if (h === ledAborted) return;
  if (want !== ledHeatWant) return; // another filter was picked meanwhile
  if (ledHeat && ledHeat.want === want && JSON.stringify(ledHeat.data) === JSON.stringify(h)) return;
  ledHeat = { want, data: h };
  if (view === "usage" && usageTab === "requests") drawLedHeat();
}
const ledHeatDate = (s) => { const [y, m, d] = String(s).split("-").map(Number); return new Date(y, m - 1, d); };
function ledHeatTip(x) {
  const loc = intlLang();
  const tip = el("div");
  tip.append(el("b", "", x.day.toLocaleDateString(loc, { year: "numeric", month: "short", day: "numeric", weekday: "short" })));
  if (!x.calls) {
    tip.append(el("span", "", t("No requests")));
    return tip;
  }
  const parts = [t("{n} tokens", { n: fmtN(x.tokens) }), t(x.calls === 1 ? "{n} request" : "{n} requests", { n: ledNum(x.calls) })];
  const c = fmtCost(x);
  if (c) parts.push("≈" + c);
  const order = { tokens: 0, calls: 1, cost: 2 }[ledHeatMetric];
  if (order && parts[order]) parts.unshift(...parts.splice(order, 1)); // the metric shown first
  tip.append(el("span", "", parts.join(" · ")));
  return tip;
}
function drawLedHeat() {
  const box = $("#ledHeat"), h = ledHeat?.data;
  const at = new Map((h?.days || []).map((d) => [d.date, d]));
  box.hidden = !h?.from || !h?.to || ![...at.values()].some((d) => d.calls > 0);
  if (box.hidden) return;
  const first = ledHeatDate(h.from), last = ledHeatDate(h.to);
  const days = [];
  for (const d = new Date(first); d <= last; d.setDate(d.getDate() + 1)) {
    const x = at.get(sessISO(d)) || { calls: 0, tokens: 0, cost: 0 };
    days.push({ ...x, day: new Date(d) });
  }
  const value = (x) => ledHeatMetric === "cost" ? x.cost : ledHeatMetric === "calls" ? x.calls : x.tokens;
  const lv = sessLevels(days.map(value));
  const seg = $("#ledHeatMetric");
  seg.replaceChildren();
  for (const [id, name] of LED_HEAT) {
    const b = el("button", "opt" + (id === ledHeatMetric ? " on" : ""), t(name));
    b.type = "button";
    b.onclick = () => {
      if (id === ledHeatMetric) return;
      ledHeatMetric = id;
      try { localStorage.setItem("magpie.ledHeatMetric", id); } catch {}
      drawLedHeat();
    };
    seg.append(b);
  }
  slide(seg, "ledHeatMetric");
  const scroll = $("#ledHeatScroll");
  // the same days in the same language keep their cells, coloured again, so
  // a refresh moves nothing and the scroll stays where the reader left it
  const shape = [h.from, h.to, locale].join("|");
  let grid = scroll.querySelector(".heat-grid");
  if (!grid || grid.dataset.shape !== shape) {
    grid = el("div", "heat-grid");
    grid.dataset.shape = shape;
    grid.setAttribute("role", "img");
    const names = sessWeekdays();
    // every row has its place at the edge, named every other day
    for (let i = 0; i < 7; i++) {
      const l = el("span", "wd", i % 2 ? "" : names[i]);
      l.style.gridArea = `${i + 2} / 1`;
      grid.append(l);
    }
    const loc = intlLang();
    const weeks = Math.ceil(days.length / 7), labels = [];
    let month = -1;
    days.forEach((x, i) => {
      const w = Math.floor(i / 7), wd = i % 7;
      if (wd === 0 && x.day.getMonth() !== month) {
        month = x.day.getMonth();
        // a month mostly gone in the first week is left unnamed, so as not
        // to crowd out the next one's name, and so is one begun in the last
        // week, with no room for its name
        if ((!labels.length || w - labels.at(-1).w >= 3) && !(w === 0 && x.day.getDate() > 14) && w < weeks - 1) {
          const m = el("span", "mo", x.day.toLocaleDateString(loc, { month: "short" }));
          labels.push({ m, w });
          grid.append(m);
        }
      }
      const c = el("i");
      c.dataset.date = sessISO(x.day);
      c.style.gridArea = `${wd + 2} / ${w + 2}`;
      grid.append(c);
    });
    // a month's name spans its weeks up to the next name, cut there, so a
    // long one never widens the grid past its last week
    labels.forEach(({ m, w }, i) => { m.style.gridArea = `1 / ${w + 2} / 2 / ${(labels[i + 1]?.w ?? weeks) + 2}`; });
    grid.onpointerover = (e) => { if (e.target.dataset?.date) ledHeatShow(e.target); };
    grid.onpointerleave = () => { $("#ledHeatTip").hidden = true; };
    scroll.replaceChildren(grid);
    if (!scroll.onscroll) {
      // the newest week in sight, by the card's own scroll, never the page's:
      // kept there as the card is laid out or resized, until the reader
      // scrolls it back, and again once they scroll to the end
      scroll.onscroll = () => {
        $("#ledHeatTip").hidden = true;
        // its own scroll to the end, seen after the card narrowed, is not the reader's
        if (scroll.clientWidth && scroll.scrollLeft !== ledHeatSetAt) ledHeatAtEnd = scroll.scrollLeft >= scroll.scrollWidth - scroll.clientWidth - 2;
      };
    }
    ledHeatSized.disconnect();
    ledHeatSized.observe(scroll);
    ledHeatSized.observe(grid);
    ledHeatKeepEnd();
  }
  grid.setAttribute("aria-label", t("Usage heatmap"));
  const cells = grid.querySelectorAll("i");
  days.forEach((x, i) => {
    const c = cells[i];
    c.className = "l" + lv(value(x));
    c.day = x;
  });
  const legend = box.querySelector(".heat-foot");
  legend.replaceChildren(sessLegend());
  const tip = $("#ledHeatTip");
  if (!tip.hidden && tip.cell?.isConnected) ledHeatShow(tip.cell);
  else tip.hidden = true;
}
let ledHeatAtEnd = true, ledHeatSetAt = -1;
const ledHeatSized = new ResizeObserver(() => ledHeatKeepEnd());
function ledHeatKeepEnd() {
  const scroll = $("#ledHeatScroll");
  if (!ledHeatAtEnd || !scroll.clientWidth || scroll.scrollWidth <= scroll.clientWidth) return;
  scroll.scrollLeft = scroll.scrollWidth;
  ledHeatSetAt = scroll.scrollLeft;
}
// the tip over a day, above it, kept inside the card
function ledHeatShow(cell) {
  const box = $("#ledHeat"), tip = $("#ledHeatTip");
  if (!cell.day) return;
  tip.replaceChildren(...ledHeatTip(cell.day).children); // the date over the figures
  tip.cell = cell;
  tip.hidden = false;
  const b = box.getBoundingClientRect(), c = cell.getBoundingClientRect();
  const w = tip.offsetWidth, hgt = tip.offsetHeight;
  const left = Math.max(6, Math.min(b.width - w - 6, c.left - b.left + c.width / 2 - w / 2));
  let top = c.top - b.top - hgt - 6;
  if (top < 4) top = c.bottom - b.top + 6;
  tip.style.left = left + "px";
  tip.style.top = top + "px";
}
// the table's scrollbar, kept in view at the window's foot (#799): as wide
// as the table can go sideways, and moving it as the table moves it
function ledHScroll() {
  const wrap = $("#ledWrap"), bar = $("#ledHScroll");
  const over = !!wrap.querySelector("table") && wrap.scrollWidth > wrap.clientWidth + 1;
  bar.hidden = !over;
  if (!over) return;
  bar.firstElementChild.style.width = wrap.scrollWidth - wrap.clientWidth + bar.clientWidth + "px";
  bar.scrollLeft = wrap.scrollLeft;
}
$("#ledWrap").addEventListener("scroll", () => { const bar = $("#ledHScroll"); if (bar.scrollLeft !== $("#ledWrap").scrollLeft) bar.scrollLeft = $("#ledWrap").scrollLeft; }, { passive: true });
$("#ledHScroll").addEventListener("scroll", () => { const wrap = $("#ledWrap"); if (wrap.scrollLeft !== $("#ledHScroll").scrollLeft) wrap.scrollLeft = $("#ledHScroll").scrollLeft; }, { passive: true });
// a window that changes size redraws the chart at its new width
// a new width fits the table again, on the next frame, and so do the
// details' width (--ledw) and the scrollbar: each changes the table's or the
// wrap's height, which this would be told of in its own call, and WebKit
// reports that as a ResizeObserver loop (number-units' page error)
let ledFitW = 0, ledFitFrame = 0;
new ResizeObserver(() => {
  if (ledFitFrame) return;
  ledFitFrame = requestAnimationFrame(() => {
    ledFitFrame = 0;
    const wrap = $("#ledWrap");
    wrap.style.setProperty("--ledw", wrap.clientWidth + "px");
    if (wrap.clientWidth !== ledFitW) { ledFitW = wrap.clientWidth; ledFit(); }
    ledHScroll();
  });
}).observe($("#ledWrap"));
// a table that grows past the window when its width didn't change (a font
// arriving, a redraw while out of sight) leaves its columns out then too;
// and its scrollbar goes once a table left without them has come to fit,
// rather than staying under a table with nothing to scroll
let ledGrowFrame = 0;
const ledGrow = new ResizeObserver(() => {
  if (ledGrowFrame) return;
  ledGrowFrame = requestAnimationFrame(() => {
    ledGrowFrame = 0;
    const wrap = $("#ledWrap"), table = wrap.querySelector("table.led");
    if (table && !table.classList.contains("tighter") && wrap.clientWidth && wrap.scrollWidth > wrap.clientWidth + 1) ledFit();
    ledHScroll();
  });
});
new ResizeObserver(() => {
  if (!ledger || usageTab !== "requests" || view !== "usage" || $("#ledDash").hidden) return;
  drawLedColumns($("#ledChart"), ledger, ledSplit, ledMetric, false);
  ledRankEdges($("#ledRank"));
}).observe($("#ledChart"));

// the third: a column a narrow window leaves out, its value in the row's
// details (#860), 2 for one left out only when that isn't enough; the
// fourth, the header's tooltip
const LED_COLS = [
  ["Time"], ["Agent"], ["Requested"], ["Provider · account"], ["Sent", "", true], ["Served"], ["Effort", "", true],
  ["In", "n"], ["Out", "n"], ["Cache write", "n", true], ["Cache read", "n", true], ["Cost", "n"], ["TTFT", "n", 2, "Time to first token"], ["Duration", "n"], ["Speed", "n"], ["Status"],
];

// the tokens a reply was seen to write and the ms it took, as
// usage.DecodeOf and routing/client.js's decodeOf tell them: one that reasoned
// counts its answer from its first text, its reasoning written before
// the stream showed any (tony on Discord); null when it tells no speed.
// A reply held back and let go in a burst at its end tells none (John on
// Discord): its content came in under a quarter of its window (flow_ms),
// or its answer at over 20 times the pace its reasoning came before it
const ledDecode = (r) => {
  if (ledFailed_(r) || r.reasoning_hidden && !(r.first_text_ms > 0) || !(r.ttft_ms > 0)) return null;
  const think = r.reasoning > 0, visible = think && !(r.first_text_ms > 0) && r.first_reasoning_ms >= r.ttft_ms && r.flow_ms > 0 && r.reasoning <= r.out;
  const n = think && !visible ? r.out - r.reasoning : r.out, from = visible ? r.first_reasoning_ms : think ? r.first_text_ms : r.ttft_ms;
  const w = r.ms - from;
  if (!(n > 0 && from > 0 && w >= 100 && n * 1000 <= 10000 * w)) return null;
  if (r.flow_ms > 0 && r.flow_ms * 4 < w) return null;
  if (think && r.first_text_ms > 0 && n * (r.first_text_ms - r.ttft_ms) > 20 * r.reasoning * w) return null;
  if (ledCodexTurn(r) && (w < LED_CODEX_WINDOW_MS || n < LED_CODEX_BODY)) return null;
  return { n, w };
};
// a Codex turn of one call read from its session file, its first token
// known: its window after the first token can be one network push, not the
// writing (Fallmonkey on Discord: a first token at 6.8 s, the rest 0.2 s
// later), so it tells a speed only over a second or more and 100 tokens or
// more (usage.CodexWindowMs, CodexBodyTokens); else ledFloor_ tells the
// least it wrote at over the whole turn
const LED_CODEX_WINDOW_MS = 1000, LED_CODEX_BODY = 100;
const ledCodexTurn = (r) => r.source === "log" && r.agent === "codex" && r.ttft_ms > 0;
// how fast a reply wrote, in tokens a second: 0 when it can't tell
const ledRowSpeed = (r) => { const d = ledDecode(r); return d ? d.n / (d.w / 1000) : 0; };
// the least a reply read from a session file wrote at, in tokens a second:
// the file has no first token, only when the line that asked for it and its
// last line were written (Claude Code's are each block's end), so its tokens
// over that whole time, the wait for the first one in it, are a floor the
// speed after the first token can't be under. Against Claude Code's own
// stream it read 2–5% under for a reply of 1,400 tokens or more, 11–33% for
// a shorter one or one asked with a tool's result, so only a reply of 1,000
// or more is told. 0 when it tells none, and a call magpie timed tells its
// own (ledDecode). Only the row's: the period's speed counts timed calls.
// Claude Code's, Claude Desktop's and Codex's files only (usage.FloorAgents):
// a call's time is from what asked for it, never from inside it. OpenCode's
// isn't checked against real sessions, and tells none.
// A Codex turn of one call that reasoned has its first token but not its
// first text (Fallmonkey on Discord): its answer, its output less the
// reasoning, all came after the first token, so the answer over the time
// after it is a floor of the answer's speed, when that time is a second or
// more and the answer 100 tokens or more, as a measured one is. A Codex turn
// of one call that tells neither tells its output over the whole turn, the
// wait in it, by the same 1,000-token rule as any whole-time floor (Fallmonkey
// on Discord: 11 tokens in 2.75 s read ≥4 tok/s). ledFloor_ says the tokens,
// the ms, and which it is (answer, turn).
const LED_FLOOR_AGENTS = new Set(["claude", "claude-desktop", "codex"]);
const ledFloor_ = (r) => {
  if (r.source !== "log" || !LED_FLOOR_AGENTS.has(r.agent) || ledFailed_(r) || ledRowSpeed(r)) return null;
  if (ledCodexTurn(r)) {
    const n = r.out - (r.reasoning || 0), w = r.ms - r.ttft_ms;
    if (r.reasoning > 0 && n >= LED_CODEX_BODY && w >= LED_CODEX_WINDOW_MS && n * 1000 <= 10000 * w) return { n, w, answer: true };
    return r.out >= 1000 && r.ms >= 1000 && r.out * 1000 <= 10000 * r.ms ? { n: r.out, w: r.ms, turn: true } : null;
  }
  return r.out >= 1000 && r.ms >= 1000 && r.out * 1000 <= 10000 * r.ms ? { n: r.out, w: r.ms } : null;
};
const ledFloorSpeed = (r) => { const f = ledFloor_(r); return f ? f.n / (f.w / 1000) : 0; };

// the table as wide as the window, when leaving out what the row's details
// say anyway makes it so: else it scrolls sideways (#799)
function ledFit() {
  const wrap = $("#ledWrap"), table = wrap.querySelector("table.led");
  let hint = $("#ledTight");
  if (!table) { if (hint) hint.hidden = true; return; }
  table.classList.remove("tight", "tight2", "tighter");
  const over = () => wrap.scrollWidth > wrap.clientWidth + 1;
  const tight = over();
  table.classList.toggle("tight", tight);
  // still too wide (#860: a Windows window at 175%, its wider fonts): the
  // price reference line goes too, in the cost's tooltip and the details
  // as well, and names are cut shorter, whole in their tooltips
  // still too wide: the first token's column goes (#2522), and only
  // then what follows
  if (tight && over()) table.classList.add("tight2");
  if (tight && over()) table.classList.add("tighter");
  if (!hint) {
    hint = el("span", "led-tight");
    hint.id = "ledTight";
    $("#ledSum").after(hint);
  }
  hint.textContent = hint.title = table.classList.contains("tight2") ? t("Sent, Effort, the first token and the cache are in each row's details") : t("Sent, Effort and the cache are in each row's details");
  hint.hidden = !tight;
}

function renderLedger() {
  const l = ledger;
  const callers = l.callerKeys || [];
  const providers = l.providers || [];
  const missingAgent = ledAgent && !l.agents.some((a) => a.id === ledAgent);
  const missingProvider = ledProvider && !providers.some((p) => p.id === ledProvider);
  const accounts = l.accounts || [];
  const missingAccount = ledAccount && !accounts.some((a) => a.id === ledAccount);
  // "none": the calls no gateway key made, a choice beside the keys when
  // there are such calls, so one key's are told from the rest (#2521)
  const noKey = callers.length > 0 && (ledCallerKey === "none" || (l.by?.callerKey || []).some((s) => s.id === "none"));
  const missingCaller = ledCallerKey && ledCallerKey !== "none" && !callers.some((k) => k.id === ledCallerKey);
  const computers = computerOpts(l.computers);
  const missingComputer = ledComputer && !computers.some((c) => c.v === ledComputer);
  if (missingAgent || missingProvider || missingAccount || missingCaller || missingComputer) {
    if (missingComputer) ledComputer = "";
    if (missingAccount) ledAccount = "";
    if (missingAgent) ledAgent = "";
    if (missingProvider) ledProvider = "";
    if (missingCaller) ledCallerKey = "";
    ledOffset = 0;
    // Reload the rows too: this response still belongs to the missing filters.
    loadLedger().catch((e) => status(e.message, "err"));
    return;
  }
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderPeriod();
  renderArchive();

  costHead(fmtCost(l), l.unpriced ? unpricedCalls(l.unpriced) : "", unpricedCallsWhy(l.unpriced));

  // the filters: choices seen in the period, and failures alone
  sessPick($("#ledPurpose"), "All purposes", ledPurpose, purposeOptions(l.purposes || [], ledPurpose), "Purpose", (v) => { ledPurpose = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  $("#ledPurpose").title = t("Purpose");
  sessPick($("#ledComputer"), "All computers", ledComputer, computers, "Computer", (v) => { ledComputer = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledAgent"), "All agents", ledAgent, l.agents.map((a) => ({ v: a.id, name: a.name, note: "" })), "Agent", (v) => { ledAgent = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledProvider"), "All providers", ledProvider, providers.map((p) => ({ v: p.id, name: t(p.name), note: "" })), "Provider", (v) => { ledProvider = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledAccount"), "All accounts", ledAccount, accounts.map((a) => ({ v: a.id, name: a.id, note: (a.providers || []).map((p) => t(p)).join(" · ") })), "Account", (v) => { ledAccount = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledKey"), "All gateway keys", ledCallerKey, callers.map((k) => ({
    v: k.id, name: k.name, note: "",
  })).concat(noKey ? [{ v: "none", name: t("No gateway key"), note: "" }] : []), "Gateway keys", (v) => { ledCallerKey = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  const seg = $("#ledStatus");
  seg.replaceChildren();
  // every request, those the agent got an error for, and those answered
  // after a try that failed first
  const shown = ledStatusOf();
  for (const [on, name] of [["", "All"], ["failed", "Failed"], ["retried", "Retried"]]) {
    const b = el("button", "opt" + (on === shown ? " on" : ""), t(name));
    if (on === "retried") b.title = t("Show only the requests answered on a retry");
    b.onclick = () => {
      if (on === ledStatusOf()) return;
      for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
      slide(seg, "ledStatus");
      ledShowStatus(on);
    };
    seg.append(b);
  }
  slide(seg, "ledStatus");
  $("#ledExport").disabled = !l.total;

  const sum = [t(l.total === 1 ? "{n} request" : "{n} requests", { n: ledNum(l.total) })];
  if (l.total) {
    sum.push(t("{n} in", { n: fmtN(l.input) }), t("{n} out", { n: fmtN(l.output) }), t("{n} cache write", { n: fmtN(l.cache_write) }), t("{n} cache read", { n: fmtN(l.cache_read) }));
    if (l.errors) sum.push(t("{n} failed", { n: l.errors }));
    if (l.retried) sum.push(t("{n} answered on a retry", { n: l.retried }));
  }
  $("#ledSum").textContent = sum.join(" · ");
  // the two sources and their total, in the same block of cells as the totals
  // above them: what magpie carried, what it only read about in the agents'
  // own session files, and the two together. Each is a filter: picking one
  // lists only its requests, and the total is the way back. Its own numbers
  // are counted with the pick cleared (usage.RequestPage), so all three stay
  // switchable whichever is on.
  const via = $("#ledVia");
  const thru = l.through || { calls: 0 }, dir = l.direct || { calls: 0 }, rep = l.reported || { calls: 0 };
  if (thru.calls + dir.calls + rep.calls > 0) {
    // the total is the two sources added up, not the page's own totals: those
    // are the rows the filter keeps, so with one source picked the total cell
    // would read that one's figures under the whole period's count
    const all = {};
    for (const k of ["calls", "errors", "input", "output", "cache_read", "cache_write", "cost", "timed", "ttft_ms", "decode_ms", "decode_out"]) {
      all[k] = (thru[k] || 0) + (dir[k] || 0) + (rep[k] || 0);
    }
    const tile = (b, label, calls, tot, pick) => {
      const on = ledVia === pick;
      b.className = "blk" + (on ? " on" : "");
      b.type = "button";
      b.setAttribute("aria-pressed", on ? "true" : "false");
      b.title = pick ? t("Show only these requests") : t("Show every request again");
      const v = el("span", "v", ledNum(calls));
      // the unit, so a bare number isn't read as the tokens below it
      v.append(el("em", "", t(calls === 1 ? "request" : "requests")));
      b.replaceChildren(el("span", "k", t(label)), v,
        el("span", "sub", t("{n} tokens", { n: fmtN(allTokens(tot)) })));
      b.onclick = () => {
        const next = ledVia === pick ? "" : pick;
        if (next === ledVia) return;
        ledVia = next;
        ledOffset = 0;
        loadLedger().catch((e) => status(e.message, "err"));
      };
      return b;
    };
    // the calls apps reported themselves (#1874) are a fourth cell, once
    // there are some, or while they are the ones picked
    const n = rep.calls > 0 || ledVia === "reported" ? 4 : 3;
    // the cells are the same elements from one redraw to the next, so
    // one being pressed or focused is never swapped out from under a reader
    let cells = [...via.children];
    if (cells.length !== n) {
      cells = cells.slice(0, n);
      while (cells.length < n) cells.push(el("button"));
      via.replaceChildren(...cells);
    }
    via.style.setProperty("--vias", String(n));
    tile(cells[0], "all requests", thru.calls + dir.calls + rep.calls, all, "");
    tile(cells[1], "through magpie", thru.calls, thru, "through");
    tile(cells[2], "not through magpie", dir.calls, dir, "direct");
    if (n === 4) tile(cells[3], "reported by apps", rep.calls, rep, "reported");
    via.hidden = false;
  } else {
    via.replaceChildren();
    via.hidden = true;
  }
  const routeFilter = $("#ledRoute");
  routeFilter.hidden = !ledRoute;
  $("#ledRouteLabel").textContent = ledRouteInfo ? t("Request: {what}", { what: new Date(ledRouteInfo.time).toLocaleString(intlLang()) + " · " + ledRouteInfo.model }) : "";
  $("#ledRouteClear").title = t("Clear filter");
  $("#ledRouteClear").setAttribute("aria-label", t("Clear filter"));
  $("#ledRouteClear").onclick = () => {
    ledRoute = 0; ledRouteInfo = null;
    if (ledBeforeRoute) ({ period, ledOffset, ledPurpose = "", ledAgent, ledProvider, ledAccount = "", ledCallerKey, ledFailed, ledRetried = false, ledQuery, ledModel, ledComputer = "", ledDay = "", ledVia = "" } = ledBeforeRoute);
    ledBeforeRoute = null;
    $("#ledQ").value = ledQuery;
    loadLedger().catch((e) => status(e.message, "err"));
  };
  renderLedgerDash(l);
  drawLedHeat();

  const wrap = $("#ledWrap");
  const pager = $("#ledPager");
  if (!l.total) {
    wrap.classList.add("none");
    const filtered = ledPurpose || ledDay || ledRoute || ledAgent || ledProvider || ledAccount || ledComputer || ledCallerKey || ledModel || ledVia || ledFailed || ledRetried || ledQuery.trim();
    const none = noCallsIn(period);
    wrap.replaceChildren(el("div", "led-none", filtered ? t("No requests match these filters.") : t(none)));
    ledFit();
    ledHScroll();
    pager.hidden = true;
    $("#ledNote").textContent = "";
    return;
  }
  wrap.classList.remove("none");
  const table = el("table", "led");
  const head = el("tr");
  const optCls = (opt) => opt === 2 ? " lc-opt2" : opt ? " lc-opt" : "";
  for (const [name, cls, opt, tip] of LED_COLS) {
    const th = el("th", (cls || "") + optCls(opt), t(name));
    if (tip) th.title = t(tip);
    head.append(th);
  }
  table.append(el("thead"), el("tbody"));
  table.tHead.append(head);
  for (const r of l.rows) {
    const bad = ledFailed_(r);
    const key = ledKey(r);
    const tr = el("tr", "led-row" + (bad ? " bad" : "") + (ledOpen.has(key) ? " open" : ""));
    tr.tabIndex = 0;
    tr.setAttribute("aria-expanded", ledOpen.has(key) ? "true" : "false");
    const td = (child, cls, title) => {
      const c = el("td", (cls || "") + optCls(LED_COLS[tr.childElementCount]?.[2]));
      if (typeof child === "string") c.textContent = child; else c.append(child);
      if (title) c.title = title;
      tr.append(c);
      return c;
    };
    const routed = r.route_id && !featuresOff.has("routing"); // the Routing page there to open it on
    const when = routed ? el("button", "text led-route-link", ledTime(r.t)) : ledTime(r.t);
    if (routed) {
      when.title = t("View routing");
      // the link opens the route, not the row's details as well
      when.onclick = (e) => { e.stopPropagation(); window.openRoute(r.route_id, r.t).catch((err) => status(err.message, "err")); };
    }
    td(when, "when", new Date(r.t).toLocaleString(intlLang()));
    const who = el("span", "who");
    // an agent on another computer, whose magpie passed the request on
    const name = r.agentName || r.agent;
    // one made on another computer, whose usage sync brought (#542)
    const at = r.computerName ? t("{agent} · on {computer}", { agent: name, computer: r.computerName }) : name;
    who.append(icon(r.icon || "generic"), el("span", "", [r.via ? t("{agent} · via {host}", { agent: at, host: r.via }) : at, r.callerKeyLabel || r.callerKeyName].filter(Boolean).join(" · ")));
    // a request magpie made by itself, apart from the agents' own (#1839)
    if (r.agent === "magpie") who.append(ownBadge());
    td(who, "", [r.kind, r.session && t("session {id}", { id: r.session })].filter(Boolean).join(" · "));
    td(r.req || "—", "model" + (r.req ? "" : " faint"), r.req || t("Not kept for requests before this version"));
    const local = r.source === "log";
    // A session file names the provider it was set up to call, which is
    // where a call that went straight to a vendor went: nothing stood
    // between the agent and it. It is the file's own record and not a route
    // magpie saw, so the id the file carries is shown as it is — never
    // resolved to a provider's name, which would promote an unknown request
    // to an official provider — and the row keeps saying it is a local
    // session. A row whose file names none says only that.
    const where = local ? (r.session_account || r.session_provider || t("Local session")) : t(r.providerName) + (r.host ? " · " + r.host : "");
    const wc = td(el("div", "where-name", where), "where", where);
    const badges = el("div", "source-badges");
    if (local && r.session_account && r.session_official_login) {
      const badge = el("span", "src official", t("OFFICIAL"));
      badge.title = t("Official login confirmed for this account by local login metadata. This does not establish the route or authentication used for this request.");
      badges.append(badge);
    }
    let access = "";
    if (!local && r.access === "subscription") access = "Subscription";
    else if (!local && r.access === "api") access = "API";
    if (access) {
      const badge = el("span", "src access access-" + r.access, t(access));
      badge.title = t(access);
      badges.append(badge);
    }
    const proto = ledProtoBadge(r);
    if (proto) badges.append(proto);
    // the badge says where the row came from, beside a name that is not
    // that: a row whose name already says it has nothing to add
    if (local && (r.session_account || r.session_provider)) {
      const k = el("span", "src local", t("Local session"));
      k.title = r.session_provider
        ? t("Read from the agent's session file, which names the provider it was set up to call. The name shown is that record, not a route magpie saw.")
        : t("Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred.");
      badges.append(k);
    }
    // a call an app reported itself (#1874): it went straight to its vendor
    if (r.source === "reported") {
      const k = el("span", "src reported", t("Reported"));
      k.title = t("Reported by the app that made it, through POST /v1/magpie/usage/requests: the call went straight to its vendor, not through magpie.");
      badges.append(k);
    }
    if (badges.childElementCount) wc.append(badges);
    // the model sent is what the gateway sent the vendor: a session file has none
    // The log names a model, but does not capture the outbound HTTP request.
    td(r.model || "—", "model " + (r.model ? "model-value" : "faint"), r.source === "log" && r.model ? t("Model recorded in the local session log; the outbound HTTP request was not captured.") : r.model);
    td(ledServed(r), "model");
    td(r.effort || "—", r.effort ? "" : "faint");
    td(ledNum(r.in), "n");
    td(ledNum(r.out), "n", r.reasoning ? t("{n} reasoning, inside output", { n: ledNum(r.reasoning) }) : "");
    td(ledNum(r.cache_write), "n" + (r.cache_write ? "" : " faint"), cacheTTLs(r, ledNum).replace(/^ · /, ""));
    td(ledNum(r.cache_read), "n" + (r.cache_read ? "" : " faint"));
    const cost = td(r.priced ? "≈" + fmtCost({ cost: r.cost, unpriced: 0 }) : "—", "n cost" + (r.priced ? "" : " faint"), r.priced ? (r.pricing_model ? t("API price reference") + ": " + r.pricing_model : "") : t("No known price for this model"));
    if (r.priced && r.pricing_model) {
      const reference = el("div", "price-reference", t("Price reference: {model}", { model: r.pricing_model }));
      reference.title = t("Model used for the API price estimate; this is not an observed model forwarding event.");
      cost.append(reference);
    }
    // a session file has the times of its lines: a call took about from the line that
    // asked for it to its last; some have none, and are dashes, not 0 ms
    const timed = r.source === "log";
    const untimed = !Number.isFinite(r.ms) || r.ms <= 0;
    // the wait for its first token, in a column of its own to compare down
    // the list: the duration holds it and the writing after it (#2522).
    // One not streamed, or that failed before any, has none
    td(r.ttft_ms > 0 ? (timed ? "≈" : "") + ledTook(r.ttft_ms) : "—", "n ttft" + (r.ttft_ms > 0 ? "" : " faint"),
      r.ttft_ms > 0 ? [t("{n} ms", { n: ledNum(r.ttft_ms) }), timed ? t("About: told from the session file's times, a little more or less than it took") : ""].filter(Boolean).join(" · ") : "");
    const durationBand = untimed ? "" : r.ms <= 10000 ? "fast" : r.ms <= 30000 ? "slow" : "long";
    td(untimed ? "—" : (timed ? "≈" : "") + ledTook(r.ms), "n duration " + (untimed ? "faint" : "duration-" + durationBand),
      [!untimed ? t("{n} ms", { n: ledNum(r.ms) }) : "", !untimed ? t("Duration colors: ≤10 s green · 10–30 s amber · >30 s red") : "", timed && !untimed ? t("About: told from the session file's times, a little more or less than it took") : "", r.ttft_ms ? t("TTFT {ms}", { ms: ledTook(r.ttft_ms) }) : ""].filter(Boolean).join(" · "));
    // how fast it wrote once it began, as CC Switch's log has it (#860)
    const v = ledRowSpeed(r), floor = ledFloorSpeed(r);
    td(v ? t("{n} tok/s", { n: ledNum(Math.round(v)) }) : floor ? "≥" + t("{n} tok/s", { n: ledNum(Math.round(floor)) }) : "—", "n speed" + (v || floor ? "" : " faint"),
      v ? (r.reasoning > 0 && r.first_text_ms > 0 ? t("{n} answer tokens in {ms} after the first text, the {r} reasoning tokens before it left out", { n: ledNum(ledDecode(r).n), ms: ledTook(ledDecode(r).w), r: ledNum(r.reasoning) })
        : t("{n} output tokens in {ms} after the first", { n: ledNum(ledDecode(r).n), ms: ledTook(ledDecode(r).w) })) + " · " + t("TTFT {ms}", { ms: ledTook(r.ttft_ms) })
        : floor && ledFloor_(r).answer ? t("At least: {n} answer tokens in {ms} after the first token, the {r} reasoning tokens left out. The session file has no first text, so reasoning after the first token is counted too", { n: ledNum(ledFloor_(r).n), ms: ledTook(ledFloor_(r).w), r: ledNum(r.reasoning) }) + " · " + t("TTFT {ms}", { ms: ledTook(r.ttft_ms) })
        : floor && ledFloor_(r).turn ? t("At least: {n} output tokens in {ms}, the whole turn. What came after the first token ({after}) is too little to time a speed by: under a second or under 100 tokens, so the wait for it is counted too", { n: ledNum(r.out), ms: ledTook(r.ms), after: ledTook(r.ms - r.ttft_ms) }) + " · " + t("TTFT {ms}", { ms: ledTook(r.ttft_ms) })
        : floor ? t("At least: {n} output tokens in {ms}, from the line that asked for it to its last. The session file has no first token, so the wait for it is counted too", { n: ledNum(r.out), ms: ledTook(r.ms) })
        : r.ttft_ms || untimed || ledFailed_(r) ? "" : timed ? t("The session file has no first token to time a speed from") : t("Not streamed: no first token to time a speed from"));
    const st = el("span", "st");
    // a status when the gateway logged the call; a session file has none,
    // and says only whether it went well
    let word = r.status ? String(r.status) : r.source === "log" ? (bad ? r.err_type || t("Failed") : t("Succeeded")) : "—";
    if (r.status && bad && r.err_type) word += " · " + r.err_type;
    if (r.handed_on) word += " · " + t("handed on");
    else if (r.tries > 1) word += " · " + t("{n} tries", { n: r.tries });
    st.append(el("i", "dot"), document.createTextNode(word));
    td(st, "", [r.handed_on && t("This try failed and the request was handed on to the next: the request is counted by how it ended, not by this try"),
      bad ? (r.err || t("Failed: the agent was answered {status}", { status: r.status })) : ""].filter(Boolean).join("\n"));
    const detail = ledOpen.has(key) ? ledDetail(r, LED_COLS.length) : null;
    const toggle = () => {
      if (ledOpen.delete(key)) { tr.classList.remove("open"); tr.setAttribute("aria-expanded", "false"); tr.nextElementSibling?.classList.contains("led-detail") && tr.nextElementSibling.remove(); return; }
      ledOpen.add(key);
      tr.classList.add("open");
      tr.setAttribute("aria-expanded", "true");
      tr.after(ledDetail(r, LED_COLS.length));
    };
    // a click that ends a text selection is the reader copying, not asking
    tr.onclick = () => { if (!String(getSelection() || "")) toggle(); };
    tr.onkeydown = (e) => { if (e.target === tr && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); toggle(); } };
    table.tBodies[0].append(tr);
    if (detail) table.tBodies[0].append(detail);
  }
  wrap.replaceChildren(table);
  wrap.style.setProperty("--ledw", wrap.clientWidth + "px");
  ledFit();
  ledHScroll();
  ledGrow.disconnect();
  ledGrow.observe(table);

  // a page at a time: the newest first
  pager.hidden = l.total <= LED_PAGE;
  pager.replaceChildren();
  if (!pager.hidden) {
    const prev = el("button", "text", t("Newer"));
    const next = el("button", "text", t("Older"));
    prev.disabled = l.offset <= 0;
    next.disabled = l.offset + l.rows.length >= l.total;
    prev.onclick = () => { ledOffset = Math.max(0, l.offset - LED_PAGE); loadLedger().catch((e) => status(e.message, "err")); };
    next.onclick = () => { ledOffset = l.offset + LED_PAGE; loadLedger().catch((e) => status(e.message, "err")); };
    pager.append(prev, el("span", "", t("{a}–{b} of {n}", { a: ledNum(l.offset + 1), b: ledNum(l.offset + l.rows.length), n: ledNum(l.total) })), next);
  }
  $("#ledNote").textContent = t("Each request's tokens as its provider reported them; the cost estimated using effective model prices, including custom prices. Export CSV saves every page.");
}

{
  let typing = 0;
  $("#ledQ").oninput = (e) => {
    ledQuery = e.target.value;
    ledModel = "";
    clearTimeout(typing);
    typing = setTimeout(() => { ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }, 250);
  };
  $("#ledQ").onkeydown = (e) => {
    if (e.key === "Escape" && e.target.value) { e.stopPropagation(); e.target.value = ""; ledQuery = ""; ledModel = ""; ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }
  };
  $("#ledExport").onclick = async () => {
    const b = $("#ledExport");
    // in a browser the file comes to it; in the app it goes to Downloads
    if (web) {
      const a = el("a");
      a.href = "/api/usage/requests.csv?" + ledParams();
      a.download = "";
      a.click();
      return;
    }
    b.classList.add("busy");
    try {
      const r = await api("usage/requests/export?" + ledParams(), {});
      status(t(r.rows === 1 ? "Saved {n} request to {path}" : "Saved {n} requests to {path}", { n: ledNum(r.rows), path: r.path }), "ok");
    } catch (e) {
      status(e.message, "err");
    } finally {
      b.classList.remove("busy");
    }
  };
}

// ---------- sessions ----------
//
// The agents' own sessions, read from their session files: what each cost,
// and the command that picks it up again. A segment of the Usage page.

const USAGE_TABS = [["usage", "Overview"], ["requests", "Requests"], ["sessions", "Sessions"], ["context", "Context"]];
let usageTab = "usage";
try { const k = localStorage.getItem("magpie.usageTab"); if (USAGE_TABS.some(([id]) => id === k)) usageTab = k; } catch {}
let sessAgent = "all";
let sessQuery = "";
// The list is drawn in pages: every session is read (so the count beside it
// and the list agree), but a filter click or a keystroke in Search draws
// SESS_PAGE rows and no more, and "Show N more" appends the next page. A
// history of thousands rebuilt whole on every keystroke took ~300ms a
// keystroke on a fast Mac (yetone, #1019). sessShown resets to one page
// whenever the filters change, so what is drawn never grows with the history.
const SESS_PAGE = 200;
let sessShown = SESS_PAGE;
// What the last draw put in the list, for the note under it to say: drawn is
// the rows drawn and total the rows the filters kept — the same list, so a
// search that matches one session says "1 session", never "1 of 2600" (the
// note is about the list, not about every session on the computer).
let sessDrawn = null, sessTotal = null;
// what the drawn list was filtered by, so a redraw from the same filters
// keeps the pages opened and a changed one starts again at the first
let sessListKey = null;
// The totals and the chart are every session's, by day, over a range; the
// list is the latest sessions within it. [id, name, days (0: all)]
const SESS_RANGES = [["today", "Today", 1], ["7d", "7 days", 7], ["30d", "30 days", 30], ["90d", "90 days", 90], ["all", "All", 0]];
let sessRange = "30d";
// what the activity chart counts each day
const SESS_METRICS = [["tokens", "Tokens"], ["output", "Output tokens"], ["messages", "Messages"], ["sessions", "Sessions"], ["cost", "Cost"], ["active", "Active"]];
let sessMetric = "tokens";
// what the top sessions are the top by
const SESS_TOPS = [["tokens", "Tokens"], ["cost", "Cost"], ["active", "Active"]];
let sessTopBy = "tokens";
try {
  const r = localStorage.getItem("magpie.sessRange");
  if (SESS_RANGES.some(([id]) => id === r)) sessRange = r;
  const m = localStorage.getItem("magpie.sessMetric");
  if (SESS_METRICS.some(([id]) => id === m)) sessMetric = m;
  const b = localStorage.getItem("magpie.sessTop");
  if (SESS_TOPS.some(([id]) => id === b)) sessTopBy = b;
} catch {}
let sessStats = null; // { from, to, days: [{ date, usage, active }], agents } for sessRange
let sessModel = ""; // "" for every model
let sessFolder = ""; // "" for every folder
// the sessions of the range summed up under the filters, by the server:
// { count, median, p90, days, top: { tokens, cost, active } }, and the
// query it answers
let sessOver = null;
let sessOverAt = "";
let sessOverLoading = "";
let sessTopOpen = ""; // the top session opened to its details
let sessListOpen = ""; // with the Sessions page switched off, the listed session opened here to its details (#2311)
const sessFull = new Map(); // a top session's key → the session read whole, or "…" while it is read

function renderUsageTab() {
  const seg = $("#usageTab");
  seg.replaceChildren();
  for (const [id, name] of USAGE_TABS) {
    const b = el("button", "opt" + (id === usageTab ? " on" : ""), t(name));
    b.onclick = () => {
      if (id === usageTab) return;
      usageTab = id;
      try { localStorage.setItem("magpie.usageTab", id); } catch {}
      $("#usageCost").replaceChildren();
      // to the Overview is to the allowances (#1588)
      loadUsage(id === "usage").catch((e) => status(e.message, "err"));
    };
    seg.append(b);
  }
  slide(seg, "usageTab");
  const on = usageTab === "sessions";
  // the Context tab has its own range, in its pane
  $("#period").hidden = on || usageTab === "context";
  $("#sessRange").hidden = !on;
  $("#ctxRange").hidden = usageTab !== "context";
  $("#usagePane").hidden = usageTab !== "usage";
  $("#ledgerPane").hidden = usageTab !== "requests";
  $("#sessionsPane").hidden = !on;
  $("#contextPane").hidden = usageTab !== "context";
}

const sessDays = () => SESS_RANGES.find(([id]) => id === sessRange)[2];
async function loadSessions() {
  const first = !sessions || !sessStats;
  if (first) renderSessionsLoading();
  const range = sessRange, q = sessOverQ();
  const stop = first ? sessWatchIndex() : () => {};
  let s, st, o;
  try {
    [s, st, o] = await Promise.all([api("sessions"), api("sessions/stats?days=" + sessDays()), sessOverview(q)]);
  } finally { stop(); }
  if (range !== sessRange) return; // another range was picked meanwhile; its load draws
  const fresh = q === sessOverQ(); // no filter changed meanwhile
  if (sessions && sessStats && JSON.stringify(s) === JSON.stringify(sessions) && JSON.stringify(st) === JSON.stringify(sessStats) &&
    (!fresh || JSON.stringify(o) === JSON.stringify(sessOver))) return;
  sessions = s;
  sessStats = st;
  if (fresh) { sessOver = o; sessOverAt = q; }
  if (view === "usage" && usageTab === "sessions") renderSessions();
}

// sessOverview reads a query's overview, once while a read of it is under
// way: the page's reload, a filter's and a range's each asked for their
// own, and over a long history each read is a long one (#2278)
const sessOverReads = new Map();
function sessOverview(q) {
  let read = sessOverReads.get(q);
  if (!read) {
    read = api("sessions/overview?" + q).finally(() => sessOverReads.delete(q));
    sessOverReads.set(q, read);
  }
  return read;
}

// the overview's query: the range and the filters
const sessOverQ = () => new URLSearchParams({ days: sessDays(), agent: sessAgent === "all" ? "" : sessAgent, model: sessModel, cwd: sessFolder }).toString();
// loadSessOverview reads the overview again when a filter changes, and draws
// the page once it is in; one read at a time for a query
function loadSessOverview() {
  const q = sessOverQ();
  if (sessOverLoading === q) return;
  sessOverLoading = q;
  const done = () => { if (sessOverLoading === q) sessOverLoading = ""; };
  sessOverview(q).then((o) => {
    done();
    if (q !== sessOverQ()) return;
    sessOver = o;
    sessOverAt = q;
    if (view === "usage" && usageTab === "sessions" && sessions && sessStats) renderSessions();
  }, (e) => { done(); status(e.message, "err"); });
}

function renderSessionsLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  renderSessRange(true);
  $("#sessAgent").replaceChildren();
  $("#sessModel").hidden = $("#sessFolder").hidden = true;
  $("#sessChart").hidden = true;
  $("#sessGrid").hidden = true;
  $("#sessListHead").hidden = true;
  const stats = $("#sessStats");
  stats.classList.remove("empty");
  stats.classList.add("six");
  stats.replaceChildren();
  for (let i = 0; i < 6; i++) {
    const tile = el("div", "kpi loading-kpi");
    tile.append(el("span", "skeleton sk-number"), el("span", "skeleton sk-label"));
    stats.append(tile);
  }
  const list = $("#sessList");
  list.hidden = false;
  list.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const r = el("div", "row sess-sk");
    r.append(el("span", "skeleton sk-title"), el("span", "skeleton sk-line short"));
    list.append(r);
  }
  $("#sessNote").textContent = t("Reading the agents' session files…");
}

const SESS_INDEX_SHOW = 700;

// sessWatchIndex asks how far the reading of the session files has got
// while the page waits on it; a read of the kept index, over in a moment,
// shows the skeleton alone, and one that takes a while the indexing show.
// It returns the stop.
function sessWatchIndex() {
  let on = true, timer = 0, shown = false, since = 0;
  const tick = async () => {
    if (!on) return;
    let p = null;
    try { p = await api("sessions/progress"); } catch {}
    if (!on) return;
    // only a real indexing run is shown: a read of the kept index, however
    // long, keeps the skeleton, and so does the catch-up read of the few
    // files the agents wrote to since (every reload has some, while an agent
    // is at work), over well before SESS_INDEX_SHOW; the show would play
    // again from nought for it
    since = p?.indexing ? since || Date.now() : 0;
    const long = since && Date.now() - since >= SESS_INDEX_SHOW;
    if (p && (long || shown) && view === "usage" && usageTab === "sessions") {
      const stats = $("#sessStats");
      let hero = stats.querySelector(".sess-indexing");
      if (!hero) {
        hero = sessIndexHero();
        stats.replaceChildren(hero);
      }
      // an earlier watch's stop may have taken the class off the hero kept
      stats.classList.add("indexing");
      shown = true;
      hero.update(p);
    }
    timer = setTimeout(tick, 350);
  };
  timer = setTimeout(tick, 150);
  return () => {
    on = false;
    clearTimeout(timer);
    if (shown) $("#sessStats").classList.remove("indexing");
  };
}

// sessIndexHero is the show put on while the session files are read: the
// words and how far it has got on the left, on the right a field of dots, the
// heatmap's cells, that light up as the files land, a wave running through
// them; a hairline along the foot. Still with reduced motion.
const SESS_TIPS = [
  "Read once, kept: after this the page opens from the index in a blink",
  "Only what changed is read again, from where it was left",
  "Every session is read on this computer; nothing leaves it",
  "The biggest files go first, so none is left running on alone",
];
function sessIndexHero() {
  const hero = el("div", "sess-indexing");
  hero.setAttribute("role", "status");
  const field = el("div", "si-field");
  const cv = el("canvas", "si-canvas");
  cv.setAttribute("aria-hidden", "true");
  field.append(cv);
  const words = el("div", "si-words");
  const head = el("div", "si-head");
  head.append(el("i", "si-spin"), el("span", "si-title", t("Indexing your sessions")));
  const pct = el("div", "si-pct");
  const num = el("b", "", "");
  pct.append(num, el("span", "", "%"));
  const line = el("div", "si-line", t("Looking for session files…"));
  const tip = el("div", "si-tip", t(SESS_TIPS[0]));
  words.append(head, pct, line, tip);
  const bar = el("div", "si-bar");
  const fill = el("i", "");
  bar.append(fill);
  hero.append(words, field, bar);

  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let frac = 0, shown = 0, known = false, tipAt = Date.now(), tipI = 0, rate = 0, last = null;
  let done = false;
  hero.update = (p) => {
    if (!(p.indexing && p.bytes > 0) && known) {
      // the run is over while the page is put together: held full, never
      // back to the unknown state or a second run from nought
      if (!done) {
        done = true;
        frac = 1;
        fill.style.width = "100%";
        hero.classList.add("done");
        line.textContent = t("Indexed · putting the page together…");
      }
      return;
    }
    if (done) return;
    known = !!p.indexing && p.bytes > 0;
    const f = known ? Math.min(1, p.read / p.bytes) : 0;
    // a steady speed, for the time left
    if (known && last && p.read > last.read) {
      const r = (p.read - last.read) / ((Date.now() - last.at) / 1000);
      rate = rate ? rate * 0.8 + r * 0.2 : r;
    }
    if (known) last = { read: p.read, at: Date.now() };
    frac = Math.max(frac, f);
    hero.classList.toggle("known", known);
    fill.style.width = known ? (frac * 100).toFixed(1) + "%" : "";
    if (known) {
      line.textContent = t("{done} of {files} files · {read} of {bytes}", { done: fmtN(p.done), files: fmtN(p.files), read: fmtBytes(p.read), bytes: fmtBytes(p.bytes) });
      const left = rate > 0 ? (p.bytes - p.read) / rate : 0;
      if (left > 3) line.textContent += " · " + t("about {t} left", { t: left < 60 ? t("{n}s", { n: Math.ceil(left) }) : t("{n} min", { n: Math.ceil(left / 60) }) });
    } else line.textContent = t("Reading the kept index…");
    if (Date.now() - tipAt > 4500) {
      tipAt = Date.now();
      tipI = (tipI + 1) % SESS_TIPS.length;
      tip.classList.remove("in");
      void tip.offsetWidth;
      tip.textContent = t(SESS_TIPS[tipI]);
      tip.classList.add("in");
    }
    if (reduced) paint(0);
  };

  // the drawing: each dot has its turn, mostly left to right, a little
  // scattered, so the edge of what is read is ragged like rain landing
  const ctx = cv.getContext("2d");
  const GAP = 14;
  let W = 0, H = 0, dpr = 1, dots = [], ink = "#1c1c21", accent = "#4f46e5", frame = 0;
  const colours = () => {
    const c = getComputedStyle(hero);
    ink = c.getPropertyValue("--fg").trim() || ink;
    accent = c.getPropertyValue("--accent").trim() || accent;
  };
  const size = () => {
    dpr = devicePixelRatio || 1;
    const r = cv.getBoundingClientRect();
    if (!r.width || !r.height) return false;
    if (r.width !== W || r.height !== H) {
      W = r.width; H = r.height;
      cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
      const cols = Math.max(1, Math.floor((W - 4) / GAP)), rows = Math.max(1, Math.floor((H - 4) / GAP));
      const ox = (W - (cols - 1) * GAP) / 2, oy = (H - (rows - 1) * GAP) / 2;
      dots = [];
      for (let j = 0; j < rows; j++) for (let i = 0; i < cols; i++) {
        const seed = Math.random();
        dots.push({ x: ox + i * GAP, y: oy + j * GAP, nx: cols > 1 ? i / (cols - 1) : 0, ny: rows > 1 ? j / (rows - 1) : 0, seed, turn: (cols > 1 ? i / (cols - 1) : 0) * 0.86 + seed * 0.14 });
      }
    }
    return true;
  };
  function paint(now) {
    if (!size()) return;
    if (frame++ % 30 === 0) colours();
    const T = now / 1000;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    shown += (frac - shown) * (reduced ? 1 : 0.06);
    // unknown: a band of light sweeps across and on again
    const scan = ((T * 0.32) % 1.4) - 0.2;
    // a faint glow where the reading is
    const gx = (known ? shown * 0.86 + 0.07 : scan) * W;
    const glow = ctx.createRadialGradient(gx, H / 2, 0, gx, H / 2, H * 0.9);
    glow.addColorStop(0, hexA(accent, reduced ? 0.06 : 0.1));
    glow.addColorStop(1, hexA(accent, 0));
    ctx.fillStyle = glow;
    ctx.fillRect(0, 0, W, H);
    for (const d of dots) {
      const wave = reduced ? 0.5 : 0.5 + 0.5 * Math.sin(d.nx * 7 + d.ny * 2.4 - T * 1.5 + d.seed * 0.8);
      let a = 0.08 + 0.05 * wave, r = 1.05 + 0.2 * wave, c = ink;
      if (known) {
        const since = shown - d.turn;
        if (since >= 0) {
          // read: resting in the accent, just landed brighter and bigger
          const fresh = Math.max(0, 1 - since / 0.07);
          c = accent;
          a = 0.4 + 0.32 * wave * (0.3 + d.seed) + 0.5 * fresh;
          r = 1.25 + 0.25 * wave + 1.1 * fresh * fresh;
        } else if (since > -0.035 && !reduced) {
          // about to land: a flicker
          const near = 1 + since / 0.035;
          if (Math.sin(T * 9 + d.seed * 40) > 0.3) { c = accent; a = 0.1 + 0.35 * near; }
        }
      } else if (!reduced) {
        const g = Math.exp(-(((d.nx - scan) / 0.07) ** 2));
        if (g > 0.02) { c = accent; a = Math.max(a, 0.9 * g * (0.55 + 0.45 * d.seed)); r += 1 * g; }
      }
      ctx.globalAlpha = Math.min(1, a);
      ctx.fillStyle = c;
      ctx.beginPath();
      ctx.arc(d.x, d.y, r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
    num.textContent = known ? String(Math.floor(shown * 100)) : "";
  }
  // it runs until the page takes it away
  const loop = (now) => {
    if (!hero.isConnected) return;
    paint(now);
    requestAnimationFrame(loop);
  };
  if (!reduced) requestAnimationFrame(loop);
  else requestAnimationFrame(() => paint(0));
  return hero;
}
// hexA is a #rrggbb colour at an alpha
function hexA(hex, a) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return `rgba(79,70,229,${a})`;
  const n = parseInt(m[1], 16);
  return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
}
const sessKey = (s) => s.agent + ":" + s.id;
const sessTokens = (s) => s.input + s.output;
// a session's cost: "—" when none of its models has a known price
function sessCost(s) {
  if (!s.models.some((m) => m.priced && (m.input || m.output || m.cache_read || m.cache_write))) return "—";
  return "≈" + fmtCost({ cost: s.cost, unpriced: s.unpriced });
}
// the range picker, in the page's head where the Overview's period is
function renderSessRange(loading) {
  const seg = $("#sessRange");
  seg.replaceChildren();
  for (const [id, name] of SESS_RANGES) {
    const b = el("button", "opt" + (id === sessRange ? " on" : ""), t(name));
    b.disabled = !!loading;
    b.onclick = () => {
      if (id === sessRange) return;
      sessRange = id;
      try { localStorage.setItem("magpie.sessRange", id); } catch {}
      sessStats = null;
      loadSessions().catch((e) => status(e.message, "err"));
    };
    seg.append(b);
  }
  slide(seg, "sessRange");
}

// a "YYYY-MM-DD" as a local date, and back
const sessDate = (d) => { const [y, m, day] = d.split("-").map(Number); return new Date(y, m - 1, day); };
const sessISO = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const sessDay = (d) => d.toLocaleDateString(intlLang(), { month: "short", day: "numeric" });
// a length of time, as hours and minutes
function fmtDur(sec) {
  const m = Math.round(sec / 60);
  if (!sec) return "0";
  if (m < 1) return t("<1m");
  if (m < 60) return t("{m}m", { m });
  return t("{h}h {m}m", { h: Math.floor(m / 60), m: m % 60 });
}

function renderSessions() {
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderSessRange();
  const all = sessions?.sessions || [];
  const st = sessStats || { from: "", to: "", days: [], agents: {} };
  const rows = st.days.flatMap((d) => d.usage.map((u) => ({ ...u, date: d.date })));
  const acts = st.days.flatMap((d) => d.active.map((a) => ({ ...a, date: d.date })));

  // one segment per agent that has sessions, or usage in the range
  const agents = [...new Map([...all.map((s) => [s.agent, s.name]), ...Object.entries(st.agents || {})]).entries()];
  if (sessAgent !== "all" && !agents.some(([id]) => id === sessAgent)) sessAgent = "all";
  const seg = $("#sessAgent");
  seg.hidden = agents.length < 2;
  const opts = [["all", t("All")], ...agents];
  // the same agents keep their buttons: drawn anew, a strip with more than
  // fit (#929) would lose where it was scrolled to (WebKit)
  const was = [...seg.querySelectorAll(":scope > .opt")];
  if (was.length !== opts.length || was.some((b, i) => b.dataset.agent !== opts[i][0] || b.textContent !== opts[i][1])) {
    seg.replaceChildren();
    for (const [id, name] of opts) {
      const b = el("button", "opt", name);
      b.dataset.agent = id;
      b.onclick = () => { sessAgent = id; renderSessions(); };
      seg.append(b);
    }
  }
  for (const b of seg.querySelectorAll(":scope > .opt")) {
    b.classList.toggle("on", b.dataset.agent === sessAgent);
    b.setAttribute("aria-pressed", String(b.dataset.agent === sessAgent));
  }
  // and the agent picked is brought into the strip, the page left where it is
  const picked = seg.querySelector(":scope > .on");
  if (picked && seg.scrollWidth > seg.clientWidth) {
    if (picked.offsetLeft < seg.scrollLeft) seg.scrollLeft = picked.offsetLeft;
    else if (picked.offsetLeft + picked.offsetWidth > seg.scrollLeft + seg.clientWidth) seg.scrollLeft = picked.offsetLeft + picked.offsetWidth - seg.clientWidth;
  }
  slide(seg, "sessAgent");

  // the model and folder filters offer what the range has, under the other filters
  const byAgent = (x) => sessAgent === "all" || x.agent === sessAgent;
  const byModel = (x) => !sessModel || x.model === sessModel;
  const byFolder = (x) => !sessFolder || x.cwd === sessFolder;
  const spent = (list, key) => {
    const m = new Map();
    for (const r of list) if (r[key]) m.set(r[key], (m.get(r[key]) || 0) + r.input + r.output);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  };
  const models = spent(rows.filter((r) => byAgent(r) && byFolder(r)), "model").map(([v, n]) => ({ v, name: v, note: t("{n} tokens", { n: fmtN(n) }) }));
  const folders = spent(rows.filter((r) => byAgent(r) && byModel(r)), "cwd").map(([v, n]) => ({ v, name: baseName(v), note: v + " · " + t("{n} tokens", { n: fmtN(n) }) }));
  sessPick($("#sessModel"), "All models", sessModel, models, "Model", (v) => { sessModel = v; renderSessions(); });
  sessPick($("#sessFolder"), "All folders", sessFolder, folders, "Folder", (v) => { sessFolder = v; renderSessions(); });

  // the list: the sessions at work in the range, under every filter. Which
  // ones were at work is the range's own answer (st.keys, the sessions
  // stats counted), not a guess from s.last: a session's file goes on being
  // written after its last call — a fork's seed, a setting — so one whose
  // last line falls in the range spent nothing in it, and it was listed but
  // not counted, the note beside the list saying more than the KPI above it.
  const inRange = st.keys ? new Set(st.keys) : null;
  const from = !inRange && sessDays() && st.from ? sessDate(st.from) : null;
  const q = sessQuery.trim().toLowerCase();
  const list = all.filter((s) => byAgent(s) && (!sessFolder || s.cwd === sessFolder) &&
    (!sessModel || s.models.some((m) => m.model === sessModel)) &&
    (inRange ? inRange.has(s.agent + ":" + s.id) : !from || new Date(s.last) >= from) &&
    (!q || [s.title, s.cwd, s.id, s.name, ...s.models.map((m) => m.model)].some((x) => (x || "").toLowerCase().includes(q))));

  // the range's totals, of every session under the filters
  const used = rows.filter((r) => byAgent(r) && byModel(r) && byFolder(r));
  const tot = { input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0 };
  const unpriced = new Set();
  for (const r of used) {
    for (const k of ["input", "output", "cache_read", "cache_write", "cost"]) tot[k] += r[k];
    if (!r.priced) unpriced.add(r.model);
  }
  tot.unpriced = unpriced.size;
  // active time isn't told apart by model
  const active = sessModel ? null : acts.filter((a) => byAgent(a) && byFolder(a)).reduce((n, a) => n + a.seconds, 0);

  const c = tot.cost ? fmtCost(tot) : "";
  const unpricedNote = tot.unpriced ? t("Not counted: {models}, with no known price", { models: sessModelNames([...unpriced]) }) : t("Estimated using effective model prices, including custom prices");
  // a session's models are counted, not its calls: the agents' files price by model
  const unpricedModels = tot.unpriced ? t(tot.unpriced === 1 ? "{n} model unpriced" : "{n} models unpriced", { n: tot.unpriced }) : "";
  costHead(c, unpricedModels, unpricedNote);

  // the overview is read again for the filters as they are now; until it is
  // in, the last one stands, dimmed
  if (sessOverAt !== sessOverQ()) loadSessOverview();
  const ov = sessOver || {};
  const stale = sessOverAt !== sessOverQ();

  const stats = $("#sessStats");
  stats.replaceChildren();
  const box = $("#sessList");
  box.replaceChildren();
  const chart = $("#sessChart");
  const grid = $("#sessGrid");
  const head = $("#sessListHead");
  if (!used.length && !list.length) {
    stats.classList.remove("six");
    stats.classList.add("empty");
    const filtered = sessAgent !== "all" || sessModel || sessFolder || q;
    stats.append(el("div", "none", !all.length && !rows.length ? t("No sessions yet. Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here, with what each cost and the command that resumes it.") : filtered ? t("No session matches.") : t("Nothing in this range.")));
    if (from && all.some((s) => byAgent(s) && byFolder(s) &&
      (!sessModel || s.models.some((m) => m.model === sessModel)) &&
      (!q || [s.title, s.cwd, s.id, s.name, ...s.models.map((m) => m.model)].some((x) => (x || "").toLowerCase().includes(q))))) {
      stats.firstChild.textContent = t("No sessions in this range");
      const history = el("button", "text sess-show-history", t("All sessions"));
      history.onclick = () => {
        sessRange = "all";
        try { localStorage.setItem("magpie.sessRange", sessRange); } catch {}
        sessStats = null;
        loadSessions().catch((e) => status(e.message, "err"));
      };
      stats.append(history);
    }
    box.hidden = true;
    chart.hidden = true;
    grid.hidden = true;
    sessDrawn = 0;
    sessTotal = 0;
    // the search stays where it was typed, to be cleared
    head.hidden = !q;
  } else {
    stats.classList.remove("empty");
    stats.classList.add("six");
    const tile = (n, label, sub, title, cls) => {
      const e = el("div", "kpi" + (cls ? " " + cls : ""));
      if (title) e.title = title;
      // a figure never wraps: a tile a line taller moves the page when a
      // filter shortens it; a time's units step down, as the Health app's do
      const b = el("b", "");
      if (cls?.includes("dur")) for (const part of n.split(/(\d+)/)) part && b.append(/^\d+$/.test(part) ? part : el("i", "u", part.trim()));
      else b.textContent = n;
      b.title = n;
      e.append(b, el("span", "", label));
      if (sub) e.append(el("small", "", sub));
      stats.append(e);
    };
    const days = new Set(used.map((r) => r.date)).size;
    // how many sessions, and what the middle one and the one in ten spent
    tile(ov.count == null ? "—" : fmtN(ov.count), t("sessions"), ov.count ? t("median {m} · p90 {p}", { m: fmtN(ov.median), p: fmtN(ov.p90) }) : "",
      t("Tokens in and out per session: the middle session's, and what nine in ten stay under"), stale ? "stale" : "");
    tile(fmtN(tot.input + tot.output), t("tokens"), t("{a} in · {b} out", { a: fmtN(tot.input), b: fmtN(tot.output) }));
    tile(c ? "≈" + c : "—", t("cost"), unpricedModels || t("at effective prices"), unpricedNote);
    // input leaves out what was read from the cache and written to it
    const prompt = tot.input + tot.cache_read + tot.cache_write;
    tile(fmtN(tot.cache_read), t("cache read"), tot.cache_read && prompt ? t("hit rate {p}", { p: Math.round(100 * tot.cache_read / prompt) + "%" }) : "", tot.cache_write ? t("{n} written", { n: fmtN(tot.cache_write) }) : "");
    tile(active == null ? "—" : fmtDur(active), t("active"), active == null ? t("not kept by model") : t(days === 1 ? "on {n} day" : "on {n} days", { n: days }),
      t("The time the sessions were at work: the pauses between one message and the next, each under five minutes"), "dur");
    // how many folders, and how much of it the first took
    const byCwd = sessSums(used, "cwd");
    const lead = byCwd[0], spentAll = byCwd.reduce((n, f) => n + f.n, 0);
    tile(String(byCwd.length), t("projects"), !lead ? "" : byCwd.length > 1 && spentAll ? t("{p} in {name}", { p: Math.round(100 * lead.n / spentAll) + "%", name: baseName(lead.v) }) : baseName(lead.v), lead?.v || "");

    const actsIn = sessModel ? null : acts.filter((a) => byAgent(a) && byFolder(a));
    renderSessChart(chart, st, used, ov, actsIn);
    grid.hidden = false;
    // the folders and models to pick from, as the pickers offer them
    sessBars($("#sessProjects"), "Projects", sessSums(rows.filter((r) => byAgent(r) && byModel(r)), "cwd"), sessFolder, (v) => { sessFolder = v; renderSessions(); }, baseName);
    sessBars($("#sessModels"), "Models", sessSums(rows.filter((r) => byAgent(r) && byFolder(r)), "model"), sessModel, (v) => { sessModel = v; renderSessions(); }, (v) => v);
    renderSessHours($("#sessHours"), actsIn);
    renderSessTop();
    renderSessShape();
    renderSessTools();
    renderSessSkills();
    head.hidden = !list.length && !q;
    box.hidden = !list.length && !q;
    // a page of the list, not all of it: the same filters keep the pages
    // opened, a changed one starts at the first (see sessListKey)
    const key = [sessAgent, sessModel, sessFolder, sessRange, q].join("\0");
    if (key !== sessListKey) { sessListKey = key; sessShown = SESS_PAGE; }
    const page = list.slice(0, sessShown);
    sessDrawn = page.length;
    sessTotal = list.length;
    for (const s of page) box.append(sessionItem(s));
    if (list.length > page.length) {
      const more = el("button", "text sess-page-more");
      more.textContent = t("Show {n} more", { n: Math.min(SESS_PAGE, list.length - page.length) });
      more.onclick = () => { sessShown += SESS_PAGE; renderSessions(); };
      box.append(more);
    }
    if (!list.length && q) box.append(el("div", "empty-state", t("No session matches.")));
  }
  const dirs = (sessions?.dirs || []).join(" · ");
  // The list is every session the filters keep, drawn in pages, so the note
  // counts that list and not every session on the computer: a search that
  // matches one session says "1 session", not "1 of 2600", which read as if
  // the rest were behind a "Show more" that isn't there (yetone, #1019).
  // Nothing left to draw, the note is just the list's length, in words a
  // reader uses: one session is a session, not "1 sessions".
  const parts = [t("Totals count native sessions and gateway requests with a session ID")];
  if (sessDrawn !== null) {
    parts.push(sessDrawn < sessTotal
      ? t("showing {drawn} of {n}", { drawn: sessDrawn, n: sessTotal })
      : t(sessTotal === 1 ? "{n} session" : "{n} sessions", { n: sessTotal }));
  }
  if (dirs) parts.push(dirs);
  $("#sessNote").textContent = parts.join(" · ");
}

// sessSums adds up usage rows by one of their fields, the most tokens first
function sessSums(list, key) {
  const m = new Map();
  for (const r of list) {
    if (!r[key]) continue;
    const s = m.get(r[key]) || { v: r[key], n: 0, cost: 0, unpriced: 0 };
    s.n += r.input + r.output;
    s.cost += r.cost;
    if (!r.priced) s.unpriced++;
    m.set(r[key], s);
  }
  return [...m.values()].sort((a, b) => b.n - a.n);
}

// sessLevels grades values 0 (none) to 4 by the quartiles of those above 0,
// as a calendar of contributions does. Under eight such values a quartile
// is one value or none, so two days 2% apart could be two shades apart and
// one twice another the same (#1921): a few are graded by their share of the
// busiest instead.
function sessLevels(values) {
  const nz = values.filter((v) => v > 0).sort((a, b) => a - b);
  if (!nz.length) return () => 0;
  if (nz.length < 8) {
    const most = nz[nz.length - 1];
    return (v) => v <= 0 ? 0 : Math.min(4, Math.max(1, Math.ceil(4 * v / most - 1e-9)));
  }
  const q = (p) => nz[Math.min(nz.length - 1, Math.floor(p * nz.length))];
  const [a, b, c, top] = [q(0.25), q(0.5), q(0.75), nz[nz.length - 1]];
  return (v) => v <= 0 ? 0 : v >= top ? 4 : v <= a ? 1 : v <= b ? 2 : v <= c ? 3 : 4;
}
function sessLegend() {
  const l = el("span", "sess-legend");
  l.append(el("span", "", t("Less")));
  for (let i = 0; i <= 4; i++) l.append(el("i", "l" + i));
  l.append(el("span", "", t("More")));
  return l;
}
// the short names of the days of the week, Monday first
function sessWeekdays() {
  const loc = intlLang();
  return Array.from({ length: 7 }, (_, i) => new Date(2024, 0, 1 + i).toLocaleDateString(loc, { weekday: "short" }));
}
function sessCardHead(title) {
  const h = el("div", "sess-card-head");
  h.append(el("span", "label", t(title)));
  return h;
}
function sessSegs(opts, cur, choose) {
  const seg = el("div", "segs");
  for (const [id, name, off] of opts) {
    const b = el("button", "opt" + (id === cur ? " on" : ""), t(name));
    if (off) { b.disabled = true; b.title = off; }
    b.onclick = () => { if (id !== cur) choose(id); };
    seg.append(b);
  }
  return seg;
}

// renderSessChart draws the range's activity: day by day as bars up to a
// hundred days, as a calendar up to a year, week by week past that. It counts
// tokens (output on top of input, as on the Overview), output tokens alone,
// messages, sessions at work, cost or active time.
function renderSessChart(chart, st, used, ov, acts) {
  const perDay = ov.days, msgs = ov.messages;
  const first = sessDate(st.from), last = sessDate(st.to);
  const n = Math.round((last - first) / 864e5) + 1;
  chart.hidden = !(n >= 2);
  if (chart.hidden) { chart.dataset.drawn = ""; return; }
  const days = [], at = new Map();
  for (let i = 0, d = new Date(first); i < n; i++, d.setDate(d.getDate() + 1)) {
    const x = { day: new Date(d), input: 0, output: 0, cost: 0, unpriced: 0, sessions: perDay?.length === n ? perDay[i] : 0,
      messages: msgs?.length === n ? msgs[i] : 0, active: 0 };
    days.push(x);
    at.set(sessISO(d), x);
  }
  for (const r of used) {
    const x = at.get(r.date);
    if (!x) continue;
    x.input += r.input; x.output += r.output; x.cost += r.cost;
    if (!r.priced) x.unpriced++;
  }
  for (const a of acts || []) { const x = at.get(a.date); if (x) x.active += a.seconds; }
  const off = { sessions: perDay?.length === n ? "" : t("Still counting"), messages: msgs?.length === n ? "" : t("Still counting"), active: acts ? "" : t("not kept by model") };
  const metric = off[sessMetric] ? "tokens" : sessMetric;
  const value = (x) => metric === "cost" ? x.cost : metric === "sessions" ? x.sessions : metric === "messages" ? x.messages :
    metric === "active" ? x.active : metric === "output" ? x.output : x.input + x.output;
  const show = (v) => metric === "cost" ? "≈" + fmtCost({ cost: v }) : metric === "active" ? fmtDur(v) : fmtN(v);
  const tip = (x, when) => {
    const parts = [x.input + x.output ? t("{n} tokens", { n: fmtN(x.input + x.output) }) : "", x.cost && fmtCost(x) ? "≈" + fmtCost(x) : "",
      x.messages ? t(x.messages === 1 ? "{n} message" : "{n} messages", { n: fmtN(x.messages) }) : "",
      x.sessions ? t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: x.sessions }) : "", x.active ? t("{d} active", { d: fmtDur(x.active) }) : ""].filter(Boolean);
    return parts.length ? when + " · " + parts.join(" · ") : t("{when} · nothing", { when });
  };
  const shape = n <= 100 ? "day" : n <= 371 ? "calendar" : "week";
  // the page reads the sessions again every few seconds, and while an agent
  // is at work today's numbers change each time: the same days by the same
  // metric keep their bars, filled in again where they are, so the one under
  // the pointer — today's, as often as not — keeps its tooltip (#309)
  const drawn = [shape, metric, st.from, n, locale, ...Object.values(off).map(Boolean)].join("|");
  const same = chart.dataset.drawn === drawn;
  chart.dataset.drawn = drawn;
  chart.redraw = () => renderSessChart(chart, st, used, ov, acts); // the metrics kept draw what is in now

  if (same && shape === "calendar") {
    const lv = sessLevels(days.map(value));
    chart.querySelectorAll(".sess-cal > i").forEach((c, i) => {
      c.className = "l" + lv(value(days[i]));
      c.title = tip(days[i], sessDay(days[i].day));
    });
    chart.querySelector(".sess-cal-side").replaceWith(sessCalSide(days, value, show));
    chart.fitCal = () => sessCalFit(chart, days, value, tip);
    return;
  }
  chart.fitCal = null;
  if (shape === "calendar") {
    chart.replaceChildren();
    chart.classList.add("cal");
    drawHead();
    const wrap = el("div", "sess-cal-wrap");
    wrap.append(sessCalendar(days, value, tip), sessCalSide(days, value, show));
    chart.append(wrap);
    chart.querySelector(".sess-chart-head").append(sessLegend());
    chart.fitCal = () => sessCalFit(chart, days, value, tip);
    chart.fitCal();
    sessCalWidth.observe(chart);
    return;
  }

  // bars: a day each, or a week
  const step = shape === "week" ? 7 : 1;
  const buckets = [];
  for (let i = 0; i < n; i += step) {
    const b = { day: days[i].day, input: 0, output: 0, cost: 0, unpriced: 0, sessions: 0, messages: 0, active: 0 };
    for (const x of days.slice(i, i + step)) for (const k of ["input", "output", "cost", "unpriced", "sessions", "messages", "active"]) b[k] += x[k];
    buckets.push(b);
  }
  const peak = Math.max(metric === "cost" ? 0.001 : 1, ...buckets.map(value));
  const fill = (bar, b) => {
    const [top, low] = bar.children;
    if (metric === "tokens") {
      top.style.height = (100 * b.output / peak).toFixed(1) + "%";
      low.style.height = (100 * b.input / peak).toFixed(1) + "%";
    } else top.style.height = (100 * value(b) / peak).toFixed(1) + "%";
    const label = sessDay(b.day);
    bar.title = tip(b, step === 7 ? t("week of {label}", { label }) : label);
  };
  if (same) {
    chart.querySelector(".sess-chart-head .peak").textContent = show(peak);
    const bars = chart.querySelectorAll(".bars > .bar");
    buckets.forEach((b, i) => fill(bars[i], b));
    return;
  }
  chart.replaceChildren();
  chart.classList.remove("cal");
  drawHead().append(el("span", "peak", show(peak)));
  const bars = el("div", "bars");
  const labels = el("div", "labels");
  const k = buckets.length;
  const every = k <= 8 ? 1 : k <= 31 ? Math.ceil(k / 6) : Math.ceil(k / 5);
  buckets.forEach((b, i) => {
    const bar = el("div", "bar");
    if (metric === "tokens") bar.append(el("i", "out"), el("i", "in"));
    else bar.append(el("i", "out"));
    fill(bar, b);
    bars.append(bar);
    const label = sessDay(b.day);
    const end = i === k - 1 && (k - 1) % every >= every / 2;
    labels.append(el("span", "", i % every === 0 || end ? label : ""));
  });
  chart.append(bars, labels);
  fitChartLabels();

  function drawHead() {
    const head = el("div", "sess-chart-head");
    const seg = sessSegs(SESS_METRICS.map(([id, name]) => [id, name, off[id]]), metric, (id) => {
      sessMetric = id;
      try { localStorage.setItem("magpie.sessMetric", id); } catch {}
      chart.redraw();
    });
    head.append(el("span", "label", t(shape === "week" ? "By week" : shape === "day" ? "By day" : "Activity")), seg, el("span", "grow"));
    chart.append(head);
    slide(seg, "sessMetric");
    return head;
  }
}

// a week's column in the calendar, cell and gap: as wide as weeks are counted
// by, and as wide as one may grow to take what is left over
const SESS_WEEK = 20, SESS_WEEK_MAX = 24;

// sessCalFit lays the calendar across its card (John on Discord: 这个活跃度怎么
// 没有铺满全部宽度呢？): a range of a few months took a few hundred pixels of a
// wide card and left the rest empty. The weeks before the range come in as
// empty cells, GitHub's way, up to a year in all, and the cells grow a little
// to take what is left; a narrow card gets none and its cells shrink.
// It fits on the next frame: a calendar put again changes the card's height,
// which this would be told of in the same call, a ResizeObserver loop.
const sessCalWidth = new ResizeObserver((es) => {
  for (const { target: chart } of es) {
    if (!chart.fitFrame) chart.fitFrame = requestAnimationFrame(() => { chart.fitFrame = 0; chart.fitCal?.(); });
  }
});
function sessCalFit(chart, days, value, tip) {
  const wrap = chart.querySelector(".sess-cal-wrap"), cal = wrap?.querySelector(".sess-cal");
  if (!cal || !wrap.clientWidth) return;
  const ws = getComputedStyle(wrap), side = wrap.querySelector(".sess-cal-side");
  const wd = Math.max(0, ...[...cal.querySelectorAll(".wd")].map((l) => l.getBoundingClientRect().width));
  const room = wrap.clientWidth - wd - (ws.flexDirection === "row" ? side.getBoundingClientRect().width + (parseFloat(ws.columnGap) || 0) : 0);
  const weeks = Math.ceil(((days[0].day.getDay() + 6) % 7 + days.length) / 7);
  const pad = Math.max(0, Math.min(53, Math.floor(room / SESS_WEEK)) - weeks);
  if (String(pad) !== cal.dataset.pad) cal.replaceWith(sessCalendar(days, value, tip, pad));
}

// sessCalendar is the range as weeks of days, Monday on top, each day as
// dark as it is busy among the others, after pad empty weeks before it
function sessCalendar(days, value, tip, pad = 0) {
  const lv = sessLevels(days.map(value));
  const lead = (days[0].day.getDay() + 6) % 7 + 7 * pad;
  const weeks = Math.ceil((lead + days.length) / 7);
  const cal = el("div", "sess-cal");
  cal.dataset.pad = pad;
  cal.style.gridTemplateColumns = `auto repeat(${weeks}, minmax(0, 1fr))`;
  cal.style.maxWidth = `calc(2.6em + ${weeks * (pad ? SESS_WEEK_MAX : SESS_WEEK)}px)`;
  const names = sessWeekdays();
  for (const i of [0, 2, 4]) {
    const l = el("span", "wd", names[i]);
    l.style.gridArea = `${i + 2} / 1`;
    cal.append(l);
  }
  const loc = intlLang();
  let month = -1, labelAt = -9;
  // the days before the range, from the Monday it is laid out from: their
  // months named as the range's are, the days themselves left blank
  const before = Array.from({ length: pad ? lead : 0 }, (_, i) => {
    const d = new Date(days[0].day);
    d.setDate(d.getDate() - lead + i);
    return { day: d, before: true };
  });
  [...before, ...days].forEach((x, i) => {
    const k = (pad ? 0 : lead) + i, w = Math.floor(k / 7), wd = k % 7;
    if ((wd === 0 || i === 0) && x.day.getMonth() !== month) {
      month = x.day.getMonth();
      // a month mostly gone when the weeks before begin is left unnamed, so
      // as not to crowd out the next one's name
      if (w - labelAt >= 3 && !(x.before && x.day.getDate() > 14)) {
        const m = el("span", "mo", x.day.toLocaleDateString(loc, month === 0 && weeks > 20 ? { month: "short", year: "numeric" } : { month: "short" }));
        m.style.gridArea = `1 / ${w + 2}`;
        cal.append(m);
        labelAt = w;
      }
    }
    const c = x.before ? el("s") : el("i", "l" + lv(value(x)));
    c.style.gridArea = `${wd + 2} / ${w + 2}`;
    if (!x.before) c.title = tip(x, sessDay(x.day));
    cal.append(c);
  });
  return cal;
}

// beside the calendar: the days at work, the longest run of them, the busiest
function sessCalSide(days, value, show) {
  const side = el("div", "sess-cal-side");
  const fact = (label, v, sub) => {
    const f = el("div", "fact");
    f.append(el("b", "", v), el("span", "", label));
    if (sub) f.append(el("small", "", sub));
    side.append(f);
  };
  const busy = (x) => x.input + x.output || x.active || x.sessions;
  let run = 0, streak = 0, best = null;
  for (const x of days) {
    run = busy(x) ? run + 1 : 0;
    streak = Math.max(streak, run);
    if (value(x) > 0 && (!best || value(x) > value(best))) best = x;
  }
  fact(t("days at work"), `${days.filter(busy).length} / ${days.length}`);
  fact(t("longest streak"), t(streak === 1 ? "{n} day" : "{n} days", { n: streak }));
  if (best) fact(t("busiest day"), sessDay(best.day), show(value(best)));
  return side;
}

// renderSessHours lays the range's active time out by day of the week and
// hour of the day, in this computer's time zone
function renderSessHours(box, acts) {
  box.replaceChildren();
  const head = sessCardHead("By hour");
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  head.append(el("span", "grow"), el("span", "tz", tz));
  box.append(head);
  if (!acts) return box.append(el("div", "sess-none", t("Active time isn't kept by model.")));
  const grid = Array.from({ length: 7 }, () => new Array(24).fill(0));
  let total = 0;
  for (const a of acts) {
    if (!a.hours) continue;
    const wd = (sessDate(a.date).getDay() + 6) % 7;
    a.hours.forEach((s, h) => { grid[wd][h] += s; total += s; });
  }
  if (!total) return box.append(el("div", "sess-none", t("No active time in this range.")));
  const lv = sessLevels(grid.flat());
  const names = sessWeekdays();
  const hh = (h) => String(h).padStart(2, "0") + ":00";
  const map = el("div", "sess-hours");
  let best = [0, 0];
  grid.forEach((row, wd) => {
    map.append(el("span", "wd", names[wd]));
    row.forEach((s, h) => {
      const c = el("i", "l" + lv(s));
      c.title = `${names[wd]} ${hh(h)} · ` + (s ? fmtDur(s) : t("nothing"));
      if (s > grid[best[0]][best[1]]) best = [wd, h];
      map.append(c);
    });
  });
  map.append(el("span", ""));
  for (let h = 0; h < 24; h++) map.append(el("span", "hr", h % 6 === 0 ? String(h) : ""));
  box.append(map);
  const foot = el("div", "sess-card-foot");
  foot.append(el("span", "", t("Busiest at {when}", { when: `${names[best[0]]} ${hh(best[1])}` })), el("span", "grow"), sessLegend());
  box.append(foot);
}

// renderSessTop lists the sessions that spent the most in the range, under
// the filters; a click opens one to its details, read whole from its files
function renderSessTop() {
  const box = $("#sessTop");
  const ov = sessOver || {};
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Top sessions");
  const seg = sessSegs(SESS_TOPS, sessTopBy, (id) => {
    sessTopBy = id;
    try { localStorage.setItem("magpie.sessTop", id); } catch {}
    renderSessTop();
  });
  head.append(el("span", "grow"), seg);
  box.append(head);
  slide(seg, "sessTop");
  const list = ov.top?.[sessTopBy] || [];
  const body = el("div", "sess-tops");
  if (!list.length) body.append(el("div", "sess-none", ov.top ? t("Nothing in this range.") : "…"));
  const icons = new Map((sessions?.sessions || []).map((s) => [s.agent, s.icon]));
  const names = sessStats?.agents || {};
  for (const s of list) {
    const open = sessTopOpen === s.key;
    const item = el("div", "sess-item" + (open ? " open" : ""));
    const r = el("div", "row sess sess-top");
    r.append(icon(icons.get(s.agent) || "generic"));
    const who = el("div", "who");
    // a session magpie saw only through the gateway (#2285: a Pi
    // subagent's, its file in a temp folder) has no folder and no prompt
    // unless gateway conversations are recorded: say so, not "(no prompt)"
    who.append(el("div", "name", s.title || t(s.gateway ? "(prompt not recorded)" : "(no prompt)")));
    const sub = el("div", "sub", [s.cwd ? baseName(s.cwd) : s.gateway ? t("Gateway session") : "", names[s.agent] || s.agent, s.models?.[0], ago(s.last)].filter(Boolean).join(" · "));
    sub.title = s.cwd || (s.gateway ? t("Seen only through the gateway: the agent kept no session file magpie can read. Turn on Record gateway conversations in Sessions to keep its prompts.") : "");
    who.append(sub);
    r.append(who);
    const fc = fmtCost({ cost: s.cost, unpriced: !s.priced });
    const v = sessTopBy === "cost" ? (fc ? "≈" + fc : "—") : sessTopBy === "active" ? fmtDur(s.active) : fmtN(s.input + s.output);
    r.append(el("div", "v", v));
    r.onclick = () => {
      if (window.getSelection()?.toString()) return;
      sessTopOpen = open ? "" : s.key;
      const had = sessFull.get(s.key);
      if (!open && (!had || had.error)) {
        sessFull.set(s.key, "…");
        api("sessions/one?key=" + encodeURIComponent(s.key)).then((full) => sessFull.set(s.key, full), (e) => sessFull.set(s.key, { error: e.message }))
          .then(() => { if (sessTopOpen === s.key && view === "usage" && usageTab === "sessions") renderSessTop(); });
      }
      renderSessTop();
    };
    item.append(r);
    if (open) {
      const full = sessFull.get(s.key);
      item.append(!full || full === "…" ? el("div", "sess-detail sess-wait", t("Reading the session…")) : full.error ? el("div", "sess-detail sess-wait", full.error) : sessionDetail(full));
    }
    body.append(item);
  }
  box.append(body);
}

// the ways a session's shape is told, and the one shown
const SESS_SHAPES = [["messages", "Messages"], ["minutes", "Length"], ["autonomy", "Tool calls"]];
let sessShapeBy = "messages";
try { const v = localStorage.getItem("magpie.sessShape"); if (SESS_SHAPES.some(([id]) => id === v)) sessShapeBy = v; } catch {}

// renderSessShape counts the range's sessions by how long they ran: by
// messages, by minutes at work, or by tool calls a prompt
function renderSessShape() {
  const box = $("#sessShape");
  const ov = sessOver || {};
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const sh = ov.shape?.[sessShapeBy];
  const head = sessCardHead("Session shape");
  const seg = sessSegs(SESS_SHAPES, sessShapeBy, (id) => {
    sessShapeBy = id;
    try { localStorage.setItem("magpie.sessShape", id); } catch {}
    renderSessShape();
  });
  head.append(seg, el("span", "grow"), el("span", "tz", sh ? t(sh.total === 1 ? "{n} session" : "{n} sessions", { n: fmtN(sh.total) }) : ""));
  box.append(head);
  slide(seg, "sessShape");
  if (!sh?.total) return box.append(el("div", "sess-none", ov.shape ? t("Nothing in this range.") : "…"));
  const label = (i) => {
    const lo = sh.edges[i], hi = sh.edges[i + 1];
    if (sessShapeBy === "autonomy" && lo === 0) return "<1";
    const r = hi == null ? lo + "+" : hi - 1 === lo ? String(lo) : `${lo}–${hi - 1}`;
    return sessShapeBy === "minutes" ? t("{r}m", { r }) : r;
  };
  const what = { messages: "{r} messages", minutes: "{r} minutes at work", autonomy: "{r} tool calls a prompt" }[sessShapeBy];
  const peak = Math.max(1, ...sh.counts);
  const bars = el("div", "sess-shape");
  sh.counts.forEach((c, i) => {
    const col = el("div", "col");
    const pct = Math.round(100 * c / sh.total);
    col.title = t(what, { r: label(i) }) + " · " + t(c === 1 ? "{n} session" : "{n} sessions", { n: fmtN(c) }) + ` (${pct}%)`;
    // a bar as tall as its share of the busiest bin, on a baseline; an
    // empty bin a stub, so no grey column reads as a bar of its own
    const plot = el("div", "plot");
    const fill = el("i", c ? "" : "none");
    fill.style.height = c ? `max(4px, calc((100% - 20px) * ${(c / peak).toFixed(3)}))` : "";
    plot.append(el("b", c ? "" : "zero", fmtN(c)), fill);
    // the busiest bin in full, when one is busiest
    col.classList.toggle("peak", c === peak && c > 0 && sh.counts.filter((n) => n === peak).length === 1);
    col.append(plot, el("span", "", label(i)));
    bars.append(col);
  });
  box.append(bars);
  // what the two axes are, said plainly
  const axis = { messages: "messages in a session, prompts and replies", minutes: "minutes a session was at work", autonomy: "tool calls the agent made on its own for each prompt" }[sessShapeBy];
  const foot = el("div", "sess-card-foot");
  foot.append(el("span", "", t("Across: {axis} · Height: sessions", { axis: t(axis) })));
  box.append(foot);
}

// the colours of the kinds of tool, as the calls are told apart
const TOOL_CATS = { Bash: "#e0823d", Edit: "#4f8cf0", Read: "#2fb087", Write: "#9b6cf0", Grep: "#e0608f", Glob: "#d4a72c", Task: "#35a9c7", Tool: "#7c83f2", Other: "#9aa0a8" };
const toolColor = (c) => TOOL_CATS[c] || TOOL_CATS.Other;
// a kind's name, as its tools are named, "Other" aside
const toolKind = (c) => c === "Other" ? t("Other") : c;
function toolDot(c) {
  const d = el("i", "dot");
  d.style.background = toolColor(c);
  return d;
}

// renderSessTools is what the sessions called their tools for: the most
// called, by kind, and week by week
function renderSessTools() {
  const box = $("#sessTools");
  const tu = sessOver?.tools;
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Tool use");
  head.append(el("span", "grow"));
  if (tu?.calls) head.append(el("span", "tz", t("{n} calls", { n: fmtN(tu.calls) }) + " · " + t(tu.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(tu.sessions) })));
  box.append(head);
  if (!tu?.calls) return box.append(el("div", "sess-none", tu ? t("No tool calls in this range.") : "…"));
  const cols = el("div", "sess-tools-cols");

  // the most called, each with its kind's dot, calls, sessions and share
  const top = el("div", "sess-bars tools");
  const peak = Math.max(1, ...tu.top.map((x) => x.calls));
  for (const x of tu.top) {
    const r = el("div", "sess-bar tool");
    r.title = `${x.name} · ${toolKind(x.category)}`;
    const n = el("span", "n");
    n.append(toolDot(x.category), el("span", "", x.name));
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.calls / peak).toFixed(1) + "%";
    fill.style.background = toolColor(x.category);
    track.append(fill);
    r.append(n, track, el("span", "v", fmtN(x.calls)), el("span", "s", t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(x.sessions) })),
      el("span", "p", Math.round(100 * x.calls / tu.calls) + "%"));
    top.append(r);
  }
  cols.append(top);

  const side = el("div", "sess-tools-side");
  // by kind: one bar of every call, and each kind's count
  const mix = el("div", "sess-mix");
  const kinds = el("div", "sess-kinds");
  for (const c of tu.categories) {
    const seg = el("i");
    seg.style.flexGrow = c.calls;
    seg.style.background = toolColor(c.name);
    seg.title = `${toolKind(c.name)} · ${t("{n} calls", { n: fmtN(c.calls) })} (${Math.round(100 * c.calls / tu.calls)}%)`;
    mix.append(seg);
    const k = el("span", "kind");
    k.title = seg.title;
    k.append(toolDot(c.name), el("span", "", toolKind(c.name)), el("b", "", fmtN(c.calls)));
    kinds.append(k);
  }
  side.append(mix, kinds);

  // week by week, the last sixteen, each kind stacked
  const weeks = tu.weeks.slice(-16);
  if (weeks.length > 1) {
    side.append(el("div", "sess-sub", t("By week")));
    const order = tu.categories.map((c) => c.name);
    const total = (w) => Object.values(w.calls).reduce((a, b) => a + b, 0);
    const wpeak = Math.max(1, ...weeks.map(total));
    const bars = el("div", "sess-weeks");
    for (const w of weeks) {
      const bar = el("div", "bar");
      const n = total(w);
      bar.title = t("week of {label}", { label: sessDay(sessDate(w.start)) }) + " · " + t("{n} calls", { n: fmtN(n) });
      const stack = el("div", "stack");
      stack.style.height = (n ? Math.max(2, 100 * n / wpeak) : 0).toFixed(1) + "%";
      for (const c of order) {
        if (!w.calls[c]) continue;
        const part = el("i");
        part.style.flexGrow = w.calls[c];
        part.style.background = toolColor(c);
        stack.append(part);
      }
      bar.append(stack);
      bars.append(bar);
    }
    const labels = el("div", "sess-weeks-labels");
    labels.append(el("span", "", sessDay(sessDate(weeks[0].start))), el("span", "", sessDay(sessDate(weeks[weeks.length - 1].start))));
    side.append(bars, labels);
  }
  cols.append(side);
  box.append(cols);
}

// renderSessSkills lists the skills the sessions called up, the most first
function renderSessSkills() {
  const box = $("#sessSkills");
  const su = sessOver?.skills;
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Top skills");
  head.append(el("span", "grow"));
  if (su?.calls) head.append(el("span", "tz", t("{n} calls", { n: fmtN(su.calls) }) + " · " + t(su.count === 1 ? "{n} skill" : "{n} skills", { n: fmtN(su.count) })));
  box.append(head);
  if (!su?.calls) return box.append(el("div", "sess-none", su ? t("No skill was called up in this range.") : "…"));
  const names = sessStats?.agents || {};
  const peak = Math.max(1, ...su.top.map((x) => x.calls));
  const body = el("div", "sess-skills");
  for (const x of su.top) {
    const r = el("div", "sess-skill");
    const line = el("div", "line");
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.calls / peak).toFixed(1) + "%";
    track.append(fill);
    line.append(el("span", "n", x.name), track, el("span", "v", fmtN(x.calls)));
    const agents = Object.entries(x.agents || {}).sort((a, b) => b[1] - a[1]);
    const share = agents.map(([a, c]) => `${names[a] || a} ${Math.round(100 * c / x.calls)}%`).join(" · ");
    const where = (x.projects || []).map((p) => baseName(p.name)).join(", ");
    const sub = el("div", "sub", [t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(x.sessions) }),
      x.last ? t("last {when}", { when: sessDay(sessDate(x.last)) }) : "", share, where].filter(Boolean).join(" · "));
    sub.title = (x.projects || []).map((p) => `${p.name} · ${t("{n} calls", { n: fmtN(p.calls) })}`).join("\n");
    r.append(line, sub);
    body.append(r);
  }
  if (su.count > su.top.length) body.append(el("div", "sess-more", t("+{n} more", { n: su.count - su.top.length })));
  box.append(body);
}

// sessBars is a card of folders or models as bars of the tokens each took;
// a click shows that one alone, and a click on it again every one
const SESS_BARS = 6;
function sessBars(box, title, items, cur, choose, name) {
  box.replaceChildren();
  box.append(sessCardHead(title));
  const body = el("div", "sess-bars");
  let shown = items.slice(0, SESS_BARS);
  const picked = cur && items.find((x) => x.v === cur);
  if (picked && !shown.includes(picked)) shown = [...shown.slice(0, SESS_BARS - 1), picked];
  const peak = Math.max(1, ...items.map((x) => x.n));
  for (const x of shown) {
    const r = el("button", "sess-bar" + (x.v === cur ? " on" : ""));
    r.type = "button";
    r.title = [name(x.v) !== x.v ? x.v : "", x.v === cur ? t("Click again to show every one") : t("Click to show only this")].filter(Boolean).join("\n");
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.n / peak).toFixed(1) + "%";
    track.append(fill);
    const fc = x.cost ? fmtCost(x) : "";
    r.append(el("span", "n", name(x.v)), track, el("span", "v", fmtN(x.n)), el("span", "c", fc ? "≈" + fc : ""));
    r.onclick = () => choose(x.v === cur ? "" : x.v);
    body.append(r);
  }
  if (!items.length) body.append(el("div", "sess-none", t("Nothing in this range.")));
  if (items.length > shown.length) body.append(el("div", "sess-more", t("+{n} more", { n: items.length - shown.length })));
  box.append(body);
}

// sessSpent is a session's tokens and cost, as the two right-hand columns
// of its row: the Usage page's list and the Sessions page's (#752)
function sessSpent(s) {
  const tokens = sessTokens(s);
  const num = el("div", "num" + (tokens ? "" : " none"));
  num.append(el("b", "", tokens ? fmtN(tokens) : "—"));
  if (tokens) num.append(el("small", "", t("{a} in · {b} out", { a: fmtN(s.input), b: fmtN(s.output) }) + (s.cache_read ? " · " + t("{n} cached", { n: fmtN(s.cache_read) }) : "")));
  const sc = sessCost(s);
  const cost = el("div", "cost" + (sc === "—" ? " none" : ""), sc);
  if (sc === "—") cost.title = t("No known price for {models}", { models: sessModelNames(s.models.map((m) => m.model)) || "—" });
  else if (s.unpriced) cost.title = t("Not counted: {models}, with no known price", { models: sessModelNames(s.models.filter((m) => !m.priced).map((m) => m.model)) });
  return [num, cost];
}

// A row of the Usage page's list: what the session spent. It opens on the
// Sessions page, where it is resumed, read in full and deleted (#752: the
// two lists were one done twice, each lacking half).
// A row opens its session on the Sessions page. With that page switched off
// (Plugins › Built-in) there is nowhere to open it, so the row unfolds the
// session's details in place, as a top session's do, instead of looking like
// a link and doing nothing when clicked (#2311, betacatZ).
function sessionItem(s) {
  const item = el("div", "sess-item");
  item.sess = s;
  const r = el("div", "row sess sess-link");
  const key = s.agent + ":" + s.id;
  const here = featuresOff.has("sessions");
  const unfolded = here && sessListOpen === key;
  r.tabIndex = 0;
  if (here) {
    r.setAttribute("role", "button");
    r.setAttribute("aria-expanded", String(unfolded));
  } else {
    r.setAttribute("role", "link");
    r.title = t("Open in Sessions");
  }
  r.append(icon(s.icon || "generic"));
  const who = el("div", "who");
  who.append(el("div", "name", s.title || t("(no prompt)")));
  const sub = el("div", "sub", [s.cwd ? baseName(s.cwd) : "", sessModelsText(s), ago(s.last)].filter(Boolean).join(" · "));
  sub.title = [s.cwd, ...(s.via || []).map(viaText)].filter(Boolean).join("\n");
  if (s.wsl) sub.prepend(wslBadge(s), " ");
  who.append(sub);
  r.append(who, ...sessSpent(s));
  const go = el("span", "sess-go");
  go.append(svg(here ? CHEV : CHEV_R, 12, 1.6));
  if (unfolded) go.classList.add("up");
  r.append(go);
  const open = (e) => {
    if (window.getSelection()?.toString()) return;
    if (!featuresOff.has("sessions")) return magpie.call("sessions", "reveal", s, e);
    // Sessions is switched off: there is no page to open it on
    sessListOpen = sessListOpen === key ? "" : key;
    // one open at a time: the one that was folds
    for (const o of item.parentElement?.querySelectorAll(":scope > .sess-item.open") || []) if (o !== item) o.replaceWith(sessionItem(o.sess));
    const next = sessionItem(s);
    item.replaceWith(next);
    next.querySelector(".row.sess").focus({ preventScroll: true });
  };
  r.onclick = open;
  r.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(e); } };
  item.append(r);
  if (unfolded) {
    item.classList.add("open");
    item.append(sessionDetail(s));
  }
  return item;
}

function sessionDetail(s) {
  const d = el("div", "sess-detail");
  const line = (label, value, extra) => {
    const l = el("div", "sess-line");
    l.append(el("span", "k", label));
    const v = el("span", "v", value);
    l.append(v);
    if (extra) l.append(extra);
    d.append(l);
  };
  line(t("Time"), stamp(s.start) + " – " + stamp(s.last));
  if (s.wsl) line("WSL", s.wsl);
  if (s.cwd) line(t("Folder"), s.cwd);
  line(t("Session ID"), s.id, copyBtn(s.id, t("Session id")));
  if (s.resume) {
    const code = el("code", "", s.resume);
    const l = el("div", "sess-line");
    l.append(el("span", "k", t("Resume")), code, copyBtn(s.resume, t("Resume command")));
    d.append(l);
  }
  sessUsageDetail(d, s, line);
  if (s.path) line(t("File"), s.path);
  return d;
}

// All sessions: the Sessions page, on the agent picked here if one is
$("#sessAll").onclick = (e) => magpie.call("sessions", "reveal", sessAgent === "all" ? {} : { agent: sessAgent }, e);
$("#sessQ").oninput = (e) => { sessQuery = e.target.value; if (sessions) renderSessions(); };
$("#sessQ").onkeydown = (e) => { if (e.key === "Escape" && e.target.value) { e.stopPropagation(); e.target.value = ""; sessQuery = ""; if (sessions) renderSessions(); } };

// ---------- settings ----------
//
// Three choices (palette, language, what the tray icon opens) and the facts
// people come looking for:
// the version, where magpie keeps its files, the gateway's address.

// The Usage page reads its numbers again while it is looked at, as often as
// the reader says — off, or every 5 s to a minute (a request through the gateway
// shows within that; the subscriptions' windows each minute at most, the vendors'
// answers being cached behind them, and the sessions each 15 s) — redrawn only
// on a change. The button beside it reads them now.
const USAGE_EVERY = [[0, "Off"], [5, "5 s"], [10, "10 s"], [30, "30 s"], [60, "1 min"]];
let usageEvery = 5; // seconds; 0 is off
try {
  const v = localStorage.getItem("magpie.usageEvery");
  if (USAGE_EVERY.some(([n]) => String(n) === v)) usageEvery = Number(v);
} catch {}
let usageLast = 0, usageReadAt = 0, sessionsAt = 0, quotasAsked = 0;

function renderUsageEvery() {
  const cur = USAGE_EVERY.find(([n]) => n === usageEvery);
  const b = $("#usageEvery");
  b.replaceChildren(el("span", "", t(cur[1])), svg(CHEV, 11, 1.6));
  b.title = t("Refresh every");
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, USAGE_EVERY.map(([n, name]) => ({ v: String(n), name: t(name), note: "" })), String(usageEvery), (v) => {
      usageEvery = Number(v);
      try { localStorage.setItem("magpie.usageEvery", v); } catch {}
      usageLast = performance.now();
      renderUsageEvery();
    }, "Refresh every", "sess-menu");
  };
  const r = $("#usageReload");
  // on the Overview it reads the allowances again too (#486: they had a
  // Refresh of their own beside it)
  const what = usageTab === "usage" ? t("Refresh now, the allowances too; a Claude account's is read by running Claude Code's own /usage") : t("Refresh now");
  r.title = usageReadAt ? what + " · " + t("Updated {time}", { time: new Date(usageReadAt).toLocaleTimeString(intlLang(), { hour12: false }) }) : what;
  r.setAttribute("aria-label", t("Refresh now"));
  r.onclick = async () => {
    r.classList.add("busy");
    try { await refreshUsage(true); } finally { r.classList.remove("busy"); r.blur(); }
  };
}

// refreshUsage reads what the tab shown draws; now is the reader asking, which
// reads the page it is on too, and the sessions and the allowances.
// A read that outlives its interval must not have another started behind it:
// one is in flight at a time, and a read asked for while one is (the timer, or
// the reader's button, which may not be swallowed) is served when it is done,
// the reader's own if either asked for it. The promise covers that second read
// too, so the button turns while the reader's own is still to come.
let usageFlight = null, usageQueued = false, usageQueuedNow = false;
// usageDrawn: the tab shown has its figures on the page already, which a
// quiet read (refreshUsage) only replaces when they changed
function usageDrawn() {
  if (usageTab === "sessions") return !!(sessions && sessStats);
  if (usageTab === "requests") return !!ledger;
  if (usageTab === "context") return true; // loadContext keeps what it drew when nothing changed
  return !!usage && !$("#view-usage").classList.contains("loading");
}
function refreshUsage(now = false) {
  usageLast = performance.now();
  if (usageFlight) {
    usageQueued = true;
    usageQueuedNow = usageQueuedNow || now;
    return usageFlight; // this promise includes the queued read
  }
  const run = async (want) => {
    try {
      if (usageTab === "sessions") {
        if (sessions && (want || performance.now() - sessionsAt > 15e3)) { sessionsAt = performance.now(); await loadSessions(); }
      } else if (usageTab === "requests") {
        // the newest page takes the requests as they come; an older one stays put
        if (ledger && (want || !ledOffset)) await loadLedger(true);
      } else if (usageTab === "context") {
        await window.loadContext?.();
      } else {
        // the reader asking reads the allowances afresh, a Claude account's by
        // running Claude Code's own /usage (the backend runs it at most once in 30s)
        let q = null;
        if (want) { quotasAsked = performance.now(); q = loadQuotas(true); }
        else if (usage && performance.now() - quotasAsked > 60e3) { quotasAsked = performance.now(); loadQuotas(); }
        if (usage) {
          const p = period, read = ++usageRead;
          const u = await api("usage?period=" + encodeURIComponent(p));
          if (read === usageRead && view === "usage" && usageTab === "usage" && p === period) {
            if (JSON.stringify(u) !== JSON.stringify(usage)) { usage = u; renderUsage(); }
            // this read claimed the newest one: a pick it superseded had put the
            // skeleton up, and the figures on hand are what ends it
            else if ($("#view-usage").classList.contains("loading")) renderUsage();
          }
        }
        await q;
      }
      usageReadAt = Date.now();
      renderUsageEvery();
    } catch {}
  };
  const flight = Promise.resolve().then(() => run(now)).then(() => {
    if (usageFlight !== flight) return;
    usageFlight = null;
    if (!usageQueued) return;
    const want = usageQueuedNow;
    usageQueued = usageQueuedNow = false;
    // one more read, the reader's own if either asked for it
    return refreshUsage(want);
  });
  usageFlight = flight;
  return flight;
}
renderUsageEvery();
setInterval(() => {
  if (!usageEvery || view !== "usage" || document.hidden || document.querySelector(".pop:not([hidden])")) return;
  if (performance.now() - usageLast >= usageEvery * 1000) refreshUsage();
}, 1000);
// opened from the tray panel's Usage tab: on the Requests of one provider or agent
if (mode === "window" && params.get("view") === "usage") {
  // An explicit destination takes precedence over the remembered tab.
  const tab = params.get("tab");
  if (tab === "requests" || tab === "usage") usageTab = tab;
  // a total in the menu bar opens the Overview at its period
  const per = params.get("period");
  if (PERIODS.some(([id]) => id === per)) period = per;
  quotaFocus = usageTab === "usage" ? (params.get("card") || params.get("provider") || "") : "";
  quotaFocusUntil = performance.now() + 5000;
  ledProvider = params.get("provider") || "";
  ledAgent = params.get("agent") || "";
  ledPurpose = params.get("purpose") || "";
  ledComputer = params.get("computer") || "";
  const v = params.get("via") || "";
  ledVia = v === "through" || v === "direct" || v === "reported" ? v : "";
  const u = new URL(location.href);
  for (const k of ["tab", "provider", "agent", "computer", "card", "purpose", "via", "period"]) u.searchParams.delete(k);
  history.replaceState(null, "", u);
}
