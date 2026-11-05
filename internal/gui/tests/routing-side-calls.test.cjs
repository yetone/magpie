// Run with Node's test runner and Playwright on the module path; see README.md.
// A DeepSeek chat in Codex showed Codex's GPT answering in the Requests
// list (#314: 为啥 deepseek 对话会调用 gpt): the new chat's title, which
// Codex asks of gpt-6-luna on a hidden thread of its own, and the web
// searches magpie runs for DeepSeek on the model it searches with. Each
// says what it was by its model, and the search's story whose it was.
// A search magpie asked a search API for is listed too (axelburks on
// Discord: 搜索API代搜时 路由页面不显示是请求了哪个API): its story names the
// API, and its row the API, where it was asked and the key.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = (model, provider) => ({ id: provider, provider, name: provider === "codex" ? "Codex" : "DeepSeek", who: "a@b.c", kind: provider === "codex" ? "account" : "key", model });
const call = (id, i, agent, model, provider, extra) => ({
  id, seq: id, time: at(i), agent, model: provider + "/" + model, provider, ...extra,
  order: [seat(model, provider)], tries: [{ id: provider, model, start: at(i), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 1200,
});
// as the gateway lists a search API's ask (keepSearch): the API by name,
// its host, the key masked
const asked = {
  id: 105, seq: 105, time: at(-0.5), agent: "magpie", model: "Tavily", provider: "api.tavily.com", kind: "web_search",
  for: { agent: "codex", model: "deepseek/deepseek-flash" }, archive: "2026-11-05/040654-a02c8d68dfe256d8",
  order: [{ id: "tavily:k1", provider: "Tavily", name: "Tavily", who: "tvly…ab12", kind: "key", model: "Tavily" }],
  tries: [{ id: "tavily:k1", model: "Tavily", start: at(-0.5), done: true, status: 200, ms: 820 }], done: true, status: 200, ms: 820,
};
const routes = [
  asked,
  call(104, 0, "magpie", "gpt-6.1-sol", "codex", { kind: "web_search", for: { agent: "codex", model: "deepseek/deepseek-flash" } }),
  call(103, 1, "magpie", "gpt-6.1-sol", "codex", { kind: "web_search" }),
  call(102, 2, "codex", "deepseek-flash", "deepseek", {}),
  call(101, 3, "codex", "gpt-6-luna", "codex", { kind: "thread_title" }),
];

function serve(lang) {
  const state = {
    agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }],
    clients: [{ id: "codex", name: "Codex", icon: "codex-color" }, { id: "magpie", name: "magpie", icon: "magpie" }],
    profiles: [], settings: { lang, theme: "light" },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { api: /Codex's deepseek\/deepseek-flash, which can't search the web by itself: Tavily searched/, tags: ["Web search", "Web search", "Web search", "", "Title"], story: /magpie ran this web search for Codex's deepseek\/deepseek-flash, which can't search the web by itself: codex\/gpt-6\.1-sol searched/, bare: /for a model that can't search the web by itself: codex\/gpt-6\.1-sol searched/, title: /Codex made this call itself \(Title\)/ },
  zh: { api: /magpie 代 Codex 的 deepseek\/deepseek-flash 发起的联网搜索.*由 Tavily 搜索/, tags: ["联网搜索", "联网搜索", "联网搜索", "", "标题"], story: /magpie 代 Codex 的 deepseek\/deepseek-flash 发起的联网搜索.*由 codex\/gpt-6\.1-sol 搜索/, bare: /代一个无法自行搜索的模型发起的联网搜索：由 codex\/gpt-6\.1-sol 搜索/, title: /Codex 自行发起的调用（标题）/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a title and magpie's web search say what they were`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-side-calls.png`), fullPage: true });
        }
        await browser.close();
      });
      // Routing's groups, drawn above the list, can land after the day's
      // list does and push it down (#1640): what is measured below is the
      // page with them in
      const grouped = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/groups");
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      await grouped;

      const tags = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.querySelector(".asked .kind")?.textContent || ""));
      assert.deepEqual(tags, want[lang].tags);

      const story = async (i) => {
        const row = page.locator(".rt-req").nth(i);
        // the list is there a frame or two before the page is done with it:
        // the next frame's redraw and fit grow the part above, and the day
        // clicked is held in place by scrolling. Where the row was is taken
        // once it has stayed put for 10 frames, not as it is drawn (#1640)
        await row.evaluate((e) => new Promise((done) => {
          let last = e.getBoundingClientRect().top, still = 0;
          const look = () => { const y = e.getBoundingClientRect().top; if (y === last) still++; else { last = y; still = 0; } if (still >= 10) done(); else requestAnimationFrame(look); };
          requestAnimationFrame(look);
        }));
        const was = await row.evaluate((e) => e.getBoundingClientRect().top);
        const scrolled = await page.evaluate(() => document.scrollingElement.scrollTop);
        await row.click();
        await page.waitForTimeout(400);
        assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the row");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scrolled, "picking the request scrolled the page");
        return page.locator(".rt-steps li.kind");
      };
      let s = await story(0);
      assert.match(await s.textContent(), want[lang].api);
      const title = await page.locator(".rt-req").nth(0).getAttribute("title");
      assert(title.includes("Tavily") && title.includes("api.tavily.com"), title);
      s = await story(1);
      assert.equal(await s.locator(".kind").textContent(), want[lang].tags[0]);
      assert.match(await s.textContent(), want[lang].story);
      s = await story(2);
      assert.match(await s.textContent(), want[lang].bare);
      s = await story(4);
      assert.match(await s.textContent(), want[lang].title);
      s = await story(3);
      assert.equal(await s.count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
