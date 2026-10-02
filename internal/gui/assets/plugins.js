// Plugins: the market of OpenCode provider plugins — subscriptions magpie
// signs in to through someone else's code. Discover lists the plugins
// magpie suggests (the community repo's list, with what npm says of each
// now) and finds the rest on npm; Installed is what was added, to sign in
// with, update, switch off or remove. Each part is drawn as it comes
// (#488): what's installed from /api/plugins, the plugins suggested from
// /api/plugins/listings, and what npm says of them (versions, downloads)
// from /api/plugins/npm after; a sign-in happens in the Providers add
// sheet, as every other subscription's does.
(() => {
  const page = $("#view-plugins");
  if (!page) return;

  let mine = null;       // /api/plugins: what's installed
  let listings = null;   // /api/plugins/listings: the plugins suggested
  const npm = {};        // /api/plugins/npm: package → what npm says of it
  let failed = "";       // why what's installed couldn't be loaded
  let failedList = "";   // ...and the plugins suggested
  let tab = "discover";
  try { tab = localStorage.getItem("magpie.pluginTab") || tab; } catch {}
  let query = "";
  let hits = null;       // npm's answer for query: { q, list } | { q, loading } | { q, error }
  let searchTimer = 0;
  const busy = new Map(); // package → "add" | "remove" | "upgrade" | "off"
  let asking = null;     // { pkg, op }: a remove or switch-off waiting on the user's yes
  let shown = "";

  const SEARCH = "M7 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M10 10l3 3";
  const DOWN = "M8 3v7.5 M4.8 7.6 8 10.8l3.2-3.2 M3.5 13h9";
  const SHIELD = "M8 2.3 13 4v3.8c0 3-2.1 5.1-5 5.9-2.9-.8-5-2.9-5-5.9V4z M5.8 8l1.6 1.6 2.9-3";
  const OUTL = "M9.5 3.5h3v3 M12.5 3.5 7.5 8.5 M11 9.5v3H3.5V5h3";
  const FOLDER = "M14.7 12.7a1.3 1.3 0 0 1-1.3 1.3H2.6a1.3 1.3 0 0 1-1.3-1.3V3.3A1.3 1.3 0 0 1 2.6 2h3.3l1.3 2h6.2a1.3 1.3 0 0 1 1.3 1.3z";

  const name = (spec) => { const i = spec.lastIndexOf("@"); return i > 0 && !spec.startsWith(".") && !spec.includes("/", i) ? spec.slice(0, i) : spec; };
  // a plugin added from a folder is a folder's path, not a package name
  const isPath = (p) => /^file:\/\//.test(p) || /^\./.test(p) || /^\//.test(p) || /^[A-Za-z]:[\\/]/.test(p);
  // ...and is known by the folder's name, not its whole path
  const label = (spec) => { const n = name(spec); return isPath(n) ? n.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || n : n; };
  // one added from a git repository (github:owner/repo, git+https://…), as
  // the plugin store tells them apart: known by the package it installed
  const isGit = (p) => !isPath(p) && (/^(?:(?:github|gitlab|bitbucket):|git\+[a-z]+:\/\/|git:\/\/)\S+$/.test(p) || /^https?:\/\/(?:www\.)?(?:github\.com|gitlab\.com|bitbucket\.org)\/\S+$/.test(p) || /^[A-Za-z0-9][A-Za-z0-9_.-]*\/[A-Za-z0-9_.-]+(?:#\S+)?$/.test(p));
  // ...and its page on the web, for the plugin's Source link
  const gitWeb = (p) => {
    const s = p.replace(/#.*$/, "");
    const m = /^(github|gitlab|bitbucket):(.+)$/.exec(s) || (/^[^:]+\/[^:]+$/.test(s) && ["", "github", s]);
    if (m) return "https://" + { github: "github.com", gitlab: "gitlab.com", bitbucket: "bitbucket.org" }[m[1]] + "/" + m[2];
    const u = s.replace(/^git\+/, "").replace(/\.git$/, "");
    return /^https?:\/\//.test(u) ? u : "";
  };
  const lang = () => (document.documentElement.lang || "").startsWith("zh") ? "zh" : "en";
  const summary = (l) => l.summary?.[lang()] || l.summary?.en || l.npm?.description || "";
  const count = (n) => n >= 1e6 ? (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "k" : String(n || 0);
  const entryOf = (pkg) => mine?.plugins?.find((e) => name(e.spec) === pkg);
  // npm has the versions of a package: not a folder's, nor a git one's
  const onNPM = (spec) => !isPath(spec) && !isGit(spec);
  // the providers a plugin signs in to, as the add sheet knows them
  const subsOf = (pkg) => (providers?.plugins || []).filter((x) => name(x.spec) === pkg);
  const newer = (a, b) => {
    const p = (v) => (v || "").split(/[.+-]/).slice(0, 3).map((x) => parseInt(x, 10) || 0);
    const [x, y] = [p(a), p(b)];
    for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] > y[i];
    return false;
  };

  function glyph(d, size = 14, stroke = 1.5) { return svg(d, size, stroke); }
  function logo(ic, big) {
    const box = el("span", "pm-logo" + (big ? " big" : ""));
    if (ic) box.append(icon(ic));
    else box.append(glyph(PUZZLE, big ? 22 : 17, 1.4));
    return box;
  }

  // npm's answers, put where the cards and rows read them: on the
  // listing and the entry themselves, so a plugin's page opened before
  // npm answered has them too
  function merge() {
    for (const l of listings || []) if (npm[l.package]) l.npm = npm[l.package];
    for (const e of mine?.plugins || []) if (onNPM(e.spec) && npm[name(e.spec)]?.version) e.latest = npm[name(e.spec)].version;
  }
  // redrawn as a part comes: the search field keeps its focus
  function redraw() {
    const q = page.querySelector(".pm-find input");
    const typing = q && document.activeElement === q;
    draw();
    if (typing) page.querySelector(".pm-find input")?.focus({ preventScroll: true });
  }
  async function loadMine() {
    try {
      mine = await api("plugins");
      failed = "";
    } catch (e) {
      failed = e.message;
    }
    merge();
  }
  async function loadListings() {
    try {
      const r = await api("plugins/listings");
      listings = r.listings || [];
      failedList = "";
    } catch (e) {
      failedList = e.message;
    }
    merge();
  }
  // what npm says of the plugins suggested and those installed: drawn
  // when it comes, never waited on
  async function askNPM(only) {
    const names = [...new Set([...(listings || []).map((l) => l.package), ...(mine?.plugins || []).filter((e) => onNPM(e.spec)).map((e) => name(e.spec))])]
      .filter((n) => !only || !npm[n]);
    if (!names.length) return;
    try {
      const r = await api("plugins/npm?names=" + encodeURIComponent(names.join(",")));
      Object.assign(npm, r.npm || {});
    } catch {}
    merge();
    // one npm said nothing of, now or ever: as one it doesn't have
    for (const l of listings || []) if (!l.npm) l.npm = { weekly: 0 };
    drawBody();
  }

  async function load() {
    if (!mine && !listings) draw();
    const parts = [
      loadMine().then(() => { redraw(); window.renderPluginDot?.(); }),
      loadListings().then(redraw),
      providers ? null : loadProviders().then(drawBody, () => {}),
    ];
    await Promise.all(parts);
    askNPM();
  }
  window.loadPlugins = load;
  // pluginQuery: Discover, looking for q (the add sheet found nothing by it)
  window.pluginQuery = (q) => {
    if (q === query && !q) return;
    query = q;
    tab = "discover";
    clearTimeout(searchTimer);
    hits = q.trim().length >= 2 ? { q: q.trim(), loading: true } : null;
    if (hits) searchNPM(hits.q);
    draw();
  };

  async function refresh() {
    try {
      const [m, p] = await Promise.all([api("plugins"), api("providers")]);
      mine = m;
      providers = p;
      merge();
      // the Providers page owns the dialog as it draws: only drawn when shown
      if (view === "providers") renderProviders();
    } catch (e) { status(e.message, "err"); }
    draw();
    window.renderPluginDot?.();
    askNPM(true);
  }

  async function act(pkg, op, body, done) {
    if (busy.size && op !== "off") return;
    busy.set(pkg, op);
    draw();
    try {
      await api("plugins/" + op, body);
      busy.delete(pkg);
      await refresh();
      if (done) done();
    } catch (e) {
      busy.delete(pkg);
      draw();
      status(e.message, "err");
    }
  }
  // the built-ins with accounts this plugin could run in their place
  const movableOf = (pkg) => (mine?.movable || []).filter((c) => c.package === pkg);
  // move: a built-in's accounts onto its plugin, installing it if need be,
  // as its editor's Runs on does
  async function move(c) {
    if (busy.size) return;
    busy.set(c.package, "move");
    asking = null;
    draw();
    try {
      providers = await api("provider/move", { id: c.id });
      busy.delete(c.package);
      await refresh();
      const now = providers.providers.find((x) => x.id === c.id);
      const accts = now?.account?.logins?.length || c.accounts, models = (now?.models || []).filter((x) => x.on).length;
      status(t("{name} now runs on its plugin — {accounts}, {models}. Its editor in Providers moves it back.", {
        name: c.name,
        accounts: t(accts === 1 ? "{n} account" : "{n} accounts", { n: accts }),
        models: t(models === 1 ? "{n} model" : "{n} models", { n: models }),
      }), "ok", 9000);
    } catch (e) {
      busy.delete(c.package);
      draw();
      // no editor here to sign in above: say where
      const why = e.why?.code === "lapsed"
        ? t("every {name} account needs signing in again. Sign one in under Providers, then move.", { name: c.name })
        : moveWhy({ why: e.why, error: e.message }, { name: c.name });
      status(t("It stays built-in: {error}", { error: why }), "err", 12000);
    }
  }
  const moveButton = (b, c, here) => {
    b.classList.add("go", "move");
    b.textContent = here ? t("Move {name} here", { name: c.name }) : t(c.accounts === 1 ? "Move my {n} {name} account" : "Move my {n} {name} accounts", { name: c.name, n: c.accounts });
    b.title = t("Runs {name} on this plugin in place of the built-in, with the same accounts; nothing changes if one doesn't work through it", { name: c.name });
    b.disabled = busy.size > 0;
    b.onclick = (ev) => { ev.stopPropagation(); move(c); };
    return b;
  };
  const install = (pkg, nm) => act(pkg, "add", { spec: pkg }, () => {
    const subs = subsOf(pkg);
    status(subs.length ? t("{name} is installed — sign in to use it", { name: nm }) : t("{name} is installed", { name: nm }), "ok");
  });

  // the one button a plugin shows: what to do with it next
  function actionFor(pkg, nm, listed) {
    const b = el("button", "pm-act");
    const e = entryOf(pkg);
    const op = busy.get(pkg);
    if (op === "add" || op === "upgrade" || op === "move") {
      b.classList.add("busy");
      if (op === "move") b.classList.add("move");
      b.append(el("span", "spin"), el("span", "", op === "move" ? t("Moving…") : op === "upgrade" ? t("Updating…") : mine?.bun ? t("Installing…") : t("Getting Bun…")));
      b.disabled = true;
      if (!mine?.bun) b.title = t("Plugins run on Bun {v}, downloaded once", { v: mine?.bunVersion || "" });
      return b;
    }
    // not known yet whether it's installed, or whether npm has it: a
    // button that waits, rather than one that says the wrong thing
    if (!mine || (!e && listed && !listed.npm)) {
      b.classList.add("busy");
      b.append(el("span", "spin"));
      b.disabled = true;
      return b;
    }
    if (!e) {
      if (listed && !listed.npm?.version) {
        b.textContent = t("Coming soon");
        b.disabled = true;
        b.title = t("Not on npm yet");
        return b;
      }
      const c = movableOf(pkg)[0];
      if (c) return moveButton(b, c);
      b.classList.add("get");
      b.textContent = t("Install");
      b.disabled = busy.size > 0;
      b.onclick = (ev) => { ev.stopPropagation(); install(pkg, nm); };
      return b;
    }
    if (e.off) {
      b.textContent = t("Switch on");
      b.onclick = (ev) => { ev.stopPropagation(); act(pkg, "off", { spec: e.spec, off: false }); };
      return b;
    }
    const subs = subsOf(pkg);
    const out = subs.find((x) => !x.signedIn);
    // signed in to nothing yet, with a built-in's accounts to bring
    const c = !subs.some((x) => x.signedIn) && movableOf(pkg)[0];
    if (c) return moveButton(b, c, true);
    if (out) {
      b.classList.add("go");
      b.textContent = t("Sign in");
      b.title = subs.length > 1 ? t("Sign in to {name}", { name: out.name }) : "";
      b.onclick = (ev) => { ev.stopPropagation(); pluginSignIn(out.id); };
      return b;
    }
    if (subs.length) {
      b.classList.add("done");
      b.append(glyph(CHECK, 11, 2), el("span", "", t("Signed in")));
      b.title = t("Open {name} in Providers", { name: subs[0].name });
      b.onclick = (ev) => { ev.stopPropagation(); openProvider(subs[0].id); };
      return b;
    }
    b.classList.add("done");
    b.append(glyph(CHECK, 11, 2), el("span", "", t("Installed")));
    b.disabled = true;
    return b;
  }

  function card(l) {
    const c = el("div", "pm-card");
    c.tabIndex = 0;
    c.setAttribute("role", "button");
    c.dataset.pkg = l.package;
    const e = entryOf(l.package);
    if (e) c.classList.add("have");
    const top = el("div", "pm-top");
    const who = el("div", "pm-who");
    const nm = el("div", "pm-name");
    nm.append(el("b", "", l.name));
    who.append(nm);
    const by = el("div", "pm-by");
    if (l.community) {
      const v = el("span", "pm-verified");
      v.append(glyph(SHIELD, 11, 1.5), el("span", "", t("magpie community")));
      v.title = t("Written for magpie by its community, in github.com/magpie-community/plugins");
      by.append(v);
    } else by.append(el("span", "", l.npm?.publisher || l.package));
    who.append(by);
    const act = actionFor(l.package, l.name, l);
    // a move says whose accounts: too long for the top row, it has one of its own
    const own = act.classList.contains("move");
    top.append(logo(l.icon), who);
    if (!own) top.append(act);
    const sum = el("p", "pm-sum", summary(l));
    const meta = el("div", "pm-meta");
    if (l.npm?.weekly) {
      const d = el("span", "pm-dl");
      d.append(glyph(DOWN, 11, 1.5), el("span", "", t("{n}/week", { n: count(l.npm.weekly) })));
      d.title = t("{n} downloads on npm last week", { n: l.npm.weekly.toLocaleString() });
      meta.append(d);
    }
    if (l.npm?.version) meta.append(el("span", "pm-ver", "v" + (e?.version || l.npm.version)));
    if (e && e.latest && e.version && newer(e.latest, e.version)) meta.append(el("span", "pm-chip up", t("Update")));
    if (e?.error && !e.off) meta.append(el("span", "pm-chip bad", t("Didn't load")));
    else if (e?.off) meta.append(el("span", "pm-chip", t("Off")));
    if (l.replaces) {
      const b = el("span", "pm-chip soft", t("Also built in"));
      b.title = t("magpie also signs in to this itself, for now; the plugin keeps it working if the built-in one is retired");
      meta.append(b);
    }
    c.append(top, sum, meta);
    if (own) c.append(act);
    c.onclick = () => detail(l);
    c.onkeydown = (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); detail(l); } };
    return c;
  }

  function skeleton(n) {
    const g = el("div", "pm-grid");
    for (let i = 0; i < n; i++) {
      const c = el("div", "pm-card ghost");
      c.append(el("div", "g1"), el("div", "g2"), el("div", "g3"));
      g.append(c);
    }
    return g;
  }

  function section(title, hint, list) {
    const box = el("section", "pm-sec");
    const h = el("div", "pm-sechead");
    h.append(el("h3", "", title));
    if (hint) h.append(el("span", "", hint));
    const g = el("div", "pm-grid");
    for (const l of list) g.append(card(l));
    box.append(h, g);
    return box;
  }

  function head() {
    const h = el("div", "lib-head pm-head");
    const n = mine?.plugins?.length || 0;
    const tabs = segs([["discover", t("Discover")], ["installed", t("Installed") + (n ? " · " + n : "")]], tab, (id) => {
      tab = id;
      try { localStorage.setItem("magpie.pluginTab", id); } catch {}
      drawBody();
    });
    tabs.classList.add("lib-tabs");
    const find = el("label", "pm-find");
    find.append(glyph(SEARCH, 13, 1.6));
    const q = el("input");
    q.type = "search";
    q.placeholder = t("Search plugins and npm…");
    q.value = query;
    q.spellcheck = false;
    q.autocomplete = "off";
    q.setAttribute("aria-label", t("Search plugins"));
    q.oninput = () => {
      query = q.value;
      if (tab !== "discover") { tab = "discover"; draw(); page.querySelector(".pm-find input")?.focus(); }
      clearTimeout(searchTimer);
      const s = query.trim();
      if (s.length < 2) hits = null;
      else {
        hits = { q: s, loading: true };
        searchTimer = setTimeout(() => searchNPM(s), 350);
      }
      drawBody();
    };
    q.onkeydown = (ev) => { ev.stopPropagation(); if (ev.key === "Escape" && q.value) { q.value = ""; q.oninput(); } };
    find.append(q);
    h.append(tabs, el("span", "grow"), find);
    h.classList.toggle("stuck", page.scrollTop > 0);
    return h;
  }

  async function searchNPM(s) {
    try {
      const r = await api("plugins/search?q=" + encodeURIComponent(s));
      if (hits?.q === s) hits = { q: s, list: r.hits };
    } catch (e) {
      if (hits?.q === s) hits = { q: s, error: e.message };
    }
    if (hits?.q === s) drawBody();
  }

  page.addEventListener("scroll", () => page.querySelector(".pm-head")?.classList.toggle("stuck", page.scrollTop > 0), { passive: true });

  let body = null;
  function draw() {
    const scroll = page.scrollTop;
    page.replaceChildren(head());
    body = el("div", "lib-body pm-body");
    page.append(body);
    drawBody();
    page.scrollTop = scroll;
  }

  function drawBody() {
    if (!body) return;
    const key = tab + (query ? ":q" : "");
    body.classList.toggle("enter", shown !== key);
    shown = key;
    body.replaceChildren();
    // the search field keeps its focus: only the body is redrawn while typing
    if (tab === "installed") return drawInstalled();
    if (failedList && !listings) {
      body.append(el("p", "pm-empty", t("Couldn't load the plugins: {error}", { error: failedList })));
      return;
    }
    if (!listings) {
      body.append(intro(), skeleton(6));
      return;
    }
    const f = query.trim().toLowerCase();
    const ls = listings.filter((l) => !f || [l.name, l.package, summary(l), (l.providers || []).join(" "), l.npm?.publisher || ""].join(" ").toLowerCase().includes(f));
    if (!f) {
      body.append(intro());
      // magpie's community's alone: others' plugins are found by a search
      const ours = ls.filter((l) => l.community);
      if (ours.length) body.append(section(t("magpie community"), t("written for magpie, checked against its own sign-ins"), ours));
      body.append(manual());
      return;
    }
    if (ls.length) body.append(section(t("Suggested"), "", ls));
    const known = new Set(listings.map((l) => l.package));
    const box = el("section", "pm-sec");
    const h = el("div", "pm-sechead");
    h.append(el("h3", "", t("On npm")), el("span", "", t("OpenCode plugins anyone published — read what one does before you install it")));
    box.append(h);
    if (!hits) box.append(el("p", "pm-note", t("Type two letters or more to search npm")));
    else if (hits.loading) box.append(skeleton(2));
    else if (hits.error) box.append(el("p", "pm-note bad", hits.error));
    else {
      const list = hits.list.filter((x) => !known.has(x.package));
      if (!list.length) box.append(el("p", "pm-note", ls.length ? t("Nothing else on npm") : t("No plugin called “{q}”", { q: query.trim() })));
      const g = el("div", "pm-grid");
      for (const x of list) g.append(card({ package: x.package, name: x.package, npm: x }));
      box.append(g);
    }
    body.append(box);
  }

  function intro() {
    const box = el("div", "pm-intro");
    const text = el("div", "pm-introtext");
    text.append(el("h2", "", t("Subscriptions, as plugins")));
    text.append(el("p", "", t("Plugins sign in to coding plans and make their requests; the models then work in every agent, like any provider's. They're OpenCode's provider plugins, run on Bun.")));
    const trust = el("p", "pm-trust");
    trust.append(glyph(SHIELD, 12, 1.5), el("span", "", t("A plugin is someone else's code with your sign-in: install the ones you trust.")));
    text.append(trust);
    const art = el("div", "pm-art");
    for (const ic of ["zcode", "githubcopilot", "gemini-color", "zed", "kiro-color"]) art.append(logo(ic));
    box.append(text, art);
    return box;
  }

  // a package or a folder the market doesn't list
  function manual() {
    const box = el("form", "pm-manual");
    box.append(el("span", "pm-mlabel", t("Have one in mind?")));
    const spec = el("input", "pm-minput");
    spec.placeholder = t("npm package, GitHub repo (github:owner/repo) or a folder");
    spec.spellcheck = false;
    spec.autocomplete = "off";
    spec.setAttribute("aria-label", t("Plugin"));
    spec.onkeydown = (ev) => ev.stopPropagation();
    const go = el("button", "text primary", t("Install"));
    go.type = "submit";
    go.disabled = busy.size > 0;
    if (busy.has("+")) { go.textContent = t("Installing…"); spec.disabled = true; }
    box.onsubmit = (ev) => {
      ev.preventDefault();
      const s = spec.value.trim();
      if (!s || busy.size) return;
      act("+", "add", { spec: s }, () => status(t("{name} is installed", { name: s }), "ok"));
    };
    box.append(spec);
    // a folder is picked, not typed, where magpie can show the system's
    // picker (`magpie web` can't, and the market says so)
    if (mine?.picker) box.append(browse(spec, go));
    box.append(go);
    return box;
  }

  // the system's folder picker: what it answers goes in the field, for the
  // Install beside it
  function browse(spec, go) {
    const b = el("button", "pm-browse");
    b.type = "button";
    b.append(glyph(FOLDER, 14, 1.5));
    b.title = t("Choose a folder on this computer");
    b.setAttribute("aria-label", t("Choose a folder"));
    b.disabled = busy.size > 0 || spec.disabled;
    b.onclick = async () => {
      b.disabled = true;
      try {
        const r = await api("plugins/choose", {});
        if (r.dir) {
          spec.value = r.dir;
          spec.focus();
        }
      } catch (e) {
        status(e.message, "err");
      } finally {
        b.disabled = busy.size > 0 || spec.disabled;
      }
    };
    return b;
  }

  function drawInstalled() {
    if (failed && !mine) {
      body.append(el("p", "pm-empty", t("Couldn't load the plugins: {error}", { error: failed })));
      return;
    }
    if (!mine) { body.append(skeleton(2)); return; }
    const es = mine.plugins || [];
    if (!es.length) {
      const none = el("div", "pm-none");
      none.append(logo("", true), el("b", "", t("No plugins yet")), el("p", "", t("Find a subscription in Discover and install it; it signs in from here.")));
      const b = el("button", "pm-act get", t("Discover plugins"));
      b.onclick = () => { tab = "discover"; draw(); };
      none.append(b);
      body.append(none);
      return;
    }
    // by name, in the order the reader picked (#481), not the order they
    // were installed in
    const list = el("div", "list pm-list");
    const fill = () => list.replaceChildren(...[...es].sort(byName(sortOf("plugins"), shownName)).map(installedRow));
    if (es.length > 1) {
      const h = el("div", "row-head pm-listhead");
      h.append(el("span", "grow"), sortBy("plugins", NAME_SORTS, fill));
      body.append(h);
    }
    fill();
    body.append(list);
    const foot = el("div", "pm-foot");
    const outdated = es.filter((e) => e.latest && e.version && newer(e.latest, e.version));
    foot.append(el("span", "", mine.bun ? t("Plugins run on Bun {v}", { v: mine.bunVersion }) : ""), el("span", "grow"));
    if (outdated.length) {
      const up = el("button", "text", busy.has("*") ? t("Updating…") : t("Update all ({n})", { n: outdated.length }));
      up.disabled = busy.size > 0;
      up.onclick = () => act("*", "update", {}, () => status(t("Plugins updated"), "ok"));
      foot.append(up);
    }
    body.append(foot);
  }

  // an installed plugin's name as its row shows it: the market's, else the
  // package's or the folder's
  function shownName(e) {
    const pkg = name(e.spec);
    return (listings || []).find((x) => x.package === pkg)?.name || (isGit(e.spec) && e.package) || label(e.spec);
  }

  function installedRow(e) {
    const pkg = name(e.spec);
    const l = (listings || []).find((x) => x.package === pkg);
    const r = el("div", "row pm-row" + (e.off ? " off" : ""));
    const who = el("div", "who");
    const nm = el("div", "name");
    nm.append(el("span", "", shownName(e)));
    if (e.version) nm.append(el("span", "pm-ver", "v" + e.version));
    if (e.latest && e.version && newer(e.latest, e.version)) nm.append(el("span", "pm-chip up", t("v{v} out", { v: e.latest })));
    else if (e.autoUpdated && e.autoUpdated.to === e.version) {
      // magpie updated it by itself lately: the row says so, quietly
      const c = el("span", "pm-chip soft", t("Auto-updated"));
      c.title = t("magpie updated it from v{from} to v{to} on {date}", { from: e.autoUpdated.from, to: e.autoUpdated.to, date: new Date(e.autoUpdated.at).toLocaleDateString(document.documentElement.lang || undefined, { month: "short", day: "numeric" }) });
      nm.append(c);
    }
    // the built-in subscriptions moved onto it: taking it away moves them
    // back, so the row says it carries them
    const moved = (e.moved || []).map((id) => SUBS.find((x) => x.agent === id)?.name || id);
    const names = moved.join(t(", "));
    if (moved.length && !e.off) {
      const c = el("span", "pm-chip moved");
      c.append(el("span", "dot"), el("span", "", t("Serves {names}", { names })));
      c.title = t("{names} runs on this plugin in place of the built-in", { names });
      nm.append(c);
    }
    who.append(nm);
    const subs = subsOf(pkg);
    const sub = el("div", "sub");
    const cands = e.off ? [] : movableOf(pkg);
    const ask = asking?.pkg === pkg && !busy.size && (asking.op === "move" ? cands.length : moved.length) ? asking.op : "";
    if (ask === "move") {
      sub.textContent = t("{names} can run on it again, with the same accounts.", { names: cands.map((c) => c.name).join(t(", ")) });
      sub.classList.add("ask");
    } else if (ask) {
      sub.textContent = t(ask === "remove" ? "{names} goes back to the built-in, with its accounts, before the plugin is removed." : "{names} goes back to the built-in, with its accounts, before the plugin is switched off.", { names });
      sub.classList.add("ask");
    } else if (e.off) sub.textContent = t("Off");
    else if (e.error) { sub.textContent = t("Didn't load: {error}", { error: e.error }); sub.classList.add("bad"); sub.title = e.error; }
    else if (subs.length) {
      for (const [i, x] of subs.entries()) {
        if (i) sub.append(el("span", "sep", " · "));
        const s = el("span", "pm-sub" + (x.signedIn ? " in" : ""));
        // its built-in runs it, with its accounts: nothing to fix here
        const builtin = !x.signedIn && cands.some((c) => c.id === x.pid);
        s.append(el("span", "dot"), el("span", "", x.signedIn ? t("{name}: signed in", { name: x.name }) : builtin ? t("{name}: on magpie's built-in", { name: x.name }) : t("{name}: not signed in", { name: x.name })));
        if (builtin) s.title = t("{name} runs on magpie's built-in, with your accounts; Move {name} here runs it on this plugin", { name: x.name });
        sub.append(s);
      }
    } else sub.textContent = e.providers.length ? t("Signs in to {names}", { names: e.providers.join(t(", ")) }) : t("Signs in to nothing magpie can use");
    who.append(sub);
    r.append(logo(l?.icon || subs[0]?.icon), who);
    const val = el("div", "val");
    const b = busy.get(pkg) || busy.get(e.spec);
    if (e.latest && e.version && newer(e.latest, e.version) && !e.off) {
      const up = el("button", "text", b === "upgrade" ? t("Updating…") : t("Update"));
      up.disabled = busy.size > 0;
      up.onclick = () => act(pkg, "upgrade", { spec: e.spec }, () => status(t("{name} updated to v{v}", { name: l?.name || pkg, v: e.latest }), "ok"));
      val.append(up);
    } else if (isGit(e.spec) && !e.off) {
      // npm has no newer version of a git one to offer: Update fetches
      // its repository again
      const up = el("button", "text", b === "upgrade" ? t("Updating…") : t("Update"));
      up.title = t("Fetches it from {repo} again", { repo: e.spec });
      up.disabled = busy.size > 0;
      up.onclick = () => act(pkg, "upgrade", { spec: e.spec }, () => status(t("{name} is up to date", { name: e.package || e.spec }), "ok"));
      val.append(up);
    }
    const here = !e.off && !e.error && !subs.some((x) => x.signedIn) && cands[0];
    if (here && !ask) {
      const mv = el("button", "text primary", b === "move" ? t("Moving…") : t("Move {name} here", { name: here.name }));
      mv.title = t("Runs {name} on this plugin in place of the built-in, with the same accounts; nothing changes if one doesn't work through it", { name: here.name });
      mv.disabled = busy.size > 0;
      mv.onclick = () => move(here);
      val.append(mv);
    }
    if (!e.off && !e.error && subs.some((x) => !x.signedIn)) {
      const s = el("button", "text" + (here ? "" : " primary"), t("Sign in"));
      s.onclick = () => pluginSignIn(subs.find((x) => !x.signedIn).id);
      val.append(s);
    }
    const back = () => { if (moved.length) status(t("{names} is back on the built-in", { names }), "ok"); };
    // switched on again: the built-ins that went back when it went off
    // are offered its way again, in the row
    const again = () => { if (movableOf(pkg).length) { asking = { pkg, op: "move" }; draw(); } };
    const off = () => act(pkg, "off", { spec: e.spec, off: !e.off }, e.off ? again : back);
    const remove = () => act(pkg, "remove", { spec: e.spec }, () => { status(t("{name} removed", { name: l?.name || pkg }), "ok"); back(); });
    if (ask) {
      // asked in the row, not a dialog: the user sees which go back
      const yes = el("button", "text primary", t(ask === "move" ? "Move" : ask === "remove" ? "Remove" : "Switch off"));
      yes.onclick = () => { asking = null; ask === "move" ? move(cands[0]) : ask === "remove" ? remove() : off(); };
      const no = el("button", "text", t(ask === "move" ? "Not now" : "Cancel"));
      no.onclick = () => { asking = null; draw(); };
      val.append(no, yes);
      r.append(val);
      return r;
    }
    const onoff = el("button", "text", b === "off" && !e.off && moved.length ? t("Moving back…") : t(e.off ? "Switch on" : "Switch off"));
    onoff.disabled = !!b;
    onoff.onclick = () => { if (!e.off && moved.length) { asking = { pkg, op: "off" }; draw(); } else off(); };
    const rm = el("button", "text quiet", b === "remove" ? t(moved.length ? "Moving back…" : "Removing…") : t("Remove"));
    rm.title = moved.length ? t("{names} goes back to the built-in first, then the plugin is removed", { names }) : t("Removes the plugin and what it installed; its sign-ins are kept until you sign out");
    rm.disabled = busy.size > 0;
    rm.onclick = () => { if (moved.length) { asking = { pkg, op: "remove" }; draw(); } else remove(); };
    val.append(onoff, rm);
    r.append(val);
    r.onclick = (ev) => { if (!ev.target.closest("button")) detail(l || (isGit(e.spec) ? { package: e.spec, name: e.package || e.spec, npm: { version: e.version, repository: gitWeb(e.spec) } } : { package: pkg, name: label(e.spec), npm: { version: e.latest } })); };
    return r;
  }

  // the plugin's page: what it is, what npm says of it, and its README
  async function detail(l) {
    // a folder plugin: what it is comes from the folder, not from npm; a
    // git one's from the folder it was installed to
    const local = isPath(l.package) || isGit(l.package);
    const ed = el("div", "editor pm-detail");
    const hd = el("div", "ehead pm-dhead");
    hd.append(logo(l.icon, true));
    const who = el("div", "pm-who");
    const nm = el("div", "pm-name");
    nm.append(el("b", "", l.name));
    const by = el("div", "pm-by"); // the words in a span of their own, or a flex row cuts them without the ellipsis
    by.append(el("span", "", l.community ? t("magpie community") + " · " + l.package : [l.npm?.publisher, l.package].filter(Boolean).join(" · ")));
    who.append(nm, by);
    const actBox = el("div", "pm-dact");
    // redrawn only when what it would say changes: a button made afresh
    // loses the pointer's hover
    let said = "";
    const redrawAct = () => {
      const b = actionFor(l.package, l.name, l.npm ? l : null);
      const now = b.className + b.textContent + b.disabled;
      if (now !== said) { said = now; actBox.replaceChildren(b); }
    };
    redrawAct();
    hd.append(who, el("span", "grow"), actBox);
    ed.append(hd);
    const main = el("div", "pm-dbody");
    const sum = summary(l);
    if (sum) main.append(el("p", "pm-dsum", sum));
    const facts = el("div", "pm-facts");
    const fact = (k, v) => { if (!v) return; const f = el("div", "pm-fact"); f.append(el("span", "k", k), typeof v === "string" ? el("span", "v", v) : v); facts.append(f); };
    fact(t("Version"), l.npm?.version && "v" + l.npm.version);
    fact(t("Downloads"), l.npm?.weekly ? t("{n}/week", { n: count(l.npm.weekly) }) : "");
    fact(t("License"), l.npm?.license);
    const upd = el("span", "v", "…");
    fact(t("Updated"), upd);
    if (l.providers?.length) fact(t("Signs in to"), l.providers.join(t(", ")));
    main.append(facts);
    const links = el("div", "pm-links");
    const link = (label, href) => {
      if (!href) return;
      const a = el("a", "pm-link");
      a.href = href;
      a.append(el("span", "", label), glyph(OUTL, 11, 1.5));
      a.onclick = (ev) => { ev.preventDefault(); api("open", { url: href }).catch(() => {}); };
      links.append(a);
    };
    // npm is where a package's page is; a folder on this computer has none
    if (!local) link("npm", "https://www.npmjs.com/package/" + l.package);
    link(t("Source"), l.npm?.repository);
    if (l.npm?.homepage && l.npm.homepage !== l.npm.repository && !l.npm.homepage.startsWith(l.npm.repository + "#")) link(t("Homepage"), l.npm.homepage);
    main.append(links);
    if (l.replaces) main.append(el("p", "pm-note", t("magpie also signs in to this itself, for now; the plugin keeps it working if the built-in one is retired.")));
    const readme = el("div", "pm-readme");
    readme.append(el("div", "pm-rskel"), el("div", "pm-rskel short"), el("div", "pm-rskel"));
    main.append(readme);
    ed.append(main);
    const bar = el("div", "bar");
    bar.append(el("span", "note", t("Plugins are other people's code: they sign in and make the requests.")), el("span", "grow"));
    const close = el("button", "text", t("Close"));
    close.onclick = () => { stop(); closeModal(); };
    bar.append(close);
    ed.append(bar);
    openModal(ed);
    // the button follows an install or a sign-in made while it's open
    const tick = setInterval(() => { if (!ed.isConnected) return stop(); redrawAct(); }, 600);
    function stop() { clearInterval(tick); }
    try {
      const p = await api("plugins/page?name=" + encodeURIComponent(l.package));
      upd.textContent = p.updated && !p.updated.startsWith("0001") ? new Date(p.updated).toLocaleDateString(document.documentElement.lang || undefined, { year: "numeric", month: "short", day: "numeric" }) : "—";
      readme.replaceChildren(markdown(p.readme || t("No README")));
    } catch (e) {
      upd.textContent = "—";
      readme.replaceChildren(el("p", "pm-note", t(local ? "Couldn't read the folder's README: {error}" : "npm didn't answer: {error}", { error: e.message })));
    }
  }

  // markdown: a README as text, links and code — no HTML of its own, no
  // pictures (a README's badges and screenshots come from anywhere)
  function markdown(src) {
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
  }

  // opened on the Plugins tab (?view=plugins): app.js showed it before
  // this file was here to load it
  if (view === "plugins") load();
})();
