// Run with Node's test runner and Playwright on the module path; see README.md.
// Kimi Code's thinking level (@apple_test_src on X: every other agent had
// one, Kimi Code none). It is Kimi Code's own [thinking] in config.toml;
// Default is the model's default_effort, which the row names, then off and
// the model's support_efforts. In the window it is in the row opened from
// its link; in the tray panel the row opens to its slider. A pick posts
// the field; nothing runs off the side of a narrow window.
// English, Simplified and Traditional Chinese, Japanese and German,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["off", "low", "medium", "high", "max"];
const model = "magpie/moonshot/kimi-k2.8";
const kimi = (effort) => ({
  id: "kimi", name: "Kimi Code", icon: "kimi", path: "/home/u/.kimi-code/config.toml", wired: true,
  fields: [
    { key: "model", label: "model", value: model, options: [{ value: model, icon: "kimi", ref: "moonshot/kimi-k2.8" }] },
    { key: "effort", label: "thinking", value: effort, options: [{ value: "", note: "Kimi Code's default for the model: medium", takes: "medium" }, ...levels.map((value) => ({ value }))] },
  ],
});
const filler = Array.from({ length: 6 }, (_, i) => ({ id: "pi" + i, name: "Pi " + i, path: "/p", fields: [{ key: "model", label: "model", value: "", options: [] }] }));

function serve(lang, sets) {
  let effort = "";
  const st = () => ({ agents: [kimi(effort), ...filler], profiles: [], settings: { lang, theme: "light", guideHidden: true } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/set") {
      const b = req.postDataJSON();
      sets.push(b);
      if (b.field === "effort") effort = b.value;
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
  en: { thinking: "thinking", def: "default (medium)", low: "low", off: "off" },
  zh: { thinking: "思考", def: "默认（中）", low: "低", off: "关闭" },
  "zh-TW": { thinking: "思考", def: "預設（中）", low: "低", off: "關閉" },
  ja: { thinking: "思考", def: "デフォルト（中）", low: "低", off: "オフ" },
  de: { thinking: "Thinking", def: "Standard (mittel)", low: "niedrig", off: "aus" },
};
const row = `.row.agent[data-id="kimi"]`;
const noSideScroll = (page) => page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Kimi Code has a thinking level`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [];
      t.after(async () => {
        if (errors.length) console.log(errors);
        await browser.close();
      });
      const open = async (mode, sets) => {
        const page = await (await browser.newContext({ viewport: { width: mode ? 440 : 560, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, sets));
        await page.goto("http://magpie.test/" + (mode ? "?mode=" + mode : ""));
        await page.locator(row).waitFor();
        return page;
      };

      await t.test("the window", async () => {
        const sets = [];
        const page = await open("", sets);
        await page.locator(`${row} .ag-link`).click();
        await page.locator(`${row} .ag-exp`).waitFor();
        const field = page.locator(`${row} .field[data-key="effort"]`);
        assert.equal(await field.count(), 1);
        assert.equal(await field.locator(".v").textContent(), w.def);
        const box = await field.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= 560, `the effort runs off the window: ${JSON.stringify(box)}`);
        assert(await noSideScroll(page), "the window scrolls sideways");

        const y = await page.evaluate(() => scrollY);
        await field.click();
        await page.locator("#effortControl:not([hidden])").waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "the click scrolled the page");
        assert.equal(await page.locator("#effortTitle").textContent(), w.thinking);
        assert.equal(await page.locator("#effortValue").textContent(), w.def);
        // the default, off, then the model's four levels
        assert.equal(await page.locator("#effortTicks i").count(), 6);
        await page.locator("#effortRange").evaluate((r) => {
          r.value = "2";
          r.dispatchEvent(new Event("input", { bubbles: true }));
          r.dispatchEvent(new Event("change", { bubbles: true }));
        });
        assert.equal(await page.locator("#effortValue").textContent(), w.low);
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="kimi"] .field[data-key="effort"] .v')?.textContent !== document.querySelector("#effortMin")?.textContent);
        assert.deepEqual(sets, [{ agent: "kimi", field: "effort", value: "low" }]);
        assert.equal(await page.locator(`${row} .field[data-key="effort"] .v`).textContent(), w.low);
        assert(await noSideScroll(page), "the window scrolls sideways");
      });

      await t.test("the panel", async () => {
        const sets = [];
        const page = await open("panel", sets);
        await page.locator(`${row} .ag-sum`).click();
        await page.waitForTimeout(700);
        const box = page.locator(`${row} .ag-open .effort-control`);
        assert.equal(await box.locator(".effort-ticks i").count(), 6);
        await box.locator(".eslide").press("ArrowRight");
        await page.waitForTimeout(300);
        assert.deepEqual(sets, [{ agent: "kimi", field: "effort", value: "off" }]);
        assert(await noSideScroll(page), "the panel scrolls sideways");
      });

      assert.deepEqual(errors, []);
    });
  }
}
