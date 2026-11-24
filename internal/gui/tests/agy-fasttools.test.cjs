// Run with Node's test runner and Playwright on the module path; see README.md.
// agy's fast tools (internal/fasttools): a square on agy's row that a click
// turns on (posting fasttools on) and off again, lit while on, its title
// saying what it does and the change said in the status, in every
// language. Chromium and WebKit, the window and the narrow tray panel.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const agy = (value) => ({
  id: "agy", name: "Antigravity CLI", icon: "antigravity-color", path: "/test/agy", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/claude/claude-opus-5-5", options: [{ value: "magpie/claude/claude-opus-5-5", icon: "claude-color", ref: "claude/claude-opus-5-5" }] },
    { key: "fasttools", label: "fast tools", value, options: [{ value: "on", note: "batched search, reads, edits and checks, and a workspace snapshot at the start" }] },
  ],
});

function serve(lang, sets) {
  let on = false;
  const st = () => ({ agents: [agy(on ? "on" : "")], profiles: [], settings: { lang, theme: "light" } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/set") {
      const b = req.postDataJSON();
      sets.push(b);
      if (b.field === "fasttools") on = b.value === "on";
      return route.fulfill({ json: st() });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { label: "fast tools", note: "workspace snapshot", said: "fast tools → on" },
  zh: { label: "快速工具", note: "工作区概况", said: "快速工具 → 开启" },
  "zh-TW": { label: "快速工具", note: "工作區概況", said: "快速工具 → 開啟" },
  ja: { label: "高速ツール", note: "ワークスペースの概要", said: "高速ツール → オン" },
  de: { label: "Schnellwerkzeuge", note: "Arbeitsbereich", said: "Schnellwerkzeuge → an" },
};
const row = `.row.agent[data-id="agy"]`;
const square = `${row} .field[data-key="fasttools"]`;
const pressed = (page, v) => page.waitForFunction(([sel, v]) => document.querySelector(sel)?.getAttribute("aria-pressed") === v, [square, v]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: agy's fast tools is a switch`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [];
      t.after(async () => {
        if (errors.length) console.log(errors);
        await browser.close();
      });
      for (const mode of ["", "panel"]) {
        await t.test(mode || "window", async () => {
          const sets = [];
          const page = await (await browser.newContext({ viewport: { width: mode ? 400 : 900, height: 560 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, sets));
          await page.goto("http://magpie.test/" + (mode ? "?mode=" + mode : ""));
          await page.locator(row).waitFor();
          await page.locator(`${row} ${mode ? ".ag-sum" : ".ag-link"}`).click();
          const sq = page.locator(square).first();
          await sq.waitFor();
          assert.equal(await sq.getAttribute("aria-pressed"), "false");
          const title = await sq.getAttribute("title");
          assert.ok(title.includes(w.label) && title.includes(w.note), `title ${JSON.stringify(title)}`);
          assert.notEqual(await sq.locator("svg path").getAttribute("d"), "undefined", "the square has no glyph");
          const box = await sq.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= (mode ? 400 : 900), "the square is off the page");
          await sq.click();
          await pressed(page, "true");
          assert.deepEqual(sets, [{ agent: "agy", field: "fasttools", value: "on" }]);
          assert.ok((await page.locator("#status").textContent()).includes(w.said), await page.locator("#status").textContent());
          assert.match(await page.locator(square).first().getAttribute("class"), /\bset\b/);
          await page.locator(square).first().click();
          await pressed(page, "false");
          assert.deepEqual(sets[1], { agent: "agy", field: "fasttools", value: "" });
        });
      }
      assert.deepEqual(errors, []);
    });
  }
}
