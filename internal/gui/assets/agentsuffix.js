// An agent's own Provider in model names (KillTry27355 on X: one switch
// for every agent was too few), in its Details: As in Settings follows the
// one for every agent (""), or On, Not on names I set or Off for this agent
// alone (settings.AgentSuffix). app.js imports this when it opens an
// agent's Details, so it weighs nothing on the core.
const SUFFIX_NAMES = { on: "On", own: "Not on names I set", off: "Off" };
window.agentSuffixPick = function (a) {
  // kept by the id its lists are kept by (Agent.ListsFor)
  const id = kindOf(a).split("#")[0].toLowerCase();
  const s = state.settings || {}, own = (s.agentSuffix || {})[id] || "";
  const every = t("As in Settings · {mode}", { mode: t(SUFFIX_NAMES[suffixMode(s)]) });
  const label = t("Provider in model names");
  const b = el("button", "sess-pick ag-suffix");
  b.type = "button";
  b.dataset.mode = own;
  b.setAttribute("aria-label", label);
  b.setAttribute("aria-haspopup", "menu");
  b.setAttribute("aria-expanded", "false");
  b.append(el("span", "", own ? t(SUFFIX_NAMES[own]) : every), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    const opts = [{ v: "", name: every, note: "" }, ...["on", "own", "off"].map((v) => ({ v, name: t(SUFFIX_NAMES[v]), note: "" }))];
    openProtoMenu(b, opts, own, (v) => {
      if (v === own) return;
      writingPrefs(api("settings/agent-suffix", { agent: a.id, mode: v }))
        .then((ns) => { prefs = state.settings = ns; renderAgents(); })
        .catch(settingsErr);
    }, label, "sess-menu", "left");
  };
  return b;
};
