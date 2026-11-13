// A key's own price rate (@tnt12231 on X: a relay bills its key groups
// at their own rates against the official price). The provider editor's
// Advanced draws a line for each key, folded as keyProxyPicker's lines
// (proxyLines); a key left empty bills at the provider's Price rate.
// app.js imports this when a provider with two or more keys opens
// (keyRateLines), so it weighs nothing on the core.
function keyPriceRateLine(it) {
  const line = el("div", "key-rate");
  line.dataset.key = it.key;
  const who = el("span", "who", it.name);
  who.title = it.name;
  const box = input(draft.keyPriceRates[it.k] ?? "", String(draft.priceRate ?? "").trim() || "1");
  box.classList.add("key-price-rate");
  box.inputMode = "decimal";
  box.setAttribute("aria-label", t("Price rate of {user}", { user: it.name }));
  box.oninput = () => { draft.keyPriceRates[it.k] = box.value; };
  line.append(who, box);
  return line;
}
window.keyPriceRateField = function (p) {
  draft.keyPriceRates = draft.keyPriceRates || {};
  const items = (p?.keyList || []).map((k) => ({ k: k.id, name: k.name || k.masked, key: k.id, info: k }));
  return field(t("Price rate per key"), proxyLines(items, "A key left empty bills at the provider's price rate", true,
    { lineOf: keyPriceRateLine, own: (it) => !!String(draft.keyPriceRates[it.k] ?? "").trim(), cls: "key-rates" }));
};
// a key left empty bills at the provider's Price rate: its placeholder
// follows that box as it is typed
document.addEventListener("input", (e) => {
  const box = e.target;
  if (!box.matches?.("input.price-rate")) return;
  for (const k of box.closest(".inner")?.querySelectorAll("input.key-price-rate") || []) k.placeholder = box.value.trim() || "1";
});
// keyPriceRates is each key's own price rate as saved, the keys left
// empty out; a key typed wrong is focused and named, and gives null. An
// editor that drew no lines sends them as they were.
window.keyPriceRates = function (ed) {
  const map = {};
  for (const [k, v] of Object.entries(draft.keyPriceRates || {})) {
    const s = String(v ?? "").trim();
    if (!s) continue;
    const n = decimalOf(s);
    if (!(n > 0 && n <= 1000) || Math.abs(n * 1000 - Math.round(n * 1000)) > 1e-6) {
      const line = ed.querySelector(`.key-rate[data-key="${k}"]`);
      shown(line?.querySelector("input.key-price-rate"))?.focus(NO_SCROLL);
      editorError(t("Price rate of {user}: a number above 0 up to 1000, at most three decimals", { user: line?.querySelector(".who")?.textContent || k }), "warn");
      return null;
    }
    map[k] = n;
  }
  return map;
};
