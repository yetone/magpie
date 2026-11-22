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
  const out = [...field(t("Top-up rate"), box, hint), ...rateEveryField(p)];
  // a provider of one key has its factor beside the box; one of more, on
  // each key's line
  const only = p?.keyList?.length === 1 && p.keyList[0];
  if (!only) return out;
  const fx = field(t("Cost factor"), keyRateFactorBox(p, only.id, only.name || only.masked, true));
  for (const n of fx) n.classList.add("solo-factor");
  showFactors(null, box.value, fx);
  return [...out, ...fx];
};
// How often the keys' group rates are read, in hours (@YIXIE233 on X
// asked for 3): empty is magpie's six. Shown, as the factors are, while a
// top-up rate is typed.
function rateEveryField(p) {
  const box = input(draft.rateReadHours ?? (p?.rateReadHours ? String(p.rateReadHours) : ""), "6");
  box.classList.add("rate-read-hours");
  box.inputMode = "numeric";
  box.oninput = () => { draft.rateReadHours = box.value; };
  const out = field(t("Read rates every (hours)"), box);
  for (const n of out) n.classList.add("rate-every");
  showFactors(null, draft.topUpRate ?? (p?.topUpRate ? String(p.topUpRate) : ""), out);
  return out;
}
// rateReadHoursOf is the hours typed in box as saved, null for the
// default; hours typed wrong are focused and named, and give undefined
function rateReadHoursOf(box) {
  const v = box.value.trim();
  if (!v) return null;
  if (/^\d+$/.test(v) && +v >= 1 && +v <= 168) return +v;
  shown(box).focus(NO_SCROLL);
  editorError(t("Read rates every: a whole number of hours from 1 to 168"), "warn");
  return undefined;
}
// A key's factor (@YIXIE233 on X): groups of one relay whose upstreams
// cache 60% and 90% of a prompt cost 2-3× apart at the same group rate.
// The read rate is multiplied by it; a key's own rate is not. Shown where
// rates are read: when the provider has a top-up rate.
function keyRateFactorBox(p, k, name, solo) {
  factorsDraft(p);
  const box = input(factorTyped(k), "1");
  box.classList.add("key-rate-factor");
  box.dataset.key = k;
  box.dataset.name = name;
  box.inputMode = "decimal";
  if (!solo) box.setAttribute("aria-label", t("Cost factor of {user}", { user: name }));
  box.oninput = () => { (draft.keyRateFactors ||= {})[k] = box.value; };
  return box;
}
// factorsSaved are the open editor's factors as saved; draft.keyRateFactors
// holds only those typed, so opening an editor leaves it unchanged (no
// Discard asked on leaving)
let factorsSaved = {};
const factorsDraft = (p) => { factorsSaved = Object.fromEntries(Object.entries(p?.keyRateFactors || {}).map(([k, f]) => [k, String(f)])); };
// factorTyped: the key's factor as typed, else as saved
const factorTyped = (k) => draft.keyRateFactors?.[k] ?? factorsSaved[k] ?? "";
// factorOf is a factor as typed, 1 for none or one typed wrong
const factorOf = (v) => { const n = decimalOf(String(v ?? "").trim()); return n > 0 ? n : 1; };
// showFactors shows the factors when the top-up rate typed is one: the
// per-key column in ed (or the nodes given), else hides them
function showFactors(ed, top, nodes) {
  const on = decimalOf(String(top ?? "").trim()) > 0;
  for (const n of nodes || ed?.querySelectorAll(".solo-factor, .rate-every") || []) n.hidden = !on;
  ed?.querySelector(".key-rates-all")?.classList.toggle("fx", on);
}
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
// from the relay times the top-up rate top and its factor f, else the
// provider's Price rate
const keyRateHint = (ed, g, top, f) => {
  top = decimalOf(String(top ?? "").trim());
  return g != null && top > 0 ? String(+(g * top * factorOf(f)).toFixed(6)) : (ed?.querySelector("input.price-rate")?.value ?? String(draft.priceRate ?? "")).trim() || "1";
};
function keyPriceRateLine(it) {
  const line = el("div", "key-rate");
  line.dataset.key = it.key;
  if (it.g != null) line.dataset.g = it.g;
  const who = el("span", "who", it.name);
  who.title = it.name;
  const factor = keyRateFactorBox(it.p, it.k, it.name);
  const box = input(draft.keyPriceRates[it.k] ?? "", keyRateHint(null, it.g, draft.topUpRate ?? it.top, factor.value));
  box.classList.add("key-price-rate");
  box.inputMode = "decimal";
  box.setAttribute("aria-label", t("Price rate of {user}", { user: it.name }));
  box.oninput = () => { draft.keyPriceRates[it.k] = box.value; };
  line.append(who, box, factor);
  return line;
}
window.keyPriceRateField = function (p) {
  draft.keyPriceRates = draft.keyPriceRates || {};
  factorsDraft(p);
  const items = (p?.keyList || []).map((k) => ({ k: k.id, name: k.name || k.masked, key: k.id, info: k, g: p?.keyGroupRates?.[k.id], top: p?.topUpRate, p }));
  const lines = proxyLines(items, "A key left empty bills at the provider's price rate", true,
    { lineOf: keyPriceRateLine, own: (it) => !!String(draft.keyPriceRates[it.k] ?? "").trim() || !!String(factorTyped(it.k)).trim(), cls: "key-rates" });
  if (!lines) return field(t("Price rate per key"), lines);
  // the factors' column is named once, over its boxes
  const all = el("div", "key-rates-all");
  const head = el("div", "key-rate-head");
  head.append(el("span"), el("span"), el("span", "", t("Cost factor")));
  all.append(head, lines);
  all.classList.toggle("fx", decimalOf(String(draft.topUpRate ?? p?.topUpRate ?? "").trim()) > 0);
  return field(t("Price rate per key"), all);
};
// a key left empty bills at the provider's Price rate, or its read group
// rate times the top-up rate and its factor: its placeholder follows those
// boxes as they are typed, and the factors show while a top-up rate is
// typed
document.addEventListener("input", (e) => {
  const box = e.target;
  if (!box.matches?.("input.price-rate, input.top-up-rate, input.key-rate-factor")) return;
  const ed = box.closest(".inner");
  const top = ed?.querySelector("input.top-up-rate")?.value;
  if (box.matches("input.top-up-rate")) showFactors(ed, top);
  for (const k of ed?.querySelectorAll("input.key-price-rate") || []) {
    const line = k.closest(".key-rate");
    const g = line?.dataset.g;
    k.placeholder = keyRateHint(ed, g == null ? null : +g, top, line?.querySelector("input.key-rate-factor")?.value);
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
  const every = ed.querySelector("input.rate-read-hours");
  if (every && body.topUpRate != null && (body.rateReadHours = rateReadHoursOf(every)) === undefined) return null;
  if (ed.querySelector("input.key-rate-factor") && (body.keyRateFactors = keyRateFactorsOf(ed)) === null) return null;
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

// keyRateFactorsOf is each key's factor as saved, the keys left empty
// out; a factor typed wrong is focused and named, and gives null
function keyRateFactorsOf(ed) {
  const map = {};
  for (const [k, v] of Object.entries({ ...factorsSaved, ...draft.keyRateFactors })) {
    const s = String(v ?? "").trim();
    if (!s) continue;
    const n = decimalOf(s);
    if (!(n > 0 && n <= 1000) || Math.abs(n * 10000 - Math.round(n * 10000)) > 1e-6) {
      const box = ed.querySelector(`input.key-rate-factor[data-key="${k}"]`);
      shown(box)?.focus(NO_SCROLL);
      editorError(t("Cost factor of {user}: a number above 0 up to 1000, at most four decimals", { user: box?.dataset.name || k }), "warn");
      return null;
    }
    map[k] = n;
  }
  return map;
}

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
