// Run with Node's test runner and Playwright on the module path; see README.md.
// A built-in subscription a community plugin can run (Zed here). The
// providers list says so in a quiet line whose link opens its editor; the
// editor says which runs it, and "Move to the plugin" says what it is doing
// on the button. A move that fails says why right under it, in the
// reader's language, and the button becomes "Try again"; one that goes
// through turns the field over in the editor, still open where it was, and
// "Use the built-in again" posts provider/moveback. The add sheet no longer
// offers the built-in Zed once it runs on its plugin, and its editor shows
// no plugin:// endpoints to test. The Plugins tab, opened as the page
// (?view=plugins), shows its market and offers Zed's accounts on the
// plugin's card. In English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const pkg = "@magpie-community/opencode-zed-auth";

function serve(lang, posts) {
  let move = { package: pkg, state: "" };
  let tries = 0;
  const payload = () => {
    const onPlugin = move.state === "plugin";
    const zed = {
      id: "zed", name: "Zed", icon: "zed", chat: onPlugin ? "plugin://zed/v1" : "", responses: "", anthropic: onPlugin ? "plugin://zed" : "", catalog: "",
      // enough models that the editor scrolls to reach Runs on
      models: Array.from({ length: 30 }, (_, i) => ({ id: "m" + i, name: "Model " + i, on: i === 0 })),
      agents: [], fallback: [], headers: {}, keyList: [], key: {},
      account: { agent: "zed", agentName: "Zed", user: "ada", logins: [{ user: "ada", active: true, on: true, own: true }, { user: "bob", on: true }] },
      move,
    };
    return {
      providers: [zed], presets: [], excluded: [], gateway: { running: true, window: true },
      plugins: onPlugin ? [{ id: "zed", pid: "zed", name: "Zed", icon: "zed", spec: pkg, signedIn: true, models: 1, methods: [] }] : [],
      onPlugins: onPlugin ? ["zed"] : [],
    };
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins/market") {
      return json({ listings: [{ package: pkg, name: "Zed", icon: "zed", providers: ["zed"], community: true, replaces: "zed", summary: { en: "Zed", zh: "Zed" }, npm: { version: "0.1.0" } }],
        state: { bun: true, bunVersion: "1.3.0", plugins: [], movable: move.state === "plugin" ? [] : [{ id: "zed", name: "Zed", package: pkg, accounts: 2 }] } });
    }
    if (url.pathname === "/api/provider/move" || url.pathname === "/api/provider/moveback") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      await new Promise((r) => setTimeout(r, 400)); // long enough to read the button
      // the first move can't reach npm; the second goes through
      if (url.pathname.endsWith("/move") && !tries++) {
        move = { package: pkg, state: "failed", error: "magpie couldn't reach npm to install the plugin.", why: { code: "offline" } };
        return route.fulfill({ status: 400, json: { error: move.error, why: move.why } });
      }
      move = { package: pkg, state: url.pathname.endsWith("moveback") ? "back" : "plugin" };
      return json(payload());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { runs: "Runs on", line: "Zed can run on community plugins, with the same accounts.", look: "Take a look",
    move: "Move to the plugin", busy: "Installing the plugin and checking each account…", again: "Try again", back: "Use the built-in again",
    failed: "It stays built-in: magpie couldn't reach npm to install the plugin. Check the network or proxy, then try again.",
    onPlugin: "The community Zed plugin", builtin: "magpie's built-in · or the community Zed plugin", done: "Zed now runs on its plugin — 2 accounts, 1 model.",
    subs: "Subscriptions", card: "Move my Zed accounts (2)", own: "Zed itself stays signed in as it is." },
  zh: { runs: "运行方式", line: "Zed 可以改由社区插件运行，账号不变。", look: "看看",
    move: "迁移到插件", busy: "正在安装插件并逐个检查账号…", again: "重试", back: "改回内置",
    failed: "仍使用内置：无法连接 npm 安装插件，请检查网络或代理后重试。",
    onPlugin: "社区 Zed 插件", builtin: "magpie 内置 · 也可改用社区 Zed 插件", done: "Zed 现在由插件运行——2 个账号，1 个模型。",
    subs: "订阅", card: "迁移我的 Zed 账号（2）", own: "Zed 本身的登录保持不变。" },
};

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a built-in subscription moves to its plugin and back`, async (t) => {
      const w = L[lang];
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");

      // the quiet line over the list opens Zed's editor
      const line = page.locator("#movable");
      await line.locator("button", { hasText: w.look }).waitFor();
      assert.ok((await line.innerText()).includes(w.line), "no line saying Zed can move");
      await line.locator("button", { hasText: w.look }).click();

      const ed = page.locator("#modal .editor");
      const btn = ed.locator(".move button");
      await btn.waitFor();
      assert.equal(await btn.innerText(), w.move);
      const text = await ed.innerText();
      assert.ok(text.includes(w.runs), "no Runs on field");
      assert.ok(text.includes(w.builtin), "doesn't say it's built-in");
      assert.equal(await ed.locator(".move").getAttribute("title"), null);
      assert.equal(await ed.locator(".move > div").first().getAttribute("title"), pkg, "the package isn't in the tooltip");
      assert.ok(!text.includes(pkg), "names the npm package in the sentence");
      assert.ok(text.includes(w.own), "doesn't say Zed stays signed in");

      // the reader scrolled to the field: nothing the move does moves it
      const body = ed.locator(".ebody");
      const top = await body.evaluate((e) => { e.scrollTop = 120; return e.scrollTop; });
      assert.ok(top > 0, "the editor doesn't scroll: the test proves nothing about keeping its place");

      await btn.click();
      await page.waitForFunction((busy) => [...document.querySelectorAll("#modal .move button")].some((b) => b.textContent === busy), w.busy, { timeout: 300 });
      assert.equal(await btn.isDisabled(), true, "the button can be pressed again mid-move");

      // it fails: said under the button, the editor open where it was
      const why = ed.locator(".move-why");
      await why.waitFor({ state: "visible" });
      assert.equal(await why.innerText(), w.failed);
      assert.equal(await btn.innerText(), w.again);
      assert.equal(await btn.isDisabled(), false);
      assert.equal(await body.evaluate((e) => e.scrollTop), top, "the failure moved the editor");
      if (process.env.ARTIFACT_DIR) await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-move-failed-${engine}-${lang}.png`) });

      // tried again, it goes through: the same editor, turned over
      await btn.click();
      const back = ed.locator(".move button", { hasText: w.back });
      await back.waitFor();
      assert.equal(await page.locator("#modal .editor").count(), 1, "the editor closed");
      assert.ok((await ed.innerText()).includes(w.onPlugin), "doesn't say the plugin runs it");
      // as the built-in said: the agent's own sign-in is where it was
      assert.ok((await ed.innerText()).includes(w.own), "the moved Zed no longer says Zed stays signed in");
      assert.equal(await why.count() && await why.isVisible(), false, "the failure is still said");
      assert.equal(await body.evaluate((e) => e.scrollTop), top, "the move moved the editor");
      assert.ok((await page.locator("#status").innerText()).startsWith(w.done), "no toast saying what moved");
      assert.deepEqual(posts.map((p) => [p.path, p.body.id]), [["/api/provider/move", "zed"], ["/api/provider/move", "zed"]]);
      // its plugin:// URLs are magpie's own: no Endpoints, no Test
      assert.equal(await ed.locator(".eps").count(), 0, "shows the plugin:// endpoints");
      assert.equal(await line.isVisible(), false, "the line still offers the move");
      if (process.env.ARTIFACT_DIR) await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-moved-${engine}-${lang}.png`) });
      await page.keyboard.press("Escape");

      // the add sheet offers Zed once, as the plugin's
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator(".kind b", { hasText: w.subs }).waitFor();
      assert.equal(await sheet.locator('.tile[data-pick="Zed"]').count(), 1);
      await page.keyboard.press("Escape");

      await page.locator(".row.provider", { hasText: "Zed" }).click();
      await back.click();
      for (let i = 0; i < 50 && posts.length < 3; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/move", "/api/provider/move", "/api/provider/moveback"]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the Plugins tab opened as the page offers Zed's accounts`, async (t) => {
      const w = L[lang];
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=plugins");
      const card = page.locator("#view-plugins button", { hasText: w.card });
      await card.waitFor();
      await card.click();
      // the first move fails (npm unreachable): said, and the card offers it again
      await page.waitForFunction((s) => document.querySelector("#status").textContent.startsWith(s), w.failed.split(":")[0] || w.failed.split("：")[0]);
      await card.waitFor();
      await card.click();
      await page.waitForFunction((s) => document.querySelector("#status").textContent.startsWith(s), w.done.split(" — ")[0].split("——")[0]);
      assert.deepEqual(posts.map((p) => [p.path, p.body.id]), [["/api/provider/move", "zed"], ["/api/provider/move", "zed"]]);
      assert.deepEqual(errors, []);
    });
  }
}
