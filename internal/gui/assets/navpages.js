// Settings › General › Sidebar pages (youimark on X: a docker gateway has
// no use for Agents or Library): which pages this browser leaves out of
// the sidebar, kept in localStorage magpie.navHidden as this reader's own
// (navMine in app.js reads it). app.js imports this when Settings draws,
// so it weighs nothing on the core.
window.renderNavPages = function () {
  const row = $("#navPagesRow");
  if (!row) return;
  const pages = [...$("#nav").querySelectorAll("button[data-view]")]
    .filter((b) => b.dataset.view !== "providers" && !(gatewayMode && GATEWAY_HIDES.includes(b.dataset.view)) && !featureHides(b))
    .map((b) => ({ v: b.dataset.view, name: b.querySelector("[data-t]")?.dataset.en || b.textContent.trim() }));
  row.hidden = mode !== "window" || !pages.length;
  if (row.hidden) return;
  const mine = navMine();
  const n = pages.filter((p) => mine.includes(p.v)).length;
  let b = row.querySelector(".nav-pages-pick");
  if (!b) {
    b = el("button", "sess-pick pref-pick nav-pages-pick");
    b.type = "button";
    menuOf(b);
    b.onclick = () => {
      const now = navMine();
      openProtoMenu(b, pages.map((p) => ({ v: p.v, name: p.name })), pages.map((p) => p.v).filter((v) => !now.includes(v)), (shown) => {
        const keep = now.filter((v) => !pages.some((p) => p.v === v));
        const hide = [...keep, ...pages.map((p) => p.v).filter((v) => !shown.includes(v))];
        try { localStorage.setItem("magpie.navHidden", JSON.stringify(hide)); } catch { /* not kept */ }
        navHides();
        if (offView(view) !== view) show(offView(view));
        else if (!sideNav()) slide($("#nav"), "nav");
        window.renderNavPages();
      }, "Sidebar pages", "", "left", true);
    };
    row.append(b);
  }
  b.textContent = n ? t("{n} hidden", { n }) : t("All shown");
};
