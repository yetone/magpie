// A key's balance typed by hand (tenacious_eagle on Discord): for a key
// whose vendor tells no balance magpie can read, the user types what the
// vendor's page says is left, and magpie counts down from it by what its
// own usage prices the calls it sends on the key (provider/
// key_balance_own.go). Typing it again sets it anew. It may be typed in
// yuan ("¥100", or a bare number while Settings shows costs in CNY), and
// may refill each month or week. keyspend.js imports this when the
// provider editor draws a key's row, so it weighs nothing on the core.
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
const weekdayName = (d) => new Date(2026, 10, 1 + d).toLocaleDateString(intlLang(), { weekday: "long" }); // 2026-11-02 is a Monday
function keyBalancePill(p, k) {
  const b = k.balance, name = k.name || k.masked, cny = b?.currency === "cny";
  const pill = el("button", "abal" + (b ? " set" : ""));
  pill.type = "button";
  pill.textContent = b ? t("≈{usd} left", { usd: b.left == null ? (cny ? "¥" : "$") + "…" : moneyText(b.left, cny) }) : t("Set balance");
  pill.title = !b ? t("Type what the vendor's page says this key has left, and magpie counts down from it as it sends requests")
    : [t("An estimate: {set} from {when}, less {spent} magpie counted spent on this key since, at the prices the Usage page shows. Click to change", {
        set: moneyText(b.set, cny), when: new Date(b.from || b.at).toLocaleString(intlLang()),
        spent: moneyText(b.spent || 0, false) + (cny && b.rate ? " (" + moneyText((b.spent || 0) * b.rate, true) + ")" : ""),
      }),
      b.left == null ? t("The dollar's rate in yuan isn't known yet, so what is left can't be counted") : "",
      b.refill && b.next ? t("Refills to {set} on {when}", { set: moneyText(b.set, cny), when: new Date(b.next).toLocaleString(intlLang(), { dateStyle: "medium" }) }) : "",
    ].filter(Boolean).join("\n");
  const set = (v) => accountAction("provider/keyownbalance", { id: p.id, account: k.id, balance: v ? v.n : null, ...v?.cny && { currency: "cny" } },
    !v ? t("{who} has no balance typed", { who: name }) : t("{who} has {usd} left", { who: name, usd: moneyText(v.n, v.cny) }));
  const refill = (r) => accountAction("provider/keybalancerefill", { id: p.id, account: k.id, refill: r },
    !r ? t("{who} no longer refills", { who: name })
      : r.every === "week" ? t("{who} refills each {weekday}", { who: name, weekday: weekdayName(r.day) })
      : t("{who} refills on day {day} of each month", { who: name, day: r.day }));
  // a bare number is in the balance's own currency, else in Settings' one
  const unit = b ? (cny ? "cny" : "usd") : currency === "cny" ? "cny" : "usd";
  const type = () => pillNumber(pill, b ? (cny ? "¥" : "") + b.set : "", unit === "cny" ? "CNY" : "USD", "abal-in",
    t(unit === "cny" ? "Balance, in CNY" : "Balance, in USD"), (s) => balanceAmount(s, unit),
    t("A balance is an amount from 0 to 1,000,000, in USD or in yuan (¥)"), null, set);
  const day = (s) => { const n = Number(s.trim()); return Number.isInteger(n) && n >= 1 && n <= 31 ? n : null; };
  const monthly = () => pillNumber(pill, String(b.refill?.every === "month" ? b.refill.day : 1), "1–31", "abal-in", t("Refill day of the month"), day,
    t("A day of the month is a whole number from 1 to 31"), b.refill?.every === "month" ? b.refill.day : null, (d) => refill({ every: "month", day: d }));
  const weekly = () => openProtoMenu(pill, [1, 2, 3, 4, 5, 6, 7].map((d) => ({ v: d, name: weekdayName(d), literalName: true, note: "" })),
    b.refill?.every === "week" ? b.refill.day : 0, (d) => refill({ every: "week", day: d }), "Refills on", "sess-menu");
  pill.onclick = (e) => {
    e.stopPropagation();
    if (!b) return type();
    if (pill.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(pill, [
      { v: "set", name: "Enter balance…", note: "Counts down from now" },
      { v: "month", name: "Refills each month…", note: "" },
      { v: "week", name: "Refills each week…", note: "" },
      { v: "never", name: "Never refills", note: "" },
      { v: "off", name: "No balance", note: "" },
    ], b.refill?.every || "never", (v) => v === "off" ? set(null) : v === "month" ? monthly() : v === "week" ? weekly()
      : v === "never" ? b.refill && refill(null) : type(), "Balance", "sess-menu");
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
