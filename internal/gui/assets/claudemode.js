// The Claude Code provider editor's "Claude Code runs as" row: whether a
// Claude subscription's runs are Claude Code's interactive mode (TUI) or
// claude -p, switched right there, how the last request went, and on hover
// what each is and what it is good for (the owner, 10-31, 11-01). With the
// interactive mode on a request it can't take fails, saying why (the owner,
// 11-01); a turn Anthropic refuses there as a third-party app's runs as -p
// (#4503). app.js imports this with the row's first
// read of /api/claude/mode (claudeModeRow), so it weighs nothing on the
// core.

// why the interactive mode can't run here, or is off, by the gateway's
// code (claudeMachineWhy)
const CC_WHY = {
  off: "the interactive mode is turned off",
  os: "this system can't run the interactive mode",
  missing: "Claude Code isn't installed on this computer",
};
const ccWhy = (code) => t(CC_WHY[code] || CC_WHY.os);

// ccTip: the two modes side by side, the one chosen marked
let ccTipEl = null;
function ccTip(anchor, tui) {
  ccTipOff();
  const tip = ccTipEl = el("div", "cc-tip");
  tip.setAttribute("role", "tooltip");
  const mode = (on, name, what, good, bad) => {
    const m = el("div", "cc-m" + (on ? " on" : ""));
    const h = el("div", "h");
    h.append(el("b", "", name));
    if (on) h.append(el("span", "now", t("In use")));
    m.append(h, el("p", "", what), el("p", "good", good));
    if (bad) m.append(el("p", "bad", bad));
    return m;
  };
  tip.append(
    mode(tui, t("Interactive mode (TUI)"),
      t("magpie opens Claude Code as a person does, in a terminal of its own that you don't see, types each request in and reads the reply from its transcript."),
      t("Your plan counts these requests as Claude Code's own use, at about half of the 5-hour allowance that -p uses for the same work.")),
    mode(!tui, "claude -p",
      t("Claude Code's print mode, the one scripts and the Agent SDK use: a request in, its reply out."),
      t("It starts at once, with no terminal of its own to open first."),
      t("The plan counts a -p request at about twice the interactive mode's.")),
    el("div", "foot", t("Switched here. With the interactive mode on, a request it can't take, or one it fails to start for, fails saying why. A turn Anthropic refuses there as a third-party app's runs as -p.")));
  document.body.append(tip);
  const a = anchor.getBoundingClientRect(), b = tip.getBoundingClientRect();
  tip.style.left = Math.max(8, Math.min(a.left, innerWidth - b.width - 8)) + "px";
  tip.style.top = (a.bottom + 8 + b.height < innerHeight - 8 ? a.bottom + 8 : Math.max(8, a.top - b.height - 8)) + "px";
}
function ccTipOff() {
  ccTipEl?.remove();
  ccTipEl = null;
}

// claudeModeDraw fills box, the row app.js put in the editor, from m, a
// read of /api/claude/mode
window.claudeModeDraw = function (box, m) {
  if (!m) return;
  const v = el("div", "cc-run");
  // on unless turned off, whether or not this machine can run it: on where
  // it can't, Claude requests fail
  const on = m.why !== "off";
  const pick = segs([["tui", t("Interactive mode (TUI)")], ["p", "claude -p"]], on ? "tui" : "p", async (id) => {
    ccTipOff();
    if ((id === "tui") === on) return;
    // the settings are read with the Settings page: one not opened yet has
    // none to keep
    if (!prefs) prefs = state.settings = await api("settings");
    await savePrefs({ ...prefsKeep(prefs), claudePrintMode: id === "p" });
    claudeMode = await api("claude/mode").catch(() => null);
    claudeModeAt = Date.now();
    if (box.isConnected) window.claudeModeDraw(box, claudeMode);
  });
  pick.classList.add("cc-pick");
  pick.classList.toggle("tui", m.tui);
  // magpie web shows the gateway's, which only its own Settings change
  for (const b of pick.querySelectorAll(".opt")) b.disabled = gatewayMode;
  // Chromium has no onfocusin property: the events are listened for
  pick.onmouseenter = () => ccTip(pick, on);
  pick.addEventListener("focusin", () => ccTip(pick, on));
  pick.onmouseleave = () => ccTipOff();
  pick.addEventListener("focusout", (e) => { if (!pick.contains(e.relatedTarget)) ccTipOff(); });
  v.append(pick);
  const last = m.last;
  let said = "";
  if (on && !m.tui) said = t("Claude requests fail: {why}", { why: ccWhy(m.why) });
  else if (!on) said = t("Every request runs as claude -p");
  else if (last && last.tui) said = t("Last request {ago}: interactive mode", { ago: ago(last.at) });
  if (said) v.append(el("span", "cc-said" + (on && !m.tui ? " bad" : ""), said));
  box.replaceChildren(...field(t("Claude Code runs as"), v, ""));
};
// the editor going away takes its tip with it
addEventListener("pointerdown", (e) => { if (!e.target.closest?.(".cc-pick")) ccTipOff(); }, true);
