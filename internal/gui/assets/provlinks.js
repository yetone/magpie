// Settings' search finds a provider's own settings (Codex's reset and
// warm-up, Claude's warm-up, a plugin's check-in) by these rows, each
// opening the provider's editor. They show only while searching, so
// app.js imports this when a search first wants them, and it weighs
// nothing on the core. A row goes with its list's plugin (warm-ups), and
// with no such provider to open (findSettings, by its agents).
window.provPrefLinksDraw = function () {
  const box = $("#provPrefLinks");
  box.replaceChildren();
  const ids = [...new Set(Object.values(PROV_PREFS).flat())];
  const by = (list) => list.classList.contains("ci-plugin") ? [list.dataset.provider] : Object.keys(PROV_PREFS).filter((k) => PROV_PREFS[k].includes(list.id));
  for (const list of [...ids.map((id) => $("#" + id)), ...$("#pluginCheckinList").children]) {
    if (!list || list.hidden) continue;
    const agents = by(list);
    for (const r of list.querySelectorAll(":scope > .row.pref:not(.ci-acct):not(.ci-now-row):not(.warm-now):not([hidden])")) {
      const name = r.querySelector(".who > .name");
      if (!name) continue;
      const row = el("div", "row pref agent-pref-link");
      const f = list.closest("[data-feature]")?.dataset.feature;
      if (f) row.dataset.feature = f;
      row.agents = agents;
      const who = el("div", "who");
      const n = el("div", "name", name.textContent);
      n.dataset.en = name.dataset.en || name.textContent;
      who.append(n, el("div", "sub", t("In {provider}'s editor, on the Providers page", { provider: subNames(agents.slice(0, 1)) })));
      const sub = r.querySelector(".sub:not(.err)");
      if (sub) {
        const says = el("span", "", sub.textContent);
        says.hidden = true;
        says.dataset.en = sub.dataset.en || sub.textContent;
        who.append(says);
      }
      const go = el("button", "text", t("Open"));
      go.type = "button";
      go.onclick = async () => {
        if (!await show("providers")) return;
        const p = providers?.providers.find((x) => agents.includes(x.account?.agent));
        if (p) openProvider(p.id);
      };
      row.append(who, go);
      box.append(row);
    }
  }
};
