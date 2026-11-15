// Run with Node's test runner and Playwright on the module path; see README.md.
// hzlxb on X: in Library → MCP servers the servers turned off sat among the
// ones on. They are kept apart now, as Providers' are: the list holds the
// ones on, and the ones off sit below it under a "Servers turned off (N)"
// fold, folded at first. The fold opens and closes on a click that leaves
// the page where it is and opens no editor, and is remembered across a
// reload; a server switched off moves under it, one switched on again comes
// back up. While picking (Select) the fold is open, for every server picked
// to be in sight. Every language, Chromium and WebKit, at 1100 and 420 wide
// with nothing running off to the side. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const NAMES = ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"];
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
  instructions: { agents: [], sets: [] }, foundSkills: [], skills: [], problems: [], foundServers: [], projects: [],
  servers: NAMES.map((name) => st.off.has(name)
    ? { name, transport: "stdio", command: "uvx", args: [name + "-mcp"], agents: [], hidden: true, paused: ["claude"] }
    : { name, transport: "stdio", command: "uvx", args: [name + "-mcp"], agents: ["claude"] }),
});

let I18N;
const tr = (lang, key, vars = {}) => {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
};

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/servers/on") {
      const { name, on } = req.postDataJSON();
      if (on) st.off.delete(name); else st.off.add(name);
      return json({ ...fixture(st), result: { changed: [] } });
    }
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, { state: "ok", tools: 2 }])) });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-offhead, #view-library .lib-offhead .fold, #view-library .lib-server")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});
const names = (page, sel) => page.locator(`#view-library ${sel} > .lib-server`).evaluateAll((rs) => rs.map((r) => r.dataset.server));
const top = (loc) => loc.evaluate((e) => e.getBoundingClientRect().top);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": MCP servers turned off fold away below the ones on", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = { off: new Set(["bravo", "echo", "golf"]) };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator('.lib-server[data-server="alpha"]').waitFor();

        // the list holds the ones on; the ones off are under the fold, folded
        assert.deepEqual(await names(page, ".lib-list:not(.lib-offlist)"), ["alpha", "charlie", "delta", "foxtrot", "hotel"]);
        const fold = v.locator(".lib-offhead .fold");
        assert(await fold.isVisible(), "the fold shows");
        assert.equal((await fold.locator(".label").textContent()).trim(), w("Servers turned off"));
        assert.equal((await fold.locator(".note").textContent()).trim(), "3");
        assert.equal(await fold.getAttribute("aria-expanded"), "false", "folded at first");
        assert.equal(await fold.getAttribute("title"), w("Show the servers turned off"));
        const offList = v.locator(".lib-offlist");
        assert(await offList.evaluate((l) => l.hidden), "folded, the ones off are hidden");
        const listBottom = await v.locator(".lib-list:not(.lib-offlist)").first().evaluate((e) => e.getBoundingClientRect().bottom);
        assert(await top(v.locator(".lib-offhead")) >= listBottom, "the fold is below the list");
        assert(await top(v.locator(".lib-offhead")) < await top(v.locator(".after-list").first()), "the fold is above Add server");
        assert.deepEqual(await sideways(page), [], "sideways scroll, folded");

        // opened with the page scrolled to it by the wheel (a scroll by
        // code is put back on the click, as not the reader's), the head
        // stays where it is
        await page.mouse.move(width / 2, 300);
        for (let i = 0; i < 12 && await top(fold) > 420; i++) {
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(120);
        }
        await page.waitForTimeout(300);
        const scrolled = await v.evaluate((x) => x.scrollTop);
        const was = await top(fold);
        await fold.click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-offhead .fold")?.getAttribute("aria-expanded") === "true");
        assert.deepEqual(await names(page, ".lib-offlist"), ["bravo", "echo", "golf"]);
        assert(await offList.isVisible());
        assert.equal(await fold.getAttribute("title"), w("Fold the servers turned off away"));
        { const now = await top(fold); assert(Math.abs(now - was) <= 1, `opening moved the head: ${was} → ${now}, scrollTop ${scrolled} → ${await v.evaluate((x) => x.scrollTop)}, window ${await page.evaluate(() => window.scrollY)}`); }
        assert(Math.abs(await v.evaluate((x) => x.scrollTop) - scrolled) <= 1, "opening scrolled the page");
        assert.equal(await page.locator("#modal").evaluate((m) => !m.hidden && m.getBoundingClientRect().height > 0).catch(() => false), false, "the fold opened the editor");
        assert.deepEqual(await sideways(page), [], "sideways scroll, open");

        // remembered across a reload
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator('.lib-server[data-server="alpha"]').waitFor();
        assert.equal(await v.locator(".lib-offhead .fold").getAttribute("aria-expanded"), "true", "the fold is remembered");
        assert.deepEqual(await names(page, ".lib-offlist"), ["bravo", "echo", "golf"]);

        // a server switched off moves under it, one switched on comes back up
        await v.locator('.lib-list:not(.lib-offlist) > .lib-server[data-server="delta"] > .lib-switch').click();
        await page.waitForFunction(() => document.querySelector('#view-library .lib-offlist > .lib-server[data-server="delta"]'));
        assert.equal((await v.locator(".lib-offhead .note").textContent()).trim(), "4");
        await v.locator('.lib-offlist > .lib-server[data-server="echo"] > .lib-switch').click();
        await page.waitForFunction(() => document.querySelector('#view-library .lib-list:not(.lib-offlist) > .lib-server[data-server="echo"]'));
        assert.deepEqual(await names(page, ".lib-offlist"), ["bravo", "delta", "golf"]);

        // folded, picking opens it, and it can't be folded while picking
        await v.locator(".lib-offhead .fold").click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-offlist")?.hidden);
        await v.locator(".lib-serverhead .lib-select").click();
        await v.locator(".lib-pickbar").waitFor();
        assert.equal(await v.locator(".lib-offhead .fold").getAttribute("aria-expanded"), "true");
        assert(await v.locator(".lib-offhead .fold").isDisabled(), "the fold folds while picking");
        assert(await v.locator(".lib-offlist").isVisible(), "a server off is out of sight while picking");
        assert.equal(await v.locator(".lib-offlist .lib-pickbox, .lib-offlist input[type=checkbox]").count() > 0, true, "a server off can't be picked");
        await v.locator(".lib-pickdone").click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-offlist")?.hidden);

        // every one off: the fold is all there is
        for (const n of NAMES) st.off.add(n);
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator(".lib-offhead .fold").waitFor();
        assert.equal((await v.locator(".lib-offhead .note").textContent()).trim(), String(NAMES.length));
        assert(await v.locator(".lib-list:not(.lib-offlist)").first().evaluate((l) => l.hidden), "an empty list shows");
        // and none off: no fold
        st.off.clear();
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator('.lib-server[data-server="alpha"]').waitFor();
        assert(await v.locator(".lib-offhead").evaluate((h) => h.hidden), "a fold over nothing");

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
