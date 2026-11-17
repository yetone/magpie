// The sidebar lights the row under the pointer as a torch would, in its
// pages (Providers, Routing…, Settings), their sub-lists and the
// gateway's address at its foot alike:
// brightest where it points, the rest of that row a step up from the
// others, which keep their own ink (app.css, span.lit). The light
// follows the pointer with a little lag and fades out as it moves on; with
// reduced motion it is where the pointer is at once. Imported by app.js
// when the pointer first comes over the sidebar (layoutNav), not at start,
// where it held up the open page's row; a touch never lights a row.
// A row's icon takes the same light as its word (app.css, .lit icons): it
// is as bright as the word's ink would be at the icon's middle, so it
// lights fully under the pointer and dims as the pointer goes along the
// word. The gateway's status dot keeps its colour.
document.documentElement.classList.add("navlamp");
const lit = new Map(); // a row's word -> how lit it is, 0 to 1
let at = null, tx = 0, x = 0, raf = 0;
// a row's icon: a sub-row's, a page's, Settings' gear
const ICON = ":scope > :is(svg, .nav-ic, .nav-s-ic)";
// how much of the torch's heart is in the word's ink at a distance from the
// pointer, as a share of the light's 36px reach: the gradient's stops in
// app.css (all of it to 12%, 45% of it at 45%, none at the edge)
const heat = (d) => d <= 0.12 ? 1 : d <= 0.45 ? 1 - (d - 0.12) / 0.33 * 0.55 : Math.max(0, 0.45 * (1 - (d - 0.45) / 0.55));

function frame() {
  raf = 0;
  const still = calm(), row = at && at.isConnected && !at.parentElement.matches(".cur, .on") ? at : null;
  if (row && !lit.has(row)) lit.set(row, 0);
  x = still ? tx : x + (tx - x) * 0.22;
  let busy = Math.abs(tx - x) > 0.3;
  for (const [s, v] of lit) {
    const to = s === row ? 1 : 0, now = still ? to : v + (to - v) * (to > v ? 0.22 : 0.1);
    const ic = s.parentElement && s.parentElement.querySelector(ICON);
    if (!to && (now < 0.01 || !s.isConnected)) {
      lit.delete(s);
      for (const e of [s, ic]) if (e) {
        e.classList.remove("lit");
        e.style.removeProperty("--si");
        e.style.removeProperty("--lx");
        e.style.removeProperty("--iw");
      }
      continue;
    }
    if (Math.abs(to - now) > 0.002) busy = true;
    lit.set(s, now);
    s.classList.add("lit");
    s.style.setProperty("--si", now.toFixed(3));
    s.style.setProperty("--lx", (x - s.getBoundingClientRect().left).toFixed(1) + "px");
    if (ic) {
      const b = ic.getBoundingClientRect();
      ic.classList.add("lit");
      ic.style.setProperty("--si", now.toFixed(3));
      ic.style.setProperty("--iw", heat(Math.abs(x - (b.left + b.width / 2)) / 36).toFixed(3));
    }
  }
  if (busy) raf = requestAnimationFrame(frame);
}
const go = () => { if (!raf) raf = requestAnimationFrame(frame); };

document.addEventListener("pointermove", (e) => {
  const row = e.pointerType === "touch" || !e.target.closest ? null : e.target.closest(".nav-sub > .nav-s, body.side-nav :is(#nav > button[data-view], #prefs, #sideGw)");
  const word = row && row.querySelector(":scope > span");
  if (word && !at) x = e.clientX;
  tx = e.clientX;
  if (word !== at) at = word;
  if (at || lit.size) go();
}, { passive: true });
document.documentElement.addEventListener("pointerleave", () => { at = null; go(); });
