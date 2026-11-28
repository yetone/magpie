// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's model list goes by maker too, as a provider's editor does
// (elis_pina on X: 给Agent配置模型的那里也加一个分类，这样可以给对应模型用对应的
// 厂商模型就好不用一个一个点): By maker puts every provider's models of one
// maker under it, in the editor's order of makers and newest first, each
// row saying whose it is; a maker's tick in the rail takes out or puts
// back all its models across providers at a click; a search still finds
// them, by maker too; the choice is kept for the next time, and a narrow
// window draws the makers without the rail.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = () => [
  { id: "anthropic/claude-opus-4-5", name: "Claude Opus 4.5", group: "Anthropic", icon: "claude-color", logo: "claude-color", maker: "claude-color", context: 200e3 },
  { id: "anthropic/claude-sonnet-4-5", name: "Claude Sonnet 4.5", group: "Anthropic", icon: "claude-color", logo: "claude-color", maker: "claude-color", context: 200e3 },
  { id: "codex/gpt-5.4", name: "GPT-5.4", group: "Codex", icon: "codex-color", logo: "openai", maker: "openai", context: 272e3 },
  { id: "codex/gpt-5.5", name: "GPT-5.5", group: "Codex", icon: "codex-color", logo: "openai", maker: "openai", context: 272e3, inUse: true },
  { id: "openrouter/anthropic/claude-opus-4.6", name: "Claude Opus 4.6", group: "OpenRouter", icon: "openrouter", logo: "claude-color", maker: "claude-color", context: 1e6 },
  { id: "openrouter/openai/gpt-5.5", name: "GPT-5.5", group: "OpenRouter", icon: "openrouter", logo: "openai", maker: "openai", context: 400e3 },
  { id: "openrouter/google/gemini-3-pro", name: "Gemini 3 Pro", group: "OpenRouter", icon: "openrouter", logo: "gemini-color", maker: "gemini-color", context: 1e6 },
  { id: "openrouter/z-ai/glm-5", name: "GLM-5", group: "OpenRouter", icon: "openrouter", logo: "zhipu-color", maker: "zhipu-color", context: 200e3 },
  { id: "openrouter/meta-llama/llama-4-maverick", name: "Llama 4 Maverick", group: "OpenRouter", icon: "openrouter", logo: "openrouter", context: 1e6 },
  { id: "group/fast", name: "fast", group: "Routing groups", icons: ["openrouter", "codex-color"], context: 272e3 },
];

function fixture(lang) {
  const list = models();
  const posts = [];
  const count = () => ({ shown: list.filter((m) => !m.hidden).length, listed: list.length, by: [] });
  const state = () => ({
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/codex/gpt-5.5", options: [{ value: "magpie/codex/gpt-5.5", label: "GPT-5.5", ref: "codex/gpt-5.5" }] }],
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
  en: { byProvider: "By provider", byMaker: "By maker", allProviders: "All providers", allMakers: "All makers", routes: "Routing groups", other: "Other", zhipu: "Zhipu", showAll: "Show all", hideAll: "Hide all" },
  zh: { byProvider: "按供应商", byMaker: "按厂商", allProviders: "全部供应商", allMakers: "全部厂商", routes: "路由组", other: "其他", zhipu: "智谱", showAll: "全部显示", hideAll: "全部隐藏" },
};

async function open(engine, lang, t, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 760 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang);
  await page.route("**/*", fx.serve);
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-by-maker.png`) });
    }
    await browser.close();
  });
  await page.goto("http://magpie.test/");
  const row = page.locator('.row.agent[data-id="codex"]');
  await row.locator(".ag-link").click();
  const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
  await entry.waitFor();
  await page.waitForFunction(() => document.getAnimations().length === 0);
  const pop = page.locator(".am-pop:not(.leaving)");
  const show = async () => {
    await entry.click();
    await pop.waitFor();
  };
  await show();
  return { page, pop, fx, errors, show };
}

// wait on what the click changes, never on a duration
const until = async (ok, what) => {
  for (const end = Date.now() + 5000; Date.now() < end; await new Promise((r) => setTimeout(r, 25))) if (await ok()) return;
  assert.fail("timed out waiting for " + what);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: an agent's model list by maker`, async (t) => {
      const w = W[lang];
      const { page, pop, fx, errors, show } = await open(engine, lang, t, 1000);
      const seg = pop.locator(".am-head .am-by button");
      const rail = () => pop.locator(".am-rail .am-ri .rn").allInnerTexts();
      const ticks = pop.locator(".am-rail .am-rk");

      // by provider first, as it always was
      assert.deepEqual(await seg.allInnerTexts(), [w.byProvider, w.byMaker]);
      assert.equal(await seg.first().getAttribute("class"), "on");
      assert.deepEqual(await rail(), [w.allProviders, w.routes, "Anthropic", "Codex", "OpenRouter"]);

      // by maker: the routing groups first, then the makers in the
      // editor's order, Other last
      await seg.nth(1).click();
      await until(async () => (await rail())[0] === w.allMakers, "the rail by maker");
      assert.deepEqual(await rail(), [w.allMakers, w.routes, "Anthropic", "OpenAI", "Google", w.zhipu, w.other]);
      assert.deepEqual(await pop.locator(".am-rail .am-ri .c").allInnerTexts(), ["10/10", "1/1", "3/3", "3/3", "1/1", "1/1", "1/1"]);
      assert.equal(await pop.locator(".am-rail").isVisible(), true);

      // a maker picked: its models alone, the newest first, each saying
      // its provider by name and logo
      await pop.locator(".am-rail .am-ri").nth(2).click();
      await until(async () => (await pop.locator(".am-g").count()) === 1, "Anthropic alone");
      assert.equal(await pop.locator(".am-g .am-gh .gn").innerText(), "Anthropic");
      assert.deepEqual(await pop.locator(".am-mr .n").allInnerTexts(), ["Claude Opus 4.6", "Claude Opus 4.5", "Claude Sonnet 4.5"]);
      assert.deepEqual(await pop.locator(".am-mr .am-pv").allInnerTexts(), ["OpenRouter", "Anthropic", "Anthropic"]);
      assert.deepEqual(await pop.locator(".am-mr .lg").evaluateAll((es) => es.map((e) => e.dataset.icon)), ["openrouter", "claude-color", "claude-color"]);

      // OpenAI's tick clears its models of every provider at a click,
      // but the one the agent is set to
      await ticks.nth(3).click();
      await until(() => fx.posts.length === 1, "a post");
      assert.deepEqual([...fx.posts[0]].sort(), ["codex/gpt-5.4", "openrouter/openai/gpt-5.5"]);
      await until(async () => (await ticks.nth(3).getAttribute("aria-checked")) === "mixed", "OpenAI part ticked");
      assert.equal(await ticks.nth(2).getAttribute("aria-checked"), "true", "Anthropic untouched");
      // and puts them back
      await ticks.nth(3).click();
      await until(() => fx.posts.length === 2, "a second post");
      assert.deepEqual(fx.posts[1], []);
      await until(async () => (await ticks.nth(3).getAttribute("aria-checked")) === "true", "OpenAI ticked");

      // a search looks in every maker: by a provider's name, and by a maker's
      const q = pop.locator(".am-search input");
      await q.fill("openrouter");
      await until(async () => (await pop.locator(".am-mr").count()) === 5, "OpenRouter's models");
      assert.deepEqual(await pop.locator(".am-g .am-gh .gn").allInnerTexts(), ["Anthropic", "OpenAI", "Google", w.zhipu, w.other]);
      await q.fill("google");
      await until(async () => (await pop.locator(".am-mr").count()) === 1, "Google's model");
      assert.deepEqual(await pop.locator(".am-mr .n").allInnerTexts(), ["Gemini 3 Pro"]);
      await q.fill("");

      // kept for the next time
      await page.keyboard.press("Escape");
      await until(async () => (await page.locator(".am-pop").count()) === 0, "the list closed");
      await show();
      await until(async () => (await rail())[0] === w.allMakers, "by maker again");
      assert.equal(await seg.nth(1).getAttribute("class"), "on");
      // and back by provider
      await seg.first().click();
      await until(async () => (await rail())[0] === w.allProviders, "by provider again");
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.agentModelsBy")), '""');
      await seg.nth(1).click();
      await until(async () => (await rail())[0] === w.allMakers, "by maker for the picture");
      // the list stays inside the window
      const box = await pop.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 1000, JSON.stringify(box));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: an agent's model list by maker in a narrow window`, async (t) => {
      const w = W[lang];
      const { pop, errors } = await open(engine, lang, t, 420);
      const seg = pop.locator(".am-head .am-by button");
      await seg.nth(1).click();
      await until(async () => (await pop.locator(".am-g .am-gh .gn").count()) === 6, "the makers");
      // no rail: each maker a section, with its own Show all or Hide all
      assert.equal(await pop.locator(".am-rail").count(), 0);
      assert.deepEqual(await pop.locator(".am-g .am-gh .gn").allInnerTexts(), [w.routes, "Anthropic", "OpenAI", "Google", w.zhipu, w.other]);
      assert.deepEqual(await pop.locator(".am-g .am-all").allInnerTexts(), Array(6).fill(w.hideAll));
      const box = await pop.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, JSON.stringify(box));
      // the title and the choice share a line, nothing past the edge
      const head = await pop.locator(".am-ht").evaluate((e) => [e.scrollWidth, e.clientWidth]);
      assert(head[0] <= head[1], JSON.stringify(head));
      assert.deepEqual(errors, []);
    });
  }
}
