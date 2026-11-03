// Run with Node's test runner and Playwright on the module path; see README.md.
// A subscription account the gateway rests says so on its row on the
// Providers page, as a key does (maxleebeta on Discord: WorkBuddy AI
// answered 429 "您的使用量已超出频率限制，将在 2026-11-03 23:57:19 UTC+8
// 重置，您也可以切换其他模型继续使用。" with 82% of its credits left, and
// the page said nothing). A rest of one model names the model and when it
// is back; the vendor's words show beside it, so a refusal whose time magpie
// couldn't read is still explained; Try now lifts it by the gateway's key.
// A rest gone by, and an account not resting, show nothing. It fits at
// 360px. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SAID = "WorkBuddy: 您的使用量已超出频率限制，将在 2026-11-03 23:57:19 UTC+8 重置，您也可以切换其他模型继续使用。";
const USER = "Max@Example.com", OTHER = "two@example.com", THIRD = "three@example.com";
const fresh = (until) => ({
  id: "workbuddy-ai", name: "WorkBuddy AI", icon: "", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6-astra", name: "GPT-6-Astra", on: true }, { id: "deepseek-v4.1-flash-sg", name: "Deepseek-V4.1-Flash", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: USER, plan: "",
    logins: [{ user: USER, active: true, on: true }, { user: OTHER, on: true }, { user: THIRD, on: true }],
  },
  accountRests: {
    "max@example.com": [{ why: "quota", status: 429, until, key: "workbuddy-ai@max@example.com/gpt-6-astra", by: "resets", model: "gpt-6-astra", error: SAID }],
    // the whole account, gone by: nothing
    "three@example.com": [{ why: "rate", status: 429, until: new Date(Date.now() - 60000).toISOString(), key: "workbuddy-ai@three@example.com", by: "cooldown" }],
  },
});

function serve(lang, until, lifted) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const providers = { providers: [fresh(until)], presets: [], excluded: [], gateway: { running: true, window: true },
    plugins: [{ id: "workbuddy-ai", pid: "workbuddy", name: "WorkBuddy AI", icon: "", spec: "@magpie-community/opencode-workbuddy-auth" }] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/gateway/unrest") { lifted.push(JSON.parse(route.request().postData() || "{}")); return json({}); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    test(`${engine} ${lang}: an account's model the gateway rests says so on its row, with the vendor's words and Try now`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [900, 360]) {
        const until = new Date(Date.now() + 3 * 3600e3).toISOString(), lifted = [];
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, until, lifted));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "WorkBuddy AI" }).first().click();
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        await row(USER).waitFor();
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        const back = await page.evaluate((u) => restBack(u), until);
        const why = "429 " + (await tr("quota used up"));

        const line = row(USER).locator(".key-rest-line");
        assert.equal(await line.count(), 1);
        assert.equal(await line.locator(".key-rest").textContent(), await tr("{model} resting · {why} · back at {time}", { model: "gpt-6-astra", why, time: back }));
        assert.equal(await line.locator(".key-rest").getAttribute("title"), await tr("Until then {model} goes to another account; its other models are used as before", { model: "gpt-6-astra" }));
        assert.equal(await line.locator(".key-rest-said").textContent(), SAID);
        assert.ok(await line.locator(".key-rest").isVisible());
        assert.ok(await line.locator(".key-unrest").isVisible());
        // an account not resting, and a rest gone by: nothing
        assert.equal(await row(OTHER).locator(".key-rest-line").count(), 0);
        assert.equal(await row(THIRD).locator(".key-rest-line").count(), 0);
        // the row's own line is untouched, and nothing scrolls sideways
        const box = await row(USER).boundingBox(), lb = await line.boundingBox();
        assert.ok(lb.x >= box.x && lb.x + lb.width <= box.x + box.width + 1, `the rest line fits its row at ${width}px`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `no sideways scroll at ${width}px`);

        // Try now lifts it by the gateway's key
        await line.locator(".key-unrest").click();
        await page.waitForFunction(() => !document.querySelector(".editor .key-rest-line"));
        assert.deepEqual(lifted, [{ key: "workbuddy-ai@max@example.com/gpt-6-astra" }]);
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
