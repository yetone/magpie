// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Magpie as a twin (xMopf7RF on X, #6459): Claude Desktop's row,
// not on magpie, has a "Use Claude Magpie" button beside its switch on a
// Mac, not in its status line. It turns the
// Claude Magpie app on and then connects Desktop, which puts only that
// app on magpie; the user's own Desktop isn't wired. The button is gone
// once Desktop is on magpie, and isn't there off a Mac (no isolated in the
// state). With Claude Magpie made already but Desktop not on magpie (the
// reporter of 0.1.4076: "没看到按钮"), or the app deleted since, the button
// is still there, and the status line says the switch puts Claude Magpie
// on magpie, not the user's own Desktop with a separate profile.
// No click scrolls the page. Every language, wide and narrow, both
// engines. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang, offered = true, made = {}) {
  const settings = { lang, theme: "light" };
  const posts = [];
  const iso = { on: false, ...made };
  let wired = false;
  const state = () => ({
    agents: [{
      id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired,
      fields: [{ key: "provider", label: "provider", value: wired ? "magpie" : "", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
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
      posts.push(["isolated", req.postDataJSON()]);
      iso.on = req.postDataJSON().on;
      iso.launcher = "/test/Applications/Claude Magpie.app";
      return json(state());
    }
    if (url.pathname === "/api/agents/connect/claude-desktop") {
      posts.push(["connect", iso.on]);
      wired = true;
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
  en: { st: "Not connected · connecting puts Claude Magpie on magpie; your own Claude Desktop stays signed in with Anthropic", b: "Use Claude Magpie", tip: "A second Claude Desktop, on magpie, in Applications. Your own Claude Desktop isn't changed. macOS only.", ok: "Open Claude Magpie from Applications" },
  zh: { st: "未接入 · 接入的是 Claude Magpie，你自己的 Claude Desktop 保持 Anthropic 登录", b: "启用 Claude Magpie", tip: "在「应用程序」里加一个接入 magpie 的 Claude Desktop 分身，你自己的 Claude Desktop 保持不变。仅限 macOS。", ok: "从「应用程序」打开 Claude Magpie" },
  "zh-TW": { st: "未接入 · 接入的是 Claude Magpie，你自己的 Claude Desktop 維持 Anthropic 登入", b: "啟用 Claude Magpie", tip: "在「應用程式」裡加一個接入 magpie 的 Claude Desktop 分身，你自己的 Claude Desktop 保持不變。僅限 macOS。", ok: "從「應用程式」打開 Claude Magpie" },
  ja: { st: "未接続 · 接続するのは Claude Magpie です。ご自身の Claude Desktop は Anthropic にサインインしたままです", b: "Claude Magpie を使う", tip: "magpie につながる 2 つ目の Claude Desktop を「アプリケーション」に追加します。ご自身の Claude Desktop は変わりません。macOS のみ。", ok: "「アプリケーション」から Claude Magpie を開いてください" },
  de: { st: "Nicht verbunden · verbunden wird Claude Magpie; dein eigenes Claude Desktop bleibt bei Anthropic angemeldet", b: "Claude Magpie nutzen", tip: "Ein zweites Claude Desktop auf magpie unter Programme. Dein eigenes Claude Desktop bleibt, wie es ist. Nur macOS.", ok: "Öffne Claude Magpie unter Programme" },
};
const cd = '.row.agent[data-id="claude-desktop"]';
const twin = `${cd} .ag-twin`;

async function open(engine, lang, width, offered, t, made) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 700 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, offered, made);
  await page.route("**/*", fx.serve);
  t.after(() => browser.close());
  await page.goto("http://magpie.test/");
  await page.locator(cd).waitFor({ state: "attached" });
  // not on magpie, its row is among the folded ones
  if (await page.locator(`.agent-fold ${cd}`).count()) {
    await page.locator(".agent-more").click();
    await page.waitForFunction(() => !document.querySelector(".agent-fold-inner")?.inert);
  }
  return { page, fx, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Use Claude Magpie makes the app and puts it alone on magpie`, async (t) => {
        const w = W[lang];
        const { page, fx, errors } = await open(engine, lang, width, true, t);
        const b = page.locator(twin);
        await b.waitFor();
        assert.equal(await b.textContent(), w.b);
        assert.equal(await b.getAttribute("title"), w.tip);
        const box = await b.boundingBox();
        assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));
        // its own button beside the row's switch, not words in the status
        // line (xMopf7RF on X: "在 Claude Desktop 旁边单独加一个按钮")
        assert.equal(await page.locator(`${cd} .ag-st .ag-twin`).count(), 0, "in the status line");
        // the fold's rows still moving in measure nothing
        await page.waitForFunction(() => !document.getAnimations().some((x) => x.playState === "running"));
        const sw = await page.locator(`${cd} > .ag-conn`).boundingBox(), at = await b.boundingBox();
        assert.ok(Math.abs(sw.y + sw.height / 2 - (at.y + at.height / 2)) < 2, `not level with the switch: ${JSON.stringify([sw, at])}`);
        const gap = sw.x - (at.x + at.width);
        assert.ok(gap >= 8 && gap <= 28, `not beside the switch: ${gap}`);
        const st = await page.locator(`${cd} .ag-st`).boundingBox();
        assert.ok(st.x + st.width <= at.x + 1, "status runs under the button");

        const y = await page.evaluate(() => scrollY);
        await b.click();
        await page.locator(`${cd} .ag-conn.on`).waitFor();
        // the app on first, so connecting wires its folder, not the user's
        assert.deepEqual(fx.posts, [["isolated", { on: true }], ["connect", true]]);
        assert.equal(await page.locator("#status").textContent(), w.ok);
        assert.equal(await page.locator(twin).count(), 0, "still offered once on");
        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
  }
  // Claude Magpie made, Desktop not on magpie: with the app there, and
  // with it deleted since (no launcher)
  for (const made of [{ on: true, launcher: "/test/Applications/Claude Magpie.app" }, { on: true }]) {
    for (const lang of Object.keys(W)) {
      for (const width of [1100, 640]) {
        test(`${engine} ${lang} ${width}px: Claude Magpie made${made.launcher ? "" : ", app gone"}, not on magpie: the button and a line naming it`, async (t) => {
          const w = W[lang];
          const { page, fx, errors } = await open(engine, lang, width, true, t, made);
          const b = page.locator(twin);
          await b.waitFor();
          assert.equal(await b.textContent(), w.b);
          const box = await b.boundingBox();
          assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));
          assert.equal((await page.locator(`${cd} .ag-st-t`).textContent()).trim(), w.st);
          const y = await page.evaluate(() => scrollY);
          await b.click();
          await page.locator(`${cd} .ag-conn.on`).waitFor();
          assert.deepEqual(fx.posts, [["isolated", { on: true }], ["connect", true]]);
          assert.equal(await page.locator(twin).count(), 0, "still offered once on");
          assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
  test(`${engine}: no Use Claude Magpie where Desktop can't run so`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, false, t);
    await page.locator(`${cd} .ag-st`).waitFor();
    assert.equal(await page.locator(twin).count(), 0);
    assert.deepEqual(errors, []);
  });
}
