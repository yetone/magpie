// Run with Node's test runner and Playwright on the module path; see README.md.
// #1797 (miniers): a conversation that never moves to another account.
// "Strict" beside Stays in a provider's editor (made with its Save) and on
// the Routing page's row of providers with several accounts (at once),
// with a hint that says the error then goes to the agent. Every option
// fits the editor at a narrow width, in every language; no native select,
// and a click never scrolls the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const sub = {
  icon: "generic", catalog: "", models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, routing: "order", affinity: "", ready: true,
  id: "codex", name: "Codex", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "one@example.test", plan: "Plus",
    logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Plus", active: i === 0, on: true })),
  },
};
const groups = () => ({
  models: [{ id: "codex/gpt-5.5", name: "gpt-5.5", providerName: "Codex", icon: "generic" }],
  pools: [{ provider: "codex", name: "Codex", icon: "generic", kind: "account", who: ["one@example.test", "two@example.test"], routing: "order", affinity: "" }],
  groups: [],
});

function serve(lang, posts) {
  const state = { agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [sub], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json(providers);
    if (r.request().method() === "POST" && url.pathname.startsWith("/api/provider")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const HINT = "A conversation stays with the account or key that answered it and never moves to another, even past its usage cap: when that one is rate limited or out of quota, the error goes to the agent. New conversations are routed as usual.";
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
// what the hint says, in each language: the error goes to the agent
const SAYS = { en: /the error goes to the agent/, zh: /错误直接交给智能体/, "zh-TW": /錯誤直接交給智能體/, ja: /エラーはそのままエージェントに渡ります/, de: /geht der Fehler an den Agenten/ };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
  for (const lang of LANGS) {
    const tr = (page, s) => page.evaluate(([k, lang]) => lang === "en" ? k : I18N[lang][k], [s, lang]);

    for (const width of [900, 560]) {
      test(`${engine} ${lang} ${width}px: a provider's conversations stay strictly, made with its Save`, async (t) => {
        const browser = await launch();
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: sub.name }).first().click();
        await page.locator(".editor .accts").first().waitFor();
        if (lang !== "en") assert.deepEqual(await page.evaluate(([keys, lang]) => keys.filter((k) => !I18N[lang][k]), [["Strict", HINT], lang]), [], "translated");
        const strict = await tr(page, "Strict"), stays = await tr(page, "Stays");
        const label = page.locator(".editor label", { hasText: new RegExp(`^${stays}$`) });
        const field = label.locator("xpath=following-sibling::div[1]");
        const opt = field.locator(".segs .opt", { hasText: new RegExp(`^${strict}$`) });
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        assert.equal(await field.locator(".segs .opt").count(), 5);
        // every option fits the editor, none past its edge
        const ed = await page.locator(".editor").first().boundingBox();
        for (const b of await field.locator(".segs .opt").evaluateAll((os) => os.map((o) => { const r = o.getBoundingClientRect(); return { x: r.x, r: r.right }; }))) {
          assert.ok(b.x >= ed.x - 1 && b.r <= ed.x + ed.width + 1, `an option past the editor's edge: ${JSON.stringify(b)} in ${JSON.stringify(ed)}`);
        }
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        await opt.scrollIntoViewIfNeeded();
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await opt.click();
        assert.match(await opt.getAttribute("class"), /\bon\b/);
        assert.match(await field.locator(".hint").last().textContent(), SAYS[lang]);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
        assert.deepEqual(posts, [], "nothing is sent before the Save");
        await page.getByRole("button", { name: await tr(page, "Save"), exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
        assert.deepEqual(posts.map((x) => x.path), ["/api/provider/save"]);
        assert.equal(posts[0].body.affinity, "strict");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: the Routing page sets a provider's conversations to stay strictly`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1000 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const pool = page.locator(".rt-pool", { hasText: "Codex" });
      await pool.waitFor();
      const strict = pool.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "Strict")}$`) });
      await page.mouse.move(550, 500);
      for (let i = 0; i < 10 && (await strict.boundingBox()).y > 1000 - 200; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const y = await page.evaluate(() => document.scrollingElement.scrollTop);
      await strict.click();
      await page.waitForTimeout(200);
      assert.deepEqual(posts.find((p) => p.path === "/api/provider/affinity")?.body, { id: "codex", affinity: "strict" });
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
      assert.equal(await page.locator("select").count(), 0, "no native select");
      assert.deepEqual(errors, []);
    });
  }
}
