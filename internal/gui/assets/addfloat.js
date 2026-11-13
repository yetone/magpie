// addToolsFloat: the add sheet's tools (search, Custom provider, Import…)
// scrolled out of sight show in the title bar's empty right, as Library's,
// Plugins' and Settings' tools do beside the sidebar (app.css
// .add-tools.afloat); scrolled back they are in their row again. The group
// itself is pinned, never a copy, so there is one search, its words and
// focus kept. A narrow window has no room in its bar: the sheet's head is
// held at the top of the page while the sheet is under it, as Settings'
// is (app.css). Each change fades in; a fade only, so reduced motion keeps it.
// app.js renderAdd imports this as the sheet first opens.
window.addToolsFloat = function (drawn) {
  const v = $("#view-providers"), g = v.querySelector("#addSheet:not([hidden]) .add-tools");
  if (!g) return;
  const head = g.parentElement, sheet = head.parentElement, top = v.getBoundingClientRect().top;
  const side = document.body.classList.contains("side-nav");
  const h = head.getBoundingClientRect();
  const up = side && !v.hidden && h.bottom <= top + 2;
  head.classList.toggle("stuck", !side && h.top - sheet.getBoundingClientRect().top > 6.5);
  if (up === g.classList.contains("afloat")) return;
  g.classList.toggle("afloat", up);
  self.sideTools?.();
  if (!drawn) g.animate([{ opacity: 0 }, { opacity: 1 }], { duration: up ? 180 : 140, easing: "cubic-bezier(.23, 1, .32, 1)" });
};
$("#view-providers").addEventListener("scroll", () => addToolsFloat(), { passive: true });
addEventListener("resize", () => addToolsFloat());

// wholeHint: the add sheet's search never cuts its hint off (WJaunita45604
// on X: 输入框里面的提示的字显示不全); app.js calls it as the sheet draws
// the search. The box is as wide as its hint (--hint-w, which app.css
// sizes it by), and where even that has no room, as in a narrow title
// bar, it gives the shorter hint instead.
window.wholeHint = function (q, short) {
  const long = q.placeholder;
  // the box's width says nothing until it is sized by its hint
  const pick = (w) => { if (q.style.getPropertyValue("--hint-w")) q.placeholder = q.offsetWidth >= w ? long : short; };
  // nothing changes in the observer's own turn, a placeholder included:
  // WebKit counts any change there as a loop. The next frame measures,
  // sizes the box and picks; --hint-s keeps the short hint room on a
  // phone-wide row
  let due = 0;
  const fit = new ResizeObserver(() => {
    if (!q.isConnected) return fit.disconnect();
    due ||= requestAnimationFrame(() => {
      due = 0;
      const cs = getComputedStyle(q), c = (window.wholeHint.c ||= document.createElement("canvas").getContext("2d"));
      c.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      const wide = (s) => Math.ceil(c.measureText(s).width + parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight) + parseFloat(cs.borderLeftWidth) * 2) + 2;
      const w = wide(long);
      q.style.setProperty("--hint-w", w + "px");
      q.style.setProperty("--hint-s", wide(short) + "px");
      pick(w);
    });
  });
  fit.observe(q);
};
