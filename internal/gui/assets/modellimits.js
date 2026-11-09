// A model's own context window and max output in the provider editor's
// Model settings (sayagoodbyeplz on Discord: WorkBuddy's deepseek-v4.1-flash
// replies 128K, not models.dev's 384K). A size typed here is staged as
// model=size in Advanced's Context window and Max output, which the Save
// sends; Advanced's field is told so it says the same. The placeholder is
// what Advanced gives every model ("*"), else what is listed. app.js
// imports this when it draws a model's row, so it weighs nothing on the core.
// Its own system prompt (liucl on X) is staged as modelPrefs[id].system,
// in a box shown at the row's end (app.css's order), before unsaved.
window.modelLimitsFill = function (at, m, p, changed) {
  const id = m.id;
  return [...[
    ["contexts", "Context window", m.listed || m.context, p.contexts, t("The window agents are told {id} takes; empty: the vendor's or models.dev's", { id }), (v) => t("Context window: {v} is not a length like 128k or 1m", { v })],
    ["outputs", "Max output", m.listedOutput || m.output, p.outputs, t("The most a reply of {id} may hold, told to agents as their max tokens and kept to by magpie; empty: the vendor's or models.dev's", { id }), (v) => t("Max output: {v} is not a number of tokens like 32k", { v })],
  ].map(([k, label, listed, saved, tip, said]) => {
    const box = el("label", "msame mlimit");
    box.dataset.limit = k;
    const now = () => parseContexts(draft[k] || "").map || {};
    const own = () => now()[id];
    const shownOf = () => own() ? tokenSize(own()) : "";
    // a window over the listed one isn't told, so "*" shows as the listed
    const placeholder = () => { const all = now()["*"]; return all ? tokenSize(k === "contexts" && listed && listed < all ? listed : all) : listed ? tokenSize(listed) : ""; };
    const i = input(shownOf(), placeholder());
    i.title = tip;
    i.spellcheck = false;
    i.onchange = () => {
      const typed = i.value.trim();
      const cx = parseContexts(draft[k] || "");
      if (cx.error) { i.value = shownOf(); status(said(cx.error), "warn"); return; }
      const n = typed === "" ? 0 : tokensTyped(typed);
      if (!(n > 0) && typed !== "") { i.value = shownOf(); status(said(typed), "warn"); return; }
      if (n) cx.map[id] = n; else delete cx.map[id];
      draft[k] = contextsText(cx.map);
      const adv = at.closest(".editor")?.querySelector("input." + k);
      if (adv) { adv.value = draft[k]; adv.dispatchEvent(new Event("input")); }
      i.value = shownOf();
      changed();
    };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") i.blur(); else if (e.key === "Escape") { i.value = shownOf(); i.blur(); } };
    box.append(el("span", "", t(label)), i);
    at.append(box);
    const again = () => { if (document.activeElement !== i) i.value = shownOf(); i.placeholder = placeholder(); };
    return { i, own, again, saved: () => saved?.[id] || 0 };
  }), modelSystemFill(at, m, changed)];
};

function modelSystemFill(at, m, changed) {
  const id = m.id, prefs = draft.modelPrefs = draft.modelPrefs || {};
  const own = () => (prefs[id]?.system ?? m.system) || 0;
  const box = el("label", "msys");
  const i = el("textarea");
  i.rows = 2;
  i.spellcheck = false;
  i.value = own() || "";
  box.title = t("Sent with every request for {id}, after the agent’s own system prompt, whichever agent sends it; empty: none", { id });
  i.oninput = i.onchange = () => {
    const v = i.value.trim(), x = prefs[id] = prefs[id] || {};
    if (v === (m.system || "")) delete x.system; else x.system = v;
    changed();
  };
  let was = "";
  i.onfocus = () => { was = i.value; };
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") { i.value = was; i.onchange(); i.blur(); } };
  box.append(el("span", "", t("System prompt")), i);
  at.append(box);
  return { i, own, again: () => { if (document.activeElement !== i) i.value = own() || ""; }, saved: () => m.system || 0 };
}
