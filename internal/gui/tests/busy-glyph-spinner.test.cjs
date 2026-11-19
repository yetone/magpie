// Run with Node's test runner and Playwright on the module path; see README.md.
// @wiirhan on X: "资源库--技能--更新技能的loading状态为啥是直接转箭头？" A
// skill's Update in the Library turned its own up arrow round and round
// while the fetch ran, which reads as a joke, not as work under way. An icon
// button at work now shows the spinner every other button turns (saving, a
// CLI's update: CLI_SPIN's arc) in place of its glyph, and gets its glyph
// back when it is done. The same for an agent row's Restart, whose circular
// arrow turned the same way. Held answers keep each button at work while it
// is looked at: the arc is there, the glyph is not, it turns at 0.9s a turn
// or faster, and with reduced motion it stands still. Chromium and WebKit,
// en, zh, zh-TW, ja and de, wide and narrow. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const SPIN = "M13.5 8a5.5 5.5 0 1 1-5.5-5.5"; // app.js CLI_SPIN
const UP = "M8 12.5v-9 M4.5 7 8 3.5 11.5 7"; // the library's GLYPH.up
const REAPPLY = "M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3.25h-3.25"; // app.js REAPPLY
const HOME = "/Users/wiirhan";
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
  instructions: { agents: [] }, servers: [], foundServers: [], foundSkills: [], projects: [], problems: [],
  skills: [{ name: "pdf", description: "Reads PDFs", kind: "github", agents: ["claude"], source: "https://github.com/owner/repo/tree/main/pdf" }],
};
const days = (n) => new Date(Date.now() - n * 86400e3).toISOString();
const workbuddy = {
  id: "workbuddy", name: "WorkBuddy", icon: "generic", path: "/fixture/models.json", wired: true, appRestart: { since: days(1) },
  fields: [{ key: "model", label: "model", value: "deepseek-v4", options: [{ value: "deepseek-v4", label: "DeepSeek V4", ref: "deepseek/deepseek-v4", note: "DeepSeek · via magpie" }] }],
};

let I18N;
const tr = (lang, key, vars = {}) => {
  const s = lang === "en" ? key : I18N[lang][key];
  assert.equal(typeof s, "string", `${lang} has no ${JSON.stringify(key)}`);
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
};

// held: the paths whose answer waits until the test lets it go
function serve(lang, held) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [workbuddy], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/skills/update") {
      await new Promise((go) => held.set("update", go));
      return json({ ...lib, result: { changed: ["claude"] } });
    }
    // the restart fails, so its button stays to show its glyph back
    if (url.pathname.startsWith("/api/agents/restart/")) {
      await new Promise((go) => held.set("restart", go));
      return json({ error: "WorkBuddy is still open" }, 409);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// what the button draws and how it turns: its paths, and each running
// animation that rotates, with its turn's length and how often it repeats
// (a colour or opacity transition is not a turn)
const look = (loc) => loc.evaluate((b) => ({
  paths: [...b.querySelectorAll("svg path")].map((p) => p.getAttribute("d")),
  turns: b.getAnimations({ subtree: true }).filter((a) => a.effect.getKeyframes().some((f) => /rotate|matrix/.test(f.transform || ""))).map((a) => {
    const tm = a.effect.getComputedTiming();
    return { duration: tm.duration, iterations: tm.iterations, on: a.effect.target.tagName.toLowerCase() };
  }),
}));

function atWork(seen, reduce, what) {
  assert.deepEqual(seen.paths, [SPIN], `${what} at work draws ${JSON.stringify(seen.paths)}, not the spinner alone`);
  if (reduce) return assert.deepEqual(seen.turns, [], `${what} turns with reduced motion`);
  const spins = seen.turns;
  assert.equal(spins.length, 1, `${what}: ${JSON.stringify(seen.turns)}`);
  assert.equal(spins[0].on, "path", `${what} turns its whole svg, not the arc inside it`);
  assert.ok(spins[0].duration <= 900, `${what} turns once in ${spins[0].duration}ms`);
  assert.equal(spins[0].iterations, Infinity);
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an icon button at work shows the spinner, not its glyph turning", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const [width, reduce] of [[1100, false], [420, true]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} at ${width}px${reduce ? ", reduced motion" : ""}`, async () => {
        const held = new Map(), errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: reduce ? "reduce" : "no-preference" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, held));

        // the Library's skill row: Update, at work and then done
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: /^pdf$/ }) });
        await row.waitFor();
        const up = row.locator(".lib-rowacts .lib-icon").first();
        assert.deepEqual((await look(up)).paths, [UP], "the row's first action is not Update");
        await row.hover();
        await up.click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-rowacts .lib-icon.busy"));
        await page.waitForFunction(() => document.querySelector("#view-library").getAttribute("aria-busy") === "true");
        assert.equal(held.size, 1, "Update posted nothing");
        atWork(await look(up), reduce, "a skill's Update");
        // the row's actions stay in view while Update works (they fade in on hover, so wait out the fade)
        await page.waitForFunction(() => getComputedStyle(document.querySelector("#view-library .lib-rowacts")).opacity === "1");
        assert.ok(await sideways(page) <= 1, "the page scrolls sideways");
        held.get("update")();
        await page.locator("#status").filter({ hasText: tr(lang, "{name} is updated", { name: "pdf" }) }).waitFor();
        const back = row.locator(".lib-rowacts .lib-icon").first();
        assert.deepEqual(await look(back), { paths: [UP], turns: [] }, "Update didn't get its arrow back");

        // an agent row's Restart, the same
        await page.goto("http://magpie.test/?view=agents");
        const go = page.locator('.row.agent[data-id="workbuddy"] .ag-restart');
        await go.waitFor();
        assert.deepEqual((await look(go)).paths, [REAPPLY]);
        await go.click();
        await page.locator("dialog.action-confirm[open] button.primary").click();
        await page.waitForFunction(() => document.querySelector(".ag-restart.busy"));
        atWork(await look(go), reduce, "Restart");
        assert.equal((await go.textContent()).trim(), tr(lang, "Restart"));
        assert.ok(await sideways(page) <= 1, "the page scrolls sideways");
        held.get("restart")();
        await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
        await page.waitForFunction(() => !document.querySelector(".ag-restart").disabled);
        assert.deepEqual(await look(go), { paths: [REAPPLY], turns: [] }, "Restart didn't get its glyph back");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
