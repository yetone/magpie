// Run with Node's test runner and Playwright on the module path; see README.md.
// liukaybee on X: the Claude import sheet said "…认证文件（JSON）或 Claude
// Code's .credentials.json or claude-swap's export。", its {own} passed in
// English. Both lines are in the reader's language in every language, and
// the sheet says a setup-token can be pasted too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the Claude import sheet's words are the reader's own`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const page = await browser.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof importSay === "function");
      const say = await page.evaluate(() => { const s = importSay("claude"); return { from: s.from, intro: s.intro }; });
      if (lang === "en") {
        assert.match(say.from, /Claude Code's \.credentials\.json or claude-swap's export/);
        assert.match(say.intro, /setup-token/);
      } else {
        for (const s of [say.from, say.intro]) assert.doesNotMatch(s, /Claude Code's|claude-swap's|'s export/, `${lang}: ${s}`);
        assert.match(say.intro, /claude setup-token/, lang);
      }
      assert.deepEqual(errors, [], lang);
      await page.close();
    }
  });
}
