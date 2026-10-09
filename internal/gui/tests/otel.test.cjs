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
        const modes = lang === "zh" ? ["仅元数据", "最多 256 KB", "完整内容"] : ["Metadata only", "Up to 256 KB", "Full content"];
        for (const id of ["otelExportRow", "otelMetricsRow", "otelSessionsRow"]) {
          assert.equal(await page.locator(`#${id} .opt.on`).textContent(), off);
        }
        const consent = await page.locator("#otelSessionsRow").textContent();
        for (const phrase of (lang === "zh" ? ["全部本地会话", "未经 Magpie", "文件内容", "命令输出", "遮蔽敏感信息"] : ["all local sessions", "not routed through Magpie", "file contents", "command output", "secrets masked"])) assert(consent.includes(phrase), phrase);
        const mode = page.locator("#otelBodiesRow .opt.on");
        assert.equal(await mode.textContent(), modes[0]);
        assert.equal(await mode.getAttribute("aria-pressed"), "true");
        assert.equal(await page.locator("#otelHeadersRow input").getAttribute("type"), "password");
        assert((await page.locator("#otelList").textContent()).includes(lang === "zh" ? "环境变量" : "Environment variables"));
        // the groups are headed as every other Settings tab heads them: a
        // small label over a card of its own, not a grey band inside one card
        assert.deepEqual(await page.locator("#otelList > .row-head .label").allTextContents(),
          lang === "zh" ? ["采集服务连接", "导出范围", "链路内容"] : ["Collector connection", "Export scope", "Trace content"]);
        const heads = await page.evaluate(() => {
          const look = (e) => { const c = getComputedStyle(e); return [c.fontSize, c.fontWeight, c.color, c.textTransform, c.letterSpacing, c.backgroundColor].join(" "); };
          return {
            otel: look(document.querySelector("#otelList > .row-head .label")),
            network: look(document.querySelector("#setPage-network .row-head .label")),
            carded: [...document.querySelectorAll("#otelList > .row-head")].every((h) => h.nextElementSibling?.matches(".list.prefs")),
            inside: document.querySelectorAll("#otelList .list .row-head, #otelList .otel-section").length,
          };
        });
        assert.equal(heads.otel, heads.network, "Observability's headings look like Network's");
        assert(heads.carded, "each heading has its own card under it");
        assert.equal(heads.inside, 0, "no heading inside a card");
        const saved = async (action) => {
          const n = posts.length + 1;
          const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
          await action(); await response; await page.waitForTimeout(200);
          assert.equal(posts.length, n); return posts[n - 1];
        };
        let p = await saved(async () => {
          const field = page.locator("#otelEndpointRow input");
          await field.fill("https://collector.test/api/public/otel/"); await field.press("Enter");
        });
        assert.equal(p.otel.endpoint, "https://collector.test/api/public/otel");
        p = await saved(async () => {
          const field = page.locator("#otelHeadersRow input");
          await field.fill("Authorization=Basic%20YWJjZA==,X-Tag=a%2Cb%2Bc"); await field.press("Enter");
        });
        assert.deepEqual(p.otel.headers, { Authorization: "Basic YWJjZA==", "X-Tag": "a,b+c" });
        p = await saved(() => page.locator("#otelExportRow .opt").nth(1).click());
        assert.equal(p.otel.enabled, true);
        p = await saved(() => page.locator("#otelMetricsRow .opt").nth(1).click());
        assert.equal(p.otel.metrics, true);
        await page.locator("#otelBodiesRow").scrollIntoViewIfNeeded();
        const scroll = () => page.locator("#view-settings").evaluate((e) => e.scrollTop);
        const before = await scroll();
        p = await saved(() => page.locator("#otelBodiesRow .opt").nth(1).click());
        assert.equal(p.otel.bodies, true);
        assert.equal(p.otel.bodiesWhole, false);
        assert.equal(await mode.textContent(), modes[1]);
        assert.equal(p.otel.metrics, true);
        assert.equal(await scroll(), before, "saving content settings must not scroll");
        p = await saved(() => page.locator("#otelBodiesRow .opt").nth(2).click());
        assert.equal(p.otel.bodiesWhole, true);
        assert.equal(p.otel.bodies, true);
        assert.equal(await mode.textContent(), modes[2]);
        await page.locator("#setTab-usage").click();
        p = await saved(() => page.locator("#currencySegs .opt").nth(1).click());
        assert.equal(p.otel.enabled, true);
        assert.equal(p.otel.bodiesWhole, true);
        assert.equal(p.otel.endpoint, "https://collector.test/api/public/otel");
        assert.equal(p.otel.headers.Authorization, "Basic YWJjZA==");
        await page.locator("#setTab-otel").click();
        p = await saved(() => page.locator("#otelExportRow .opt").first().click());
        assert.equal(p.otel.enabled, false);
        await page.reload();
        await page.locator("#otelExportRow .opt.on").waitFor();
        assert.equal(await mode.textContent(), modes[2], "existing full-content settings survive reload");
        p = await saved(() => page.locator("#otelBodiesRow .opt").first().click());
        assert.equal(p.otel.bodies, false);
        assert.equal(p.otel.bodiesWhole, false);
        assert.equal(await mode.textContent(), modes[0]);
        p = await saved(() => page.locator("#otelSessionsRow .opt").nth(1).click());
        assert.equal(p.otel.sessions, true);
        await page.reload();
        await page.locator("#otelSessionsRow .opt.on").waitFor();
        p = await saved(() => page.locator("#otelBodiesRow .opt").nth(1).click());
        assert.equal(p.otel.sessions, true, "content choices preserve session tracing");
        // Native windows and narrow panels must show the entire consent text,
        // keep the controls inside their rows, and give URLs a full-width field.
        for (const width of [1100, 520, 360]) {
          await page.setViewportSize({ width, height: 1000 });
          const layout = await page.locator("#otelList").evaluate((list) => {
            const sub = list.querySelector("#otelSessionsRow .sub");
            const row = list.querySelector("#otelEndpointRow");
            const field = row.querySelector("input");
            return {
              wraps: getComputedStyle(sub).whiteSpace === "normal",
              clipped: sub.scrollHeight > sub.clientHeight + 1,
              fieldWidth: field.getBoundingClientRect().width,
              rowWidth: row.getBoundingClientRect().width,
              overflow: [...list.querySelectorAll(".row, .val, .segs")].some((e) => e.scrollWidth > e.clientWidth + 1),
            };
          });
          assert(layout.wraps && !layout.clipped, `consent visible at ${width}px`);
          assert(layout.fieldWidth > layout.rowWidth - 40, `wide endpoint at ${width}px`);
          assert.equal(layout.overflow, false, `no overflowing controls at ${width}px`);
        }
        if (process.env.OTEL_SCREENSHOTS) {
          await page.setViewportSize({ width: 1100, height: 1000 });
          await page.locator("#otelList").screenshot({ path: `/tmp/magpie-otel-${engine}-${lang}.png` });
        }
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
