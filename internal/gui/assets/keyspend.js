// A key's spend cap (sparkling_lemur_62868 on Discord: a team's OpenAI
// key, with credits other services spend from too): how much magpie may
// spend on it, in USD at the Usage page's prices, a day, a month or in
// all. Past it the gateway passes the key over, as a usage cap holds an
// account (provider/key_spend.go). app.js imports this when the provider
// editor draws a key's row (keySpend), so it weighs nothing on the core.
const usdText = (v) => "$" + (v >= 100 ? v.toFixed(0) : v.toFixed(2));
const SPEND_PERIODS = { day: "a day", month: "a month", total: "in all" };
function keySpendPill(p, k) {
  const sc = k.spend, name = k.name || k.masked;
  const pill = el("button", "aspend" + (sc ? " set" : "") + (sc?.held ? " at-cap" : ""));
  pill.type = "button";
  const v = sc && { spent: usdText(sc.spent || 0), cap: usdText(sc.usd) };
  pill.textContent = !sc ? t("No spend cap") : t(sc.period === "month" ? "{spent} of {cap} this month" : sc.period === "total" ? "{spent} of {cap} in all" : "{spent} of {cap} today", v);
  pill.title = sc ? t("Stops at {cap} {period}, counted at the prices the Usage page shows for the calls magpie sends on this key. Past it, magpie goes on to the provider's other keys and its fallbacks. Requests under way still finish, so it can go a little past the cap, and a model with no known price counts nothing. Click to change", { cap: v.cap, period: t(SPEND_PERIODS[sc.period] || "a day") })
    : t("No spend cap: magpie sends this key requests whatever they cost. Click to set how much it may spend, a day, a month or in all; past it, magpie goes on to the provider's other keys and its fallbacks");
  pill.setAttribute("aria-haspopup", "menu");
  pill.setAttribute("aria-expanded", "false");
  const set = (usd, period) => accountAction("provider/keyspendcap", { id: p.id, account: k.id, spend: usd ? { usd, period } : null },
    !usd ? t("{who} has no spend cap", { who: name })
      : t(period === "month" ? "{who} stops at {usd} a month" : period === "total" ? "{who} stops at {usd} in all" : "{who} stops at {usd} a day", { who: name, usd: usdText(usd) }));
  const amount = (s) => { const n = Number(s.replace(/^\$/, "").trim()); return n > 0 && n <= 1e6 ? n : null; };
  pill.onclick = (e) => {
    e.stopPropagation();
    if (pill.classList.contains("open")) return closeProtoMenu();
    const opts = [
      { v: "off", name: "No spend cap", note: "Spend as much as the key has" },
      { v: "day", name: "Per day…", note: "Counts again each day" },
      { v: "month", name: "Per month…", note: "Counts again each month" },
      { v: "total", name: "In all…", note: "Counted from when it is set" },
    ];
    openProtoMenu(pill, opts, sc?.period || "off", (per) => {
      if (per === "off") return sc && set(0);
      pillNumber(pill, sc ? String(sc.usd) : "", "USD", "aspend-in", t("Spend cap, in USD"), amount, t("A spend cap is an amount in USD above 0, up to 1,000,000"), sc?.period === per ? sc.usd : null, (usd) => set(usd, per));
    }, "Spend cap", "sess-menu");
  };
  return pill;
}
// keySpendLine: a key held at its spend cap, on a line under its row
function keySpendLine(k) {
  const sc = k.spend;
  if (!sc?.held) return null;
  const s = el("span", "key-rest", sc.reset ? t("At its spend cap · back {in}", { in: untilText(new Date(sc.reset)) }) : t("At its spend cap · until it is raised"));
  s.title = t("This key has spent its cap, so the gateway passes it over and tries the next key or a fallback. With none, requests are refused with a spend-cap error")
    + (sc.reset ? "\n" + resetText(new Date(sc.reset), new Date(sc.reset).toLocaleString(intlLang())) : "");
  const line = el("div", "key-rest-line key-spend-line");
  line.append(s);
  return line;
}

// keySpendFill puts key k's spend cap pill in its row, before the gap, and
// its held line under the row's other lines
window.keySpendFill = function (row, p, k) {
  // and its typed balance, in keybalance.js, for a provider that takes one
  if (window.keyBalanceFill) keyBalanceFill(row, p, k);
  else if (p.takesOwnBalance) import("./keybalance.js").then(() => keyBalanceFill(row, p, k), () => {});
  row.querySelector(":scope > .aspend")?.remove();
  row.querySelector(":scope > .key-spend-line")?.remove();
  const pill = keySpendPill(p, k), gap = row.querySelector(":scope > .grow");
  gap ? gap.before(pill) : row.append(pill);
  const line = keySpendLine(k);
  if (!line) return;
  const at = row.querySelector(":scope > .key-rest-line") || row.querySelector(":scope > .key-acts");
  at ? at.after(line) : row.append(line);
  row.classList.add("resting");
};
