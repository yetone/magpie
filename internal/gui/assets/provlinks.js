// Settings' search finds a provider's own settings (Codex's reset and
// warm-up, Claude's warm-up, a plugin's check-in) by these rows, each
// opening the provider's editor. They show only while searching, so
// app.js imports this when a search first wants them, and it weighs
// nothing on the core. A row goes with its list's plugin (warm-ups), and
// with no such provider to open (findSettings, by its agents). The
// settings an agent's sheet builds when it opens (Claude Code's prompt
// cache and commit signature, its own fields under Advanced) and the
// Claude editor's run mode, which aren't in the page until then, have
// rows here too.
const prefLink = (name, en, where, says, open) => {
  const row = el("div", "row pref agent-pref-link"), who = el("div", "who"), n = el("div", "name", name);
  n.dataset.en = en || name;
  who.append(n, el("div", "sub", where));
  if (says) {
    const w = el("span", "", says[0]);
    w.hidden = true;
    w.dataset.en = says[1] || says[0];
    who.append(w);
  }
  const go = el("button", "text", t("Open"));
  go.type = "button";
  go.onclick = open;
  row.append(who, go);
  return row;
};
// opened, the row it names is brought into sight and lit once drawn, and
// again if the page draws it anew (the editor once the list is in)
const lightWhen = (find) => {
  let n = 0;
  const look = () => {
    const r = find();
    if (r && !r.classList.contains("flash")) {
      const fold = r.closest("details:not([open])");
      if (fold) fold.open = true;
      r.scrollIntoView({ block: "center" });
      r.classList.add("flash");
    }
    if (++n < 40) setTimeout(look, 50);
  };
  look();
};
// an agent's sheet's own rows, by the class compactFill gives them
const SHEET_ROWS = {
  claude: [["Prompt cache · main conversation", ".claude-cache", 0], ["Prompt cache · subagents", ".claude-cache", 1], ["Commit signature (Co-Authored-By)", ".claude-attribution", 0]],
};
function sheetLinks(box) {
  const by = new Map();
  for (const a of state.agents) {
    if (a.id.includes("@wsl:") || !agentPrefsOf(a)) continue;
    const rows = [...(SHEET_ROWS[kindOf(a)] || []).map(([label, sel, i]) => ({ label, sel, i })),
      ...(a.own || []).map((f) => ({ label: f.label[0].toUpperCase() + f.label.slice(1), own: true }))];
    for (const r of rows) {
      const k = r.label + "\n" + (r.sel || "own");
      by.has(k) ? by.get(k).agents.push(a) : by.set(k, { ...r, agents: [a] });
    }
  }
  for (const { label, sel, i, own, agents } of by.values()) {
    box.append(prefLink(t(label), label, t("In {agent}'s settings, on the Agents page", { agent: agents.map((a) => a.name).join(", ") }), null, async () => {
      if (await show("agents") === false) return;
      await openAgentPrefs(agents[0]);
      lightWhen(() => own ? [...document.querySelectorAll("#modal .own-fields .row")].find((r) => r.querySelector(".name")?.textContent === t(label)) : document.querySelectorAll("#modal " + sel)[i]);
    }));
  }
}
// opens the editor of the first provider of these agents, Advanced open
async function openIn(agents, find) {
  if (!await show("providers")) return;
  const p = providers?.providers.find((x) => agents.includes(x.account?.agent));
  if (!p) return;
  advanced = { of: p.id, open: true };
  await openProvider(p.id);
  lightWhen(find);
}
// Settings' search: a hit under a folded Advanced is unfolded while the
// search lasts (findSettings), and folded again when it ends
window.setFolds = (v, end) => {
  for (const d of v.querySelectorAll(end ? "details[data-found]" : "details:not([open]):not([hidden])")) {
    if (end) delete d.dataset.found, d.open = false;
    else if ([...d.querySelectorAll(".row:not(.set-miss)")].some((r) => !r.closest("[hidden]") && !featureHides(r))) d.open = d.dataset.found = true;
  }
};
window.provPrefLinksDraw = function () {
  const box = $("#provPrefLinks");
  box.replaceChildren();
  // the Claude editor's run mode, drawn as the editor opens
  const run = prefLink(t("Claude Code runs as"), "Claude Code runs as", t("In {provider}'s editor, on the Providers page", { provider: subNames(["claude"]) }),
    [t("Interactive mode (TUI)") + " claude -p", "Interactive mode (TUI) claude -p"], () => openIn(["claude"], () => $(".editor .claude-mode")));
  run.agents = ["claude"];
  box.append(run);
  const ids = [...new Set(Object.values(PROV_PREFS).flat())];
  const by = (list) => list.classList.contains("ci-plugin") ? [list.dataset.provider] : Object.keys(PROV_PREFS).filter((k) => PROV_PREFS[k].includes(list.id));
  for (const list of [...ids.map((id) => $("#" + id)), ...$("#pluginCheckinList").children]) {
    if (!list || list.hidden) continue;
    const agents = by(list);
    for (const r of list.querySelectorAll(":scope > .row.pref:not(.ci-acct):not(.ci-now-row):not(.warm-now):not([hidden])")) {
      const name = r.querySelector(".who > .name");
      if (!name) continue;
      const sub = r.querySelector(".sub:not(.err)");
      const row = prefLink(name.textContent, name.dataset.en, t("In {provider}'s editor, on the Providers page", { provider: subNames(agents.slice(0, 1)) }), sub && [sub.textContent, sub.dataset.en], () => openIn(agents, () => r.isConnected && r));
      const f = list.closest("[data-feature]")?.dataset.feature;
      if (f) row.dataset.feature = f;
      row.agents = agents;
      box.append(row);
    }
  }
  sheetLinks(box);
};
