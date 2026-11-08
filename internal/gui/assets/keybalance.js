// A key's balance typed by hand (tenacious_eagle on Discord): for a key
// whose vendor tells no balance magpie can read, the user types what the
// vendor's page says is left, and magpie counts down from it by what its
// own usage prices the calls it sends on the key (provider/
// key_balance_own.go). Typing it again sets it anew. It may be typed in
// yuan ("¥100", or picked beside the field, which starts at the balance's
// own currency, else Settings' one), and may refill each month or week.
// Credit may come on top that expires on a day, in grants (promotional
// credit twice, gone on two days: tenacious_eagle_96548), the one that
// goes soonest spent first: the pill says how much of each is left and in
// how many days, amber from a week; each is added in one small form,
// amount and last day together, and taken off from the menu. keyspend.js
// imports this when the provider editor draws a key's row, so it weighs
// nothing on the core.
const moneyText = (v, cny) => (v < 0 ? "-" : "") + (cny ? "¥" : "$") + Math.abs(v).toFixed(2);
// balanceAmount reads what was typed: an amount and whether it is in
// yuan, a sign or a word saying which, else unit; null for none
function balanceAmount(s, unit) {
  let a = s.trim().toLowerCase(), cny = unit === "cny";
  const yuan = /^(¥|￥|cny|rmb)\s*|\s*(元|cny|rmb|yuan)$/g, usd = /^\$\s*|\s*usd$/g;
  if (yuan.test(a)) { a = a.replace(yuan, ""); cny = true; }
  else if (usd.test(a)) { a = a.replace(usd, ""); cny = false; }
  a = a.replace(/,/g, "").trim();
  const n = Number(a);
  return a && n >= 0 && n <= 1e6 ? { n, cny } : null;
}
// dayOf: a day as the date it is here, 2026-12-31; dateText: one shown
const dayOf = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const dateText = (s) => new Date(s + "T00:00").toLocaleDateString(intlLang(), { dateStyle: "medium" });
// lastDay reads the last day credit counts: 2026-12-31 (or with / or .),
// from today on; null else
function lastDay(s) {
  const m = s.trim().match(/^(\d{4})[-/.](\d{1,2})[-/.](\d{1,2})$/);
  if (!m) return null;
  const d = new Date(+m[1], m[2] - 1, +m[3]);
  return d.getMonth() === m[2] - 1 && d.getDate() === +m[3] && dayOf(d) >= dayOf(new Date()) ? dayOf(d) : null;
}
const weekdayName = (d) => new Date(2026, 10, 1 + d).toLocaleDateString(intlLang(), { weekday: "long" }); // 2026-11-02 is a Monday
function keyBalancePill(p, k) {
  const b = k.balance, name = k.name || k.masked, cny = b?.currency === "cny";
  const pill = el("button", "abal" + (b ? " set" : ""));
  pill.type = "button";
  pill.textContent = b ? t("≈{usd} left", { usd: b.left == null ? (cny ? "¥" : "$") + "…" : moneyText(b.left, cny) }) : t("Set balance");
  // each grant still counting, soonest first as the server lists them
  const xs = (b?.expiring || []).map((x) => ({ ...x, in: expiresIn(x.ends), last: dayOf(new Date(new Date(x.ends) - 1)) }));
  for (const x of xs) {
    if (!(x.left > 0)) continue;
    const amount = moneyText(x.left, cny);
    pill.append(" · ", el("span", "abal-exp" + (x.in.soon ? " soon" : ""), x.in.days <= 0 ? t("{amount} expires today", { amount })
      : x.in.days === 1 ? t("{amount} expires tomorrow", { amount }) : t("{amount} expires in {n} days", { amount, n: x.in.days })));
  }
  // two or more wrap at a grant on a narrow row (a class, not :has(),
  // which Safari before 15.4 drops the rule for)
  pill.classList.toggle("abal-many", pill.querySelectorAll(".abal-exp").length > 1);
  pill.title = !b ? t("Type what the vendor's page says this key has left, and magpie counts down from it as it sends requests")
    : [t("An estimate: {set} from {when}, less {spent} magpie counted spent on this key since, at the prices the Usage page shows. Click to change", {
        set: moneyText(b.set, cny), when: new Date(b.from || b.at).toLocaleString(intlLang()),
        spent: moneyText(b.spent || 0, false) + (cny && b.rate ? " (" + moneyText((b.spent || 0) * b.rate, true) + ")" : ""),
      }),
      b.left == null ? t("The dollar's rate in yuan isn't known yet, so what is left can't be counted") : "",
      b.refill && b.next ? t("Refills to {set} on {when}", { set: moneyText(b.set, cny), when: new Date(b.next).toLocaleString(intlLang(), { dateStyle: "medium" }) }) : "",
      ...xs.map((x) => t("Of it, {left} of the {set} typed {when} is spent first and counts through {last}", { left: x.left == null ? "…" : moneyText(x.left, cny), set: moneyText(x.set, cny),
        when: new Date(x.at).toLocaleString(intlLang()), last: dateText(x.last) })),
      b.expired ? t("The credit that expired on {last} no longer counts", { last: dateText(dayOf(new Date(new Date(b.expired) - 1))) }) : "",
    ].filter(Boolean).join("\n");
  const set = (v) => accountAction("provider/keyownbalance", { id: p.id, account: k.id, balance: v ? v.n : null, ...v?.cny && { currency: "cny" } },
    !v ? t("{who} has no balance typed", { who: name }) : t("{who} has {usd} left", { who: name, usd: moneyText(v.n, v.cny) }));
  const refill = (r) => accountAction("provider/keybalancerefill", { id: p.id, account: k.id, refill: r },
    !r ? t("{who} no longer refills", { who: name })
      : r.every === "week" ? t("{who} refills each {weekday}", { who: name, weekday: weekdayName(r.day) })
      : t("{who} refills on day {day} of each month", { who: name, day: r.day }));
  const expiring = (v) => accountAction("provider/keyexpiring", { id: p.id, account: k.id, expiring: v },
    !v ? t("{who} has no credit that expires", { who: name }) : t("{who} has {amount} that counts through {last}", { who: name, amount: moneyText(v.amount, cny), last: dateText(v.until) }));
  const unexpiring = (x) => accountAction("provider/keyexpiring", { id: p.id, account: k.id, expiring: { at: x.at, until: x.until }, remove: true },
    t("{who} no longer has {amount} that counts through {last}", { who: name, amount: moneyText(x.set, cny), last: dateText(x.last) }));
  // a grant of credit that expires: its amount, in the balance's
  // currency, and its last day, in one small form under the pill; Add or
  // Enter saves, Escape or a click outside leaves it
  const credit = () => {
    const own = cny ? "cny" : "usd", head = t("Add credit that expires");
    const box = el("div", "pop abal-form");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-label", head);
    const amount = el("input", "abal-f-n"), day = el("input", "abal-f-d"), add = el("button", "text primary", t("Add"));
    amount.type = "text";
    amount.inputMode = "decimal";
    amount.autocomplete = "off";
    amount.placeholder = "0.00";
    amount.setAttribute("aria-label", t(cny ? "Credit that expires, in CNY" : "Credit that expires, in USD"));
    day.type = "date";
    day.min = dayOf(new Date());
    // a month from today: a promotion's usual run, and the picker opens there
    const soon = new Date();
    soon.setDate(soon.getDate() + 30);
    day.value = dayOf(soon);
    day.setAttribute("aria-label", t("Last day it counts"));
    add.type = "button";
    const field = (label, ...kids) => { const f = el("label", "abal-f"); f.append(el("span", "abal-f-l", label), ...kids); return f; };
    const money = el("span", "abal-f-m");
    money.append(el("span", "abal-f-c", cny ? "¥" : "$"), amount);
    const bar = el("div", "abal-f-bar");
    bar.append(add);
    box.append(el("div", "abal-f-h", head), field(t("Amount"), money), field(t("Last day it counts"), day), bar);
    const save = () => {
      const v = balanceAmount(amount.value, own), n = v && v.n > 0 && v.cny === cny ? v.n : null, until = lastDay(day.value);
      amount.toggleAttribute("aria-invalid", n == null);
      money.classList.toggle("bad", n == null);
      day.toggleAttribute("aria-invalid", n != null && !until);
      if (n == null) { status(t("Credit that expires is an amount above 0, in the balance's currency"), "err"); return amount.focus(NO_SCROLL); }
      if (!until) { status(t("The last day is a date from today on, as 2026-12-31"), "err"); return day.focus(NO_SCROLL); }
      closeProtoMenu();
      expiring({ amount: n, until });
    };
    add.onclick = (e) => { e.stopPropagation(); save(); };
    box.onclick = (e) => e.stopPropagation();
    document.body.append(box);
    placeMenu(box, pill);
    pill.classList.add("open");
    holdMenu(box, pill, (e) => {
      if (e.key === "Enter" && box.contains(e.target) && e.target !== add) { e.preventDefault(); e.stopPropagation(); save(); }
    }, (e) => { if (scrollCloses(box, e)) closeProtoMenu(); });
    amount.focus(NO_SCROLL);
  };
  // a bare number is in the currency the button beside the field shows:
  // the balance's own, else Settings' one, until the user picks the other
  // there (tenacious_eagle: no clearing it and going to Settings). Picking
  // keeps the number typed, so 100 becomes ¥100 only by that pick.
  let unit = b ? (cny ? "cny" : "usd") : currency === "cny" ? "cny" : "usd";
  const type = () => {
    const at = pill.parentNode;
    pillNumber(pill, b ? String(b.set) : "", "", "abal-in", "", (s) => balanceAmount(s, unit),
      t("A balance is an amount from 0 to 1,000,000, in USD or in yuan (¥)"), null, set);
    const i = at?.querySelector(":scope > .abal-in");
    if (!i) return;
    const cur = el("button", "abal-cur"), save = i.onblur;
    cur.type = "button";
    cur.setAttribute("aria-haspopup", "menu");
    cur.setAttribute("aria-expanded", "false");
    let picking = false;
    // a sign typed in the field says its currency, and the button follows
    const now = () => (balanceAmount(i.value, unit)?.cny ?? unit === "cny") ? "cny" : "usd";
    const show = () => {
      const c = now();
      cur.replaceChildren(c.toUpperCase(), svg(CHEV, 12, 1.6));
      cur.setAttribute("aria-label", t("Currency"));
      i.setAttribute("aria-label", t(c === "cny" ? "Balance, in CNY" : "Balance, in USD"));
    };
    i.oninput = show;
    // the field saves as it is left, but not for its currency's menu
    i.onblur = (e) => { if (!picking && e.relatedTarget !== cur) save(); };
    cur.onblur = (e) => { if (!picking && e.relatedTarget !== i) save(); };
    cur.onmousedown = (e) => e.preventDefault();
    cur.onkeydown = (e) => e.stopPropagation();
    cur.onclick = (e) => {
      e.stopPropagation();
      if (cur.classList.contains("open")) return closeProtoMenu();
      picking = true;
      openProtoMenu(cur, CURRENCIES.map(([v, name]) => ({ v, name, literalName: true, note: "" })), now(), (v) => {
        unit = v;
        i.value = i.value.trim().replace(/^(\$|¥|￥|cny|rmb|usd)\s*|\s*(元|cny|rmb|yuan|usd)$/gi, "");
        show();
      }, "Currency", "sess-menu");
      protoMenu.done = () => { picking = false; i.focus(NO_SCROLL); };
    };
    show();
    i.after(cur);
  };
  const day = (s) => { const n = Number(s.trim()); return Number.isInteger(n) && n >= 1 && n <= 31 ? n : null; };
  const monthly = () => pillNumber(pill, String(b.refill?.every === "month" ? b.refill.day : 1), "1–31", "abal-in", t("Refill day of the month"), day,
    t("A day of the month is a whole number from 1 to 31"), b.refill?.every === "month" ? b.refill.day : null, (d) => refill({ every: "month", day: d }));
  const weekly = () => openProtoMenu(pill, [1, 2, 3, 4, 5, 6, 7].map((d) => ({ v: d, name: weekdayName(d), literalName: true, note: "" })),
    b.refill?.every === "week" ? b.refill.day : 0, (d) => refill({ every: "week", day: d }), "Refills on", "sess-menu");
  pill.onclick = (e) => {
    e.stopPropagation();
    if (!b) return type();
    if (pill.classList.contains("open")) return closeProtoMenu();
    const grants = xs.map((x, i) => ({ v: "x" + i, name: t("Remove {amount}", { amount: moneyText(x.set, cny) }), literalName: true, note: x.in.label,
      title: t("Of it, {left} of the {set} typed {when} is spent first and counts through {last}", { left: x.left == null ? "…" : moneyText(x.left, cny),
        set: moneyText(x.set, cny), when: new Date(x.at).toLocaleString(intlLang()), last: dateText(x.last) }) }));
    openProtoMenu(pill, [
      { v: "set", name: "Enter balance…", note: "Counts down from now" },
      { v: "exp", name: "Add credit that expires…", note: "" },
      ...grants,
      ...xs.length > 1 || b.expired ? [{ v: "noexp", name: "No credit that expires", note: "" }] : [],
      { v: "month", name: "Refills each month…", note: "" },
      { v: "week", name: "Refills each week…", note: "" },
      { v: "never", name: "Never refills", note: "" },
      { v: "off", name: "No balance", note: "" },
    ], b.refill?.every || "never", (v) => v === "off" ? set(null) : v === "exp" ? credit() : v === "noexp" ? expiring(null) : v === "month" ? monthly() : v === "week" ? weekly()
      : v === "never" ? b.refill && refill(null) : v[0] === "x" ? unexpiring(xs[+v.slice(1)]) : type(), "Balance", "sess-menu");
    // a grant that goes within a week says so in amber, as on the pill
    const items = protoMenu?.box.querySelectorAll(".pm-item");
    xs.forEach((x, i) => x.in.soon && items?.[2 + i]?.querySelector(".pm-note").classList.add("soon"));
  };
  return pill;
}

// keyBalanceFill puts key k's typed balance pill in its row, before the
// gap, when p's vendor tells no balance of its own
window.keyBalanceFill = function (row, p, k) {
  row.querySelector(":scope > .abal")?.remove();
  if (!p.takesOwnBalance || p.balanceFound) return;
  const pill = keyBalancePill(p, k), gap = row.querySelector(":scope > .grow");
  gap ? gap.before(pill) : row.append(pill);
};
