// The contributors page's finder. credits.mjs renderWall puts the field,
// hidden, above the list, and gives each row an id and in data-k what else
// finds it (GitHub login, X account); this shows the field and keeps only
// the rows whose name, login or X account holds what is typed, in any case,
// a leading @ left off. No script: the field stays hidden, the list whole.
//
// A link to a row (#lijiho96940561) lands on it; one written in another
// case or to an X account of a row filed under its GitHub login is sent to
// that row's own id.
(function () {
  function norm(s) {
    return String(s == null ? "" : s).normalize("NFKC").trim().replace(/^@+/, "").trim().toLowerCase();
  }
  // keys are what a row is found by, normalised; q as typed
  function matches(keys, q) {
    q = norm(q);
    if (!q) return true;
    for (const k of keys) if (k.includes(q)) return true;
    return false;
  }
  function rowKeys(name, also) {
    return [norm(name)].concat(String(also || "").split(/\s+/).filter(Boolean).map(norm));
  }
  if (typeof module === "object" && module.exports) module.exports = { norm, matches, rowKeys };
  if (typeof document === "undefined") return;

  const box = document.querySelector(".cw-find");
  const wrap = document.querySelector(".cw-wrap");
  if (!box || !wrap) return;
  const input = box.querySelector("input");
  const count = box.querySelector(".cw-n");
  // the list comes in parts (credits.mjs renderWall), each its own table
  // under content-visibility: auto (docs.css), so a part off screen keeps
  // the size it had when last drawn. A part the finder cuts down is drawn
  // always (.cw-drawn) while cut, and for a frame after it is whole
  // again, so the size it keeps is its whole one.
  const parts = Array.from(wrap.querySelectorAll(".cw-part"), (el) => ({ el, n: 0, all: 0, cut: false, held: 0 }));
  const rows = [];
  for (const part of parts) for (const el of part.el.querySelector("table").tBodies[0].rows) {
    const b = el.cells[0] && el.cells[0].querySelector("b");
    rows.push({ el, part, keys: rowKeys(b ? b.textContent : "", el.dataset.k) });
    part.all++;
  }
  function hold(p) {
    p.el.classList.add("cw-drawn");
    const k = ++p.held;
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (p.held === k && !p.cut) p.el.classList.remove("cw-drawn");
    }));
  }
  // A part never drawn has the size credits.mjs guessed for it. Below the
  // screen that moves nothing; above it, the page jumps when the part is
  // first drawn as one scrolls back up (WebKit doesn't anchor it). So a
  // part above the screen that hasn't been drawn at this width, which only
  // a jump past it leaves (a link to a row, the finder, the scroll bar),
  // is drawn once, nearest first, a few at a time, holding what is on
  // screen in place. Read in order from the top, nothing is left to do.
  let width = 0, soon = 0, busy = false;
  const top = () => parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
  const io = window.IntersectionObserver && new IntersectionObserver((es) => {
    for (const e of es) { const p = e.target._part; p.on = e.isIntersecting; if (p.on) p.w = width; }
  });
  for (const p of parts) { p.el._part = p; p.w = 0; p.on = false; if (io) io.observe(p.el); }
  function measure() {
    if (busy) return;
    const y0 = top();
    const todo = parts.filter((p) => p.w !== width && !p.el.hidden && !p.cut && p.el.getBoundingClientRect().bottom <= y0).reverse();
    if (!todo.length) return;
    busy = true;
    let i = 0;
    const step = () => {
      const t = performance.now(), y = top();
      while (i < todo.length && performance.now() - t < 6) {
        const p = todo[i++];
        if (p.w === width || p.el.hidden || p.cut) continue;
        const next = p.el.nextElementSibling;
        const at = next && next.getBoundingClientRect().top;
        hold(p);
        p.w = width;
        // a browser that anchors has moved it back itself
        if (next && p.el.getBoundingClientRect().bottom <= y) { const d = next.getBoundingClientRect().top - at; if (Math.abs(d) >= .5) scrollBy(0, d); }
      }
      if (i < todo.length) setTimeout(step, 50);
      else { busy = false; later(); }
    };
    step();
  }
  // once scrolling stops: a part a jump went past
  const later = () => { clearTimeout(soon); soon = setTimeout(measure, 250); };
  addEventListener("scroll", later, { passive: true });
  if (document.readyState === "complete") later(); else addEventListener("load", later);
  // a new width wraps every row anew: only what is on screen is drawn at it
  if (window.ResizeObserver) {
    new ResizeObserver(() => {
      const w = wrap.clientWidth;
      if (!w || w === width) return;
      width = w;
      for (const p of parts) if (p.on) p.w = w;
      later();
    }).observe(wrap);
  }
  let lastRow = null;
  function run() {
    const q = input.value;
    let n = 0, last = null;
    for (const p of parts) p.n = 0;
    for (const r of rows) {
      const on = matches(r.keys, q);
      if (r.el.hidden === on) r.el.hidden = !on;
      if (on) { n++; last = r.el; r.part.n++; }
    }
    for (const p of parts) {
      if (p.el.hidden === p.n > 0) p.el.hidden = !p.n;
      const cut = p.n > 0 && p.n < p.all;
      if (cut) { p.cut = true; p.held++; p.el.classList.add("cw-drawn"); }
      else if (p.cut) { p.cut = false; hold(p); }
    }
    // the last row shown has no rule under it, as the table's last row
    if (lastRow !== last) {
      if (lastRow) lastRow.classList.remove("cw-last");
      if (last) last.classList.add("cw-last");
      lastRow = last;
    }
    wrap.hidden = n === 0;
    count.textContent = norm(q) ? (n === 1 ? box.dataset.one : box.dataset.many.replace("{n}", String(n))) : "";
  }
  input.addEventListener("input", run);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && input.value) { e.preventDefault(); input.value = ""; run(); }
  });
  box.hidden = false;
  // a field the browser filled back in after a reload
  if (input.value) run();

  function land() {
    let h;
    try { h = decodeURIComponent(location.hash.slice(1)); } catch (e) { return; }
    if (!h) return;
    const el = document.getElementById(h);
    if (el) {
      if (el.closest(".cw-wrap") === wrap && el.hidden) {
        input.value = ""; run();
        // a part not drawn has its rows where they were when it last was
        hold(parts.find((p) => p.el.contains(el)));
        el.scrollIntoView();
      }
      return;
    }
    const want = norm(h);
    const hit = rows.find((r) => r.keys.includes(want)) || rows.find((r) => r.el.id === want);
    if (hit) location.replace("#" + encodeURIComponent(hit.el.id));
  }
  addEventListener("hashchange", land);
  land();
})();
