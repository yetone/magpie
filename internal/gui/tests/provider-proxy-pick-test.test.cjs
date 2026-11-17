// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's saved proxy is tested from its editor (IcyLiqu1d on X): a
// Test beside the saved proxy picked, shown only in Saved mode, asks
// /api/proxies/test for that proxy and nothing before it is clicked. The
// answer, its latency and the exit's region in the reader's language, or
// why it failed, is said where the picker's hint is, until another proxy
// or mode is chosen. In every language, both engines, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "@px1",
};
const proxies = [
  { id: "px1", name: "Tokyo", url: "http://10.0.0.1:8080", uses: [{ provider: "relay" }] },
  { id: "px2", name: "Down", url: "socks5://10.0.0.9:1080" },
];

function serve(lang, posts) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const settings = { theme: "light", lang, proxy: "", proxySource: "off", proxies, version: "test", dir: "/test", lanURLs: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/proxies/test") {
      const { id } = req.postDataJSON();
      posts.push({ test: id });
      return json(id === "px1" ? { ok: true, ms: 42, ip: "203.0.113.9", loc: "JP", at: 1 } : { ok: false, error: "connect: connection refused", at: 1 });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the region the trace page's JP names, as the reader's language says it
const japan = { en: "Japan", zh: "日本", "zh-TW": "日本", ja: "日本", de: "Japan" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(japan)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a provider's saved proxy is tested from its editor`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        const adv = page.locator(".editor details.advanced");
        await adv.waitFor({ state: "attached" });
        if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
        const row = page.locator(".editor .proxy-row");
        const test = row.locator("button.proxy-test");
        const hint = page.locator(".editor .proxy-pick .hint");
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        await test.waitFor();
        await row.locator("button.proxy-saved").getByText("Tokyo", { exact: true }).waitFor();
        assert.equal(await test.textContent(), await tr("Test"));
        assert.deepEqual(posts, [], "nothing is tested before Test is clicked");
        const savedHint = await tr("Requests to it go through this saved proxy, kept in Settings");
        assert.equal(await hint.textContent(), savedHint);

        const grey = await hint.evaluate((e) => getComputedStyle(e).color);
        // the latency and the exit's region, in the reader's language
        await test.click();
        const ok = (await tr("Reachable, {ms} ms", { ms: 42 })) + " · " + japan[lang];
        await page.waitForFunction(([s]) => document.querySelector(".editor .proxy-pick .hint")?.textContent === s, [ok]);
        assert.deepEqual(posts, [{ test: "px1" }]);
        assert.equal(await hint.evaluate((e) => e.classList.contains("err")), false);
        assert.equal(await test.isDisabled(), false);

        // another proxy chosen puts the hint back; its failure says why, in red
        await row.locator("button.proxy-saved").click();
        await page.locator(".proto-menu .pm-item", { hasText: "Down" }).click();
        await page.waitForFunction(([s]) => document.querySelector(".editor .proxy-pick .hint")?.textContent === s, [savedHint]);
        await test.click();
        const bad = await tr("Unreachable: {error}", { error: "connect: connection refused" });
        await page.waitForFunction(([s]) => document.querySelector(".editor .proxy-pick .hint")?.textContent === s, [bad]);
        assert.deepEqual(posts.at(-1), { test: "px2" });
        assert.equal(await hint.evaluate((e) => e.classList.contains("err")), true);
        assert.notEqual(await hint.evaluate((e) => getComputedStyle(e).color), grey, "a failure isn't the hint's grey");

        // another mode: the Test goes, the hint is that mode's
        await page.locator(".editor .proxy-mode .opt").first().click();
        await page.waitForFunction(() => !document.querySelector(".editor .proxy-pick .hint")?.classList.contains("err"));
        assert.equal(await test.isVisible(), false, "Test is for a saved proxy only");
        assert.equal(await hint.textContent(), await tr("Follows the proxy in Settings"));

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.equal(posts.length, 2, "one request a click");
        assert.deepEqual(errors, []);
      });
    }
  }
}
