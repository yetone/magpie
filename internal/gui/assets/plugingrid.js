// Discover's long lists (all 369 subscriptions, a search that matches
// hundreds): only the rows near the view are drawn, as the page was slow
// with all of them. The grid keeps the whole list's height in its padding
// above and below what it shows, and the rows follow the scroll; a card
// with focus is kept. plugins.js imports this when its page is first drawn,
// so it weighs nothing on the core; a list shorter than VMIN is drawn
// whole there. body() is Discover's body, and draw() draws it again.
window.pmVirtualGrids = function (page, card, body, draw) {
  const VMIN = 48; // the shortest list drawn in part
  const VMORE = 700; // px drawn past each edge of the view
  // a long list drawn whole before this came is drawn again, once the
  // caller holds what this returns
  queueMicrotask(() => { if (body()?.querySelector(".pm-grid:not(.pm-virtual) > .pm-card:nth-child(" + VMIN + ")")) draw(); });
  const vgrids = new Set();
  let vrowH = 172, vcols = 3; // a row's height and the columns, as last measured
  function grid(g, list) {
    g.classList.add("pm-virtual");
    const v = { g, list, cols: 0, h: [], seen: new Set(), made: new Map() };
    vgrids.add(v);
    vlayout(v);
    return g;
  }
  function vlayout(v) {
    const { g, list } = v;
    // laid out: in the page, and the page shown
    const on = g.isConnected && g.getClientRects().length > 0;
    const cs = on ? getComputedStyle(g) : null;
    const cols = on ? Math.max(1, cs.gridTemplateColumns.split(" ").filter(Boolean).length) : vcols;
    const gap = (cs && parseFloat(cs.rowGap)) || 10;
    const rows = Math.ceil(list.length / cols);
    if (cols !== v.cols) {
      v.cols = cols;
      v.h = new Array(rows).fill(vrowH);
      v.seen.clear();
      if (on) vcols = cols;
    }
    const above = (first) => { let y = 0; for (let r = 0; r < first; r++) y += v.h[r] + gap; return y; };
    const below = (last) => { let y = 0; for (let r = last + 1; r < rows; r++) y += v.h[r] + gap; return y; };
    for (let pass = 0; pass < 2; pass++) {
      // the view, from the grid's top
      let top = -VMORE, bottom = innerHeight + VMORE;
      if (on) {
        const gt = g.getBoundingClientRect().top, pr = page.getBoundingClientRect();
        top = pr.top - gt - VMORE;
        bottom = pr.bottom - gt + VMORE;
      }
      let first = -1, last = rows - 1, y = 0;
      for (let r = 0; r < rows; r++) {
        if (first < 0 && y + v.h[r] >= top) first = r;
        if (y > bottom) { last = r - 1; break; }
        y += v.h[r] + gap;
      }
      if (first < 0) first = rows - 1;
      last = Math.max(first, last);
      const nodes = [];
      for (let i = first * cols; i < Math.min(list.length, (last + 1) * cols); i++) {
        let c = v.made.get(i);
        if (!c) {
          c = card(list[i]);
          c.dataset.i = i;
          v.made.set(i, c);
        }
        c.classList.remove("pm-held");
        c.style.cssText = "";
        nodes.push(c);
      }
      // the card with focus stays in the page, where it would be, out of
      // the rows drawn (keys go on from it as before)
      const f = document.activeElement?.closest?.(".pm-card");
      if (on && f && f.parentNode === g && !nodes.includes(f)) {
        const i = +f.dataset.i, fr = Math.floor(i / cols), col = i % cols;
        const ws = cs.gridTemplateColumns.split(" ").map(parseFloat), cg = parseFloat(cs.columnGap) || gap;
        let left = parseFloat(cs.paddingLeft) || 0;
        for (let k = 0; k < col; k++) left += ws[k] + cg;
        f.classList.add("pm-held");
        f.style.cssText = `position:absolute;top:${above(fr)}px;left:${left}px;width:${ws[col]}px;height:${v.h[fr]}px`;
        nodes.push(f);
      }
      replaceKeeping(g, nodes);
      g.style.paddingTop = above(first) + "px";
      g.style.paddingBottom = below(last) + "px";
      if (!on) return;
      // the rows as drawn: a row is as tall as its tallest card
      let moved = false;
      for (let r = first; r <= last; r++) {
        const c = nodes[(r - first) * cols];
        const h = c && !c.classList.contains("pm-held") ? c.offsetHeight : 0;
        if (!h) continue;
        if (!v.seen.size && Math.abs(vrowH - h) > 0.5) {
          // the first measure: every row not drawn yet is taken to be as tall
          for (let k = 0; k < rows; k++) v.h[k] = h;
          vrowH = h;
          moved = true;
        }
        v.seen.add(r);
        if (Math.abs(v.h[r] - h) > 0.5) { v.h[r] = h; moved = true; }
      }
      if (!moved) return;
      g.style.paddingTop = above(first) + "px";
      g.style.paddingBottom = below(last) + "px";
    }
  }
  // every long grid in the page follows the scroll and the width
  let vframe = 0;
  function vnow() {
    cancelAnimationFrame(vframe);
    vframe = 0;
    for (const v of vgrids) v.g.isConnected ? vlayout(v) : vgrids.delete(v);
  }
  const vsoon = () => { if (vgrids.size && !vframe) vframe = requestAnimationFrame(vnow); };
  page.addEventListener("scroll", vsoon, { passive: true });
  if (window.ResizeObserver) new ResizeObserver(vsoon).observe(page);
  return { grid, now: vnow, min: VMIN };
};
