// Run with Node's test runner and Playwright on the module path; see README.md.
// An icon beside a column of words taller than it meets the top of the
// first line's letters; beside words no taller than it, it stays centred
// on them (the owner, 11-28: 「对于这种图标右边的文字是多行的，且它整个高度
// 高于这个图标的，那么这个图标跟右边文字的对齐方式不应该是垂直方向居中，而是
// 靠文字顶部对齐」, then 「另外要把它变成一个强制的代码规范」). The Installed
// list's logos were centred on their rows, so a five-line Providers Hub
// sat its logo low beside a two-line River's, and the column of logos went
// up and down with the rows.
//
// This sweeps every page with rows of an icon beside words, and finds the
// pairs by geometry, not by class: a box of 12 to 56px, near square, with
// no words of its own, and the flex item beside it holding words. Where
// those words run to more than one line and stand taller than the icon,
// the icon's top (its tile's or its picture's as laid out, a glyph's ink)
// meets the top of the first line's capitals: an H put before its words,
// read off the rendered pixels at 4x, as the words' own letters stand at
// different heights (an O over the line, a CJK line 1 to 1.5px over it).
// They meet within 0.75px: a row at a fractional height is snapped to the
// device's pixels, its text and its picture apart, by up to half a pixel.
// A glyph no taller than that line is centred on it, within a pixel.
// Where the words are one line no taller than the icon, their middles meet
// within a pixel. Every offender is named by page, row and offset. A pair
// drawn otherwise on purpose goes in ALLOW with why.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
// pairs drawn otherwise on purpose: a piece of the icon's or the words'
// path, and why
const ALLOW = [
  // the Add sheet's partner tiles: partner-tile-top.test.cjs holds the
  // logo to the name's own letters, on a whole pixel, 0.5 to 1px over the
  // capitals
  ["button.tile.two > span.ic", "partner-tile-top.test.cjs"],
  // a Usage row (by agent, model, account): usage-row-logo-top.test.cjs
  // holds its logo to the name's own letters, a CJK name's or a lower-case
  // one's too, which stand up to 2px over the capitals
  ["div.row.stat > ", "usage-row-logo-top.test.cjs"],
];
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';
const RIVER = "@magpie-community/opencode-river-auth";
const KILO = "@magpie-community/agent-kilo";
const HUB = "@magpie-community/opencode-providers-hub";
const NOTES = "@magpie-community/notes-tools";
const hubSubs = ["OpenRouter", "Groq", "Cerebras", "Together", "Fireworks", "DeepInfra", "Mistral", "Hyperbolic", "Novita", "SambaNova", "Nebius", "Chutes"];
const ago = (m) => new Date(Date.now() - m * 60e3).toISOString();

function server(lang) {
  const listings = [
    { package: RIVER, name: "River", providers: ["river"], community: true, github: { stars: 7, owner: "a" }, summary: { en: "River pay-as-you-go inference." } },
    { package: KILO, name: "Kilo Code", kind: "agent", community: true, github: { stars: 0, owner: "a" }, summary: { en: "Kilo Code on magpie's Agents page, with every model magpie serves." } },
    { package: HUB, name: "Providers Hub", providers: hubSubs.map((n) => n.toLowerCase()), community: true, github: { stars: 120, owner: "a" }, summary: { en: "Twelve pay-as-you-go providers in one plugin, each with its own key and its own list of models." } },
  ];
  const npm = { [RIVER]: { version: "0.1.3", weekly: 40 }, [KILO]: { version: "2.5.2", weekly: 3 }, [HUB]: { version: "0.4.2", weekly: 900 } };
  // the owner's Installed list: a five-line Providers Hub among two- and
  // three-line rows
  const pstate = { bun: true, bunVersion: "1.3.0", plugins: [
    { spec: "npm:" + RIVER, ownName: "River", version: "0.1.3", providers: ["River"], moved: [], named: true },
    { spec: "npm:" + HUB, ownName: "Providers Hub", version: "0.4.2", providers: hubSubs, moved: [], named: true, tools: [{ name: "hub_models", description: "list models" }, { name: "hub_usage" }] },
    { spec: "npm:" + NOTES, ownName: "Notes", version: "1.0.0", providers: [], named: true, tools: [{ name: "note_add" }], page: { reads: [] }, inMagpieOnly: true },
    { spec: "npm:" + KILO, ownName: "Kilo Code", version: "2.5.2", providers: [], named: true, isAgent: true, agent: { id: "kilo", name: "Kilo Code" } },
  ] };
  const pluginSubs = [
    { id: "river", pid: "river", name: "River", spec: "npm:" + RIVER, signedIn: true, methods: [], models: 0, icon: "file:logo.svg" },
    ...hubSubs.map((n, i) => ({ id: "hub-" + i, pid: "hub-" + i, name: n, spec: "npm:" + HUB, signedIn: i % 3 !== 2, methods: [], models: 0, icon: "file:logo.svg" })),
  ];
  const settings = { lang, theme: "light", tray: "panel", currency: "usd" };
  const base = { input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, errors: 0, calls: 7 };
  const agents = [{ id: "alma", name: "Alma", icon: "file:logo.svg", ...base }];
  const models = [{ id: "m1", name: "claude-haiku-4.5", sub: "Kiro", icon: "file:logo.svg", ...base }];
  const accounts = [{ id: "a1", name: "Yetone", sub: "WorkBuddy", icon: "file:logo.svg", ...base }];
  const lib = {
    dir: "/u/.magpie/library", backups: "/u/.magpie/backups", home: "/u", mcpHub: false, hubPath: "/mcp/magpie/hub/",
    agents: [{ id: "claude", name: "Claude Code", icon: "file:logo.svg", skills: "/u/.claude/skills", mcp: "/u/.claude.json" }, { id: "qwen", name: "Qwen Code", icon: "file:logo.svg", skills: "/u/.qwen/skills" }],
    instructions: { agents: [], sets: [] }, foundSkills: [], problems: [], foundServers: [], projects: [],
    skills: [
      { name: "pdf", description: "Read and write PDF files.", agents: ["claude"] },
      { name: "frontend-design", description: "Create distinctive, production-grade frontend interfaces with high design quality, avoiding generic looks, for components, pages and whole applications.", agents: ["claude", "qwen"] },
    ],
    servers: [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: ["claude"] }],
  };
  const fields = [{ key: "model", label: "model", value: "claude-sonnet-4.5", options: [{ value: "claude-sonnet-4.5", label: "claude-sonnet-4.5" }] }];
  const stateAgents = [
    { id: "claude", name: "Claude Code", icon: "file:logo.svg", path: "/u/.claude/settings.json", fields },
    { id: "codex", name: "Codex", icon: "file:logo.svg", path: "/u/.codex/config.toml", fields },
  ];
  const providers = [
    { id: "openai", name: "OpenAI", icon: "file:logo.svg", preset: "openai", host: "api.openai.com", models: [{ id: "gpt-5", on: true }], agents: [], key: { set: true, masked: "sk-…ab12" } },
    { id: "codex-sub", name: "ChatGPT Pro", icon: "file:logo.svg", host: "chatgpt.com", models: [{ id: "gpt-5", on: true }, { id: "gpt-5-codex", on: true }], agents: [], key: { set: false }, account: { user: "someone.with.a.long.address@example.com", agentName: "Codex", plan: "Pro", logins: [] } },
  ];
  const partners = [{ id: "kimi-p", name: "Kimi Code", icon: "file:logo.svg", kind: "partner", sponsored: true, notes: { en: "One plan, every agent shares it", zh: "一份订阅，所有 agent 共享额度", ja: "ひとつのプランを全エージェントで" }, chat: "https://api.kimi.example.com/v1" }];
  const presets = [{ id: "openai", name: "OpenAI", icon: "file:logo.svg", kind: "vendor", chat: "https://api.openai.example.com/v1" }];
  const sessions = {
    agents: [{ agent: "claude", count: 2, deletable: true, name: "Claude Code", icon: "file:logo.svg" }], agent: "claude", terminal: false, trashDir: "~/x/trash/sessions", trash: [],
    sessions: [
      { agent: "claude", id: "s1", cwd: "/work/app", title: "fix the build", start: ago(90), last: ago(60), resume: "cd /work/app && claude --resume s1", path: "~/.claude/projects/-work-app/s1.jsonl", size: 2048, messages: 4, files: 1, deletable: true },
      { agent: "claude", id: "s2", cwd: "/work/a-rather-long-project-folder-name", title: "Find why the release workflow's notarisation step times out on the second try, and make it retry with the right credentials", start: ago(300), last: ago(200), resume: "claude --resume s2", path: "~/.claude/projects/-work-b/s2.jsonl", size: 8192, messages: 40, files: 6, deletable: true },
    ],
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: stateAgents, profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/partner") return route.fulfill({ status: 204, body: "" });
    if (url.pathname === "/api/providers") return json({ providers, presets: [...partners, ...presets], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", calls: [] }, plugins: pluginSubs });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204, body: "" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 7, errors: 0, input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, unpriced: 0, accounts, callerKeys: [], agents, models, series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname === "/api/sessions/manage") return json(sessions);
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/plugins") return json(pstate);
    if (url.pathname === "/api/plugins/builtin") return json([{ id: "sessions", name: "Sessions", sub: "Every agent's past sessions, searchable", on: true }, { id: "library", name: "Library", sub: "Skills and MCP servers, given to every agent", on: true }]);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/market") return json({ listings, state: pstate });
    if (url.pathname === "/api/plugins/counts") return json({ plugins: { [RIVER]: { users: 3, likes: 2 }, [KILO]: { users: 2500, likes: 6 } }, liked: [] });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// Every icon beside words on the page, found by geometry: an icon is a
// box of 12 to 56px, near square, with no words of its own, shown; the
// words are the flex item after it in a row, starting within 24px of its
// right edge. Returns each pair with the icon's and the words' boxes, the
// words' lines, and their first line (the first run of words).
function iconPairs() {
  const out = [];
  const name = (e) => {
    const p = [];
    for (let n = e; n && n !== document.body && p.length < 4; n = n.parentElement) {
      let s = n.tagName.toLowerCase();
      if (n.id) { s += "#" + n.id; p.unshift(s); break; }
      const c = [...n.classList].slice(0, 2).join(".");
      if (c) s += "." + c;
      p.unshift(s);
    }
    return p.join(" > ");
  };
  const shown = (e) => {
    const r = e.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    for (let n = e; n; n = n.parentElement) {
      const cs = getComputedStyle(n);
      if (cs.visibility === "hidden" || cs.display === "none" || +cs.opacity === 0) return false;
    }
    return true;
  };
  const words = (n) => {
    const all = [], w = document.createTreeWalker(n, NodeFilter.SHOW_TEXT);
    for (let t = w.nextNode(); t; t = w.nextNode()) {
      if (!t.data.trim() || !t.parentElement || t.parentElement.closest("svg, [aria-hidden=true]") || !shown(t.parentElement)) continue;
      const rg = document.createRange();
      rg.selectNodeContents(t);
      const rects = [...rg.getClientRects()].filter((x) => x.width > 0.5 && x.height > 0.5);
      if (rects.length) all.push({ t, rects });
    }
    return all;
  };
  // a picture: an image, a glyph, or a box painted by one
  const drawn = (n) => {
    if (n.matches("img, svg")) return true;
    const cs = getComputedStyle(n);
    return cs.backgroundImage !== "none" || (cs.maskImage || cs.webkitMaskImage || "none") !== "none";
  };
  const seenIcon = new Set();
  window.ittPairs = [];
  for (const e of document.querySelectorAll("body *")) {
    if (e.closest("svg") && e.tagName.toLowerCase() !== "svg") continue;
    const r = e.getBoundingClientRect();
    if (r.width < 12 || r.width > 56 || r.height < 12 || r.height > 56 || Math.abs(r.width - r.height) > 3) continue;
    if (e.textContent.trim() || e.matches("input, textarea, select, label, [role=switch]")) continue;
    if (![e, ...e.querySelectorAll("*")].some(drawn)) continue;
    const parent = e.parentElement, pcs = parent && getComputedStyle(parent);
    if (!pcs || !(/flex/.test(pcs.display) && /^row/.test(pcs.flexDirection)) && !/grid/.test(pcs.display)) continue;
    let sib = e.nextElementSibling;
    while (sib && !shown(sib)) sib = sib.nextElementSibling;
    if (!sib || !shown(e)) continue;
    const s = sib.getBoundingClientRect();
    if (s.left < r.right - 1 || s.left - r.right > 24 || s.top > r.bottom || s.bottom < r.top) continue;
    const ws = words(sib);
    if (!ws.length) continue;
    // its lines: runs of words that overlap on y
    const rects = ws.flatMap((x) => x.rects).sort((a, b) => a.top - b.top);
    const lines = [];
    for (const x of rects) {
      const l = lines[lines.length - 1];
      if (l && x.top < l.bottom - 2) l.bottom = Math.max(l.bottom, x.bottom);
      else lines.push({ top: x.top, bottom: x.bottom });
    }
    let icon = e;
    // an icon inside an icon (a logo's picture) is the outer one's
    if ([...seenIcon].some((x) => x.contains(e))) continue;
    seenIcon.add(icon);
    const first = ws.reduce((a, b) => (b.rects[0].top < a.rects[0].top - 2 ? b : a));
    const f = first.rects[0];
    // what paints the icon: a tile of its own colour, or the picture in it
    // (an image, a mask, a background); its laid-out top is the icon's, as
    // a picture is snapped to whole pixels when painted. A glyph (svg) is
    // read off its ink, as its strokes stand inside its box.
    const filled = (n) => {
      const cs = getComputedStyle(n), bg = cs.backgroundColor.match(/[\d.]+/g) || [];
      return (bg.length > 3 ? +bg[3] : bg.length ? 1 : 0) > 0.05 || parseFloat(cs.borderTopWidth) > 0;
    };
    const paint = filled(icon) ? icon : [icon, ...icon.querySelectorAll("*")].find(drawn);
    const framed = !paint.matches("svg");
    window.ittPairs.push([icon, sib, first.t, paint]);
    out.push({
      icon: name(icon), words: name(sib), text: first.t.data.trim().slice(0, 28),
      framed,
      ib: { x: r.left, y: r.top, w: r.width, h: r.height }, sb: { x: s.left, y: s.top, w: s.width, h: s.height },
      line: { x: f.left, y: f.top, w: f.width, h: f.height }, lines: lines.length,
    });
  }
  return out;
}

// the top of the ink in each box of a clip, in CSS px from the clip's top,
// on a 4x screenshot: the first row with a pixel off the clip's commonest
// colour
async function inkTops(page, clip, parts) {
  const png = (await page.screenshot({ clip })).toString("base64");
  return page.evaluate(async ({ png, clip, parts }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / clip.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const count = new Map();
    for (let i = 0; i < d.length; i += 4) { const key = (d[i] << 16) | (d[i + 1] << 8) | d[i + 2]; count.set(key, (count.get(key) || 0) + 1); }
    const bgKey = [...count].sort((a, b) => b[1] - a[1])[0][0], bg = [bgKey >> 16, (bgKey >> 8) & 255, bgKey & 255];
    return parts.map((b) => {
      const x0 = Math.max(0, Math.round((b.x - clip.x) * k)), x1 = Math.min(c.width, Math.round((b.x + b.w - clip.x) * k));
      const y0 = Math.max(0, Math.floor((b.y - clip.y) * k)), y1 = Math.min(c.height, Math.round((b.y + b.h - clip.y) * k));
      for (let y = y0; y < y1; y++) for (let xx = x0; xx < x1; xx++) {
        const i = (y * c.width + xx) * 4;
        if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 120) return clip.y + y / k;
      }
      return -1;
    });
  }, { png, clip, parts });
}

// each page, how to get there, and what shows it is drawn
const PAGES = [
  ["agents", "/?view=agents", "#view-agents .row.agent"],
  ["providers", "/?view=providers", "#view-providers .row.provider"],
  ["add sheet", "/?view=providers", "#addSheet .tile", async (page) => { await page.locator("#view-providers .row.provider").first().waitFor(); await page.locator("#addProvider").click(); }],
  ["plugins built-in", "/?view=plugins", "#builtinList .row", (page) => page.locator(":is(.lib-tabs .opt, .nav-s):visible").nth(0).click()],
  ["plugins discover", "/?view=plugins", `#view-plugins .pm-card[data-pkg="${HUB}"]`, (page) => page.locator(":is(.lib-tabs .opt, .nav-s):visible").nth(1).click()],
  ["plugins installed", "/?view=plugins", "#view-plugins .pm-row >> nth=3", (page) => page.locator(":is(.lib-tabs .opt, .nav-s):visible").nth(2).click()],
  ["library", "/?view=library", "#view-library .lib-body .row-head"],
  ["sessions", "/?view=sessions", "#view-sessions .row"],
  ["usage", "/?view=usage", "#usageAccounts .row.stat"],
  ["settings", "/?view=settings", "#view-settings .row"],
  ["panel", "/?mode=panel", "#agents .row", null, 440],
];
const LANGS = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"];
const WIDTHS = process.env.WIDTHS ? process.env.WIDTHS.split(",").map(Number) : [1180, 420];
const ONLY = process.env.PAGES ? process.env.PAGES.split(",") : null;
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an icon beside taller words meets their top, beside one line their middle", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) for (const width of WIDTHS) {
      await t.test(lang + " " + width, async (st) => {
        const bad = [], seen = new Set(), errors = [];
        let tall = 0, short = 0;
        for (const [view, at, ready, act, own] of PAGES) {
          if (ONLY && !ONLY.includes(view)) continue;
          // the panel has its own width; it is measured once a language
          if (own && width !== WIDTHS[0]) continue;
          const ctx = await browser.newContext({ viewport: { width: own || width, height: 900 }, deviceScaleFactor: 4, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(10000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(view + ": " + e.message));
            await page.route("**/*", server(lang));
            await page.goto("http://magpie.test" + at);
            if (act) await act(page);
            await page.locator(ready).first().waitFor().catch((e) => { throw new Error(view + ": " + ready + " never drew: " + e.message); });
            await page.evaluate(() => document.fonts.ready);
            await page.evaluate(() => Promise.all([...document.images].map((i) => i.decode().catch(() => {}))));
            await page.mouse.move(0, 0);
            await page.waitForTimeout(150);
            const pairs = await page.evaluate(`(${iconPairs.toString()})()`);
            for (let i = 0; i < pairs.length; i++) {
              const p = pairs[i];
              const where = `${p.icon} | ${p.words}`;
              const isTall = p.lines > 1 && p.sb.h > p.ib.h + 1;
              const key = `${view} ${where} ${p.text}`;
              if (seen.has(key)) continue;
              seen.add(key);
              if (ALLOW.some(([sel]) => where.includes(sel))) continue;
              if (!isTall) {
                // one line, or no taller than the icon: middles meet
                if (p.lines > 1) continue;
                short++;
                const d = (p.ib.y + p.ib.h / 2) - (p.sb.y + p.sb.h / 2);
                if (process.env.SHOW) console.log(engine, lang, width, view, "short", d.toFixed(2), where, JSON.stringify(p.text));
                if (Math.abs(d) > 1) bad.push(`${view}: ${where} (${JSON.stringify(p.text)}): one line, the icon's middle is ${Math.abs(d).toFixed(2)}px ${d > 0 ? "below" : "above"} the words'`);
                continue;
              }
              tall++;
              // brought into view, and both read again where they are now
              const q = await page.evaluate((i) => {
                const [e, , tn, pe] = window.ittPairs[i];
                e.scrollIntoView({ block: "center" });
                // the line's capitals, read off an H put before its words
                // for the shot: the words' own letters stand at different
                // heights, an O over the line and a y under it
                window.ittWords = tn.data;
                tn.data = "H" + tn.data;
                const r = e.getBoundingClientRect(), rg = document.createRange();
                rg.setStart(tn, 0); rg.setEnd(tn, 1);
                const f = rg.getBoundingClientRect();
                return { ib: { x: r.left, y: r.top, w: r.width, h: r.height }, top: pe.getBoundingClientRect().top, line: { x: f.left, y: f.top, w: f.width, h: f.height } };
              }, i);
              const words = () => page.evaluate((i) => { window.ittPairs[i][2].data = window.ittWords; }, i);
              // a glyph no taller than the line is centred on the first line
              if (q.ib.h <= q.line.h + 2) {
                await words();
                const d = (q.ib.y + q.ib.h / 2) - (q.line.y + q.line.h / 2);
                if (process.env.SHOW) console.log(engine, lang, width, view, "glyph", p.lines, d.toFixed(2), where, JSON.stringify(p.text));
                if (Math.abs(d) > 1) bad.push(`${view}: ${where} (${JSON.stringify(p.text)}, ${p.lines} lines): the glyph's middle is ${Math.abs(d).toFixed(2)}px ${d > 0 ? "below" : "above"} the first line's`);
                continue;
              }
              // on whole pixels, so the shot's rows map back exactly
              const top = Math.max(0, Math.floor(Math.min(q.ib.y, q.line.y) - 3)), left = Math.max(0, Math.floor(Math.min(q.ib.x, q.line.x) - 3));
              const clip = { x: left, y: top, width: Math.ceil(Math.max(q.ib.x + q.ib.w, q.line.x + q.line.w) + 3) - left, height: Math.ceil(Math.max(q.ib.y + q.ib.h, q.line.y + q.line.h) + 3) - top };
              const [ink, text] = await inkTops(page, clip, [q.ib, q.line]);
              await words();
              const icon = p.framed && ink >= 0 ? q.top : ink;
              const d = icon - text, room = 0.75;
              if (process.env.SHOW) console.log(engine, lang, width, view, "tall", p.lines, d.toFixed(2), where, JSON.stringify(p.text));
              if (icon < 0 || text < 0) { bad.push(`${view}: ${where} (${JSON.stringify(p.text)}): nothing drawn`); continue; }
              if (Math.abs(d) > room) bad.push(`${view}: ${where} (${JSON.stringify(p.text)}, ${p.lines} lines): the icon's top is ${Math.abs(d).toFixed(2)}px ${d > 0 ? "below" : "above"} the first line's capitals`);
            }
          } finally {
            await ctx.close();
          }
        }
        assert.ok(ONLY || tall >= 8, `only ${tall} icons beside taller words were found`);
        assert.ok(ONLY || short >= 8, `only ${short} icons beside one line were found`);
        assert.deepEqual(bad, [], `${bad.length} icons off their words`);
        assert.deepEqual(errors, []);
      });
    }
  });
}
