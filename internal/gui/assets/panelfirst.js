// The tray panel's Usage tab puts a vendor's account first (RealZeroP27
// on X: switch Codex's account there, not on the Providers page). app.js
// draws the vendor's count as a button (panelFirstMenu) and imports this
// when it is pressed, so the menu weighs nothing on the core. The menu
// lists the accounts the provider editor can put first, the first
// ticked; a pick does what the editor's Make first or Use does
// (putFirst), and Manage accounts… opens the window's Providers.
window.panelFirstOpen = async function open(b, qs, f) {
  // the kept list drew it: the menu waits for a fresh read, as the
  // Agents row's account menu does (switchEntry)
  const s = panelAccts?.last && await api("providers").catch(() => null);
  if (s) {
    panelAccts = s;
    renderAgents();
    renderPanelQuota(true);
    const again = [...document.querySelectorAll("#panelQuota .pq-pick")].find((x) => x.closest(".pq-group")?.dataset.provider === qs[0].provider);
    return again?.click();
  }
  const opts = f.can.map((l) => ({ v: "u:" + l.user, name: l.user, literalName: true, note: accountPlan({ agent: f.a.agent, builtin: f.a.builtin, plan: l.plan }) }));
  opts.push({ v: "manage", name: "Manage accounts…", note: "" });
  openProtoMenu(b, opts, "u:" + (f.user || ""), (v) => {
    const l = f.can.find((x) => "u:" + x.user === v);
    if (v === "manage") api("window/main?view=providers", {}).catch(statusErr);
    else if (l && l.user !== f.user) putFirst(f.a, f.p, l).then(() => renderPanelQuota(true));
  }, "Use first", "sess-menu", "left");
};
