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
const TABLE_WINDOWS = 4;
let sorted = null; // { key, down }

// the figure a window sorts and bars by: the share as the page shows it
// (used, or left), -1 when it has none
const share = (w) => (!w || w.unlimited || quotaNoShare(w) ? -1 : quotaFill(w));
const spanOf = (name) => {
  const m = /^(\d+)\s*(hour|day|week)s?$/i.exec(name || "");
  return m ? m[1] * { hour: 1, day: 24, week: 168 }[m[2].toLowerCase()] : Infinity;
};

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
  // the windows most rows have, by their span
  const count = new Map();
  for (const r of rows) for (const w of new Set(r.ws.filter((x) => !x.members).map((x) => x.name))) count.set(w, (count.get(w) || 0) + 1);
  const names = [...count.keys()].map((n, i) => [n, i]).sort(([a, i], [b, j]) => count.get(b) - count.get(a) || i - j).slice(0, TABLE_WINDOWS)
    .sort(([a, i], [b, j]) => spanOf(a) - spanOf(b) || i - j).map(([n]) => n);
  const extra = (r) => r.ws.filter((w) => !names.includes(w.name));
  const bare = (r) => r.sub.balance && !r.ws.length && !r.sub.error; // a balance and no window
  const more = rows.some((r) => extra(r).length || (r.sub.balance && r.ws.length)) || (!names.length && rows.some(bare));
  const value = (r, key) => key === "sub" ? quotaName(r.sub).toLowerCase()
    : key === "acct" ? (r.sub.alias || r.sub.user || "").toLowerCase() || null
    : Math.max(-1, ...r.ws.filter((w) => w.name === key).map(share));
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
  for (const n of names) head.append(th(n, t(n), "qt-w"));
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
        const w = ws.find((x) => x.name === n);
        const c = el("td", "qt-w");
        if (w) c.append(windowCell(w));
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
