// Run with Node's test runner and Playwright on the module path; see README.md.
// A rate limit or quota stays on its row after the gateway's rest of it
// ends (@eachg0 on X: the vendor said "try again in 6 hours", the red
// line went within the hour, and a restart lost it too). A notice with
// the time the vendor named says when it resets; one with none says since
// when, till the account or key answers again (the gateway drops it then).
// It has no Try now: nothing rests. A notice past its time shows nothing,
// and a key or model resting shows its rest, not its notice. On a
// subscription account's row and on a key's row; it fits at 360px. In
// every language, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SAID = "Rate limit reached for requests. Please try again in 6 hours.";
const USER = "Max@Example.com", OTHER = "two@example.com", THIRD = "three@example.com";
const at = new Date(Date.now() - 2 * 3600e3).toISOString();
const until = new Date(Date.now() + 4 * 3600e3).toISOString();
const gone = new Date(Date.now() - 60e3).toISOString();

const account = () => ({
  id: "workbuddy-ai", name: "WorkBuddy AI", icon: "", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5.3", name: "GLM-5.3", on: true }], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: USER, plan: "",
    logins: [{ user: USER, active: true, on: true }, { user: OTHER, on: true }, { user: THIRD, on: true }],
  },
  accountRests: {
    "max@example.com": [{ why: "rate", status: 429, until, at, key: "workbuddy-ai@max@example.com", notice: true, error: SAID }],
    "two@example.com": [{ why: "quota", status: 429, at, key: "workbuddy-ai@two@example.com/glm-5.3", model: "glm-5.3", notice: true }],
    // the vendor's time gone by: nothing
    "three@example.com": [{ why: "rate", status: 429, until: gone, at, key: "workbuddy-ai@three@example.com", notice: true }],
  },
});
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "One", masked: "sk-…one", on: true, active: true, rest: { why: "rate", status: 429, at, key: "relay#aaaaaaaaaa", notice: true, error: SAID } },
    { id: "bbbbbbbbbb", name: "Two", masked: "sk-…two", on: true, rest: { why: "quota", status: 429, until, at, key: "relay#bbbbbbbbbb", notice: true } },
    { id: "cccccccccc", name: "Three", masked: "sk-…three", on: true, rest: { why: "rate", status: 429, until: gone, at, key: "relay#cccccccccc", notice: true } },
  ],
});

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const providers = { providers: [account(), relay()], presets: [], excluded: [], gateway: { running: true, window: true },
    plugins: [{ id: "workbuddy-ai", pid: "workbuddy", name: "WorkBuddy AI", icon: "", spec: "@magpie-community/opencode-workbuddy-auth" }] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
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
    test(`${engine} ${lang}: a rate limit stays on its row after its rest, till it resets or answers`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [900, 360]) {
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        const time = (iso) => page.evaluate((u) => restBack(u), iso);
        const rate = "429 " + (await tr("rate limited")), quota = "429 " + (await tr("quota used up"));
        const resets = await tr("The gateway asks it again; this shows until the limit resets");
        const answers = await tr("The gateway asks it again; this shows until a request on it is answered");
        const fits = async (row, line) => {
          const box = await row.boundingBox(), lb = await line.boundingBox();
          assert.ok(lb.x >= box.x && lb.x + lb.width <= box.x + box.width + 1, `the line fits its row at ${width}px`);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `no sideways scroll at ${width}px`);
        };

        // a subscription's accounts
        await page.locator(".row.provider", { hasText: "WorkBuddy AI" }).first().click();
        const acc = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        await acc(USER).waitFor();
        let line = acc(USER).locator(".key-rest-line");
        assert.equal(await line.count(), 1);
        assert.equal(await line.locator(".key-rest").textContent(), await tr("{why} · resets at {time}", { why: rate, time: await time(until) }));
        assert.equal(await line.locator(".key-rest").getAttribute("title"), resets);
        assert.equal(await line.locator(".key-rest-said").textContent(), SAID);
        assert.equal(await line.locator(".key-unrest").count(), 0, "a notice offers Try now");
        assert.ok(await line.locator(".key-rest").isVisible());
        await fits(acc(USER), line);
        line = acc(OTHER).locator(".key-rest-line");
        assert.equal(await line.locator(".key-rest").textContent(), "glm-5.3 " + (await tr("{why} since {time}", { why: quota, time: await time(at) })));
        assert.equal(await line.locator(".key-rest").getAttribute("title"), answers);
        assert.equal(await line.locator(".key-unrest").count(), 0);
        await fits(acc(OTHER), line);
        assert.equal(await acc(THIRD).locator(".key-rest-line").count(), 0, "a notice past its time is shown");

        // a provider's keys
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        const key = (id) => page.locator(`.acc[data-account-id="${id}"]`);
        await key("aaaaaaaaaa").locator(".key-rest").waitFor();
        line = key("aaaaaaaaaa").locator(".key-rest-line");
        assert.equal(await line.locator(".key-rest").textContent(), await tr("{why} since {time}", { why: rate, time: await time(at) }));
        assert.equal(await line.locator(".key-rest-said").textContent(), SAID);
        assert.equal(await line.locator(".key-unrest").count(), 0);
        await fits(key("aaaaaaaaaa"), line);
        line = key("bbbbbbbbbb").locator(".key-rest-line");
        assert.equal(await line.locator(".key-rest").textContent(), await tr("{why} · resets at {time}", { why: quota, time: await time(until) }));
        assert.equal(await line.locator(".key-unrest").count(), 0);
        assert.equal(await key("cccccccccc").locator(".key-rest-line").count(), 0, "a key's notice past its time is shown");
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
