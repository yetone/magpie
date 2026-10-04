// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group opened from its row folds back from the editor's heading
// (ARNO on Discord: 路由组详情现在可以单击展开，是否可以再加上单击收起):
// a click, or Enter on it, closes the editor while nothing was changed;
// with a change it stays open and says to Save or Cancel, so no edit is
// lost. Nothing is saved by folding. English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"], canFast: true },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", canFast: true },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/glm-5"];
const saved = { firstToken: 0 };
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, firstToken: saved.firstToken,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { fold: "Click to fold it", first: "Save or Cancel your changes first", cancel: "Cancel" },
  zh: { fold: "点击收起", first: "有未保存的修改，请先保存或取消", cancel: "取消" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
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
      saved.firstToken = body.firstToken || 0;
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: an open group folds back from its heading`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      const ed = page.locator(".rt-gedit"), head = ed.locator(".ehead");
      const closed = () => page.waitForFunction(() => !document.querySelector(".rt-gedit") && document.querySelector(".rt-group"));

      // opened from the row, folded from the heading
      await card.locator(".main").click();
      await head.waitFor();
      assert.equal(await head.getAttribute("title"), w.fold);
      assert.equal(await head.getAttribute("aria-expanded"), "true");
      await head.click();
      await closed();

      // and from the keyboard
      await card.locator(".main").click();
      await head.focus();
      await page.keyboard.press("Enter");
      await closed();

      // a change keeps it open, and says so; Cancel still closes it
      await card.locator(".main").click();
      await ed.locator("input").first().fill("Sol 2");
      await head.click();
      await page.locator("#status").filter({ hasText: w.first }).waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), "Sol 2", "the edit is kept");
      await ed.locator("button", { hasText: new RegExp("^" + w.cancel + "$") }).click();
      await closed();

      assert.deepEqual(posts, [], "folding saves nothing");
      assert.deepEqual(errors, []);
    });
  }
}
