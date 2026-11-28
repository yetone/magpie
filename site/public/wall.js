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
  // Each part is drawn once soon after the page loads, a few at a time, so
  // the size it keeps off screen is its own, not the guess. One drawn at
  // its own size only when scrolled to would move the rows being read
  // (WebKit doesn't anchor the scroll for it); one above the screen
  // drawn here is scrolled by what it changed, in the same task.
  let pass = 0, width = 0;
  function measure() {
    const k = ++pass;
    delete wrap.dataset.drawn;
    // nearest the screen first: what is read next
    const order = parts.map((p) => {
      const r = p.el.getBoundingClientRect();
      return { p, far: r.bottom < 0 ? -r.bottom : r.top > innerHeight ? r.top - innerHeight : 0 };
    }).sort((a, b) => a.far - b.far).map((o) => o.p);
    let i = 0;
    const step = () => {
      if (k !== pass) return;
      const t = performance.now();
      const top = parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
      while (i < order.length && performance.now() - t < 8) {
        const p = order[i++];
        if (p.el.hidden || p.cut) continue;
        // what is on screen below a part above it is held where it was,
        // by how far it moved: a browser that anchors moved it back itself
        const above = p.el.getBoundingClientRect().bottom <= top;
        const next = above && p.el.nextElementSibling;
        const y = next && next.getBoundingClientRect().top;
        hold(p);
        if (next) { const d = next.getBoundingClientRect().top - y; if (Math.abs(d) >= .5) scrollBy(0, d); }
      }
      if (i < order.length) setTimeout(step, 40); else wrap.dataset.drawn = "";
    };
    step();
  }
  const start = () => setTimeout(measure, 400);
  if (document.readyState === "complete") start(); else addEventListener("load", start);
  // a new width wraps the rows anew
  if (window.ResizeObserver) {
    let soon = 0;
    new ResizeObserver(() => {
      const w = wrap.clientWidth;
      if (!w || w === width) return;
      const first = !width;
      width = w;
      if (!first) { clearTimeout(soon); soon = setTimeout(measure, 200); }
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
