// Run with Node's test runner and Playwright on the module path; see README.md.
// An icon beside words sits level with them (the owner, 11-23: 「图标与文字
// 没有水平对齐，这个问题好像出现过无数次，根治这个问题」). Discover's
// person beside a user count sat 1.5 device pixels below the star beside
// the next count: the layout centres every glyph's box on its words the
// same way, but the person's ink sat low in its 16-unit box and the
// star's high. The fixes before were nudges, one spot at a time, each
// fitted to one glyph. Now every small glyph beside a line of words, on
// every page this visits, has its ink centred in its own box (ink-align.cjs)
// within ALLOWED of a 16-unit box, none is moved off the line by hand
// (a translate or a relative top: .pm-dl's .1em sat the person 1.5 device
// px low in the real app), and the offenders are listed by where they are. Geometry, so the same in each engine and at any scale; both
// engines and every language still run, as each draws its own rows.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { inkPairs } = require("./ink-align.cjs");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const ALLOWED = 0.1; // of a 16-unit box
// drawn off their box's middle by design, each with why
const ALLOW = [
  // a puzzle piece: the knob on top stands out of the square, and it is
  // the square that is centred, so the piece reads as sitting on the line
  ["span.puzzle > svg", "More in Plugins: knob above its centred square"],
];
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';
const RIVER = "@magpie-community/opencode-river-auth";
const KILO = "@magpie-community/agent-kilo";

function server(lang) {
  const listings = [
    { package: RIVER, name: "River", providers: ["river"], community: true, github: { stars: 7, owner: "a" }, summary: { en: "River pay-as-you-go inference." } },
    { package: KILO, name: "Kilo Code", kind: "agent", community: true, github: { stars: 0, owner: "a" }, summary: { en: "Kilo Code on magpie's Agents page." } },
  ];
  const npm = { [RIVER]: { version: "0.1.3", weekly: 40 }, [KILO]: { version: "2.5.2", weekly: 3 } };
  const pstate = { bun: true, bunVersion: "1.3.0", plugins: [] };
  const settings = { lang, theme: "light", tray: "panel", currency: "usd" };
  const base = { input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, errors: 0, calls: 7 };
  const agents = [{ id: "alma", name: "Alma", icon: "file:logo.svg", ...base }];
  const models = [{ id: "m1", name: "claude-haiku-4.5", sub: "Kiro", icon: "file:logo.svg", ...base }];
  const accounts = [{ id: "a1", name: "yetone", sub: "WorkBuddy", icon: "file:logo.svg", ...base }];
  const lib = {
    dir: "/u/.magpie/library", backups: "/u/.magpie/backups", home: "/u", mcpHub: false, hubPath: "/mcp/magpie/hub/",
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: "/u/.claude/skills", mcp: "/u/.claude.json" }, { id: "qwen", name: "Qwen Code", skills: "/u/.qwen/skills" }],
    instructions: { agents: [], sets: [] }, foundSkills: [], problems: [], foundServers: [], projects: [],
    skills: [{ name: "pdf", description: "Read and write PDF files.", agents: ["claude"] }],
    servers: [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: ["claude"] }],
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    }
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", calls: [] }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 7, errors: 0, input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, unpriced: 0, accounts, callerKeys: [], agents, models, series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname === "/api/plugins") return json(pstate);
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
    if (url.pathname === "/api/plugins/page") return json({ readme: "# River", updated: "2026-10-30T00:00:00Z" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// each page, and what shows it is drawn
const PAGES = [
  ["plugins", `#view-plugins .pm-card[data-pkg="${KILO}"] .pm-ver`],
  ["library", "#view-library .lib-body .row-head"],
  ["usage", "#usageAccounts .row.stat"],
  ["providers", "#view-providers"],
  ["settings", "#view-settings .row"],
];
const LANGS = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "ja"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every glyph beside words is centred in its box", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async (st) => {
        const ctx = await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        const bad = [], seen = new Set();
        let count = 0;
        await page.goto("http://magpie.test/");
        for (const [view, ready] of PAGES) {
          await page.locator(view === "settings" ? "#prefs" : `#nav button[data-view="${view}"]`).first().click();
          await page.locator(ready).first().waitFor().catch((e) => { throw new Error(view + ": " + ready + " never drew: " + e.message); });
          await page.evaluate(() => document.fonts.ready);
          await page.mouse.move(0, 0);
          const pairs = await page.evaluate(`(${inkPairs.toString()})(document.body)`);
          for (const p of pairs) {
            count++;
            if (ALLOW.some(([sel]) => p.where.includes(sel))) continue;
            const key = view + " " + p.where + " " + JSON.stringify(p.words);
            if (seen.has(key)) continue;
            seen.add(key);
            if (process.env.INK_LOG) console.log(engine, lang, p.units.toFixed(2), view, p.where, JSON.stringify(p.words), "|", p.path);
            if (Math.abs(p.shift) > 0.01) bad.push(`${view}: ${p.where} beside ${JSON.stringify(p.words)} is moved ${p.shift.toFixed(2)}px off the line by hand: centre its ink in its box instead`);
            if (Math.abs(p.units) > ALLOWED) bad.push(`${view}: ${p.where} beside ${JSON.stringify(p.words)} is ${Math.abs(p.units).toFixed(2)}/16 ${p.units > 0 ? "low" : "high"} in its box (${p.path})`);
          }
        }
        assert.ok(count >= 20, `only ${count} icons beside words were found`);
        assert.deepEqual(bad, [], `${bad.length} glyphs off their box's middle by more than ${ALLOWED}/16, or moved by hand`);
        assert.deepEqual(errors, []);
      });
    }
  });
}
