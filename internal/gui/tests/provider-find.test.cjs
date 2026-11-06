// Run with Node's test runner and Playwright on the module path; see README.md.
// #1614 (ququdu): "供应商很多，然后经常需要翻找，排序拖动也比较慢". The
// Providers page has a search box over the list: the words typed keep the
// rows that say them, matched on the name, the id, a model's id, an
// account's user and the rest, the ones turned off too (their fold opens
// while searching); nothing found says so; Escape clears it and puts the
// fold back. A row's right-click has Move to Top, Move Up, Move Down and
// Move to Bottom, then Use first, Alt+Shift+↑/↓ go to the top or the bottom from the
// keyboard. While searching, a row moves among the ones shown (Alt+↑/↓, a
// drag) and the ones hidden keep their order. Nothing scrolls, and in a
// narrow window nothing runs off to the side. Every language, Chromium and
// WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const prov = (id, name, extra = {}) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: id + "-chat", name: name + " Chat", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ...extra,
});

function fixture(lang) {
  const providers = {
    providers: [
      prov("openai", "OpenAI"),
      prov("relay-one", "Relay One", { models: [{ id: "gpt-5", name: "GPT-5", on: true }] }),
      prov("anthropic", "Anthropic"),
      prov("relay-two", "Relay Two", { models: [{ id: "gpt-5", name: "GPT-5", on: true }] }),
      prov("deepseek", "DeepSeek", { models: [{ id: "deepseek-v3.2", name: "DeepSeek V3.2", on: true }] }),
      prov("relay-three", "Relay Three", { models: [{ id: "gpt-5", name: "GPT-5", on: true }] }),
      prov("moonshot", "Moonshot", { keyList: [{ id: "k1", name: "work key", masked: "sk-…1", active: true, on: true }] }),
      prov("zhipu", "Zhipu GLM", { account: { user: "alice@example.com", agentName: "ZCode", agentIcon: "generic", plan: "Coding Plan" } }),
      prov("relay-off", "Relay Off", { off: true, models: [{ id: "gpt-5", name: "GPT-5", on: true }] }),
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const posted = [];
  const route = async (route) => {
    const request = route.request(), url = new URL(request.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers/arrange") {
      const { order } = request.postDataJSON();
      posted.push(order);
      providers.providers = order.map((id) => providers.providers.find((p) => p.id === id));
      return json(providers);
    }
    if (request.method() !== "GET") posted.push(url.pathname);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return { posted, route, providers };
}

const ON = ["openai", "relay-one", "anthropic", "relay-two", "deepseek", "relay-three", "moonshot", "zhipu"];
// the rows that show, on and off
const shown = (page) => page.evaluate(() => {
  const ids = (sel) => [...document.querySelectorAll(sel)].filter((r) => r.getClientRects().length).map((r) => r.dataset.id);
  return { on: ids("#providers > .row.provider"), off: ids("#offProviders > .row.provider") };
});
const none = (page) => page.evaluate(() => {
  const n = document.querySelector("#pvNone");
  return n.getClientRects().length ? n.textContent : "";
});
const scrolled = (page) => page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-providers").scrollTop]);
const overflow = (page) => page.evaluate(() => {
  const out = [], de = document.documentElement, v = document.querySelector("#view-providers");
  if (de.scrollWidth > de.clientWidth) out.push("page " + de.scrollWidth + ">" + de.clientWidth);
  if (v.scrollWidth > v.clientWidth) out.push("view " + v.scrollWidth + ">" + v.clientWidth);
  const r = v.getBoundingClientRect(), q = document.querySelector("#pvQ").getBoundingClientRect();
  if (q.left < r.left - 0.5 || q.right > r.right + 0.5) out.push("box " + q.left + "-" + q.right);
  return out;
});

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Providers page can be searched, and a provider moved to the top", { timeout: 180000 }, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      await browser.close();
      assert.deepEqual(errors, []);
    });
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 420, height: 560 }, reducedMotion: "reduce" });
        const page = await ctx.newPage(), f = fixture(lang);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", f.route);
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#providers > .row.provider").nth(7).waitFor();
        const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
        const q = page.locator("#pvQ");
        const type = async (s) => { await q.fill(s); await page.waitForTimeout(60); };

        // the box is over the list, in the shown language, inside the window
        assert(await q.isVisible(), "no search box on the Providers page");
        assert.equal(await q.getAttribute("placeholder"), await tr("Find a provider"));
        assert.equal(await q.getAttribute("aria-label"), await tr("Find a provider"));
        if (lang !== "en") assert.notEqual(await tr("Find a provider"), "Find a provider", "no translation");
        assert.deepEqual(await overflow(page), []);
        assert.deepEqual(await shown(page), { on: ON, off: [] }, "the ones off start folded");
        const y0 = await scrolled(page);

        // by name, case aside; by a model's id, the one off too and its fold
        // opened; by an account's user; by a key's name; by two words
        await type("deepSEEK");
        assert.deepEqual(await shown(page), { on: ["deepseek"], off: [] });
        await type("gpt-5");
        assert.deepEqual(await shown(page), { on: ["relay-one", "relay-two", "relay-three"], off: ["relay-off"] });
        assert.equal(await page.locator("#foldOff").getAttribute("aria-expanded"), "true");
        await type("alice@example");
        assert.deepEqual(await shown(page), { on: ["zhipu"], off: [] });
        await type("work key");
        assert.deepEqual(await shown(page), { on: ["moonshot"], off: [] });
        await type("relay two");
        assert.deepEqual(await shown(page), { on: ["relay-two"], off: [] });
        assert.equal(await none(page), "");

        // nothing found says so
        await type("no-such-thing");
        assert.deepEqual(await shown(page), { on: [], off: [] });
        assert.equal(await none(page), await tr("No provider matches “{q}”", { q: "no-such-thing" }));
        assert.deepEqual(await overflow(page), []);

        // while searching, Alt+↑ moves a row above the one shown above it;
        // the ones hidden keep their order
        await type("relay");
        assert.deepEqual((await shown(page)).on, ["relay-one", "relay-two", "relay-three"]);
        await page.locator('#providers > .row.provider[data-id="relay-three"] .pv-handle').focus();
        await page.keyboard.press("Alt+ArrowUp");
        await page.waitForFunction(() => document.querySelector("#status")?.textContent);
        await page.waitForFunction(() => [...document.querySelectorAll("#providers > .row.provider")].filter((r) => !r.hidden).map((r) => r.dataset.id).join() === "relay-one,relay-three,relay-two");
        assert.deepEqual(f.posted.at(-1), ["openai", "relay-one", "anthropic", "relay-three", "relay-two", "deepseek", "moonshot", "zhipu", "relay-off"]);
        await page.waitForFunction(() => document.activeElement?.closest(".row.provider")?.dataset.id === "relay-three");
        assert.equal(await q.inputValue(), "relay", "the move cleared the search");

        // a drag among the ones shown: relay-two dropped on relay-one takes
        // its place, relay-one and the hidden anthropic after it
        const box = async (id) => page.locator(`#providers > .row.provider[data-id="${id}"] .pv-handle`).boundingBox();
        const from = await box("relay-two"), to = await box("relay-one");
        await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
        await page.mouse.down();
        await page.mouse.move(from.x + from.width / 2, to.y - 10, { steps: 12 });
        await page.locator("#providers > .row.dragging").waitFor();
        await page.mouse.up();
        await page.waitForFunction(() => [...document.querySelectorAll("#providers > .row.provider")].filter((r) => !r.hidden).map((r) => r.dataset.id).join() === "relay-two,relay-one,relay-three");
        assert.deepEqual(f.posted.at(-1), ["openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "moonshot", "zhipu", "relay-off"]);

        // Escape clears it: every row back, the fold as it was
        await q.focus();
        await page.keyboard.press("Escape");
        assert.equal(await q.inputValue(), "");
        assert.deepEqual(await shown(page), { on: ["openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "moonshot", "zhipu"], off: [] });
        assert.equal(await page.locator("#foldOff").getAttribute("aria-expanded"), "false");
        assert.equal(await none(page), "");

        // the last one's right-click: Move to Top goes first, with nothing scrolled
        const row = page.locator('#providers > .row.provider[data-id="zhipu"]');
        await row.locator(".name").click({ button: "right" });
        const menu = page.locator(".pop.row-menu");
        await menu.waitFor();
        const names = await menu.locator(".rm-name").allTextContents();
        assert.deepEqual(names, [await tr("Move to top"), await tr("Move up"), await tr("Move down"), await tr("Move to bottom"), await tr("Use first")]);
        assert.deepEqual(await menu.locator(".rm-item").evaluateAll((bs) => bs.map((b) => b.disabled)), [false, false, true, true, false]);
        await menu.locator(".rm-item").first().click();
        await page.waitForFunction(() => document.querySelector("#providers > .row.provider").dataset.id === "zhipu");
        assert.deepEqual(f.posted.at(-1), ["zhipu", "openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "moonshot", "relay-off"]);
        assert.equal(await page.locator("#providers > .row.provider.selected").count(), 0, "the right-click opened the row");

        // the first one's: Move to Bottom
        await page.locator('#providers > .row.provider[data-id="zhipu"] .name').click({ button: "right" });
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".rm-item").evaluateAll((bs) => bs.map((b) => b.disabled)), [true, true, false, false, false]);
        await menu.locator(".rm-item").nth(3).click();
        await page.waitForFunction(() => [...document.querySelectorAll("#providers > .row.provider")].at(-1).dataset.id === "zhipu");
        assert.deepEqual(f.posted.at(-1), ["openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "moonshot", "zhipu", "relay-off"]);

        // Alt+Shift+↑ from the keyboard: to the top, the keyboard kept on it
        await page.locator('#providers > .row.provider[data-id="moonshot"] .pv-handle').focus();
        await page.keyboard.press("Alt+Shift+ArrowUp");
        await page.waitForFunction(() => document.querySelector("#providers > .row.provider").dataset.id === "moonshot");
        await page.waitForFunction(() => document.activeElement?.closest(".row.provider")?.dataset.id === "moonshot");
        assert.deepEqual(f.posted.at(-1), ["moonshot", "openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "zhipu", "relay-off"]);
        await page.keyboard.press("Alt+Shift+ArrowDown");
        await page.waitForFunction(() => [...document.querySelectorAll("#providers > .row.provider")].at(-1).dataset.id === "moonshot");
        assert.deepEqual(f.posted.at(-1), ["openai", "relay-two", "relay-one", "anthropic", "relay-three", "deepseek", "zhipu", "moonshot", "relay-off"]);

        // only the order was posted, and nothing scrolled
        assert(f.posted.every((x) => Array.isArray(x)), "something else was posted: " + JSON.stringify(f.posted.filter((x) => !Array.isArray(x))));
        assert.deepEqual(await scrolled(page), y0);
        assert.deepEqual(await overflow(page), []);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await type("relay");
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-provider-find-${lang}.png`) });
        }
        await ctx.close();
      });
    }
  });
}
