// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's status line (steven_4422 on Discord: the 5h and 7d use in
// Claude Code): a square on Claude Code's row, beside ultracode's, that a
// click turns on (posting statusline on) and off again, lit while on, its
// title saying what it shows and the change said in the status, in every
// language. A row whose settings.json has a status line of the user's own
// gets no options from magpie and shows no square. Chromium and WebKit,
// the window and the narrow tray panel.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const NOTE = "the 5h and 7d use of the accounts magpie serves, under Claude Code's prompt";
const claude = (id, value, offered) => ({
  id, name: "Claude Code " + id, icon: "claudecode-color", path: "/test/" + id, wired: true,
  fields: [
    { key: "model", label: "model", value: "claude-opus-5-5", options: [{ value: "claude-opus-5-5", icon: "claude-color", ref: "claude/claude-opus-5-5" }] },
    { key: "statusline", label: "status line", value, options: offered ? [{ value: "on", note: NOTE }] : [] },
  ],
});

function serve(lang, sets) {
  let on = false;
  const st = () => ({ agents: [claude("mine", on ? "on" : "", true), claude("theirs", "", false)], profiles: [], settings: { lang, theme: "light" } });
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
  en: { label: "status line", note: "5h and 7d use", said: "status line → on" },
  zh: { label: "状态栏", note: "5 小时和 7 天用量", said: "状态栏 → 开启" },
  "zh-TW": { label: "狀態列", note: "5 小時和 7 天用量", said: "狀態列 → 開啟" },
  ja: { label: "ステータスライン", note: "5 時間と 7 日の使用量", said: "ステータスライン → オン" },
  de: { label: "Statuszeile", note: "7-Tage-Nutzung", said: "Statuszeile → an" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;
const square = (id) => `${row(id)} .field[data-key="statusline"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's status line is a switch`, async (t) => {
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
            if (id === "theirs") assert.equal(await page.locator(square("theirs")).count(), 0, "a square over the user's own status line");
          }
          const sq = page.locator(square("mine")).first();
          await sq.waitFor();
          assert.equal(await sq.getAttribute("aria-pressed"), "false");
          const title = await sq.getAttribute("title");
          assert.ok(title.includes(w.label) && title.includes(w.note), `title ${JSON.stringify(title)}`);
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
