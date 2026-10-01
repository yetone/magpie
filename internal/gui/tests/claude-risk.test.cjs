// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude's subscription (the user: 添加 Claude 订阅账号时要像其他 Subscription
// Provider 一样显示风险提示): starting its sign-in first says Anthropic may
// ban the account, as Antigravity's and Command Code's do, and nothing is
// asked of the backend until "Sign in anyway". The click moves nothing on the
// page. English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin") {
      asked.push(route.request().postDataJSON());
      return json({ id: "s1", agent: "claude", state: "waiting", url: "https://claude.ai/oauth/authorize" });
    }
    if (url.pathname.startsWith("/api/signin/")) return json({ id: "s1", agent: "claude", state: "waiting" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { title: "Claude accounts can be suspended", note: /^Anthropic may suspend or ban a Claude account it sees used outside its own apps\..*at your own risk\./, anyway: "Sign in anyway", cancel: "Cancel" },
  zh: { title: "Claude 账号可能被封禁", note: /^Anthropic 官方可能会封禁在其官方应用之外使用的 Claude 账号。.*风险由你自行承担。/, anyway: "仍然登录", cancel: "取消" },
};

// where everything that can scroll stands
const scrolls = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => e.scrollTop + "," + e.scrollLeft)].join("|"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Claude's sign-in warns of a ban first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        const row = sheet.locator('.tile[data-pick="Claude"]');
        await row.waitFor();

        // Cancel leaves it unasked
        await row.click();
        let box = sheet.locator(".signing");
        await box.waitFor();
        await box.locator("button", { hasText: w.cancel }).click();
        await box.waitFor({ state: "detached" });
        assert.equal(asked.length, 0);

        const before = await scrolls(page);
        await row.click();
        box = sheet.locator(".signing");
        await box.waitFor();
        assert.equal(await scrolls(page), before, "the click moved nothing");
        assert.equal(await box.locator(".n").textContent(), w.title);
        assert.match(await box.locator(".s").textContent(), w.note);
        assert.doesNotMatch(await box.textContent(), /Antigravity|Google|Command Code/);
        // a dot marks it, not a coloured stripe down its left side
        const edge = await box.evaluate((b) => { const s = getComputedStyle(b); return [s.borderLeftWidth, s.borderLeftColor, s.borderRightColor]; });
        assert.ok(edge[0] === "0px" || edge[1] === edge[2], "no left-border accent: " + edge);
        assert.equal(asked.length, 0, "nothing asked before it is said");

        await box.locator("button", { hasText: w.anyway }).click();
        for (let i = 0; i < 50 && !asked.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(asked, [{ agent: "claude" }]);
        assert.deepEqual(errors, []);
      });
    }
  });
}
