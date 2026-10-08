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

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [codex], profiles: [], settings: { lang, theme: "light" } });
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
