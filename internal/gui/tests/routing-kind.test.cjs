// Run with Node's test runner and Playwright on the module path; see README.md.
// A call Codex makes for itself — its guardian review of an approval, a
// thread's title, memories — or a turn on Luna Reserve, carries a grey tag
// by the model in the Routing page's Requests list and in the request's
// story, so a list of Luna calls under a Sol composer reads as it is
// (碳碳双键: 为何 codex 桌面选的 5.6sol，但是请求记录…显示的全是 gpt-6-luna).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = { id: "codex", provider: "openai", name: "OpenAI", who: "Codex's own sign-in", kind: "account", agent: "codex", model: "gpt-6-luna" };
const kinds = ["guardian", "thread_title", "thread_title_reconsideration", "memory_consolidation", "luna_reserve", "ambient_suggestions", "ambient_suggestion_safety", "something_new", ""];
const routes = kinds.map((kind, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "gpt-6-luna", provider: "openai", ...(kind ? { kind } : {}),
  order: [seat], tries: [{ id: seat.id, model: "gpt-6-luna", start: at(i), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 1200,
}));

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: ["Approval check", "Title", "Title", "Memory", "Luna Reserve", "Suggestions", "Suggestions", "something_new"],
  zh: ["审批判定", "标题", "标题", "记忆", "Luna 储备", "提示词建议", "提示词建议", "something_new"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a call Codex makes for itself is tagged by its model`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 860 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-kind.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(kinds.length - 1).waitFor();

      const tags = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.querySelector(".asked .kind")?.textContent || ""));
      assert.deepEqual(tags, [...want[lang], ""]);
      // beside the model, not in its place, the model keeping its room first
      const look = await page.locator(".rt-req .asked .kind").first().evaluate((e) => {
        const m = e.parentElement.querySelector(".m");
        return { after: m.getBoundingClientRect().right <= e.getBoundingClientRect().left + 0.5, shown: m.clientWidth >= m.scrollWidth, model: m.textContent };
      });
      assert(look.after && look.shown && look.model === "gpt-6-luna", JSON.stringify(look));

      // the story says what it was, and the click leaves the row where it is
      const row = page.locator(".rt-req").nth(0);
      const was = await row.evaluate((e) => e.getBoundingClientRect().top);
      await row.click();
      await page.waitForTimeout(600);
      assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
      const story = page.locator(".rt-steps li.kind");
      assert.equal(await story.locator(".kind").textContent(), want[lang][0]);
      assert.match(await story.textContent(), lang === "zh" ? /不是对话中的一轮/ : /not as a turn of the conversation/);
      // Codex's home-page suggestions say where Codex turns them off (#705)
      await page.locator(".rt-req").nth(kinds.indexOf("ambient_suggestions")).click();
      await page.waitForTimeout(300);
      assert.match(await story.textContent(), lang === "zh" ? /设置 › 配置 › 提示词建议/ : /Settings › Configuration › Suggested prompts/);
      // a turn has no tag in its story
      await page.locator(".rt-req").nth(kinds.length - 1).click();
      await page.waitForTimeout(300);
      assert.equal(await page.locator(".rt-steps li.kind").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
