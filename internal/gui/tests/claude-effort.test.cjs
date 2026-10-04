// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's effort follows its model (#354), ultracode is a switch of its
// own (#352), and an effort or ultracode set says that an open session keeps
// what it started with (#353). On Opus 5.5 the row has an ultracode square:
// a click posts ultracode on, lights it and says the restart; the effort
// slider's change says it too. On Opus 4.5 the slider has its three levels
// and no ultracode; on Haiku 4.5 there is neither an effort nor ultracode.
// In the window they are in a connected row, opened from its link; the
// window scrolled down stays where it is through the clicks. English and
// Chinese, Chromium and WebKit, the window and the tray panel.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const five = ["low", "medium", "high", "xhigh", "max"];
const claude = (id, model, efforts, ultracode) => ({
  id, name: "Claude Code " + id, icon: "claudecode-color", path: "/test/" + id, wired: true,
  fields: [
    { key: "model", label: "model", value: model, options: [{ value: model, icon: "claude-color", ref: "claude/" + model }] },
    { key: "effort", label: "effort", value: "", options: efforts.map((value) => ({ value })) },
    { key: "ultracode", label: "ultracode", value: ultracode, options: efforts.includes("xhigh") ? [{ value: "on", note: "Claude plans a workflow for each substantive task" }] : [] },
  ],
});
const filler = Array.from({ length: 14 }, (_, i) => ({ id: "pi" + i, name: "Pi " + i, path: "/p", fields: [{ key: "model", label: "model", value: "", options: [] }] }));
const agents = (on) => [claude("o55", "claude-opus-5-5", five, on ? "on" : ""), claude("o45", "claude-opus-4-5", ["low", "medium", "high"], ""), claude("h45", "claude-haiku-4-5", [], ""), ...filler];
const EFFORT = "an open Claude Code session keeps the effort it started with — restart it to use this.";
const ULTRA = "an open Claude Code session keeps the ultracode it started with — restart it to use this.";

function serve(lang, sets) {
  let on = false;
  const st = (notice) => ({ agents: agents(on), profiles: [], settings: { lang, theme: "light" }, ...(notice ? { notice } : {}) });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/set") {
      const b = req.postDataJSON();
      sets.push(b);
      if (b.field === "ultracode") on = b.value === "on";
      return route.fulfill({ json: st(b.field === "ultracode" ? ULTRA : EFFORT) });
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
  en: { effort: "restart it to use this", ultra: "keeps the ultracode it started with" },
  zh: { effort: "仍按启动时的推理强度运行", ultra: "仍按启动时的 ultracode 设置运行" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's effort follows its model, ultracode is a switch`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [];
      t.after(async () => {
        if (errors.length) console.log(errors);
        await browser.close();
      });
      const open = async (mode, sets) => {
        const page = await (await browser.newContext({ viewport: { width: mode ? 440 : 900, height: 560 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, sets));
        await page.goto("http://magpie.test/" + (mode ? "?mode=" + mode : ""));
        await page.locator(row("o55")).waitFor();
        return page;
      };

      await t.test("the window", async () => {
        const sets = [];
        const page = await open("", sets);
        // the rows as the model has them, each opened in turn
        const expand = async (id) => {
          await page.locator(`${row(id)} .ag-link`).click();
          await page.locator(`${row(id)} .ag-exp`).waitFor();
        };
        await expand("o45");
        assert.equal(await page.locator(`${row("o45")} .field[data-key="ultracode"]`).count(), 0, "ultracode on a model without xhigh");
        assert.equal(await page.locator(`${row("o45")} .field[data-key="effort"]`).count(), 1);
        await expand("h45");
        assert.equal(await page.locator(`${row("h45")} .field[data-key="effort"]`).count(), 0, "an effort for Haiku 4.5");
        assert.equal(await page.locator(`${row("h45")} .field[data-key="ultracode"]`).count(), 0);
        await expand("o55");
        assert.equal(await page.locator(`${row("o55")} .field[data-key="ultracode"]`).count(), 1);

        const sq = page.locator(`${row("o55")} .field[data-key="ultracode"]`);
        assert.equal(await sq.getAttribute("aria-pressed"), "false");
        await page.evaluate(() => { const s = document.scrollingElement; s.scrollTop = 40; });
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await sq.click();
        await page.waitForTimeout(300);
        assert.deepEqual(sets, [{ agent: "o55", field: "ultracode", value: "on" }]);
        assert.match(await page.locator("#status").textContent(), new RegExp(w.ultra));
        const lit = page.locator(`${row("o55")} .field[data-key="ultracode"]`);
        assert.equal(await lit.getAttribute("aria-pressed"), "true");
        assert.match(await lit.getAttribute("class"), /\bset\b/);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click scrolled the page");
        await lit.click();
        await page.waitForTimeout(300);
        assert.deepEqual(sets[1], { agent: "o55", field: "ultracode", value: "" });
      });

      await t.test("the panel", async () => {
        const sets = [];
        const page = await open("panel", sets);
        await page.locator(`${row("o45")} .ag-sum`).click();
        await page.waitForTimeout(700);
        const box = page.locator(`${row("o45")} .ag-open .effort-control`);
        // the default where it stands, then Opus 4.5's three levels
        assert.equal(await box.locator(".effort-ticks i").count(), 4);
        assert.equal(await box.locator(".effort-ends span").last().textContent(), lang === "zh" ? "高" : "high");
        await box.locator(".eslide").press("ArrowRight");
        await page.waitForTimeout(300);
        assert.deepEqual(sets, [{ agent: "o45", field: "effort", value: "low" }]);
        assert.match(await page.locator("#status").textContent(), new RegExp(w.effort));
      });

      assert.deepEqual(errors, []);
    });
  }
}
