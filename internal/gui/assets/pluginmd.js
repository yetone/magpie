// A plugin's README on its page (plugins.js's detail), loaded when the
// window has started (plugins.js imports it) rather than with the core;
// the Library's skill files read through it too (window.mdRender).
// markdown: a README as text, links and code — no HTML of its own, no
// pictures (a README's badges and screenshots come from anywhere)
window.pluginMarkdown = function markdown(src) {
  const root = el("div", "pm-md");
  const lines = src.replace(/\r\n?/g, "\n").replace(/<!--[\s\S]*?-->|<(script|style)\b[\s\S]*?<\/\1\s*>/gi, "")
    .replace(/\[\s*!\[[^\]]*\]\([^)]*\)\s*\]\([^)]*\)/g, "") // a badge: a picture in a link
    .split("\n");
  // a link: to the web, opened outside; to a heading of the README
  // ("#-quick-start"), there, as the reader asked
  const link = (href, text, into) => {
    const a = el("a", "", "");
    a.href = href;
    inline(text.replace(/!\[[^\]]*\]\([^)]*\)/g, "").trim() || href, a);
    a.onclick = (ev) => {
      ev.preventDefault();
      if (href[0] !== "#") return api("open", { url: href }).catch(() => {});
      const to = root.querySelector(`[data-slug="${CSS.escape(decodeURIComponent(href.slice(1)).toLowerCase())}"]`);
      if (to && window.scrollOnPurpose?.(ev)) to.scrollIntoView({ block: "start", behavior: "smooth" });
    };
    into.append(a);
  };
  const inline = (text, into) => {
    const re = /(`[^`]+`)|\*\*(.+?)\*\*|__(.+?)__|\*([^*\s][^*]*)\*|!\[[^\]]*\]\([^)]*\)|\[([^\]]+)\]\(([^)\s]+)[^)]*\)|<(https?:\/\/[^>\s]+)>|<a\s[^>]*?href\s*=\s*["']([^"']+)["'][^>]*>([\s\S]*?)<\/a\s*>|<\/?[a-zA-Z][^>]*>/g;
    let at = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > at) into.append(text.slice(at, m.index));
      if (m[1]) into.append(el("code", "", m[1].slice(1, -1)));
      else if (m[2] || m[3]) { const b = el("strong"); inline(m[2] || m[3], b); into.append(b); }
      else if (m[4]) { const e = el("em"); inline(m[4], e); into.append(e); }
      else if (m[5] || m[8]) {
        const href = m[6] || m[8], text = m[5] || m[9].replace(/<[^>]*>/g, "");
        if (/^https?:\/\//.test(href) || /^#./.test(href)) link(href, text, into);
        else inline(text, into);
      } else if (m[7]) link(m[7], m[7], into);
      at = re.lastIndex;
    }
    if (at < text.length) into.append(text.slice(at));
  };
  // GitHub's anchor for a heading: lower case, its punctuation and
  // emoji gone, spaces as dashes ("💡 Philosophy" is "-philosophy")
  const slug = (s) => s.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/\s/g, "-");
  let i = 0, list = null, para = null;
  const flush = () => { list = null; para = null; };
  while (i < lines.length) {
    const line = lines[i];
    const fence = line.match(/^\s*(```|~~~)\s*([\w+-]*)/);
    if (fence) {
      flush();
      const code = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith(fence[1])) code.push(lines[i++]);
      i++;
      const pre = el("pre", "", "");
      pre.append(el("code", "", code.join("\n")));
      root.append(pre);
      continue;
    }
    const h = line.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/);
    if (h) {
      flush();
      const e = el("h" + Math.min(6, h[1].length + 2));
      inline(h[2], e);
      e.dataset.slug = slug(e.textContent);
      if (e.textContent.trim()) root.append(e);
      i++;
      continue;
    }
    if (/^\s*\|.*\|\s*$/.test(line) && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1] || "")) {
      flush();
      const cells = (s) => s.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
      const wrap = el("div", "pm-table");
      const table = el("table");
      const tr = el("tr");
      for (const c of cells(line)) { const th = el("th"); inline(c, th); tr.append(th); }
      table.append(tr);
      i += 2;
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) {
        const r = el("tr");
        for (const c of cells(lines[i])) { const td = el("td"); inline(c, td); r.append(td); }
        table.append(r);
        i++;
      }
      wrap.append(table);
      root.append(wrap);
      continue;
    }
    const li = line.match(/^\s*([-*+]|\d+[.)])\s+(.*)$/);
    if (li) {
      para = null;
      const ordered = /\d/.test(li[1]);
      if (!list || list.tagName !== (ordered ? "OL" : "UL")) { list = el(ordered ? "ol" : "ul"); root.append(list); }
      const item = el("li");
      inline(li[2].replace(/^\[( |x)\]\s*/i, (s, x) => x.trim() ? "✓ " : "☐ "), item);
      list.append(item);
      i++;
      continue;
    }
    const q = line.match(/^\s*>\s?(.*)$/);
    if (q) {
      flush();
      const bq = el("blockquote");
      const parts = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) parts.push(lines[i++].replace(/^\s*>\s?/, ""));
      inline(parts.join(" ").replace(/^\[!(\w+)\]\s*/, (s, k) => k.toUpperCase() + ": "), bq);
      root.append(bq);
      continue;
    }
    if (/^\s*([-*_])\s*\1\s*\1[\s\1]*$/.test(line)) { flush(); root.append(el("hr")); i++; continue; }
    if (!line.trim()) { flush(); i++; continue; }
    if (list && /^\s{2,}\S/.test(line)) { const last = list.lastElementChild; last.append(" "); inline(line.trim(), last); i++; continue; }
    if (!para) { para = el("p"); root.append(para); list = null; } else para.append(" ");
    inline(line.trim(), para);
    if (!para.textContent.trim() && !para.children.length) { para.remove(); para = null; }
    i++;
  }
  for (const p of root.querySelectorAll("p")) if (!p.textContent.trim()) p.remove();
  return root;
};
