// Run with Node's test runner and Playwright on the module path; see README.md.
// Opening Codex (Yink88564391 on X): Codex's row menu has Open the Codex app
// where the desktop app is installed and Open Codex with doesn't say
// Terminal (openApp); it posts agents/open with the id alone, while Open in
// terminal asks for the terminal. Nothing of it sits on the row: every
// row's Details and model picker line up (the owner, 11-13, on the Open
// button ee1553913 had put between them). Another agent's menu, the web
// build and a Codex with nothing to open have no such item. Codex's
// settings have "Open Codex with" (Automatic, Codex app, Terminal), an app
// menu, which posts settings/codex-open. Every language, Chromium and
// WebKit, wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, web, codex, posted) {
  let cur = { theme: "light", lang, version: "0.1.400", titleModels: [], codexOpen: "" };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ ...agentRow("codex", "Codex"), ...codex }, { ...agentRow("claude", "Claude Code"), terminal: true, command: "claude" }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/agents/open") { posted.push(["open", req.postDataJSON()]); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/settings/codex-open") {
      const body = req.postDataJSON();
      posted.push(["codex-open", body]);
      cur = { ...cur, codexOpen: body.open };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const W = {
  en: { open: "Open", app: "Open the Codex app", term: "Open in terminal", name: "Open Codex with", auto: "Automatic", capp: "Codex app", cterm: "Terminal" },
  zh: { open: "打开", app: "打开 Codex 应用", term: "在终端中打开", name: "打开 Codex 的方式", auto: "自动", capp: "Codex 应用", cterm: "终端" },
  "zh-TW": { open: "開啟", app: "開啟 Codex 應用程式", term: "在終端機中開啟", name: "開啟 Codex 的方式", auto: "自動", capp: "Codex 應用程式", cterm: "終端機" },
  ja: { open: "開く", app: "Codex アプリを開く", term: "ターミナルで開く", name: "Codex を開く方法", auto: "自動", capp: "Codex アプリ", cterm: "ターミナル" },
  de: { open: "Öffnen", app: "Codex-App öffnen", term: "Im Terminal öffnen", name: "Codex öffnen mit", auto: "Automatisch", capp: "Codex-App", cterm: "Terminal" },
};
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

async function start(engine, t, lang, width, web, codex) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posted = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, web, codex, posted));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=agents");
  await page.locator('.row.agent[data-id="codex"]').waitFor();
  return { page, errors, posted };
}

// lineUp: every agent row's Details and model picker start where every
// other row's do; nothing of Codex's own sits between them (an Open button
// there pushed its Details left, the owner, 11-13)
async function lineUp(page) {
  await page.locator('.row.agent[data-id="claude"] .ag-start').waitFor();
  const xs = await page.evaluate(() => [...document.querySelectorAll(".row.agent.conn")].filter((r) => r.offsetParent).map((r) => {
    const x = (s) => { const e = r.querySelector(s); return e ? Math.round(e.getBoundingClientRect().left) : null; };
    return { id: r.dataset.id, link: x(".ag-link"), pick: x(".ag-start") };
  }));
  assert(xs.length >= 2, `rows: ${JSON.stringify(xs)}`);
  for (const k of ["link", "pick"]) assert.equal(new Set(xs.map((r) => r[k])).size, 1, `${k} not lined up: ${JSON.stringify(xs)}`);
}

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [900, 400]) {
      const w = W[lang];
      test(`${engine} ${lang} ${width}px: Codex's row menu opens the app, and its row lines up with the others`, async (t) => {
        const { page, errors, posted } = await start(engine, t, lang, width, false, { terminal: true, command: "codex", openApp: true });
        await lineUp(page);
        await page.locator('.row.agent[data-id="codex"] .ag-handle').click();
        const menu = page.locator(".pop.row-menu");
        await menu.waitFor();
        const app = menu.locator(".rm-item", { hasText: w.app });
        assert.equal(await app.count(), 1, "Open the Codex app in the menu");
        assert.equal(await menu.locator(".rm-item", { hasText: w.term }).count(), 1, "Open in terminal beside it");
        const mb = await menu.boundingBox();
        assert(mb.x >= 0 && mb.x + mb.width <= width, `the menu runs off the window: ${JSON.stringify(mb)}`);
        const sent = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/agents/open");
        await app.click();
        await sent;
        assert.deepEqual(posted, [["open", { agent: "codex" }]], "the id alone");
        await menu.waitFor({ state: "detached" });
        await page.locator('.row.agent[data-id="claude"] .ag-handle').click();
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item", { hasText: w.app }).count(), 0, "another agent's menu");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: with no app the menu opens the terminal only`, async (t) => {
        const { page, errors } = await start(engine, t, lang, width, false, { terminal: true, command: "codex" });
        await lineUp(page);
        await page.locator('.row.agent[data-id="codex"] .ag-handle').click();
        const menu = page.locator(".pop.row-menu");
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item", { hasText: w.app }).count(), 0);
        assert.equal(await menu.locator(".rm-item", { hasText: w.term }).count(), 1);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: Open Codex with is picked from the app menu`, async (t) => {
        const { page, errors, posted } = await start(engine, t, lang, width, false, { terminal: true, command: "codex", openApp: true });
        await openAgentSettings(page);
        const row = page.locator('.row.agent[data-id="codex"] #codexOpenRow');
        const pick = page.locator("#codexOpenPick button");
        await pick.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        assert.equal((await pick.textContent()).trim(), w.auto);
        assert.equal(await page.locator("select").count(), 0, "a native select");
        await page.locator('.row.agent[data-id="claude"] .ag-link').click();
        await page.locator('.row.agent[data-id="claude"] .ag-prefs').waitFor();
        assert.equal(await page.locator('.row.agent[data-id="claude"] #codexOpenRow').count(), 0, "the row in Claude Code's settings");
        await openAgentSettings(page);
        await pick.scrollIntoViewIfNeeded();
        for (const [label, open] of [[w.cterm, "terminal"], [w.capp, "app"], [w.auto, ""]]) {
          await pick.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          assert.deepEqual((await menu.locator(".pm-item .pm-name").allTextContents()).map((s) => s.trim()), [w.auto, w.capp, w.cterm]);
          const mb = await menu.boundingBox();
          assert(mb.x >= 0 && mb.x + mb.width <= width, `the menu runs off the window: ${JSON.stringify(mb)}`);
          await menu.locator(".pm-item").filter({ has: page.locator(".pm-name", { hasText: new RegExp("^" + label + "$") }) }).click();
          await page.locator("#codexOpenPick button", { hasText: new RegExp("^\\s*" + label + "\\s*$") }).waitFor();
          assert.deepEqual(posted.at(-1), ["codex-open", { open }]);
        }
        assert.equal(posted.length, 3, JSON.stringify(posted));
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: the web build and a Codex with nothing to open have no Open the Codex app`, async (t) => {
    const item = async (page) => {
      await page.locator('.row.agent[data-id="codex"] .ag-handle').click();
      await page.locator(".pop.row-menu").waitFor();
      return page.locator(".pop.row-menu .rm-item", { hasText: "Open the Codex app" }).count();
    };
    let { page, errors } = await start(engine, t, "en", 900, true, { terminal: true, command: "codex", openApp: true });
    assert.equal(await item(page), 0, "the web build");
    assert.deepEqual(errors, []);
    ({ page, errors } = await start(engine, t, "en", 900, false, {}));
    assert.equal(await item(page), 0, "nothing to open");
    assert.deepEqual(errors, []);
  });
}
