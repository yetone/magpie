// A package's sheet, opened from outside the Plugins page: a
// magpie://plugin?name=<package> link (the window comes up on Plugins with
// ?plugin=, importlink.go) or a plugin page's magpie.openPlugin(name)
// (pluginpages.js), where the sheet comes up over the page the reader is
// on. It only opens the sheet: what the package is, who published it and
// its README, and Install for the reader to press. plugins.js imports
// this when one comes, so it weighs nothing on the core.

// a package's name as npm takes a new one, as plugin.NPMName: no
// version, path, git repository or URL
const NAME = /^(@[a-z0-9][a-z0-9._~-]*\/)?[a-z0-9][a-z0-9._~-]*$/;
export const packageName = (s) => typeof s === "string" && s.length <= 214 && NAME.test(s);

const SHIELD = "M8 2.3 13 4v3.8c0 3-2.1 5.1-5 5.9-2.9-.8-5-2.9-5-5.9V4z M5.8 8l1.6 1.6 2.9-3";
function unlisted(pkg) {
  const w = el("p", "pm-note warn");
  w.append(svg(SHIELD, 12, 1.5), el("span", "", t("Opened from a link: magpie doesn't list {name}, and nobody at magpie has reviewed it. Read what it does before you install it.", { name: pkg })));
  return w;
}

// got is plugins.js's: it loads what's installed and the market if they
// aren't yet, then gives [[the market's cards, the tagged
// repositories'], what npm said of each package, detail(l): the sheet]
export async function open(pkg, got) {
  // the window's own ?plugin=, which a reload doesn't open again; only in
  // the main window, not the panel
  if (pkg instanceof URLSearchParams) {
    const q = pkg;
    pkg = q.get("plugin");
    q.delete("plugin");
    history.replaceState(null, "", "?" + q);
    if (mode === "window") return open(pkg, got).catch(statusErr);
    return;
  }
  if (!packageName(pkg)) throw new Error(JSON.stringify(String(pkg).slice(0, 100)) + " is not an npm package's name");
  const [[a, b], npm, detail] = await got();
  const l = [...a || [], ...b || []].find((x) => x.package === pkg);
  if (l) return detail(l);
  if (!npm[pkg]?.version) {
    try { Object.assign(npm, (await api("plugins/npm?names=" + encodeURIComponent(pkg))).npm || {}); } catch {}
  }
  // one the market doesn't list: the sheet says nobody reviewed it
  await detail({ package: pkg, name: pkg, npm: npm[pkg], note: unlisted(pkg) });
}

// how often a page may open a sheet
const OPEN_MS = 1500;

// a plugin page's (or panel's) magpie.openPlugin, p its pluginpages.js
// record: only from one the reader is looking at, over no other dialog,
// and once each OPEN_MS, so a page can't keep throwing sheets up. A
// refused name spends the turn too.
export async function fromPage(p, m) {
  const seen = p.context ? !!p.frame?.isConnected && !!p.frame.offsetParent : view === p.entry.key && p.shown !== false;
  if (!seen || document.hidden) throw new Error("magpie.openPlugin: only a page the reader is looking at opens a plugin");
  const dlg = $("#modal");
  if (dlg && !dlg.hidden && !dlg.classList.contains("out") && !dlg.querySelector(".pm-detail")) throw new Error("magpie.openPlugin: a dialog is open");
  if (Date.now() - (p.opened || 0) < OPEN_MS) throw new Error("magpie.openPlugin: one each " + OPEN_MS / 1e3 + "s");
  if (!window.pluginOpen) throw new Error("magpie.openPlugin: the Plugins page is off");
  p.opened = Date.now();
  await window.pluginOpen(m.params?.name);
  return { opened: true };
}
