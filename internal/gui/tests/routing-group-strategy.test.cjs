// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's routing is switched from its row (MoChou, #3450): the
// routing shown on the row is a button that opens the app's own menu of the
// routings the editor offers, the current one checked. Picking one saves the
// group as the editor's Save does with only its routing changed (Manual
// keeps the pick it had; a routing that can't send a rate limited one to the
// back saves sink off), the editor stays shut and nothing on the page moves.
// From the keyboard: Enter opens it on the current one, arrows move, Enter
// picks, Escape shuts it; the focus stays on the row's routing. At a narrow
// window the routing stays on the row. In English, Chinese, Traditional
// Chinese, Japanese and German, light and dark.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/sol", name: "sol", providerName: "A", icon: "generic" },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic" },
  { id: "c/luna", name: "luna", providerName: "C", icon: "generic" },
];
const info = [{ id: "a/sol", ready: true }, { id: "b/flash", ready: true }, { id: "c/luna", ready: true }];
const base = { members: ["a/sol", "b/flash", "c/luna"], ready: true, memberInfo: info, offers: [], shared: [] };

const words = {
  en: { order: "In order", manual: "Manual", weight: "By weight", smart: "Smart", head: "Routing", edit: "Edit",
    names: ["Smart", "In order", "Until used up", "In turn", "Least used", "Use before expiry", "Free first", "Speed first", "Cheapest first", "Free quota by expiry", "By weight", "Manual"],
    tip: "How Auto routes: click to change it", saved: "Auto: Manual" },
  zh: { order: "按顺序", manual: "手动", weight: "按权重", smart: "智能", head: "路由", edit: "编辑",
    tip: "Auto 的路由方式：点击更改", saved: "Auto：手动" },
  "zh-TW": { tip: "Auto 的路由方式：點擊更改" },
  ja: { tip: "Auto のルーティング方法：クリックで変更" },
  de: { tip: "Wie Auto weiterleitet: zum Ändern klicken" },
};

function serve(lang, theme, web, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme } };
  const g = {
    hand: { ...base, id: "hand", name: "Hand", routing: "manual", pick: "b/flash", picked: "b/flash", affinity: "session" },
    auto: { ...base, id: "auto", name: "Auto", routing: "order", sink: true, affinity: "turn", rules: [{ use: "c/luna", tokens: 100000 }] },
  };
  const groups = () => ({ models, pools: [], groups: [
    // filler above, so that the reader scrolls down to the groups
    ...Array.from({ length: 14 }, (_, i) => ({ ...base, id: "f" + i, name: "Filler " + i, routing: "" })),
    g.hand, g.auto,
  ] });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/save" && g[body.id]) {
        // as provider.cleanGroup: Manual with no pick takes the first member
        const pick = body.routing === "manual" && !body.pick ? body.members[0] : body.pick;
        g[body.id] = { ...g[body.id], ...body, pick, picked: body.routing === "manual" ? pick : undefined };
      }
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, { lang, theme = "light", web = true, width = 1100 }) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 700 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, theme, web, posts));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-group", { hasText: "Auto" }).waitFor();
  return { page, errors, posts };
}

// the reader wheels the row into the middle of the view (code that scrolls
// is put back: only the reader moves the view)
async function wheelTo(page, row, width) {
  const dy = await row.evaluate((g) => { const v = document.querySelector("#view-routing");
    return Math.round(g.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
  await page.mouse.move(width / 2, 400);
  await page.mouse.wheel(0, dy);
  await page.waitForTimeout(400);
}

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.waitForTimeout(500); // the menu's opening, done
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    for (const theme of ["light", "dark"]) {
      test(`${engine} ${lang} ${theme}: a group's routing is switched from its row`, async (t) => {
        const { page, errors, posts } = await open(t, engine, { lang, theme });
        const card = () => page.locator(".rt-group", { hasText: "Auto" });
        const btn = () => card().locator("button.rt-groute");
        assert.equal(await btn().count(), 1, "the row's routing is a button");
        assert.equal(await btn().textContent(), w.order);
        assert.equal(await btn().getAttribute("aria-haspopup"), "menu");
        assert.equal(await btn().getAttribute("aria-expanded"), "false");
        assert.equal(await btn().getAttribute("title"), w.tip);
        assert.equal(await page.locator(".rt-group select, select").count(), 0, "no native select");

        // the reader scrolls the card to the middle, then opens its routing
        const view = page.locator("#view-routing");
        const dy = await card().evaluate((g) => { const v = document.querySelector("#view-routing");
          return Math.round(g.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
        await page.mouse.move(550, 400);
        await page.mouse.wheel(0, dy);
        await page.waitForTimeout(400);
        const at = () => view.evaluate((v) => v.scrollTop + "|" + Math.round([...document.querySelectorAll(".rt-group")].find((r) => r.dataset.id === "auto").getBoundingClientRect().top));
        const before = await at();
        assert.ok(parseInt(before) > 200, `the view must be scrolled down to the group (${before})`);
        await btn().click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal(await btn().getAttribute("aria-expanded"), "true");
        assert.equal(await menu.locator(".pm-head").textContent(), w.head);
        const items = menu.locator(".pm-item");
        if (w.names) assert.deepEqual(await items.locator(".pm-name").allTextContents(), w.names);
        assert.equal(await items.count(), 12);
        assert.deepEqual(await menu.locator('.pm-item[aria-checked="true"] .pm-name').allTextContents(), [w.order]);
        assert.equal(await page.locator(".rt-gedit").count(), 0, "opening the menu opens no editor");
        // the menu sits inside the window
        const box = await menu.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= 1100 && box.y >= 0 && box.y + box.height <= 700, JSON.stringify(box));
        await shot(page, `${engine}-${lang}-${theme}-menu`);

        // Manual: saved with only the routing changed, sink off, the
        // group's other fields as they were; nothing moves
        await items.filter({ hasText: w.manual }).click();
        await page.waitForFunction(() => document.querySelector('.rt-group[data-id="auto"] .rt-picks'));
        assert.equal(await at(), before, "the pick moved nothing");
        assert.equal(await page.locator(".proto-menu").count(), 0);
        assert.equal(await page.locator(".rt-gedit").count(), 0, "the editor stays shut");
        assert.equal(posts.length, 1);
        assert.equal(posts[0].path, "/api/groups/save");
        const b = posts[0].body;
        assert.deepEqual({ id: b.id, name: b.name, routing: b.routing, pick: b.pick, members: b.members, affinity: b.affinity, sink: b.sink, rules: b.rules },
          { id: "auto", name: "Auto", routing: "manual", pick: "", members: ["a/sol", "b/flash", "c/luna"], affinity: "turn", sink: false, rules: [{ use: "c/luna", tokens: 100000 }] });
        assert.equal(await btn().textContent(), w.manual);
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.saved || words.en.saved);
        await shot(page, `${engine}-${lang}-${theme}-row`);

        // the one checked already saves nothing
        await btn().click();
        await page.locator('.proto-menu .pm-item[aria-checked="true"]', { hasText: w.manual }).click();
        await page.waitForTimeout(200);
        assert.equal(posts.length, 1);

        // the keyboard: Hand's routing, Enter opens on Manual, ↑ is By
        // weight, Enter saves it with Hand's pick kept
        const hand = page.locator('.rt-group[data-id="hand"] button.rt-groute');
        await hand.focus();
        await page.keyboard.press("Enter");
        await menu.waitFor();
        assert.equal(await page.evaluate(() => document.activeElement.querySelector(".pm-name")?.textContent), w.manual);
        await page.keyboard.press("Escape");
        assert.equal(await page.locator(".proto-menu").count(), 0);
        assert.equal(await page.evaluate(() => document.activeElement.classList.contains("rt-groute")), true);
        await page.keyboard.press("Enter");
        await menu.waitFor();
        await page.keyboard.press("ArrowUp");
        assert.equal(await page.evaluate(() => document.activeElement.querySelector(".pm-name")?.textContent), w.weight);
        await page.keyboard.press("Enter");
        await page.waitForFunction(() => !document.querySelector('.rt-group[data-id="hand"] .rt-picks'));
        assert.equal(posts.length, 2);
        assert.deepEqual({ id: posts[1].body.id, routing: posts[1].body.routing, pick: posts[1].body.pick }, { id: "hand", routing: "weight", pick: "b/flash" });
        await page.waitForFunction(() => document.activeElement?.closest('.rt-group[data-id="hand"]') && document.activeElement.classList.contains("rt-groute"));
        assert.equal(await page.locator(".rt-gedit").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }

  // narrow windows: the app's window at 520, where the row's other tags
  // give way, and the web page at a phone's width
  for (const [web, width] of [[false, 520], [true, 390]]) {
    test(`${engine}: at ${width}px (${web ? "web" : "window"}) a group's routing stays on its row`, async (t) => {
      const { page, errors, posts } = await open(t, engine, { lang: "en", web, width });
      const card = page.locator('.rt-group[data-id="auto"]');
      await wheelTo(page, card, width);
      const btn = card.locator("button.rt-groute");
      assert.ok(await btn.isVisible(), "the routing is on the row");
      const r = await btn.boundingBox();
      assert.ok(r.x >= 0 && r.x + r.width <= width, JSON.stringify(r));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
      if (!web) assert.equal(await card.locator(".tags > .tag:not(.rt-groute)").evaluateAll((ts) => ts.filter((x) => x.offsetParent).length), 0, "the other tags give way");
      await btn.click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      const box = await menu.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));
      await menu.locator(".pm-item", { hasText: "Smart" }).click();
      await page.waitForFunction(() => document.querySelector('.rt-group[data-id="auto"] .rt-groute')?.textContent === "Smart");
      assert.equal(posts.at(-1).body.routing, "");
      assert.deepEqual(errors, []);
    });
  }

  // every language has the button's words
  for (const lang of ["zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the row's routing says what it does`, async (t) => {
      const { page, errors } = await open(t, engine, { lang });
      const btn = page.locator('.rt-group[data-id="auto"] button.rt-groute');
      await wheelTo(page, page.locator('.rt-group[data-id="auto"]'), 1100);
      assert.equal(await btn.getAttribute("title"), words[lang].tip);
      await btn.click();
      await page.locator(".proto-menu").waitFor();
      assert.equal(await page.locator(".proto-menu .pm-item").count(), 12);
      assert.deepEqual(errors, []);
    });
  }
}
