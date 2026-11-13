// The page's side of a plugin page's messages to magpie's window
// (docs/subsystems/plugin-runtime.md, P4). A plugin's page loads it with
// <script src="_magpie/bridge.js"></script>; it is served beside every
// page's own files.
//
//   await magpie.ready            the window's theme, once it has answered
//   magpie.theme                  { dark, lang, tokens: { bg, fg, … } }
//   magpie.onTheme(fn)            fn(theme) on each change of it
//   await magpie.options.get()    the plugin's own options (plugins.json)
//   await magpie.options.set(o)   sets them, and answers with them
//   await magpie.read(name, p)    a read its package.json's
//                                 magpie.permissions declares
//   await magpie.image.copy(png)  puts a PNG (a Blob, as a canvas's
//                                 toBlob makes it) on the clipboard
//   await magpie.image.save(png, name)
//                                 saves it in Downloads as name.png, and
//                                 answers { path } ("" in magpie web,
//                                 where the browser downloads it)
//   magpie.context                where a panel is (P5): { provider: {
//                                 id, name } } in a provider's editor,
//                                 else {}; set once magpie.ready is
//   magpie.service               the address of the page's local service
//                                 (package.json's magpie.page.service)
//                                 through magpie, set once magpie.ready
//                                 is, on a page a browser opened through
//                                 magpie web; "" where 127.0.0.1 is the
//                                 service's own computer (the app's
//                                 window) or magpie is older
//   magpie.onAction(fn)           fn({ id, provider | agent: { id, name } })
//                                 when one of its magpie.actions is picked
//                                 in a row's menu (P6); one picked before
//                                 fn was given waits for it
//   magpie.visible               false while the reader is on another of
//                                 the window's pages
//   magpie.onVisibility(fn)      fn(visible) as the reader leaves the
//                                 page and comes back, to pause what only
//                                 matters while it is seen; a page left
//                                 10 minutes is let go, and loads again
//                                 when next opened, unless package.json
//                                 says magpie.page.keepAlive
//
// A panel (magpie.panels) is as tall as what it draws: the window is told
// its height as it changes.
//
// What the page leaves unstyled takes magpie's look: the bridge adds a
// stylesheet of no weight (:where()) for the font, colours, headings,
// buttons, fields and tables, and the theme's colours are the page's
// --magpie-<token> variables.
//
// The page can't reach the network or magpie's API itself: everything goes
// through the window, which answers only what the package declared. An
// error the page doesn't catch shows the window's "This page stopped
// working" in its place.
(() => {
  const waiting = new Map();
  let next = 0;
  const themed = [];
  const watched = [];
  let acting = null;
  const acted = [];
  function call(method, params) {
    const id = ++next;
    return new Promise((resolve, reject) => {
      waiting.set(id, { resolve, reject });
      parent.postMessage({ magpie: 1, id, method, params }, "*");
    });
  }
  // A page that came as one document (pageinline.go) can't ask for its
  // other files itself: a proxy that signs readers in by cookie turns away
  // a sandboxed frame's requests. A script it adds is fetched by the
  // window, which has the cookie, and loaded from a data: address.
  if (globalThis.__magpieInlined) {
    const put = (node, ref) => {
      let abs;
      try { abs = new URL(ref, location.href).href; } catch { return false; }
      call("asset", { url: abs }).then(({ type, data }) => {
        // a data: address, not a blob's: WebKit finds no blob made in a
        // sandboxed frame
        let bin = "";
        const bytes = new Uint8Array(data);
        for (let i = 0; i < bytes.length; i += 8192) bin += String.fromCharCode(...bytes.subarray(i, i + 8192));
        node.setAttribute("src", "data:text/javascript;base64," + btoa(bin));
        node.__magpiePut(node);
      }, () => node.__magpiePut(node));
      return true;
    };
    const add = (orig) => function (node, ...rest) {
      if (node instanceof HTMLScriptElement && node.hasAttribute("src") && !node.__magpieDone && !/^(?:[a-z][a-z0-9+.-]*:|\/\/)/i.test(node.getAttribute("src"))) {
        const ref = node.getAttribute("src");
        node.__magpiePut = (n) => { n.__magpieDone = true; orig.call(this, n, ...rest); };
        if (put(node, ref)) return node;
      }
      return orig.call(this, node, ...rest);
    };
    Node.prototype.appendChild = add(Node.prototype.appendChild);
    Node.prototype.insertBefore = add(Node.prototype.insertBefore);
  }
  function paint(theme) {
    const root = document.documentElement;
    root.dataset.theme = theme.dark ? "dark" : "light";
    // the frame's canvas matches the window's, not a white sheet in dark;
    // a light one is light only, so a browser that repaints light pages
    // dark leaves it as the window is left (xjp on X)
    root.style.colorScheme = theme.dark ? "dark" : "light only";
    root.lang = theme.lang || "en";
    for (const [k, v] of Object.entries(theme.tokens || {})) root.style.setProperty("--magpie-" + k, v);
  }
  addEventListener("message", (e) => {
    if (e.source !== parent) return;
    const m = e.data;
    if (!m || m.magpie !== 1) return;
    if (m.event === "theme") {
      api.theme = m.data;
      paint(m.data);
      for (const fn of themed) { try { fn(m.data); } catch (err) { report(err); } }
      return;
    }
    if (m.event === "visibility") {
      api.visible = !!m.data?.visible;
      for (const fn of watched) { try { fn(api.visible); } catch (err) { report(err); } }
      return;
    }
    if (m.event === "action") {
      if (acting) act(m.data); else acted.push(m.data);
      return;
    }
    const w = waiting.get(m.id);
    if (!w) return;
    waiting.delete(m.id);
    if (m.error) w.reject(new Error(m.error)); else w.resolve(m.result);
  });
  // What the page's own code did wrong is what breaks it. Two things that
  // reach its error handlers are not that: a script the browser put in
  // the frame (a wallet's or a translator's extension, whose stack names
  // chrome-extension://…), and a read of the window or another frame the
  // sandbox shuts out (its storage, "The document is sandboxed and lacks the
  // 'allow-same-origin' flag"; "Blocked a frame with origin "null" from accessing
  // a cross-origin frame" in Chromium, "Sandbox access violation" in
  // WebKit; a SecurityError). The page goes on without
  // either, and the window's "stopped working" over it was a page that
  // was fine (a day-card page, magpie 0.1.3702).
  function foreign(err, file) {
    const where = String(file || "") + "\n" + String(err?.stack || "");
    if (/(?:chrome|moz|safari-web)-extension:\/\//i.test(where)) return true;
    // storage the sandbox has none of: "Failed to read the 'localStorage'
    // property from 'Window': The document is sandboxed and lacks the
    // 'allow-same-origin' flag" (Chromium; WebKit says "The operation is
    // insecure"), read by a script that isn't the page's (nivekxft on X)
    const why = String(err?.message);
    if (err?.name === "SecurityError" && (/The operation is insecure/.test(why) || (/(?:local|session)Storage|\bstorage\b|cookie/i.test(why) && /sandboxed|allow-same-origin/i.test(why)))) return true;
    return err?.name === "SecurityError" && /Blocked a frame (?:with origin|at )/.test(why);
  }
  function report(err) {
    parent.postMessage({ magpie: 1, method: "error", params: { message: String(err?.message || err) } }, "*");
  }
  // a script's error reaches a sandboxed page only as "Script error." (its
  // files are another origin's to it); the window then says "The page hit
  // an error" in the reader's language, and the console has the rest
  //
  // The size watch below can be told its own "ResizeObserver loop
  // completed with undelivered notifications" when the window resizes the
  // frame in the same frame of drawing; that is no error of the page's,
  // and WebKit hands it to a sandboxed page as a bare "Script error." with
  // no file, so one that comes while the watch has just run is let go
  let measuring = false;
  addEventListener("error", (e) => {
    if (/ResizeObserver loop/.test(e.message) || (measuring && !e.error && !e.filename)) { e.preventDefault(); return; }
    if (foreign(e.error, e.filename)) { e.preventDefault(); return; }
    report(e.error || (e.message === "Script error." ? "" : e.message));
  });
  addEventListener("unhandledrejection", (e) => {
    if (foreign(e.reason)) { e.preventDefault(); return; }
    report(e.reason);
  });
  function act(data) {
    try { Promise.resolve(acting(data)).catch(report); } catch (err) { report(err); }
  }
  const api = {
    theme: null,
    context: {},
    service: "",
    visible: true,
    onTheme(fn) { themed.push(fn); },
    onVisibility(fn) { watched.push(fn); },
    onAction(fn) {
      acting = fn;
      for (const d of acted.splice(0)) act(d);
    },
    options: {
      get: () => call("options.get"),
      set: (options) => call("options.set", { options }),
    },
    read: (name, params) => call("read", { name, params }),
    image: {
      copy: (png) => call("image.copy", { png }),
      save: (png, name) => call("image.save", { png, name }),
    },
  };
  api.ready = call("hello").then(({ context, service, visible, ...theme }) => {
    api.theme = theme;
    if (visible === false) api.visible = false;
    api.context = context || {};
    api.service = "";
    try { if (service) api.service = new URL(service, location.href).href; } catch { /* none */ }
    paint(theme);
    return theme;
  });
  // how tall the page draws, for a panel's frame; a page's frame fills its
  // view, and the window doesn't heed it there
  let told = -1;
  const size = () => {
    const h = Math.ceil(document.documentElement.getBoundingClientRect().height);
    if (h === told) return;
    told = h;
    parent.postMessage({ magpie: 1, method: "size", params: { height: h } }, "*");
  };
  const watch = () => {
    new ResizeObserver(() => {
      if (!measuring) { measuring = true; setTimeout(() => { measuring = false; }); }
      size();
    }).observe(document.documentElement);
    size();
  };
  if (document.readyState === "loading") addEventListener("DOMContentLoaded", watch); else watch();
  // magpie's look for what the page leaves unstyled: its font, colours,
  // headings, buttons, fields and tables, so a page with no stylesheet of
  // its own reads as part of the window, not a browser's default sheet.
  // Every rule is :where(), of no weight: any rule of the page's wins.
  const base = document.createElement("style");
  base.dataset.magpie = "base";
  base.textContent = `
:where(html) { background: var(--magpie-bg, Canvas); color: var(--magpie-fg, CanvasText); font: 13px/1.45 var(--magpie-font, -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif); -webkit-font-smoothing: antialiased; }
:where(body) { margin: 0; padding: 20px 24px; }
:where(h1, h2, h3, h4) { font-weight: 600; margin: 0 0 8px; letter-spacing: -.01em; }
:where(h1) { font-size: 20px; } :where(h2) { font-size: 15px; } :where(h3, h4) { font-size: 13px; }
:where(p) { margin: 0 0 8px; }
:where(small, .muted) { color: var(--magpie-muted, GrayText); }
:where(a) { color: var(--magpie-accent, LinkText); text-decoration: none; }
:where(a:hover) { text-decoration: underline; }
:where(button, input, textarea, select) { font: inherit; color: inherit; }
:where(button) { background: var(--magpie-card, ButtonFace); border: 1px solid var(--magpie-line, ButtonBorder); border-radius: 6px; padding: 3px 10px; }
:where(button:hover:not(:disabled)) { border-color: var(--magpie-muted, ButtonBorder); }
:where(button[aria-pressed="true"], button[aria-selected="true"]) { background: var(--magpie-accent-soft, Highlight); border-color: transparent; color: var(--magpie-accent, HighlightText); }
:where(button:disabled) { opacity: .5; }
:where(input, textarea, select) { background: var(--magpie-card, Field); border: 1px solid var(--magpie-line, ButtonBorder); border-radius: 6px; padding: 4px 8px; }
:where(:focus-visible) { outline: 2px solid var(--magpie-accent, Highlight); outline-offset: 1px; }
:where(table) { border-collapse: collapse; width: 100%; }
:where(th) { text-align: left; font-weight: 500; font-size: 12px; color: var(--magpie-muted, GrayText); }
:where(th, td) { padding: 6px 8px; border-bottom: 1px solid var(--magpie-line, ButtonBorder); }
:where(code, kbd, pre) { font-family: var(--magpie-code, ui-monospace, Menlo, monospace); font-size: 12px; }
:where(pre) { background: var(--magpie-card, Canvas); border: 1px solid var(--magpie-line, ButtonBorder); border-radius: 8px; padding: 8px 10px; overflow: auto; }
:where(hr) { border: 0; border-top: 1px solid var(--magpie-line, ButtonBorder); margin: 12px 0; }
:where(::selection) { background: var(--magpie-accent-soft, Highlight); }
`;
  (document.head || document.documentElement).prepend(base);
  window.magpie = api;
})();
