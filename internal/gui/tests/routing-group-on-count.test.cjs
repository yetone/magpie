// Run with Node's test runner and Playwright on the module path; see README.md.
// adorable_piglet on Discord: a group magpie found, GPT-6 Astra, In order,
// three of its five members on (two on providers switched off). A Codex
// conversation held to one member — here the key that compacted it
// natively, as Strict or a sealed task would hold it — read "GPT-6 Astra ·
// routing group · 1 on": the header counted who that one request was
// weighed among, not the group's members on. It says the group's members
// on as the request came (the route's group.members), and the first line
// says why only one was asked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const key = (p, name) => ({ id: p + "#k1", provider: p, name, kind: "key", model: "gpt-6-astra", routing: "order" });
const b = key("relay-b", "Relay B"), c = key("relay-c", "Relay C"), e = key("relay-e", "Relay E");
const group = {
  id: "auto-gpt-6-astra", ref: "auto/gpt-6-astra", name: "GPT-6 Astra", routing: "order", affinity: "", auto: true,
  members: ["relay-b/gpt-6-astra", "relay-c/gpt-6-astra", "relay-e/gpt-6-astra"], via: ["", "", ""],
};
const route = (id, fields) => ({
  id, seq: id, time: at(1), agent: "codex", model: "auto/gpt-6-astra", provider: "relay-b", group,
  done: true, status: 200, ms: 900, tokens: 1200,
  tries: [{ id: b.id, model: b.model, start: at(1), done: true, status: 200, ms: 900 }], ...fields,
});
const routes = [
  route(102, { order: [b], compaction: b.id }), // held to the member that compacted it
  route(101, { order: [b, c, e] }),
];

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({
      contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};`,
    });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({
      contentType: "text/javascript", body: "export const Window = {};",
    });
    if (url.pathname === "/api/state") return json({
      agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }],
      profiles: [], settings: { lang, theme: "light" },
    });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((resolve) => setTimeout(resolve, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 102, totals: { requests: routes.length }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const three = { en: "3 on", zh: "启用 3 个", "zh-TW": "啟用 3 個", ja: "3 個有効", de: "3 aktiv" };
const one = { en: "1 on", zh: "启用 1 个", "zh-TW": "啟用 1 個", ja: "1 つ有効", de: "1 aktiv" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a group's header counts its members on, not who one request was held to`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 1200 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const tail = () => page.locator(".rt-what span").textContent();

      // the held request: the group still has three on
      await page.locator(".rt-req").first().click();
      await page.waitForFunction(() => document.querySelector(".rt-what b")?.textContent === "GPT-6 Astra");
      await page.waitForFunction(() => document.querySelector(".rt-steps li.why"));
      assert.ok((await tail()).includes(three[lang]), await tail());
      assert.ok(!(await tail()).includes(one[lang]), await tail());

      // one weighed among all three says the same
      await page.locator(".rt-req").nth(1).click();
      await page.waitForFunction(() => !/compact|压缩|壓縮|圧縮|kompri/.test(document.querySelector(".rt-steps li.why")?.textContent || "x"));
      assert.ok((await tail()).includes(three[lang]), await tail());
      assert.deepEqual(errors, []);
    });
  }
}
