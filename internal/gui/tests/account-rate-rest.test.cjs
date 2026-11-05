// Run with Node's test runner and Playwright on the module path; see README.md.
// A model the vendor rate-limits until a time it names says "rate limited",
// not "quota used up" (taoxiaoxin on Discord: WorkBuddy answered
// deepseek-v4.1-flash 429 "您的使用量已超出频率限制，将在 2026-11-06 17:13:04
// (UTC+8) 重置，您也可以切换其他模型继续使用。" with credits left, and
// Routing called the account's quota used up). The gateway marks such a rest
// Rate; the Providers row, Routing's stage row and its Accounts line all say
// rate limited, with the time it is back. In every language, at a wide and a
// narrow window, in Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SAID = "WorkBuddy: 您的使用量已超出频率限制，将在 2026-11-06 17:13:04 (UTC+8) 重置，您也可以切换其他模型继续使用。";
const USER = "me@x.test", MODEL = "deepseek-v4.1-flash";
const now = new Date();
const until = new Date(now.getTime() + 5 * 3600e3).toISOString();
const rest = { why: "quota", status: 429, rate: true, until, by: "resets", key: `workbuddy@${USER}/${MODEL}`, model: MODEL, error: SAID };

const provider = {
  id: "workbuddy", name: "WorkBuddy", icon: "", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: MODEL, name: "DeepSeek-V4.1-Flash", on: true }, { id: "glm-5.1", name: "GLM-5.1", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: { agent: "workbuddy", agentName: "WorkBuddy", user: USER, plan: "", logins: [{ user: USER, active: true, on: true }] },
  accountRests: { [USER]: [rest] },
};

function routes() {
  const seat = (model, extra = {}) => ({ id: "workbuddy@" + USER, provider: "workbuddy", name: "WorkBuddy", kind: "account", icon: "", who: USER, model, known: true, ...extra });
  const order = [seat(MODEL, { rest }), seat("glm-5.1")];
  const time = new Date(now.getTime() - 60e3).toISOString();
  const tries = [
    { id: order[0].id, model: MODEL, start: time, done: true, status: 429, ms: 300, fail: "quota", rest, error: SAID },
    { id: order[1].id, model: "glm-5.1", start: time, done: true, status: 200, ms: 900 },
  ];
  return [{ id: 1, seq: 1, time, agent: "codex", model: "group/g", provider: "workbuddy", order, tries, done: true, status: 200, ms: 2000 }];
}

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true },
      plugins: [{ id: "workbuddy", pid: "workbuddy", name: "WorkBuddy", icon: "", spec: "@magpie-community/opencode-workbuddy-auth" }] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return;
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 1, errors: 0 }, routes: routes() });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a model rate-limited until a named time says rate limited, not quota used up`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [1100, 420]) {
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));

        // Providers: the account's row
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "WorkBuddy" }).first().click();
        const acc = page.locator(`.editor .accts .acc[data-account-id="${USER}"]`);
        await acc.waitFor();
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        const rate = await tr("rate limited"), quota = await tr("quota used up");
        assert.notEqual(rate, quota);
        const back = await page.evaluate((u) => restBack(u), until);
        const line = acc.locator(".key-rest-line .key-rest");
        assert.equal(await line.textContent(), await tr("{model} resting · {why} · back at {time}", { model: MODEL, why: "429 " + rate, time: back }), `${width}px providers`);
        assert(!(await acc.textContent()).includes(quota), `${width}px: the account's row says ${quota}`);
        assert(!(await sideways(page)), `${width}px: Providers scrolls sideways`);

        // Routing: the stage row and the Accounts line
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-act").first().waitFor();
        const row = page.locator("li", { has: page.locator("code.mdl", { hasText: new RegExp(`^${MODEL.replace(/\./g, "\\.")}$`) }) }).filter({ has: page.locator(".who", { hasText: USER }) });
        await row.first().evaluate((e) => new Promise((done) => { const ok = () => e.classList.contains("rest") ? done() : requestAnimationFrame(ok); setTimeout(done, 5000); ok(); }));
        assert(await row.first().evaluate((e) => e.classList.contains("rest")), `${width}px: the stage row doesn't rest`);
        const said = (await row.first().locator("em").textContent()).trim();
        assert(said.includes(rate), `${width}px: the stage row says ${said}`);
        const part = page.locator(".rt-act", { hasText: USER }).locator(".st.rest.part");
        assert.equal(await part.count(), 1);
        const text = await part.textContent();
        assert(text.startsWith(MODEL + " · ") && text.includes(rate), `${width}px: the Accounts line says ${text}`);
        // the account's tally counts the refusal as the vendor called it
        assert((await page.locator(".rt-act", { hasText: USER }).locator(".tally .bad").textContent()).includes("1 " + rate), `${width}px: the tally doesn't count it as ${rate}`);
        assert(!(await page.locator("body").textContent()).includes(quota), `${width}px: Routing says ${quota} `);
        assert(!(await sideways(page)), `${width}px: Routing scrolls sideways`);
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
