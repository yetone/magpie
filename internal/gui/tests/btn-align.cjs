// Shared by the button guard (button-text-center.test.cjs): every control on
// a page that shows one line of words in a box of its own, and where that
// line sits in the box. A line is laid out by its font's ascent and descent
// (the text's own rects), and a control centres it only while the line fits
// its content box: a fixed height with padding that leaves the content box
// shorter than the line puts the line at the top of it, and the words sit
// low (the Usage speed table's 检查…, 11-26: .text's 3px padding won over
// .led-spd-check's 0 inside its 20px).
//
// Returns [{ where, words, d, over, h, line }]: d in CSS px, the line's
// middle less the box's middle inside its borders, less the same for the
// words centred in a box of that height (positive: low); over,
// how far the line passes the content box (padding taken off), 0 when it
// fits; h, the box's height; line, the line's height.
function buttonLines(root) {
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
  const shown = (e) => {
    const r = e.getBoundingClientRect();
    if (!r.width || !r.height) return false;
    for (let n = e; n; n = n.parentElement) {
      const cs = getComputedStyle(n);
      if (cs.visibility === "hidden" || cs.display === "none" || +cs.opacity === 0) return false;
    }
    return true;
  };
  for (const b of root.querySelectorAll("button, [role=button], [role=tab], a.text, .opt")) {
    if (!shown(b) || b.closest("[data-ink-own]")) continue;
    const cs = getComputedStyle(b);
    // a control the size of one, not a page-sized area
    const r = b.getBoundingClientRect();
    if (r.height > 44 || r.height < 12) continue;
    // its words: text nodes not inside a glyph, a field or a nested control
    const texts = [];
    const walk = document.createTreeWalker(b, NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      if (!n.data.trim()) continue;
      const pe = n.parentElement;
      if (pe.closest("svg, input, textarea, select, [aria-hidden=true]")) continue;
      if (pe.closest("button, [role=button], [role=tab], a.text, .opt") !== b) continue;
      texts.push(n);
    }
    if (!texts.length) continue;
    const rects = [];
    for (const n of texts) {
      const rg = document.createRange();
      rg.selectNodeContents(n);
      for (const x of rg.getClientRects()) if (x.width > 0 && x.height > 0) rects.push(x);
    }
    if (!rects.length) continue;
    // one line only: a wrapped label is laid out on purpose
    const top = Math.min(...rects.map((x) => x.top)), bottom = Math.max(...rects.map((x) => x.bottom));
    if (rects.some((x) => x.top > rects[0].top + rects[0].height / 2 || x.bottom < rects[0].bottom - rects[0].height / 2)) continue;
    // words cut by the box's own overflow (a scroller, an ellipsis box) are
    // not where its line is
    if (top < r.top - 1 && bottom > r.bottom + 1) continue;
    const bt = parseFloat(cs.borderTopWidth) || 0, bb = parseFloat(cs.borderBottomWidth) || 0;
    const pt = parseFloat(cs.paddingTop) || 0, pb = parseFloat(cs.paddingBottom) || 0;
    // a line inside a child box (an inline-flex label, a span with its own
    // padding) is measured against the control: its middle is what shows
    const inTop = r.top + bt, inBottom = r.bottom - bb;
    // where the engine puts the same words when they are centred: a box of
    // the same inner height, its one line in the middle, in the same font.
    // A font's ascent and descent are seldom even, so a line's text rect
    // sits off its middle by a font's own amount (0.9px high for 12px
    // system-ui); only what the control adds to that is its fault.
    const fe = texts[0].parentElement, fs = getComputedStyle(fe);
    const probe = document.createElement("div");
    probe.style.cssText = `position:fixed;left:0;top:0;visibility:hidden;display:flex;align-items:center;padding:0;border:0;height:${inBottom - inTop}px;font:${fs.fontStyle} ${fs.fontWeight} ${fs.fontSize}/${fs.lineHeight} ${fs.fontFamily};letter-spacing:${fs.letterSpacing};white-space:nowrap`;
    const ps = document.createElement("span");
    ps.textContent = texts.map((n) => n.data).join("");
    probe.append(ps);
    document.body.append(probe);
    const pr = document.createRange();
    pr.selectNodeContents(ps.firstChild);
    const pb2 = probe.getBoundingClientRect(), prs = [...pr.getClientRects()];
    const ref = (Math.min(...prs.map((x) => x.top)) + Math.max(...prs.map((x) => x.bottom))) / 2 - (pb2.top + pb2.bottom) / 2;
    probe.remove();
    const d = (top + bottom) / 2 - (inTop + inBottom) / 2 - ref;
    const over = Math.max(0, (inTop + pt) - top, bottom - (inBottom - pb));
    out.push({ where: name(b), words: texts.map((n) => n.data.trim()).join(" ").slice(0, 24), d, over, h: r.height, line: bottom - top });
  }
  return out;
}
module.exports = { buttonLines };
