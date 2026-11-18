// Settings › General › Sidebar pages (youimark on X: a docker gateway has
// no use for Agents or Library): which pages this browser leaves out of
// the sidebar, kept in localStorage magpie.navHidden as this reader's own
// (navMine in app.js reads it), and the order they go in (chet030714 on
// X: Routing, Providers and Agents first in a phone's tab bar, the rest
// under More), kept in magpie.navOrder (navorder.js).
// A row is dragged to its place, or moved with Alt+↑/↓; Providers is
// always shown. app.js imports this when Settings draws, so it weighs
// nothing on the core.
import "./navorder.js";
import "./guideback.js";
const navPagesOf = (nav) => [...nav.querySelectorAll(":scope > button[data-view]")]
  .filter((b) => !(gatewayMode && GATEWAY_HIDES.includes(b.dataset.view)) && !featureHides(b))
  .map((b) => ({ v: b.dataset.view, name: (b.querySelector("span[data-t]") || b.querySelector("span"))?.textContent || b.textContent.trim(), plugin: "pluginPage" in b.dataset }));

function navPagesSave(k, list) {
  try {
    if (list) localStorage.setItem("magpie.nav" + k, JSON.stringify(list));
    else localStorage.removeItem("magpie.nav" + k);
  } catch { /* not kept */ }
  navHides();
  if (offView(view) !== view) show(offView(view));
  else if (!sideNav()) slide($("#nav"), "nav");
  navSubsAt = "";
  navSubs();
  window.renderNavPages();
}

const words = (name) => { const w = el("span", "pm-words"); w.append(el("span", "pm-name", name)); return w; };

function navPagesMenu(anchor) {
  closeProtoMenu();
  const nav = $("#nav"), box = el("div", "pop proto-menu nav-pages-menu"), list = el("div", "nav-pages-list");
  box.setAttribute("role", "menu");
  const reset = el("button", "pm-item nav-pages-reset");
  reset.type = "button";
  reset.append(el("span", "pm-tick"), words(t("Default order")));
  reset.onclick = () => { navPagesSave("Order", null); draw(); list.querySelector(".pm-item")?.focus(NO_SCROLL); };
  hoverFocus(reset);
  box.append(el("div", "pm-head", t("Sidebar pages")), list, reset);
  const home = () => navHome.flatMap((e) => e.dataset.view || []);
  const movable = () => [...list.querySelectorAll(".pm-item[data-moves]")];
  const focus = (v) => list.querySelector(`[data-v="${CSS.escape(v)}"]`)?.focus(NO_SCROLL);
  // v to place to among the pages this menu lists, the others where they are
  const move = (v, to) => {
    const ps = movable().map((r) => r.dataset.v), all = navOrder(home());
    if (to < 0 || to >= ps.length || ps.indexOf(v) === to) return;
    const s = ps.filter((x) => x !== v);
    s.splice(to, 0, v);
    const o = all.map((x) => ps.includes(x) ? s.shift() : x);
    navPagesSave("Order", o.join() === home().join() ? null : o);
    draw();
    focus(v);
  };
  const item = (p, hid) => {
    const r = el("button", "pm-item"), on = p.v === "providers" || !hid.includes(p.v);
    r.type = "button";
    r.dataset.v = p.v;
    r.setAttribute("role", "menuitemcheckbox");
    r.setAttribute("aria-checked", String(on));
    r.classList.toggle("on", on);
    const tick = el("span", "pm-tick");
    if (on) tick.append(svg(CHECK, 12, 1.9));
    r.append(tick, words(p.name));
    if (p.v === "providers") {
      r.classList.add("pm-fixed");
      r.setAttribute("aria-disabled", "true");
      r.title = t("Always shown");
    }
    r.onclick = () => {
      if (r.dataset.dragged || p.v === "providers") return;
      const h = navMine();
      navPagesSave("Hidden", on ? [...h, p.v] : h.filter((x) => x !== p.v));
      draw();
      focus(p.v);
    };
    hoverFocus(r);
    // a plugin's page keeps its place in the Plugins group (pluginpages.js)
    if (!p.plugin) {
      r.dataset.moves = "";
      r.append(el("span", "grip"));
      r.setAttribute("aria-description", t("Drag to reorder · Alt+↑/↓ to move"));
      r.onpointerdown = (e) => dragRows(e, r, r, list, movable(), (to) => move(p.v, to));
    }
    return r;
  };
  const draw = () => {
    const hid = navMine();
    list.replaceChildren(...navPagesOf(nav).map((p) => item(p, hid)));
    reset.hidden = navOrder(home()).join() === home().join();
  };
  draw();
  const keys = (e) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [...box.querySelectorAll(".pm-item:not([hidden])")], i = items.indexOf(document.activeElement);
    if (!e.altKey) return stepFocus(e, items, i);
    const r = items[i];
    if (!r || !("moves" in r.dataset)) return;
    e.preventDefault();
    e.stopPropagation();
    move(r.dataset.v, movable().indexOf(r) + (e.key === "ArrowDown" ? 1 : -1));
  };
  document.body.append(box);
  placeMenu(box, anchor, "left");
  anchor.classList.add("open");
  anchor.setAttribute("aria-expanded", "true");
  holdMenu(box, anchor, keys, (e) => { if (scrollCloses(box, e)) closeProtoMenu(); });
  list.querySelector(".pm-item")?.focus(NO_SCROLL);
}

window.renderNavPages = function () {
  window.guideBack(); // Get started, beside these (guideback.js)
  const row = $("#navPagesRow"), nav = $("#nav");
  if (!row) return;
  // only a window has a sidebar: the panel has no #nav (#6139)
  if (mode !== "window" || !nav) { row.hidden = true; return; }
  const pages = navPagesOf(nav);
  row.hidden = pages.length < 2;
  if (row.hidden) return;
  const mine = navMine();
  const n = pages.filter((p) => p.v !== "providers" && mine.includes(p.v)).length;
  let b = row.querySelector(".nav-pages-pick");
  if (!b) {
    b = el("button", "sess-pick pref-pick nav-pages-pick");
    b.type = "button";
    menuOf(b);
    b.onclick = () => b.classList.contains("open") ? closeProtoMenu() : navPagesMenu(b);
    row.append(b);
  }
  b.textContent = n ? t("{n} hidden", { n }) : t("All shown");
};
