// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's own first-token time (GoForceX on X: "支持调整对应 provider
// 的超时等待时间"): "Slow to start" under the editor's Advanced, after
// Queue wait, in a key provider's editor and a signed-in subscription's.
// Advanced stays folded with nothing set and opens when it is set; the
// saved seconds show, a save posts them, empty posts 0 (none of its own),
// and a number past 600 is refused with the field's own error. In every
// language, wide and narrow, Chromium and WebKit; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = (firstToken) => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [{ id: "k1", name: "Main", masked: "sk-…one", on: true }],
  balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null, ...(firstToken ? { firstToken } : {}),
});
const codex = () => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", maxConcurrency: null, firstToken: 45,
});

function serve(lang, posts, list) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length), body = route.request().postDataJSON();
      posts.push({ action, body });
      const was = list.find((q) => q.id === body.id);
      if (action === "save" && was) was.firstToken = body.firstToken || undefined;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const KEYS = [
  "Slow to start",
  "Seconds a streamed reply may take to begin before the next key, account or member is asked; the last is waited for. Empty follows the group",
  "Slow to start: a whole number of seconds from 0 to 600",
  "Default",
];
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const WIDTHS = [900, 420];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a provider's Slow to start under Advanced, set, shown and saved`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) for (const width of WIDTHS) {
      await t.test(`${lang} ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        const list = [relay(0), { ...relay(20), id: "relay2", name: "Relay Two" }, codex()];
        await page.route("**/*", serve(lang, posts, list));
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k) => page.evaluate(([l, k]) => l === "en" ? k : I18N[l][k], [lang, k]);
        const label = await tr("Slow to start");
        assert(label, "Slow to start has its " + lang);
        if (lang !== "en") {
          const missing = await page.evaluate(([l, keys]) => keys.filter((k) => !I18N[l][k]), [lang, KEYS]);
          assert.deepEqual(missing, [], lang + " has every string");
        }
        const open = async (name) => {
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor").waitFor();
          await page.waitForTimeout(250);
        };
        const fold = () => page.locator(".editor details.adv-fold.advanced");
        const box = () => page.locator(".editor input.first-token");
        const save = () => page.locator(".editor .bar .ed-save, .editor .bar button.primary").last();
        const saves = () => posts.filter((p) => p.action === "save");
        // a save closes the editor once its answer is in
        const closed = async () => {
          await page.locator(".editor").waitFor({ state: "detached" });
          await page.waitForTimeout(150);
        };

        // nothing set: Advanced folded, the field in it empty, after Queue wait
        await open("Relay");
        assert.equal(await fold().evaluate((d) => d.open), false, "Advanced folded with nothing set");
        assert.equal(await box().inputValue(), "");
        assert.equal(await box().getAttribute("placeholder"), await tr("Default"));
        const order = await page.locator(".editor .adv-fold.advanced label").evaluateAll((ls) => ls.map((l) => l.textContent.trim()));
        assert.equal(order[order.indexOf(await tr("Queue wait")) + 1], label, "after Queue wait: " + order.join(" | "));
        await page.locator(".editor details.adv-fold.advanced > summary").click();
        await page.waitForTimeout(200);
        await box().scrollIntoViewIfNeeded();
        assert(await box().isVisible(), "shown once Advanced is opened");
        // the label on one line, the field as wide as Queue wait's
        const lh = await page.locator(".editor label", { hasText: label }).evaluate((e) => e.getBoundingClientRect().height);
        const one = await page.locator(".editor label", { hasText: await tr("Queue wait") }).evaluate((e) => e.getBoundingClientRect().height);
        assert(lh <= one + 1, `the label takes ${lh}px, Queue wait's ${one}px`);
        const w = (l) => l.evaluate((e) => e.getBoundingClientRect().width);
        assert.equal(await w(box()), await w(page.locator(".editor input.queue-wait")));
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "nothing wider than the window");
        // out of range: refused, nothing posted
        await box().fill("700");
        await save().click();
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), await tr("Slow to start: a whole number of seconds from 0 to 600"));
        assert.equal(saves().length, 0);
        await box().fill("15");
        await save().click();
        for (let i = 0; i < 60 && !saves().length; i++) await page.waitForTimeout(50);
        assert.equal(saves().at(-1)?.body.firstToken, 15);
        await closed();

        // set: Advanced opens with it, empty saves 0
        await open("Relay Two");
        assert.equal(await fold().evaluate((d) => d.open), true, "Advanced opens when it is set");
        assert.equal(await box().inputValue(), "20");
        await box().fill("");
        const n = saves().length;
        await save().click();
        for (let i = 0; i < 60 && saves().length === n; i++) await page.waitForTimeout(50);
        assert.equal(saves().at(-1)?.body.firstToken, 0, "empty: none of its own");
        await closed();

        // a signed-in subscription's editor has it too
        await open("Codex");
        assert.equal(await box().inputValue(), "45");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
