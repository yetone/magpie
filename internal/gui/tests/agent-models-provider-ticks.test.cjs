// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's model list ticks a provider whole from the rail (coolAndyHe on
// X: every provider is ticked, and an agent meant for one or two takes a
// select-all and a clear-all): all providers' tick clears every model but
// the one in use, at a second click, as the foot's Hide all does, and a
// provider's own tick then puts back just that provider's models, or takes
// them out again, without picking it first.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = () => [
  ...["deepseek-v4", "deepseek-v4-flash", "deepseek-r2"].map((m, i) => ({ id: "deepseek/" + m, name: m, group: "DeepSeek", icon: "deepseek", context: 128e3, inUse: i === 0 })),
  ...["kimi-k3", "kimi-k3-turbo", "kimi-k2.5"].map((m) => ({ id: "kimi/" + m, name: m, group: "Kimi", icon: "moonshot", context: 256e3 })),
  ...Array.from({ length: 10 }, (_, i) => ({ id: "openrouter/m" + i, name: "Router Model " + i, group: "OpenRouter", icon: "openrouter", context: 128e3 })),
  ...["glm-5", "glm-5-air"].map((m) => ({ id: "zhipu/" + m, name: m, group: "Zhipu GLM", icon: "zhipu", context: 200e3 })),
];

function fixture(lang) {
  const list = models();
  const posts = [];
  const count = () => ({ shown: list.filter((m) => !m.hidden).length, listed: list.length, by: [] });
  const state = () => ({
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/deepseek/deepseek-v4", options: [{ value: "magpie/deepseek/deepseek-v4", label: "deepseek-v4", ref: "deepseek/deepseek-v4" }] }],
      models: count(),
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
    if (url.pathname === "/api/agent-models/codex") {
      if (req.method() === "POST") {
        const { hidden } = req.postDataJSON();
        posts.push(hidden);
        for (const m of list) m.hidden = hidden.includes(m.id) && !m.inUse;
        return json({ models: list, count: count() });
      }
      return json({ models: list });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    if (!body) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  }
  return { serve, posts };
}

const W = {
  en: { armHide: /^Hide \d+, of every provider\? Click again$/, hideAll: "Hide all", showAll: "Show all" },
  zh: { armHide: /^隐藏所有供应商的 \d+ 个模型？再点一次$/, hideAll: "全部隐藏", showAll: "全部显示" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a provider ticked whole from the model list's rail`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const fx = fixture(lang);
      await page.route("**/*", fx.serve);
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-ticks.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      const row = page.locator('.row.agent[data-id="codex"]');
      await row.locator(".ag-link").click();
      const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
      await entry.waitFor();
      await page.waitForFunction(() => document.getAnimations().length === 0);
      await entry.click();
      const pop = page.locator(".am-pop:not(.leaving)");
      await pop.waitFor();

      const ticks = pop.locator(".am-rail .am-rk");
      // wait on what the click changes, never on a duration
      const until = async (ok, what) => {
        for (const end = Date.now() + 5000; Date.now() < end; await new Promise((r) => setTimeout(r, 25))) if (await ok()) return;
        assert.fail("timed out waiting for " + what);
      };
      const posted = (n) => until(() => fx.posts.length === n, n + " posts");
      const states = async () => Promise.all((await ticks.all()).map((k) => k.getAttribute("aria-checked")));
      // All providers, then DeepSeek, Kimi, OpenRouter, Zhipu GLM: every one ticked
      assert.equal(await ticks.count(), 5);
      assert.deepEqual(await states(), ["true", "true", "true", "true", "true"]);
      assert.equal(await ticks.nth(2).getAttribute("title"), w.hideAll);

      // all providers' tick asks first, as the foot's Hide all does
      const hideAll = pop.locator(".am-hide");
      await ticks.first().click();
      await until(async () => w.armHide.test(await hideAll.innerText()), "Hide all to ask");
      assert.equal(fx.posts.length, 0, "a first click changes nothing");
      assert.match(await hideAll.innerText(), w.armHide);
      await ticks.first().click();
      await posted(1);
      await until(async () => (await states())[2] === "false", "Kimi cleared");
      assert.equal(fx.posts[0].length, 17, "every model but the one in use");
      assert(!fx.posts[0].includes("deepseek/deepseek-v4"));
      // the one in use stays on, so DeepSeek and the lot are part ticked
      assert.deepEqual(await states(), ["mixed", "mixed", "false", "false", "false"]);
      assert.equal(await ticks.nth(2).getAttribute("title"), w.showAll);

      // Kimi and Zhipu ticked whole, at a click each
      await ticks.nth(2).click();
      await posted(2);
      await ticks.nth(4).click();
      await posted(3);
      await until(async () => (await states())[4] === "true", "Zhipu ticked");
      const last = fx.posts.at(-1);
      assert(!last.some((id) => id.startsWith("kimi/") || id.startsWith("zhipu/")), JSON.stringify(last));
      assert.equal(last.filter((id) => id.startsWith("openrouter/")).length, 10);
      assert.deepEqual(await states(), ["mixed", "mixed", "true", "false", "true"]);
      assert.deepEqual((await pop.locator(".am-rail .am-ri .c").allInnerTexts()), ["6/18", "1/3", "3/3", "0/10", "2/2"]);
      // a tick picks nothing: the list still shows every provider
      assert.equal(await pop.locator(".am-rail .am-ri").first().getAttribute("aria-pressed"), "true");

      // and Kimi cleared again
      await ticks.nth(2).click();
      await posted(4);
      await until(async () => (await states())[2] === "false", "Kimi cleared again");
      assert(fx.posts.at(-1).includes("kimi/kimi-k3"));
      assert.deepEqual(await states(), ["mixed", "mixed", "false", "false", "true"]);

      // the provider with its one in use: ticked whole, then cleared but that one
      await ticks.nth(1).click();
      await posted(5);
      await until(async () => (await ticks.nth(1).getAttribute("aria-checked")) === "true", "DeepSeek ticked");
      await ticks.nth(1).click();
      await posted(6);
      await until(async () => (await ticks.nth(1).getAttribute("aria-checked")) === "mixed", "DeepSeek cleared");
      assert.equal(await ticks.nth(1).getAttribute("aria-checked"), "mixed");
      assert(!fx.posts.at(-1).includes("deepseek/deepseek-v4"));

      // all providers' tick, mixed, ticks every one back at a second click
      await ticks.first().click();
      await ticks.first().click();
      await until(async () => (await states()).every((s) => s === "true"), "every provider ticked");
      assert.deepEqual(fx.posts.at(-1), []);
      assert.deepEqual(await states(), ["true", "true", "true", "true", "true"]);
      assert.deepEqual(errors, []);
    });
  }
}
