// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Network and sharing › Model ids without the provider (love1sbug
// on X: apps moved from CLIProxyAPI ask for gpt-5.6-sol, and magpie's
// /v1/models only gave codex/gpt-5.6-sol): off by default; On posts
// settings/bare-ids on its own and lights On, Only (qQianye on X: a Docker
// magpie's client listed every model twice, and a gateway has no Agents
// page) posts it with only, Off posts it back, and no click scrolls the
// Settings page. The row is drawn in gateway mode too,
// which is where such apps are. Every language, Chromium and WebKit, at a
// narrow and a wide window; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, wheelTo } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lan: false, lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// gateway: magpie as a gateway alone (magpie web with no agents here), as
// gateway-mode.test.cjs fakes it
function server(lang, gateway, posted) {
  let cur = settingsPayload({ lang, ...(gateway ? { web: true, gatewayMode: "on", gatewayOn: true } : {}) });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${gateway}${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/bare-ids") {
      const body = req.postDataJSON();
      posted.push(["bare-ids", body]);
      cur = { ...cur, bareModelIDs: body.on, bareModelIDsOnly: body.on && body.only };
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
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { name: "Model ids without the provider", off: "Off", on: "On", only: "Only", sub: "gpt-5.6-sol beside codex/gpt-5.6-sol" },
  zh: { name: "不带供应商的模型 ID", off: "关闭", on: "开启", only: "仅此", sub: "codex/gpt-5.6-sol 旁的 gpt-5.6-sol" },
  "zh-TW": { name: "不帶供應商的模型 ID", off: "關閉", on: "開啟", only: "僅此", sub: "codex/gpt-5.6-sol 旁的 gpt-5.6-sol" },
  ja: { name: "プロバイダなしのモデル ID", off: "オフ", on: "オン", only: "のみ", sub: "codex/gpt-5.6-sol の隣に gpt-5.6-sol" },
  de: { name: "Modell-IDs ohne Anbieter", off: "Aus", on: "An", only: "Nur", sub: "gpt-5.6-sol neben codex/gpt-5.6-sol" },
};
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 900]) {
    for (const lang of Object.keys(want)) {
      test(`${engine} ${width} ${lang}: the gateway lists bare model ids, or not`, async (t) => {
        const w = want[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 420 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        // gateway mode on the narrow one: the row is the gateway's, not an
        // agent file's, so it stays where agent-file rows are hidden
        const gateway = width === 420;
        await page.route("**/*", server(lang, gateway, posted));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-bare-ids.png`) });
          }
          await browser.close();
        });
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=network");
        await settingsDrawn(page);
        const segs = page.locator("#bareIdsSegs .opt");
        await segs.first().waitFor();
        const row = page.locator("#bareIdsRow");
        assert.equal(await page.locator("#alertList").evaluate((r) => r.hidden), gateway, "gateway mode as faked");
        assert(await row.isVisible(), "the row is drawn");
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        assert((await row.locator(".sub").textContent()).includes(w.sub), "the sub says what it does");
        assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), [w.off, w.on, w.only]);
        assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "off by default");
        assert.equal(await page.locator("#bareIdsRow select").count(), 0, "no native select");
        const border = await row.evaluate((r) => getComputedStyle(r).borderLeftWidth);
        assert(border === "0px" || border === "", "no left border accent: " + border);
        // nothing wider than the window at the narrow width
        assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");
        const rb = await row.boundingBox(), sb = await page.locator("#bareIdsSegs").boundingBox();
        assert(sb.x + sb.width <= rb.x + rb.width + 1, "the switch runs out of its row");

        // scrolled by a wheel till the row is mid-view, On moves nothing and
        // is posted on its own
        // (each wheel's scroll lands before the next: a fixed pause read
        // the row's place from before it under load, wheeled past the row,
        // under the header, and the click was on a control out of sight,
        // which the page brought back into view as it took focus, #2370)
        await wheelTo(page, segs.nth(1), 200);
        const before = await view(page);
        assert(before > 0, "the settings list must scroll to the row");
        await segs.nth(1).click();
        await page.locator("#bareIdsSegs .opt.on", { hasText: w.on }).waitFor();
        await page.waitForTimeout(400);
        assert.equal(await view(page), before, "the click scrolled the page");
        assert.deepEqual(posted, [["bare-ids", { on: true, only: false }]]);

        // bare ids alone: Only lights, and alone
        await segs.nth(2).click();
        await page.locator("#bareIdsSegs .opt.on", { hasText: w.only }).waitFor();
        assert.equal(await page.locator("#bareIdsSegs .opt.on").count(), 1);
        assert.deepEqual(posted.at(-1), ["bare-ids", { on: true, only: true }]);
        assert.equal(await view(page), before, "the click scrolled the page");

        // back off
        await segs.nth(0).click();
        await page.locator("#bareIdsSegs .opt.on", { hasText: w.off }).waitFor();
        assert.deepEqual(posted.at(-1), ["bare-ids", { on: false, only: false }]);
        assert.equal(posted.length, 3);
        assert.equal(await view(page), before, "the click scrolled the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}
