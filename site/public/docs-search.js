// The docs' search: docs.js loads this on the first open (the search
// button, / or Cmd/Ctrl+K). It reads /search/<lang>.json (search-index.mjs
// builds it from the pages) once, and finds in the browser alone; nothing
// typed leaves the page. find and snippet are plain functions, which
// search.test.mjs runs in Node.
(() => {
  // a word in the docs is matched as typed, any case; Chinese and Japanese
  // have no spaces between words, so a term is a run of text, not a word
  const fold = (s) => s.toLowerCase();
  const terms = (q) => [...new Set(fold(q).split(/\s+/).filter(Boolean))];

  // find returns the sections every term of q is in, the best first: a
  // term in a section's heading counts most, then in its page's title,
  // then in its text; a page gives at most `per` sections.
  function find(index, q, limit = 40, per = 3) {
    const ts = terms(q);
    if (!ts.length) return [];
    const whole = fold(q.trim());
    const hits = [];
    for (const [path, title, sections] of index) {
      const ft = fold(title);
      for (const [id, head, body] of sections) {
        const fh = fold(head), fb = fold(body);
        let score = 0, all = true;
        for (const t of ts) {
          const inHead = fh.includes(t), inTitle = ft.includes(t), inBody = fb.includes(t);
          if (!inHead && !inTitle && !inBody) { all = false; break; }
          score += (inHead ? 8 : 0) + (inTitle ? 5 : 0) + (inBody ? 1 + Math.min(3, count(fb, t) - 1) * 0.25 : 0);
        }
        if (!all) continue;
        if (fh.includes(whole)) score += 6;
        if (ft.includes(whole)) score += id ? 3 : 8; // the page itself, at its top
        if (fh.startsWith(ts[0]) || ft.startsWith(ts[0])) score += 2;
        hits.push({ path, title, id, head, body, score });
      }
    }
    hits.sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
    const perPage = new Map(), out = [];
    for (const h of hits) {
      const n = perPage.get(h.path) || 0;
      if (n >= per) continue;
      perPage.set(h.path, n + 1);
      out.push(h);
      if (out.length >= limit) break;
    }
    return out;
  }
  function count(s, t) {
    let n = 0;
    for (let i = s.indexOf(t); i >= 0 && n < 8; i = s.indexOf(t, i + t.length)) n++;
    return n;
  }

  // snippet is the stretch of text around the first term found, as
  // [before-or-between, match, …] pieces: odd ones are what was typed
  function snippet(text, q, width = 150) {
    const ts = terms(q), ft = fold(text);
    let at = -1;
    for (const t of ts) { const i = ft.indexOf(t); if (i >= 0 && (at < 0 || i < at)) at = i; }
    let from = 0;
    if (at > 40) from = at - 40;
    let s = text.slice(from, from + width);
    if (from > 0) s = "…" + s.replace(/^\S{0,12}\s/, "");
    if (from + width < text.length) s += "…";
    if (!ts.length) return [s];
    const re = new RegExp(ts.sort((a, b) => b.length - a.length).map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"), "gi");
    return s.split(new RegExp("(" + re.source + ")", "gi"));
  }

  const api = { find, snippet };
  if (typeof module === "object" && module.exports) { module.exports = api; return; }
  window.docsSearchFind = api;

  // the dialog
  const lang = (document.documentElement.lang || "en").slice(0, 2);
  const L = {
    en: { search: "Search", ph: "Search the docs", none: "Nothing found for “{q}”", fail: "The search couldn't load. Check your connection and try again." },
    zh: { search: "搜索", ph: "搜索文档", none: "没有找到与“{q}”相关的内容", fail: "搜索没能加载，请检查网络后重试。" },
    ja: { search: "検索", ph: "ドキュメントを検索", none: "「{q}」に一致する内容はありません", fail: "検索を読み込めませんでした。接続を確かめて、もう一度お試しください。" },
  }[lang] || null;
  const words = L || { search: "Search", ph: "Search the docs", none: "Nothing found for “{q}”", fail: "The search couldn't load." };
  const file = "/search/" + (L ? lang : "en") + ".json";

  let index = null, loading = null;
  const load = () => loading || (loading = fetch(file).then((r) => { if (!r.ok) throw new Error(r.status); return r.json(); })
    .then((d) => (index = d), (e) => { loading = null; throw e; }));

  const el = (tag, cls, txt) => { const e = document.createElement(tag); if (cls) e.className = cls; if (txt) e.textContent = txt; return e; };
  const box = el("div", "sx");
  box.hidden = true;
  box.innerHTML = '<div class="sx-scrim"></div><div class="sx-panel" role="dialog" aria-modal="true"><div class="sx-top"><svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="7" cy="7" r="4.75"/><path d="M10.5 10.5 14 14"/></svg><input type="search" autocomplete="off" spellcheck="false" enterkeyhint="go" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="sx-list"><kbd>Esc</kbd></div><div class="sx-list" id="sx-list" role="listbox"></div></div>';
  const panel = box.querySelector(".sx-panel"), input = box.querySelector("input"), list = box.querySelector(".sx-list");
  panel.setAttribute("aria-label", words.search);
  input.placeholder = words.ph;
  input.setAttribute("aria-label", words.ph);
  document.body.append(box);

  let items = [], cur = -1, back = null, gen = 0;
  const pick = (i, keep) => {
    items.forEach((a, j) => a.setAttribute("aria-selected", String(j === i)));
    cur = i;
    if (i >= 0) {
      input.setAttribute("aria-activedescendant", items[i].id);
      // the list scrolls itself, never the page
      if (!keep) {
        const a = items[i], top = a.offsetTop, bottom = top + a.offsetHeight;
        if (top < list.scrollTop) list.scrollTop = top - 6;
        else if (bottom > list.scrollTop + list.clientHeight) list.scrollTop = bottom - list.clientHeight + 6;
      }
    } else input.removeAttribute("aria-activedescendant");
  };
  const draw = () => {
    const q = input.value.trim();
    list.textContent = "";
    items = [];
    cur = -1;
    input.removeAttribute("aria-activedescendant");
    box.classList.toggle("has", !!q);
    if (!q) { input.setAttribute("aria-expanded", "false"); return; }
    if (!index) {
      const g = ++gen;
      load().then(() => { if (g === gen && !box.hidden) draw(); }, () => { if (g === gen) { list.textContent = ""; list.append(el("p", "sx-none", words.fail)); } });
      return;
    }
    const hits = find(index, q);
    input.setAttribute("aria-expanded", String(hits.length > 0));
    if (!hits.length) { list.append(el("p", "sx-none", words.none.replace("{q}", q))); return; }
    hits.forEach((h, i) => {
      const a = el("a", "sx-item");
      a.href = h.path + (h.id ? "#" + h.id : "");
      a.id = "sx-" + i;
      a.setAttribute("role", "option");
      a.tabIndex = -1;
      const where = el("span", "sx-where");
      where.append(el("span", "sx-page", h.title));
      if (h.head) where.append(el("span", "sx-sep", "›"), el("span", "sx-head", h.head));
      a.append(where);
      const sn = el("span", "sx-snip");
      snippet(h.body || h.head || h.title, q).forEach((part, j) => sn.append(j % 2 ? el("mark", "", part) : part));
      a.append(sn);
      a.addEventListener("mousemove", () => { if (cur !== i) pick(i, true); });
      a.addEventListener("click", (e) => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) go(a, e); });
      list.append(a);
      items.push(a);
    });
    list.scrollTop = 0;
    pick(0, true);
  };
  const go = (a, e) => {
    const u = new URL(a.href);
    if (u.pathname === location.pathname.replace(/\.html$/, "")) {
      // this page: jump to the section, the dialog out of the way first
      e && e.preventDefault();
      close(false);
      if (u.hash) location.hash = u.hash; else scrollTo(0, 0);
      return;
    }
    if (!e) location.href = a.href;
  };
  input.addEventListener("input", draw);
  input.addEventListener("keydown", (e) => {
    if (e.isComposing || e.keyCode === 229) return; // an IME choosing its words
    if (e.key === "ArrowDown" || (e.key === "n" && e.ctrlKey)) { e.preventDefault(); if (items.length) pick((cur + 1) % items.length); }
    else if (e.key === "ArrowUp" || (e.key === "p" && e.ctrlKey)) { e.preventDefault(); if (items.length) pick((cur - 1 + items.length) % items.length); }
    else if (e.key === "Enter") { e.preventDefault(); if (items[cur]) go(items[cur]); }
    else if (e.key === "Escape") { e.preventDefault(); close(true); }
  });
  box.querySelector(".sx-scrim").addEventListener("click", () => close(true));
  // Tab stays in the dialog: the field is all there is to focus
  panel.addEventListener("keydown", (e) => { if (e.key === "Tab") { e.preventDefault(); input.focus(); } });

  function open(from) {
    if (!box.hidden) { input.focus(); input.select(); return; }
    back = from || document.activeElement;
    box.hidden = false;
    // a short fade: it opens many times, and comes from nowhere on the page
    requestAnimationFrame(() => box.classList.add("in"));
    input.focus();
    input.select();
    load().catch(() => {});
    if (input.value.trim()) draw();
  }
  function close(refocus) {
    if (box.hidden) return;
    box.hidden = true;
    box.classList.remove("in");
    if (refocus && back && back.focus) back.focus();
  }
  // back to a page left from the dialog: the page, not the dialog
  addEventListener("pageshow", (e) => { if (e.persisted) close(false); });
  window.docsSearch = { open, close, load };
})();
