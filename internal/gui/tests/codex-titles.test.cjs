// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' "Codex thread titles" (#705, DuGuaiYA: Codex's thread_title
// requests go through its own ChatGPT sign-in even on magpie's models): by
// default Codex's own (ChatGPT); the app's menu (no native select) offers
// Off and magpie's models; a pick posts settings/codex-titles on its own,
// the row says what now happens, and no click scrolls the Settings page.
// English and Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    titleModels: [
      { id: "deepseek/deepseek-v4-flash", name: "DeepSeek V4 Flash", provider: "deepseek", providerName: "DeepSeek", icon: "deepseek" },
      { id: "group/cheap", name: "cheap", provider: "", providerName: "Routing groups" },
    ],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posted) {
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/codex-titles") {
      const body = req.postDataJSON();
      posted.push(["codex-titles", body]);
      cur = { ...cur, codexTitles: body.model };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posted.push(["settings", req.postDataJSON()]);
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const want = {
  en: { name: "Codex thread titles", own: "Codex’s own (ChatGPT)", off: "Off", subOwn: /through its ChatGPT sign-in/, subOff: /sent nowhere/, subModel: /go to this model/ },
  zh: { name: "Codex 会话标题", own: "Codex 自带（ChatGPT）", off: "关闭", subOwn: /ChatGPT 登录/, subOff: /不发往任何地方/, subModel: /改由这个模型/ },
};
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Codex's title requests go where Settings says`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posted = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posted));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-titles.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=settings");
      const row = page.locator("#codexTitlesRow");
      const pick = page.locator("#codexTitlesPick button");
      await pick.waitFor();
      assert.equal(await page.locator("#codexTitlesRow select").count(), 0, "no native select");
      assert.equal((await row.locator(".name").textContent()).trim(), want[lang].name);
      assert.equal((await pick.textContent()).trim(), want[lang].own, "Codex's own by default");
      assert.match(await row.locator(".sub").textContent(), want[lang].subOwn);

      // scrolled by a wheel till the row is mid-view, so a click that
      // moved the page would show
      const box = await page.locator("#view-settings").boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      const top = () => pick.evaluate((e) => e.getBoundingClientRect().top);
      for (let i = 0; i < 40 && (await top()) > 200; i++) { await page.mouse.wheel(0, 100); await page.waitForTimeout(30); }
      const before = await view(page);
      assert(before > 0, "the settings list must scroll to the row");

      // the app's menu: Codex's own, Off and magpie's models
      await pick.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const items = (await page.locator("#list li:not(.group)").allTextContents()).join("|");
      for (const s of [want[lang].own, want[lang].off, "DeepSeek V4 Flash", "cheap"]) assert(items.includes(s), `${s} in ${items}`);
      await page.locator("#list li:not(.group)").filter({ hasText: want[lang].off }).first().click();
      await page.locator("#codexTitlesPick button", { hasText: want[lang].off }).waitFor();
      assert.deepEqual(posted, [["codex-titles", { model: "off" }]]);
      assert.match(await row.locator(".sub").textContent(), want[lang].subOff);
      await page.waitForTimeout(300);
      assert.equal(await view(page), before, "picking Off scrolled the page");

      // a model of magpie's
      await pick.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li:not(.group)").filter({ hasText: "DeepSeek V4 Flash" }).first().click();
      await page.locator("#codexTitlesPick button", { hasText: "DeepSeek V4 Flash" }).waitFor();
      assert.deepEqual(posted.at(-1), ["codex-titles", { model: "deepseek/deepseek-v4-flash" }]);
      assert.match(await row.locator(".sub").textContent(), want[lang].subModel);

      // and back to Codex's own
      await pick.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li:not(.group)").filter({ hasText: want[lang].own }).first().click();
      await page.locator("#codexTitlesPick button", { hasText: want[lang].own }).waitFor();
      assert.deepEqual(posted.at(-1), ["codex-titles", { model: "" }]);
      await page.waitForTimeout(300);
      assert.equal(await view(page), before, "the picks scrolled the page");
      assert(!posted.some(([k]) => k === "settings"), "set on its own, not with the other preferences");
      assert.deepEqual(errors, []);
    });
  }
}
