// formRows: every label of a form grid that sits beside its control, with
// how far the middle of its first line is from the middle of the
// control's first line. The first line of the control is its first
// visible field, button, switch row, tick or line of text, in order.
async function formRows(page, root = ".editor") {
  return page.evaluate((root) => {
    const shown = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== "hidden"; };
    const lineOf = (n) => { const r = document.createRange(); r.selectNodeContents(n); return [...r.getClientRects()].find((x) => x.width > 0 && x.height > 0); };
    const LEAF = "input:not([type=hidden]), textarea, select, button, [role=switch], label.tick, .sess-pick, .icon-now";
    const first = (box) => {
      const w = document.createTreeWalker(box, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
      for (let n = w.currentNode; n; n = w.nextNode()) {
        if (n.nodeType === 3) { if (n.data.trim() && n.parentElement && shown(n.parentElement)) { const r = lineOf(n); if (r) return { r, what: "text " + n.data.trim().slice(0, 24) }; } continue; }
        if (n !== box && n.matches(LEAF) && shown(n)) {
          let r = n.getBoundingClientRect();
          // a box of several lines: its first line, inside its border and padding
          if (n.matches("textarea")) {
            const s = getComputedStyle(n), lh = parseFloat(s.lineHeight) || parseFloat(s.fontSize) * 1.2;
            const top = r.top + parseFloat(s.borderTopWidth) + parseFloat(s.paddingTop);
            r = { top, height: lh };
          }
          return { r, what: n.tagName.toLowerCase() + "." + [...n.classList].join(".") };
        }
      }
      return null;
    };
    const out = [];
    for (const ed of document.querySelectorAll(root)) {
      for (const l of ed.querySelectorAll("label")) {
        const p = l.parentElement, c = l.nextElementSibling;
        if (!p || getComputedStyle(p).display !== "grid" || !c || l.matches(".tick") || !shown(l) || !shown(c)) continue;
        const lr = l.getBoundingClientRect(), cr = c.getBoundingClientRect();
        if (lr.right > cr.left + 1) continue; // stacked (a phone's one column)
        const t = lineOf(l), f = first(c);
        if (!t || !f) continue;
        const mid = (r) => r.top + r.height / 2;
        out.push({ label: l.textContent.trim().slice(0, 30), what: f.what, off: Math.round((mid(t) - mid(f.r)) * 10) / 10 });
      }
    }
    return out;
  }, root);
}
module.exports = { formRows };
