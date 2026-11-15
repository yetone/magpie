// Filtering reuses cards and leaves controls/editors in place. Real assets,
// fixture APIs only; no running magpie or agent configuration is touched.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.MAGPIE_GROUP_ASSETS || path.resolve(__dirname, "../assets");
function fixture() {
  const models = Array.from({ length: 4000 }, (_, i) => ({ id: `p${i % 10}/model-${i}`, name: "Model " + i,
    provider: "p" + i % 10, providerName: "Provider " + i % 10, icon: "generic" }));
  return { models, pools: [{ provider: "p9", name: "Pool", kind: "account", who: ["A", "B"] }],
    groups: Array.from({ length: 10 }, (_, i) => {
      const id = models[3990 + i].id + ":high#acct";
      return { id: "g" + i, name: "Group " + i, members: [id], ready: true, routing: "order",
        memberInfo: [{ id, provider: models[3990 + i].provider, name: "Provider", model: models[3990 + i].name, ready: true }] };
    }), accounts: Object.fromEntries(Array.from({ length: 10 }, (_, i) => ["p" + i, [{ id: `p${i}/acct`, name: "Account A" }]])) };
}
async function serve(route, lang, feed) {
  const url = new URL(route.request().url()), json = (x) => route.fulfill({ json: x });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/groups") return json(feed.data);
  if (url.pathname === "/api/gateway/trace") {
    if (url.searchParams.has("wait")) return new Promise(() => {});
    return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: {}, routes: [] });
  }
  if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
  if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try {
    let body = await fs.readFile(file);
    if (feed.inspect && url.pathname === "/plugins/routing/client.js") {
      body = body.toString().replace("  // ---------- where the reader is, as requests come ----------",
        '  window.__routingGroupCache = () => ({ cached: !!groupIndex, rows: groupRows.size, timer: !!groupIndexTimer });\n  // ---------- where the reader is, as requests come ----------');
    }
    await route.fulfill({ body, contentType });
  } catch { await route.fulfill({ status: 404, body: "" }); }
}
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}px: group filtering reuses cards, controls and an editor`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(10000);
        page.setDefaultNavigationTimeout(60000);
        const feed = { data: fixture() }, errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => { if (m.type() === "error") console.error(m.text()); });
        await page.route("**/*", (r) => serve(r, lang, feed));
        await page.goto("http://magpie.test/?view=groups");
        await page.locator('.rt-group[data-id="g9"]').waitFor().catch(async (e) => { console.error(errors, await page.locator("body").innerText()); throw e; });
        const q = page.locator("input.rt-gfilter"), rows = page.locator(".rt-groups .rt-group");
        await page.evaluate(() => {
          window.keptCard = document.querySelector('.rt-group[data-id="g9"]');
          window.keptPool = document.querySelector(".rt-pool");
          window.keptFound = document.querySelector('.rt-gfound button[role="switch"]');
          window.keptInput = document.querySelector("input.rt-gfilter");
        });
        await q.fill("3999");
        assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["g9"]);
        assert.equal(await page.evaluate(() => window.keptCard === document.querySelector('.rt-group[data-id="g9"]')), true, "filtering rebuilt a matching card");
        assert.equal(await page.evaluate(() => window.keptPool === document.querySelector(".rt-pool")), true, "filtering rebuilt an unrelated pool");
        assert.equal(await page.evaluate(() => window.keptFound === document.querySelector('.rt-gfound button[role="switch"]')), true, "filtering rebuilt settings");
        await q.fill("absent-query");
        assert.equal(await rows.count(), 0);
        await q.fill("");
        assert.equal(await rows.count(), 10);
        assert.equal(await page.evaluate(() => window.keptCard === document.querySelector('.rt-group[data-id="g9"]')), true, "clearing the query rebuilt a card");
        assert.equal(await page.evaluate(() => document.activeElement === window.keptInput && window.keptInput.isConnected), true);
        // An open editor stays in place, including its draft and live fields.
        await q.fill("3999");
        await page.locator('.rt-group[data-id="g9"] > button.text').click();
        const editor = page.locator(".rt-gedit");
        await editor.waitFor();
        const name = editor.locator("input").first();
        await name.fill("Unsaved name");
        await editor.evaluate((e) => { window.keptEditor = e; });
        await q.fill("no-match");
        assert.equal(await page.evaluate(() => window.keptEditor === document.querySelector(".rt-gedit")), true, "filtering recreated the editor");
        assert.equal(await name.inputValue(), "Unsaved name");
        assert.equal(await rows.count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  test(`${engine}: group caches expire five minutes after leaving and refresh on return`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ reducedMotion: "reduce" });
    page.setDefaultTimeout(10000);
    page.setDefaultNavigationTimeout(60000);
    await page.clock.install();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    const feed = { data: fixture(), inspect: true };
    await page.route("**/*", (r) => serve(r, "en", feed));
    await page.goto("http://magpie.test/?view=groups");
    await page.locator('.rt-group[data-id="g9"]').waitFor();
    assert.equal(await page.evaluate(() => window.__routingGroupCache().cached), true);
    await page.evaluate(() => window.show("agents"));
    await page.waitForFunction(() => window.__routingGroupCache().timer);
    await page.clock.fastForward(299999);
    assert.equal(await page.evaluate(() => window.__routingGroupCache().cached), true);
    await page.clock.fastForward(1);
    assert.deepEqual(await page.evaluate(() => window.__routingGroupCache()), { cached: false, rows: 0, timer: false });
    feed.data.models[3999].name = "Renamed model";
    feed.data.accounts.p9[0].name = "Account B";
    await page.evaluate(() => window.show("groups"));
    await page.waitForFunction(() => document.querySelector('.rt-group[data-id="g9"]')?.textContent.includes("Renamed model"));
    const q = page.locator("input.rt-gfilter");
    await q.fill("renamed account b");
    assert.deepEqual(await page.locator(".rt-groups .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["g9"]);
  });
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  test(`${engine}: quota readings arriving during filtering are drawn after blur`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ reducedMotion: "reduce" });
    page.setDefaultTimeout(10000);
    page.setDefaultNavigationTimeout(60000);
    const feed = { data: fixture() };
    await page.route("**/*", (r) => serve(r, "en", feed));
    await page.goto("http://magpie.test/?view=groups");
    await page.locator('.rt-group[data-id="g9"]').waitFor();
    const q = page.locator("input.rt-gfilter");
    await q.focus();
    await page.evaluate(() => {
      quotas = ["A", "B"].map((user) => ({ provider: "p9", name: "Provider 9", kind: "subscription", user,
        windows: [{ name: "Credits", amount: 100, limit: 500, unit: "credits", used: 20 }] }));
    });
    await q.fill("3999");
    assert.equal(await page.locator('.rt-group[data-id="g9"] .rt-gquota').count(), 0, "filtering should reuse the card while typing");
    await q.evaluate((input) => input.blur());
    const line = page.locator('.rt-group[data-id="g9"] .rt-gquota');
    await line.waitFor();
    assert.match(await line.innerText(), /200/);
  });
}
