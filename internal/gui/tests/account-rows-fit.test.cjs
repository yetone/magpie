// Run with Node's test runner and Playwright on the module path; see README.md.
// An account row holds its readings when they take two lines (the owner,
// with a screenshot of Claude Code's 账号: "有时候这个页面的排版会崩掉",
// then "每次点击手动刷新额度的小按钮之后"). The list was a grid, and
// WebKit kept a row at the height it had before its 5-hour and 7-day
// meters wrapped, so the second meter ran under the next account's name.
// Each row is checked as the editor opens and again after a row's refresh.
// Chromium and WebKit, every language, a wide and a narrow window; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const users = ["ferronatoousseni@example.com", "sales@example.ai", "shiro@example.com", "worktrial@example.ai", "yetoneful@example.ai"];
const at = (ms) => new Date(Date.now() + ms).toISOString();
const quotas = users.map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [
  { name: "5 hours", used: [100, 0, 100, 0, 0][i], resetsAt: at(36e5 * (i + 1)) },
  { name: "7 days", used: [89, 97, 50, 96, 98][i], resetsAt: at(864e5 * (i + 1)) },
] }));

function serve(lang, reads) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/providers") return json({ providers: [{
      id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
      account: { agent: "claude", agentName: "Claude Code", user: users[0], plan: "max",
        logins: users.map((user, i) => ({ user, plan: "max", active: i === 0, on: true })) },
    }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/login/usage") { reads.n++; return json(Object.fromEntries(quotas.map((q) => [q.user, q]))); }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// how far each row's last line reaches past the row, and whether its
// readings took more than one line
const rows = (page) => page.evaluate(() => [...document.querySelectorAll(".editor .acc.with-aq")].map((r) => {
  const aq = r.querySelector(".aq"), w = [...aq.querySelectorAll(".aq-w")];
  return {
    user: r.querySelector(".n")?.textContent || "",
    past: Math.max(...[...r.children].map((c) => c.getBoundingClientRect().bottom)) - r.getBoundingClientRect().bottom,
    wraps: w.length > 1 && w.at(-1).offsetTop > w[0].offsetTop,
  };
}));
const frames = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

// where the readings wrap depends on the language and the width; every
// language wraps at one of them at least, so each is tested holding two lines
const wrapped = new Set();
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 720]) {
      test(`${engine} ${lang} ${width}px: an account row holds its two lines of readings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const reads = { n: 0 };
        await page.route("**/*", serve(lang, reads));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Claude" }).first().click();
        await page.waitForFunction(() => document.querySelectorAll(".editor .acc.with-aq .aq-w").length === 10);
        await frames(page);
        const check = async (when) => {
          const rs = await rows(page);
          assert.equal(rs.length, users.length);
          if (rs.some((r) => r.wraps)) wrapped.add(`${engine} ${lang} ${width} ${when}`);
          for (const r of rs) assert(r.past <= 0.5, `${when}: ${r.user}'s readings reach ${r.past.toFixed(1)}px past its row: ${JSON.stringify(rs)}`);
        };
        await check("opened");
        for (const i of [1, 3]) {
          const n = reads.n;
          await page.locator(".editor .acc.with-aq").nth(i).locator(".quota-refresh").click();
          for (let k = 0; k < 500 && reads.n === n; k++) await page.waitForTimeout(10);
          assert(reads.n > n, "the refresh read the usage again");
          await page.waitForFunction(() => !document.querySelector(".editor .quota-refresh.busy"));
          await frames(page);
          await check(`after row ${i}'s refresh`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
test("the readings wrapped where a row is tested holding two lines", () => {
  for (const engine of engines) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    assert([...wrapped].some((w) => w.startsWith(`${engine} ${lang} `)), `${engine} ${lang} never wrapped: ${[...wrapped].join("; ")}`);
  }
});
