// Plugins: the market of OpenCode plugins. Discover lists the suggested
// ones and finds the rest on npm; Installed is what was added. Each part is
// drawn as it comes (#488): /api/plugins, /api/plugins/listings, then
// /api/plugins/npm; a sign-in happens in the Providers add sheet.
(() => {
  const page = $("#view-plugins");
  if (!page) return;
  window.mdRender = markdown;

  let mine = null;       // /api/plugins: what's installed
  let listings = null;   // /api/plugins/listings: the plugins suggested
  let tagged = null;     // /api/plugins/github: repositories their authors tagged magpie-plugin, as listings
  let taggedErr = "";    // why the list isn't GitHub's whole answer: it couldn't be asked, or...
  let taggedKind = "";   // its kind: "slow", "limited" or ""
  let taggedMissing = 0; // ...this many repositories it named couldn't be read
  let topic = "magpie-plugin";
  const npm = {};        // /api/plugins/npm: package → what npm says of it
  const heads = {};      // /api/plugins/git: a git plugin's spec → the commit its repository is at
  let failed = "";       // why what's installed couldn't be loaded
  let failedList = "";   // ...and the plugins suggested
  let tab = "discover", regBox;
  try { tab = localStorage.getItem("magpie.pluginTab") || tab; } catch {}
  let query = "";
  let hits = null;       // npm's answer for query: { q, list } | { q, loading } | { q, error }
  let searchTimer = 0;
  const busy = new Map(); // package → "add" | "remove" | "upgrade" | "off"
  let editing = null;    // { pkg, text, error }: a middleware's options being edited in its row
  let asking = null;     // { pkg, op }: a remove or switch-off waiting on the user's yes
  let checking = false;  // Check for updates: npm being asked now
  let checked = null;    // ...and what it said: { at, by: spec → check }
  let shown = "";

  const SEARCH = "M7 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M10 10l3 3";
  const DOWN = "M8 3v7.5 M4.8 7.6 8 10.8l3.2-3.2 M3.5 13h9";
  const STAR = "M8 2.2l1.8 3.7 4 .6-2.9 2.8.7 4L8 11.4l-3.6 1.9.7-4L2.2 6.5l4-.6z";
  const SHIELD = "M8 2.3 13 4v3.8c0 3-2.1 5.1-5 5.9-2.9-.8-5-2.9-5-5.9V4z M5.8 8l1.6 1.6 2.9-3";
  const OUTL = "M9.5 3.5h3v3 M12.5 3.5 7.5 8.5 M11 9.5v3H3.5V5h3";
  const PLUG = "M6 2v3 M10 2v3 M4.5 5h7v2.5a3.5 3.5 0 0 1-7 0z M8 11v3";
  const PIPE = "M1.5 8h3 M11.5 8h3 M4.5 4.5h7v7h-7z";
  const TERM = "M2 3.5h12v9H2z M4.5 6.5l2 1.5-2 1.5 M8 9.5h3";
  const PAGE = "M2 3.5h12v9H2z M2 6h12 M4 4.75h.01";
  const TOOL = "M10.6 2.4a3 3 0 0 0-3.4 4L2.6 11a1.2 1.2 0 0 0 1.7 1.7l4.6-4.6a3 3 0 0 0 4-3.4l-1.7 1.7-1.6-.3-.3-1.6z";
  const FOLDER = "M14.7 12.7a1.3 1.3 0 0 1-1.3 1.3H2.6a1.3 1.3 0 0 1-1.3-1.3V3.3A1.3 1.3 0 0 1 2.6 2h3.3l1.3 2h6.2a1.3 1.3 0 0 1 1.3 1.3z";

  // a folder's path is its name whole, as plugin.Name: C:\Users\me@corp\…
  // has an @ that is no version
  const name = (spec) => { if (isPath(spec)) return spec; const i = spec.lastIndexOf("@"); return i > 0 && !spec.includes("/", i) ? spec.slice(0, i) : spec; };
  // a plugin added from a folder is a folder's path, not a package name.
  // a leading \ is one on every system, as plugin.IsPath (#2229): a UNC
  // share (\\server\share\plugin) and a leading-backslash path
  const isPath = (p) => /^file:\/\//.test(p) || /^\./.test(p) || /^[/\\]/.test(p) || /^[A-Za-z]:[\\/]/.test(p);
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
  const lang = () => (document.documentElement.lang || "en").slice(0, 2);
  const summary = (l) => l.summary?.[lang()] || l.summary?.en || l.npm?.description || "";
  const count = (n) => n >= 1e6 ? (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "k" : String(n || 0);
  // a repository tagged magpie-plugin is installed by whichever spec
  // names it (github:owner/repo, its page's or a git URL), never by its
  // package's name, which another repository can give too (#3685)
  const ghRepo = (spec) => { const m = /^(?:github:|(?:git\+)?(?:https?|ssh|git):\/\/(?:[^@/\s]+@)?(?:www\.|ssh\.)?github\.com(?::\d+(?=\/))?[/:])([^/#\s]+\/[^/#\s]+?)(?:\.git)?(?:[/#].*)?$/i.exec(spec) || (isGit(spec) && /^([^:/#]+\/[^:/#]+)(?:#.*)?$/.exec(spec)); return m ? m[1].toLowerCase() : ""; };
  const entryOf = (pkg) => {
    const g = (tagged || []).find((x) => x.package === pkg)?.github;
    return mine?.plugins?.find((e) => name(e.spec) === pkg || (g && ghRepo(e.spec) === g.repo.toLowerCase()));
  };
  // npm has the versions of a package: not a folder's, nor a git one's
  const onNPM = (spec) => !isPath(spec) && !isGit(spec);
  // a git one is out of date when its repository is at another commit than
  // the one installed, whatever its version (ARNO); a short sha names the full one
  const sameCommit = (a, b) => !!a && !!b && (a.length > b.length ? a.startsWith(b) : b.startsWith(a));
  const gitBehind = (e) => isGit(e.spec) && !!e.commit && !!e.head && !sameCommit(e.commit, e.head);
  // the providers a plugin signs in to, as the add sheet knows them
  const subsOf = (pkg) => (providers?.plugins || []).filter((x) => name(x.spec) === pkg);
  // as the Add sheet names it
  const subName = (x) => SUBS.find((y) => y.agent === x.id)?.name || x.name;
  // with several, the reader picks one by name
  const signInTo = (b, outs, ev) => {
    if (outs.length === 1) return pluginSignIn(outs[0].id, ev);
    openProtoMenu(b, outs.map((x) => ({ v: x.id, name: subName(x), literalName: true, note: "" })), "",
      (id, e) => pluginSignIn(id, e), "Sign in to which subscription", "", "right");
  };
  // whether version a comes after b in semver's order, as update.Newer
  const newer = (a, b) => {
    const p = (v) => {
      const m = /^v?(\d+)\.(\d+)\.(\d+)(?:-([^+]*))?(?:\+.*)?$/.exec(String(v || "").trim());
      return m && { n: [+m[1], +m[2], +m[3]], pre: m[4] || "" };
    };
    const [x, y] = [p(a), p(b)];
    if (!x || !y) return false;
    for (let i = 0; i < 3; i++) if (x.n[i] !== y.n[i]) return x.n[i] > y.n[i];
    if (x.pre === y.pre) return false;
    if (!x.pre || !y.pre) return !x.pre;
    const [s, u] = [x.pre.split("."), y.pre.split(".")];
    for (let i = 0; i < s.length && i < u.length; i++) {
      const [d, e] = [/^\d+$/.test(s[i]), /^\d+$/.test(u[i])];
      if (d && e) { if (+s[i] !== +u[i]) return +s[i] > +u[i]; }
      else if (d !== e) return e;
      else if (s[i] !== u[i]) return s[i] > u[i];
    }
    return s.length > u.length;
  };

  function glyph(d, size = 14, stroke = 1.5) { return svg(d, size, stroke); }
  // kindChip says what a plugin is, as KINDS' words say
  const KINDS = {
    pv: [PLUG, "Provider", "Signs in to a subscription and serves its models"],
    mw: [PIPE, "Middleware", "Runs in magpie's gateway on the requests your agents send and on the replies"],
    ag: [TERM, "Agent", "Adds an agent magpie has no setup of its own for: pick its model on the Agents page and it goes through magpie"],
    tl: [TOOL, "Tools", "Gives every agent tools of its own, through magpie's MCP server"],
    pg: [PAGE, "Page", "Adds a page of its own to the sidebar. It runs shut off from the rest of magpie and can read only what its row lists"],
    pn: [PAGE, "Panels", "Adds panels to magpie's own pages. They run shut off from the rest of magpie and can read only what its row lists"],
  };
  const kindOf = (l) => ({ middleware: "mw", hooks: "mw", agent: "ag", tools: "tl", page: "pg" })[l.kind] || "pv";
  function kindChip(kind, name) {
    const [d, word, tip] = KINDS[kind];
    const c = el("span", "pm-chip kind " + kind);
    c.append(glyph(d, 10, 1.5), el("span", "", t(name || word)));
    c.title = t(tip);
    return c;
  }
  function logo(ic, big, kind) {
    const box = el("span", "pm-logo" + (big ? " big" : ""));
    if (ic) box.append(icon(ic, !big));
    else box.append(glyph({ mw: PIPE, ag: TERM, tl: TOOL, pg: PAGE }[kind] || PUZZLE, big ? 22 : 17, 1.4));
    return box;
  }

  // npm's answers, put where the cards and rows read them: on the
  // listing and the entry themselves, so a plugin's page opened before
  // npm answered has them too
  function merge() {
    for (const l of listings || []) if (npm[l.package]) l.npm = npm[l.package];
    for (const e of mine?.plugins || []) if (onNPM(e.spec) && npm[name(e.spec)]?.version) e.latest = npm[name(e.spec)].version;
    for (const e of mine?.plugins || []) if (isGit(e.spec) && heads[e.spec]) e.head = heads[e.spec];
  }
  async function loadMine() {
    try {
      mine = await api("plugins");
      failed = "";
      // a plugin turned on or off brings its page's tab, or takes it
      window.loadPluginPages?.();
    } catch (e) {
      failed = e.message;
    }
    merge();
  }
  // a repository tagged magpie-plugin as the market's cards read a
  // listing: its owner as the publisher, its version, and installed by its
  // spec (its npm name when published there from the repository, else
  // github:owner/repo); its name and summary its package.json's own (#3608)
  const ghListing = (r) => ({
    package: r.spec, name: r.name || r.repo.split("/")[1], kind: r.kind, github: r, icon: r.icon || undefined,
    summary: r.summary || r.description ? { ...(r.description && { en: r.description }), ...r.summary } : undefined,
    own: !!(r.name || r.summary),
    npm: { version: r.version || "", publisher: r.owner, repository: r.url, license: r.license, weekly: 0 },
  });
  // A failed ask is said, with why, list or not (till on Discord); a list
  // had before stands. again: Try again, which asks GitHub at once.
  async function loadTagged(again) {
    try {
      const r = await api("plugins/github" + (again ? "?again=1" : ""));
      const got = (r.repos || []).map(ghListing);
      if (!(r.error && !got.length && tagged?.length)) tagged = got;
      taggedErr = r.error || "";
      taggedKind = r.why || "";
      taggedMissing = r.missing || 0;
      if (r.topic) topic = r.topic;
    } catch (e) {
      taggedErr = e.message || String(e);
      taggedKind = "";
      taggedMissing = 0;
      tagged = tagged || [];
    }
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
  let npmTries = 0;
  async function askNPM(only) {
    const names = [...new Set([...(listings || []).map((l) => l.package), ...(mine?.plugins || []).filter((e) => onNPM(e.spec)).map((e) => name(e.spec))])]
      .filter((n) => !only || !npm[n]);
    if (!names.length) return;
    try {
      const r = await api("plugins/npm?names=" + encodeURIComponent(names.join(",")));
      Object.assign(npm, r.npm || {});
    } catch {}
    merge();
    // one npm said nothing of: unknown (Install), asked again
    let u = 0;
    for (const l of listings || []) if (!l.npm || l.npm.u) u = l.npm = { weekly: 0, u: 1 };
    if (u && npmTries++ < 4) setTimeout(() => askNPM(true), 15000);
    drawBody();
  }

  // the commit each git plugin's repository is at now: drawn when it
  // comes, never waited on (only: when one, just added, isn't known yet)
  async function askGit(only) {
    if (!(mine?.plugins || []).some((e) => isGit(e.spec) && (!only || !heads[e.spec]))) return;
    try {
      const r = await api("plugins/git");
      Object.assign(heads, r.heads || {});
    } catch { return; }
    merge();
    drawBody();
  }

  async function load() {
    if (!mine && !listings) draw();
    const parts = [
      loadMine().then(() => { draw(); window.renderPluginDot?.(); }),
      loadListings().then(draw),
      loadTagged().then(drawBody),
      loadBuiltins(),
      providers ? null : loadProviders().then(drawBody, () => {}),
    ];
    await Promise.all(parts);
    askNPM();
    askGit();
  }
  window.loadPlugins = load;
  // the sidebar's dot, followed: Plugins opens on Installed, where the
  // plugins with an update are named at the top
  window.pluginTabTo = (id) => { tab = id; };
  // pluginQuery: Discover, looking for q (the add sheet found nothing by it)
  window.pluginQuery = (q) => {
    query = q;
    tab = "discover";
    clearTimeout(searchTimer);
    hits = q.trim().length >= 2 ? { q: q.trim(), loading: true } : null;
    if (hits) searchNPM(hits.q);
    draw();
  };

  // pluginAgentsTo: Discover, at its Agents (the Agents page's More in
  // Plugins). The scroll is the reader's click's, so it is made only while
  // the click is fresh: Agents drawn later, the listings slow, is left
  // where the page is.
  let agentsFor = null; // the click that asked for Agents, till they are drawn
  window.pluginAgentsTo = (ev) => {
    query = "";
    tab = "discover";
    clearTimeout(searchTimer);
    hits = null;
    agentsFor = ev;
    draw();
  };

  async function refresh() {
    // a plugin installed, switched on or off, or removed brings its
    // page's tab, or takes it, now: not on the Plugins page's next load
    window.loadPluginPages?.();
    try {
      const [m, p] = await Promise.all([api("plugins"), api("providers")]);
      mine = m;
      providers = p;
      merge();
      // the Providers page owns the dialog as it draws: only drawn when shown
      if (view === "providers") renderProviders();
    } catch (e) { statusErr(e); }
    draw();
    window.renderPluginDot?.();
    askNPM(true);
    askGit(true);
  }

  async function act(pkg, op, body, done) {
    if (busy.size && op !== "off") return;
    busy.set(pkg, op);
    draw();
    try {
      await api("plugins/" + op, body);
      busy.delete(pkg);
      for (const e of mine?.plugins || []) if (op === "update" || e.spec === body?.spec) delete heads[e.spec]; // stale once installed (#4802)
      await refresh();
      if (done) done();
    } catch (e) {
      busy.delete(pkg);
      draw();
      statusErr(e);
    }
  }
  // Check for updates: npm asked now, not what it said within the hour,
  // for each installed plugin's newest version. Each row then says so (an
  // update, up to date, or why it couldn't be checked), and its Update, or
  // Update all, installs it as before. Nothing is installed by the check.
  async function checkUpdates() {
    if (checking || busy.size) return;
    checking = true;
    draw();
    try {
      const r = await api("plugins/check", {});
      mine = r.state || mine;
      const by = {};
      for (const c of r.plugins || []) {
        by[c.spec] = c;
        // npm's answer now is the one the rows read, and a git one's
        // repository's
        if (c.latest) npm[c.package] = { ...(npm[c.package] || {}), version: c.latest };
        if (c.head) heads[c.spec] = c.head;
      }
      checked = { at: r.at, by };
      merge();
      const asked = (r.plugins || []).filter((c) => c.status === "update" || c.status === "current" || c.status === "unknown");
      const ups = asked.filter((c) => c.status === "update" && !c.off).length;
      const unknown = asked.filter((c) => c.status === "unknown");
      if (asked.length && unknown.length === asked.length) {
        status(t("Couldn't check for updates: {error}", { error: whyText(unknown[0]) }), "err", 8000);
      } else {
        // with some not checked, "every plugin" isn't known to be up to
        // date: only the ones checked are (#1942)
        let msg = ups ? (ups === 1 ? t("1 plugin has an update") : t("{n} plugins have updates", { n: ups }))
          : unknown.length ? t("No updates for the plugins checked") : t("Every plugin is up to date");
        if (unknown.length) {
          msg += " · " + t("{n} couldn't be checked: {error}", { n: unknown.length, error: whyText(unknown[0]) });
          status(msg, "warn", 8000);
        } else status(msg, "ok");
      }
    } catch (e) {
      status(t("Couldn't check for updates: {error}", { error: e.message }), "err", 8000);
    }
    checking = false;
    draw();
    window.renderPluginDot?.();
  }
  // why npm said nothing of a plugin, in the page's words; a git one's
  // repository, in its own
  function whyText(c) {
    if (isGit(c.spec || "")) return t("Couldn't ask {repo} for its newest commit: {error}", { repo: c.spec, error: c.limited ? limitedText(c.limited) : c.error || "" });
    switch (c.why) {
      case "offline": return t("npm couldn't be reached — check your connection or proxy");
      case "limited": return t("npm is turning requests away for now (too many); try again in a few minutes");
      case "missing": return t("npm doesn't have it");
      default: return t("npm answered with an error: {error}", { error: c.error || "" });
    }
  }
  // GitHub's rate limit used up, in the page's words as the library's
  // skill check says it: when it lifts, and how a token raises it (#1942)
  function limitedText(l) {
    const d = l.until ? new Date(l.until) : null;
    const time = d && !isNaN(d) ? d.toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit" }) : "";
    if (l.token) return time ? t("GitHub's rate limit for your GitHub token is used up until {time}", { time })
      : t("GitHub's rate limit for your GitHub token is used up for now");
    return time ? t("GitHub allows 60 requests an hour without a token, used up until {time}. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.", { time })
      : t("GitHub allows 60 requests an hour without a token, used up for now. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.");
  }
  const checkedAt = () => checked ? new Date(checked.at).toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit" }) : "";

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
    // one word beside the name, as Install is: the card already says whose
    // accounts, the title says how many and what it does (a whole sentence
    // was a solid bar under the card that broke onto two lines)
    b.classList.add("get", "move");
    b.textContent = t("Move");
    b.title = (here ? t("Move {name} here", { name: c.name }) : t(c.accounts === 1 ? "Move my {n} {name} account" : "Move my {n} {name} accounts", { name: c.name, n: c.accounts }))
      + " — " + t("Runs {name} on this plugin in place of the built-in, with the same accounts; nothing changes if one doesn't work through it", { name: c.name });
    b.disabled = busy.size > 0;
    b.onclick = (ev) => { ev.stopPropagation(); move(c); };
    return b;
  };
  // the installed plugin spec's Install would replace (plugins/ holds one
  // package of a name, plugin.Add), both named by source (#3685). typed:
  // the field's, where a bare name may be a community plugin's and an
  // unread repository's package is unknown: neither is asked about.
  const sourceOf = (spec) => isGit(spec) ? spec : "npm:" + name(spec.replace(/^npm:\s*/, ""));
  const namesakeOf = (spec, typed) => {
    const r = ghRepo(spec);
    const g = (tagged || []).find((x) => x.package === spec || (r && x.github.repo.toLowerCase() === r))?.github;
    const as = g ? g.package || "" : spec.startsWith("npm:") ? name(spec.slice(4).trim()) : onNPM(spec) && (!typed || spec.startsWith("@")) ? name(spec) : "";
    const e = as && mine?.plugins?.find((x) => (isGit(x.spec) ? x.package : onNPM(x.spec) && name(x.spec)) === as);
    // no other plugin: the same repository, or npm's at another version
    if (!e || (r ? ghRepo(e.spec) === r : !isGit(spec) && !isGit(e.spec))) return null;
    return { old: sourceOf(e.spec), new: sourceOf(spec), package: as, off: !!e.off };
  };
  // false when the user keeps the plugin spec would replace
  const replaceOK = async (spec, typed) => {
    const was = namesakeOf(spec, typed);
    if (!was) return true;
    return confirmAction(t("Replace {old} with {new}?", was), was.off
      ? t("{new} and {old} are both the package {package}, and only one of them can be installed: {old} is removed, and its options and sign-ins pass to {new}. {old} is off, so {new} is installed off too.", was)
      : t("{new} and {old} are both the package {package}, and only one of them can be installed: {old} is removed, and its options and sign-ins pass to {new}.", was), t("Replace"));
  };
  const install = async (pkg, nm) => {
    if (!await replaceOK(pkg)) return;
    act(pkg, "add", { spec: pkg }, () => {
      const subs = subsOf(pkg);
      status(subs.length ? t("{name} is installed — sign in to use it", { name: nm }) : t("{name} is installed", { name: nm }), "ok");
    });
  };

  // A plugin's Update, wherever it is offered: npm has a newer version, or
  // a git one's repository a newer commit. Makes b that button and says
  // whether there is one.
  function updateButton(b, e) {
    if (!e || e.off) return false;
    const npm = e.latest && e.version && newer(e.latest, e.version);
    if (!npm && !gitBehind(e)) return false;
    const pkg = name(e.spec);
    const l = (listings || []).find((x) => x.package === pkg);
    b.classList.add("pm-act", "pm-up");
    b.type = "button";
    b.textContent = busy.get(pkg) === "upgrade" || busy.get(e.spec) === "upgrade" ? t("Updating…") : t("Update");
    b.disabled = busy.size > 0 || checking;
    if (npm) {
      b.title = t("Update to {v}", { v: "v" + e.latest });
      // the community's would come by itself: Update brings it now
      if (pkg.startsWith("@magpie-community/") && !/@(?!latest$)[^@/]+$/.test(e.spec.slice(pkg.length))) b.title += "\n" + t("magpie updates it by itself within the hour; Update does it now");
    } else b.title = t("Fetches it from {repo} again", { repo: e.spec }) + "\n" + t("{have} installed, {repo} is at {head}", { have: e.commit, repo: e.spec, head: e.head });
    b.onclick = (ev) => {
      ev.stopPropagation();
      const was = e.commit;
      act(pkg, "upgrade", { spec: e.spec }, () => {
        const now = mine?.plugins?.find((x) => x.spec === e.spec)?.commit;
        status(npm ? t("{name} updated to v{v}", { name: l?.name || pkg, v: e.latest }) : now && was && !sameCommit(now, was) ? t("{name} updated to {commit}", { name: shownName(e), commit: now }) : t("{name} is up to date", { name: shownName(e) }), "ok");
      });
    };
    b.onkeydown = (ev) => ev.stopPropagation();
    return true;
  }

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
      if (listed && !listed.github && !listed.npm?.version && !listed.npm.u) {
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
    // one of its subscriptions signed in (Qoder, beside Qoder CN; #556): the
    // card says it is, rather than a Sign in that reads as signed out; the
    // Installed row offers the others by name
    const on = subs.find((x) => x.signedIn);
    if (out && !on) {
      b.classList.add("go");
      b.textContent = t("Sign in");
      b.title = subs.length > 1 ? t("Sign in to {name}", { name: subs.map(subName).join(t(", ")) }) : "";
      b.onclick = (ev) => { ev.stopPropagation(); signInTo(b, subs, ev); };
      return b;
    }
    // an update out: the one button says so in place of Signed in or
    // Installed, the size of Install in a colour of its own, as an app
    // store's Update takes Open's place (#1587). Sign in, not done yet,
    // comes first; the Installed row offers its Update then
    if (updateButton(b, e)) return b;
    if (on) {
      b.classList.add("done");
      b.append(glyph(CHECK, 11, 2), el("span", "", t("Signed in")));
      b.title = t("Open {name} in Providers", { name: on.name });
      b.onclick = (ev) => { ev.stopPropagation(); openProvider(on.id); };
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
    if (l.github) c.classList.add("gh");
    const go = actionFor(l.package, l.name, l);
    top.append(logo(l.icon || l.npm?.icon, false, kindOf(l)), who, go);
    const sum = el("p", "pm-sum", summary(l));
    const meta = el("div", "pm-meta");
    meta.append(kindChip(kindOf(l), l.kind === "hooks" && "Request hook"));
    if (l.own) {
      const o = el("span", "pm-chip", t("Its own words"));
      o.title = t("Its name and summary are what the package says of itself in its package.json: nobody reviewed them");
      meta.append(o);
    }
    if (l.github) meta.append(stars(l.github.stars));
    if (l.npm?.weekly) {
      const d = el("span", "pm-dl");
      d.append(glyph(DOWN, 11, 1.5), el("span", "", t("{n}/week", { n: count(l.npm.weekly) })));
      d.title = t("{n} downloads on npm last week", { n: l.npm.weekly.toLocaleString(intlLang()) });
      meta.append(d);
    }
    if (l.npm?.version) {
      // an update out, and the card's button is its Update: the version it
      // goes to beside the one installed (#1587)
      const up = go.classList.contains("pm-up") && e.latest && e.version && newer(e.latest, e.version);
      meta.append(el("span", "pm-ver", "v" + (e?.version || l.npm.version) + (up ? " → v" + e.latest : "")));
    }
    if (e?.error && !e.off) meta.append(el("span", "pm-chip bad", t("Didn't load")));
    else if (e?.off) meta.append(el("span", "pm-chip", t("Off")));
    if (l.replaces) {
      const b = el("span", "pm-chip soft", t("Replaces built-in"));
      b.title = t("magpie's own sign-in to this subscription is deprecated: this plugin takes its place, and accounts signed in there move to it");
      meta.append(b);
    }
    c.append(top, sum, meta);
    c.onclick = () => detail(l);
    c.onkeydown = (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); detail(l); } };
    return c;
  }

  function stars(n) {
    const d = el("span", "pm-dl");
    d.append(glyph(STAR, 11, 1.4), el("span", "", count(n)));
    d.title = t("{n} stars on GitHub", { n: (n || 0).toLocaleString(intlLang()) });
    return d;
  }

  // cards still to come, drawn as a card is laid out (its picture, name
  // and author, two lines of summary, its facts) for a light to pass over,
  // one card after the other
  function skeleton(n) {
    const g = el("div", "pm-grid");
    g.setAttribute("aria-hidden", "true");
    const bar = (c) => el("div", "sk " + c);
    for (let i = 0; i < n; i++) {
      const c = el("div", "pm-card ghost");
      c.style.setProperty("--i", i);
      const top = el("div", "gtop"), who = el("div", "gwho");
      who.append(bar("gname"), bar("gby"));
      top.append(bar("glogo"), who, bar("gact"));
      const meta = el("div", "gmeta");
      meta.append(bar("gchip"), bar("gchip short"));
      c.append(top, bar("gline"), bar("gline short"), meta);
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

  // GitHub's section: its own kinds, an installed strip and a field, each
  // counting what the others leave; only the section redraws (#1055)
  let ghKind = "";       // "" (all) | "pv" | "mw" | "ag"
  let ghHave = "";       // "" (all) | "have" | "none"
  let ghQuery = "";
  let ghList = [];       // what the page's search left of the repositories
  const ghSec = el("section", "pm-sec pm-ghsec");
  const ghHead = el("div", "pm-sechead");
  const ghBar = el("div", "pm-ghbar");
  const ghGrid = el("div", "pm-grid");
  const ghFind = el("label", "pm-find pm-ghfind");
  const ghQ = el("input");
  ghQ.type = "search";
  ghQ.spellcheck = false;
  ghQ.autocomplete = "off";
  ghQ.oninput = () => { ghQuery = ghQ.value; drawGh(); };
  ghQ.onkeydown = (ev) => { ev.stopPropagation(); if (ev.key === "Escape" && ghQ.value) { ghQ.value = ""; ghQ.oninput(); } };
  ghFind.append(glyph(SEARCH, 13, 1.6), ghQ);
  const ghMatch = (l) => {
    const f = ghQuery.trim().toLowerCase();
    return !f || [l.name, l.github.repo, l.github.owner, l.package, summary(l)].join(" ").toLowerCase().includes(f);
  };
  const ghTitle = () => [el("h3", "", t("Unofficial, on GitHub")), el("span", "", t("repositories their authors tagged {topic} — nobody has reviewed them; read the code before you install one", { topic }))];
  // until GitHub answers, its section stands in place, saying it is looking
  let ghArrive = false;
  function ghWaiting() {
    const look = el("span", "pm-ghlook");
    look.setAttribute("role", "status");
    const dots = el("span", "dots");
    dots.setAttribute("aria-hidden", "true");
    dots.append(el("i"), el("i"), el("i"));
    look.append(dots, el("span", "", t("Looking on GitHub")));
    ghHead.replaceChildren(...ghTitle(), look);
    ghSec.setAttribute("aria-busy", "true");
    ghArrive = true;
    replaceKeeping(ghSec, [ghHead, skeleton(3)]);
    return ghSec;
  }
  // why the list isn't GitHub's whole answer; kept: cards are shown. A
  // known kind in the reader's words, the raw error as its hover
  const ghWhy = (kept) => {
    const error = { slow: t("GitHub didn't answer in time"), limited: t("GitHub is turning requests away for now") }[taggedKind] || taggedErr;
    const s = el("span", "", taggedMissing
      ? t(kept ? "{n} more on GitHub couldn't be read: {error}" : "GitHub found {n} repositories, but none of them could be read: {error}", { n: taggedMissing, error })
      : t(kept ? "Couldn't ask GitHub, so these are from before: {error}" : "Couldn't ask GitHub: {error}", { error }));
    if (error !== taggedErr) s.title = taggedErr;
    return s;
  };
  // and one GitHub couldn't be asked of says so, with another go
  function ghFailed() {
    ghHead.replaceChildren(...ghTitle());
    ghSec.removeAttribute("aria-busy");
    const box = el("div", "pm-ghnone");
    const again = el("button", "text", t("Try again"));
    again.type = "button";
    again.onclick = () => {
      tagged = null;
      taggedErr = "";
      taggedMissing = 0;
      drawBody();
      loadTagged(true).then(drawBody);
    };
    box.append(ghWhy(false), again);
    replaceKeeping(ghSec, [ghHead, box]);
    return ghSec;
  }
  // the cards GitHub could give, and a line saying what it couldn't, with
  // another go that keeps them until it answers
  function ghNote() {
    const note = el("div", "pm-ghwarn");
    note.setAttribute("role", "status");
    const again = el("button", "text", t("Try again"));
    again.type = "button";
    again.onclick = () => {
      again.disabled = true;
      loadTagged(true).then(drawBody);
    };
    note.append(ghWhy(true), again);
    return note;
  }
  function ghSection(list) {
    ghList = list;
    ghHead.replaceChildren(...ghTitle());
    ghSec.removeAttribute("aria-busy");
    drawGh();
    // the first cards after the wait come in, one after the other; the
    // grid drawn again for a pick doesn't
    if (ghArrive) {
      ghArrive = false;
      ghSec.classList.add("pm-arrive");
      [...ghGrid.children].forEach((c, i) => c.style.setProperty("--i", Math.min(i, 9)));
      setTimeout(() => ghSec.classList.remove("pm-arrive"), 900);
    }
    return ghSec;
  }
  function drawGh() {
    const head = taggedErr ? [ghHead, ghNote()] : [ghHead];
    if ((tagged || []).length <= 6) {
      ghGrid.replaceChildren(...ghList.map(card));
      replaceKeeping(ghSec, [...head, ghGrid]);
      return;
    }
    // the kinds there are, in the market's order, each with how many the
    // field leaves of it; one kind alone needs no picking
    const found = ghList.filter(ghMatch);
    const kinds = ["pv", "mw", "ag", "tl", "pg"].filter((k) => ghList.some((l) => kindOf(l) === k));
    if (!kinds.includes(ghKind)) ghKind = "";
    // which of them are here, asked once each: the counts below would ask
    // it again for every pick in both strips. Like a kind alone, a list
    // that is all installed or all not has nothing to pick out, and a pick
    // that can no longer be seen doesn't go on narrowing the grid.
    const here = new Map(ghList.map((l) => [l, !!entryOf(l.package)]));
    const have = (l) => here.get(l);
    const both = ghList.some(have) && ghList.some((l) => !have(l));
    if (!both) ghHave = "";
    // the field and the other strip narrow it, so each count is what a
    // pick in this strip would leave; the kind's own pick is the one left
    // out, or every count but All would be the picked kind's alone
    const n = (k, h) => found.filter((l) => (!k || kindOf(l) === k) && (!h || have(l) === (h === "have"))).length;
    const bar = [];
    if (kinds.length > 1) {
      const pick = segs([["", t("All") + " · " + n("", ghHave)], ...kinds.map((k) => [k, t(KINDS[k][1]) + " · " + n(k, ghHave)])], ghKind, (k) => { ghKind = k; drawGh(); });
      pick.classList.add("pm-ghkinds");
      pick.setAttribute("role", "group");
      pick.setAttribute("aria-label", t("Kind of plugin"));
      bar.push(pick);
    }
    // what's installed, on both sides of it: a reader looks for what to
    // add as much as for what they have
    if (both) {
      const pick = segs([["", t("All")], ["have", t("Installed") + " · " + n(ghKind, "have")], ["none", t("Not installed") + " · " + n(ghKind, "none")]], ghHave, (h) => { ghHave = h; drawGh(); });
      pick.classList.add("pm-ghhave");
      pick.setAttribute("role", "group");
      pick.setAttribute("aria-label", t("Installed or not"));
      bar.push(pick);
    }
    bar.push(el("span", "grow"), ghFind);
    ghQ.placeholder = t("Filter by name, author or description");
    ghQ.setAttribute("aria-label", t("Filter the plugins on GitHub"));
    if (ghQ.value !== ghQuery) ghQ.value = ghQuery;
    replaceKeeping(ghBar, bar);
    const shown = found.filter((l) => (!ghKind || kindOf(l) === ghKind) && (!ghHave || have(l) === (ghHave === "have")));
    if (shown.length) {
      ghGrid.replaceChildren(...shown.map(card));
      replaceKeeping(ghSec, [...head, ghBar, ghGrid]);
      return;
    }
    const none = el("div", "pm-ghnone");
    const all = el("button", "text", t("Show all"));
    all.type = "button";
    all.onclick = () => { ghKind = ""; ghHave = ""; ghQuery = ""; drawGh(); ghQ.focus(); };
    none.append(el("span", "", t("No plugin on GitHub matches")), all);
    replaceKeeping(ghSec, [...head, ghBar, none]);
  }

  // the head and its search field are made once and drawn again in
  // place: the field is never taken out of the page, or a key an IME
  // commits while it is out lands twice (#1055)
  const headBox = el("div", "lib-head pm-head");
  const find = el("label", "pm-find");
  const q = el("input");
  q.type = "search";
  q.spellcheck = false;
  q.autocomplete = "off";
  q.oninput = () => {
    query = q.value;
    if (tab !== "discover") { tab = "discover"; draw(); }
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
  find.append(glyph(SEARCH, 13, 1.6), q);

  function head() {
    const n = mine?.plugins?.length || 0;
    const tabs = segs([["builtin", t("Built-in")], ["discover", t("Discover")], ["installed", t("Installed") + (n ? " · " + n : "")]], tab, (id) => {
      tab = id;
      try { localStorage.setItem("magpie.pluginTab", id); } catch {}
      searchShown();
      drawBody();
    });
    tabs.classList.add("lib-tabs");
    tabs.querySelectorAll(".opt")[2].dataset.sub = "installed";
    q.placeholder = t("Search plugins and npm…");
    if (q.value !== query) q.value = query;
    q.setAttribute("aria-label", t("Search plugins"));
    mirror = mirrorSwitch();
    replaceKeeping(headBox, [tabs, el("span", "grow"), mirror, find]);
    searchShown();
    headBox.classList.toggle("stuck", page.scrollTop > 0);
    return headBox;
  }

  // 「国内镜像」: the list, npm's packages and answers, and Bun asked of
  // mirrors in China first (npmmirror, jsDelivr); none before what's
  // installed has come, which says how it is set
  let mirrorSaving = false;
  // the built-in plugins are a short list of switches: nothing to search
  // or fetch through a mirror
  let mirror = null;
  function searchShown() {
    find.hidden = tab === "builtin";
    if (mirror) mirror.hidden = !mine || tab === "builtin";
  }
  function mirrorSwitch() {
    const on = !!mine?.mirror;
    const b = el("button", "pm-mirror" + (on ? " on" : ""));
    b.type = "button";
    b.hidden = !mine;
    b.disabled = mirrorSaving;
    b.setAttribute("role", "switch");
    b.setAttribute("aria-checked", on ? "true" : "false");
    b.setAttribute("aria-label", t("Mirrors in China"));
    b.title = t("Download the plugin list, plugins and Bun from mirrors in China first (npmmirror, jsDelivr), their official addresses after. Each is a copy of the very file, checked as the original is");
    const sw = el("span", "lib-switch" + (on ? " on" : ""));
    sw.append(el("i"));
    b.append(el("span", "", t("Mirrors in China")), sw);
    b.onclick = async () => {
      if (mirrorSaving || !mine) return;
      mirrorSaving = true;
      mine.mirror = !on;
      draw();
      try {
        const r = await api("plugins/mirror", { on: !on });
        mine.mirror = r.mirror;
        // the list again, now from the mirror, and npm's answers with it
        if (r.mirror) loadListings().then(() => { draw(); askNPM(); });
      } catch (e) {
        mine.mirror = on;
        statusTErr(e);
      }
      mirrorSaving = false;
      draw();
    };
    return b;
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
    // the body is made once, so the GitHub section's field stays in the page
    body = body || el("div", "lib-body pm-body");
    replaceKeeping(page, [head(), body]);
    drawBody();
    page.scrollTop = scroll;
  }

  function drawBody() {
    if (!body) return;
    const key = tab + (query ? ":q" : "");
    body.classList.toggle("enter", shown !== key);
    shown = key;
    // the search field keeps its focus: only the body is redrawn while
    // typing, and the GitHub section's field is kept in it
    if (tab === "installed" || tab === "builtin" || !listings) body.replaceChildren();
    if (tab === "installed") return drawInstalled();
    if (tab === "builtin") return drawBuiltin();
    if (failedList && !listings) {
      body.append(el("p", "pm-empty", t("Couldn't load the plugins: {error}", { error: failedList })));
      return;
    }
    if (!listings) {
      body.append(intro(), skeleton(6));
      return;
    }
    const f = query.trim().toLowerCase();
    const match = (l) => !f || [l.name, l.package, summary(l), (l.providers || []).join(" "), l.npm?.publisher || ""].join(" ").toLowerCase().includes(f);
    const ls = listings.filter(match);
    // a tagged repository the market already lists is the market's card
    const listed = new Set(listings.map((l) => l.package));
    const gh = (tagged || []).filter((l) => !(l.github.package && listed.has(l.github.package)) && match(l));
    // put in place around the GitHub section, which stays in the page
    const out = [];
    if (!f) {
      out.push(intro());
      // magpie's community's alone: others' plugins are found by a search
      const ours = ls.filter((l) => l.community && kindOf(l) === "pv");
      const mws = ls.filter((l) => l.community && kindOf(l) === "mw");
      const ags = ls.filter((l) => l.community && kindOf(l) === "ag");
      const tls = ls.filter((l) => l.community && kindOf(l) === "tl");
      const pgs = ls.filter((l) => l.community && kindOf(l) === "pg");
      if (ours.length) out.push(section(t("Subscriptions"), t("written for magpie, checked against its own sign-ins"), ours));
      if (mws.length) out.push(section(t("Gateway middleware"), t("runs in magpie's gateway on what every agent sends and gets back, whichever provider serves it"), mws));
      const agSec = ags.length ? section(t("Agents"), t("agents magpie has no setup of its own for, connected to it as its own are"), ags) : null;
      if (agSec) { agSec.classList.add("pm-agents"); out.push(agSec); }
      if (tls.length) out.push(section(t("Tools for agents"), t("tools every agent can call, through magpie's MCP server"), tls));
      if (pgs.length) out.push(section(t("Pages"), t("pages of their own in magpie's window, each shut off from the rest and reading only what it lists"), pgs));
      if (gh.length) out.push(ghSection(gh));
      else if (!tagged) out.push(ghWaiting());
      else if (taggedErr && !tagged.length) out.push(ghFailed());
      out.push(manual());
      if (mine?.registry) {
        out.push(regBox = regBox || el("div", "pm-registry"));
        import("./registry.js").then(() => pmRegistry(regBox, mine, askNPM), statusTErr);
      }
      replaceKeeping(body, out);
      if (agentsFor && agSec) {
        // after the view is put back where the reader was, as showing it
        // does; the section then is the one drawn last (npm's answers draw
        // it again)
        if (window.scrollOnPurpose?.(agentsFor)) requestAnimationFrame(() => { body.querySelector(".pm-agents")?.scrollIntoView({ block: "start" }); window.arrived?.(page); });
        agentsFor = null;
      }
      return;
    }
    if (ls.length) out.push(section(t("Suggested"), "", ls));
    if (gh.length) out.push(ghSection(gh));
    else if (!tagged) out.push(ghWaiting());
    else if (taggedErr && !tagged.length) out.push(ghFailed());
    // npm's card goes where a card above is its package (a listing, or a
    // tagged repository npm has), not where a repository's package.json
    // only names it (#3685)
    const known = new Set([...listings.map((l) => l.package), ...(tagged || []).map((l) => l.package)]);
    const box = el("section", "pm-sec");
    const h = el("div", "pm-sechead");
    h.append(el("h3", "", t("On npm")), el("span", "", t("OpenCode plugins and pi packages anyone published — read what one does before you install it")));
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
    out.push(box);
    replaceKeeping(body, out);
  }

  function intro() {
    const box = el("div", "pm-intro");
    const text = el("div", "pm-introtext");
    text.append(el("h2", "", t("Plugins extend magpie")));
    text.append(el("p", "", t("Plugins sign in to coding plans and make their requests; the models then work in every agent, like any provider's. They're OpenCode's provider plugins or pi's packages, run on Bun.")));
    const mw = el("p", "");
    mw.append(document.createTextNode(t("A plugin can also run in magpie's gateway on what agents send and get back, pick which account a routing asks, give every agent tools, connect an agent, or add a page of its own.") + " "));
    // the docs in the reader's language: the site has en, zh and ja, and
    // Chinese is nearer a zh-TW reader than English is
    const how = el("a", "pm-link pm-write", "");
    how.href = "https://usemagpie.ai/docs/" + ({ zh: "zh/", "zh-TW": "zh/", ja: "ja/" }[lang()] || "") + "plugins";
    how.append(el("span", "", t("Write a plugin")), glyph(OUTL, 11, 1.5));
    how.onclick = (ev) => { ev.preventDefault(); api("open", { url: how.href }).catch(() => {}); };
    mw.append(how);
    text.append(mw);
    const trust = el("p", "pm-trust");
    trust.append(glyph(SHIELD, 12, 1.5), el("span", "", t("A plugin is someone else's code with your sign-in or your requests: install the ones you trust.")));
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
    box.onsubmit = async (ev) => {
      ev.preventDefault();
      const s = spec.value.trim();
      if (!s || busy.size || !await replaceOK(s, true)) return;
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
        statusErr(e);
      } finally {
        b.disabled = busy.size > 0 || spec.disabled;
      }
    };
    return b;
  }

  // Built-in: magpie's own plugins (Sessions, Library, routing groups…),
  // each with a switch (renderBuiltins in app.js), read afresh each time
  // the tab is drawn
  let builtins = null, builtinsFailed = "";
  async function loadBuiltins() {
    try {
      builtins = await api("plugins/builtin");
      builtinsFailed = "";
    } catch (e) {
      builtinsFailed = e.message;
    }
    if (tab === "builtin") drawBody();
  }
  function drawBuiltin() {
    const say = el("p", "feat-say", t("Parts of magpie not everyone needs. Switched off, a part leaves the sidebar, its pages and Settings; magpie works the same either way."));
    const list = el("div", "list prefs");
    list.id = "builtinList";
    body.append(say, list);
    if (builtinsFailed && !builtins) list.append(el("p", "pm-empty", t(builtinsFailed)));
    else if (builtins) window.renderBuiltins(list, builtins, (next) => { builtins = next; drawBody(); });
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
      none.append(logo("", true), el("b", "", t("No plugins yet")), el("p", "", t("Install a subscription or a middleware from Discover; a subscription signs in from here.")));
      const b = el("button", "pm-act get", t("Discover plugins"));
      b.onclick = () => { tab = "discover"; draw(); };
      none.append(b);
      body.append(none);
      return;
    }
    // what the sidebar's dot is about, said where it's acted on: which
    // plugins have a newer version, and one press to update them all
    const outdated = es.filter((e) => (e.latest && e.version && newer(e.latest, e.version)) || (gitBehind(e) && !e.off));
    if (outdated.length) {
      const bar = el("div", "pm-updates");
      const names = outdated.map(shownName).join(t(", "));
      bar.append(el("span", "pm-dot"), el("span", "grow", outdated.length === 1
        ? t("A newer version of {name} is out", { name: names })
        : t("Newer versions of {n} plugins are out: {names}", { n: outdated.length, names })));
      const up = el("button", "pm-act pm-up", busy.has("*") ? t("Updating…") : t("Update all ({n})", { n: outdated.length }));
      up.type = "button";
      up.disabled = busy.size > 0 || checking;
      up.onclick = () => act("*", "update", {}, () => status(t("Plugins updated"), "ok"));
      bar.append(up);
      body.append(bar);
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
    foot.append(el("span", "", mine.bun ? t("Plugins run on Bun {v}", { v: mine.bunVersion }) : ""), el("span", "grow"));
    // npm has versions to ask for, and a git one's repository commits; a
    // folder has none
    if (es.some((e) => onNPM(e.spec) || isGit(e.spec))) {
      const c = el("button", "text pm-check" + (checking ? " busy" : ""));
      if (checking) c.append(el("span", "spin"));
      c.append(el("span", "", checking ? t("Checking…") : t("Check for updates")));
      c.title = t("Ask npm now for each plugin's newest version") + (checked ? "\n" + t("Last checked {time}", { time: checkedAt() }) : "");
      c.disabled = checking || busy.size > 0;
      c.onclick = () => checkUpdates();
      foot.append(c);
    }
    body.append(foot);
  }

  // an installed plugin's name as its row shows it: the market's, else the
  // one its package.json gives itself (magpie.name, #4558), else the
  // package's or the folder's
  function shownName(e) {
    const pkg = name(e.spec);
    return (listings || []).find((x) => x.package === pkg)?.name || e.ownName || (isGit(e.spec) && e.package) || label(e.spec);
  }

  function installedRow(e) {
    const pkg = name(e.spec);
    const l = (listings || []).find((x) => x.package === pkg);
    const r = el("div", "row pm-row" + (e.off ? " off" : ""));
    const who = el("div", "who");
    const nm = el("div", "name");
    nm.append(el("span", "", shownName(e)));
    if (e.version) nm.append(el("span", "pm-ver", "v" + e.version));
    // a git one's commit says which of its repository's it is
    if (isGit(e.spec) && e.commit) {
      const c = el("span", "pm-ver", e.commit);
      c.title = t("The commit of {repo} installed", { repo: e.spec });
      nm.append(c);
    }
    const ck = checked?.by[e.spec];
    if (gitBehind(e)) {
      const c = el("span", "pm-chip up", t("New commits"));
      c.title = t("{have} installed, {repo} is at {head}", { have: e.commit, repo: e.spec, head: e.head });
      nm.append(c);
    } else if (e.latest && e.version && newer(e.latest, e.version)) {
      const c = el("span", "pm-chip up", t("v{v} out", { v: e.latest }));
      c.title = t("v{have} installed, v{v} on npm", { have: e.version, v: e.latest });
      nm.append(c);
    } else if (ck?.status === "unknown") {
      // asked just now, and npm didn't say: the row says why
      const c = el("span", "pm-chip warn");
      c.append(el("span", "dot"), el("span", "", t("Couldn't check")));
      c.title = whyText(ck);
      nm.append(c);
    } else if (e.autoUpdated && e.autoUpdated.to === e.version) {
      // magpie updated it by itself lately: the row says so, quietly
      const c = el("span", "pm-chip soft", t("Auto-updated"));
      c.title = t("magpie updated it from v{from} to v{to} on {date}", { from: e.autoUpdated.from, to: e.autoUpdated.to, date: new Date(e.autoUpdated.at).toLocaleDateString(intlLang(), { month: "short", day: "numeric" }) });
      nm.append(c);
    } else if ((ck?.status === "current" || ck?.status === "update") && e.version) {
      const c = el("span", "pm-chip soft", t("Up to date"));
      c.title = (isGit(e.spec) ? t("{head} is the newest commit of {repo}", { head: e.head || ck.head || "", repo: e.spec }) : t("v{v} is the newest on npm", { v: e.version })) + "\n" + t("Last checked {time}", { time: checkedAt() });
      nm.append(c);
    }
    // built-ins moved onto it, then its other providers if 2+ (#3902)
    const moved = (e.moved || []).map((id) => SUBS.find((x) => x.agent === id)?.name || id);
    const names = moved.join(t(", "));
    const all = [...moved, ...e.serves || []];
    if (all[0] && !e.off) {
      const c = el("span", "pm-chip moved");
      c.append(el("span", "dot"), el("span", "", t("Serves {names}", { names: all.join(t(", ")) })));
      if (names) c.title = t("{names} runs on this plugin in place of the built-in", { names });
      nm.append(c);
    }
    // gateway middleware runs in magpie's gateway, not as a subscription:
    // the row says so, and how it is doing, on a line of its own
    const mw = e.middleware;
    const gw = e.gateway;
    const ag = e.agent;
    // a provider's or agent's options (plugins#592)
    const ownOpts = !e.isMiddleware && !e.isGateway && !!(e.options || e.optionsExample);
    if (mw || e.isMiddleware) nm.append(kindChip("mw"));
    if (gw || e.isGateway) nm.append(kindChip("mw", "Request hook"));
    if (ag || e.isAgent) nm.append(kindChip("ag"));
    // a plugin of tools alone signs in to nothing: no Provider chip
    const toolsOnly = !!e.tools?.length && !e.providers?.length;
    if (e.tools?.length) nm.append(kindChip("tl"));
    if (e.page) nm.append(kindChip("pg"));
    if (e.panels?.length) nm.append(kindChip("pn"));
    if (e.providers?.length || !(e.gatewayOnly || e.middlewareOnly || e.inMagpieOnly || e.isAgent || toolsOnly || (mw && !e.isMiddleware) || (ag && !e.isAgent))) nm.append(kindChip("pv"));
    who.append(nm);
    const subs = subsOf(pkg);
    const sub = el("div", "sub");
    const cands = e.off ? [] : movableOf(pkg);
    // a provider another installed plugin signs in to as well (neiko on
    // Discord: a third-party plugin and their own one both name it). One
    // of them serves it; the row says which, and the other gets a way to
    // take it over, rather than the provider just going missing
    const clashes = e.off || e.error ? [] : e.clashes || [];
    const nameOfSpec = (spec) => { const o = (mine?.plugins || []).find((x) => x.spec === spec); return o ? shownName(o) : label(spec); };
    const ask = asking?.pkg === pkg && !busy.size && (asking.op === "move" ? cands.length : moved.length) ? asking.op : "";
    if (ask === "move") {
      sub.textContent = t("{names} can run on it again, with the same accounts.", { names: cands.map((c) => c.name).join(t(", ")) });
      sub.classList.add("ask");
    } else if (ask) {
      sub.textContent = t(ask === "remove" ? "{names} stops working until the plugin is installed again. Its accounts are kept." : "{names} stops working while the plugin is off. Its accounts are kept.", { names });
      sub.classList.add("ask");
    } else if (e.off) sub.textContent = t("Off");
    else if (e.error) { sub.textContent = t("Didn't load: {error}", { error: e.error }); sub.classList.add("bad"); sub.title = e.error; }
    else if (subs.length) {
      for (const [i, x] of subs.entries()) {
        if (i) sub.append(el("span", "sep", " · "));
        const s = el("span", "pm-sub" + (x.signedIn ? " in" : ""));
        // its built-in runs it, with its accounts: nothing to fix here
        const builtin = !x.signedIn && cands.some((c) => c.id === x.pid);
        const nx = subName(x);
        s.append(el("span", "dot"), el("span", "", x.signedIn ? t("{name}: signed in", { name: nx }) : builtin ? t("{name}: on magpie's built-in", { name: nx }) : t("{name}: not signed in", { name: nx })));
        if (builtin) s.title = t("{name} runs on magpie's built-in, with your accounts; Move {name} here runs it on this plugin", { name: nx });
        else if (!x.signedIn && subs.some((y) => y.signedIn)) s.title = t("{name} is a subscription of its own; {other} works without it", { name: nx, other: subName(subs.find((y) => y.signedIn)) });
        sub.append(s);
      }
    } else if ((mw || ag || gw || e.isGateway || toolsOnly || (e.page || e.panels?.length) && e.inMagpieOnly) && !e.providers.length) {
      // only middleware, a request hook, an agent, tools, a page or panels: its own line says what it does
    } else if (clashes.length && !e.providers.length) {
      // its own line below says whose it is
    } else if (e.providers.length) sub.textContent = t("Signs in to {names}", { names: e.providers.join(t(", ")) });
    // an empty list only means "nothing" once the plugins have answered: a
    // plugin installed a moment ago has no names yet, and the page says
    // nothing rather than claiming here that it signs in to nothing magpie
    // can use, which isn't known until the answer comes (#1112)
    else if (e.named) sub.textContent = t("Signs in to nothing magpie can use");
    if (sub.textContent) who.append(sub);
    if (!ask) for (const g of clashGroups(e, clashes)) who.append(clashLine(e, g, nameOfSpec));
    if (mw && !e.off && !ask) who.append(mwLine(mw));
    if (gw && !e.off && !ask) who.append(gatewayLine(gw));
    if (ag && !e.off && !ask) who.append(agentLine(ag));
    if (e.tools?.length && !e.off && !ask) who.append(toolsLine(e.tools, e));
    // off too: what its page may read is said before it is turned on
    if (e.page && !ask) who.append(pageLine(e.page));
    if (e.panels?.length && !ask) who.append(panelsLine(e.panels, e.panelReads));
    if (e.actions?.length && !ask) who.append(actionsLine(e.actions));
    if ((mw || e.isGateway || ownOpts) && !e.off && !ask && editing?.pkg === pkg) who.append(optionsEditor(e));
    // the plugin's own picture (package.json's magpie.icon) before its
    // first provider's, which a plugin of several gives each its own (#1775)
    r.append(logo(l?.icon || l?.npm?.icon || e.icon || subs[0]?.icon || ag?.icon, false, e.middlewareOnly ? "mw" : e.isAgent && !e.isMiddleware && !e.providers.length ? "ag" : e.inMagpieOnly && (e.page || e.panels?.length) && !e.isAgent && !e.isMiddleware ? "pg" : toolsOnly && !e.page && !mw && !ag ? "tl" : ""), who);
    const val = el("div", "val");
    const b = busy.get(pkg) || busy.get(e.spec);
    // npm's newer version, or a git one's repository at a newer commit
    // than the one installed (Update shown all the time before, and still
    // there right after an update): the same Update as a card's (#1587)
    const up = el("button");
    if (updateButton(up, e)) val.append(up);
    const here = !e.off && !e.error && !subs.some((x) => x.signedIn) && cands[0];
    if (here && !ask) {
      const mv = el("button", "text primary", b === "move" ? t("Moving…") : t("Move {name} here", { name: here.name }));
      mv.title = t("Runs {name} on this plugin in place of the built-in, with the same accounts; nothing changes if one doesn't work through it", { name: here.name });
      mv.disabled = busy.size > 0;
      mv.onclick = () => move(here);
      val.append(mv);
    }
    for (const c of ask ? [] : clashes.filter((c) => c.by !== e.spec)) {
      const u = el("button", "text", b === "prefer" ? t("Switching…") : t("Use for {name}", { name: c.name }));
      u.title = t("{name} runs on this plugin instead of {other}; both stay installed", { name: c.name, other: nameOfSpec(c.by) });
      u.disabled = busy.size > 0;
      u.onclick = () => act(pkg, "prefer", { spec: e.spec, provider: c.id }, () => status(t("{name} now runs on {plugin}", { name: c.name, plugin: shownName(e) }), "ok"));
      val.append(u);
    }
    if (!e.off && !e.error && subs.some((x) => !x.signedIn)) {
      // signed in to one of its subscriptions already (Qoder, beside Qoder
      // CN; #556): a blue Sign in read as the plugin signed out, so the
      // button names the one it signs in to and stays quiet
      const outs = subs.filter((x) => !x.signedIn), some = subs.some((x) => x.signedIn);
      const s = el("button", "text" + (here || some ? "" : " primary"), !some ? t("Sign in") : outs.length > 1 ? t("Sign in to another…") : t("Sign in to {name}", { name: subName(outs[0]) }));
      if (outs.length > 1) s.setAttribute("aria-haspopup", "menu");
      s.onclick = (ev) => { ev.stopPropagation(); signInTo(s, outs, ev); };
      val.append(s);
    }
    // switched on again: a built-in it can carry is offered its way, in
    // the row
    const again = () => { if (movableOf(pkg).length) { asking = { pkg, op: "move" }; draw(); } };
    const off = () => act(pkg, "off", { spec: e.spec, off: !e.off }, e.off ? again : null);
    const remove = () => act(pkg, "remove", { spec: e.spec }, () => status(t("{name} removed", { name: l?.name || pkg }), "ok"));
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
    if ((e.isGateway || (mw && !mw.error) || ownOpts) && !e.off && editing?.pkg !== pkg) {
      const o = el("button", "text", t("Options"));
      o.title = e.options ? t("The options it runs with") : t("It runs with no options; its package suggests some");
      o.disabled = !!b;
      o.onclick = () => {
        editing = { pkg, text: JSON.stringify(e.options || e.optionsExample || {}, null, 2), error: "" };
        draw();
        // the field the click opened takes the keys, where it is
        page.querySelector(".pm-opts textarea")?.focus(NO_SCROLL);
      };
      val.append(o);
    }
    const onoff = el("button", "text", t(e.off ? "Switch on" : "Switch off"));
    onoff.disabled = !!b;
    onoff.onclick = () => { if (!e.off && moved.length) { asking = { pkg, op: "off" }; draw(); } else off(); };
    const rm = el("button", "text quiet", b === "remove" ? t("Removing…") : t("Remove"));
    rm.title = moved.length ? t("{names} runs on this plugin: removed, it stops working until the plugin is installed again", { names }) : t("Removes the plugin and what it installed; its sign-ins are kept until you sign out");
    rm.disabled = busy.size > 0;
    rm.onclick = async () => {
      if (moved.length) {
        asking = { pkg, op: "remove" };
        draw();
        return;
      }
      if (await confirmRemoval(l?.name || pkg, rm.title)) remove();
    };
    val.append(onoff, rm);
    r.append(val);
    r.onclick = (ev) => { if (!ev.target.closest("button, .pm-opts")) detail(l || (isGit(e.spec) ? { package: e.spec, name: shownName(e), icon: e.icon, npm: { version: e.version, repository: gitWeb(e.spec) } } : { package: pkg, name: shownName(e), icon: e.icon, npm: { version: e.latest } })); };
    return r;
  }

  // optionsEditor is a plugin's options as JSON, in its row: what it
  // runs with, or what its package suggests when it has none. The draft is
  // kept in editing, so a redraw while typing doesn't lose it.
  function optionsEditor(e) {
    const box = el("div", "pm-opts");
    const ta = document.createElement("textarea");
    ta.setAttribute("aria-label", t("Options"));
    ta.setAttribute("aria-describedby", "pluginOptionsNote pluginOptionsError");
    ta.setAttribute("aria-invalid", String(!!editing.error));
    ta.spellcheck = false;
    ta.value = editing.text;
    ta.rows = Math.min(14, Math.max(4, editing.text.split("\n").length + 1));
    ta.oninput = () => { editing.text = ta.value; };
    ta.onkeydown = (ev) => {
      if (ev.key === "Escape") { editing = null; draw(); }
      if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) { ev.preventDefault(); save(); }
    };
    const err = el("div", "pm-opts-err", editing.error);
    err.id = "pluginOptionsError";
    err.setAttribute("role", "alert");
    const bar = el("div", "pm-opts-bar");
    const note = el("span", "", e.options ? t("Applied to the next request") : t("Not set: these are its package's example"));
    note.id = "pluginOptionsNote";
    const save = () => {
      let v;
      try { v = JSON.parse(editing.text || "{}"); } catch (x) { editing.error = t("Not JSON: {error}", { error: x.message }); return draw(); }
      if (!v || typeof v !== "object" || Array.isArray(v)) { editing.error = t("Options are a JSON object"); return draw(); }
      editing = null;
      act(name(e.spec), "options", { spec: e.spec, options: v }, () => status(t("{name}'s options saved", { name: shownName(e) }), "ok"));
    };
    const ok = el("button", "text primary", t("Save"));
    ok.onclick = save;
    const no = el("button", "text", t("Cancel"));
    no.onclick = () => { editing = null; draw(); };
    bar.append(note);
    if (e.options) {
      const clr = el("button", "text quiet", t("Clear"));
      clr.title = t("Runs it with no options");
      clr.onclick = async () => {
        if (!await confirmAction(t("Clear"), t("Saved options will be removed. The plugin will run with no options."), t("Clear"))) return;
        editing = null;
        act(name(e.spec), "options", { spec: e.spec, options: null }, () => status(t("{name}'s options cleared", { name: shownName(e) }), "ok"));
      };
      bar.append(clr);
    }
    bar.append(no, ok);
    box.append(ta, err, bar);
    return box;
  }

  // clashGroups are a row's clashes, one per other plugin and side: a
  // plugin that signs in to two providers another one does too is one
  // line naming both, not a line each saying the same thing (#565)
  function clashGroups(e, clashes) {
    const out = [];
    for (const c of clashes) {
      const here = c.by === e.spec;
      const others = here ? c.with : [c.by];
      const g = out.find((x) => x.here === here && x.others.join("\n") === others.join("\n"));
      if (g) g.names.push(c.name);
      else out.push({ here, others, names: [c.name] });
    }
    return out;
  }

  // clashLine is the providers two installed plugins both sign in to, as
  // either row says it. The other one goes by its name, or by where it
  // came from when that is this row's name too (one plugin installed
  // twice: it read as the plugin naming itself, #565)
  function clashLine(e, g, nameOfSpec) {
    const d = el("div", "sub pm-clash" + (g.here ? "" : " warn"));
    const own = shownName(e);
    const copies = g.others.filter((s) => nameOfSpec(s) === own);
    const names = g.names.join(t(", "));
    const many = g.names.length > 1;
    let text;
    if (copies.length && copies.length === g.others.length) {
      const spec = copies.join(t(", "));
      text = g.here
        ? t("Another copy of this plugin ({spec}) signs in to {names} too; this copy serves them", { spec, names })
        : t("Another copy of this plugin ({spec}) serves {names}", { spec, names });
    } else {
      const other = g.others.map(nameOfSpec).join(t(", "));
      text = g.here
        ? many ? t("{other} signs in to {names} too; this one serves them", { other, names }) : t("{other} signs in to {name} too; this one serves it", { other, name: names })
        : many ? t("{names} are served by {other}, which signs in to them too", { other, names }) : t("{name} is served by {other}, which signs in to it too", { other, name: names });
    }
    d.append(el("span", "dot"), el("span", "", text));
    d.title = g.names.map((name) => g.here
      ? t("Two plugins sign in to {name}; magpie runs it on one. Use for {name} on the other one's row switches it", { name })
      : t("Two plugins sign in to {name}; magpie runs it on one. Use for {name} switches it to this one, or remove the one you don't want", { name })).join("\n");
    return d;
  }

  // agentLine is the agent a plugin adds, as the Agents page lists it, or
  // why it isn't there
  function agentLine(a) {
    const d = el("div", "sub pm-mw");
    if (a.error) {
      d.textContent = t("Agent didn't load: {error}", { error: a.error });
      d.classList.add("bad");
      d.title = a.error;
      return d;
    }
    // not found here, so Agents has no row (zzZ): say so, and the plugin's notice
    if (a.missing) {
      d.classList.add("pm-clash");
      d.textContent = d.title = [t("{name} isn't in Agents: magpie doesn't find it in {path}", { name: a.name, path: a.looks || "~" }), a.notice].filter(Boolean).join(" ");
      return d;
    }
    d.textContent = d.title = t("Adds {name} to Agents", { name: a.name });
    return d;
  }

  // toolsLine is the tools a plugin gives, which magpie's plugin tools MCP
  // server offers every agent: each by the name an agent sees, its
  // description on hover, and the server's address to copy into an agent
  // and, with its Library server (toolsServer), a switch for every agent
  function toolsLine(tools, e) {
    const d = el("div", "sub pm-mw pm-tools"), sv = e.toolsServer;
    const names = tools.map((x) => x.mcp || x.name).join(t(", "));
    d.append(el("span", "", t(!sv ? "Tools for every agent: {names}" : sv.on ? "Tools for agents: {names}" : "Tools, hidden from agents: {names}", { names })));
    d.title = tools.map((x) => (x.mcp || x.name) + (x.description ? " — " + x.description : "")).join("\n");
    const url = mcpAddress(state.settings || {}, "/mcp/magpie/tools");
    const b = copyBtn(url, t("MCP server address"));
    b.title = t("Copy the MCP server's address ({url}): add it to an agent as an MCP server (Streamable HTTP)", { url });
    d.append(b);
    if (sv && !gatewayMode && !featuresOff.has("library")) {
      const f = el("button", "link pm-tools-flip", t(sv.on ? "Hide from agents" : "Give to agents"));
      f.onclick = (ev) => {
        ev.stopPropagation();
        f.disabled = true;
        api("library/servers/plugin-on", { name: sv.plugin, on: !sv.on }).then(refresh, (err) => { f.disabled = false; statusErr(err); });
      };
      d.append(f);
    }
    return d;
  }

  // what a plugin's page may read of magpie's: what its package.json's
  // magpie.permissions declares, each a read with no secret in it
  const READS = { "usage:summary": "your usage totals, by model", "providers:list": "your providers' names" };
  const PAGES = { sessions: "Sessions", library: "Library", routing: "Routing", usage: "Usage" };
  function pageLine(p) {
    const d = el("div", "sub pm-mw pm-page");
    const reads = (p.reads || []).map((r) => t(READS[r] || r));
    const page = p.replaces && t(PAGES[p.replaces] || p.replaces);
    d.textContent = d.title = page
      ? reads.length
        ? t("Takes the place of the {page} page, and can read {reads}", { page, reads: reads.join(t(", ")) })
        : t("Takes the place of the {page} page, and reads nothing of magpie's", { page })
      : reads.length
        ? t("Adds a page to the sidebar, which can read {reads}", { reads: reads.join(t(", ")) })
        : t("Adds a page to the sidebar, which reads nothing of magpie's");
    return d;
  }

  // where a plugin's panels go in magpie's own pages (magpie.panels), and
  // the row menus its page adds items to (magpie.actions), titled with
  // their titles; picking an item tells the page the row's name only
  const SLOTS = { agents: "the Agents page", usage: "the Usage page", settings: "Settings", provider: "each provider's editor" };
  const MENUS = { provider: "providers' menus", agent: "agents' menus" };
  function whereLine(xs, key, names, text) {
    const d = el("div", "sub pm-mw pm-page");
    d.textContent = text([...new Set(xs.map((x) => x[key]))].map((s) => t(names[s] || s)).join(t(", ")));
    d.title = xs.map((x) => x.title).filter(Boolean).join("\n") || d.textContent;
    return d;
  }
  function panelsLine(panels, panelReads) {
    const reads = (panelReads || []).map((r) => t(READS[r] || r)).join(t(", "));
    return whereLine(panels, "slot", SLOTS, (where) => reads
      ? t("Adds panels to {where}, which can read {reads}", { where, reads })
      : t("Adds panels to {where}, which read nothing of magpie's", { where }));
  }
  function actionsLine(actions) {
    return whereLine(actions, "menu", MENUS, (where) => t("Adds items to {where}, which open its page", { where }));
  }

  // mwLine is a middleware's hooks, calls, time each and failures, or why it didn't load
  function mwLine(m) {
    const d = el("div", "sub pm-mw");
    if (m.error) {
      d.textContent = t("Middleware didn't load: {error}", { error: m.error });
      d.classList.add("bad");
      d.title = m.error;
      return d;
    }
    const lang = intlLang();
    // a gateway module's hooks, the routings it serves (strategies) and
    // its usage observer (onUsage), each said when it has it
    const parts = [];
    if (m.hooks.length) {
      let s = t("Gateway middleware: {hooks}", { hooks: m.hooks.join(t(", ")) });
      if (m.calls) s += " · " + t("{n} calls, {us} µs each", { n: m.calls.toLocaleString(lang), us: m.avgMicros < 10 ? m.avgMicros.toFixed(1) : Math.round(m.avgMicros).toLocaleString(lang) });
      parts.push(s);
    }
    if (m.strategies?.length) parts.push(t("Routings: {routings}", { routings: m.strategies.join(t(", ")) }) + (m.orders ? " · " + t("asked {n} times", { n: m.orders.toLocaleString(lang) }) : ""));
    if (m.usage) parts.push(t("Usage observer: {n} rows", { n: (m.rows || 0).toLocaleString(lang) }));
    d.append(el("span", "", parts.join(" · ")));
    if (m.events?.length) d.title = t("onEvent sees only {events} events", { events: m.events.join(t(", ")) });
    if (m.failures) {
      const f = el("span", "pm-mw-fail", t("{n} failed", { n: m.failures.toLocaleString(lang) }));
      f.title = t("A failed call leaves the request or reply as it was") + (m.lastError ? "\n" + t("Last: {error}", { error: m.lastError }) : "");
      d.append(el("span", "sep", " · "), f);
    }
    if (m.dropped) {
      const f = el("span", "pm-mw-fail", t("{n} rows dropped", { n: m.dropped.toLocaleString(lang) }));
      f.title = t("Rows that came while onUsage was still busy are dropped: a request never waits for it");
      d.append(el("span", "sep", " · "), f);
    }
    return d;
  }

  function gatewayLine(g) {
    const d = el("div", "sub pm-mw pm-gateway");
    const labels = {
      idle: "Waiting for a request", ready: "Context injected", empty: "No matching context",
      unbound: "Scope not bound", unauthorized: "Authorization failed",
      unavailable: "Context service unavailable", invalid: "Invalid context response",
      skipped: "Request skipped", cancelled: "Request cancelled", timeout: "Recall timed out", error: "Request hook failed",
    };
    let text = t("Gateway request hook") + ": " + t(labels[g.lastOutcome] || labels.idle);
    if (g.calls) text += " · " + t("{n} calls, {ms} ms each", { n: g.calls.toLocaleString(intlLang()), ms: g.avgMillis.toFixed(1) });
    if (g.lastBytes) text += " · " + t("{n} bytes injected", { n: g.lastBytes.toLocaleString(intlLang()) });
    d.append(el("span", "", text));
    if (g.failures) d.append(el("span", "sep", " · "), el("span", "pm-mw-fail", t("{n} failed", { n: g.failures.toLocaleString(intlLang()) })));
    d.title = t("Diagnostics reset when the plugin host restarts; prompts and recalled text are not stored");
    return d;
  }

  // a README on its way: its lines are strings, and magpie's bird drops in
  // and bounces down them, each bending under its feet and wobbling after
  // it; leave() sends it off when the README comes. Its marks are data-*,
  // not classes, so the dialog's observer doesn't place it again each hop
  function bounceLoader() {
    const box = el("div", "pm-bounce"), s = document.createElementNS("http://www.w3.org/2000/svg", "svg"), b = el("div", "sk-bird");
    s.innerHTML = "<path/><path/><path/><path/>";
    b.innerHTML = BIRD;
    // with Settings' Loading bird off, the strings stand still, birdless
    const off = birdOff();
    box.append(s);
    if (!off) box.append(b);
    const ps = [...s.children], ws = [1, .94, .6, .82], at = [.16, .44, .3, .58];
    const L = ws.map((w, i) => ({ y: 31 + i * 22, d: 0, v: 0, bx: 0, hold: false }));
    // the bird is placed by its feet: f faces it, r leans it, sx/sy squash it
    let W = 0, x = 0, y = 0, f = 1, r = 0, sx = 1, sy = 1, gone = false;
    const put = (o) => { ({ x = x, y = y, f = f, r = r, sx = sx, sy = sy } = o); b.style.transform = `translate(${x}px,${y}px) rotate(${r * f}deg) scale(${f * sx},${sy})`; };
    const draw = () => L.forEach((l, i) => {
      const h = l.y, d = l.d, x1 = W * ws[i] - 5, c = Math.min(x1 - 12, Math.max(17, l.bx));
      ps[i].setAttribute("d", `M5 ${h}C${(5 + c) / 2} ${h} ${c - (c - 5) * .35} ${h + d} ${c} ${h + d}C${c + (x1 - c) * .35} ${h + d} ${(c + x1) / 2} ${h} ${x1} ${h}`);
    });
    // a step over frames; it stops when the box is gone, or the README came
    const tween = (ms, fn, ease = (t) => t, exit) => new Promise((ok, no) => {
      const t0 = performance.now();
      const step = (now) => {
        if (!box.isConnected || (gone && !exit)) return no();
        const t = Math.min(1, (now - t0) / ms);
        fn(ease(t), t);
        t < 1 ? requestAnimationFrame(step) : ok();
      };
      requestAnimationFrame(step);
    });
    const easeIn = (t) => t * t * t, easeOut = (t) => 1 - (1 - t) ** 3;
    // a README waits for three landings (the owner, 10-30), 3s at most
    let ready;
    box.ready = new Promise((ok) => { ready = ok; });
    setTimeout(ready, 3000);
    (async () => {
      await new Promise(requestAnimationFrame);
      if (!box.isConnected || gone) return;
      W = box.clientWidth;
      if (off) return draw();
      if (calm()) return draw(), put({ x: at[0] * W, y: L[0].y - 5 });
      // each string a damped spring, let go of when the bird leaves it
      (async () => {
        let last = performance.now();
        for (;;) {
          const now = await new Promise(requestAnimationFrame), dt = Math.min(.032, (now - last) / 1000);
          if (!box.isConnected || gone) return;
          last = now;
          for (const l of L) if (!l.hold) { l.v += (-625 * l.d - 6 * l.v) * dt; l.d += l.v * dt; }
          draw();
        }
      })();
      for (;;) {
        W = box.clientWidth;
        ps.forEach((p) => delete p.dataset.lit);
        const x0 = at[0] * W;
        await tween(300, (e, t) => put({ x: x0 - 24 + 24 * t, y: -30 + (L[0].y + 25) * e, f: 1, r: 0, sx: 1 - .05 * t, sy: 1 + .08 * t }), easeIn);
        for (let i = 0; i < L.length; i++) {
          const l = L[i], top = l.y - 5, deep = i === L.length - 1 ? 16 : 10;
          l.hold = true;
          l.bx = x;
          ps[i].dataset.lit = "";
          await tween(140, (e) => { put({ y: top + deep * e, sx: 1 + .16 * e, sy: 1 - .2 * e }); l.d = y - top; }, easeOut);
          await tween(90, (e) => { put({ y: top + deep * (1 - e), sx: 1.16 - .16 * e, sy: .8 + .3 * e }); l.d = y - top; }, easeIn);
          l.hold = false;
          l.v = -deep * 14;
          if (i === 2) ready();
          const xa = x, ya = y;
          if (i + 1 < L.length) {
            const xb = at[i + 1] * W, yb = L[i + 1].y - 5;
            put({ f: xb > xa ? 1 : -1 });
            await tween(340, (e, t) => put({ x: xa + (xb - xa) * t, y: ya + (yb - ya) * t - 72 * t * (1 - t), sx: 1 - .06 * Math.sin(Math.PI * t), sy: 1 + .12 * Math.sin(Math.PI * t), r: (t - .5) * 20 }));
          } else {
            // the last one throws it high, out of sight, and it drops in again
            b.dataset.flap = "";
            await tween(520, (e) => put({ x: xa + 70 * e, y: ya - (ya + 60) * e, f: 1, r: -12, sx: 1, sy: 1 }), easeOut);
            delete b.dataset.flap;
            await tween(260, () => {});
          }
        }
      }
    })().catch(() => {}).finally(ready);
    box.leave = () => {
      if (off) return box.remove();
      gone = true;
      box.dataset.gone = b.dataset.flap = "";
      const xa = x, ya = y;
      tween(420, (e) => put({ x: xa + (W + 60 - xa) * e, y: ya - (ya + 80) * e, f: 1, r: -14, sx: 1, sy: 1 }), easeIn, true)
        // gone once the dialog has grown to the README, not while it does
        .then(() => tween(200, () => {}, undefined, true)).then(() => box.remove(), () => {});
    };
    return box;
  }

  // a README read once shows at once when its page opens again, birdless,
  // and is read again quietly (the owner, 10-30). The last 20 opened only,
  // a Map in the order they were: the oldest goes first
  const pages = new Map();
  const keep = (k, p) => { pages.delete(k); pages.set(k, p); if (pages.size > 20) pages.delete(pages.keys().next().value); };

  // the plugin's page: what it is, what npm says of it, and its README
  async function detail(l) {
    // a folder plugin: what it is comes from the folder, not from npm; a
    // git one's from the folder it was installed to
    const local = isPath(l.package) || isGit(l.package);
    const ed = el("div", "editor pm-detail");
    const hd = el("div", "ehead pm-dhead");
    hd.append(logo(l.icon || l.npm?.icon, true, kindOf(l)));
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
    if (l.github) fact(t("Stars"), count(l.github.stars));
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
    link(l.github ? "GitHub" : t("Source"), l.npm?.repository);
    if (l.npm?.homepage && l.npm.homepage !== l.npm.repository && !l.npm.homepage.startsWith(l.npm.repository + "#")) link(t("Homepage"), l.npm.homepage);
    main.append(links);
    if (l.github) {
      const w = el("p", "pm-note warn");
      // from npm when its author published it there from the repository
      const from = isGit(l.package)
        ? "Unofficial: {owner} tagged it {topic} on GitHub, and nobody at magpie has reviewed it. It is installed from the repository as it stands, and runs with your sign-in or your requests."
        : "Unofficial: {owner} tagged it {topic} on GitHub, and nobody at magpie has reviewed it. It is installed from npm, as its author published it from the repository, and runs with your sign-in or your requests.";
      w.append(glyph(SHIELD, 12, 1.5), el("span", "", t(from, { owner: l.github.owner, topic })));
      main.append(w);
    }
    if (l.replaces) main.append(el("p", "pm-note", t("magpie's own sign-in to this subscription is deprecated: this plugin takes its place, and accounts signed in there move to it.")));
    const readme = el("div", "pm-readme");
    const had = pages.get(l.package);
    const day = (p) => {
      const at = l.github && !entryOf(l.package) ? l.github.pushed : p.updated;
      upd.textContent = at && !at.startsWith("0001") ? new Date(at).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }) : "—";
    };
    if (had) day(had);
    readme.append(had ? markdown(had.readme || t("No README")) : bounceLoader());
    main.append(readme);
    ed.append(main);
    const bar = el("div", "bar");
    bar.append(el("span", "note", t("Plugins are other people's code: they sign in and make the requests, or see them as middleware.")), el("span", "grow"));
    const close = el("button", "text", t("Close"));
    close.onclick = () => { stop(); closeModal(); };
    bar.append(close);
    ed.append(bar);
    openModal(ed);
    // the button follows an install or a sign-in made while it's open
    const tick = setInterval(() => { if (!ed.isConnected) return stop(); redrawAct(); }, 600);
    function stop() { clearInterval(tick); }
    // the README takes the skeleton's place, and the dialog grows to it on
    // the spring it opened on, centred as it goes, not in one jump; the
    // bird flies off over it
    const fill = async (n) => {
      await readme.querySelector(".pm-bounce")?.ready;
      if (!ed.isConnected) return;
      const d = ed.parentElement, h = d.offsetHeight, y = d.style.top, wait = readme.querySelector(".pm-bounce");
      const m = d.parentElement, at = wait?.getBoundingClientRect(), o = m.getBoundingClientRect();
      readme.replaceChildren(n);
      if (!ed.isConnected || calm()) return;
      // it leaves from over the dialog, where it was: anywhere in it, WebKit
      // lays the fields out 8px short while it is there
      if (wait) {
        Object.assign(wait.style, { top: at.top - o.top + m.scrollTop + "px", left: at.left - o.left + m.scrollLeft + "px", width: at.width + "px" });
        m.append(wait);
        wait.leave();
      }
      holdModal();
      d.animate([{ height: h + "px", top: y, overflow: "hidden" }, { height: d.offsetHeight + "px", top: d.style.top, overflow: "hidden" }], { duration: 570, easing: SPRING });
      n.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 300, easing: "ease-out" });
    };
    try {
      const p = await api("plugins/page?name=" + encodeURIComponent(l.package));
      keep(l.package, p);
      day(p);
      if (!had) fill(markdown(p.readme || t("No README")));
      else if (p.readme !== had.readme) readme.replaceChildren(markdown(p.readme || t("No README")));
    } catch (e) {
      if (had) return;
      upd.textContent = "—";
      fill(el("p", "pm-note", t(l.github && isGit(l.package) && !entryOf(l.package) ? "GitHub didn't answer: {error}" : local ? "Couldn't read the folder's README: {error}" : "npm didn't answer: {error}", { error: e.message })));
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
