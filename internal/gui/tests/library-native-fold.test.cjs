// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner (11-23, a Library screenshot): "Installed in the agents
// themselves" shows only its first few rows, then folds. A long list shows
// its first 5 and "Show {n} more", the way "In your agents" folds; the click
// shows them all without scrolling the page and "Show less" folds them
// again. Open stays open for the session, across a redraw. A list of 7 or
// fewer is shown whole, and so is one plugin's own, picked from a row. On
// the Skills, MCP, Hooks and Subagents tabs, in every language, both
// engines, at 1100 and 380. No backend: the API is faked, and any write to
// it fails the test.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    { id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: true, hooks: true },
    { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: true, hooks: true },
    { id: "pi", name: "Pi", icon: "", skills: `${HOME}/.pi/agent/skills`, mcp: false, hooks: false },
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [], hooks: [],
});
const parts = (kind, prefix, n) => Array.from({ length: n }, (_, i) => ({ kind, name: prefix + "-" + String(i).padStart(2, "0"), detail: prefix + " " + i }));
const native = [
  {
    agent: "codex", agentName: "Codex", name: "big-kit", version: "1.2.0", source: "openai-bundled", scope: "user", dir: `${HOME}/.codex/plugins/cache/openai-bundled/big-kit/1.2.0`, loaded: "unknown",
    parts: [...parts("skill", "skill", 11), ...parts("mcp", "srv", 8), ...parts("hook", "hook", 9)],
  },
  {
    agent: "claude", agentName: "Claude Code", name: "agents@fixmarket", version: "0.1.0", source: "fixmarket", scope: "user", dir: `${HOME}/.claude/plugins/cache/fixmarket/agents/0.1.0`, enabled: true, loaded: "unknown",
    parts: parts("subagent", "agent", 7),
  },
  {
    agent: "pi", agentName: "Pi", name: "proj-kit", version: "2.0.0", source: "npm:proj-kit", scope: "project", project: `${HOME}/work/app`, loaded: "unknown",
    parts: [{ kind: "skill", name: "review", detail: "Review code" }],
  },
];

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, tab, writes) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (req.method() !== "GET" && url.pathname.startsWith("/api/library")) writes.push(url.pathname);
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};try{localStorage.setItem("magpie.libTab","${tab}")}catch{}` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(lib());
    if (url.pathname === "/api/library/native") return json(native);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the tab, how many native rows it lists (0 more: shown whole), and the
// big-kit row name a pick is made from
const TABS = [
  ["skills", 12, "skill-00"],
  ["mcp", 8, "srv-00"],
  ["hooks", 9, "hook-00"],
  ["subagents", 7, null],
];

const inView = (b) => b.evaluate((e) => { const r = e.getBoundingClientRect(), v = e.closest(".view").getBoundingClientRect(); return r.top >= v.top && r.bottom <= v.bottom; });
const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a long Installed in the agents themselves list is folded", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 380]) for (const [tab, total, at] of TABS) {
      await t.test(`${lang} ${width} ${tab}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], writes = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, tab, writes));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const box = page.locator("#view-library .lib-nativebox");
        const rows = box.locator(".lib-native");
        const more = box.locator("button.lib-foundmore");
        await rows.first().waitFor();
        assert.equal(await box.locator(".row-head .label").first().textContent(), tr(lang, "Installed in the agents themselves"));

        if (total <= 7) {
          // a short list is shown whole, with nothing to unfold
          assert.equal(await rows.count(), total);
          assert.equal(await more.count(), 0);
        } else {
          assert.equal(await rows.count(), 5);
          assert.equal((await more.textContent()).trim(), tr(lang, "Show {n} more", { n: total - 5 }));
          assert.equal(await more.getAttribute("aria-expanded"), "false");
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert(over <= 0, `the page scrolls sideways by ${over}px`);

          // the click shows every row, in place
          // at a phone's width the button is below the fold: the reader
          // wheels down to it (a script's scroll the page puts back)
          await page.mouse.move(width / 2, 450);
          for (let i = 0; i < 20 && !(await inView(more)); i++) { await page.mouse.wheel(0, 120); await page.waitForTimeout(60); }
          assert(await inView(more), "the button never came into view");
          await page.waitForTimeout(400);
          const before = await scrolled(page);
          await more.click();
          await page.waitForFunction((n) => document.querySelectorAll("#view-library .lib-nativebox .lib-native").length === n, total);
          assert.equal((await more.textContent()).trim(), tr(lang, "Show less"));
          assert.equal(await more.getAttribute("aria-expanded"), "true");
          assert.equal(await scrolled(page), before, "the click scrolls the page");

          // one plugin's own is shown whole; Show all brings the list back
          // as it was left, still open
          const pick = rows.filter({ has: page.locator(".name", { hasText: new RegExp("^" + at + "$") }) }).locator(".lib-srcpick");
          await pick.click();
          const bar = box.locator(".lib-nativeonly");
          await bar.waitFor();
          assert.equal(await rows.count(), total - (tab === "skills" ? 1 : 0));
          assert.equal(await more.count(), 0);
          await bar.locator(".lib-bundleall").click();
          await bar.waitFor({ state: "detached" });
          assert.equal(await rows.count(), total);
          assert.equal((await more.textContent()).trim(), tr(lang, "Show less"));

          // and Show less folds it again
          await more.click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-nativebox .lib-native").length === 5);
          assert.equal((await more.textContent()).trim(), tr(lang, "Show {n} more", { n: total - 5 }));
        }
        assert.deepEqual(writes, [], "folding wrote to the library");
        assert.deepEqual(errors, []);
      });
    }
  });
}
