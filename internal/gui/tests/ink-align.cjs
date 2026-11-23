// Shared by the icon-alignment guard (icon-text-align.test.cjs): every small
// glyph that sits beside a line of words on a page, and where its ink sits
// in its own box. A glyph's ink is its shapes' geometry (getBBox) grown by
// half its stroke, through the whole matrix (a turned chevron is turned
// ink). The layout centres a glyph's box on its words' line, the same for
// every glyph, so a glyph drawn off its box's middle is the one that sits
// off its neighbours: Discover's person beside a count sat 1.5 device
// pixels below the star beside the next one.
//
// A glyph moved off the line by hand (a translate, a relative top) is
// reported too, as shift in CSS px: such a nudge is fitted to one font and
// one reference, and .pm-dl's .1em, fitted to words with descenders, sat
// the person 1.5 device px below its count in the real app (11-23).
//
// Returns [{ where, words, d, units, shift, path }]: d in CSS px, the ink's
// middle less the box's middle (positive: low); units, the same in a
// 16-unit box; shift, how far the glyph or a box holding only it is moved
// down from where the layout put it.
function inkPairs(root) {
  const out = [];
  const name = (e) => {
    const p = [];
    for (let n = e; n && n !== document.body && p.length < 4; n = n.parentElement) {
      let s = n.tagName.toLowerCase();
      if (n.id) { s += "#" + n.id; p.unshift(s); break; }
      const c = [...n.classList].slice(0, 2).join(".");
      if (c) s += "." + c;
      p.unshift(s);
    }
    return p.join(" > ");
  };
  const shown = (e) => { const r = e.getBoundingClientRect(); if (!r.width || !r.height) return false; for (let n = e; n; n = n.parentElement) { const cs = getComputedStyle(n); if (cs.visibility === "hidden" || cs.display === "none" || +cs.opacity === 0) return false; } return true; };
  // the first run of words in a node, as a text node with something in it
  const firstText = (n) => {
    if (n.nodeType === 3) return n.data.trim() ? n : null;
    if (n.nodeType !== 1 || n.matches("svg, img, input, textarea, select, .ic, [aria-hidden=true]")) return null;
    for (const c of n.childNodes) { const t = firstText(c); if (t) return t; }
    return null;
  };
  for (const s of root.querySelectorAll("svg")) {
    if (s.parentElement?.closest("svg")) continue;
    const r = s.getBoundingClientRect();
    if (r.width < 6 || r.height < 6 || r.height > 22 || !shown(s)) continue;
    if (s.closest("[data-ink-own]")) continue;
    // the glyph's holder: the svg, or a box that holds nothing else
    let hold = s;
    while (hold.parentElement && hold.parentElement.childNodes.length === 1 && hold.parentElement !== root) hold = hold.parentElement;
    // the words beside it: the next node with words, else the one before
    const near = (dir) => { for (let n = hold[dir]; n; n = n[dir]) { if (n.nodeType === 3 && !n.data.trim()) continue; return firstText(n) ? n : null; } return null; };
    const sib = near("nextSibling") || near("previousSibling");
    if (!sib) continue;
    const tn = firstText(sib);
    const rg = document.createRange();
    rg.selectNodeContents(tn);
    const rects = [...rg.getClientRects()].filter((x) => x.width > 0);
    if (!rects.length) continue;
    const line = rects[0];
    // beside it on one line: overlapping it, and near
    if (rects.some((x) => Math.abs(x.top - line.top) > 2)) continue;
    const gap = line.left > r.right ? line.left - r.right : r.left - (rects[rects.length - 1].right);
    if (gap < -1 || gap > 14) continue;
    if (line.bottom < r.top || line.top > r.bottom) continue;
    let bb;
    try { bb = s.getBBox(); } catch { continue; }
    if (!bb.height && !bb.width) continue;
    let sw = 0;
    for (const p of s.querySelectorAll("path, circle, rect, line, polyline, polygon, ellipse")) {
      const cs = getComputedStyle(p);
      if (cs.stroke && cs.stroke !== "none") sw = Math.max(sw, parseFloat(cs.strokeWidth) || 0);
    }
    const ctm = s.getScreenCTM();
    if (!ctm) continue;
    // the corners through the whole matrix: a turned chevron is turned ink
    const ys = [];
    for (const x of [bb.x - sw / 2, bb.x + bb.width + sw / 2]) for (const y of [bb.y - sw / 2, bb.y + bb.height + sw / 2]) ys.push(ctm.b * x + ctm.d * y + ctm.f);
    const icon = (Math.min(...ys) + Math.max(...ys)) / 2, d = icon - (r.top + r.height / 2);
    let shift = 0;
    for (let n = s; n && !n.contains(sib); n = n.parentElement) {
      const cs = getComputedStyle(n);
      if (cs.transform && cs.transform !== "none") shift += new DOMMatrixReadOnly(cs.transform).m42;
      if (cs.translate && cs.translate !== "none") shift += parseFloat(cs.translate.split(" ")[1] || "0") || 0;
      if (cs.position === "relative") shift += (parseFloat(cs.top) || 0) - (cs.top === "auto" ? parseFloat(cs.bottom) || 0 : 0);
    }
    out.push({ where: name(s), words: tn.data.trim().slice(0, 24), d, units: d / r.height * 16, shift, path: (s.querySelector("path")?.getAttribute("d") || s.firstElementChild?.tagName || "").slice(0, 40) });
  }
  return out;
}
module.exports = { inkPairs };
