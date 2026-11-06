// Run with Node's test runner and Playwright on the module path; see README.md.
// hzh_1119 on Discord: an OpenAI API key's monthly credit spent before the
// ChatGPT subscription where both serve a model. A provider row's
// right-click has Use first: it posts provider/usefirst, and once on the
// item shows a tick and posts it off again. On the Routing page, a request
// whose first went first for it says so, not that its allowance renews
// sooner. The page doesn't move. Every language, Chromium and WebKit, at
// 1100 and 420 wide; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const CHECK = "m3.5 8.5 3 3 6-7";
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const prov = (id, name, extra = {}) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ...extra,
});
const order = [
  { id: "openai#k", provider: "openai", name: "OpenAI", who: "OpenAI", kind: "key", model: "gpt-5.5", routing: "", useFirst: true },
  { id: "codex#u", provider: "codex", name: "Codex", who: "me@example.com", kind: "account", model: "gpt-5.5", routing: "", known: true, used: 10,
    windows: [{ used: 10, resets: new Date(now.getTime() + 2 * 3600e3).toISOString() }] },
];

function serve(lang, posts) {
  let list = [prov("codex", "Codex"), prov("openai", "OpenAI")];
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "auto/gpt-5.5", provider: "openai", done: true, status: 200, ms: 900, tokens: 1200, order,
    tries: [{ id: "openai#k", model: "gpt-5.5", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/usefirst") {
      const body = request.request().postDataJSON();
      posts.push(body);
      list = list.map((p) => p.id === body.id ? { ...p, useFirst: body.useFirst } : p);
      return json(providers());
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = [
  "Use first",
  "{name} is used first",
  "{name} takes its turn with the others",
  "{who} goes first: its provider is set to be used first.",
  "its provider is set to be used first.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const open = async (t, view) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=" + view);
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        return { page, errors, posts, say };
      };

      test(`${engine} ${width} ${lang}: a provider's right-click turns Use first on and off`, async (t) => {
        const { page, errors, posts, say } = await open(t, "providers");
        await page.locator(".row.provider").first().waitFor();
        if (lang === "en") {
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), strings);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        const scrollers = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(document.scrollingElement.scrollTop));
        const before = await scrollers();
        const row = page.locator('#providers > .row.provider[data-id="openai"]');
        const menu = page.locator(".pop.row-menu");
        const item = menu.locator(".rm-item", { hasText: await say("Use first") });
        await row.locator(".name").click({ button: "right" });
        await menu.waitFor();
        assert.equal(await item.count(), 1);
        assert.notEqual(await item.locator("path").getAttribute("d"), CHECK, "off, no tick");
        const box = await menu.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the menu fits across ${width}`);
        await item.click();
        await page.waitForFunction(() => !document.querySelector(".pop.row-menu"));
        assert.deepEqual(posts, [{ id: "openai", useFirst: true }]);
        assert.equal(await page.locator("#providers > .row.provider.selected").count(), 0, "the right-click opened the row");

        await row.locator(".name").click({ button: "right" });
        await menu.waitFor();
        assert.equal(await item.locator("path").getAttribute("d"), CHECK, "on, a tick");
        await item.click();
        await page.waitForFunction(() => !document.querySelector(".pop.row-menu"));
        assert.deepEqual(posts, [{ id: "openai", useFirst: true }, { id: "openai", useFirst: false }]);

        await page.locator('#providers > .row.provider[data-id="codex"] .name').click({ button: "right" });
        await menu.waitFor();
        assert.notEqual(await item.locator("path").getAttribute("d"), CHECK, "another provider is off");
        await page.keyboard.press("Escape");
        assert.deepEqual(await scrollers(), before, "the page doesn't move");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: the Routing page says the first went first for Use first`, async (t) => {
        const { page, errors, say } = await open(t, "routing");
        const req = page.locator(".rt-req").first();
        await req.waitFor();
        if (await req.getAttribute("aria-pressed") !== "true") await req.click();
        const why = page.locator(".rt-steps li.why").first();
        await why.waitFor();
        assert.equal((await why.textContent()).trim(), await say("{who} goes first: its provider is set to be used first.", { who: "OpenAI" }));
        assert.deepEqual(errors, []);
      });
    }
  }
}
