// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's Open button (Yink88564391 on X): Codex's row on the Agents page has a
// visible Open button beside its model, not only the row menu's Open in
// terminal. It opens the desktop app where one is installed (openApp) and
// the CLI in a terminal otherwise; either way it posts agents/open with the
// id alone, while the menu's Open in terminal asks for the terminal. A row
// of any other agent, the web build and a Codex with nothing to open have no
// button. Codex's settings have "Open Codex with" (Automatic, Codex app,
// Terminal), an app menu, which posts settings/codex-open. Every language,
// Chromium and WebKit, wide and narrow; no backend, the API is faked here.
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
  en: { open: "Open", app: "Open the Codex app", term: "Open Codex in a terminal", name: "Open Codex with", auto: "Automatic", capp: "Codex app", cterm: "Terminal" },
  zh: { open: "打开", app: "打开 Codex 应用", term: "在终端中打开 Codex", name: "打开 Codex 的方式", auto: "自动", capp: "Codex 应用", cterm: "终端" },
  "zh-TW": { open: "開啟", app: "開啟 Codex 應用程式", term: "在終端機中開啟 Codex", name: "開啟 Codex 的方式", auto: "自動", capp: "Codex 應用程式", cterm: "終端機" },
  ja: { open: "開く", app: "Codex アプリを開く", term: "ターミナルで Codex を開く", name: "Codex を開く方法", auto: "自動", capp: "Codex アプリ", cterm: "ターミナル" },
  de: { open: "Öffnen", app: "Codex-App öffnen", term: "Codex im Terminal öffnen", name: "Codex öffnen mit", auto: "Automatisch", capp: "Codex-App", cterm: "Terminal" },
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

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [900, 400]) {
      const w = W[lang];
      test(`${engine} ${lang} ${width}px: Codex's row has an Open button that opens the app`, async (t) => {
        const { page, errors, posted } = await start(engine, t, lang, width, false, { terminal: true, command: "codex", openApp: true });
        const btn = page.locator('.row.agent[data-id="codex"] .ag-openapp');
        await btn.waitFor();
        assert.equal((await btn.textContent()).trim(), w.open);
        assert.equal(await btn.getAttribute("title"), w.app);
        assert.equal(await page.locator('.row.agent[data-id="claude"] .ag-openapp').count(), 0, "another agent's row");
        const box = await btn.boundingBox(), row = await page.locator('.row.agent[data-id="codex"]').boundingBox();
        assert(box.width > 0 && box.x >= row.x && box.x + box.width <= row.x + row.width, `the button leaves the row: ${JSON.stringify([box, row])}`);
        // it does not sit on the model picker or the switch
        for (const sel of [".ag-start", ".ag-conn"]) {
          const o = await page.locator(`.row.agent[data-id="codex"] ${sel}`).first().boundingBox();
          if (o) assert(box.x + box.width <= o.x + 1 || o.x + o.width <= box.x + 1 || box.y + box.height <= o.y + 1 || o.y + o.height <= box.y + 1, `Open overlaps ${sel}`);
        }
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        const sent = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/agents/open");
        await btn.click();
        await sent;
        assert.deepEqual(posted, [["open", { agent: "codex" }]], "the id alone");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: with no app the button opens the terminal`, async (t) => {
        const { page, errors, posted } = await start(engine, t, lang, width, false, { terminal: true, command: "codex" });
        const btn = page.locator('.row.agent[data-id="codex"] .ag-openapp');
        await btn.waitFor();
        assert.equal(await btn.getAttribute("title"), w.term);
        const sent = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/agents/open");
        await btn.click();
        await sent;
        assert.deepEqual(posted, [["open", { agent: "codex" }]]);
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

  test(`${engine}: the web build and a Codex with nothing to open have no Open button`, async (t) => {
    let { page, errors } = await start(engine, t, "en", 900, true, { terminal: true, command: "codex", openApp: true });
    assert.equal(await page.locator('.row.agent[data-id="codex"] .ag-openapp').count(), 0, "the web build");
    assert.deepEqual(errors, []);
    ({ page, errors } = await start(engine, t, "en", 900, false, {}));
    assert.equal(await page.locator('.row.agent[data-id="codex"] .ag-openapp').count(), 0, "nothing to open");
    assert.deepEqual(errors, []);
  });
}
