// A community plugin's own page in the window (docs/subsystems/
// plugin-runtime.md, P4): package.json's magpie.page, in a tab of the
// sidebar's Plugins group, after the built-in pages. The page runs in a
// frame sandboxed to scripts alone, in an origin of its own: it has no
// way to the window, to magpie's API (apiFramed) or to the network (its
// policy, pluginpage.go). What it asks of the window comes by message,
// from that frame only, and the window answers only what the page may:
// the theme, its own options, and the reads its package declared
// (magpie.permissions), which /api/plugin-pages/read checks again.
//
// Panels (magpie.panels, P5) are framed the same way in a page's
// .plugin-slot, as tall as they draw. plugin-runtime.md says the rest:
// a page broken, in a built-in one's place, opened by an action, shared.
(() => {
  if (mode !== "window") return;
  // how long a page has to say hello before it is taken as broken
  const HELLO_MS = 10e3;
  // how long a page the reader left keeps its frame: one left longer is
  // let go, and loads again the next time it is opened, unless its
  // package keeps it (magpie.page.keepAlive)
  const IDLE_MS = 10 * 60e3;
  const pages = new Map(); // key → { entry, frame, hello, timer, shown, idle }
  const panels = new Map(); // frame → { entry, frame, context, hello, timer, body }
  let panelList = []; // /api/plugin-panels: every panel of a plugin on
  let ctx = null;
  // a built-in page → the plugin's page in its place (magpie.page.replaces)
  let replaced = new Map();
  magpie.replacing = (v) => replaced.get(v) || v;
  magpie.plugin("plugin-pages", (c) => { ctx = c; });

  const TOKENS = ["bg", "card", "fg", "fg-2", "muted", "faint", "line", "accent", "accent-soft", "accent-fg", "green", "red", "amber", "font", "code"];
  function theme() {
    const css = getComputedStyle(document.documentElement);
    const tokens = {};
    for (const k of TOKENS) tokens[k] = css.getPropertyValue("--" + k).trim();
    const dark = document.documentElement.dataset.theme === "dark"
      || (document.documentElement.dataset.theme !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
    return { dark, lang: locale, tokens };
  }

  function post(win, msg) { win?.postMessage({ magpie: 1, ...msg }, "*"); }

  // the theme or the language changed: every page open is told
  function pushTheme() {
    const data = theme();
    for (const p of [...pages.values(), ...panels.values()]) if (p.hello) post(p.frame?.contentWindow, { event: "theme", data });
  }
  new MutationObserver(pushTheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "lang", "class", "style"] });
  matchMedia("(prefers-color-scheme: dark)").addEventListener?.("change", pushTheme);

  function broke(key, why) { ctx?.broke(key, new Error(why)); }

  // a page's or a panel's title in the window's language: its titles
  // (magpie.page.titles, {"zh": "…"}) by the language, then by its first
  // part (zh for zh-TW), else its title
  function titled(e) {
    const m = e.titles || {};
    return m[locale] || m[locale.split("-")[0]] || e.title;
  }

  // a page is told when the reader leaves it and comes back
  // (magpie.onVisibility): a frame in a hidden view isn't hidden to itself
  function seen(p, visible) {
    if (p.shown === visible) return;
    p.shown = visible;
    if (p.hello) post(p.frame?.contentWindow, { event: "visibility", data: { visible } });
  }

  // the page's frame, made the first time the page is opened (and again
  // after Try again, or once it was let go): a plugin's page costs
  // nothing until it is looked at
  function open(key) {
    const p = pages.get(key);
    if (!p) return undefined;
    clearTimeout(p.idle);
    if (p.frame?.isConnected) { seen(p, true); return p.ready; }
    const main = $("#view-" + key);
    const f = document.createElement("iframe");
    f.className = "plugin-frame";
    // scripts, and nothing else: no same origin (no cookie, no storage of
    // the window's, no reach into it), no forms, popups or top navigation
    f.setAttribute("sandbox", "allow-scripts");
    f.setAttribute("referrerpolicy", "no-referrer");
    f.setAttribute("allow", "");
    f.title = titled(p.entry);
    f.src = framed(p.entry.url);
    p.frame = f;
    p.hello = false;
    p.shown = true;
    p.ready = new Promise((resolve, reject) => {
      p.said = resolve;
      clearTimeout(p.timer);
      p.timer = setTimeout(() => { if (!p.hello) reject(new Error(t("{name}'s page didn't answer", { name: titled(p.entry) }))); }, HELLO_MS);
    });
    main.replaceChildren(f);
    return p.ready;
  }

  // the reader went to another page: this one is told, and its frame is
  // let go after IDLE_MS there, with what it holds (its scripts, timers,
  // connections); one that is back by then, or keeps itself, stays
  function leave(key) {
    const p = pages.get(key);
    if (!p?.frame) return;
    seen(p, false);
    clearTimeout(p.idle);
    if (p.entry.keepAlive) return;
    p.idle = setTimeout(() => {
      if (view === key || !p.frame) return;
      clearTimeout(p.timer);
      p.frame.remove();
      p.frame = null;
      p.hello = false;
    }, IDLE_MS);
  }

  async function answer(p, m) {
    const e = p.entry;
    switch (m.method) {
      case "hello":
        p.hello = true;
        clearTimeout(p.timer);
        p.said?.();
        // a panel is told where it is: the provider whose editor it is in;
        // a page served to a browser, where its local service is through
        // this server (pageservice.go), relative to the page
        return { ...theme(), context: p.context || {}, service: (!p.context && e.service) || "", visible: p.shown ?? true };
      case "theme":
        return theme();
      case "options.get":
        return api("plugin-pages/options", { spec: e.spec });
      case "options.set": {
        const o = m.params?.options;
        if (!o || typeof o !== "object" || Array.isArray(o)) throw new Error(t("A page's options are an object"));
        return api("plugin-pages/options", { spec: e.spec, options: o });
      }
      case "read": {
        const name = String(m.params?.name || "");
        // what its package didn't declare is refused here, and again by
        // the server
        if (!e.reads.includes(name)) throw new Error(t("{name} doesn't ask to read {what}", { name: titled(e), what: name }));
        return api("plugin-pages/read", { spec: e.spec, read: name, params: m.params?.params || {} });
      }
      case "asset":
        return import("./pageshare.js").then(() => pageAsset(e.url, m.params?.url));
      case "image.copy":
      case "image.save":
        return import("./pageshare.js").then(() => pageShare(e.spec, m.method === "image.save", m.params));
    }
    throw new Error(t("No such call: {what}", { what: String(m.method) }));
  }

  addEventListener("message", async (ev) => {
    // only a page's own frame is listened to: another window's message,
    // or one that names a page, is nothing
    let key = "", p = null;
    for (const [k, x] of pages) if (x.frame && ev.source === x.frame.contentWindow) { key = k; p = x; break; }
    if (!p) {
      for (const [f, x] of panels) {
        if (!f.isConnected) { panels.delete(f); clearTimeout(x.timer); continue; }
        if (ev.source === f.contentWindow) { p = x; break; }
      }
    }
    if (!p) return;
    const m = ev.data;
    if (!m || typeof m !== "object" || m.magpie !== 1 || typeof m.method !== "string") return;
    if (m.method === "error") {
      const why = String(m.params?.message || t("The page hit an error"));
      if (p.context) panelBroke(p, why); else broke(key, why);
      return;
    }
    // a panel is as tall as what it draws; a page fills its view
    if (m.method === "size") {
      const h = Number(m.params?.height);
      if (p.context && Number.isFinite(h)) p.frame.style.height = Math.min(Math.max(Math.ceil(h), 0), PANEL_MAX) + "px";
      return;
    }
    const src = ev.source;
    try {
      post(src, { id: m.id, result: await answer(p, m) });
    } catch (err) {
      post(src, { id: m.id, error: String(err?.message || err) });
    }
  });

  // the sidebar's tab for each page, in a Plugins group of its own after
  // the built-in pages
  function tab(e) {
    const b = document.createElement("button");
    b.dataset.view = e.key;
    b.dataset.pluginPage = "";
    if (e.icon) {
      // a mark in one near-black or near-white tone, as a currentColor
      // SVG is in an <img> (black), is drawn in the tab's own ink, as the
      // built-in pages' marks are (shannnnnks on X: Day card's was black
      // on the dark sidebar); a coloured picture is drawn as it is
      const box = el("i", "nav-ic nav-pic"), img = el("img");
      img.src = e.icon;
      img.alt = "";
      box.append(img);
      b.append(box);
      toneCheck(img);
    } else {
      const s = svg(PUZZLE, 16, 1.5);
      s.classList.add("nav-ic");
      s.setAttribute("aria-hidden", "true");
      b.append(s);
    }
    b.append(el("span", "", titled(e)));
    b.onclick = () => { if (b.dataset.dragged) return; show(e.key); b.blur(); };
    b.addEventListener("pointerdown", (ev) => hold(b, ev));
    return b;
  }

  // a press held 400ms on a tab picks it up; it is dragged to its place
  // and the order kept (settings PluginPageOrder)
  function hold(b, ev) {
    if (ev.button !== 0 || !document.body.classList.contains("side-nav")) return;
    let last = ev;
    const mv = (m) => { last = m; if (Math.hypot(m.clientX - ev.clientX, m.clientY - ev.clientY) > 6) stop(); };
    const stop = () => {
      clearTimeout(timer);
      document.removeEventListener("pointermove", mv);
      document.removeEventListener("pointerup", stop);
      document.removeEventListener("pointercancel", stop);
    };
    const timer = setTimeout(() => {
      stop();
      const nav = $("#nav");
      const rows = [...nav.querySelectorAll("button[data-plugin-page]")].filter((x) => !pages.get(x.dataset.view)?.entry.replaces);
      b.dataset.dragged = "1";
      const at = { button: 0, isPrimary: true, pointerId: ev.pointerId, clientX: last.clientX, clientY: last.clientY };
      if (!dragRows(at, b, b, nav, rows, (to) => {
        const rest = rows.filter((x) => x !== b);
        rest.splice(to, 0, b);
        rows[to][to > rows.indexOf(b) ? "after" : "before"](b);
        api("plugin-pages/arrange", { order: rest.map((x) => x.dataset.view) }).catch((err) => { statusErr(err); load(); });
      })) delete b.dataset.dragged;
    }, 400);
    document.addEventListener("pointermove", mv);
    document.addEventListener("pointerup", stop);
    document.addEventListener("pointercancel", stop);
  }

  async function load() {
    loadPanels();
    let list;
    try { list = await api("plugin-pages"); } catch { return; }
    // an answer that isn't a list is not known: the tabs drawn stay
    if (!Array.isArray(list)) return;
    const nav = $("#nav");
    const keys = new Set(list.map((e) => e.key));
    // a page whose plugin went off, or away: its tab goes, and the window
    // leaves it if it was open
    for (const b of nav.querySelectorAll("button[data-plugin-page]")) if (!keys.has(b.dataset.view)) b.remove();
    // a built-in page replaced by a plugin's that went is back in its place
    const replacing = new Map(list.filter((e) => e.replaces).map((e) => [e.replaces, e.key]));
    for (const b of nav.querySelectorAll("button[data-replaced]")) if (!replacing.has(b.dataset.view)) b.removeAttribute("data-replaced");
    replaced = replacing;
    for (const [k, p] of pages) {
      if (keys.has(k)) continue;
      p.frame?.remove();
      p.frame = null;
      p.gone = true;
      if (view === k) show("plugins");
    }
    let head = nav.querySelector(".nav-gl[data-plugin-pages]");
    const own = list.filter((e) => !e.replaces);
    if (!own.length) head?.remove();
    if (!head && own.length) {
      head = el("span", "nav-gl", t("Plugins"));
      head.dataset.pluginPages = "";
      head.dataset.en = "Plugins";
      head.setAttribute("data-t", "");
      nav.append(head);
    }
    let at = head;
    for (const e of list) {
      let p = pages.get(e.key);
      if (!p) {
        p = { entry: e };
        pages.set(e.key, p);
        ctx.slots.add("page", { key: e.key, title: titled(e), html: "", mount(main) { main.classList.add("plugin-page"); p.frame = null; }, open: () => open(e.key), leave: () => leave(e.key) });
      } else if (p.entry.url !== e.url || p.gone) {
        p.frame?.remove();
        p.frame = null;
      }
      p.gone = false;
      p.entry = e;
      const part = magpie.page(e.key);
      if (part) part.title = titled(e);
      let b = nav.querySelector(`button[data-plugin-page][data-view="${e.key}"]`);
      if (!b) b = tab(e);
      else b.querySelector("span").textContent = titled(e);
      b.classList.toggle("on", view === e.key);
      // in the place of the built-in page it replaces, which steps aside
      // (and the reader on it is taken to the plugin's); else in the
      // Plugins group
      const built = e.replaces && nav.querySelector(`button[data-view="${e.replaces}"]:not([data-plugin-page])`);
      if (built) {
        built.dataset.replaced = e.key;
        built.after(b);
        if (view === e.replaces) show(e.key);
        continue;
      }
      at.after(b);
      at = b;
    }
    navSubs();
    // a reload of a plugin's page, or the one the window was left on,
    // comes back to it once its tab is there
    if (view === "agents" && !opened) {
      opened = true;
      let want = params.get("view") || "";
      if (!want) { try { want = localStorage.getItem("magpie.lastView") || ""; } catch { /* none kept */ } }
      if (pages.has(want)) show(want);
    }
  }
  let opened = false;

  // ---- actions ----

  // the row menu's items for menu ("provider", "agent"), each told
  // context: { provider: { id, name } } or { agent: { id, name } }
  magpie.actions = (menu, context) => {
    const items = [];
    for (const [key, p] of pages) {
      if (p.gone) continue;
      for (const a of p.entry.actions || []) {
        if (a.menu !== menu) continue;
        items.push({ name: titled(a), literal: true, icon: PUZZLE, sep: !items.length, run: () => act(key, a.id, context) });
      }
    }
    return items;
  };

  async function act(key, id, context) {
    // a reader who keeps an unsaved edit where they are stays there
    if (await show(key) === false) return;
    const p = pages.get(key);
    try { await open(key); } catch { return; } // its page says it broke
    post(p?.frame?.contentWindow, { event: "action", data: { id, ...context } });
  }

  // ---- panels ----

  // how tall a panel can be, so one that says it is endless isn't
  const PANEL_MAX = 2400;

  function panelBroke(p, why) {
    if (p.broken) return;
    p.broken = true;
    clearTimeout(p.timer);
    panels.delete(p.frame);
    const card = el("div", "plugin-panel-broke");
    card.setAttribute("role", "alert");
    card.append(el("b", "", t("{name} stopped working", { name: titled(p.entry) })), el("span", "note", why));
    const again = el("button", "text", t("Try again"));
    again.type = "button";
    again.onclick = () => panelFrame(p.entry, p.context, p.body);
    card.append(again);
    p.body.replaceChildren(card);
    console.error(`[magpie] ${p.entry.name}: panel ${p.entry.key}: ${why}`);
  }

  // The address a frame loads: a browser's page names its own origin in
  // it (the frame sends no Referer), as the server's policy for the frame
  // can't know the address a reverse proxy that keeps no Host serves it
  // at. The desktop window's scheme is the server's own to name.
  function framed(url) {
    return /^https?:$/.test(location.protocol) ? url + (url.includes("?") ? "&" : "?") + "_o=" + encodeURIComponent(location.origin) : url;
  }

  // a panel's frame in body, told context once it says hello
  function panelFrame(e, context, body) {
    const f = document.createElement("iframe");
    f.className = "plugin-frame plugin-panel-frame";
    f.setAttribute("sandbox", "allow-scripts");
    f.setAttribute("referrerpolicy", "no-referrer");
    f.setAttribute("allow", "");
    f.title = titled(e);
    f.src = framed(e.url);
    const p = { entry: e, frame: f, context, body, hello: false, broken: false };
    p.timer = setTimeout(() => { if (!p.hello) panelBroke(p, t("{name}'s page didn't answer", { name: titled(e) })); }, HELLO_MS);
    panels.set(f, p);
    body.replaceChildren(f);
  }

  // fill a slot with the panels of its name: drawn again only when they
  // changed, so a panel already open goes on as it is
  function fill(box) {
    const name = box.dataset.slot;
    const list = panelList.filter((e) => e.slot === name);
    const sig = list.map((e) => e.key + " " + e.url + " " + titled(e)).join("|");
    if (box.dataset.sig === sig && box.childElementCount === list.length) return;
    box.dataset.sig = sig;
    const context = box.pluginContext || {};
    const parts = list.map((e) => {
      const sec = el("section", "plugin-panel");
      sec.dataset.panel = e.key;
      const head = el("div", "row-head");
      head.append(el("span", "label", titled(e)));
      if (titled(e) !== e.name) head.append(el("span", "note", e.name));
      const body = el("div", "plugin-panel-body");
      sec.append(head, body);
      panelFrame(e, context, body);
      return sec;
    });
    box.replaceChildren(...parts);
    box.hidden = !parts.length;
    // Settings' Plugins tab comes or goes with its panels, in the sidebar too
    if (name === "settings" && typeof setSetTab === "function") { setSetTab(setTab); navSubs(); }
  }

  // a slot app.js draws, with what its panels are told (a provider's
  // editor: { provider: { id, name } })
  magpie.slot = (name, context) => {
    const box = document.createElement("div");
    box.className = "plugin-slot";
    box.dataset.slot = name;
    box.pluginContext = context || {};
    box.hidden = true;
    fill(box);
    return box;
  };

  // a page's markup written after the panels came (a built-in page
  // registered late, or put back by Try again): its slots filled now
  magpie.fillSlots = (root) => { for (const box of root.querySelectorAll(".plugin-slot[data-slot]")) fill(box); };

  async function loadPanels() {
    let list;
    try { list = await api("plugin-panels"); } catch { return; }
    // an answer that isn't a list is not known: the panels drawn stay
    if (!Array.isArray(list)) return;
    panelList = list;
    for (const box of document.querySelectorAll(".plugin-slot[data-slot]")) fill(box);
  }

  window.loadPluginPages = load;
  load();
  // a new language: the tabs and the panels' heads are titled in it
  let lang = document.documentElement.lang;
  new MutationObserver(() => {
    if (document.documentElement.lang === lang) return;
    lang = document.documentElement.lang;
    load();
  }).observe(document.documentElement, { attributes: true, attributeFilter: ["lang"] });
})();
