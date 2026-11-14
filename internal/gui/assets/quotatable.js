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
// A column is what windows are of, not their names (normalcoder_ on X: an
// Antigravity account's "Gemini · 5 hours" and "Gemini · 7 days" went in
// Other beside a Gemini column): a pool's windows go in its family's
// column, a family's in its own, and a window named for its span in the
// span's, however the vendor words it ("Weekly", "7 days"). A cell holding
// more than one, or one named otherwise than its column, names each.
const TABLE_WINDOWS = 4;
let sorted = null; // { key, down }

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
// columnOf: the column a window goes in, its key and its head
function columnOf(w) {
  if (w.window && w.pool) { const f = poolWord(w.pool); return { key: "n:" + f, head: f }; }
  const h = spanOf(w.name);
  return h < Infinity ? { key: "s:" + h, head: w.name } : { key: "n:" + w.name, head: w.name };
}

// a window's cell: its share and bar, and when it starts again
function windowCell(w, long) {
  const box = el("div", "qt-win");
  if (!w) return box;
  const top = el("div", "qt-fig");
  const text = quotaText(w);
  const bar = share(w) >= 0;
  top.append(el("span", "qt-n", bar ? quotaFill(w) + "%" : text));
  if (long) top.prepend(el("span", "qt-name", t(w.name)), " ");
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
  const names = [...count.keys()].map((n, i) => [n, i]).sort(([a, i], [b, j]) => count.get(b) - count.get(a) || i - j).slice(0, TABLE_WINDOWS)
    .sort(([a, i], [b, j]) => spanOf(headOf(a)) - spanOf(headOf(b)) || i - j).map(([n]) => n);
  const inCol = (ws, k) => ws.filter((w) => columnOf(w).key === k);
  const extra = (r) => r.ws.filter((w) => !names.includes(columnOf(w).key));
  const bare = (r) => r.sub.balance && !r.ws.length && !r.sub.error; // a balance and no window
  const more = rows.some((r) => extra(r).length || (r.sub.balance && r.ws.length)) || (!names.length && rows.some(bare));
  const value = (r, key) => key === "sub" ? quotaName(r.sub).toLowerCase()
    : key === "acct" ? (r.sub.alias || r.sub.user || "").toLowerCase() || null
    : Math.max(-1, ...inCol(r.ws, key).map(share));
  if (sorted && sorted.key !== "sub" && sorted.key !== "acct" && !names.includes(sorted.key)) sorted = null;
  const list = sorted ? [...rows].sort((a, b) => {
    const x = value(a, sorted.key), y = value(b, sorted.key);
    // a row with nothing here stays below the rest either way
    const none = (v) => v === null || v < 0;
    if (none(x) !== none(y)) return none(x) ? 1 : -1;
    return (x < y ? -1 : x > y ? 1 : 0) * (sorted.down ? -1 : 1) || a.order - b.order;
  }) : rows;

  const wrap = el("div", "qt-wrap");
  const table = el("table", "qt");
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
  head.append(th("sub", t("Subscription"), "qt-sub"), th("acct", t("Account"), "qt-acct"));
  for (const n of names) head.append(th(n, t(headOf(n)), "qt-w"));
  if (more) head.append(el("th", "qt-w", t("Other")));
  table.append(el("thead", "", head));

  const body = el("tbody");
  for (const { sub, ws } of list) {
    const tr = el("tr");
    tr.dataset.card = trayCardID(sub);
    tr.dataset.provider = sub.provider;
    const s = el("td", "qt-sub");
    const who = el("div", "qt-id");
    who.append(icon(sub.icon), el("b", "", quotaName(sub)));
    s.append(who);
    if (sub.plan || sub.until) s.append(planSpan(sub));
    const a = el("td", "qt-acct");
    const name = el("div", "subscription-account");
    name.append(...quotaWho(sub));
    a.append(name);
    tr.append(s, a);
    const cols = names.length + (more ? 1 : 0);
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
        for (const w of here) c.append(windowCell(w, here.length > 1 || w.name !== headOf(n)));
        tr.append(c);
      }
      if (more && !bare({ sub, ws })) {
        const o = el("td", "qt-w qt-other");
        for (const w of extra({ ws })) o.append(windowCell(w, true));
        if (sub.balance) o.append(el("div", "qt-fig", t("{n} left", { n: sub.balance })));
        tr.append(o);
      }
    }
    body.append(tr);
  }
  table.append(body);
  wrap.append(table);
  return wrap;
}
window.quotaTable = quotaTable;
