// Settings' search, and what it finds on the other pages (canlee886 on X):
// a routing group's strategy, a provider's proxy or an agent are set on
// their own pages, and a search for one in Settings said no setting
// matched. Each page's places that match are listed after Settings' own
// rows, under the page's name, and Open goes to that page and to the
// place, lit. ⌘K (Ctrl+K off the Mac) and the header's magnifier bring the
// search up from any page (app.js findAnywhere). app.js imports this the
// first time a search runs, so it weighs nothing on the core.

// Settings' own rows matching the words, shown or English, by their text,
// section heading or part name (sgfvdsg86265 on X); Escape restores
const setText = (n) => [n.textContent, ...[...n.querySelectorAll("[data-en]")].map((e) => e.dataset.en)].join(" ").toLowerCase();
window.setFind = function () {
  if (!provLinksDrawn && setQuery) provLinksDraw();
  const v = $("#view-settings");
  v.classList.add("set-searching");
  const words = setQuery.split(/\s+/);
  let any = false;
  for (const id of SET_TABS) {
    const page = $("#setPage-" + id), tab = $("#setTab-" + id);
    const part = setText(tab) + (id === "otel" ? " otel" : ""); // not "usage" for Allowances
    let hits = 0, head = "";
    const lit = new Set();
    for (const n of page.querySelectorAll(".row-head, .row")) {
      if (n.matches(".row-head")) {
        head = setText(n);
        continue;
      }
      if (n.parentElement.closest(".row")) continue; // a row in a row goes with it
      const says = part + " " + head + " " + setText(n);
      const hit = words.every((w) => says.includes(w));
      n.classList.toggle("set-miss", !hit);
      let shows = hit;
      for (let a = n; shows && a && a !== page; a = a.parentElement) if (a.hidden) shows = false;
      if (shows && featureHides(n)) shows = false; // a built-in plugin switched off
      // a provider's link with no such provider to open
      const ps = n.agents && (providers || providersEarly?.got)?.providers;
      if (shows && ps && !ps.some((x) => n.agents.includes(x.account?.agent))) n.classList.add("set-miss"), shows = false;
      if (!shows) continue;
      hits++;
      lit.add(n.closest(".list") || n.parentElement);
    }
    // a section's heading and list go when nothing in them is left
    for (const box of page.querySelectorAll(".list")) {
      if (box.parentElement.closest(".row")) continue; // inside a row: it goes with the row
      box.classList.toggle("set-miss", ![...lit].some((l) => box.contains(l)));
    }
    for (const h of page.querySelectorAll(":scope > .row-head")) {
      const next = h.nextElementSibling;
      h.classList.toggle("set-miss", !next || next.classList.contains("set-miss"));
    }
    page.dataset.part = tab.textContent;
    page.hidden = !hits;
    any ||= hits > 0;
  }
  window.setFolds?.(v); // a hit under a folded Advanced (provlinks.js)
  // the built-ins that match (setfeats.js)
  if (window.setFeatsFind) any = setFeatsFind(words) > 0 || any;
  else import("./setfeats.js").then(() => setQuery && setFind(), () => {});
  any = elseFind(words) > 0 || any;
  const none = $("#setNone");
  none.hidden = any;
  none.textContent = t("No setting matches “{q}”", { q: $("#setQ").value.trim() });
};

// a place's words: its English and the reader's language, lower case
const say = (...s) => s.filter(Boolean).map((x) => x + " " + t(x)).join(" ").toLowerCase();
// the pages the sidebar has: one switched off is not offered
const pageOn = (v) => {
  const b = $(`#nav button[data-view="${v}"]`);
  return !!b && !b.hidden && !featureHides(b);
};
// a place found lit, once more on a second Open
function lightUp(n) {
  n.classList.remove("find-lit");
  void n.offsetWidth;
  n.classList.add("find-lit");
}
// to a page from Settings' search, Back coming back to the search
async function goTo(v) {
  jumpFrom(v);
  return show(v);
}
// at a place on a page now shown: in sight when the click asked, and lit
function landAt(n, go, v) {
  if (!n) return;
  if (go) { n.scrollIntoView({ block: "center" }); arrived($("#view-" + v)); }
  lightUp(n);
}
// a page's control drawn a moment after it is shown (a plugin page's tabs)
async function drawn(sel) {
  for (let i = 0; i < 40; i++) {
    const n = $(sel);
    if (n) return n;
    await new Promise((r) => setTimeout(r, 50));
  }
  return null;
}
const LIB_TABS = [["instructions", "Instructions"], ["memory", "Memory"], ["mcp", "MCP servers", "mcp"], ["skills", "Skills"], ["hooks", "Hooks"],
  ["env", "Environment variables", "env"], ["subagents", "Subagents"], ["plugins", "Claude plugins"]];
const USE_TABS = [["usage", "Overview", "allowances quota"], ["requests", "Requests", "ledger"], ["sessions", "Sessions"], ["context", "Context"]];
// what a provider's editor sets, and the words a reader may look for it by;
// when says whether this provider's editor has it
const PROV_FIELDS = [["Models", "model"], ["Accounts", "keys", (p) => p.account || p.keyList?.length], ["API key", "token", (p) => !p.account && !p.keyList?.length],
  ["Endpoints", "base url address", (p) => !p.account], ["Proxy"], ["Fallback"], ["Context window"], ["Max output", "tokens", (p) => !p.account],
  ["Concurrency"], ["Requests per minute", "rpm rate limit"], ["Price rate"], ["Free for me"], ["Headers", "", (p) => !p.account], ["Extra body", "", (p) => !p.account]];

// every place on the other pages: { page, key, name, sub, own, of, open }.
// own is the place's own words, of its parent's (a group's or a provider's
// name): a field of every group is listed only for words that say the
// field, not for a group's name alone
function places() {
  const out = [];
  for (const v of ["agents", "sessions", "library", "providers", "routing", "groups", "gateway", "usage", "masking", "plugins"]) {
    if (!pageOn(v)) continue;
    out.push({ page: v, key: v, name: t(pageName(v)), own: say(pageName(v)) + " " + v, open: () => goTo(v) });
  }
  if (pageOn("agents")) {
    for (const a of state.agents || []) out.push({ page: "agents", key: "a:" + a.id, name: a.name, own: (a.name + " " + a.id).toLowerCase(), open: async (go) => {
      if (await goTo("agents")) landAt($(`#agents .row.agent[data-id="${CSS.escape(a.id)}"]`), go, "agents");
    } });
    out.push({ page: "agents", key: "profiles", name: t("Profiles"), own: say("Profiles"), open: async (go) => { if (await goTo("agents")) landAt($("#view-agents .profiles"), go, "agents"); } });
  }
  const tabs = (v, list, sel) => {
    if (!pageOn(v)) return;
    for (const [id, name, more] of list) out.push({ page: v, key: v + ":" + id, name: t(name), own: say(name) + " " + (more || ""), open: async (go) => {
      if (!await goTo(v)) return;
      const b = await drawn(sel(id));
      if (!b) return;
      if (!b.classList.contains("on")) b.click();
      landAt($(sel(id)), go, v);
    } });
  };
  tabs("library", LIB_TABS, (id) => `#view-library .lib-tabs .opt[data-seg="${id}"]`);
  tabs("usage", USE_TABS, (id) => `#usageTab .opt[data-seg="${id}"]`);
  if (pageOn("providers")) {
    for (const p of providers?.providers || []) {
      const name = p.name || p.id;
      const open = (f) => async (go) => {
        if (f) advanced = { of: p.id, open: true }; // a field under Advanced is in sight
        jumpFrom("providers");
        await openProvider(p.id);
        if (view !== "providers") return;
        // the editor is drawn in the modal, and again as the list comes in:
        // the place is lit once the same one is there a moment later
        const at = () => {
          const ed = $("#modal .editor");
          return (f && ed && [...ed.querySelectorAll("label")].find((x) => x.textContent.trim().startsWith(t(f)))) || ed;
        };
        let n = await drawn("#modal .editor");
        for (let i = 0; i < 10 && n; i++) {
          await new Promise((r) => setTimeout(r, 150));
          const m = at();
          if (m === n) break;
          n = m;
        }
        if (view === "providers") landAt(n, go, "providers");
      };
      const of = (name + " " + p.id).toLowerCase();
      out.push({ page: "providers", key: "p:" + p.id, name, own: of, open: open(null) });
      for (const [f, more, when] of PROV_FIELDS) {
        if (when && !when(p)) continue;
        out.push({ page: "providers", key: "p:" + p.id + ":" + f, name: t(f), sub: name, own: say(f) + " " + (more || ""), of, open: open(f) });
      }
    }
  }
  // the routing groups, read by their page (plugins/routing/client.js)
  const gs = pageOn("groups") && window.routingFind?.();
  for (const g of gs?.groups || []) {
    const of = (g.name + " " + g.id + " " + (g.note || "")).toLowerCase();
    out.push({ page: "groups", key: "g:" + g.id, name: g.name, sub: g.note, own: of, open: (go) => { jumpFrom("groups"); return routingOpenGroup(g.id, "", go); } });
    for (const [f, name, words] of gs.fields) {
      out.push({ page: "groups", key: "g:" + g.id + ":" + f, name, sub: g.name, own: words, of, open: (go) => { jumpFrom("groups"); return routingOpenGroup(g.id, f, go); } });
    }
  }
  return out;
}

// the rows a page shows before Show more
const FIRST = 8;
let elseMore = new Set(), elseWords = "";
function elseFind(words) {
  let box = document.getElementById("setElse");
  if (!box) {
    box = el("div", "set-else");
    box.id = "setElse";
    $("#view-settings").append(box);
  }
  const q = words.join(" ");
  if (q !== elseWords) elseMore = new Set(), elseWords = q;
  const found = !words.length ? [] : places().filter((x) => {
    const all = x.own + " " + (x.of || "");
    return words.every((w) => all.includes(w)) && (!x.of || words.some((w) => x.own.includes(w)));
  });
  // a page's places, its own name first, then what a name says before a field
  const by = new Map();
  for (const x of found) {
    if (!by.has(x.page)) by.set(x.page, []);
    by.get(x.page).push(x);
  }
  const key = document.documentElement.lang + " " + q + " " + [...elseMore].join(",") + " " + found.map((x) => x.key + "=" + x.name + "/" + (x.sub || "")).join(" ");
  if (box.dataset.key === key) return found.length;
  box.dataset.key = key;
  const secs = [];
  for (const [v, list] of by) {
    list.sort((a, b) => !!a.of - !!b.of);
    const sec = el("section", "set-page find-page");
    sec.dataset.page = v;
    sec.dataset.part = t(pageName(v));
    const rows = el("div", "list prefs");
    const all = elseMore.has(v);
    for (const x of all ? list : list.slice(0, FIRST)) {
      const r = el("div", "row pref find-row");
      r.dataset.key = x.key;
      const who = el("div", "who");
      who.append(el("div", "name", x.name));
      if (x.sub) who.append(el("div", "sub", x.sub));
      const go = el("button", "text", t("Open"));
      go.type = "button";
      go.onclick = (e) => x.open(scrollOnPurpose(e, 3000));
      r.append(who, go);
      rows.append(r);
    }
    if (!all && list.length > FIRST) {
      const more = el("button", "text find-more", t("Show {n} more", { n: list.length - FIRST }));
      more.type = "button";
      more.onclick = () => { elseMore.add(v); elseFind(words); };
      sec.append(rows, more);
    } else sec.append(rows);
    secs.push(sec);
  }
  box.replaceChildren(...secs);
  // after the built-ins found, which setfeats.js adds once
  const feats = document.getElementById("setFeats");
  if (feats && feats.nextElementSibling !== box) feats.after(box);
  box.hidden = !found.length;
  return found.length;
}
window.findElse = elseFind;
