// Run with Node's test runner and Playwright on the module path; see README.md.
// #1603 (SwimmingLiu): Codex on CC Switch's custom relay, not through
// magpie, with two Codex accounts on in magpie. Its picker had the GPT
// models direct only. The backend now offers the ones magpie serves on
// those accounts (codex/…) as well: the picker shows GPT-5.5 twice, the
// relay's direct and the accounts' via magpie, and opening it sends
// nothing. WebKit or Chromium, English and Chinese, wide and narrow; the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const state = (lang) => ({
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", fields: [{ key: "model", label: "model", value: "gpt-5.5", options: [
      { value: "gpt-5.5", label: "GPT-5.5", group: "custom", direct: "custom" },
      { value: "codex/gpt-5.5", label: "GPT-5.5", note: "me@example.com · via magpie", group: "Codex", ref: "codex/gpt-5.5" },
      { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie", group: "Relay" }] }] },
  ],
  profiles: [], settings: { lang, theme: "light", guideHidden: true },
});

function server(lang, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() !== "GET" && url.pathname.startsWith("/api/agents")) posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state(lang));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: { via: "via magpie", direct: "direct, not via magpie" }, zh: { via: "经 magpie", direct: "直连，不经 magpie" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    for (const width of [980, 420]) {
      test(`${engine} ${lang} ${width}px: Codex's accounts in magpie are offered via magpie beside the relay's direct`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator('.row.agent[data-id="codex"] > .field.ag-start').click();
        const rows = page.locator("#pop:not([hidden]) #list li", { hasText: "GPT-5.5" });
        await rows.first().waitFor();
        const tags = [];
        for (const li of await rows.all()) tags.push((await li.locator(".badge.path").allTextContents()).join(" ").trim());
        assert.deepEqual(tags.sort(), [w.direct, w.via].sort(), "GPT-5.5 direct, and via magpie on the accounts");
        await page.keyboard.press("Escape");
        assert.deepEqual(posts, [], "opening the picker changes nothing");
        assert.deepEqual(errors, []);
      });
    }
  }
}
