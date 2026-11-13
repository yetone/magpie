// Installed narrowed as Discover is (WJaunita45604 on X): the same strip of
// kinds and the same Origin menu, over what is installed. Each plugin is
// put where Discover puts it: its card's kind and origin (kindOf, srcOf),
// and for one the market doesn't list, the same read from what it is: its
// chips' kind, a git one on GitHub, a @magpie-community package the
// community's, else another author's. plugins.js imports this when its
// page is first drawn, so it weighs nothing on the core; until then the
// list is drawn whole. st holds the picks; the strip and the menu show
// only where there are two or more to pick from.
window.pmInstalled = function ({ es, st, everything, entryOf, kindOf, srcOf, name, isGit, ORDER, SOURCES, kindStrip, originButton, drawBody }) {
  const card = new Map();
  for (const l of everything(() => true)) { const e = entryOf(l.package); if (e && !card.has(e)) card.set(e, l); }
  const sorted = es.map((e) => {
    const l = card.get(e);
    if (l) return { e, kind: kindOf(l), src: srcOf(l) };
    const kind = e.middleware || e.isMiddleware || e.gateway || e.isGateway ? "mw" : e.agent || e.isAgent ? "ag" : e.tools?.length && !e.providers?.length ? "tl" : e.page && !e.providers?.length ? "pg" : "pv";
    return { e, kind, src: isGit(e.spec) ? "gh" : name(e.spec).startsWith("@magpie-community/") ? "ours" : "third" };
  });
  const kinds = ORDER.filter((k) => sorted.some((x) => x.kind === k));
  const srcs = SOURCES.map(([v]) => v).filter((v) => v && sorted.some((x) => x.src === v));
  if (!kinds.includes(st.kind) || kinds.length < 2) st.kind = "";
  if (!srcs.includes(st.src) || srcs.length < 2) st.src = "";
  const base = sorted.filter((x) => !st.src || x.src === st.src);
  const shown = base.filter((x) => !st.kind || x.kind === st.kind).map((x) => x.e);
  let none = null;
  if (!shown.length) {
    // a pick that leaves nothing says so, with the way back
    none = el("div", "pm-ghnone");
    const every = el("button", "text", t("Show all"));
    every.type = "button";
    every.onclick = () => { st.kind = st.src = ""; drawBody(); };
    none.append(el("span", "", t("No plugin matches")), every);
  }
  return {
    shown, none,
    strip: kinds.length > 1 ? kindStrip(kinds, (k) => base.filter((x) => !k || x.kind === k).length, st.kind, (k) => { st.kind = k; drawBody(); }) : null,
    pop: srcs.length > 1 ? originButton(st.src, null, (v) => { st.src = v; drawBody(); }) : null,
  };
};
