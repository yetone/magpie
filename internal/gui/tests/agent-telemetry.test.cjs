// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's own telemetry off from its page (OipsAnthony on X): an agent
// whose state carries telemetry has a Telemetry row in its opened row's
// Settings and in its settings sheet, on until switched. Off posts
// {off: true} and the row follows the state the reply carries; a refusal
// (a value of the user's own) is said and the row goes back. A value of
// the user's own that keeps it off is said under the name. An agent with
// no switch has no row. Every language, wide and narrow, no native select,
// no scroll on click. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang, { own = [], refuse = "" } = {}) {
  const settings = { lang, theme: "light" };
  const posts = [];
  const tel = { off: own.length > 0, own, keys: ["privacy.usageStatisticsEnabled"], file: "/test/.gemini/settings.json" };
  const state = () => ({
    agents: [
      {
        id: "gemini", name: "Gemini CLI", path: "/test/.gemini/settings.json", icon: "geminicli-color", wired: true,
        fields: [{ key: "model", label: "model", value: "magpie/glm-5", options: [{ value: "magpie/glm-5", label: "GLM-5", ref: "magpie/glm-5" }] }],
        telemetry: { ...tel },
      },
      {
        id: "opencode", name: "OpenCode", path: "/test/.config/opencode/opencode.json", icon: "opencode", wired: true,
        fields: [{ key: "model", label: "model", value: "magpie/glm-5", options: [{ value: "magpie/glm-5", label: "GLM-5", ref: "magpie/glm-5" }] }],
      },
    ],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agents/telemetry/gemini") {
      const body = req.postDataJSON();
      posts.push(body);
      if (refuse) return json({ error: refuse }, 400);
      tel.off = body.off;
      return json(state());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  }
  return { serve, posts };
}

const W = {
  en: { name: "Telemetry", off: "Off", on: "On", ok: "Takes effect when Gemini CLI next starts", own: "Turned off in your own config too: privacy.usageStatisticsEnabled", details: "Details", settings: "Settings" },
  zh: { name: "遥测", off: "关闭", on: "开启", ok: "Gemini CLI 下次启动时生效", own: "你自己的配置里也关了：privacy.usageStatisticsEnabled", details: "详情", settings: "设置" },
  "zh-TW": { name: "遙測", off: "關閉", on: "開啟", ok: "Gemini CLI 下次啟動時生效", own: "你自己的設定裡也關了：privacy.usageStatisticsEnabled", details: "詳情", settings: "設定" },
  ja: { name: "テレメトリ", off: "オフ", on: "オン", ok: "Gemini CLI の次回起動時に反映されます", own: "ご自身の設定でもオフです：privacy.usageStatisticsEnabled", details: "詳細", settings: "設定" },
  de: { name: "Telemetrie", off: "Aus", on: "An", ok: "Wirkt beim nächsten Start von Gemini CLI", own: "Auch in deiner eigenen Konfiguration aus: privacy.usageStatisticsEnabled", details: "Details", settings: "Einstellungen" },
};
const gm = '.row.agent[data-id="gemini"]';
const oc = '.row.agent[data-id="opencode"]';

async function open(engine, lang, width, opts, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 700 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, opts);
  await page.route("**/*", fx.serve);
  t.after(() => browser.close());
  await page.goto("http://magpie.test/?view=agents");
  await page.locator(gm).waitFor();
  await page.locator(`${gm} .ag-link`, { hasText: W[lang].details }).click();
  await page.locator(`${gm} .ag-exp .ag-telemetry`).waitFor();
  return { page, fx, errors };
}

// until the faked backend has had n posts
async function posted(fx, n) {
  for (let i = 0; i < 500 && fx.posts.length < n; i++) await new Promise((r) => setTimeout(r, 10));
}
const pressed = (page, sel) => page.evaluate((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent, sel);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Gemini CLI's telemetry off and back`, async (t) => {
        const w = W[lang];
        const { page, fx, errors } = await open(engine, lang, width, {}, t);
        const row = `${gm} .ag-exp .ag-telemetry`;
        assert.equal(await page.locator(`${row} .name`).textContent(), w.name);
        assert.equal(await pressed(page, row), w.on, "off before it was switched");
        assert.equal(await page.locator(`${row} .sub`).count(), 0);
        const box = await page.locator(row).boundingBox();
        assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await page.locator(`${row} .opt`, { hasText: w.off }).click();
        await page.waitForFunction(([s, off]) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === off, [row, w.off]);
        // the reply's state drawn: the status says it
        await page.waitForFunction((ok) => document.querySelector("#status")?.textContent === ok, w.ok);
        assert.deepEqual(fx.posts, [{ off: true }]);
        assert.equal(await pressed(page, row), w.off);
        await page.locator(`${row} .opt`, { hasText: w.on }).click();
        await posted(fx, 2);
        await page.waitForFunction(([s, on]) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === on, [row, w.on]);
        assert.deepEqual(fx.posts, [{ off: true }, { off: false }]);

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        // the agent with no switch has none
        assert.equal(await page.locator(`${oc} .ag-telemetry`).count(), 0);
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: the user's own off is said`, async (t) => {
      const w = W[lang];
      const { page, errors } = await open(engine, lang, 1100, { own: ["privacy.usageStatisticsEnabled"] }, t);
      const row = `${gm} .ag-exp .ag-telemetry`;
      assert.equal(await pressed(page, row), w.off);
      assert.equal(await page.locator(`${row} .sub`).textContent(), w.own);
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: a refusal is said and the row goes back`, async (t) => {
    const refuse = "/test/.gemini/settings.json sets privacy.usageStatisticsEnabled to true; magpie leaves a value of yours as it is (change it there)";
    const { page, fx, errors } = await open(engine, "en", 1100, { refuse }, t);
    const row = `${gm} .ag-exp .ag-telemetry`;
    await page.locator(`${row} .opt`, { hasText: "Off" }).click();
    await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, refuse);
    await page.waitForFunction((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === "On", row);
    assert.deepEqual(fx.posts, [{ off: true }]);
    assert.deepEqual(errors, []);
  });
  test(`${engine}: the agent's menu opens a sheet with the row`, async (t) => {
    const { page, fx, errors } = await open(engine, "en", 1100, {}, t);
    await page.evaluate(() => openAgentPrefs(state.agents.find((a) => a.id === "gemini")));
    const row = "#modal .agent-prefs .ag-telemetry";
    await page.locator(row).waitFor();
    assert.equal(await page.locator(`#modal .agent-prefs .ehead b`).textContent(), "Gemini CLI settings");
    await page.locator(`${row} .opt`, { hasText: "Off" }).click();
    await posted(fx, 1);
    await page.waitForFunction((s) => document.querySelector(s)?.querySelector(".opt.on")?.textContent === "Off", row);
    assert.deepEqual(fx.posts, [{ off: true }]);
    assert.deepEqual(errors, []);
  });
}
