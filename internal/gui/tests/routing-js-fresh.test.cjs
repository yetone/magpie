// Run with Node's test runner and Playwright on the module path; see README.md.
// guhailin3 on X (#6026): a JS gateway plugin's routing
// ("magpie-pace-drain/pace-drain") was missing from Codex's Routing picker
// unless Settings had been opened since the last read of the state, and
// went again after picking another routing. The routings served are read
// with the state (jsRoutings), with the Routing page's groups and with the
// Plugins page's list, so every picker offers them without a visit to
// Settings: on the Routing page, after a pick re-reads the page, when a
// plugin starts serving one while the window is open, and in a provider's
// editor right after the plugin is switched on. Every language, both
// engines, the window's width and a phone's.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const PD = { id: "magpie-pace-drain/pace-drain", name: "pace-drain", plugin: "magpie-pace-drain" };
const base = { icon: "generic", catalog: "", models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, affinity: "", ready: true };

// world: what the real magpie would answer now. served is whether the
// plugin serves its routing; routing is Codex's.
function serve(lang, world) {
  const jsRoutings = () => (world.served ? [PD] : []);
  const codex = () => ({
    ...base, id: "codex", name: "Codex", routing: world.routing, chat: "", responses: "", anthropic: "", keyList: [],
    account: {
      agent: "codex", agentName: "Codex", user: "one@example.test", plan: "Plus",
      logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Plus", active: i === 0, on: true })),
    },
  });
  const groups = () => ({
    groups: [], models: [],
    pools: [{ provider: "codex", name: "Codex", icon: "generic", kind: "account", who: ["one@example.test", "two@example.test"], routing: world.routing, affinity: "" }],
    jsRoutings: jsRoutings(),
  });
  const providers = () => ({ providers: [codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
  const plugins = () => ({ bun: true, bunVersion: "1.3.0", plugins: [{ spec: "/x/magpie-pace-drain", off: !world.served, providers: [], moved: [] }], jsRoutings: jsRoutings() });
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    // as the real one: the settings carry no jsRoutings, the state does
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, jsRoutings: jsRoutings() });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/plugins") return json(plugins());
    if (req.method() === "POST" && url.pathname === "/api/plugins/off") { world.served = !req.postDataJSON().off; return json(plugins()); }
    if (req.method() === "POST" && url.pathname === "/api/provider/route") {
      world.posts.push(req.postDataJSON());
      world.routing = req.postDataJSON().routing;
      return json(providers());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, width, world, view) {
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, world));
  await page.goto(`http://magpie.test/?view=${view}`);
  return { page, errors };
}

// the options of the Codex row's Routing picker on the Routing groups page
const poolOpts = (page) => page.locator('.rt-pool[data-provider="codex"] .segs').first()
  .evaluate((box) => [...box.querySelectorAll(".opt")].filter((b) => getComputedStyle(b).display !== "none").map((b) => [b.dataset.seg, b.classList.contains("on")]));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the Routing page offers a plugin's routing without Settings, and keeps it after another pick`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      for (const width of [1100, 420]) {
        const where = `${width}px`;
        const world = { served: true, routing: "order", posts: [] };
        const { page, errors } = await open(browser, lang, width, world, "groups");
        const pool = page.locator('.rt-pool[data-provider="codex"]');
        await pool.waitFor();
        assert.ok((await poolOpts(page)).some(([id]) => id === PD.id), `${where}: offered from the start ${JSON.stringify(await poolOpts(page))}`);
        await pool.locator(`.opt[data-seg="${PD.id}"]`).click();
        await page.waitForFunction(() => document.querySelector('.rt-pool[data-provider="codex"] .opt[data-seg="magpie-pace-drain/pace-drain"]')?.classList.contains("on"));
        assert.deepEqual(world.posts.at(-1), { id: "codex", routing: PD.id }, `${where}: picked`);
        // the reporter's case: another routing picked, the page read again
        await pool.locator('.opt[data-seg="order"]').click();
        await page.waitForFunction(() => document.querySelector('.rt-pool[data-provider="codex"] .opt[data-seg="order"]')?.classList.contains("on"));
        await page.waitForTimeout(300); // the state read after the pick has landed
        const after = await poolOpts(page);
        assert.ok(after.some(([id, on]) => id === PD.id && !on), `${where}: still offered after another pick ${JSON.stringify(after)}`);
        assert.ok(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), `${where}: fits`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    });

    test(`${engine} ${lang}: a routing a plugin starts serving with the window open is offered when the Routing page is shown again`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      const world = { served: false, routing: "", posts: [] };
      const { page, errors } = await open(browser, lang, 1100, world, "groups");
      await page.locator('.rt-pool[data-provider="codex"]').waitFor();
      assert.ok(!(await poolOpts(page)).some(([id]) => id === PD.id), "not offered before it is served");
      world.served = true; // switched on from the CLI, or another window
      await page.evaluate(() => window.show("providers"));
      await page.evaluate(() => window.show("groups"));
      await page.waitForFunction(() => !!document.querySelector('.rt-pool[data-provider="codex"] .opt[data-seg="magpie-pace-drain/pace-drain"]'));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a plugin switched on in Plugins has its routing in a provider's editor at once`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      for (const width of [900, 420]) {
        const where = `${width}px`;
        const world = { served: false, routing: "", posts: [] };
        const { page, errors } = await open(browser, lang, width, world, "plugins");
        const tr = (k) => page.evaluate((k) => t(k), k);
        const view = page.locator("#view-plugins");
        await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: await tr("Installed") }).first().click();
        const row = view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: "magpie-pace-drain" }) });
        await row.getByRole("button", { name: await tr("Switch on"), exact: true }).click();
        await row.getByRole("button", { name: await tr("Switch off"), exact: true }).waitFor();
        await page.evaluate(() => window.show("providers"));
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts").first().waitFor();
        const adv = page.locator(".editor details.advanced");
        if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
        const field = page.locator(".editor label", { hasText: new RegExp(`^${await tr("Routing")}$`) }).locator("xpath=following-sibling::div[1]");
        const names = await field.locator(".segs").first().evaluate((box) => [...box.querySelectorAll(".opt")].filter((b) => getComputedStyle(b).display !== "none").map((b) => b.textContent));
        assert.ok(names.includes(PD.id), `${where}: offered ${JSON.stringify(names)}`);
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    });
  }
}
