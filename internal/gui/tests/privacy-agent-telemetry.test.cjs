// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy turns off every agent's telemetry (OipsAnthony on X).
// The switch posts settings/agent-telemetry {off} and draws the settings the
// reply carries; an agent that couldn't follow is said in the status. While
// it is on, an agent's own Telemetry row shows off, says why, and can't be
// pressed; off again, the row is the agent's own. Every language, wide and
// narrow, no native select, no scroll on click. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang, refused = "") {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd" };
  const posts = [];
  // Gemini CLI's own row said on
  const state = () => ({
    agents: [{
      id: "gemini", name: "Gemini CLI", path: "/test/.gemini/settings.json", icon: "geminicli-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/glm-5", options: [{ value: "magpie/glm-5", label: "GLM-5", ref: "magpie/glm-5" }] }],
      telemetry: { off: !!settings.agentTelemetryOff, all: !!settings.agentTelemetryOff, keys: ["privacy.usageStatisticsEnabled"], file: "/test/.gemini/settings.json" },
    }],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/settings/agent-telemetry") {
      const body = req.postDataJSON();
      posts.push(body);
      settings.agentTelemetryOff = body.off || undefined;
      return json({ ...settings, ...(refused && body.off ? { refused } : {}) });
    }
    if (url.pathname === "/api/agents/telemetry/gemini") return json({ error: "held" }, 400);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  }
  return { serve, posts };
}

const W = {
  en: { all: "Turn off telemetry for all agents", why: "Off for all agents in Settings › Privacy", off: "Off", on: "On", details: "Details" },
  zh: { all: "关闭所有 Agent 的遥测", why: "已在 设置 › 隐私 中为所有 Agent 关闭", off: "关闭", on: "开启", details: "详情" },
  "zh-TW": { all: "關閉所有 Agent 的遙測", why: "已在 設定 › 隱私 中為所有 Agent 關閉", off: "關閉", on: "開啟", details: "詳情" },
  ja: { all: "すべてのエージェントのテレメトリをオフ", why: "設定 › プライバシー ですべてのエージェントでオフです", off: "オフ", on: "オン", details: "詳細" },
  de: { all: "Telemetrie aller Agents ausschalten", why: "Für alle Agents aus in Einstellungen › Datenschutz", off: "Aus", on: "An", details: "Details" },
};
const gm = '.row.agent[data-id="gemini"]';
const ROW = "#agentTelemetryAllRow";

async function open(engine, lang, width, refused, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, refused);
  await page.route("**/*", fx.serve);
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=privacy");
  await settingsDrawn(page);
  await page.locator(ROW).waitFor();
  return { page, fx, errors };
}

const pressed = (page, sel) => page.evaluate((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent, sel);
// scrolled to with the wheel as a reader does: a script doesn't scroll
// Settings in WebKit
async function reveal(page, sel) {
  const x = (page.viewportSize().width / 2) | 0;
  await page.mouse.move(x, 300);
  for (let i = 0; i < 60; i++) {
    const y = (await page.locator(sel).boundingBox())?.y ?? 9999;
    if (y > 60 && y < 600) break;
    await page.mouse.wheel(0, y > 600 ? 200 : -200);
    await page.waitForTimeout(60);
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: every agent's telemetry off from Privacy, over the agent's row`, async (t) => {
        const w = W[lang];
        const { page, fx, errors } = await open(engine, lang, width, "", t);
        assert.equal(await page.locator(`${ROW} .name`).textContent(), w.all);
        assert.equal(await pressed(page, ROW), w.off, "on before it was switched");
        await reveal(page, ROW);
        const box = await page.locator(ROW).boundingBox();
        assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));

        const y = await page.evaluate(() => document.querySelector("#view-settings").scrollTop + scrollY);
        await page.locator(`${ROW} .opt`, { hasText: w.on }).click();
        await page.waitForFunction(([s, on]) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === on, [ROW, w.on]);
        assert.deepEqual(fx.posts, [{ off: true }]);
        assert.equal(await page.evaluate(() => document.querySelector("#view-settings").scrollTop + scrollY), y, "a click scrolled the page");

        // the agent's own row: off, why, held
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(gm).waitFor();
        await page.locator(`${gm} .ag-link`, { hasText: w.details }).click();
        const row = `${gm} .ag-exp .ag-telemetry`;
        await page.locator(row).waitFor();
        await page.waitForFunction(([s, off]) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === off, [row, w.off]);
        assert.equal(await page.locator(`${row} .sub`).textContent(), w.why);
        assert.equal(await page.locator(`${row} button:not([disabled])`).count(), 0, "the agent's row can be pressed");

        // off again: the agent's own choice, pressable
        await page.evaluate(() => api("settings/agent-telemetry", { off: false }).then(() => api("state")).then((s) => { state = s; renderAgents(); }));
        await page.waitForFunction(([s, on]) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === on, [row, w.on]);
        assert.equal(await page.locator(`${row} .sub`).count(), 0);
        assert.equal(await page.locator(`${row} button[disabled]`).count(), 0);
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: an agent that couldn't follow is said, the switch on`, async (t) => {
    const refused = "Codex: /test/.codex/config.toml sets analytics.enabled to true; magpie leaves a value of yours as it is (change it there)";
    const { page, fx, errors } = await open(engine, "en", 1100, refused, t);
    await reveal(page, ROW);
    await page.locator(`${ROW} .opt`, { hasText: "On" }).click();
    await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, refused);
    await page.waitForFunction((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === "On", ROW);
    // and off again
    await page.locator(`${ROW} .opt`, { hasText: "Off" }).click();
    await page.waitForFunction((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === "Off", ROW);
    assert.deepEqual(fx.posts, [{ off: true }, { off: false }]);
    assert.deepEqual(errors, []);
  });
}
