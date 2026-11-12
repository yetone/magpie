// Settings › Models › Web search's rows (the search plugin's part of
// Settings): the provider that searches for a model that can't, and the
// search APIs tried when no provider can, with their keys and order. Run
// after app.js, whose helpers it shares (el, segs, input, api, writingPrefs,
// dragRows, openPicker, sideEffortChip, savePrefs), and before start.js
// draws Settings.

// renderSearch: the web search APIs a model's search goes to when no
// provider can search (#419) — one row each, in the order they are tried,
// and a row to add one: which API, its key, and the address of one the user
// runs (SearXNG). They are set on their own; one magpie refuses is said in
// the row, what was typed kept.
let searchDraft = { vendor: "tavily", key: "", url: "", err: "" };
function renderSearch(s, keep) {
  const box = $("#searchList");
  box.replaceChildren();
  const off = s.builtins?.find((f) => f.id === "search")?.on === false;
  featOff(box, s, "search", t("magpie searches for no model until it is on. Adding a search API or naming a provider below turns it on"));
  renderSearcher(s, keep, box);
  // rows go into the box, or into a search API's own unit (its row and its
  // keys' row), which is dragged as one (#2286)
  let into = box;
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name), el("div", "sub", sub));
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    into.append(r);
    return r;
  };
  const set = (body, done) => writingPrefs(api("settings/search-api", body))
    .then((ns) => { prefs = ns; searchDraft.err = ""; done?.(); renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { searchDraft.err = e.message; status(e.message, "err"); renderSettings(); });
  const vendors = s.searchVendors || [];
  const d = searchDraft;
  if (!vendors.some((v) => v.id === d.vendor)) d.vendor = vendors[0]?.id || "";
  const vendor = () => vendors.find((v) => v.id === d.vendor) || {};
  // the one of the picked vendor already added: Add then changes it, its
  // key kept when none is typed
  const saved = () => (s.searchAPIs || []).find((a) => a.vendor === d.vendor);
  const pick = el("button", "proto pick search-vendor");
  pick.type = "button";
  pick.setAttribute("aria-label", t("Search API"));
  const key = input(d.key, t("API key"), "password");
  // any API's base URL (#1580): an endpoint of the user's own that answers
  // as that API does; empty is the vendor's own, a SearXNG's is required
  const url = input(d.url, "https://searx.example.com");
  key.className = "words search-key";
  url.className = "words search-url";
  key.setAttribute("aria-label", t("API key"));
  const get = el("button", "link", t("Get a key ↗"));
  get.onclick = () => vendor().keysURL && api("open", { url: vendor().keysURL });
  // a plugin's API that signs in as the plugin's provider takes no key: its
  // accounts are the provider's, on Providers
  const signs = el("span", "search-signs");
  const draw = () => {
    const v = vendor();
    pick.replaceChildren(el("span", "", v.name || ""), svg(CHEV, 11, 1.6));
    // the address it is set at shows in the field, so changing its key
    // never puts the vendor's own back in its place
    if (d.urlOf !== d.vendor) { d.url = saved()?.url || ""; d.urlOf = d.vendor; }
    url.value = d.url;
    url.placeholder = v.needURL ? "https://searx.example.com" : t("Base URL, empty for {url}", { url: v.base || "" });
    url.setAttribute("aria-label", v.needURL ? t("Address") : t("Base URL"));
    key.placeholder = v.needURL ? t("API key, if it needs one")
      : saved()?.keyList?.length ? t("Another key for {name}, or empty to keep its keys", { name: v.name || "" })
      : v.keyOptional ? t("API key, or empty for its free search")
      : t("API key, or several split by commas");
    get.hidden = !v.keysURL || !!v.auth;
    key.hidden = !!v.auth;
    signs.hidden = !v.auth;
    signs.textContent = v.auth ? t("Signs in with {name}", { name: v.auth.name }) : "";
  };
  pick.onclick = (e) => {
    e.stopPropagation();
    if (pick.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(pick, vendors.map((v) => ({ v: v.id, name: v.name, note: v.needURL ? t("your own") : "" })), d.vendor,
      (id) => { d.vendor = id; draw(); }, "Search API");
  };
  const add = el("button", "text", t("Add"));
  add.onclick = () => {
    d.key = key.value.trim(); d.url = url.value.trim();
    // one added before keeps its key when none is typed
    // You.com is asked at its free search with none (#1626)
    if (vendor().needURL ? !d.url : !d.key && !saved()?.key && !vendor().keyOptional && !vendor().auth) return (vendor().needURL ? url : key).focus();
    // a key typed for one added before joins its keys, never replaces them
    set({ vendor: d.vendor, key: d.key, url: d.url, ...(saved() && d.key ? { append: true } : {}) },
      () => { searchDraft = { vendor: d.vendor, key: "", url: "", err: "" }; });
  };
  for (const i of [key, url]) {
    i.oninput = () => { d.key = key.value; d.url = url.value; };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
  }
  draw();
  // the ones a search asks: one switched off is kept, never asked (#2286)
  const asked = (s.searchAPIs || []).filter((a) => !a.off);
  const apiFirst = s.searchFirst === "api" && asked.length > 0;
  const by = off ? t("Off in Plugins › Built-in")
    : s.searcher === "off" ? t("Provider search is off; only these APIs are used")
    : !s.searchProvider ? t("No provider can search, so these are asked")
    : apiFirst ? t("Asked before {who}, which searches when these fail", { who: s.searchProvider })
    : t("Now done by {who}; these come after it", { who: s.searchProvider });
  const head = row(t("Search APIs"), d.err || t("When a model can't search the web, magpie searches for it with these, in this order, and gives it what they found") + " · " + by,
    pick, key, signs, get, add, url);
  head.classList.add("rule-row", "search-add");
  head.querySelector(".val").classList.add("rule-add");
  if (d.err) head.querySelector(".sub").classList.add("err");
  const units = [];
  (s.searchAPIs || []).forEach((a, n) => {
    const unit = el("div", "search-unit" + (a.off ? " off" : ""));
    unit.dataset.vendor = a.vendor;
    box.append(unit);
    into = unit;
    units.push(unit);
    const x = el("button", "text", t("Remove"));
    x.onclick = () => set({ vendor: a.vendor, remove: true });
    // one that signs in says whose accounts it uses, or why it has none
    const auth = vendors.find((v) => v.id === a.vendor)?.auth;
    const signedIn = !auth ? ""
      : !auth.signedIn ? t("Not signed in to {name}: sign in to it on Providers", { name: auth.name })
      : !auth.on ? t("No account of {name} is on", { name: auth.name })
      : t("Signed in to {name} · accounts on: {n}", { name: auth.name, n: auth.on });
    // its keys are listed under it, each with its own Show and Copy
    const what = [signedIn || (a.ready ? "" : t("needs its key")), a.url].filter(Boolean).join(" · ");
    const tools = [x];
    // switched off it stays, key and all, and no search asks it (#2286)
    const sw = el("button", "lib-switch use-credits search-on" + (a.off ? "" : " on"));
    sw.type = "button";
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-checked", String(!a.off));
    sw.setAttribute("aria-label", t("Ask {name}", { name: a.name }));
    sw.title = a.off ? t("Off: kept with its key, and never asked") : t("On: asked in its turn");
    sw.append(el("i"));
    sw.onclick = () => {
      if (sw.disabled) return;
      sw.disabled = true;
      set({ vendor: a.vendor, on: !!a.off });
    };
    tools.push(sw);
    const r = row(`${n + 1}. ${a.name}`, a.off ? [t("Off"), what].filter(Boolean).join(" · ") : what, ...tools);
    r.classList.add("search-api");
    if (auth && !a.off && !(auth.signedIn && auth.on)) r.querySelector(".sub").classList.add("err");
    // with-keys, not :has(), which Safari before 15.4 can't parse
    if (!auth && (a.keyList?.length || !vendors.find((v) => v.id === a.vendor)?.needURL)) { unit.classList.add("with-keys"); searchKeysRow(s, a, row, set); }
    into = box;
  });
  searchArrange(box, units, set);
  // which goes first when both a provider and a search API can search
  // (#928); with only one of them there is nothing to choose
  if (s.searchProvider && asked.length) {
    const r = row(t("Search first with"), t("The other is asked when it fails"),
      segs([["model", t("Model search")], ["api", t("Search APIs")]], apiFirst ? "api" : "model",
        (v) => savePrefs({ ...keep, searchFirst: v === "api" ? "api" : "" })));
    r.id = "searchFirstRow";
  }
  // a model whose provider searches the web by itself is given magpie's
  // search instead, as every other model is (cybercaius on Discord). The
  // row is always there: with nothing magpie can search with it can't be
  // chosen, and says why and where to add a search ("开关在哪个位置")
  {
    const can = !!(s.searchProvider || asked.length);
    const sw = segs([["own", t("Their own")], ["magpie", t("magpie's")]], s.searchAll ? "magpie" : "own",
      (v) => writingPrefs(api("settings/search-all", { on: v === "magpie" }))
        .then((ns) => { prefs = state.settings = ns; renderSettings(); }).catch(settingsErr));
    if (!can) for (const b of sw.querySelectorAll("button")) b.disabled = true;
    const r = row(t("Models that search by themselves"), can ? t("Search with their provider's own search, or with magpie's, as every other model does")
      : t("magpie has nothing to search with: add a Search API above, or pick a provider under Searches for other models"), sw);
    r.id = "searchAllRow";
    r.classList.add("search-all-row", ...(can ? [] : ["off"]));
  }
  // how many results a search that names no count is answered with, as
  // Claude Code's WebSearch and Codex's web_search never do (#5545,
  // mdwsk88); a count set by hand in settings.json is offered too
  {
    const cur = s.searchCount > 0 ? Math.min(s.searchCount, 20) : 6;
    const ns = [...new Set([3, 6, 10, 15, 20, cur])].sort((a, b) => a - b);
    const r = row(t("Results per search"), t("More results give the model more to read: each carries up to 1500 bytes of its page"),
      segs(ns.map((n) => [String(n), String(n)]), String(cur),
        (v) => writingPrefs(api("settings/search-count", { n: Number(v) }))
          .then((ns) => { prefs = state.settings = ns; renderSettings(); }).catch(settingsErr)));
    r.id = "searchCountRow";
  }
  // the same search as an MCP server of the gateway's (Kayphoon on
  // Discord), for an agent to mount as its web_search and web_fetch tools
  const mcpURL = mcpAddress(s, "/mcp/magpie/web-search");
  const mcp = row(t("MCP server"), t("Cursor, Claude Desktop and other agents can add this search as an MCP server (Streamable HTTP), with a web_search tool and a web_fetch tool that reads a page by its URL. From another computer, send a gateway key as Authorization: Bearer <key>."),
    ...mcpWhere(s, renderSettings), el("code", "", mcpURL), copyBtn(mcpURL, t("Address")));
  mcp.id = "searchMcpRow";
  // The Library gives this search to the agents picked for it, as Magpie
  // Web Search, and lists it under its MCP servers: an address alone had
  // the reader look for it there and not find it (lutra_fs on Discord).
  // As Images' Magpie Image (#2091), the row says how to get there from
  // where the reader is: gateway mode has no Library, and the Library can
  // be turned off
  mcp.querySelector(".who").append(...libWay("magpie-web-search", t("Give agents Magpie Web Search…"), "search-mcp-lib"));
}

// Plugins › Built-in › Web search's own line (featGo): what a model that
// can't search is searched with now, in place of how many are set up, or
// that nothing is yet (netube99 on X)
featWho.search = (line, f, who) => {
  if (!f.on) return;
  if (f.searches?.length) who.querySelector(".feat-why")?.remove();
  line.append(f.searches?.length ? t("Searches with {names}", { names: f.searches.join(" → ") })
    : el("span", "warn", t("Nothing to search with yet")));
};

// searchArrange: the search APIs are asked in the order they are listed,
// and put in another by dragging one anywhere off its buttons, or with
// Alt+↑/↓ on it (#2286, as routing's groups are, #779). The new order
// shows at once and is put back when it can't be saved.
let searchArranging = false;
function searchArrange(box, units, set) {
  if (units.length < 2) return;
  const move = (unit, to) => {
    const from = units.indexOf(unit);
    to = Math.max(0, Math.min(units.length - 1, to));
    for (const u of units) u.style.transform = "";
    if (to === from) return;
    // moving a node in the page takes the keyboard off it
    const focus = document.activeElement === unit;
    const order = units.map((u) => u.dataset.vendor);
    order.splice(to, 0, ...order.splice(from, 1));
    const anchor = units.at(-1).nextSibling;
    const next = order.map((v) => units.find((u) => u.dataset.vendor === v));
    for (const u of next) box.insertBefore(u, anchor);
    // a second move before the page is drawn again goes from this order
    units.splice(0, units.length, ...next);
    next.forEach((u, i) => {
      const name = u.querySelector(".search-api .name");
      if (name) name.textContent = name.textContent.replace(/^\d+\./, `${i + 1}.`);
    });
    if (focus) unit.focus({ preventScroll: true });
    set({ arrange: order }, () => {
      if (focus) requestAnimationFrame(() => document.querySelector(`#searchList .search-unit[data-vendor="${CSS.escape(unit.dataset.vendor)}"]`)?.focus({ preventScroll: true }));
    });
    // a refused order: set draws the page again from what is saved, the
    // order as it was
  };
  for (const unit of units) {
    const name = unit.querySelector(".search-api .name")?.textContent || "";
    unit.tabIndex = 0;
    unit.setAttribute("role", "group");
    unit.setAttribute("aria-label", name + " · " + t("Drag to reorder · Alt+↑/↓ to move"));
    unit.title = t("Drag to reorder · Alt+↑/↓ to move");
    unit.prepend(el("span", "grip"));
    unit.addEventListener("click", (e) => {
      if (unit.dataset.dragged) { e.preventDefault(); e.stopImmediatePropagation(); }
    }, true);
    unit.onkeydown = (e) => {
      if (e.target !== unit || !e.altKey || !["ArrowUp", "ArrowDown"].includes(e.key) || searchArranging) return;
      e.preventDefault(); e.stopPropagation();
      move(unit, units.indexOf(unit) + (e.key === "ArrowUp" ? -1 : 1));
    };
    unit.onpointerdown = (e) => {
      if (searchArranging || e.target.closest("input, textarea, select, a, button, code, .segs, .sub.revealed, .search-key-list")) return;
      searchArranging = dragRows(e, unit, unit, box, units, (to) => move(unit, to), () => {},
        () => { searchArranging = false; });
    };
  }
}

// searchKeysRow: under a search API that takes keys, its list of them
// (辰林鸭~ on Discord), as a provider's keys are listed: each masked, on or
// off by its dot, shown, copied or removed, dragged into another order or
// moved with Alt+↑/↓, and one or several added at its foot. How a search
// picks among those on, in turn (the default) or in order (SlouchyQuill on
// Discord), and why a key is asked last now and until when, show beside
// them; what each has used of its limit (Tavily's) is read once as the page
// is drawn and again on Check again. A read that fails says unknown and
// why, never 0.
const searchQuotaSeen = {};
let searchKeyAdding = null, searchKeyShown = {};
function searchKeysRow(s, a, row, set) {
  const list0 = a.keyList || [], on = list0.filter((k) => !k.off).length;
  const tools = [];
  if (on > 1) tools.push(segs([["", t("In turn")], ["order", t("In order")]], a.pick || "",
    (v) => set({ vendor: a.vendor, pick: v || "turns" })));
  const keys = (body, said) => writingPrefs(api("settings/search-keys", { vendor: a.vendor, ...body }))
    .then((r) => { prefs = r.settings; searchKeyAdding = null; renderSettings(); status(said?.(r) || t("Saved"), "ok", 1500); })
    .catch((e) => { status(e.message, "err"); renderSettings(); });
  const list = el("div", "accts keys search-key-list");
  const seenAs = [a.key, a.url].join(" ");
  const ask = (fresh) => {
    searchQuotaSeen[a.vendor] = { as: seenAs, loading: true };
    draw();
    api("settings/search-quota", { vendor: a.vendor, fresh })
      .then((r) => { searchQuotaSeen[a.vendor] = { as: seenAs, keys: r.keys || [] }; })
      .catch((e) => { searchQuotaSeen[a.vendor] = { as: seenAs, err: e.message }; })
      .finally(draw);
  };
  const num = (n) => Number(n).toLocaleString(intlLang());
  const whyOf = (w) => ({ "turned away": t("turned away"), "out of credit": t("out of credit"), "rate limited": t("rate limited"), "used up": t("used up") })[w] || w;
  // each key's quota, in the rows in the page now: Settings may have been
  // drawn again while the read was out
  const draw = () => {
    const box = document.querySelector(`.search-keys[data-vendor="${CSS.escape(a.vendor)}"] .search-key-list`) || list;
    const seen = searchQuotaSeen[a.vendor];
    for (const line of box.querySelectorAll(".acc[data-ref]")) {
      const at = line.querySelector(".key-quota");
      if (!at) continue;
      at.replaceChildren();
      if (!seen || seen.loading) { at.append(el("span", "faint", t("Reading what each key has used…"))); continue; }
      if (seen.err) { at.append(el("span", "err", t("Unknown: {why}", { why: seen.err }))); continue; }
      const q = seen.keys.find((x) => x.ref === line.dataset.ref);
      if (!q) continue;
      const said = !q.known ? t("Unknown: {why}", { why: q.err || "" })
        : q.unlimited ? t("{used} used, no limit", { used: num(q.used) })
        : t("{used} of {limit} used", { used: num(q.used), limit: num(q.limit) });
      const notes = [];
      if (q.known && q.of === "plan") notes.push(q.plan ? t("{plan} plan, shared by its account's keys", { plan: q.plan }) : t("the plan's, shared by its account's keys"));
      if (q.paygoLeft > 0) notes.push(t("{n} pay-as-you-go left", { n: num(q.paygoLeft) }));
      at.append(el("span", q.known ? (q.usedUp ? "used warn" : "used") : "used err", said));
      if (q.usedUp) at.append(el("span", "warn", t("used up")));
      if (notes.length) at.append(el("span", "faint", notes.join(" · ")));
    }
  };
  const rows = [];
  for (const k of list0) {
    const r = el("div", "acc" + (k.off ? " off" : " in-use"));
    r.dataset.ref = k.ref;
    const dot = el("button", "dot tick");
    dot.type = "button";
    if (!k.off) dot.append(svg(CHECK, 10, 2.2));
    dot.title = k.off ? t("Use this key too") : t("Stop using this key");
    dot.setAttribute("aria-label", dot.title);
    dot.setAttribute("aria-pressed", String(!k.off));
    dot.onclick = () => keys({ ref: k.ref, on: !!k.off });
    const shown = searchKeyShown[a.vendor + " " + k.ref];
    const code = el("code", "n" + (shown ? " revealed" : ""), shown || k.masked);
    const eye = el("button", "text search-eye", shown ? t("Hide") : t("Show"));
    eye.onclick = async () => {
      const id = a.vendor + " " + k.ref;
      if (searchKeyShown[id]) delete searchKeyShown[id];
      else try { searchKeyShown[id] = (await api("settings/search-key", { vendor: a.vendor, ref: k.ref })).key; } catch (e) { return status(e.message, "err"); }
      code.textContent = searchKeyShown[id] || k.masked;
      code.classList.toggle("revealed", !!searchKeyShown[id]);
      eye.textContent = searchKeyShown[id] ? t("Hide") : t("Show");
    };
    const cp = el("button", "copy search-copy");
    cp.type = "button";
    cp.title = t("Copy");
    cp.setAttribute("aria-label", t("Copy"));
    cp.append(svg(COPY_ICON, 12, 1.5));
    cp.onclick = async () => {
      try { copy((await api("settings/search-key", { vendor: a.vendor, ref: k.ref })).key, t("API key"), cp); } catch (e) { status(e.message, "err"); }
    };
    const x = el("button", "text search-key-remove", t("Remove"));
    x.onclick = () => keys({ ref: k.ref, remove: true });
    r.append(dot, code, el("span", "grow"), eye, cp, x);
    // why it is asked last now, and until when: on a line of its own
    const state = el("div", "key-state");
    if (k.off) state.append(el("span", "faint", t("Off")));
    else if (k.rest && new Date(k.restUntil) > Date.now()) {
      r.classList.add("resting");
      state.append(el("span", "warn key-rest", t("asked last until {time}: {why}", { time: hhmm(k.restUntil), why: whyOf(k.rest) })));
    }
    if (a.quota && !k.off) state.append(el("span", "key-quota"));
    if (state.childNodes.length) r.append(state);
    rows.push(r);
    list.append(r);
  }
  if (searchKeyAdding === a.vendor) {
    // one key, or several pasted at once, split as a provider's are
    const box = el("div", "acc adding");
    const i = input("", t("Paste a key, or several split by commas or lines"), "password");
    i.className = "words search-key-new";
    i.setAttribute("aria-label", t("API key"));
    const go = () => {
      const n = splitKeys(i.value).length;
      if (!n) return i.focus(NO_SCROLL);
      keys({ add: i.value }, (r) => r.had ? t("{n} keys added, {m} already there", { n: r.added, m: r.had })
        : r.added === 1 ? t("Key added") : t("{n} keys added", { n: r.added }));
    };
    const add = el("button", "text primary", t("Add"));
    add.onclick = go;
    const no = el("button", "text", t("Cancel"));
    no.onclick = () => { searchKeyAdding = null; renderSettings(); };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") go(); else if (e.key === "Escape") no.onclick(); };
    box.append(i, no, add);
    list.append(box);
    queueMicrotask(() => i.focus(NO_SCROLL));
  } else list.append(addRow(list0.length ? t("Add another key") : t("Add a key"), () => { searchKeyAdding = a.vendor; renderSettings(); }));
  searchKeyArrange(a, list, rows, keys);
  if (a.quota && on) {
    const again = el("button", "text search-quota-again", t("Check again"));
    again.onclick = () => ask(true);
    tools.push(again);
  }
  const r = row(t("Keys of {name}", { name: a.name }),
    on > 1 ? t("In turn spreads searches over the keys; in order uses the first until it is refused. A key refused lately is asked last.") : "",
    ...tools);
  r.classList.add("search-keys");
  r.dataset.vendor = a.vendor;
  r.querySelector(".who").append(list);
  if (a.quota && on) {
    if (searchQuotaSeen[a.vendor]?.as !== seenAs) ask(false);
    else draw();
  }
}

// searchKeyArrange: a search API's keys are asked in their list's order
// (in order, or from the first in turn), put in another by dragging a key
// or with Alt+↑/↓ on it; the order shows at once and the page is drawn
// again from what is saved.
function searchKeyArrange(a, list, rows, keys) {
  if (rows.length < 2) return;
  list.classList.add("reorderable");
  let busy = false;
  const move = (r, to) => {
    const from = rows.indexOf(r);
    to = Math.max(0, Math.min(rows.length - 1, to));
    if (to === from || busy) return;
    busy = true;
    const order = rows.map((x) => x.dataset.ref);
    order.splice(to, 0, ...order.splice(from, 1));
    const focus = document.activeElement === r;
    list.insertBefore(r, to > from ? rows[to].nextSibling : rows[to]);
    rows.splice(to, 0, ...rows.splice(from, 1));
    if (focus) r.focus(NO_SCROLL);
    keys({ arrange: order }).then(() => {
      if (focus) document.querySelector(`.search-keys[data-vendor="${CSS.escape(a.vendor)}"] .acc[data-ref="${CSS.escape(r.dataset.ref)}"]`)?.focus(NO_SCROLL);
    });
  };
  for (const r of rows) {
    r.tabIndex = 0;
    r.title = t("Drag to reorder · Alt+↑/↓ to move");
    r.prepend(el("span", "grip"));
    r.addEventListener("click", (e) => {
      if (r.dataset.dragged) { e.preventDefault(); e.stopImmediatePropagation(); }
    }, true);
    r.onkeydown = (e) => {
      if (e.target !== r || !e.altKey || !["ArrowUp", "ArrowDown"].includes(e.key)) return;
      e.preventDefault(); e.stopPropagation();
      move(r, rows.indexOf(r) + (e.key === "ArrowUp" ? -1 : 1));
    };
    r.onpointerdown = (e) => {
      if (busy || e.target.closest("input, button, code.revealed")) return;
      e.stopPropagation();
      dragRows(e, r, r, list, rows, (to) => move(r, to));
    };
  }
}

// renderSearcher: the provider that searches the web for a model that
// can't — the one magpie picks, or one named, by itself (its small model)
// or with a model of it. One named that is gone, off or can't search gives
// way to magpie's pick, which the row says. Relays said to search can be
// named, but are never picked automatically (#359), and the row says so.
function renderSearcher(s, keep, box) {
  const choices = s.searchChoices || [];
  const v = s.searcher || "";
  const off = v === "off";
  const named = (id) => {
    const [pid, ...rest] = id.split("/");
    const c = choices.find((x) => x.id === pid);
    if (!c) return id;
    if (c.service || (c.search && rest.join("/") === "@search")) return `${c.name} · ${t("its web search")}`;
    if (!rest.length) return `${c.name} · ${c.smallName || c.small}`;
    const m = c.models.find((x) => x.id === id);
    return `${m ? m.name : rest.join("/")} · ${c.name}`;
  };
  const icOf = (id) => choices.find((x) => x.id === id.split("/")[0])?.icon;
  const r = el("div", "row pref searcher-row");
  const who = el("div", "who");
  const sub = el("div", "sub", off ? (s.searchAll ? t("When provider search is off, only configured Search APIs are used, for every model. Without a Search API, magpie does not add a search tool.") : t("When provider search is off, only configured Search APIs are used. Without a Search API, magpie does not add a search tool. Providers' native web search is unchanged.")) : t("When a model can't search the web directly, the selected provider searches for it and returns the results. Searches may use the service's quota or incur charges; if the provider magpie picks fails, it tries other available sources, and a provider you select here gives way to the ones you add after it, in their order, then, when it can't search at all (out of quota or rate-limited), to other available sources, then to your Search APIs."));
  if (!off && v && s.searchUnused) {
    const why = { gone: t("it is no longer in magpie"), off: t("it is turned off"), cant: t("it can't search the web by itself"), nomodel: t("it lists no model") }[s.searchUnused] || s.searchUnused;
    sub.append(" · ", el("span", "warn searcher-unused", t("{who} isn't used: {why}, so magpie picks one", { who: named(v), why })));
  }
  // the one named didn't search last time, and no other provider searched
  // in its place (Gearclickist on Discord: Grok picked answered without
  // searching, and Codex's pricier model searched instead, unasked). First,
  // so it reads before the help.
  const failed = s.searchFailed;
  if (!off && v && !s.searchUnused && failed) {
    const why = failed.notSearched ? t("it answered without searching the web, so its account or plan may not offer web search") : failed.nothingFound ? t("its reply named no page it found") : failed.why || t("it failed");
    const said = failed.instead ? t("{who} didn't search {when}: {why}. {next} searched in its place.", { who: named(failed.searcher), when: ago(failed.at), why, next: named(failed.instead) })
      : (s.searcherThen || []).length ? t("{who} didn't search {when}: {why}. The ones after it didn't search either; add another provider after it, or a Search API.", { who: named(v), when: ago(failed.at), why })
      : t("{who} didn't search {when}: {why}. No other provider searched in its place; pick Automatic or another provider, or add a Search API.", { who: named(v), when: ago(failed.at), why });
    sub.prepend(el("span", "warn searcher-failed", said), " · ");
  }
  const plans = choices.filter((c) => c.service).map((c) => c.name);
  if (!off && plans.length) sub.append(" · ", el("span", "searcher-own",
    t("A Kimi Code plan ({names}) searches for its own models first, with its web search; for other models only when named here", { names: plans.join(", ") })));
  const googles = choices.filter((c) => c.own).map((c) => c.name);
  if (!off && googles.length) sub.append(" · ", el("span", "searcher-own",
    t("{names} search for their own models first, with Gemini's Google Search", { names: googles.join(", ") })));
  if (!off && s.searchRelays?.length) sub.append(" · ", el("span", "searcher-relays",
    t("These relays must be selected manually and are not used for automatic selection or fallback: {names}.", { names: s.searchRelays.join(", ") })));
  // Being left out can also mean no usable model, not just no search support.
  if (!off && s.searchLeftOut?.length) sub.append(" · ", el("span", "searcher-left-out",
    t("These providers can't be selected to search for other models with the current configuration: {names}. Their models can still get search results through other available search providers or configured search APIs.", { names: s.searchLeftOut.length > 6
      ? t("{names} and {n} more", { names: s.searchLeftOut.slice(0, 5).join(", "), n: s.searchLeftOut.length - 5 })
      : s.searchLeftOut.join(", ") })));
  who.append(el("div", "name", t("Searches for other models")), sub);
  const b = el("button", "rt-cond on searcher-pick");
  b.type = "button";
  b.setAttribute("aria-label", t("Searches for other models"));
  if (off) b.append(el("span", "", t("Off")));
  else if (v && !s.searchUnused) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else {
    if (s.searchAuto) b.append(icon(choices[0]?.icon || "generic"));
    b.append(el("span", "", t("Automatic") + " · " + (s.searchAuto || t("no provider that searches"))));
  }
  const options = [{ value: "", label: t("Automatic"), note: s.searchAuto || t("no provider that searches"), reset: true },
    { value: "off", label: t("Off"), note: t("Search APIs"), reset: true }];
  for (const c of choices) {
    if (c.service) {
      options.push({ value: c.id, label: t("its web search"), note: c.name, icon: c.icon, group: c.name });
      continue;
    }
    // a ChatGPT account searches by its own web search, with no model's
    // tokens spent (lucy_85908 on Discord); its models stay to be picked
    if (c.search) options.push({ value: `${c.id}/@search`, label: t("its web search"), note: c.name, icon: c.icon, group: c.name });
    // its small model is offered once (Player on Discord): the model of the
    // same name below it searched just the same, "<provider>/<small>" being
    // that model named rather than magpie's pick, so a pick saved so is
    // ticked here. No "via magpie": no agent asks for these
    const small = c.small ? `${c.id}/${c.small}` : "";
    options.push({ value: v === small ? small : c.id, label: t("{model}, its small model", { model: c.smallName || c.small }), note: c.name, icon: c.icon, group: c.name });
    for (const m of c.models) if (m.id !== small) options.push({ value: m.id, label: m.name || m.id, note: c.name, icon: c.icon, group: c.name });
  }
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "searcher", label: "model", value: v, options,
    onPick: (id) => { if (id !== v) savePrefs({ ...keep, searcher: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  if (!off) sideEffortChip(val, s.searchEfforts, s.searcherEffort || "", s.searchEffortNow, "searcher-effort",
    "none sent, the model's own", (e) => savePrefs({ ...keep, searcherEffort: e }));
  r.append(who, val);
  box.append(r);
  if (!off && v && !s.searchUnused) renderSearcherThen(s, keep, box, options, named, icOf);
}

// renderSearcherThen: the providers asked, in their order, when the one
// named above fails to search, and no other (axelburks on Discord: "代搜
// 供应商能不能支持设置多个，这样可以一个失败了换另一个供应商"). Each
// is a chip that changes or removes it, and Add puts one at the end.
function renderSearcherThen(s, keep, box, options, named, icOf) {
  const v = s.searcher || "";
  const then = s.searcherThen || [];
  const save = (list) => savePrefs({ ...keep, searcherThen: list });
  // what can still be added: not the one above, nor one listed already
  const free = (keepOne) => options.filter((o) => !o.reset && o.value !== v && (o.value === keepOne || !then.includes(o.value)));
  const r = el("div", "row pref searcher-then");
  const who = el("div", "who");
  who.append(el("div", "name", t("If it fails")),
    el("div", "sub", t("These providers search in its place, in this order, when the one above fails; no other provider does.")));
  const val = el("div", "val searcher-then-list");
  then.forEach((x, n) => {
    const b = el("button", "rt-cond on searcher-then-pick");
    b.type = "button";
    b.append(el("span", "searcher-then-n", `${n + 1}.`), icon(icOf(x) || "generic"), el("span", "", named(x)));
    b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "searcherThen", label: "model", value: x,
      options: [{ value: "", label: t("Remove"), reset: true }, ...free(x)],
      onPick: (id) => { if (id !== x) save(id ? then.map((y, i) => i === n ? id : y) : then.filter((_, i) => i !== n)); } }, b, ev);
    val.append(b);
  });
  const left = free("");
  if (left.length) {
    const add = el("button", "text searcher-then-add", t("Add"));
    add.type = "button";
    add.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "searcherThen", label: "model", value: "", options: left,
      onPick: (id) => { if (id) save([...then, id]); } }, add, ev);
    val.append(add);
  }
  r.append(who, val);
  box.append(r);
}
