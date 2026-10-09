// Run with Node's test runner and Playwright on the module path; see README.md.
// miaopasi on Discord: the Codex row stayed "已接入 · 重开 Codex 后生效"
// after Codex was restarted. On macOS closing the Codex app's window keeps
// the app running, and an editor's Codex or the CLI's daemon runs on
// through a reopened app. Opened, the row says which copy is left on the
// old list, since when, and how that one is reopened: ⌘Q for the app, the
// daemon's restart command, the app whose own Codex it is. Every language, wide and narrow; "Got it"
// takes it away. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const days = (n) => new Date(Date.now() - n * 86400e3).toISOString();
const codex = {
  id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: true,
  fields: [{ key: "model", label: "model", value: "gpt-6.1-sol", options: [{ value: "gpt-6.1-sol", label: "GPT-6.1 Sol", ref: "group/auto-gpt-6-1-sol", note: "Group · via magpie" }] }],
  stale: 3, staleCopies: [{ kind: "app", since: days(3) }, { kind: "daemon", since: days(1) }, { kind: "embedded", app: "Agents Anywhere", since: days(1) }],
};

function serve(lang, agent = codex) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agent], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="codex"]';
// as each engine's Intl says "3 days ago"
const ago3 = { en: /3 days ago/, zh: /3 ?天前/, "zh-TW": /3 ?天前/, ja: /3 ?日前/, de: /vor 3 Tagen/ };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [980, 440]) {
      test(`${engine} ${lang} ${width}px: the opened row says which Codex is left and how to reopen it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 800 } });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row} .ag-conn`).waitFor();
        await page.locator(`${row} .ag-link`).click();
        const copies = page.locator(`${row} .ag-stale-copy`);
        await copies.first().waitFor();
        assert.equal(await copies.count(), 3);
        const [app, daemon, embedded] = await copies.allTextContents();
        // miaopasi: Agents Anywhere's own Codex, named by its app
        assert.equal(embedded.split("Agents Anywhere").length - 1, 2, embedded);
        assert.equal(await copies.nth(0).locator("code").textContent(), "⌘Q");
        assert.equal(await copies.nth(1).locator("code").textContent(), "codex app-server daemon restart");
        assert.match(app, ago3[lang], "the app's start");
        for (const s of [app, daemon, embedded]) {
          assert.ok(s.includes("Codex"), s);
          assert.doesNotMatch(s, /\{\w+\}/, "a placeholder left: " + s);
        }
        if (lang !== "en") assert.doesNotMatch(app, /closing its window/, "not translated: " + app);
        // nothing runs past the row
        const over = await page.evaluate((r) => [...document.querySelectorAll(r + " .ag-stale-copy, " + r + " .ag-stale-copy *")].filter((e) => e.scrollWidth > e.clientWidth + 1 || e.getBoundingClientRect().right > document.documentElement.clientWidth).map((e) => e.className || e.tagName), row);
        assert.deepEqual(over, []);
        // told: the lines go with the warning
        await page.locator(`${row} .ag-warn button`).click();
        await page.waitForFunction((r) => !document.querySelector(r + " .ag-stale-copy"), row);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// #1374 (SivanCola): folded, the row said only "reopen Codex", so the app
// was reopened while VS Code's Codex was the copy left. Folded, it names
// the one kind left and how it is reopened, counts several, and its hover
// has each copy whole. The reporter's case first: one editor's Codex.
const folded = {
  ide: { copies: [{ kind: "ide", since: days(1) }],
    say: { en: "reload the editor's window", zh: "重新加载编辑器窗口", "zh-TW": "重新載入編輯器視窗", ja: "エディタのウィンドウを再読み込み", de: "Editorfenster neu laden" } },
  app: { copies: [{ kind: "app", since: days(1) }],
    say: { en: "quit and reopen the Codex app", zh: "退出并重开 Codex 应用", "zh-TW": "結束並重開 Codex 應用程式", ja: "Codex アプリを終了", de: "Codex-App beenden" } },
  embedded: { copies: [{ kind: "embedded", app: "Agents Anywhere", since: days(1) }],
    say: { en: "reopen Agents Anywhere", zh: "重开 Agents Anywhere", "zh-TW": "重開 Agents Anywhere", ja: "Agents Anywhere を開き直す", de: "Agents Anywhere neu öffnen" } },
  several: { copies: codex.staleCopies,
    say: { en: "3 copies of Codex", zh: "3 个运行中的 Codex", "zh-TW": "3 個執行中的 Codex", ja: "Codex 3 件", de: "3 Codex-Instanzen" } },
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [980, 440]) {
      test(`${engine} ${lang} ${width}px: the folded row says which Codex to reopen`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 800 } });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        for (const [name, c] of Object.entries(folded)) {
          await page.unrouteAll();
          await page.route("**/*", serve(lang, { ...codex, stale: c.copies.length, staleCopies: c.copies }));
          await page.goto("http://magpie.test/?view=agents");
          const words = page.locator(`${row} .ag-st-t`);
          await page.locator(`${row} .ag-conn`).waitFor();
          const said = await words.textContent();
          assert.ok(said.includes(c.say[lang]), `${name}: ${said}`);
          assert.doesNotMatch(said, /\{\w+\}/, `${name}: a placeholder left`);
          // the hover has every copy whole, with its command
          const title = await words.getAttribute("title");
          assert.equal(title.split("\n").length, c.copies.length, `${name}: ${title}`);
          if (name === "app") assert.ok(title.includes("⌘Q"), title);
          assert.doesNotMatch(title, /\{\w+\}/, `${name}: a placeholder left in ${title}`);
          // one line, nothing past the row
          const box = await words.evaluate((e) => ({ right: e.getBoundingClientRect().right, lines: Math.round(e.getBoundingClientRect().height / parseFloat(getComputedStyle(e).lineHeight)) }));
          assert.ok(box.right <= width, `${name}: past the window`);
          assert.equal(box.lines, 1, `${name}: one line`);
          if (process.env.ARTIFACT_DIR) await page.locator(row).screenshot({ path: path.join(process.env.ARTIFACT_DIR, `stale-${engine}-${lang}-${width}-${name}.png`) });
        }
        // a copy of no kind magpie knows keeps the plain words
        await page.unrouteAll();
        await page.route("**/*", serve(lang, { ...codex, stale: 1, staleCopies: [{ since: days(1) }] }));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row} .ag-conn`).waitFor();
        assert.equal(await page.locator(`${row} .ag-st-t`).getAttribute("title"), null);
        assert.deepEqual(errors, []);
      });
    }
  }
}
