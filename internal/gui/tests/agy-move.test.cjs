// Run with Node's test runner and Playwright on the module path; see README.md.
// The withdrawn Antigravity plugin's accounts can move to Antigravity (local
// agy CLI), which signs in with the same Google client (@tongzhenbao on X).
// A quiet line over the Providers list names them, with Move: pressed, it
// is busy until the move is through, an account agy refused is said by name
// in the line, and the rest are said moved. Not now hides it until the
// accounts left to move change, a reload too. While it is drawn it stands
// in for the withdrawn plugin's own line (the same accounts, the same way
// out); once it goes, that line is back. In every language; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the withdrawn Antigravity plugin, kept: /api/providers' withdrawn
const withdrawn = [{ id: "antigravity", name: "Antigravity", instead: "Antigravity (local agy CLI)", package: "@magpie-community/opencode-agy-cli-auth" }];

function serve(lang, posts, hold) {
  let left = ["a@x.com", "b@x.com"];
  let tries = 0;
  const payload = (moved) => ({
    providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], withdrawn,
    ...(left.length ? { agyMove: left } : {}), ...(moved ? { agyMoved: moved } : {}),
  });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/toagy") {
      posts.push(url.pathname);
      // the first move is held until the test has read the busy button
      if (!tries) await new Promise((r) => (hold.release = r));
      // the first: a moves, agy refuses b; the second: b moves
      if (!tries++) {
        left = ["b@x.com"];
        return json(payload([{ user: "a@x.com", status: "added" }, { user: "b@x.com", status: "failed", error: "invalid_grant" }]));
      }
      left = [];
      return json(payload([{ user: "b@x.com", status: "added" }]));
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { line: "Antigravity's accounts can move to Antigravity (local agy CLI)", users: "a@x.com, b@x.com move as they are", move: "Move", busy: "Installing agy and checking each account…", failed: "b@x.com: invalid_grant", again: "Try again", done: "1 moved to Antigravity (local agy CLI)" },
  zh: { line: "Antigravity 的账号可以迁移到 Antigravity（本机 agy CLI）", users: "a@x.com、b@x.com 可以原样迁移", move: "迁移", busy: "正在安装 agy 并逐个检查账号…", failed: "b@x.com：invalid_grant", again: "重试", done: "已有 1 个账号迁移到 Antigravity（本机 agy CLI）" },
  "zh-TW": { line: "Antigravity 的帳號可以遷移到 Antigravity（本機 agy CLI）", users: "a@x.com、b@x.com 可以原樣遷移", move: "遷移", busy: "正在安裝 agy 並逐一檢查帳號…", failed: "b@x.com：invalid_grant", again: "重試", done: "已有 1 個帳號遷移到 Antigravity（本機 agy CLI）" },
  ja: { line: "Antigravity のアカウントを Antigravity（ローカル agy CLI）に移行できます", users: "a@x.com、b@x.com はそのまま移行できます", move: "移行", busy: "agy をインストールし、各アカウントを確認しています…", failed: "b@x.com：invalid_grant", again: "再試行", done: "1 件を Antigravity（ローカル agy CLI）に移行しました" },
  de: { line: "Die Konten von Antigravity können zu Antigravity (lokale agy CLI) umziehen", users: "ziehen a@x.com, b@x.com unverändert um", move: "Umziehen", busy: "agy wird installiert und jedes Konto geprüft…", failed: "b@x.com: invalid_grant", again: "Erneut versuchen", done: "1 zu Antigravity (lokale agy CLI) umgezogen" },
};

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) {
    test(`${engine} ${lang}: the withdrawn Antigravity plugin's accounts move to agy-cli from the line over the list`, async (t) => {
      const w = L[lang];
      const browser = await launch(engine);
      t.after(() => browser.close());
      const ctx = await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], hold = {};
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, hold));
      await page.goto("http://magpie.test/?view=providers");

      const line = page.locator("#movable .agy-move");
      const go = line.locator("button.action");
      await go.waitFor();
      const text = await line.innerText();
      assert.ok(text.includes(w.line), `no line: ${text}`);
      assert.ok(text.includes(w.users), `doesn't name the accounts: ${text}`);
      assert.equal(await go.innerText(), w.move);
      assert.equal(await page.locator("#movable .withdrawn").count(), 0, "two lines for the same accounts");
      // the line fits a narrow window: nothing runs off its side
      const box = await line.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= 420, `the line runs off: ${JSON.stringify(box)}`);
      if (process.env.ARTIFACT_DIR) await line.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `agy-move-${engine}-${lang}.png`) });

      await go.click();
      await page.waitForFunction((busy) => document.querySelector("#movable .agy-move button.action")?.textContent === busy, w.busy);
      assert.equal(await go.isDisabled(), true, "Move can be pressed again mid-move");
      for (let i = 0; !hold.release && i < 250; i++) await page.waitForTimeout(20);
      assert.ok(hold.release, "the move was never asked");
      hold.release();
      // a moved, agy refused b: b said by name in the line, a said moved
      await page.waitForFunction((want) => document.querySelector("#movable .agy-move")?.innerText.includes(want), w.failed);
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.done);
      assert.equal(await go.innerText(), w.again);
      assert.equal(await go.isDisabled(), false);
      await page.evaluate(() => renderProviders());
      assert.ok((await line.innerText()).includes(w.failed), "a redraw forgot why b didn't move");

      // tried again, b moves: the line goes
      await go.click();
      await page.waitForFunction(() => !document.querySelector("#movable .agy-move"));
      await page.locator("#movable .withdrawn").waitFor();
      assert.deepEqual(posts, ["/api/provider/toagy", "/api/provider/toagy"]);
      assert.deepEqual(errors, []);
    });
  }
}

// Not now hides the line, a reload too, until the accounts left change.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Not now hides the Antigravity move until its accounts change`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let users = ["a@x.com"];
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], withdrawn, agyMove: users } });
      return serve("en", [], {})(route);
    });
    await page.goto("http://magpie.test/?view=providers");
    const line = page.locator("#movable .agy-move");
    await line.waitFor();
    await line.locator("button", { hasText: "Not now" }).click();
    assert.equal(await line.count(), 0, "Not now left the line");
    await page.locator("#movable .withdrawn").waitFor();
    await page.reload();
    await page.waitForFunction(() => typeof providers === "object" && providers?.agyMove);
    await page.waitForTimeout(200);
    assert.equal(await line.count(), 0, "a reload brought the hidden line back");
    users = ["a@x.com", "c@x.com"];
    await page.reload();
    await line.waitFor();
    assert.deepEqual(errors, []);
  });
}
