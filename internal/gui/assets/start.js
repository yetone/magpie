// The window's start, loaded last: app.js, the plugins' pages and the
// scripts after app.js are all there, so the first page shown has its
// code (the Usage page's is a plugin's, run after app.js).
// the plugins' pages wired up (kernel.js), before the first is shown
magpie.start();
navHides(); // the pages built-in plugins switched off have, from boot.js
// The window opens on the page the reader last left, as a Mac app comes back
// as it was (molvqingtai, lc on Discord): a page asked for (?view=, a link
// from the tray or a reload) first, then the one kept; a page gateway mode
// leaves out, or one there is no longer, gives way to the first page.
// the pages the window can open on besides the first (Agents)
const OPEN_VIEWS = ["providers", "plugins", "settings", ...magpie.pages()];
function keptView() {
  const asked = params.get("view");
  if (asked === "agents" || OPEN_VIEWS.includes(asked)) return asked;
  let v = "";
  try { v = localStorage.getItem("magpie.lastView") || ""; } catch { /* none kept */ }
  return OPEN_VIEWS.includes(v) ? v : ""; // one gateway mode leaves out, show() makes Providers
}
const firstView = mode === "window" ? keptView() || (navMine().includes("agents") ? "providers" : "") : "";
// from the tray panel's More in Plugins (q): Discover, once show() picked the tab (#3579)
if (OPEN_VIEWS.includes(firstView)) show(firstView).then(() => view === "plugins" && params.has("q") && window.pluginQuery?.(params.get("q")));
else if (mode === "window" && !gatewayMode && !sideNav()) slide($("#nav"), "nav");
// in gateway mode the first page is Providers, Agents' tab gone
if (gatewayMode) setGatewayMode(true);
load();
