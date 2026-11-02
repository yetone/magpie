// Scroll to top and Scroll to bottom (#1090, emo172): a small capsule at the
// lower right of the open page, shown once the page is scrolled a screen
// down and gone again near its top, like a long page's in Safari. app.js
// imports this on the window's first scroll, so it weighs nothing on the
// core. A press is the reader's: it goes through scrollOnPurpose, and
// arrived() keeps the page where it ended. Home and End still scroll the
// page as they always did; the buttons are for a mouse, and Tab reaches
// them too. The tray panel has none.
(() => {
  if (document.body.classList.contains("panel")) return;
  const box = el("div", "view-ends");
  const up = el("button", "ve-up"), down = el("button", "ve-down");
  up.type = down.type = "button";
  up.append(svg("m4.5 9.5 3.5-3.5 3.5 3.5", 14, 1.7));
  down.append(svg("m4.5 6.5 3.5 3.5 3.5-3.5", 14, 1.7));
  box.append(up, el("span", "ve-sep"), down);
  document.body.append(box);
  let lang = null;
  const open = () => [...document.querySelectorAll(".view")].find((v) => !v.hidden);
  const atEnd = (v) => v.scrollTop + v.clientHeight >= v.scrollHeight - 2;
  function paint() {
    if (lang !== locale) {
      lang = locale;
      for (const [b, s] of [[up, "Scroll to top"], [down, "Scroll to bottom"]]) b.title = t(s), b.setAttribute("aria-label", t(s));
    }
    const v = open(), modal = document.getElementById("modal");
    const held = box.contains(document.activeElement) || box.matches(":hover");
    let on = !!v && (!modal || modal.hidden) && v.scrollTop > v.clientHeight;
    if (!v || !on && !held) { box.classList.remove("on"); return; }
    const r = v.getBoundingClientRect(), bar = v.offsetWidth - v.clientWidth, pad = parseFloat(getComputedStyle(v).paddingRight) || 0;
    // in the page's right margin when a wide window leaves one; else over the
    // page's corner, and out of the way at the page's foot, where its last
    // row can't be scrolled out from under it. The page's own height is
    // never changed for it: app.js's "where the reader is" measures it.
    const gutter = pad >= box.offsetWidth + 16;
    if (!gutter && v.scrollHeight - v.clientHeight - v.scrollTop < box.offsetHeight + 24) on = false;
    box.classList.toggle("on", on || held);
    let bottom = innerHeight - r.bottom + 16;
    // above a page's own sticky foot (Providers' Add buttons)
    const foot = v.querySelector(":scope > .after-list:not(.away)");
    if (foot && getComputedStyle(foot).position === "sticky") bottom = Math.max(bottom, innerHeight - foot.getBoundingClientRect().top + 8);
    box.style.right = Math.max(8, innerWidth - r.right + bar + (gutter ? (pad - box.offsetWidth) / 2 : 10)) + "px";
    box.style.bottom = bottom + "px";
    up.setAttribute("aria-disabled", v.scrollTop < 1);
    down.setAttribute("aria-disabled", atEnd(v));
  }
  let queued = false;
  const later = () => { if (!queued) { queued = true; requestAnimationFrame(() => { queued = false; paint(); }); } };
  addEventListener("scroll", later, { capture: true, passive: true });
  addEventListener("resize", later);
  box.addEventListener("focusout", later);
  box.addEventListener("pointerleave", later);
  new MutationObserver(later).observe(document.body, { subtree: true, attributes: true, attributeFilter: ["hidden"] });
  function go(e, top) {
    const v = open();
    if (!v || e.currentTarget.getAttribute("aria-disabled") === "true") return;
    if (!scrollOnPurpose(e, 2500)) return;
    const to = top ? 0 : v.scrollHeight - v.clientHeight;
    v.scrollTo({ top: to, behavior: calm() ? "auto" : "smooth" });
    // done when it has stood still a few frames (WebKit has no scrollend)
    let last = -1, still = 0;
    const until = performance.now() + 2400;
    const settle = () => {
      if (v.scrollTop === last) still++; else still = 0, last = v.scrollTop;
      if (still < 3 && performance.now() < until) return requestAnimationFrame(settle);
      arrived(v);
      paint();
    };
    requestAnimationFrame(settle);
  }
  up.onclick = (e) => go(e, true);
  down.onclick = (e) => go(e, false);
  paint();
})();
