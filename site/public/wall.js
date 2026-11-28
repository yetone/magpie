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
  const table = document.querySelector("table.cw");
  if (!box || !table) return;
  const input = box.querySelector("input");
  const count = box.querySelector(".cw-n");
  const wrap = table.closest(".table-wrap");
  const rows = Array.from(table.tBodies[0].rows, (el) => {
    const b = el.cells[0] && el.cells[0].querySelector("b");
    return { el, keys: rowKeys(b ? b.textContent : "", el.dataset.k) };
  });
  let lastRow = null;
  function run() {
    const q = input.value;
    let n = 0, last = null;
    for (const r of rows) {
      const on = matches(r.keys, q);
      if (r.el.hidden === on) r.el.hidden = !on;
      if (on) { n++; last = r.el; }
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
      if (el.parentNode === table.tBodies[0] && el.hidden) { input.value = ""; run(); el.scrollIntoView(); }
      return;
    }
    const want = norm(h);
    const hit = rows.find((r) => r.keys.includes(want)) || rows.find((r) => r.el.id === want);
    if (hit) location.replace("#" + encodeURIComponent(hit.el.id));
  }
  addEventListener("hashchange", land);
  land();
})();
