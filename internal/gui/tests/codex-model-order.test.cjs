// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's /model in the user's order (M3chD09, #855): its model list's
// "Order" lists the models shown as Codex does, the account's own first,
// and a drag or Alt+↑/↓ puts one elsewhere, saved at once; "Default order"
// puts magpie's back. The hidden stay out, and nothing scrolls the page.
// Every agent's list has the Order (#1052): Claude Code's, which had none,
// orders and saves the same way, saying it is the order magpie hands it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// magpie's first, as the catalog has them; Codex's own after
const models = () => [
  { id: "relay/claude-opus-5.5", name: "claude-opus-5.5", group: "Relay", icon: "anthropic" },
  { id: "relay/claude-sonnet-4-5", name: "claude-sonnet-4-5", group: "Relay", icon: "anthropic" },
  { id: "relay/old", name: "old", group: "Relay", icon: "anthropic", hidden: true },
  { id: "codex-acc/gpt-5.5", name: "gpt-5.5", group: "ChatGPT", icon: "openai", own: true, inUse: true },
  { id: "codex-acc/gpt-5-codex", name: "gpt-5-codex", group: "ChatGPT", icon: "openai", own: true },
];

function fixture(lang, agent = { id: "codex", name: "Codex", icon: "codex-color" }) {
  // only Codex has models of its own, which the server marks
  const mine = () => models().map((m) => agent.id === "codex" ? m : { ...m, own: undefined });
  const first = mine();
  let list = mine();
  const posts = [];
  const state = () => ({
    agents: [{
      id: agent.id, name: agent.name, path: "/test/config.toml", icon: agent.icon, wired: true,
      fields: [{ key: "model", label: "model", value: "gpt-5.5", options: [{ value: "gpt-5.5", label: "gpt-5.5", ref: "codex-acc/gpt-5.5" }] }],
      models: { shown: 4, listed: 5, by: [{ name: "Relay", icon: "anthropic", n: 2 }, { name: "ChatGPT", icon: "openai", n: 2 }] },
    }],
    profiles: [], settings: { lang, theme: "light" },
  });
  async function serve(route) {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agent-models/" + agent.id) {
      if (req.method() === "POST") {
        const { order } = req.postDataJSON();
        posts.push(order);
        const at = (m) => order.indexOf(m.id) < 0 ? order.length : order.indexOf(m.id);
        list = order.length ? [...first].sort((x, y) => at(x) - at(y)) : first;
        return json({ models: list, ordered: order.length > 0 });
      }
      return json({ models: list, ordered: false });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts };
}

const W = {
  en: { entry: "1 hidden", order: "Order", unorder: "Default order", note: "Drag to put them in the order Codex lists them; new models go last", handed: "Drag to put them in the order magpie hands them to Claude Code; new models go last" },
  zh: { entry: "已隐藏 1 个", order: "排序", unorder: "恢复默认顺序", note: "拖动排列 Codex 列表中的顺序；新模型排在最后", handed: "拖动排列 magpie 交给 Claude Code 的模型顺序；新模型排在最后" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Codex's models in the user's order`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const fx = fixture(lang);
      await page.route("**/*", fx.serve);
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-order.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      const row = page.locator('.row.agent[data-id="codex"]');
      await row.locator(".ag-link").click();
      const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
      await entry.waitFor();
      await page.waitForFunction(() => document.getAnimations().length === 0);
      const scroll = () => page.evaluate(() => [scrollY, document.scrollingElement.scrollTop, $("#view-agents").scrollTop]);
      const was = await scroll();
      await entry.click();
      const pop = page.locator(".am-pop:not(.leaving)");
      await pop.waitFor();

      const seg = pop.locator(".am-seg button", { hasText: w.order });
      assert.equal(await seg.count(), 1, "Codex's list has an Order");
      await seg.click();
      assert(await seg.evaluate((e) => e.classList.contains("on")));
      assert.deepEqual(await scroll(), was, "nothing scrolls");
      // the shown as Codex lists them: its own first, the hidden left out
      const names = async () => (await pop.locator(".am-or .n").allInnerTexts()).map((s) => s.trim());
      assert.deepEqual(await names(), ["gpt-5.5", "gpt-5-codex", "claude-opus-5.5", "claude-sonnet-4-5"]);
      assert.deepEqual(await pop.locator(".am-or .ix").allInnerTexts(), ["1", "2", "3", "4"]);
      assert(!(await pop.locator(".am-search").isVisible()), "no search while ordering");
      assert(!(await pop.locator(".am-rail").isVisible()), "no providers' rail while ordering");
      assert.equal(await pop.locator(".am-foot").innerText().then((s) => s.includes(w.note)), true);
      const unorder = pop.locator(".am-foot .am-reset", { hasText: w.unorder });
      assert(await unorder.isDisabled(), "nothing to put back yet");
      // the grip shows on hover, not before
      const grip = pop.locator(".am-or").nth(2).locator(".grip");
      assert.equal(await grip.evaluate((e) => getComputedStyle(e).opacity), "0");

      // Alt+↓ moves one down, saved at once, and keeps the keys
      await pop.locator(".am-or").first().focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForTimeout(150);
      assert.deepEqual(fx.posts.at(-1), ["codex-acc/gpt-5-codex", "codex-acc/gpt-5.5", "relay/claude-opus-5.5", "relay/claude-sonnet-4-5"]);
      assert.deepEqual(await names(), ["gpt-5-codex", "gpt-5.5", "claude-opus-5.5", "claude-sonnet-4-5"]);
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.id), "codex-acc/gpt-5.5");
      assert(!(await unorder.isDisabled()));

      // a drag puts the last first
      const rows = pop.locator(".am-or");
      const last = await rows.nth(3).boundingBox(), top = await rows.nth(0).boundingBox();
      await page.mouse.move(last.x + 20, last.y + last.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 12; i++) await page.mouse.move(last.x + 20, last.y + last.height / 2 - (last.y - top.y + 10) * i / 12);
      await page.mouse.up();
      await page.waitForTimeout(250);
      assert.deepEqual(fx.posts.at(-1), ["relay/claude-sonnet-4-5", "codex-acc/gpt-5-codex", "codex-acc/gpt-5.5", "relay/claude-opus-5.5"]);
      assert.deepEqual(await names(), ["claude-sonnet-4-5", "gpt-5-codex", "gpt-5.5", "claude-opus-5.5"]);
      assert.deepEqual(await scroll(), was, "nothing scrolls");

      // All shows every one again, the hidden among them, in that order
      await pop.locator(".am-seg button").first().click();
      assert(await pop.locator(".am-search").isVisible());
      await seg.click();
      assert.deepEqual(await names(), ["claude-sonnet-4-5", "gpt-5-codex", "gpt-5.5", "claude-opus-5.5"]);

      // Default order puts magpie's back
      await unorder.click();
      await page.waitForTimeout(150);
      assert.deepEqual(fx.posts.at(-1), []);
      assert.deepEqual(await names(), ["gpt-5.5", "gpt-5-codex", "claude-opus-5.5", "claude-sonnet-4-5"]);
      assert(await unorder.isDisabled());
      assert.deepEqual(await scroll(), was, "nothing scrolls");
      assert.deepEqual(errors, []);
    });
  }
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Claude Code's list has an Order too (#1052)`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      // narrow: the three views still fit beside the search
      const page = await browser.newPage({ viewport: { width: 440, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const fx = fixture(lang, { id: "claude", name: "Claude Code", icon: "claude-color" });
      await page.route("**/*", fx.serve);
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-order.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      const row = page.locator('.row.agent[data-id="claude"]');
      await row.locator(".ag-link").click();
      const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
      await entry.waitFor();
      await page.waitForFunction(() => document.getAnimations().length === 0);
      const scroll = () => page.evaluate(() => [scrollY, document.scrollingElement.scrollTop, $("#view-agents").scrollTop]);
      const was = await scroll();
      await entry.click();
      const pop = page.locator(".am-pop:not(.leaving)");
      await pop.waitFor();
      assert.equal(await pop.locator(".am-seg button").count(), 3);
      const seg = pop.locator(".am-seg button", { hasText: w.order });
      // the three views whole, not squeezed to "…", and inside the box
      for (const b of await pop.locator(".am-seg button").all()) {
        assert(await b.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "a view's name is cut");
      }
      const box = await pop.boundingBox(), sb = await seg.boundingBox();
      assert(sb.x + sb.width <= box.x + box.width + 1 && sb.x >= box.x - 1, "Order is outside the box");
      await seg.click();
      assert.deepEqual(await scroll(), was, "nothing scrolls");
      const names = async () => (await pop.locator(".am-or .n").allInnerTexts()).map((s) => s.trim());
      assert.deepEqual(await names(), ["claude-opus-5.5", "claude-sonnet-4-5", "gpt-5.5", "gpt-5-codex"], "the shown as handed, the hidden left out");
      assert.equal(await pop.locator(".am-foot").innerText().then((s) => s.includes(w.handed)), true);
      await pop.locator(".am-or").first().focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForTimeout(150);
      assert.deepEqual(fx.posts.at(-1), ["relay/claude-sonnet-4-5", "relay/claude-opus-5.5", "codex-acc/gpt-5.5", "codex-acc/gpt-5-codex"]);
      assert.deepEqual(await names(), ["claude-sonnet-4-5", "claude-opus-5.5", "gpt-5.5", "gpt-5-codex"]);
      const unorder = pop.locator(".am-foot .am-reset", { hasText: w.unorder });
      await unorder.click();
      await page.waitForTimeout(150);
      assert.deepEqual(fx.posts.at(-1), []);
      assert.deepEqual(await scroll(), was, "nothing scrolls");
      assert.deepEqual(errors, []);
    });
  }
}
