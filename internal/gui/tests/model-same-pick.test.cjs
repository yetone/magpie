// timfang4162 (X): in a model's metadata a typed id is all the editor gave
// for binding it to models.dev's data. The Same as box now has a picker
// beside it: the app's own menu (never a <select>) listing the catalog magpie
// has synced, with a filter over id, name and maker. A pick fills the box and
// is staged and saved like a typed one; what the user types still wins.
// Every language, Chromium and WebKit, at a phone's width and wide; the API
// is faked, nothing reaches the network.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const catalog = [
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", maker: "Anthropic", context: 1000000, output: 64000, input: 3, cost: 15, images: true },
  { id: "kimi-k3", name: "Kimi K3", maker: "Moonshot AI", context: 262144, output: 32768, input: 0.6, cost: 2.5 },
  { id: "glm-5-turbo", name: "GLM-5-Turbo", maker: "Zhipu AI" },
];

function serve(lang, posts, calls, fail) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [{ id: "ep-2026-sol", name: "ep-2026-sol", on: true, efforts: [], images: false, merge: "ep-2026-sol" }];
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/catalog/models") { calls.push(1); return fail ? route.fulfill({ status: 500, body: "no" }) : json(catalog); }
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [400, 900]) {
      test(`${engine} ${lang} ${width}px: a models.dev model is picked for Same as, filtered, staged and saved`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [], calls = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, calls));
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k) => page.evaluate(([l, k]) => (l === "en" ? null : I18N[l][k]) || k, [lang, k]);
        const [names, same, pickLabel, save] = await Promise.all([tr("Model settings"), tr("Same as"), tr("Pick from models.dev"), tr("Save")]);
        await page.locator('.row.provider[data-id="relay"]').click();
        await page.getByRole("button", { name: names, exact: true }).click();
        const row = page.locator(".mname", { has: page.locator("code", { hasText: "ep-2026-sol" }) });
        const box = row.getByRole("textbox", { name: same, exact: true });
        const pick = row.getByRole("button", { name: pickLabel, exact: true });
        assert.equal(await page.locator("select").count(), 0, "never a native select");
        const bb = await row.boundingBox(), pb = await pick.boundingBox();
        assert(pb.x >= bb.x - 0.5 && pb.x + pb.width <= bb.x + bb.width + 0.5, "the button stays inside its row");
        const y = await page.evaluate(() => scrollY);
        await pick.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "a click never scrolls the page");
        assert.equal(await menu.locator(".pm-item").count(), 3, "the whole catalog is listed");
        assert.equal(await page.evaluate(() => document.activeElement?.className), "sc-q", "the filter has focus");
        // filtered by the maker and by the name as well as by the id
        await menu.locator(".sc-q").fill("moonshot");
        assert.deepEqual(await menu.locator(".pm-item:not([hidden]) .pm-name").allTextContents(), ["kimi-k3"]);
        await menu.locator(".sc-q").fill("sonnet 4");
        assert.deepEqual(await menu.locator(".pm-item:not([hidden]) .pm-name").allTextContents(), []);
        await menu.locator(".sc-q").fill("sonnet");
        assert.deepEqual(await menu.locator(".pm-item:not([hidden]) .pm-name").allTextContents(), ["claude-sonnet-5"]);
        assert(/1M/.test(await menu.locator(".pm-item:not([hidden]) .pm-note").innerText()), "its window is told");
        await page.keyboard.press("Enter");
        await menu.waitFor({ state: "hidden" });
        assert.equal(await box.inputValue(), "claude-sonnet-5");
        assert.deepEqual(posts, [], "nothing is sent before the Save");
        assert(await row.getByText(await tr("unsaved"), { exact: true }).isVisible(), "it says it is unsaved");
        // opened again it ticks the one picked, and the catalog is asked for once
        await pick.click();
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-item.on .pm-name").innerText(), "claude-sonnet-5");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "hidden" });
        assert.equal(calls.length, 1);
        // a value typed after it wins
        await box.fill("kimi-k3");
        await box.press("Enter");
        await page.getByRole("button", { name: save, exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".mnames"));
        assert.deepEqual(posts[0].body.modelPrefs, { "ep-2026-sol": { same: "kimi-k3" } });
        const missing = await page.evaluate(() => ["Pick from models.dev", "models.dev's list could not be read"].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => `${l}: ${k}`)));
        assert.deepEqual(missing, []);
        assert.deepEqual(errors, []);
      });
    }
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a catalog that can't be read says so and leaves the box alone`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    const errors = [], calls = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", [], calls, true));
    await page.goto("http://magpie.test/?view=providers");
    await page.locator('.row.provider[data-id="relay"]').click();
    await page.getByRole("button", { name: "Model settings", exact: true }).click();
    const row = page.locator(".mname", { has: page.locator("code", { hasText: "ep-2026-sol" }) });
    await row.getByRole("button", { name: "Pick from models.dev", exact: true }).click();
    await page.getByText("models.dev's list could not be read").waitFor();
    assert.equal(await page.locator(".proto-menu").count(), 0);
    assert.equal(await row.getByRole("textbox", { name: "Same as", exact: true }).inputValue(), "");
    assert.deepEqual(errors, []);
  });
}
