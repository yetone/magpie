// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Desktop on magpie from an app of its own (#5671): on a Mac, Claude
// Desktop's opened row has a "Separate app" line with a Claude Magpie
// switch. Off as before; a click posts the new choice, the switch follows
// the state the reply carries, and no click scrolls the page. Where the
// state has no isolated (Windows, Linux) the line isn't there. Every
// language, wide and narrow. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang, offered = true) {
  const settings = { lang, theme: "light" };
  const posts = [];
  const iso = { on: false };
  const state = () => ({
    agents: [{
      id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
      fields: [{ key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
      models: { shown: 2, listed: 2, by: [{ name: "OpenCode Go", icon: "opencode", n: 2 }], names: ["DeepSeek V4 Pro", "Exo Free"] },
      ...(offered ? { isolated: { ...iso } } : {}),
    }],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/settings/desktop-isolated") {
      const body = req.postDataJSON();
      posts.push(body);
      iso.on = body.on;
      if (body.on) iso.launcher = "/test/Applications/Claude Magpie.app";
      else delete iso.launcher;
      return json(state());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts };
}

const W = {
  en: { k: "Separate app", hint: "Your own Claude Desktop stays signed in with Anthropic. macOS only.", ok: "Open Claude Magpie from Applications", details: "Details" },
  zh: { k: "独立应用", hint: "你自己的 Claude Desktop 保持 Anthropic 登录。仅限 macOS。", ok: "从「应用程序」打开 Claude Magpie", details: "详情" },
  "zh-TW": { k: "獨立應用程式", hint: "你自己的 Claude Desktop 維持 Anthropic 登入。僅限 macOS。", ok: "從「應用程式」打開 Claude Magpie", details: "詳情" },
  ja: { k: "独立アプリ", hint: "ご自身の Claude Desktop は Anthropic にサインインしたままです。macOS のみ。", ok: "「アプリケーション」から Claude Magpie を開いてください", details: "詳細" },
  de: { k: "Eigene App", hint: "Dein eigenes Claude Desktop bleibt bei Anthropic angemeldet. Nur macOS.", ok: "Öffne Claude Magpie unter Programme", details: "Details" },
};
const cd = '.row.agent[data-id="claude-desktop"]';
const sw = `${cd} .ag-isolated .lib-switch`;

async function open(engine, lang, width, offered, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 700 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, offered);
  await page.route("**/*", fx.serve);
  t.after(() => browser.close());
  await page.goto("http://magpie.test/");
  await page.locator(cd).waitFor();
  await page.locator(`${cd} .ag-link`, { hasText: W[lang].details }).click();
  await page.locator(`${cd} .ag-exp`).waitFor();
  return { page, fx, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Claude Desktop's row turns Claude Magpie on and off`, async (t) => {
        const w = W[lang];
        const { page, fx, errors } = await open(engine, lang, width, true, t);
        const line = page.locator(`${cd} .ag-exp .ag-isolated`);
        await line.waitFor();
        const row = page.locator(`${cd} .ag-exp .ag-kv`, { has: page.locator(".ag-isolated") });
        assert.equal(await row.locator(".ag-k").textContent(), w.k);
        assert.equal(await line.locator(".ag-isolated-say").textContent(), "Claude Magpie");
        assert.equal(await line.locator(".ag-hint").textContent(), w.hint);
        assert.equal(await page.locator(sw).getAttribute("aria-checked"), "false", "on before it was switched");
        const box = await line.boundingBox();
        assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await page.locator(sw).click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "true", sw);
        assert.deepEqual(fx.posts, [{ on: true }]);
        assert.equal(await page.locator("#status").textContent(), w.ok);
        // and off again, by its words
        await page.locator(`${cd} .ag-isolated .ag-isolated-say`).click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "false", sw);
        assert.deepEqual(fx.posts, [{ on: true }, { on: false }]);

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: no Separate app line where Desktop can't run so`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, false, t);
    assert.equal(await page.locator(`${cd} .ag-isolated`).count(), 0);
    assert.equal(await page.locator(`${cd} .ag-exp .ag-k`, { hasText: W.en.k }).count(), 0);
    assert.deepEqual(errors, []);
  });
}
