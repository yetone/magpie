// Run with Node's test runner and Playwright on the module path; see README.md.
// KillTry27355 on X: Provider in model names was one switch for every
// agent. An agent's Details now has its own: As in Settings (every agent's,
// named), On, Not on names I set or Off, saved for that agent by the id its
// lists are kept by (settings.AgentSuffix). An agent whose list doesn't take
// magpie's names (noNames) has none. Every language, narrow too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { agentRow } = require("./reader.cjs");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const words = {
  en: { every: "As in Settings · Off", on: "On", off: "Off", label: "Provider in model names" },
  zh: { every: "跟随设置 · 关闭", on: "开启", off: "关闭", label: "模型名带供应商" },
  "zh-TW": { every: "跟隨設定 · 關閉", on: "開啟", off: "關閉", label: "模型名帶供應商" },
  ja: { every: "設定に従う · オフ", on: "オン", off: "オフ", label: "モデル名にプロバイダ" },
  de: { every: "Wie in Einstellungen · Aus", on: "An", off: "Aus", label: "Anbieter in Modellnamen" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;
const withModels = (a) => ({ ...a, models: { shown: 1, listed: 1, by: [{ name: "Relay", icon: "generic", n: 1 }] } });

async function openPage(t, engine, lang, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-agent-suffix.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(8000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // every agent's is Off; OpenCode has none of its own yet
  const cur = { lang, theme: "light", plainNames: true, agentSuffix: {} };
  const agents = [withModels(agentRow("opencode", "OpenCode")), withModels({ ...agentRow("goose", "Goose"), noNames: true })];
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url()), json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: cur });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/settings/agent-suffix") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const own = { ...cur.agentSuffix };
      if (body.mode) own[body.agent] = body.mode; else delete own[body.agent];
      cur.agentSuffix = own;
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  });
  await page.goto("http://magpie.test/?view=agents");
  await page.locator(row("opencode") + " .ag-link").waitFor();
  return { page, errors, posts };
}

const overflows = (page) => page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1180, 390]) {
    test(`${engine} ${lang} ${width}: an agent's Details sets its own Provider in model names`, async (t) => {
      const w = words[lang];
      const { page, errors, posts } = await openPage(t, engine, lang, width);
      await page.locator(row("opencode") + " .ag-link").click();
      const pick = page.locator(row("opencode") + " .ag-suffix");
      await pick.waitFor();
      const key = pick.locator("xpath=ancestor::div[contains(@class,'ag-kv')]").locator(".ag-k");
      assert.equal(await key.textContent(), w.label);
      assert.equal((await pick.textContent()).trim(), w.every);
      assert.equal(await overflows(page), false, "the page scrolls sideways");

      await pick.click();
      await page.locator(".sess-menu .pm-item", { hasText: new RegExp(`^\\s*${w.on}\\s*$`) }).click();
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="opencode"] .ag-suffix')?.dataset.mode === "on");
      assert.deepEqual(posts, [{ agent: "opencode", mode: "on" }]);
      assert.equal((await page.locator(row("opencode") + " .ag-suffix").textContent()).trim(), w.on);

      // back to every agent's
      await page.locator(row("opencode") + " .ag-suffix").click();
      await page.locator(".sess-menu .pm-item", { hasText: w.every }).click();
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="opencode"] .ag-suffix')?.dataset.mode === "");
      assert.deepEqual(posts.at(-1), { agent: "opencode", mode: "" });
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: an agent whose list takes no names of magpie's has none`, async (t) => {
    const { page, errors } = await openPage(t, engine, "en", 1180);
    await page.locator(row("goose") + " .ag-link").click();
    await page.locator(row("goose") + " .ag-exp").waitFor();
    assert.equal(await page.locator(row("goose") + " .ag-suffix").count(), 0);
    assert.deepEqual(errors, []);
  });
}
