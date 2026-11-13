// Claude Magpie (#6459, xMopf7RF on X): the Separate app's Advanced fold.
// app.js imports this the first time it shows (isoLazy), so it
// weighs nothing on the core.
// the Claude Magpie app's Code tab on a Claude Code folder of its own
// (#6459): set once, seldom changed, so it waits under Advanced, open while
// it is on or the user opened it
let desktopIsoMoreOpen = false;
window.desktopIsolatedMore = function (iso) {
  const d = el("details", "ag-iso-more");
  d.open = desktopIsoMoreOpen || !!iso.claude;
  d.ontoggle = () => { desktopIsoMoreOpen = d.open; };
  d.append(el("summary", "", t("Advanced")), agSwitchLine("ag-iso-claude", !!iso.claude, t("Its own Claude Code folder"), t("Its Code tab uses {path}, without your CLAUDE.md, skills, plugins or history. Quit and reopen Claude Magpie after a change.", { path: iso.claudeDir || "~/.claude-magpie-desktop" }), async (on) => {
    state = await api("settings/desktop-isolated-claude", { on });
    status(t("Saved — quit and reopen Claude Magpie to use it"), "ok");
  }));
  return d;
};
