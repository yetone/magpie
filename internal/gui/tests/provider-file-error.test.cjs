// Run with Node's test runner and Playwright on the module path; see README.md.
// A providers.json that is there but can't be read (#415's review) is not
// an empty list: the Providers page says over the list that the file can't
// be read, was left unchanged, and is to be fixed or moved aside, with the
// reason magpie gives; the Add sheet doesn't open as for a first use, and
// the signed-in accounts still listed stay. A file that reads shows none of
// it. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const REASON = "/home/me/.config/magpie/providers.json can't be read (unexpected end of JSON input); magpie left it unchanged — fix it or move it aside";

function serve(lang, list, fileError) {
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true }, ...(fileError ? { fileError } : {}) });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const account = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "https://chatgpt.com/backend-api/codex", anthropic: "", catalog: "",
  host: "chatgpt.com", models: [{ id: "gpt-5", name: "GPT-5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "Plus", logins: [] },
};
const heading = { en: "Your providers file can't be read", zh: "无法读取供应商配置文件" };
const body = { en: "Fix the file or move it aside", zh: "请修复该文件或将其移走" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: an unreadable providers.json is said, not shown as no providers`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-file-error.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));

      // nothing listed: the error, and no Add sheet as for a first use
      await page.route("**/*", serve(lang, [], REASON));
      await page.goto("http://magpie.test/?view=providers");
      const box = page.locator("#fileError");
      await box.locator(".signing").waitFor();
      assert(await box.isVisible(), "the error shows");
      const text = await box.textContent();
      assert(text.includes(heading[lang]), text);
      assert(text.includes(body[lang]), text);
      assert(text.includes(REASON), "the reason is said");
      assert.equal(await box.locator('[role="alert"]').count(), 1);
      await page.waitForTimeout(200);
      assert(await page.locator("#addSheet").evaluate((s) => s.hidden), "the Add sheet opened as for a first use");

      // a signed-in account still listed stays, under the error
      await page.unroute("**/*");
      await page.route("**/*", serve(lang, [account], REASON));
      await page.reload();
      await page.locator('#providers .row.provider[data-id="codex"]').waitFor();
      assert(await box.isVisible(), "the error shows over the accounts");
      const errBottom = await box.evaluate((e) => e.getBoundingClientRect().bottom);
      const listTop = await page.locator("#providers").evaluate((e) => e.getBoundingClientRect().top);
      assert(errBottom <= listTop, "the error is over the list");

      // a file that reads: none of it
      await page.unroute("**/*");
      await page.route("**/*", serve(lang, [account], ""));
      await page.reload();
      await page.locator('#providers .row.provider[data-id="codex"]').waitFor();
      assert(await box.evaluate((b) => b.hidden), "the error shows for a file that reads");
      assert.deepEqual(errors, []);
    });
  }
}
