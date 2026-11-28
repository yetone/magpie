// The Usage page's allowances as a table (zysicyj on X: a dozen-plus
// accounts are hard to scan as cards): a row an account, the subscription
// and the account first, then its windows' used (or left) share with the
// time they start again, the same readings the cards draw. app.js imports
// this once the table is chosen (quotaTableJS), so it weighs nothing on the
// core.
//
// The columns are the windows most accounts have ("5 hours", "7 days");
// the rest of an account's go in the last column. A click on a head sorts
// by it, again the other way, a third time back to the cards' order.
//
// A column is how long its windows run, not what they are called
// (normalcoder_ on X, then LotusDecoder: Volcengine's "Weekly", Zhipu's
// "MCP · Month" and Antigravity's pools' 5 hours and 7 days went in
// Other). A window whose length magpie knows (the period it sends, else
// the window's name: "5 hours", "Weekly", "MCP · Month") goes in that
// length's column, and a month is one column whatever its days (Mistral's
// "API · Month" goes in Month). Windows of one length on one account
// share its cell, each named by what isn't its length: Antigravity's
// "Gemini" and "Claude & GPT", Zhipu's "MCP". A window of no known length
// goes by its name; the lengths' columns come first. A cell holding more
// than one, or one named otherwise than its column, names each.
//
// A provider with several accounts is one row (jay68xu on X, #6740): its
// name and how many, and each column's share averaged over its accounts
// (the pool as a whole). Its accounts are rows under it, folded until it
// is clicked, and what is open is remembered (magpie.quotaTableOpen, by
// provider), as the cards remember theirs. A column with nothing in any
// row (Account, when no row names one) is left out.
//
// An account with a daily check-in says how today's went under its name,
// as its card does, and the vendor's first row has Auto check-in (and
// Check in now while one isn't in): in the table they were gone, so a
// WorkBuddy user who picked it lost the switch (Haoxiang581607 on X). A
// folded provider's row has the switch alone, its accounts' days being
// under it.
const TABLE_WINDOWS = 4;
let sorted = null; // { key, down }
const opened = new Set(kept("quotaTableOpen", []));

// the figure a window sorts and bars by: the share as the page shows it
// (used, or left), -1 when it has none
const share = (w) => (!w || w.unlimited || quotaNoShare(w) ? -1 : quotaFill(w));
// a window's span in hours from its name ("5 hours", "5h", "Weekly",
// "Month"), Infinity when its name says none
const spanOf = (name) => {
  const n = (name || "").trim().toLowerCase();
  const word = { daily: 24, day: 24, weekly: 168, week: 168, monthly: 720, month: 720 }[n];
  if (word) return word;
  const m = /^(\d+)\s*(h|hours?|d|days?|w|weeks?)$/.exec(n);
  return m ? m[1] * { h: 1, d: 24, w: 168 }[m[2][0]] : Infinity;
};
// a name's parts, "MCP · Month" as MCP and Month
const partsOf = (name) => (name || "").split(" · ");
// hoursOf: how long a window runs in hours, by the period magpie sends,
// else by its name or a part of it ("MCP · Month"); a month of any
// length is 720; Infinity when not known
function hoursOf(w) {
  const h = w.period > 0 ? Math.round(w.period / 3600) : Math.min(...partsOf(w.window || w.name).map(spanOf));
  return h >= 28 * 24 && h <= 31 * 24 ? 720 : h;
}
// labelOf: what a window is called in its length's column: its pool, else
// its name less the part saying its length ("MCP" of "MCP · Month"); ""
// for one named for its length alone ("Weekly"), which the column says
function labelOf(w) {
  if (w.window && w.pool) return w.pool;
  return partsOf(w.name).filter((p) => spanOf(p) === Infinity).join(" · ");
}
// a length's head where no window of it is named for the length alone
const SPAN_HEAD = { 5: "5 hours", 24: "1 day", 168: "7 days", 720: "Month" };
const spanHead = (h) => SPAN_HEAD[h] || (h % 24 ? t("{n} h", { n: h }) : t("{n} days", { n: h / 24 }));
// columnOf: the column a window goes in, its key and its head: a length's
// head is a name of one of its windows that says the length alone
// ("Weekly"), else the length's own
function columnOf(w) {
  const h = hoursOf(w);
  if (h < Infinity) {
    const n = spanOf(w.name), own = n === h || (h === 720 && n >= 28 * 24 && n <= 31 * 24);
    return { key: "s:" + h, head: own ? w.name : spanHead(h), hours: h };
  }
  if (w.window && w.pool) { const f = poolWord(w.pool); return { key: "n:" + f, head: f }; }
  return { key: "n:" + w.name, head: w.name };
}

// a window's cell: its share and bar, and when it starts again; named
// (name) when its column doesn't say what it is
function windowCell(w, name) {
  const box = el("div", "qt-win");
  if (!w) return box;
  const top = el("div", "qt-fig");
  const text = quotaText(w);
  const bar = share(w) >= 0;
  top.append(el("span", "qt-n", bar ? quotaFill(w) + "%" : text));
  if (name) top.prepend(el("span", "qt-name", t(name)), " ");
  box.append(top);
  if (bar) {
    const track = el("div", "quota-track"), fill = el("i");
    fill.style.width = quotaFill(w) + "%";
    track.append(fill);
    box.append(track);
  }
  const at = w.resetsAt ? new Date(w.resetsAt) : null;
  if (at) box.append(el("div", "qt-reset", at > Date.now() ? resetClock(at) : t("Reset time passed")));
  box.title = tipLines(t(w.name) + " · " + text, at && resetText(at, at.toLocaleString(intlLang())), w.tiers && tiersText(w));
  return box;
}

function quotaTable(subs) {
  const rows = subs.map((sub, order) => ({ sub, order, ws: sub.error ? [] : familyWindows(sub.windows) || [] }));
  // the columns most rows have, by their span; each headed by the name
  // most of its windows have, a tie by the one saying its span in figures
  // ("7 days" over "Weekly")
  const count = new Map(), heads = new Map();
  for (const r of rows) {
    for (const k of new Set(r.ws.map((x) => columnOf(x).key))) count.set(k, (count.get(k) || 0) + 1);
    for (const x of r.ws) {
      const { key, head } = columnOf(x);
      const h = heads.get(key) || heads.set(key, new Map()).get(key);
      h.set(head, (h.get(head) || 0) + 1);
    }
  }
  const headOf = (k) => [...heads.get(k)].sort(([x, a], [y, b]) => b - a || /^\d/.test(y) - /^\d/.test(x))[0][0];
  // the lengths' columns first, then the most rows have; shown shortest first
  const hrs = (k) => (k.startsWith("s:") ? +k.slice(2) : Infinity);
  const names = [...count.keys()].map((n, i) => [n, i])
    .sort(([a, i], [b, j]) => (hrs(a) === Infinity) - (hrs(b) === Infinity) || count.get(b) - count.get(a) || i - j).slice(0, TABLE_WINDOWS)
    .sort(([a, i], [b, j]) => hrs(a) - hrs(b) || i - j).map(([n]) => n);
  const inCol = (ws, k) => ws.filter((w) => columnOf(w).key === k);
  const extra = (r) => r.ws.filter((w) => !names.includes(columnOf(w).key));
  const bare = (r) => r.sub.balance && !r.ws.length && !r.sub.error; // a balance and no window
  const more = rows.some((r) => extra(r).length || (r.sub.balance && r.ws.length)) || (!names.length && rows.some(bare));
  const value = (r, key) => key === "sub" ? quotaName(r.sub).toLowerCase()
    : key === "acct" ? (r.sub.alias || r.sub.user || "").toLowerCase() || null
    : Math.max(-1, ...inCol(r.ws, key).map(share));
  if (sorted && sorted.key !== "sub" && sorted.key !== "acct" && !names.includes(sorted.key)) sorted = null;
  const by = (val) => (a, b) => {
    const x = val(a), y = val(b);
    // a row with nothing here stays below the rest either way
    const none = (v) => v === null || v < 0;
    if (none(x) !== none(y)) return none(x) ? 1 : -1;
    return (x < y ? -1 : x > y ? 1 : 0) * (sorted.down ? -1 : 1) || a.order - b.order;
  };
  // a provider's accounts together, as its card has them
  const groups = [];
  for (const r of rows) {
    const g = r.sub.user && groups.find((x) => x.rows[0].sub.user && x.rows[0].sub.provider === r.sub.provider);
    if (g) g.rows.push(r); else groups.push({ order: r.order, rows: [r] });
  }
  // a column's share over a provider's accounts: their mean, -1 when none has one
  const mean = (g, key) => {
    const v = g.rows.map((r) => value(r, key)).filter((x) => x >= 0);
    return v.length ? Math.round(v.reduce((a, b) => a + b, 0) / v.length) : -1;
  };
  if (sorted) {
    for (const g of groups) g.rows.sort(by((r) => value(r, sorted.key)));
    groups.sort(by((g) => g.rows.length > 1 && sorted.key !== "sub" && sorted.key !== "acct" ? mean(g, sorted.key) : value(g.rows[0], sorted.key)));
  }
  // the vendor's switch on its first row drawn, as on the first card
  const switched = new Set();
  const checkin = (sub, alone) => {
    if (!sub?.checkins || typeof checkinRow !== "function") return null;
    const by = sub.checkinBy || "";
    const row = checkinRow(sub, !switched.has(by), subs);
    switched.add(by);
    if (alone) {
      row.querySelector(".ci-say")?.remove();
      if (!row.childElementCount) return null;
    }
    return row;
  };
  const whos = new Map(rows.map((r) => [r, quotaWho(r.sub)]));
  const acct = [...whos.values()].some((x) => x.length);

  const wrap = el("div", "qt-wrap");
  const table = el("table", groups.some((g) => g.rows.length > 1) ? "qt grouped" : "qt");
  const head = el("tr");
  const th = (key, label, cls = "") => {
    const c = el("th", cls);
    const on = sorted?.key === key;
    c.scope = "col";
    c.setAttribute("aria-sort", on ? (sorted.down ? "descending" : "ascending") : "none");
    const b = el("button", "qt-sort" + (on ? " on" : ""), label);
    b.type = "button";
    const arrow = el("span", "qt-arrow", on ? (sorted.down ? "↓" : "↑") : "");
    arrow.setAttribute("aria-hidden", "true");
    b.append(arrow);
    b.onclick = () => {
      // windows start at the most used, names at A
      const first = key !== "sub" && key !== "acct";
      sorted = !on ? { key, down: first } : sorted.down === first ? { key, down: !first } : null;
      renderQuotas();
    };
    c.append(b);
    return c;
  };
  head.append(th("sub", t("Subscription"), "qt-sub"));
  if (acct) head.append(th("acct", t("Account"), "qt-acct"));
  for (const n of names) head.append(th(n, t(headOf(n)), "qt-w"));
  if (more) head.append(el("th", "qt-w", t("Other")));
  table.append(el("thead", "", head));

  const body = el("tbody");
  const cols = names.length + (more ? 1 : 0);
  for (const g of groups) {
    const several = g.rows.length > 1;
    if (several) {
      const p = g.rows[0].sub.provider, open = opened.has(p);
      const tr = el("tr", "qt-group");
      tr.dataset.provider = p;
      const s = el("td", "qt-sub");
      const b = el("button", "qt-id qt-fold");
      b.type = "button";
      b.setAttribute("aria-expanded", String(open));
      b.append(el("span", "chev", svg(CHEV_R, 10, 1.6)), icon(g.rows[0].sub.icon), el("b", "", quotaName(g.rows[0].sub)));
      s.append(b);
      tr.append(s);
      const n = t("{n} accounts", { n: g.rows.length });
      const ci = !open && checkin(g.rows.find((r) => r.sub.checkins)?.sub, true);
      if (acct) tr.append(el("td", "qt-acct qt-count", n));
      else s.append(el("span", "plan qt-count", n));
      if (ci) s.append(ci);
      for (const k of names) {
        const c = el("td", "qt-w"), v = mean(g, k);
        if (v >= 0) {
          const box = el("div", "qt-win"), track = el("div", "quota-track"), fill = el("i");
          fill.style.width = v + "%";
          track.append(fill);
          box.append(el("div", "qt-fig", el("span", "qt-n", v + "%")), track);
          c.append(box);
        }
        tr.append(c);
      }
      if (more) tr.append(el("td", "qt-w qt-other"));
      tr.onclick = () => {
        const back = document.activeElement === b;
        if (open) opened.delete(p); else opened.add(p);
        keep("quotaTableOpen", [...opened]);
        renderQuotas();
        backToReader($("#view-usage"));
        if (back) $(`.qt-group[data-provider="${CSS.escape(p)}"] .qt-fold`)?.focus({ preventScroll: true });
      };
      body.append(tr);
      if (!open) continue;
    }
    for (const r of g.rows) {
      const { sub, ws } = r;
      const tr = el("tr", several ? "qt-kid" : "");
      tr.dataset.card = trayCardID(sub);
      tr.dataset.provider = sub.provider;
      const s = el("td", "qt-sub");
      if (!several) {
        const who = el("div", "qt-id");
        who.append(icon(sub.icon), el("b", "", quotaName(sub)));
        s.append(who);
      }
      if (sub.plan || sub.until) s.append(planSpan(sub));
      const ci = checkin(sub);
      if (ci) s.append(ci);
      tr.append(s);
      if (acct) {
        const a = el("td", "qt-acct");
        const name = el("div", "subscription-account");
        name.append(...whos.get(r));
        a.append(name);
        tr.append(a);
      }
      if (sub.error) {
        const e = el("td", "qt-msg", quotaError(sub.error, sub));
        e.colSpan = cols;
        e.title = sub.error;
        tr.append(e);
      } else {
        if (bare({ sub, ws })) {
          const b = el("td", "qt-msg", t("{n} left", { n: sub.balance }));
          b.colSpan = cols;
          tr.append(b);
        } else for (const n of names) {
          const here = inCol(ws, n);
          const c = el("td", "qt-w");
          const label = (w) => (n.startsWith("s:") ? labelOf(w) || (here.length > 1 ? w.name : "")
            : here.length > 1 || w.name !== headOf(n) ? w.name : "");
          for (const w of here) c.append(windowCell(w, label(w)));
          tr.append(c);
        }
        if (more && !bare({ sub, ws })) {
          const o = el("td", "qt-w qt-other");
          for (const w of extra({ ws })) o.append(windowCell(w, w.name));
          if (sub.balance) o.append(el("div", "qt-fig", t("{n} left", { n: sub.balance })));
          tr.append(o);
        }
      }
      body.append(tr);
    }
  }
  table.append(body);
  wrap.append(table);
  return wrap;
}
window.quotaTable = quotaTable;
