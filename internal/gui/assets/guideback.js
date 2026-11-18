// Settings › General › Get started: the Agents page's card, once its ×
// put it away, brought back (nothing did, and a user who hid it before
// setting up had no guide left). The row shows only while the card is
// hidden and would show otherwise, so it is seldom there. navpages.js
// imports this when Settings draws, so it weighs nothing on the core.
window.guideBack = function () {
  const row = $("#guideBackRow");
  if (!row || !state) return;
  const set = ((state.served || 0) > 0 || anyMagpieModels()) && state.agents.some((a) => a.wired);
  row.hidden = gatewayMode || !state.settings?.guideHidden || set;
  if (row.hidden || row.querySelector("button")) return;
  const b = el("button", "text", t("Show"));
  b.type = "button";
  b.onclick = async () => {
    b.disabled = true;
    try {
      prefs = await writingPrefs(api("settings/guide", { hidden: false }));
      state.settings = prefs;
      renderAgents();
      status(t("Get started is back on the Agents page"), "ok");
    } catch (err) {
      statusTErr(err);
    }
    b.disabled = false;
    window.guideBack();
  };
  row.append(b);
};
