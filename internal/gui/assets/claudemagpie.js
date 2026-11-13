// Claude Magpie (#6459, xMopf7RF on X): the Agents page's "Use Claude
// Magpie" button and the Separate app's Advanced fold. app.js imports this
// the first time either shows (isoLazy), so it weighs nothing on the
// core.
// desktopTwinBtn: Claude Magpie.app made and put on magpie in one go, the
// user's own Claude Desktop left as it is (xMopf7RF on X, #6459), like a
// phone's app twin: its own app, its own folder
window.desktopTwinBtn = function (a) {
  const b = el("button", "ag-quiet ag-twin", t("Use Claude Magpie"));
  b.type = "button";
  b.title = t("A second Claude Desktop, on magpie, in Applications. Your own Claude Desktop isn't changed. macOS only.");
  b.onclick = async (e) => {
    e.stopPropagation();
    if (b.disabled) return;
    b.disabled = true;
    try {
      state = await api("settings/desktop-isolated", { on: true });
      state = await api("agents/connect/" + a.id, {});
      renderAgents();
      status(t("Open Claude Magpie from Applications"), "ok");
    } catch (err) {
      renderAgents();
      status(t(err.message), "err");
    }
  };
  return b;
};

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
