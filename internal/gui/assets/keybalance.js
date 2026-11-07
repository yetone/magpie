// A key's balance typed by hand (tenacious_eagle on Discord): for a key
// whose vendor tells no balance magpie can read, the user types what the
// vendor's page says is left, and magpie counts down from it by what its
// own usage prices the calls it sends on the key (provider/
// key_balance_own.go). Typing it again sets it anew. keyspend.js imports this
// when the provider editor draws a key's row, so it weighs nothing on the
// core.
const usdText = (v) => (v < 0 ? "-$" : "$") + Math.abs(v).toFixed(2);
function keyBalancePill(p, k) {
  const b = k.balance, name = k.name || k.masked;
  const pill = el("button", "abal" + (b ? " set" : ""));
  pill.type = "button";
  const left = b ? b.set - (b.spent || 0) : 0;
  pill.textContent = b ? t("≈{usd} left", { usd: usdText(left) }) : t("Set balance");
  pill.title = b ? t("An estimate: {set} typed {when}, less {spent} magpie counted spent on this key since, at the prices the Usage page shows. Click to type it again", { set: usdText(b.set), when: new Date(b.at).toLocaleString(intlLang()), spent: usdText(b.spent || 0) })
    : t("Type what the vendor's page says this key has left, and magpie counts down from it as it sends requests");
  const set = (usd) => accountAction("provider/keyownbalance", { id: p.id, account: k.id, balance: usd },
    usd == null ? t("{who} has no balance typed", { who: name }) : t("{who} has {usd} left", { who: name, usd: usdText(usd) }));
  const amount = (s) => { const n = Number(s.replace(/^\$/, "").replace(/,/g, "").trim()); return s && n >= 0 && n <= 1e6 ? n : null; };
  const type = () => pillNumber(pill, b ? String(b.set) : "", "USD", "abal-in", t("Balance, in USD"), amount, t("A balance is an amount in USD from 0 to 1,000,000"), null, set);
  pill.onclick = (e) => {
    e.stopPropagation();
    if (!b) return type();
    if (pill.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(pill, [
      { v: "set", name: "Enter balance…", note: "Counts down from now" },
      { v: "off", name: "No balance", note: "" },
    ], "", (v) => v === "off" ? set(null) : type(), "Balance", "sess-menu");
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
