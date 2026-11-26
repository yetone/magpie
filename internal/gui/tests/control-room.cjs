// Shared by the overlap guard (control-overlap.test.cjs): every control a
// pointer can reach on a page, and where two of them take the same room.
// A control drawn over another, or pulled over it by a negative margin,
// hides part of it and takes its clicks: a Usage card's ⋯ sat over "Show
// the other accounts in brief" (the owner, 11-26), absolutely placed in an
// 18px room the pill's -7px margin had already taken 7px of. A control at
// opacity 0 counts: it is clicked where it stands, and shows on hover.
//
// Returns [{ a, b, w, h }] for each pair whose boxes cross by more than a
// pixel each way, and [{ a, by }] for each whose middle a pointer can't
// reach, something else being drawn there.
function controlClashes(root) {
  const SEL = "button, a[href], input:not([type=hidden]), select, textarea, summary, [role=button], [role=tab], [role=switch], [role=checkbox], [role=radio], [role=menuitem], [contenteditable=true]";
  const name = (e) => {
    const p = [];
    for (let n = e; n && n !== document.body && p.length < 4; n = n.parentElement) {
      let s = n.tagName.toLowerCase();
      if (n.id) { s += "#" + n.id; p.unshift(s); break; }
      const c = [...n.classList].slice(0, 2).join(".");
      if (c) s += "." + c;
      p.unshift(s);
    }
    const words = (e.getAttribute("aria-label") || e.textContent || e.value || "").trim().replace(/\s+/g, " ").slice(0, 24);
    return p.join(" > ") + (words ? ` "${words}"` : "");
  };
  // the part of a box not cut away by a scroller or a clipping box around it
  const seen = (e) => {
    let r = e.getBoundingClientRect();
    let x0 = r.left, y0 = r.top, x1 = r.right, y1 = r.bottom;
    for (let n = e.parentElement; n; n = n.parentElement) {
      const cs = getComputedStyle(n);
      if (cs.overflowX !== "visible" || cs.overflowY !== "visible" || cs.clipPath !== "none") {
        const c = n.getBoundingClientRect();
        x0 = Math.max(x0, c.left); y0 = Math.max(y0, c.top); x1 = Math.min(x1, c.right); y1 = Math.min(y1, c.bottom);
      }
    }
    x0 = Math.max(x0, 0); y0 = Math.max(y0, 0); x1 = Math.min(x1, innerWidth); y1 = Math.min(y1, innerHeight);
    return { r, x0, y0, x1, y1, part: Math.max(0, x1 - x0) * Math.max(0, y1 - y0) / (r.width * r.height) };
  };
  const live = [];
  for (const e of root.querySelectorAll(SEL)) {
    const r = e.getBoundingClientRect();
    if (r.width < 4 || r.height < 4) continue;
    if (e.closest("[inert], [aria-hidden=true]") || e.disabled) continue;
    const cs = getComputedStyle(e);
    if (cs.pointerEvents === "none" || cs.visibility !== "visible") continue;
    let gone = false;
    for (let n = e; n && !gone; n = n.parentElement) if (getComputedStyle(n).display === "none") gone = true;
    if (gone) continue;
    // a control inside another (a field's clear button) is its own part
    const s = seen(e);
    if (s.part < 0.6) continue;
    live.push({ e, ...s });
  }
  const crossed = [], covered = [];
  for (let i = 0; i < live.length; i++) {
    const A = live[i];
    for (let j = i + 1; j < live.length; j++) {
      const B = live[j];
      if (A.e.contains(B.e) || B.e.contains(A.e)) continue;
      const w = Math.min(A.x1, B.x1) - Math.max(A.x0, B.x0), h = Math.min(A.y1, B.y1) - Math.max(A.y0, B.y0);
      if (w > 1 && h > 1) crossed.push({ a: name(A.e), b: name(B.e), w, h });
    }
    // its middle, as a pointer reaches it
    const x = (A.x0 + A.x1) / 2, y = (A.y0 + A.y1) / 2;
    const at = document.elementFromPoint(x, y);
    if (!at || A.e.contains(at)) continue;
    // a label clicks its field; a control inside the one it was drawn for
    if (at.closest("label")?.contains(A.e) || (A.e.labels && [...A.e.labels].some((l) => l.contains(at)))) continue;
    covered.push({ a: name(A.e), by: name(at) });
  }
  return { crossed, covered, count: live.length };
}
module.exports = { controlClashes };
