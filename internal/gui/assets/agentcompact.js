// An agent's own Long conversations (molianyin4227 on Discord: models
// differ, so one size for every agent fits none): Every agent follows the
// row above, else this agent compacts at its own size or runs to the whole
// window (settings.AgentCompacts). app.js imports this when an agent's
// sheet holds Long conversations, so it weighs nothing on the core.
window.agentCompactFill = function (list, a) {
  const kind = kindOf(a);
  const row = el("div", "row pref agent-compact");
  row.dataset.agent = kind;
  const who = el("div", "who"), box = el("div");
  who.append(el("div", "name", t("Long conversations in {agent}", { agent: a.name })), el("div", "sub", t("Its own size, for models that differ; Every agent follows Long conversations above")));
  row.append(who, box);
  let drawn;
  const draw = () => {
    // the opened row is built before the settings are read; drawn again then,
    // and only when its own values changed, so the page under it holds still
    const s = prefs || {}, own = (s.agentCompacts || {})[kind];
    const key = JSON.stringify([own, s.compactAt]);
    if (key === drawn) return;
    drawn = key;
    const mode = !own ? "all" : own.full ? "full" : "at";
    // a size of its own starts from the one every agent has
    const at = own ? own.at || 0 : s.compactAt || 0;
    const set = (body) => writingPrefs(api("settings/agent-compact", { agent: kind, ...body }))
      .then((ns) => { prefs = ns; renderSettings(); if (row.isConnected) draw(); }).catch(settingsErr);
    const size = contextsText({ "*": at || 272000 });
    const sg = segs([["all", t("Every agent")], ["at", t("Compact at {n}", { n: size.toUpperCase() })], ["full", t("Full window")]], mode,
      (v) => set(v === "all" ? { own: false } : { own: true, full: v === "full", at }));
    const num = input(at && own ? size : "", "272k");
    num.className = "words compact-num";
    num.hidden = mode !== "at";
    num.title = t("Compact at");
    num.setAttribute("aria-label", t("Compact at"));
    num.onchange = () => {
      const r = parseContexts(num.value);
      if (r.error || Object.keys(r.map).some((k) => k !== "*")) return status(t("Compact at: {v} is not a number of tokens like 272k", { v: num.value.trim() }), "err");
      set({ own: true, full: false, at: r.map["*"] || 0 });
    };
    const line = el("div", "compact-at");
    line.append(sg, num);
    box.replaceChildren(line);
  };
  draw();
  list.querySelector(".agent-compact")?.remove();
  const above = list.querySelector("#fullContextRow");
  above ? above.after(row) : list.append(row);
  // every agent's size, in its label, changes when the row above is drawn
  const seen = new MutationObserver(() => row.isConnected ? draw() : seen.disconnect());
  // Details can build this list before attaching it to the page.
  const segsBox = list.querySelector("#fullContextSegs");
  if (segsBox) seen.observe(segsBox, { childList: true });
};
