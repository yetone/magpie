// usemagpie.ai on Omarchy (omarchy.org), the Hyprland desktop: the pages draw
// themselves in Omarchy's own look (its theme's palette, its monospace font,
// square flat surfaces framed in the accent) and, where magpie runs on the
// same computer, in the very theme Omarchy has on, followed as it changes.
// Each page loads this only in a Linux desktop browser, or once a reader
// turned it on (?omarchy=1); elsewhere nothing here runs.
//
// Omarchy is told by its font: it installs "omarchy" (its logo, U+E900)
// system-wide, and a page may load a local font by name. The theme comes from
// magpie's gateway, GET http://127.0.0.1:3425/v1/magpie/omarchy, which only
// usemagpie.ai may read. Chrome asks the reader before a site reaches this
// computer, so the page asks magpie by itself only once that was allowed;
// until then it takes Omarchy's default theme for light or dark, and the chip
// in the corner offers to follow the computer, or any built-in theme.
//
// localStorage "omarchy": {on: true|false (unset: when Omarchy is found),
// seen: Omarchy was found here, pick: a built-in theme's name ("" follows the
// computer), live: what magpie last told, painted at once next time}.
// ?omarchy=1|0|<theme> sets it.
(() => {
  const root = document.documentElement;
  const lang = (root.lang || "en").slice(0, 2);
  const T = (en, zh, ja) => ({ zh, ja })[lang] ?? en;
  const KEY = "omarchy";
  const GATEWAY = "http://127.0.0.1:3425/v1/magpie/omarchy";
  // Omarchy 4's themes (/usr/share/omarchy/themes/*/colors.toml): mode,
  // background, foreground, bright foreground, dim text, accent, green, red,
  // yellow
  const THEMES = {
    "catppuccin": "dark 1e1e2e cdd6f4 cdd6f4 6c7086 89b4fa a6e3a1 f38ba8 f9e2af",
    "catppuccin-latte": "light eff1f5 4c4f69 4c4f69 acb0be 1e66f5 40a02b d20f39 df8e1d",
    "ethereal": "dark 060b1e ffcead ffcead 6d7db6 7d82d9 92a593 ed5b5a e9bb4f",
    "everforest": "dark 2d353b d3c6aa d3c6aa 4f585e 7fbbb3 a7c080 e67e80 dbbc7f",
    "flexoki-light": "light fffcf0 100f0f 100f0f b7b5ac 205ea6 879a39 d14d41 d0a215",
    "gruvbox": "dark 282828 d4be98 d4be98 7c6f64 7daea3 a9b665 ea6962 d8a657",
    "hackerman": "dark 0b0c16 ddf7ff ddf7ff 6a6e95 82fb9c 4fe88f 50f872 50f7d4",
    "kanagawa": "dark 1f1f28 dcd7ba dcd7ba 727169 dcd7ba 76946a c34043 c0a36e",
    "last-horizon": "dark 0c0b0c fafcfb e2dddc 584e51 b59790 87a9b0 c38b7b 6b5e73",
    "lumon": "dark 16242d d6e2ee f2fcff 4d86b0 8bc9eb 5e95bc 4d86b0 6fa4c9",
    "lupine": "light fafafa 212121 000000 9e9e9e 3264eb 4a2fd0 c900c4 026fde",
    "matte-black": "dark 121212 bebebe bebebe 555555 e68e0d ffc107 d35f5f b91c1c",
    "miasma": "dark 222222 c2c2b0 c2c2b0 555555 78824b 5f875f 685742 b36d43",
    "nord": "dark 2e3440 d8dee9 d8dee9 667080 81a1c1 a3be8c bf616a ebcb8b",
    "osaka-jade": "dark 111c18 c1c497 f7e8b2 81b8a8 509475 549e6a ff5345 459451",
    "retro-82": "dark 05182e f6dcac f6dcac 3f8f8a faa968 028391 f85525 e97b3c",
    "ristretto": "dark 2c2525 e6d9db e6d9db 72696a f38d70 adda78 fd6883 f9cc6c",
    "rose-pine": "light faf4ed 575279 575279 cecacd 56949f 286983 b4637a ea9d34",
    "solitude": "dark 101315 cacccc a5aeb4 4b4e55 798186 9fa5a9 565d60 d9dbdc",
    "tokyo-night": "dark 1a1b26 a9b1d6 c0caf5 565f89 7aa2f7 9ece6a f7768e e0af68",
    "vantablack": "dark 000000 ffffff ffffff 505050 8d8d8d b6b6b6 a4a4a4 cecece",
    "white": "light ffffff 000000 000000 808080 6e6e6e 3a3a3a 2a2a2a 4a4a4a",
  };
  // Omarchy's own default, and a light one for a light desktop
  const GUESS = { dark: "tokyo-night", light: "catppuccin-latte" };

  let st = {};
  try { st = JSON.parse(localStorage.getItem(KEY) || "{}") || {}; } catch {}
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(st)); } catch {} };

  const q = new URLSearchParams(location.search);
  if (q.has("omarchy")) {
    const v = q.get("omarchy");
    if (v === "0" || v === "off") st.on = false;
    else { st.on = true; st.pick = THEMES[v] ? v : ""; }
    save();
    q.delete("omarchy");
    const rest = q.toString();
    history.replaceState(history.state, "", location.pathname + (rest ? "?" + rest : "") + location.hash);
  }

  // a at t over b, each "rrggbb"
  const rgb = (h) => [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
  const mix = (a, b, t) => "#" + rgb(a).map((x, i) => Math.round(rgb(b)[i] + (x - rgb(b)[i]) * t).toString(16).padStart(2, "0")).join("");

  function builtin(name) {
    const [mode, bg, fg, hi, dim, accent, green, red, yellow] = THEMES[name].split(" ");
    return {
      name, mode, bg: "#" + bg, fg: "#" + fg, hi: "#" + hi, dim: mode === "light" ? mix(fg, bg, 0.62) : "#" + dim,
      accent: "#" + accent, accentFg: "#" + bg, card: mix(fg, bg, 0.04), code: mix(fg, bg, 0.06), line: mix(fg, bg, 0.16),
      sel: mix(accent, bg, 0.2), edge: "#" + accent, green: "#" + green, red: "#" + red, yellow: "#" + yellow,
      font: "", radius: "0px", border: "2px",
    };
  }

  // what magpie's windows draw with (internal/omarchy Vars), as the page's
  // palette; a value that isn't a plain colour, length or font list is refused
  const plain = /^[\w\s#%.,()"'-]{1,200}$/;
  function fromMagpie(th) {
    const v = (th && th.vars) || {};
    const p = {
      name: String(th.name || ""), mode: th.mode === "light" ? "light" : "dark",
      bg: v["--bg"], fg: v["--fg"], hi: v["--fg-hi"] || v["--fg"], dim: v["--muted"], accent: v["--accent"], accentFg: v["--accent-fg"],
      card: v["--card"], code: v["--skel"] || v["--card"], line: v["--line"], sel: v["--om-sel"] || v["--accent-soft"], edge: v["--om-edge"] || v["--accent"],
      green: v["--green"], red: v["--red"], yellow: v["--amber"], font: v["--font"] || "", radius: v["--om-radius"] || "0px", border: v["--om-border"] || "2px",
    };
    for (const [k, x] of Object.entries(p)) if (k !== "name" && k !== "font" && !(typeof x === "string" && plain.test(x))) return null;
    if (p.font && !plain.test(p.font)) p.font = "";
    return p;
  }

  let parsing = true; // this script runs while the page is parsed
  let style = null, cur = null;
  function paint(p) {
    cur = p;
    if (!root.classList.contains("omarchy")) {
      root.classList.add("omarchy");
      // the look itself, before the page paints when this runs in its head
      if (parsing && document.readyState === "loading") document.write('<link rel="stylesheet" href="/omarchy.css">');
      else document.head.append(Object.assign(document.createElement("link"), { rel: "stylesheet", href: "/omarchy.css" }));
    }
    const vars = {
      "--bg": p.bg, "--bg2": p.card, "--card": p.card, "--ink": p.hi, "--ink2": p.fg, "--ink3": p.dim, "--line": p.line,
      "--btn": p.accent, "--btn-ink": p.accentFg, "--code": p.code, "--warn": p.yellow, "--shadow": "none",
      "--om-accent": p.accent, "--om-sel": p.sel, "--om-edge": p.edge, "--om-green": p.green, "--om-red": p.red, "--om-yellow": p.yellow,
      "--om-radius": p.radius, "--om-border": p.border, "--om-font": p.font || "monospace",
    };
    if (!style) {
      style = document.createElement("style");
      style.id = "omarchy-theme";
      document.head.append(style);
    }
    style.textContent = `:root.omarchy { ${Object.entries(vars).map(([k, v]) => `${k}: ${v} !important;`).join(" ")} color-scheme: ${p.mode}; }`;
    root.dataset.theme = p.mode;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = p.bg;
    render();
    shots();
  }

  // magpie's screenshots as magpie draws itself on Omarchy: its own windows
  // shot in tokyo-night (dark) and catppuccin-latte (light), in place of the
  // page's, as wide as before; shots not taken there keep the page's
  const SHOTS = {
    "add": [2016, 1600], "agents": [2080, 1078], "import": [1280, 1120], "panel": [880, 1000], "picker": [880, 1320],
    "providers": [2080, 1004], "routing": [2080, 1666], "routing-zh": [2080, 1592], "usage": [2080, 1004],
    "nested-routing": [2080, 1434], "nested-routing-zh": [2080, 1362], "intent-rules": [2012, 2150], "intent-rules-zh": [2012, 2088],
    "intent-routing-hit": [2080, 1220], "intent-routing-hit-zh": [2080, 1112], "intent-trace": [2016, 964], "intent-trace-zh": [2016, 892],
  };
  const ZH = new Set(["add", "agents", "import", "routing", "usage", "nested-routing", "intent-rules", "intent-routing-hit", "intent-trace"]);
  let shot = false;
  function shots() {
    if (shot) return;
    if (document.readyState === "loading") { document.addEventListener("DOMContentLoaded", shots, { once: true }); return; }
    shot = true;
    for (const img of document.querySelectorAll('img[src^="/img/"]')) {
      const m = img.getAttribute("src").match(/^\/img\/([a-z]+(?:-[a-z]+)*?)(-zh)?-(dark|light)\.png$/);
      if (!m || !SHOTS[m[1]] || (m[2] && !ZH.has(m[1]))) continue;
      const [w, h] = SHOTS[m[1] + (m[2] || "")] || SHOTS[m[1]];
      const width = +img.getAttribute("width");
      if (width) img.setAttribute("height", Math.round((width * h) / w));
      img.src = `/img/omarchy/${m[1]}${m[2] || ""}-${m[3]}.png`;
    }
  }
  // the page's own light/dark switch follows the system; Omarchy's mode wins
  new MutationObserver(() => {
    if (!cur) return;
    if (root.dataset.theme !== cur.mode) root.dataset.theme = cur.mode;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = cur.bg;
  }).observe(root, { attributes: true, attributeFilter: ["data-theme"] });

  function choose() {
    if (st.pick && THEMES[st.pick]) return builtin(st.pick);
    const live = st.live && fromMagpie(st.live);
    if (live) return live;
    return builtin(GUESS[scheme()]);
  }
  const light = matchMedia("(prefers-color-scheme: light)");
  function scheme() { return light.matches ? "light" : "dark"; }

  // magpie on this computer: "live" (it answered), "ask" (the browser will
  // ask the reader first), "denied" (the reader said no), "absent" (no magpie
  // answered), "" (not asked yet)
  let status = "", timer = 0, reached = false, missed = 0, asks = 0;
  async function allowed() {
    for (const name of ["loopback-network", "local-network-access"]) {
      try { return (await navigator.permissions.query({ name })).state; } catch {}
    }
    return "granted"; // a browser that doesn't ask (Firefox, Safari)
  }
  async function ask(byReader) {
    clearTimeout(timer);
    const n = ++asks; // a later ask (both permissions turning) polls in its place
    if (!byReader && !reached) {
      const s = await allowed();
      if (s !== "granted") { status = s === "denied" ? "denied" : "ask"; render(); return; }
    }
    try {
      const ctl = new AbortController();
      // the reader may be answering the browser's question meanwhile
      const t = setTimeout(() => ctl.abort(), byReader ? 120000 : 5000);
      const r = await fetch(GATEWAY, { cache: "no-store", signal: ctl.signal });
      clearTimeout(t);
      if (!r.ok) throw new Error(r.status);
      const th = await r.json();
      const p = fromMagpie(th);
      if (!p) throw new Error("theme");
      status = "live";
      missed = 0;
      reached = true; // the browser let it through: asked again freely
      const keep = { name: th.name, mode: th.mode, stamp: th.stamp, vars: th.vars };
      const changed = !st.live || st.live.stamp !== th.stamp;
      st.live = keep;
      save();
      if (!st.pick && (changed || !cur || cur.name !== p.name)) paint(p); else render();
    } catch {
      // once reached, one slow answer (Omarchy setting a theme reloads
      // Hyprland meanwhile) isn't magpie gone: asked again as often
      if (byReader || !reached || ++missed > 1) {
        status = byReader && (await allowed()) === "denied" ? "denied" : "absent";
        render();
      }
    }
    // followed while the page shows: a theme picked in Omarchy's menu comes
    // through within seconds
    if (n === asks && !st.pick && document.visibilityState === "visible" && status !== "ask" && status !== "denied") {
      timer = setTimeout(() => ask(false), status === "live" || reached ? 3000 : 30000);
    }
  }
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && cur && !st.pick && status !== "ask" && status !== "denied") ask(false);
  });

  // until magpie answers, Omarchy's default for light or dark follows the
  // desktop as it turns (a theme set in Omarchy's menu turns it), and a page
  // the browser lets through later (its prompt, its site settings) starts
  // following then, both without a reload
  let watching = false;
  function watch() {
    if (watching) return;
    watching = true;
    light.addEventListener("change", () => {
      if (!cur || st.pick || status === "live") return;
      const live = st.live && st.live.mode === scheme() && fromMagpie(st.live);
      paint(live || builtin(GUESS[scheme()]));
    });
    for (const name of ["loopback-network", "local-network-access"]) {
      navigator.permissions?.query({ name }).then((p) => p.addEventListener("change", () => {
        if (!cur || st.pick || status === "live") return;
        if (p.state === "granted") ask(false);
        else if (p.state === "denied") { clearTimeout(timer); status = "denied"; render(); }
      }), () => {});
    }
  }

  function start() {
    watch();
    paint(choose());
    if (!st.pick) ask(false);
  }

  // the chip in the corner, Omarchy's logo and the theme: open, it follows
  // the computer, picks a built-in theme, or turns the look off
  let bar = null, open = false;
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  function render() {
    // before <body> (Omarchy found while the head is parsed): once it's there
    if (!document.body) { document.addEventListener("DOMContentLoaded", render, { once: true }); return; }
    if (!bar) {
      bar = document.createElement("div");
      bar.className = "om-bar";
      document.body.append(bar);
      bar.addEventListener("click", onClick);
      // the clicked button may be gone by now (render replaced it): the path
      // the click took tells whether it was inside
      document.addEventListener("click", (e) => { if (open && !e.composedPath().includes(bar)) { open = false; render(); } });
      document.addEventListener("keydown", (e) => { if (open && e.key === "Escape") { open = false; render(); bar.querySelector(".om-chip")?.focus(); } });
    }
    if (!cur) {
      bar.innerHTML = `<button class="om-chip om-off" type="button" title="${esc(T("Omarchy look", "Omarchy 风格", "Omarchy スタイル"))}"><span class="om-logo" aria-hidden="true"></span></button>`;
      return;
    }
    const following = !st.pick;
    const note = {
      live: T(`Following ${cur.name} via magpie`, `正在跟随 ${cur.name}（来自 magpie）`, `magpie 経由で ${cur.name} に追従中`),
      ask: T("Ask magpie on this computer for the theme", "向本机的 magpie 询问当前主题", "このコンピュータの magpie にテーマを尋ねる"),
      denied: T("The browser blocks this page from reaching magpie", "浏览器禁止了本页访问本机的 magpie", "ブラウザがこのページから magpie への接続をブロックしています"),
      absent: T("magpie isn't running on this computer", "本机没有运行 magpie", "このコンピュータで magpie が動いていません"),
      "": T("Asking magpie…", "正在询问 magpie…", "magpie に問い合わせ中…"),
    }[status];
    const rows = Object.keys(THEMES).map((n) => {
      const [mode, bg, fg, , , accent] = THEMES[n].split(" ");
      const on = st.pick === n ? " on" : "";
      return `<button class="om-row${on}" type="button" role="menuitemradio" aria-checked="${!!on}" data-pick="${n}"><span class="om-sw" style="background:#${bg};color:#${fg}"><i style="background:#${accent}"></i><i style="background:#${fg}"></i></span>${n}${mode === "light" ? ' <span class="om-dim">light</span>' : ""}</button>`;
    }).join("");
    // not let through yet: the chip says what one click does
    const cta = status === "ask" && following && !open
      ? `<button class="om-cta" type="button" data-ask title="${esc(T("Your browser will ask to let this page reach magpie on this computer", "浏览器会询问是否允许本页访问本机的 magpie", "このページがこのコンピュータの magpie に接続してよいか、ブラウザが尋ねます"))}">${esc(T("Follow my Omarchy theme", "跟随 Omarchy 主题", "Omarchy のテーマに合わせる"))}</button>`
      : "";
    bar.innerHTML = cta + `<button class="om-chip${status === "live" && following ? " live" : ""}" type="button" aria-haspopup="menu" aria-expanded="${open}"><span class="om-logo" aria-hidden="true"></span><span class="om-name">${esc(cur.name || "omarchy")}</span></button>`
      + (open ? `<div class="om-menu" role="menu">
        <div class="om-h">Omarchy</div>
        <button class="om-row${following ? " on" : ""}" type="button" role="menuitemradio" aria-checked="${following}" data-pick=""><span class="om-sw om-follow"></span>${esc(T("Follow this computer", "跟随这台电脑", "このコンピュータに合わせる"))}</button>
        <div class="om-note">${status === "ask" ? `<button class="om-ask" type="button" data-ask>${esc(note)} →</button>` : esc(note)}${status === "absent" ? ` · <a href="/#download">${esc(T("Get magpie", "下载 magpie", "magpie を入手"))}</a>` : ""}</div>
        <div class="om-h">${esc(T("Themes", "主题", "テーマ"))}</div>
        <div class="om-list">${rows}</div>
        <button class="om-row om-quit" type="button" data-off>${esc(T("Turn off Omarchy look", "关闭 Omarchy 风格", "Omarchy スタイルをオフ"))}</button>
      </div>` : "");
  }
  function onClick(e) {
    const b = e.target.closest("button");
    if (!b) return;
    if (b.classList.contains("om-chip")) {
      if (!cur) { st.on = true; save(); start(); return; }
      open = !open;
      render();
      bar.querySelector(".om-row.on")?.scrollIntoView({ block: "nearest" });
      return;
    }
    if (b.hasAttribute("data-ask")) { st.pick = ""; save(); status = ""; render(); ask(true); return; }
    if (b.hasAttribute("data-off")) { st.on = false; save(); location.reload(); return; }
    if (b.hasAttribute("data-pick")) {
      st.pick = b.dataset.pick;
      save();
      if (st.pick) { clearTimeout(timer); paint(builtin(st.pick)); }
      else { paint(choose()); if (status !== "live") { status = ""; render(); ask(true); } else ask(false); }
    }
  }

  if (st.on === true || (st.on !== false && st.seen)) start();
  else if (st.on !== false && document.fonts && window.FontFace) {
    new FontFace("omarchy-probe", 'local("omarchy")').load().then(() => { st.seen = true; save(); start(); }, () => {});
  }
  parsing = false;
  // Omarchy found, the look turned off: the chip alone, to turn it back on
  const off = st.on === false && st.seen;
  if (cur || off) render();
})();
