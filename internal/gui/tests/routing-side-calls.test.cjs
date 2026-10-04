// Run with Node's test runner and Playwright on the module path; see README.md.
// A DeepSeek chat in Codex showed Codex's GPT answering in the Requests
// list (#314: 为啥 deepseek 对话会调用 gpt): the new chat's title, which
// Codex asks of gpt-6-luna on a hidden thread of its own, and the web
// searches magpie runs for DeepSeek on the model it searches with. Each
// says what it was by its model, and the search's story whose it was.
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
const routes = [
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
  en: { tags: ["Web search", "Web search", "", "Title"], story: /magpie ran this web search for Codex's deepseek\/deepseek-flash, which can't search the web by itself: codex\/gpt-6\.1-sol searched/, bare: /for a model that can't search the web by itself: codex\/gpt-6\.1-sol searched/, title: /Codex made this call itself \(Title\)/ },
  zh: { tags: ["联网搜索", "联网搜索", "", "标题"], story: /magpie 替 Codex 的 deepseek\/deepseek-flash 发起的联网搜索.*由 codex\/gpt-6\.1-sol 去搜/, bare: /替一个自己不能搜索网页的模型发起的联网搜索：由 codex\/gpt-6\.1-sol 去搜/, title: /这是 Codex 自己发起的调用（标题）/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a title and magpie's web search say what they were`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
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
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();

      const tags = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.querySelector(".asked .kind")?.textContent || ""));
      assert.deepEqual(tags, want[lang].tags);

      const story = async (i) => {
        const row = page.locator(".rt-req").nth(i);
        const was = await row.evaluate((e) => e.getBoundingClientRect().top);
        const scrolled = await page.evaluate(() => document.scrollingElement.scrollTop);
        await row.click();
        await page.waitForTimeout(400);
        assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the row");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scrolled, "picking the request scrolled the page");
        return page.locator(".rt-steps li.kind");
      };
      let s = await story(0);
      assert.equal(await s.locator(".kind").textContent(), want[lang].tags[0]);
      assert.match(await s.textContent(), want[lang].story);
      s = await story(1);
      assert.match(await s.textContent(), want[lang].bare);
      s = await story(3);
      assert.match(await s.textContent(), want[lang].title);
      s = await story(2);
      assert.equal(await s.count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
