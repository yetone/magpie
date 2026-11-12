// An agent's own context windows (ul3140773077392 on X: dsh compacts
// earlier than Codex at the same window, so its window wants setting
// apart from every other agent's). One line in the agent's sheet, model=size
// pairs or one size for all: the model as the catalog has it
// (provider/model, group/<id>), provider/*, a bare model id or *. Empty is
// each model's own window (settings.AgentContexts). app.js imports this when
// an agent's sheet opens, so it weighs nothing on the core.
window.agentWindowFill = function (list, a) {
  const kind = kindOf(a).split("#")[0];
  const row = el("div", "row pref agent-window");
  row.dataset.agent = kind;
  const who = el("div", "who"), box = el("div");
  who.append(el("div", "name", t("Context window")));
  const own = () => ((prefs || {}).agentContexts || {})[kind] || {};
  const num = input(contextsText(own()), t("e.g. 128k · or gpt-6=1m, comma separated"));
  num.className = "words window-in";
  num.spellcheck = false;
  num.setAttribute("aria-label", t("Context window"));
  const shown = () => { if (document.activeElement !== num) num.value = contextsText(own()); };
  num.onchange = () => {
    const r = parseContexts(num.value);
    if (r.error) return status(t("Context window: {v} is not a length like 128k or 1m", { v: r.error }), "err");
    writingPrefs(api("settings/agent-contexts", { agent: kind, contexts: r.map }))
      .then((ns) => { prefs = ns; renderSettings(); shown(); status(t("Takes effect when {agent} next starts", { agent: a.name }), "ok"); }).catch(settingsErr);
  };
  num.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") num.blur(); };
  box.append(num);
  row.append(who, box);
  list.querySelector(".agent-window")?.remove();
  list.append(row);
};
