// Insights › Masking's code (client.js has its markup and hooks): run after
// app.js, whose masking helpers it shares (maskTip, maskColor,
// maskShape, maskValue: Settings' Try it and the masked calls' badges use
// them too; Try it uses this page's maskMatches in turn). What a
// placeholder stood for is in this magpie's memory alone, never on disk,
// so after a restart it is gone; so is the text a request went out with
// (/api/masking/sent), kept for the newest requests only.

const stillMotion = calm;
// maskMatches is text's stand-ins as matchAll gives them: its {{…}}
// placeholders, and the made-up keys "Keep a key's form" sent (redact's
// shape.go), the keys of info that are no placeholder, which only the
// record's own list tells from the text around them (#3599)
const maskLookRes = new WeakMap();
function maskMatches(text, info) {
  let re = maskLookRes.get(info);
  if (!re) {
    const looks = [...info.keys()].filter((p) => p && !p.startsWith("{{")).sort((a, b) => b.length - a.length)
      .map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
    re = looks.length ? new RegExp(PLACEHOLDER.source + "|" + looks.join("|"), "g") : PLACEHOLDER;
    maskLookRes.set(info, re);
  }
  return text.matchAll(re);
}
// the providers' names, read once when the page opened before any page
// that loads them
let maskProv = null, maskProvAsked = false;
function maskWho(id, kind) {
  const x = kind === "agent" ? state.agents?.find((a) => a.id === id) : (providers || maskProv)?.providers?.find((p) => p.id === id);
  return { name: x?.name || id || t("Unknown"), icon: x?.icon };
}
// what a request was, beside a chat (gateway's noteMasked)
const MASK_WHAT = { search: "Web search", image: "Image", video: "Video", decide: "System One", embeddings: "Embeddings", audio: "Audio" };
// maskDayName: Today, Yesterday or the date a masked request went on
function maskDayName(when) {
  const d = new Date(when), now = new Date();
  const days = Math.round((new Date(now.toDateString()) - new Date(d.toDateString())) / 86400000);
  if (days === 0) return t("Today");
  if (days === 1) return t("Yesterday");
  return d.toLocaleDateString(intlLang(), { weekday: "short", month: "short", day: "numeric" });
}

// ---- the summary: over 7 or 30 days, kept per machine ----
let maskSumDays = 0, maskAt = 0, maskBusy = null, maskBusyDays = 0;
let maskDays = 7;
try { if (localStorage.getItem("magpie.maskDays") === "30") maskDays = 30; } catch {}
let maskDrawn = "";   // what the card says, so a redraw that changes nothing is skipped
let maskKeyRead = false; // the address's key=, a masked call opened from the tray panel
// the list: by request or by value, and what it is narrowed to
const maskL = { mode: "req", facet: "", kind: "", agent: "", provider: "", records: [], more: false, busy: false, error: "", read: false };
let maskO = null;     // the request open, while it is

// maskLoad reads the summary for the days shown, when what is kept is
// older than 10s or for other days; forced, it reads again whatever is kept
function maskLoad(force) {
  const days = maskDays;
  if (maskBusy && maskBusyDays === days) return maskBusy;
  if (!force && maskSum && maskSumDays === days && Date.now() - maskAt < 10000) return null;
  maskBusyDays = days;
  const p = maskBusy = api("masking/summary?days=" + days).then((sum) => {
    if (days !== maskDays) return;
    maskSum = { days: [], kinds: [], agents: [], providers: [], values: [], by: {}, n: 0, distinct: 0, requests: 0, ...(sum || {}) };
    maskSumDays = days;
    maskAt = Date.now();
  }).catch(() => {}).finally(() => { if (maskBusy === p) maskBusy = null; });
  return p;
}
const maskSettings = () => prefs || state?.settings || {};
// on as the masker is (masking.Wanted): your own words mask with neither
// switch on
const maskOn = () => { const s = maskSettings(); return !!(s.redact || s.redactPersonal || s.redactWords?.length); };

function maskPageOpen() {
  if (!providers && !maskProvAsked) {
    maskProvAsked = true;
    api("providers").then((r) => { maskProv = r; maskDrawn = ""; maskDrawAll(); }).catch(() => { maskProvAsked = false; });
  }
  // a masked call in the tray panel opens the window on its request
  if (!maskKeyRead) {
    maskKeyRead = true;
    const k = params.get("key");
    if (k) {
      const q = new URLSearchParams(location.search);
      q.delete("key");
      history.replaceState(null, "", "?" + q);
      maskOneOpen(k);
    }
  }
  return maskPageLoad();
}
function maskPageLoad(force) {
  maskDrawAll();
  const sum = maskLoad(force)?.then(() => { drawMaskCard(); drawMaskList(); });
  const recs = maskL.read && force ? maskRefresh() : maskL.read ? null : maskFetch();
  return Promise.all([sum, recs]);
}
function maskDrawAll() {
  drawMaskCard();
  drawMaskList();
  if (maskO) drawMaskOne();
}
// while the page is on screen it keeps up with requests as they go
setInterval(() => {
  if (view !== "masking" || document.hidden) return;
  maskLoad(true)?.then(() => { drawMaskCard(); if (maskL.mode === "val" || !maskO) drawMaskList(); });
  if (!maskL.busy) maskRefresh();
}, 15000);

// ---- the card: how much was kept, and where it went ----
function drawMaskCard() {
  const box = $("#maskCard");
  if (!box) return;
  // until the window's state is read, agents would show as their ids
  const on = maskOn(), sum = maskSumDays === maskDays && load.done ? maskSum : null;
  const key = JSON.stringify([document.documentElement.lang, maskDays, on, !!maskSettings().redactWords?.length, sum, state?.agents?.length, (providers || maskProv)?.providers?.length]);
  if (key === maskDrawn && box.firstChild) return;
  maskDrawn = key;
  // it moves when it first has numbers to say, and for other days: not
  // on every poll
  const move = !!sum && box.dataset.drawn !== String(maskDays) && !stillMotion();
  if (sum) box.dataset.drawn = String(maskDays);
  maskTipOff();
  box.replaceChildren();
  box.classList.remove("off");
  if (sum && !sum.requests && !on) return maskOffCard(box);

  const head = el("div", "msk-head"), words = el("div", "msk-title");
  words.append(el("h2", "", t("Kept from vendors")),
    el("span", "msk-sub", on ? t("Masked on this computer before each request left it, and put back in each answer") : t("Masking is off: requests go to vendors as they are")));
  head.append(words, el("span", "grow"));
  if (!on) {
    const b = el("button", "text", t("Turn on masking"));
    b.type = "button";
    b.onclick = maskTurnOn;
    head.append(b);
  } else {
    // back to its settings (netube99 on X), unless Turn off goes there
    if (!maskSettings().redactWords?.length) {
      const set = el("button", "link", t("Settings") + " › " + $("#setTab-privacy").textContent);
      set.type = "button";
      set.onclick = (e) => openSettingsAt(e, "privacy", "#redactList .row");
      head.append(set);
    }
    head.append(maskOffButton());
  }
  if (sum?.requests || maskDays === 30) head.append(segs([["7", t("7 days")], ["30", t("30 days")]], String(maskDays), (v) => {
    maskDays = +v;
    try { localStorage.setItem("magpie.maskDays", v); } catch {}
    maskLoad(true)?.then(() => { drawMaskCard(); drawMaskList(); });
  }));
  box.append(head);
  const band = el("div", "msk-band");
  box.append(band);
  if (!sum) {
    band.append(el("span", "skeleton msk-sk-num"), el("span", "skeleton msk-sk-flow"));
    return;
  }

  // the number, what makes it up, and the days
  const nums = el("div", "msk-nums");
  const num = el("div", "msk-num", sum.n.toLocaleString(intlLang()));
  nums.append(num, el("div", "msk-unit", t(sum.n === 1 ? "value masked" : "values masked")));
  const stats = el("div", "msk-stats");
  const stat = (label, n, cls) => { const s = el("span", cls || ""); s.append(el("b", "", fmtN(n)), document.createTextNode(label)); stats.append(s); };
  stat(t("Different values"), sum.distinct);
  stat(t("Requests"), sum.requests);
  if (sum.blocked) stat(t("Held back"), sum.blocked, "held");
  nums.append(stats);
  const days = el("div", "msk-days");
  nums.append(days);
  band.append(nums);
  maskStrip(days, sum.days, move);
  if (move && sum.n) maskCountUp(num, sum.n);
  if (sum.agents.length && sum.providers.length) band.append(maskFlow(sum, move));
  else band.append(maskNone());
  if (move) for (const [i, n] of [...band.children].entries()) n.animate([{ opacity: 0, transform: "translateY(6px)", filter: "blur(6px)" }, {}], { duration: 900, delay: i * 120, easing: "cubic-bezier(.2, .7, .1, 1)", fill: "backwards" });
}

// masking off and nothing ever masked: what it would do, and the switch
function maskOffCard(box) {
  box.classList.add("off");
  const s = svg(SHIELD, 40, .9);
  s.classList.add("msk-off-ic");
  const b = el("button", "text primary", t("Turn on masking"));
  b.type = "button";
  b.onclick = maskTurnOn;
  const also = el("span", "msk-also");
  const link = el("button", "text", t("Settings › Privacy"));
  link.type = "button";
  link.onclick = (e) => openSettingsAt(e, "privacy");
  also.append(el("span", "", t("You can also change it in")), link);
  box.append(s, el("h2", "", t("Requests go to vendors as they are")),
    el("p", "", t("Turned on, keys, passwords and tokens become placeholders before they leave this computer, and are put back in each answer. Emails and phone numbers too once Mask personal data is on. What each request had masked shows here.")),
    b, also);
}
// maskNone: what the card says while nothing was masked. With secrets
// alone on, an agent's everyday requests carry nothing to mask (an email
// or a name isn't a secret), so it says that, and where personal data is
// turned on, rather than a 0 that reads as masking not working
function maskNone() {
  const s = maskSettings(), box = el("div", "msk-flow-none");
  if (s.redactPersonal || !s.redact) {
    box.textContent = t("A request that carries a key, a password or personal data shows here as it goes");
    return box;
  }
  box.append(el("span", "", t("Only keys, passwords and tokens are masked now, and a request that carries one shows here as it goes. Emails and phone numbers are masked once Mask personal data is on, in")));
  const link = el("button", "text", t("Settings › Privacy"));
  link.type = "button";
  link.onclick = (e) => openSettingsAt(e, "privacy");
  box.append(" ", link);
  return box;
}
// the way off, beside the way on (ZekeXiao on X: "脱敏好像只有一个打开
// 开关，不能关？"). Both switches go off at once. Your own words mask with neither
// on, and only Settings › Privacy can take them out, so with words the
// button opens it there, and says so, rather than leave the page
// saying off while words still mask
function maskOffButton() {
  const words = maskSettings().redactWords?.length;
  const b = el("button", "text", t(words ? "Turn off in Settings" : "Turn off masking"));
  b.type = "button";
  if (words) {
    b.title = t("Your own masked words still mask until you take them out, in Settings › Privacy");
    b.onclick = (e) => openSettingsAt(e, "privacy");
    return b;
  }
  b.onclick = () => {
    b.disabled = true;
    (prefs ? Promise.resolve() : loadSettings())
      .then(() => {
        // turned on again from this page, it comes back as it was
        maskWasPersonal = !!prefs.redactPersonal;
        return savePrefs({ ...prefsKeep(prefs), redact: false, redactPersonal: false });
      })
      .then(() => { maskDrawn = ""; drawMaskCard(); })
      .finally(() => { b.disabled = false; });
  };
  return b;
}
let maskWasPersonal = false;
function maskTurnOn(e) {
  const b = e.currentTarget;
  b.disabled = true;
  (prefs ? Promise.resolve() : loadSettings())
    .then(() => {
      const back = maskWasPersonal;
      maskWasPersonal = false;
      if (back) window.hideAccounts?.set(true, true);
      return savePrefs({ ...prefsKeep(prefs), redact: true, ...(back ? { redactPersonal: true } : {}) });
    })
    .then(() => { maskDrawn = ""; drawMaskCard(); })
    .finally(() => { b.disabled = false; });
}

function maskCountUp(node, n) {
  const start = performance.now(), ms = 1100;
  const step = (now) => {
    if (!node.isConnected) return;
    const p = Math.min(1, (now - start) / ms);
    const e = p === 1 ? 1 : 1 - Math.pow(2, -10 * p);
    node.textContent = Math.round(n * e).toLocaleString(intlLang());
    if (p < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

// maskStrip: the values masked each day, a bar a day on a hairline, today
// in the accent; the day pointed at said over it
function maskStrip(box, days, move) {
  const total = days.reduce((a, d) => a + d.n, 0);
  box.setAttribute("role", "img");
  box.setAttribute("aria-label", t("{n} values masked over {days} days", { n: fmtN(total), days: days.length }));
  if (!days.length) return;
  const max = Math.max(1, ...days.map((d) => d.n));
  const bars = el("div", "msk-bars" + (days.length > 14 ? " many" : ""));
  const tip = el("div", "msk-ctip");
  tip.hidden = true;
  const fmt = (d) => d === days.at(-1).day ? t("Today") : new Date(d + "T00:00").toLocaleDateString(intlLang(), { month: "short", day: "numeric" });
  days.forEach((d, i) => {
    const b = el("span", "msk-day" + (i === days.length - 1 ? " now" : "") + (d.n ? "" : " zero"));
    const f = el("i");
    if (d.n) f.style.height = Math.max(8, (100 * d.n) / max) + "%";
    b.append(f);
    b.onpointerenter = () => {
      tip.hidden = false;
      tip.textContent = `${fmt(d.day)} · ${t(d.n === 1 ? "1 value masked" : "{n} values masked", { n: fmtN(d.n) })} · ${t(d.requests === 1 ? "1 request" : "{n} requests", { n: fmtN(d.requests) })}`;
      const w = tip.offsetWidth, W = box.clientWidth, x = b.offsetLeft + b.offsetWidth / 2;
      tip.style.left = Math.max(w / 2, Math.min(x, W - w / 2)) + "px";
    };
    b.onpointerleave = () => { tip.hidden = true; };
    if (move && d.n) f.animate([{ transform: "scaleY(0)" }, { transform: "none" }], { duration: 760, delay: 160 + (i * 520) / days.length, easing: "cubic-bezier(.32, .72, 0, 1)", fill: "backwards" });
    bars.append(b);
  });
  const axis = el("div", "msk-axis");
  axis.append(el("span", "", fmt(days[0].day)), el("span", "", fmt(days.at(-1).day)));
  box.append(bars, axis, tip);
}

// maskFlow: hairlines from the agents into this computer, uneven as
// written, and out of it to the providers, even as placeholders; magpie's
// bird on the line between, so a screenshot says who masked them
function maskFlow(sum, move) {
  const sec = el("div", "msk-fl"), flow = el("div", "mo-flow");
  const side = (list, kind) => {
    const shown = list.slice(0, 3), rest = list.slice(3);
    const rows = shown.map((x) => ({ id: x.name, n: x.n, ...maskWho(x.name, kind) }));
    if (rest.length) rows.push({ id: "", n: rest.reduce((a, x) => a + x.n, 0), name: t("{n} more", { n: rest.length }) });
    const col = el("div", "mo-col " + kind);
    for (const r of rows) {
      const b = el("button", "mo-node" + (r.id && maskL[kind] === r.id ? " on" : ""));
      b.type = "button";
      // its logo first, then its name, on both sides; "N more" keeps the
      // logo's room so the names line up
      const ic = r.id ? icon(r.icon || "generic") : el("span", "ic");
      const tx = el("span", "tx");
      tx.append(el("span", "nm", r.name), el("span", "ct", fmtN(r.n)));
      b.append(ic, tx);
      if (r.id) {
        b.title = t("Show what {name} sent masked", { name: r.name });
        b.onclick = () => maskNarrow({ [kind]: maskL[kind] === r.id ? "" : r.id });
      } else b.disabled = true;
      col.append(b);
    }
    return [col, rows];
  };
  const [from, fromRows] = side(sum.agents, "agent");
  const [to, toRows] = side(sum.providers, "provider");
  const mid = el("div", "mo-mid");
  const rowH = 34, H = Math.max(fromRows.length, toRows.length, 3) * rowH;
  const max = Math.max(...fromRows.map((r) => r.n), ...toRows.map((r) => r.n), 1);
  const hub = el("span", "msk-hub");
  hub.title = "magpie";
  hub.innerHTML = '<svg viewBox="0 0 44 44" aria-hidden="true"><use href="#bird"/></svg>';
  // drawn in the middle's own pixels on each resize: a stretched viewBox
  // would bend the hairlines
  const s = sv("svg", { "aria-hidden": "true" });
  let drawnW = 0;
  const draw = () => {
    const W = Math.round(mid.clientWidth);
    if (!W || W === drawnW) return;
    drawnW = W;
    s.setAttribute("viewBox", `0 0 ${W} ${H}`);
    s.replaceChildren();
    const m = W / 2, still = stillMotion(), lines = [];
    const add = (tag, a, to = s) => { const n = sv(tag, a); to.append(n); return n; };
    const curve = (x0, y0, x1, y1) => `M${x0},${y0}C${(x0 + x1) / 2},${y0} ${(x0 + x1) / 2},${y1} ${x1},${y1}`;
    const bundle = (rows, out) => {
      const ks = rows.map((r) => Math.max(1, Math.round(18 * r.n / max))), all = ks.reduce((a, k) => a + k, 0);
      let j = 0, rnd = out ? 11 : 7;
      rows.forEach((r, i) => {
        const y = (H - rows.length * rowH) / 2 + (i + .5) * rowH;
        for (let q = 0; q < ks[i]; q++, j++) {
          const at = all > 1 ? H * (.22 + .56 * j / (all - 1)) : H / 2;
          rnd = (rnd * 16807) % 2147483647;
          const end = y + (q - (ks[i] - 1) / 2) * 1.4 + (out ? 0 : (rnd % 30 - 15) / 10);
          const d = out ? curve(m, at, W, end) : curve(0, end, m, at);
          lines.push(add("path", { d, pathLength: 1, class: "mo-th" + (out ? " out" : "") + (ks[i] === 1 ? " one" : ""), "stroke-opacity": ks[i] === 1 ? (out ? .8 : .4) : out ? .42 : .07 + (rnd % 20) / 100 }));
          // a value goes in round, a placeholder out square
          if (q === ks[i] >> 1 && !still) for (let k = 0, dots = 1 + Math.round(2 * r.n / max); k < dots; k++) {
            const dur = 1.7 + i * .5, begin = (out ? dur : 0) + k * 2 * dur / dots + "s";
            const z = out ? 1.8 : 1.7, dot = add("rect", { x: -z, y: -z, width: 2 * z, height: 2 * z, rx: out ? .6 : z, class: "mo-dot" + (out ? " out" : "") });
            // twice round when drawn, not forever: an endless motion lays the
            // page out each frame, ~16% of a core while it sits open (#3185)
            const a = { dur: dur + "s", begin, repeatCount: 2 };
            add("animateMotion", { path: d, ...a }, dot);
            add("animate", { attributeName: "opacity", values: "0;1;1;0", keyTimes: "0;.1;.85;1", ...a }, dot);
          }
        }
      });
    };
    bundle(fromRows, false);
    bundle(toRows, true);
    // this computer: a ticked line, and a mark going down it
    add("line", { x1: m, y1: 0, x2: m, y2: H, class: "mo-mem" });
    for (let y = 0; y <= H; y += 17) add("line", { x1: m - 3, y1: y, x2: m + 3, y2: y, class: "mo-tick" });
    if (!still) add("rect", { x: m - 1, y: 0, width: 2, height: 18, rx: 1, class: "mo-scan" }).style.setProperty("--h", H - 18 + "px");
    // drawn in once, the way in first
    if (move) lines.forEach((p) => {
      p.animate([{ strokeDashoffset: 1 }, {}], { duration: 2400, delay: p.classList.contains("out") ? 700 : 300, easing: "cubic-bezier(.3, .6, .1, 1)", fill: "backwards" });
    });
    move = false;
  };
  mid.append(s, hub);
  flow.style.height = H + "px";
  flow.append(from, mid, to);
  new ResizeObserver(draw).observe(mid);
  sec.append(flow, el("div", "mo-cap", t("magpie masked these on this computer")));
  return sec;
}

// ---- the list: each request masking changed, or each value ----
const MASK_FACETS = [
  ["", "All", (s) => s.n],
  ["secret", "Secrets", (s) => (s.kinds || []).filter((k) => MASK_SECRET_KINDS.includes(k.name)).reduce((a, k) => a + k.n, 0)],
  ["rule", "Rules", (s) => s.by?.rule || 0],
  ["model", "Local model", (s) => s.by?.model || 0],
  ["blocked", "Held back", (s) => s.blocked || 0],
];
function maskQuery(before) {
  const q = new URLSearchParams({ limit: "40" });
  if (before) q.set("before", before);
  const f = maskL.facet;
  if (maskL.kind) q.set("kind", maskL.kind);
  else if (f === "secret") q.set("kind", MASK_SECRET_KINDS.join(","));
  if (f === "rule" || f === "model") q.set("by", f);
  if (f === "blocked") q.set("blocked", "1");
  if (maskL.agent) q.set("agent", maskL.agent);
  if (maskL.provider) q.set("provider", maskL.provider);
  return "masking/records?" + q;
}
// maskNarrow: the list for other filters, read again from the start
function maskNarrow(change) {
  Object.assign(maskL, change);
  maskDrawn = "";
  drawMaskCard();
  return maskFetch();
}
let maskSeq = 0;
function maskFetch(more) {
  const seq = ++maskSeq;
  maskL.busy = true;
  maskL.error = "";
  drawMaskList();
  return api(maskQuery(more ? maskL.records.at(-1)?.key : 0))
    .then((r) => {
      if (seq !== maskSeq) return;
      const got = r?.records || [];
      maskL.records = more ? [...maskL.records, ...got] : got;
      maskL.more = !!r?.more;
    })
    .catch((e) => { if (seq === maskSeq) maskL.error = e.message; })
    .finally(() => {
      if (seq !== maskSeq) return;
      maskL.busy = false;
      maskL.read = true;
      drawMaskList();
      if (maskO) drawMaskOne(); // its previous and next
    });
}
// new records come in over the ones shown, which keep their place
function maskRefresh() {
  if (!maskL.records.length) return maskFetch();
  const seq = maskSeq;
  return api(maskQuery(0)).then((r) => {
    if (seq !== maskSeq || !maskL.records.length) return;
    const fresh = (r?.records || []).filter((x) => BigInt(x.key) > BigInt(maskL.records[0].key));
    if (!fresh.length) return;
    maskL.records = [...fresh, ...maskL.records];
    if (!maskO) drawMaskList();
    else drawMaskOne();
  }).catch(() => {});
}

function drawMaskList() {
  const box = $("#maskList");
  if (!box) return;
  const sum = (maskSumDays === maskDays && maskSum) || null;
  maskTipOff();
  box.replaceChildren();
  const bar = el("div", "msk-bar");
  bar.append(segs([["req", t("By request")], ["val", t("By value")]], maskL.mode, (v) => { maskL.mode = v; drawMaskList(); }));
  const live = el("span", "msk-live");
  live.title = t("Read again every 15 seconds while this page is open");
  live.append(el("i"), el("span", "", t("Live")));
  bar.append(live, el("span", "grow"));
  if (maskL.mode === "req") {
    const pick = (key, all, head, opts) => {
      const b = el("button", "sess-pick");
      b.type = "button";
      if (maskL[key] && !opts.some((o) => o.v === maskL[key])) opts.unshift({ v: maskL[key], name: maskWho(maskL[key], key).name });
      sessPick(b, all, maskL[key], opts, head, (v) => maskNarrow({ [key]: v || "" }));
      bar.append(b);
    };
    pick("agent", "Every agent", "Agent", (sum?.agents || []).map((a) => ({ v: a.name, name: maskWho(a.name, "agent").name })));
    pick("provider", "Every provider", "Provider", (sum?.providers || []).map((p) => ({ v: p.name, name: maskWho(p.name, "provider").name })));
  }
  box.append(bar);

  // what was masked and how: counts that narrow the list
  const facets = el("div", "msk-facets");
  facets.setAttribute("role", "group");
  facets.setAttribute("aria-label", t("Show"));
  for (const [id, name, count] of MASK_FACETS) {
    const n = sum ? count(sum) : 0;
    if (id && !n && maskL.facet !== id) continue;
    const b = el("button", "msk-facet" + (id ? " " + id : ""));
    b.type = "button";
    b.setAttribute("aria-pressed", String(maskL.facet === id && !maskL.kind));
    if (id) b.append(el("span", "msk-fdot"));
    b.append(el("span", "", t(name)));
    if (sum) b.append(el("b", "", fmtN(n)));
    b.onclick = () => maskNarrow({ facet: id, kind: "" });
    facets.append(b);
  }
  if (maskL.kind) {
    const b = el("button", "msk-facet kind");
    b.type = "button";
    b.setAttribute("aria-pressed", "true");
    b.title = t("Clear filter");
    b.append(maskDot(maskL.kind), el("span", "", maskKindName(maskL.kind)), el("span", "x", "×"));
    b.onclick = () => maskNarrow({ kind: "" });
    facets.append(b);
  }
  box.append(facets);
  if (maskL.mode === "val") maskValueList(box, sum);
  else maskRequestList(box);
  const foot = el("div", "msk-foot");
  foot.append(el("span", "", t("Kept on this computer for 30 days, without the values themselves")));
  if (maskL.mode === "req" && maskL.more) {
    const more = el("button", "text", maskL.busy ? t("Loading…") : t("Load more"));
    more.type = "button";
    more.disabled = maskL.busy;
    more.onclick = () => maskFetch(true);
    foot.append(more);
  }
  // what was kept goes whole, after a confirm: maxyou2090's strict masking
  // had kept far more than he wanted to keep
  if (maskL.records.length || maskSum?.requests) {
    const clear = el("button", "text msk-clear", t("Clear records…"));
    clear.type = "button";
    clear.disabled = maskL.busy || maskClearing;
    clear.onclick = maskClear;
    foot.append(clear);
  }
  box.append(foot);
}
let maskClearing = false;
async function maskClear() {
  const ok = await confirmAction(t("Clear masking records?"),
    t("Every request's record of what was masked is deleted from this computer. Masking stays as it is, and requests from now on are recorded again."),
    t("Clear"));
  if (!ok) return;
  maskClearing = true;
  drawMaskList();
  try {
    await api("masking/clear", {});
    status(t("Masking records cleared"), "ok", 1500);
  } catch (e) {
    // what could not be deleted is still listed below
    status(t(e.message), "err");
  }
  maskClearing = false;
  if (maskO) maskOneClose();
  maskSeq++;
  Object.assign(maskL, { records: [], more: false, busy: false, read: false, error: "" });
  maskDrawn = "";
  return maskPageLoad(true);
}

function maskRequestList(box) {
  const filtered = maskL.facet || maskL.kind || maskL.agent || maskL.provider;
  if (maskL.error) return void box.append(el("div", "msk-none err", t(maskL.error)));
  if (!maskL.records.length) return void box.append(el("div", "msk-none", maskL.busy || !maskL.read ? t("Loading…") : filtered ? t("Nothing masked matches these filters") : t("Nothing masked yet")));
  let day = "", list = null;
  for (const r of maskL.records) {
    const d = new Date(r.t).toDateString();
    if (d !== day) {
      day = d;
      const g = el("div", "msk-group");
      g.append(el("div", "msk-gday", maskDayName(r.t)));
      list = el("div", "list");
      g.append(list);
      box.append(g);
    }
    list.append(maskRecordRow(r));
  }
}
// who to whom, an icon where there is one
function maskRoute(into, r) {
  const a = maskWho(r.agent, "agent"), p = maskWho(r.provider, "provider");
  if (r.agent) {
    if (a.icon) into.append(icon(a.icon));
    into.append(el("span", "who", a.name), el("span", "arrow", "→"));
  }
  if (p.icon && r.provider) into.append(icon(p.icon));
  into.append(el("span", "who", r.provider ? p.name : t("A vendor")));
}
function maskKindsOf(r) {
  const by = {};
  for (const x of r.values || []) by[x.k] = (by[x.k] || 0) + (x.n || 1);
  return Object.entries(by).sort((x, y) => y[1] - x[1]);
}
function maskRecordRow(r) {
  const row = el("button", "msk-row" + (r.blocked ? " held" : ""));
  row.type = "button";
  row.dataset.key = r.key;
  const route = el("span", "msk-route");
  maskRoute(route, r);
  const model = el("span", "msk-model", r.model || "");
  const kinds = el("span", "msk-kinds");
  if (r.blocked) kinds.append(el("span", "msk-held", t("Held back")));
  else if (r.what && r.what !== "chat") kinds.append(el("span", "msk-what", MASK_WHAT[r.what] ? t(MASK_WHAT[r.what]) : r.what));
  const ranked = maskKindsOf(r);
  for (const [k, n] of ranked.slice(0, 3)) {
    const c = el("span", "msk-kind");
    c.append(maskDot(k), el("span", "", maskKindName(k)), el("b", "", "×" + fmtN(n)));
    kinds.append(c);
  }
  if (ranked.length > 3) kinds.append(el("span", "msk-kind more", "+" + (ranked.length - 3)));
  const n = el("span", "msk-n", fmtN(r.n));
  n.title = t(r.n === 1 ? "1 value masked" : "{n} values masked", { n: fmtN(r.n) });
  const chev = el("span", "msk-chev");
  chev.append(svg(CHEV_R, 11, 1.6));
  row.append(el("span", "msk-time", new Date(r.t).toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit" })), route, model, kinds, n, chev);
  row.onclick = () => maskOneOpen(r.key, r);
  return row;
}

// the values masked most, as the summary ranks them: what a facet can
// narrow them by; one opens the requests with its kind
function maskValueList(box, sum) {
  const f = maskL.facet;
  const vs = (sum?.values || []).filter((v) => (!maskL.kind || v.kind === maskL.kind) && (f !== "secret" || MASK_SECRET_KINDS.includes(v.kind)) && (f !== "rule" && f !== "model" || v.by === f) && f !== "blocked");
  if (!sum) return void box.append(el("div", "msk-none", t("Loading…")));
  if (!vs.length) return void box.append(el("div", "msk-none", sum.values?.length ? t("Nothing masked matches these filters") : t("Nothing masked yet")));
  const head = el("div", "msk-vhead");
  head.append(el("span", "", t("Masked most")), el("span", "grow"), el("span", "", t("Point at one to see what it was")));
  const list = el("div", "list msk-vals"), max = vs[0].n || 1;
  for (const [i, v] of vs.entries()) {
    const c = el("button", "msk-vrow"), sh = maskShape(v.head, v.len), bar = el("span", "bar"), fill = el("i");
    c.type = "button";
    c.setAttribute("aria-label", maskKindName(v.kind) + " " + sh);
    fill.style.width = Math.max(3, (100 * v.n) / max) + "%";
    bar.append(fill);
    const kind = el("span", "k");
    kind.append(maskDot(v.kind), el("span", "", maskKindName(v.kind)));
    c.append(el("span", "i", String(i + 1).padStart(2, "0")), el("span", "sh", sh), kind, el("span", "by", maskByName(v.by)), bar, el("span", "x", fmtN(v.n)));
    maskTipOn(c, { p: v.p, kind: v.kind, by: v.by, meta: t(v.requests === 1 ? "In 1 request, {when}" : "In {n} requests, most recently {when}", { n: fmtN(v.requests), when: ago(v.last) }) });
    c.onclick = () => { maskL.mode = "req"; maskNarrow({ kind: v.kind, facet: "" }); };
    list.append(c);
  }
  box.append(head, list);
}

// ---- one request: what it carried, what the vendor got, how ----
function maskOneOpen(key, r) {
  if (!key) return;
  maskTipOff();
  const o = maskO = { key, r: r || maskL.records.find((x) => x.key === key) || null, sent: undefined, leaves: null, vals: new Map(), view: "both", pick: "", open: new Set(), whole: new Set() };
  $("#maskCard").hidden = $("#maskList").hidden = $("#maskDemo").hidden = true;
  xqd?.wake();
  $("#maskOne").hidden = false;
  drawMaskOne();
  (o.r ? Promise.resolve(o.r) : api("masking/record?key=" + key))
    .then((rec) => {
      if (maskO !== o) return;
      o.r = rec;
      drawMaskOne();
      const ps = (rec.values || []).map((x) => x.p);
      const vals = ps.length ? api("masking/values", { ps }).then((x) => {
        for (const [p, v] of Object.entries(x?.values || {})) { o.vals.set(p, v); maskValues.set(p, v); }
      }).catch(() => {}) : null;
      const sent = api("masking/sent?key=" + key).then((x) => (typeof x?.body === "string" ? x.body : null), () => null);
      return Promise.all([sent, vals]).then(([body]) => {
        if (maskO !== o) return;
        o.sent = body;
        o.leaves = body === null ? null : maskLeaves(body);
        if (body === null && !o.vals.size) o.view = "vendor";
        drawMaskOne();
      });
    })
    .catch(() => { if (maskO === o) { o.r = false; drawMaskOne(); } });
}
function maskOneClose() {
  maskTipOff();
  maskO = null;
  $("#maskOne").hidden = true;
  $("#maskOne").replaceChildren();
  $("#maskCard").hidden = $("#maskList").hidden = $("#maskDemo").hidden = false;
  xqd?.wake();
  drawMaskList();
}

// maskLeaves: each piece of text in the body a request went out with,
// where it sits and whose it is (the message's role, else its type)
function maskLeaves(body) {
  let root;
  try { root = JSON.parse(body); } catch { return [{ path: "", role: "", text: body }]; }
  const out = [];
  const walk = (v, path, role) => {
    if (typeof v === "string") return void out.push({ path, role, text: v });
    if (!v || typeof v !== "object") return;
    if (Array.isArray(v)) return void v.forEach((x, i) => walk(x, path + "[" + i + "]", role));
    const r = typeof v.role === "string" ? v.role : role || (typeof v.type === "string" ? v.type : "");
    for (const [k, x] of Object.entries(v)) walk(x, path ? path + "." + k : k, r);
  };
  walk(root, "", "");
  return out;
}
const maskTook = (us) => us >= 1e6 ? (us / 1e6).toFixed(1) + " s" : us >= 1000 ? (us / 1000).toFixed(us >= 1e4 ? 0 : 1) + " ms" : Math.max(1, Math.round(us)) + " µs";

function drawMaskOne() {
  const box = $("#maskOne"), o = maskO;
  if (!box || !o) return;
  maskTipOff();
  box.replaceChildren();
  const r = o.r;
  // back, when, and the requests beside it in the list
  const bar = el("div", "msk-onebar");
  const back = el("button", "text msk-back");
  back.type = "button";
  back.append(svg("m9.5 3.5-4.5 4.5 4.5 4.5", 12, 1.7), el("span", "", t("Masking")));
  back.onclick = maskOneClose;
  bar.append(back);
  if (r) bar.append(el("span", "msk-crumb", "/"), el("span", "msk-when", maskDayName(r.t) + " " + new Date(r.t).toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
  bar.append(el("span", "grow"));
  const at = maskL.records.findIndex((x) => x.key === o.key);
  const step = (d, label, path) => {
    const b = el("button", "icon msk-step");
    b.type = "button";
    b.setAttribute("aria-label", t(label));
    b.dataset.tt = "";
    b.append(svg(path, 12, 1.7));
    const to = at < 0 ? null : maskL.records[at + d];
    b.disabled = !to;
    if (to) b.onclick = () => maskOneOpen(to.key, to);
    bar.append(b);
  };
  step(-1, "Newer request", "M3.5 9.5 8 5l4.5 4.5");
  step(1, "Older request", "M3.5 6.5 8 11l4.5-4.5");
  box.append(bar);
  if (r === false) return void box.append(el("div", "msk-none", t("This request's record is gone: records are kept for 30 days")));
  if (!r) return void box.append(el("div", "msk-none", t("Loading…")));

  const info = new Map((r.values || []).map((x) => [x.p, x]));
  // the request: who to whom, and what happened to it in order
  const card = el("section", "msk-req");
  const who = el("div", "msk-who");
  maskRoute(who, r);
  if (r.model) who.append(el("span", "msk-chip", r.model));
  if (r.what && r.what !== "chat") who.append(el("span", "msk-chip", MASK_WHAT[r.what] ? t(MASK_WHAT[r.what]) : r.what));
  card.append(who, maskSteps(r, o));
  box.append(card);
  if (r.blocked) box.append(maskBlocked(r));

  const grid = el("div", "msk-onegrid");
  grid.append(maskContent(r, o, info), maskHow(r, o, info));
  box.append(grid);
  maskPick(box, o.pick);
}

// maskSteps: received, found, replaced, sent (or held back), put back
function maskSteps(r, o) {
  const ol = el("ol", "msk-steps");
  ol.setAttribute("aria-label", t("What happened"));
  const memory = o.sent || o.vals.size || Object.keys(r.snips || {}).length;
  const back = Object.values(r.back || {}).reduce((a, n) => a + n, 0);
  const by = [...new Set((r.values || []).map((x) => maskByName(x.b)))].join(", ");
  const a = maskWho(r.agent, "agent").name, p = r.provider ? maskWho(r.provider, "provider").name : t("A vendor");
  const steps = [
    ["", t("Received"), r.agent ? t("From {agent}", { agent: a }) + (o.sent ? " · " + fmtBytes(new Blob([o.sent]).size) : "") : o.sent ? fmtBytes(new Blob([o.sent]).size) : ""],
    ["", t((r.values || []).length === 1 ? "Found 1 value" : "Found {n} values", { n: fmtN((r.values || []).length) }), by + (r.took ? " · " + maskTook(r.took) : "")],
    ["", t(r.n === 1 ? "Replaced 1 place" : "Replaced {n} places", { n: fmtN(r.n) }), t("With placeholders")],
    r.blocked ? ["held", t("Held back"), t("Not sent to {provider}", { provider: p })] : ["", t("Sent"), p + (r.model ? " · " + r.model : "")],
    r.blocked ? ["none", t("Put back"), t("Nothing was sent")]
      : back ? ["", t(back === 1 ? "Put back 1" : "Put back {n}", { n: fmtN(back) }), t("In the answer, before {agent} got it", { agent: a })]
      : memory ? ["none", t("Nothing to put back"), t("The answer named no placeholder")]
      : ["none", t("Put back"), t("Not known after a restart")],
  ];
  steps.forEach(([cls, title, sub], i) => {
    const li = el("li", cls);
    const mk = el("div", "msk-sdot");
    mk.append(el("span", "", String(i + 1)));
    li.append(mk, el("div", "msk-st", title), el("div", "msk-ss", sub));
    ol.append(li);
  });
  return ol;
}

// a request held back: why, and the way to send it masked instead
function maskBlocked(r) {
  const box = el("section", "msk-blocked");
  const head = el("div", "msk-bhead"), ic = svg(SHIELD, 20, 1.3);
  ic.append(sv("path", { d: "m6 6 4 4M10 6l-4 4" }));
  head.append(ic, el("h2", "", t("This request was not sent")));
  const kinds = [...new Set((r.values || []).filter((x) => x.a === "block").map((x) => x.k))];
  const a = maskWho(r.agent, "agent").name, p = r.provider ? maskWho(r.provider, "provider").name : t("A vendor");
  box.append(head, el("p", "", t("It carried {kinds}, which you set to hold back, so magpie didn't send it to {provider}. {agent} got an error saying why.", { kinds: kinds.map(maskKindName).join(", ") || t("a value"), provider: p, agent: r.agent ? a : t("The agent") })));
  const tools = el("div", "msk-btools");
  if (kinds.length) {
    const b = el("button", "text primary", t("Mask it instead"));
    b.type = "button";
    b.title = t("From now on {kinds} go to vendors as placeholders, like the other values", { kinds: kinds.map(maskKindName).join(", ") });
    b.onclick = () => {
      b.disabled = true;
      (prefs ? Promise.resolve() : loadSettings())
        .then(() => writingPrefs(api("settings/redact-actions", { actions: { ...(prefs.redactActions || {}), ...Object.fromEntries(kinds.map((k) => [k, "mask"])) } })))
        .then((ns) => { prefs = ns; if (state) state.settings = ns; b.textContent = t("Masked from now on"); status(t("Saved"), "ok", 1500); })
        .catch((e) => { b.disabled = false; status(t(e.message), "err"); });
    };
    tools.append(b);
  }
  const rules = el("button", "text", t("See the rules in Settings"));
  rules.type = "button";
  rules.onclick = (e) => openSettingsAt(e, "privacy", $("#maskActList"));
  tools.append(rules);
  box.append(tools);
  return box;
}

// maskInk: text with this request's placeholders marked, as the vendor
// got them or, here, as they were written
function maskInk(into, text, here, o, info) {
  let at = 0;
  for (const m of maskMatches(text, info)) {
    const x = info.get(m[0]);
    if (!x) continue;
    into.append(document.createTextNode(text.slice(at, m.index)));
    let mark;
    if (here) {
      const v = o.vals.get(m[0]);
      mark = el("mark", "msk-orig" + (typeof v === "string" ? "" : " gone"), typeof v === "string" ? v : maskShape(x.h, x.l));
      mark.style.setProperty("--k", maskColor(x.k));
      mark.tabIndex = 0;
      maskTipOn(mark, { p: m[0], kind: x.k, by: x.b });
    } else mark = maskPlace("msk-ph", m[0], x);
    mark.dataset.p = m[0];
    maskActsOn(mark, x, o);
    into.append(mark);
    at = m.index + m[0].length;
  }
  into.append(document.createTextNode(text.slice(at)));
}
// maskActsOn: a value marked in the text, while this magpie still knows
// it, opens a menu on a click (or Enter): Copy it, or let it go as
// written from now on (yew_fence on Discord: a value masked by mistake
// could be neither copied nor let go from here)
function maskActsOn(mark, x, o) {
  if (typeof o.vals.get(x.p) !== "string") return;
  mark.classList.add("acts");
  mark.setAttribute("role", "button");
  mark.setAttribute("aria-haspopup", "menu");
  mark.onclick = (e) => { e.stopPropagation(); maskValueMenu(mark, x, o); };
  mark.onkeydown = (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    e.stopPropagation();
    maskValueMenu(mark, x, o);
  };
}
// maskValueMenu: Copy puts the value itself on the clipboard; Never mask
// adds it to Settings › Privacy's list of that name (/api/masking/allow,
// the same list), so new messages send it as written while the ones sent
// before stay masked and the vendor's cache of them still matches
// (redact's allowkept.go, NicoleS76448271 on X). A value of the user's
// own Masked words, or one in the list already, can't be added.
function maskValueMenu(anchor, x, o) {
  const again = agentMenu?.anchor === anchor;
  closeAgentMenu();
  maskTipOff();
  if (again) return;
  const v = o.vals.get(x.p);
  if (typeof v !== "string") return;
  const s = prefs || state?.settings || {};
  const had = (s.redactAllow || []).some((a) => a.trim().toLowerCase() === v.trim().toLowerCase());
  const why = x.b === "word" ? t("One of your Masked words") : had ? t("In Never mask already") : "";
  openRowMenu(anchor, [
    { name: "Copy", icon: COPY_ICON, run: () => copy(v, "", null, t("Value copied")) },
    { name: "Never mask", icon: EYE, off: !!why, why, run: () => {
      writingPrefs(api("masking/allow", { p: x.p }))
        .then((ns) => { prefs = ns; if (state) state.settings = ns; status(t("Sent as written in new messages; earlier ones stay masked, so the cache still holds"), "ok"); })
        .catch((e) => status(t(e.message), "err"));
    } },
  ]);
}

// the parts of a long text around its placeholders, 120 characters each
// side, the rest left out
function maskWindows(text, info) {
  const rs = [];
  for (const m of maskMatches(text, info)) if (info.has(m[0])) rs.push([Math.max(0, m.index - 120), Math.min(text.length, m.index + m[0].length + 120)]);
  const out = [];
  for (const r of rs) {
    const last = out.at(-1);
    if (last && r[0] <= last[1] + 20) last[1] = Math.max(last[1], r[1]);
    else out.push([...r]);
  }
  return out;
}

// maskContent: the request's text, only the pieces masking changed, the
// rest folded; as written, as the vendor got it, or side by side
function maskContent(r, o, info) {
  const sec = el("section", "msk-content");
  const head = el("div", "msk-chead");
  head.append(el("h2", "", t("Request content")), el("span", "msk-hint", t("Only the parts masking changed")), el("span", "grow"));
  const known = o.sent || o.vals.size;
  const views = [["here", t("Original")], ["vendor", t("What the vendor saw")], ["both", t("Side by side")]];
  const sg = segs(views, o.view, (v) => { o.view = v; drawMaskOne(); });
  if (!known && o.sent !== undefined) for (const b of sg.querySelectorAll("button")) if (!b.getAttribute("aria-pressed") || b.getAttribute("aria-pressed") === "false") b.disabled = true;
  head.append(sg);
  sec.append(head);
  const view = known || o.sent === undefined ? o.view : "vendor";
  if (o.sent === undefined) return sec.append(el("div", "msk-none", t("Loading…"))), sec;

  let items;
  if (o.leaves) {
    items = [];
    let fold = null;
    for (const l of o.leaves) {
      const changed = [...maskMatches(l.text, info)].some((m) => info.has(m[0]));
      if (changed) { fold = null; items.push({ leaf: l }); continue; }
      if (!fold) items.push(fold = { fold: [] });
      fold.fold.push(l);
    }
  } else {
    // only the lines around each value are left, while memory has the
    // record: the request's whole text was in memory, and is no longer
    items = maskSnipTexts(r).map((text) => ({ leaf: { path: "", role: "", text } }));
    if (items.length && !r.blocked) sec.append(el("p", "msk-gone", o.vals.size
      ? t("This request's whole text is no longer in memory: magpie keeps it for the newest requests only. The lines around each value are still here.")
      : t("What was written is kept in memory only, and magpie has restarted since. Still here: each value's kind, length and first characters, and the line around each placeholder as the vendor got it.")));
    // after a restart nothing of the text is left, by design: the record on
    // disk never holds it (maskrec), so no folder or volume brings it back
    // (brianeee2 on X, in Docker). A request held back sent nothing, and
    // its card says so.
    else if (!items.length && !r.blocked) sec.append(el("p", "msk-gone", t("This request's text and the values masked in it were kept in memory only, never on disk, and magpie has restarted since. The record keeps each value's kind, length and first characters.")));
  }
  const list = el("div", "msk-msgs" + (view === "both" ? " both" : ""));
  if (view === "both") {
    const cols = el("div", "msk-cols");
    cols.append(el("span", "", t("As written here")), el("span", "", t("What the vendor saw")));
    list.append(cols);
  }
  items.forEach((it, i) => {
    if (it.fold) {
      const size = it.fold.reduce((a, l) => a + l.text.length, 0);
      const f = el("div", "msk-fold" + (o.open.has(i) ? " open" : ""));
      const b = el("button", "msk-foldb");
      b.type = "button";
      b.setAttribute("aria-expanded", String(o.open.has(i)));
      b.append(svg(CHEV_R, 10, 1.8), el("span", "", t(it.fold.length === 1 ? "1 part unchanged" : "{n} parts unchanged", { n: fmtN(it.fold.length) })), el("span", "msk-size", fmtBytes(size)));
      b.onclick = () => { if (o.open.has(i)) o.open.delete(i); else o.open.add(i); drawMaskOne(); };
      f.append(b);
      if (o.open.has(i)) {
        const rest = el("div", "msk-same");
        for (const l of it.fold.slice(0, 40)) {
          const p = el("div", "msk-sameline");
          p.append(el("span", "msk-path", l.path), el("span", "", l.text.length > 400 ? l.text.slice(0, 400) + "…" : l.text));
          rest.append(p);
        }
        if (it.fold.length > 40) rest.append(el("div", "msk-path", t("{n} more", { n: fmtN(it.fold.length - 40) })));
        f.append(rest);
      }
      return list.append(f);
    }
    const l = it.leaf, m = el("div", "msk-msg");
    const n = [...maskMatches(l.text, info)].filter((x) => info.has(x[0])).length;
    const mh = el("div", "msk-mhead");
    if (l.role) mh.append(el("span", "msk-role", l.role));
    if (l.path) mh.append(el("span", "msk-path", l.path));
    mh.append(el("span", "grow"), el("span", "msk-count", t(n === 1 ? "1 value" : "{n} values", { n: fmtN(n) })));
    m.append(mh);
    const whole = o.whole.has(i), wins = whole || l.text.length <= 600 ? [[0, l.text.length]] : maskWindows(l.text, info);
    const col = (here) => {
      const c = el("div", "msk-text");
      wins.forEach(([a, b], j) => {
        if (j || a) c.append(el("span", "msk-gap", "…"));
        maskInk(c, l.text.slice(a, b), here, o, info);
      });
      if (wins.at(-1)[1] < l.text.length) c.append(el("span", "msk-gap", "…"));
      return c;
    };
    const body = el("div", "msk-mbody");
    if (view === "both") body.append(col(true), col(false));
    else body.append(col(view === "here"));
    m.append(body);
    if (l.text.length > 600) {
      const b = el("button", "text msk-whole", whole ? t("Show only the changes") : t("Show all of it"));
      b.type = "button";
      b.onclick = () => { if (whole) o.whole.delete(i); else o.whole.add(i); drawMaskOne(); };
      m.append(b);
    }
    list.append(m);
  });
  sec.append(list);
  return sec;
}

// maskHow: each value, what it became, who found it, where it was, and
// how often the answer had it put back; picked, its places light up
function maskHow(r, o, info) {
  const aside = el("aside", "msk-how");
  const vs = r.values || [];
  const head = el("div", "msk-chead");
  head.append(el("h2", "", t("How it was masked")), el("span", "msk-hint", t(vs.length === 1 ? "1 value" : "{n} values", { n: fmtN(vs.length) }) + " · " + t(r.n === 1 ? "1 place" : "{n} places", { n: fmtN(r.n) })));
  aside.append(head);
  const where = new Map();
  for (const l of o.leaves || []) for (const m of maskMatches(l.text, info)) if (info.has(m[0]) && l.path) {
    const w = where.get(m[0]) || new Set();
    w.add(l.path);
    where.set(m[0], w);
  }
  const memory = o.sent || o.vals.size;
  for (const x of vs) {
    const b = el("button", "msk-val");
    b.type = "button";
    b.dataset.p = x.p;
    b.style.setProperty("--k", maskColor(x.k));
    b.setAttribute("aria-pressed", String(o.pick === x.p));
    const top = el("div", "msk-vtop");
    top.append(maskDot(x.k), el("span", "msk-vk", maskKindName(x.k)), el("span", "grow"));
    if (x.a === "warn") top.append(el("span", "mr-act warn", t("Sent as written")));
    else if (x.a === "block") top.append(el("span", "mr-act block", t("Held back")));
    top.append(el("span", "msk-vx", "×" + fmtN(x.n || 1)));
    const v = o.vals.get(x.p), shown = o.pick === x.p && typeof v === "string" ? v : maskShape(x.h, x.l);
    const mid = el("div", "msk-vmid");
    mid.append(el("span", "msk-vshown", shown), svg("M1 8h11M9 5l3 3-3 3", 12, 1.3), el("span", "msk-vph", x.a === "warn" ? t("as written") : x.p));
    const meta = el("dl", "msk-vmeta");
    const add = (k, val) => meta.append(el("dt", "", k), el("dd", "", val));
    add(t("Found by"), maskByName(x.b));
    const w = [...(where.get(x.p) || [])];
    if (w.length) add(t("Where"), w[0] + (w.length > 1 ? " +" + (w.length - 1) : ""));
    const n = r.back?.[x.p] || 0;
    add(t("In the answer"), n ? t(n === 1 ? "Put back 1" : "Put back {n}", { n: fmtN(n) }) : r.blocked ? "—" : memory || r.back ? t("Not named") : t("Not known after a restart"));
    b.append(top, mid, meta);
    b.onclick = () => { o.pick = o.pick === x.p ? "" : x.p; drawMaskOne(); };
    b.oncontextmenu = (e) => { if (typeof o.vals.get(x.p) !== "string") return; e.preventDefault(); maskValueMenu(b, x, o); };
    aside.append(b);
  }
  aside.append(el("p", "msk-note", t("A value itself is in this computer's memory only: shown while it is pointed at or picked, and gone once magpie restarts. The record on disk keeps its kind, length and first characters.")));
  return aside;
}
// a value picked lights its places; the rest fade
function maskPick(box, p) {
  box.classList.toggle("picking", !!p);
  for (const m of box.querySelectorAll("mark[data-p]")) m.classList.toggle("lit", m.dataset.p === p);
}
