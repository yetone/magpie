// A key's own price rate (@tnt12231 on X: a relay bills its key groups
// at their own rates against the official price). The provider editor's
// Advanced draws a line for each key, folded as keyProxyPicker's lines
// (proxyLines); a key left empty bills at the provider's Price rate.
// app.js imports this when a provider with two or more keys opens, or a
// provider of keys of its own (keyRateField), so it weighs nothing on the
// core.
//
// A relay's top-up rate (@YIXIE233 on X): a relay built on sub2api tells
// each key its group's rate, and relays sell their dollars at their own
// prices (¥1 for $1 at one, ¥7 for $100 at another). The user types once
// what a dollar of the relay cost them; magpie reads each key's group
// rate from the relay and counts its calls at the two multiplied
// (provider/key_rate_read.go).
window.topUpRateField = function (p) {
  const box = input(draft.topUpRate ?? (p?.topUpRate ? String(p.topUpRate) : ""), "0.07");
  box.classList.add("top-up-rate");
  box.inputMode = "decimal";
  box.oninput = () => { draft.topUpRate = box.value; };
  const rates = Object.entries(p?.keyGroupRates || {});
  let hint = t("What one dollar of the relay cost you, like 0.07 for ¥7 per $100. Each key's group rate is read from the relay, and its calls cost the two multiplied");
  if (rates.length) {
    const name = (id) => { const k = p.keyList?.find((x) => x.id === id); return k ? k.name || k.masked : id; };
    const read = rates.length === 1 ? "×" + rates[0][1] : rates.map(([id, r]) => name(id) + " ×" + r).join(", ");
    hint += " · " + t("Read from the relay: {rates}", { rates: read });
  }
  return field(t("Top-up rate"), box, hint);
};
// topUpRateOf is the top-up rate typed in box as saved, null for none; a
// rate typed wrong is focused and named, and gives undefined
function topUpRateOf(box) {
  const v = box.value.trim();
  if (!v) return null;
  const n = decimalOf(v);
  if (n > 0 && n <= 1000 && Math.abs(n * 10000 - Math.round(n * 10000)) <= 1e-6) return n;
  shown(box).focus(NO_SCROLL);
  editorError(t("Top-up rate: a number above 0 up to 1000, at most four decimals"), "warn");
  return undefined;
}
// keyRateHint is what a key left empty bills at: its group's rate g read
// from the relay times the top-up rate top, else the provider's Price rate
const keyRateHint = (ed, g, top) => {
  top = decimalOf(String(top ?? "").trim());
  return g != null && top > 0 ? String(+(g * top).toFixed(6)) : (ed?.querySelector("input.price-rate")?.value ?? String(draft.priceRate ?? "")).trim() || "1";
};
function keyPriceRateLine(it) {
  const line = el("div", "key-rate");
  line.dataset.key = it.key;
  if (it.g != null) line.dataset.g = it.g;
  const who = el("span", "who", it.name);
  who.title = it.name;
  const box = input(draft.keyPriceRates[it.k] ?? "", keyRateHint(null, it.g, draft.topUpRate ?? it.top));
  box.classList.add("key-price-rate");
  box.inputMode = "decimal";
  box.setAttribute("aria-label", t("Price rate of {user}", { user: it.name }));
  box.oninput = () => { draft.keyPriceRates[it.k] = box.value; };
  line.append(who, box);
  return line;
}
window.keyPriceRateField = function (p) {
  draft.keyPriceRates = draft.keyPriceRates || {};
  const items = (p?.keyList || []).map((k) => ({ k: k.id, name: k.name || k.masked, key: k.id, info: k, g: p?.keyGroupRates?.[k.id], top: p?.topUpRate }));
  return field(t("Price rate per key"), proxyLines(items, "A key left empty bills at the provider's price rate", true,
    { lineOf: keyPriceRateLine, own: (it) => !!String(draft.keyPriceRates[it.k] ?? "").trim(), cls: "key-rates" }));
};
// a key left empty bills at the provider's Price rate, or its read group
// rate times the top-up rate: its placeholder follows those boxes as they
// are typed
document.addEventListener("input", (e) => {
  const box = e.target;
  if (!box.matches?.("input.price-rate, input.top-up-rate")) return;
  const ed = box.closest(".inner");
  for (const k of ed?.querySelectorAll("input.key-price-rate") || []) {
    const g = k.closest(".key-rate")?.dataset.g;
    k.placeholder = keyRateHint(ed, g == null ? null : +g, ed.querySelector("input.top-up-rate")?.value);
  }
});
// keyPriceRates is each key's own price rate as saved, the keys left
// empty out; a key typed wrong is focused and named, and gives null. An
// editor that drew no lines sends them as they were (undefined). It puts
// the top-up rate in body too, when the editor drew its box, and gives
// null when that is typed wrong.
window.keyPriceRates = function (ed, body) {
  const top = ed.querySelector("input.top-up-rate");
  if (top && (body.topUpRate = topUpRateOf(top)) === undefined) return null;
  if (!ed.querySelector(".key-rates")) return undefined;
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

// keysTag: which of a provider's keys list a model, when not all of them
// do (cliddO11 on X): the key's name, two named, or how many; none when
// every key lists it. The server sends keys only for a provider with two
// or more on, the same providers this file is loaded for.
window.keysTag = function (p, m) {
  const ids = m.keys || [];
  if (!ids.length) return null;
  const n = m.keyCount || ids.length;
  const names = ids.map((id) => { const k = p.keyList?.find((x) => x.id === id); return k ? k.name || k.masked : id; });
  const tag = el("span", "badge mkey", n <= 2 ? names.join(" · ") : t("{n} keys", { n }));
  tag.title = t("Listed by {keys} only", { keys: names.join(", ") + (n > names.length ? " …" : "") });
  return tag;
};
// Copy all keys (@emilochen on X): every key of the provider, one a line,
// for Paste several in another magpie; each key's row still copies its
// own. Not .add: that is the one row that adds.
window.copyAllKeysRow = function (p) {
  const all = addRow(t("Copy all {n} keys", { n: p.keyList.length }), async () => {
    try {
      const r = await api("provider/key", { id: p.id, allKeys: true });
      copy(r.key, "", null, t("{n} keys copied, one a line — paste them into Paste several in another magpie", { n: r.count }));
    } catch (err) { statusErr(err); }
  }, COPY_ICON);
  all.classList.replace("add", "keys-copy-all");
  return [all];
};
