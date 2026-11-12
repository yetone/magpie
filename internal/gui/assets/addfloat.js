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
