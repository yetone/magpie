// Each tab's mark in the sidebar's list, by the page and the tab's id (segs'
// data-seg, Settings' data-set). app.js imports this with the first list it
// draws, so it weighs nothing on the core; a tab with none (a plugin's) keeps
// its place in line all the same.
// The sections a page's list is cut into, by tab id: a section none of whose
// tabs shows has no name (app.js navSubs), as does a list of one section.
window.NAV_SUB_GROUPS = {
  library: { instructions: "Every agent", memory: "Every agent", mcp: "Every agent", skills: "Every agent", hooks: "Every agent", env: "Every agent", subagents: "Every agent", plugins: "One agent", pi: "One agent", rtk: "magpie", bili: "magpie" },
};
// the tabs [tab, its place in the strip, its section's name where one starts],
// in sections in order, each in the strip's order
window.navSections = (view, items, t) => {
  const gs = window.NAV_SUB_GROUPS[view], grp = (b) => gs?.[b.dataset.seg], ranks = gs ? [...new Set(Object.values(gs))] : [];
  const named = new Set(items.map(grp)).size > 1;
  let sect;
  return items.map((b, i) => [b, i]).sort(([a], [b]) => ranks.indexOf(grp(a)) - ranks.indexOf(grp(b)))
    .map(([b, i]) => [b, i, named && grp(b) !== sect ? t((sect = grp(b)) || "") : ""]);
};
window.NAV_SUB_IC = {
  library: {
    instructions: "M4 2.5h5.5L12 5v8.5H4z M9.5 2.5V5H12 M6 8h4 M6 10.5h3",
    memory: "M5 2.5h6v11L8 11l-3 2.5z",
    mcp: "M6 2.5v3 M10 2.5v3 M4.5 5.5h7v2a3.5 3.5 0 0 1-7 0z M8 11v2.5",
    skills: "M8 2.5l1.4 3.6 3.6 1.4-3.6 1.4L8 12.5 6.6 8.9 3 7.5l3.6-1.4z",
    plugins: "M6.5 3h3v2a1.2 1.2 0 0 0 2.4 0V3h1.1v10.5H3.5v-3.2h2a1.2 1.2 0 0 0 0-2.4h-2V3z",
    hooks: "M10.5 2.5v6.5a3 3 0 0 1-6 0V7.5 M3 9l1.5-1.5L6 9",
    env: "M5.5 3.5h-1.5v9h1.5 M10.5 3.5h1.5v9h-1.5 M6.5 8h3",
    subagents: "M6 3a2.2 2.2 0 1 0 0 4.4A2.2 2.2 0 0 0 6 3z M2 13c.5-2.2 2-3.4 4-3.4s3.5 1.2 4 3.4 M11.5 7.5a1.5 1.5 0 1 0 0 .01 M10.2 12.5c.3-1.3 1.1-2 2.3-2s1.6.5 1.9 1.4",
    rtk: "M2.5 3.5h11v9h-11z M5 6.5l1.5 1.5L5 9.5 M8.5 9.5h2.5",
    bili: "M8 2v4 M6 4.2 8 6l2-1.8 M8 14v-4 M6 11.8 8 10l2 1.8 M3 8h10",
    pi: "M8 2.5l5 2.5v6L8 13.5 3 11V5z M3 5l5 2.5L13 5 M8 7.5v6",
  },
  usage: {
    usage: "M3.5 13V9 M6.5 13V4.5 M9.5 13v-3 M12.5 13V7",
    requests: "M3 5.5h9.5 M10.5 3.5l2 2-2 2 M13 10.5H3.5 M5.5 8.5l-2 2 2 2",
    sessions: "M3 3.5h10v7H7l-3 2.5v-2.5H3z",
    context: "M8 2.5l5.5 3L8 8.5l-5.5-3z M2.5 8.5 8 11.5l5.5-3 M2.5 11 8 14l5.5-3",
  },
  plugins: {
    builtin: "M8 2.5l5 2.5v6L8 13.5 3 11V5z M3 5l5 2.5L13 5 M8 7.5v6",
    discover: "M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11z M10.2 5.8 9 9 5.8 10.2 7 7z",
    installed: "M8 2.5v7 M5.5 7 8 9.5 10.5 7 M3 10v3h10v-3",
  },
  settings: {
    general: "M3 5h10 M3 11h10 M6 3.5v3 M10 9.5v3",
    usage: "M2.5 10.5a5.5 5.5 0 1 1 11 0 M8 10.5l2.5-3",
    network: "M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11z M2.5 8h11 M8 2.5c1.6 1.7 2.3 3.5 2.3 5.5S9.6 11.8 8 13.5 M8 2.5C6.4 4.2 5.7 6 5.7 8s.7 3.8 2.3 5.5",
    models: "M4.5 4.5h7v7h-7z M6.5 2.5v2 M9.5 2.5v2 M6.5 11.5v2 M9.5 11.5v2 M2.5 6.5h2 M2.5 9.5h2 M11.5 6.5h2 M11.5 9.5h2",
    privacy: "M8 2.5l4.5 1.8V8c0 2.8-2 4.6-4.5 5.5C5.5 12.6 3.5 10.8 3.5 8V4.3z",
    otel: "M2.5 8.5h2.5l1.5-4 3 7 1.5-3h2.5",
    sync: "M3.2 8a4.8 4.8 0 1 0 1.4-3.4 M2.8 2.6v2.4h2.4 M8 5.3V8l1.9 1.2",
    plugins: "M6.5 3h3v2a1.2 1.2 0 0 0 2.4 0V3h1.1v10.5H3.5v-3.2h2a1.2 1.2 0 0 0 0-2.4h-2V3z",
    about: "M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11z M8 7.5v3.5 M8 5h.01",
  },
};
