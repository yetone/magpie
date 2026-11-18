// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's status line (lyj1115 on X: the account's quota or the relay's
// balance under Codex's chat box, off unless turned on): the same square
// as Claude Code's on Codex's row, its title Codex's own note (the
// option's, not Claude Code's 5h and 7d), a click posting statusline on
// and off, in every language. A Codex the switch isn't offered to (too old
// to draw it, or a provider of the user's own) shows no square. Chromium
// and WebKit, the window and the narrow tray panel.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const NOTE = "the remaining quota or balance of the account in use, under Codex's prompt";
const codex = (id, value, offered) => ({
  id, name: "Codex " + id, icon: "codex-color", path: "/test/" + id, wired: true,
  fields: [
    { key: "model", label: "model", value: "gpt-5.5", options: [{ value: "gpt-5.5", icon: "openai", ref: "codex/gpt-5.5" }] },
    { key: "statusline", label: "status line", value, options: offered ? [{ value: "on", note: NOTE }] : [] },
  ],
});

function serve(lang, sets) {
  let on = false;
  const st = () => ({ agents: [codex("mine", on ? "on" : "", true), codex("theirs", "", false)], profiles: [], settings: { lang, theme: "light" } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/set") {
      const b = req.postDataJSON();
      sets.push(b);
      if (b.field === "statusline") on = b.value === "on";
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
  en: { label: "status line", note: "under Codex's prompt", said: "status line → on" },
  zh: { label: "状态栏", note: "Codex 输入框下方显示当前账号的剩余额度或中转余额", said: "状态栏 → 开启" },
  "zh-TW": { label: "狀態列", note: "Codex 輸入框下方顯示目前帳號的剩餘額度或中轉餘額", said: "狀態列 → 開啟" },
  ja: { label: "ステータスライン", note: "リレーの残高", said: "ステータスライン → オン" },
  de: { label: "Statuszeile", note: "Guthaben des genutzten Kontos", said: "Statuszeile → an" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;
const square = (id) => `${row(id)} .field[data-key="statusline"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Codex's status line is a switch`, async (t) => {
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
          await page.locator(row("mine")).waitFor();
          // one row open at a time: the user's first, then magpie's
          for (const id of ["theirs", "mine"]) {
            await page.locator(`${row(id)} ${mode ? ".ag-sum" : ".ag-link"}`).click();
            await page.waitForTimeout(mode ? 700 : 200);
            if (id === "theirs") assert.equal(await page.locator(square("theirs")).count(), 0, "a square on a Codex it isn't offered to");
          }
          const sq = page.locator(square("mine")).first();
          await sq.waitFor();
          assert.equal(await sq.getAttribute("aria-pressed"), "false");
          const title = await sq.getAttribute("title");
          assert.ok(title.includes(w.label) && title.includes(w.note) && !/5h|5 小时|5 小時|5 時間|5-Stunden/.test(title), `title ${JSON.stringify(title)}`);
          const box = await sq.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= (mode ? 400 : 900), "the square is off the page");
          await sq.click();
          await page.waitForTimeout(300);
          assert.deepEqual(sets, [{ agent: "mine", field: "statusline", value: "on" }]);
          assert.ok((await page.locator("#status").textContent()).includes(w.said), await page.locator("#status").textContent());
          const lit = page.locator(square("mine")).first();
          assert.equal(await lit.getAttribute("aria-pressed"), "true");
          assert.match(await lit.getAttribute("class"), /\bset\b/);
          await lit.click();
          await page.waitForTimeout(300);
          assert.deepEqual(sets[1], { agent: "mine", field: "statusline", value: "" });
        });
      }
      assert.deepEqual(errors, []);
    });
  }
}
