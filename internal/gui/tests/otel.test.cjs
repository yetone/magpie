// OTLP preferences: metadata export stays off until enabled, credentials are
// masked, and later preference saves retain the endpoint and headers.
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
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// POST replaces saved preferences, as the Settings handler does.
function server(lang, posts) {
  const fixed = settingsPayload({ lang, otelEnv: true });
  let cur = fixed;
  const answer = () => cur;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...fixed, ...body };
      }
      return json(answer());
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": OTLP settings", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const posts = [], errors = [];
        const context = await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=settings&tab=otel");
        await page.locator("#otelExportRow").waitFor();
        const off = lang === "zh" ? "关闭" : "Off";
        assert.equal(await page.locator("#otelExportRow .opt.on").textContent(), off);
        assert.equal(await page.locator("#otelMetricsRow .opt.on").textContent(), off);
        assert.equal(await page.locator("#otelBodiesRow .opt.on").textContent(), off);
        assert.equal(await page.locator("#otelWholeRow").count(), 0, "whole bodies require bodies on");
        assert((await page.locator("#otelBodiesRow").textContent()).includes(lang === "zh" ? "包含请求和响应内容" : "Include request and response bodies"));
        assert.equal(await page.locator("#otelHeadersRow input").getAttribute("type"), "password");
        assert((await page.locator("#otelList").textContent()).includes(lang === "zh" ? "环境变量" : "Environment variables"));
        const scroll = () => page.locator("#view-settings").evaluate((e) => e.scrollTop);
        await page.locator("#view-settings").hover();
        for (let i = 0; i < 80; i++) {
          const box = await page.locator("#otelBodiesRow").boundingBox();
          if (box && box.y > 100 && box.y + box.height < 750) break;
          await page.mouse.wheel(0, 150);
          await page.waitForTimeout(30);
        }
        const before = await scroll();
        // Wait for the response-driven redraw after each save.
        const saved = async (action, n) => {
          const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
          await action(); await response; await page.waitForTimeout(200);
          assert.equal(posts.length, n); return posts[n - 1];
        };
        let p = await saved(async () => {
          const field = page.locator("#otelEndpointRow input");
          await field.fill("https://collector.test/api/public/otel/"); await field.press("Enter");
        }, 1);
        assert.equal(p.otel.endpoint, "https://collector.test/api/public/otel");
        p = await saved(async () => {
          const field = page.locator("#otelHeadersRow input");
          await field.fill("Authorization=Basic%20YWJjZA==,X-Tag=a%2Cb%2Bc"); await field.press("Enter");
        }, 2);
        assert.deepEqual(p.otel.headers, { Authorization: "Basic YWJjZA==", "X-Tag": "a,b+c" });
        p = await saved(() => page.locator("#otelExportRow .opt").nth(1).click(), 3);
        assert.equal(p.otel.enabled, true);
        p = await saved(() => page.locator("#otelMetricsRow .opt").nth(1).click(), 4);
        assert.equal(p.otel.metrics, true);
        p = await saved(() => page.locator("#otelBodiesRow .opt").nth(1).click(), 5);
        assert.equal(p.otel.bodies, true);
        assert.equal(await page.locator("#otelWholeRow .opt.on").textContent(), off);
        assert.equal(p.otel.metrics, true);
        assert.equal(await scroll(), before, "saving OTLP settings must not scroll");
        for (let i = 0; i < 40; i++) {
          const box = await page.locator("#otelWholeRow").boundingBox();
          if (box && box.y > 100 && box.y + box.height < 750) break;
          await page.mouse.wheel(0, 100); await page.waitForTimeout(30);
        }
        const beforeWhole = await scroll();
        p = await saved(() => page.locator("#otelWholeRow .opt").nth(1).click(), 6);
        assert.equal(p.otel.bodiesWhole, true);
        assert.equal(p.otel.bodies, true);
        assert.equal(await scroll(), beforeWhole, "saving the whole-bodies switch must not scroll");
        await page.locator("#setTab-usage").click();
        for (let i = 0; i < 60; i++) {
          const box = await page.locator("#currencySegs").boundingBox();
          if (box && box.y > 100 && box.y < 750) break;
          await page.mouse.wheel(0, -150); await page.waitForTimeout(30);
        }
        p = await saved(() => page.locator("#currencySegs .opt").nth(1).click(), 7);
        assert.equal(p.otel.enabled, true);
        assert.equal(p.otel.bodies, true);
        assert.equal(p.otel.bodiesWhole, true);
        assert.equal(p.otel.endpoint, "https://collector.test/api/public/otel");
        assert.equal(p.otel.headers.Authorization, "Basic YWJjZA==");
        await page.locator("#setTab-otel").click();
        for (let i = 0; i < 60; i++) {
          const box = await page.locator("#otelExportRow").boundingBox();
          if (box && box.y > 100 && box.y < 650) break;
          await page.mouse.wheel(0, 150); await page.waitForTimeout(30);
        }
        p = await saved(() => page.locator("#otelExportRow .opt").first().click(), 8);
        assert.equal(p.otel.enabled, false);
        await page.reload();
        await page.locator("#otelExportRow .opt.on").waitFor();
        assert.equal(await page.locator("#otelExportRow .opt.on").textContent(), off);
        assert.equal(await page.locator("#otelWholeRow .opt.on").textContent(), lang === "zh" ? "开启" : "On");
        assert.equal(await page.locator("#otelEndpointRow input").inputValue(), p.otel.endpoint);
        p = await saved(() => page.locator("#otelBodiesRow .opt").first().click(), 9);
        assert.equal(p.otel.bodies, false);
        assert.equal(p.otel.bodiesWhole, true, "hiding the row preserves the preference");
        assert.equal(await page.locator("#otelWholeRow").count(), 0);
        p = await saved(() => page.locator("#otelBodiesRow .opt").nth(1).click(), 10);
        assert.equal(p.otel.bodies, true);
        assert.equal(await page.locator("#otelWholeRow .opt.on").textContent(), lang === "zh" ? "开启" : "On");
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
