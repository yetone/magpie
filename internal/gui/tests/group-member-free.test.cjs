// Run with Node's test runner and Playwright on the module path; see README.md.
// jiabubu on Discord: a routing group says which of its members cost
// nothing (a provider marked Free for me, memberJSON.Free), whatever the
// group's routing, so it shows before Free first is picked: on the
// group's card, and in its editor beside the member's provider, with a
// hover in the window's language. A paid member says nothing of it. Every
// language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "scope/qwen", name: "qwen", provider: "scope", providerName: "Scope", icon: "generic" },
  { id: "paid/qwen", name: "qwen", provider: "paid", providerName: "Paid", icon: "generic" },
];
const words = {
  en: { edit: "Edit", free: "free" },
  zh: { edit: "编辑", free: "免费" },
  "zh-TW": { edit: "編輯", free: "免費" },
  ja: { edit: "編集", free: "無料" },
  de: { edit: "Bearbeiten", free: "kostenlos" },
};
const hover = "Free first uses it before paid ones, and Cheapest first counts it as costing nothing";

function serve(lang) {
  // the gateway's word (memberJSON.Free): Scope is Free for me, Paid not
  const groups = () => ({
    models, deciders: [], pools: [],
    groups: [
      { id: "g", name: "G", members: ["scope/qwen", "paid/qwen"], off: [], routing: "smart", ready: true,
        memberInfo: [{ id: "scope/qwen", ready: true, provider: "scope", name: "Scope", model: "qwen", free: true }, { id: "paid/qwen", ready: true, provider: "paid", name: "Paid", model: "qwen" }] },
    ],
  });
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      test(`${engine} ${width} ${lang}: a free member says so on its group's card and in the editor, a paid one doesn't`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1200 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator('.rt-group[data-id="g"]');
        await card.waitFor();

        const mem = (await card.locator(".mem").first().textContent()).trim();
        const parts = mem.split(" · ");
        assert.deepEqual(parts, ["Scope", "qwen", w.free, "Paid", "qwen"], `the card's members read ${mem}`);

        await card.locator("button", { hasText: w.edit }).click();
        const rows = page.locator(".rt-gedit .fbl").first().locator(".fbrow");
        await rows.nth(1).waitFor();
        const scope = rows.filter({ hasText: "Scope" }), paid = rows.filter({ hasText: "Paid" });
        const tag = scope.locator(".n small.rt-free");
        assert.equal(await tag.count(), 1, "the free member has no free tag");
        assert.equal((await tag.textContent()).trim(), w.free);
        const title = await tag.getAttribute("title");
        if (lang === "en") assert.equal(title, hover);
        else assert.ok(title && title !== hover, `the tag's hover is in English in ${lang}: ${title}`);
        await tag.scrollIntoViewIfNeeded();
        assert.ok(await tag.isVisible(), "the free tag is out of sight");
        const box = await tag.boundingBox();
        assert.ok(box.width > 4, `the free tag is squeezed to ${box.width}px`);
        assert.equal(await paid.locator(".rt-free").count(), 0, "the paid member says it is free");
        assert.ok(!(await paid.locator(".n").textContent()).includes(w.free), "the paid member's name says free");

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
