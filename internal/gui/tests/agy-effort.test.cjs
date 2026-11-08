// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity CLI's effort (xyenon on Discord): agy's --effort is for one
// session and turned away for magpie's models, and its settings.json has
// no key for one, so its row has an effort, which magpie's gateway asks
// its requests for. The default (as agy asks) is the first stop, then the
// levels its models have. In the window it is in the row opened from its
// link; in the tray panel the row opens to its slider. A pick posts the
// field; nothing runs off the side of a narrow window.
// English, Simplified and Traditional Chinese, Japanese and German,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["low", "high"];
const model = "magpie/think/gemini-3.8-flash";
const agy = (effort) => ({
  id: "agy", name: "Antigravity CLI", icon: "antigravity-color", path: "/home/u/.gemini/antigravity-cli/settings.json", wired: true,
  fields: [
    { key: "model", label: "model", value: model, options: [{ value: model, icon: "gemini-color", ref: "think/gemini-3.8-flash" }] },
    { key: "effort", label: "effort", value: effort, options: [{ value: "" }, ...levels.map((value) => ({ value }))] },
  ],
});
const filler = Array.from({ length: 6 }, (_, i) => ({ id: "pi" + i, name: "Pi " + i, path: "/p", fields: [{ key: "model", label: "model", value: "", options: [] }] }));

function serve(lang, sets) {
  let effort = "";
  const st = () => ({ agents: [agy(effort), ...filler], profiles: [], settings: { lang, theme: "light", guideHidden: true } });
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
  en: { effort: "effort", def: "default", high: "high" },
  zh: { effort: "推理强度", def: "默认", high: "高" },
  "zh-TW": { effort: "推理強度", def: "預設", high: "高" },
  ja: { effort: "推論レベル", def: "デフォルト", high: "高" },
  de: { effort: "Reasoning-Effort", def: "Standard", high: "hoch" },
};
const row = `.row.agent[data-id="agy"]`;
const noSideScroll = (page) => page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Antigravity CLI has an effort`, async (t) => {
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
        assert.equal(await page.locator("#effortTitle").textContent(), w.effort);
        assert.equal(await page.locator("#effortValue").textContent(), w.def);
        // the default, then its models' two levels
        assert.equal(await page.locator("#effortTicks i").count(), 3);
        await page.locator("#effortRange").evaluate((r) => {
          r.value = "2";
          r.dispatchEvent(new Event("input", { bubbles: true }));
          r.dispatchEvent(new Event("change", { bubbles: true }));
        });
        assert.equal(await page.locator("#effortValue").textContent(), w.high);
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="agy"] .field[data-key="effort"] .v')?.textContent !== document.querySelector("#effortMin")?.textContent);
        assert.deepEqual(sets, [{ agent: "agy", field: "effort", value: "high" }]);
        assert.equal(await page.locator(`${row} .field[data-key="effort"] .v`).textContent(), w.high);
        assert(await noSideScroll(page), "the window scrolls sideways");
      });

      await t.test("the panel", async () => {
        const sets = [];
        const page = await open("panel", sets);
        await page.locator(`${row} .ag-sum`).click();
        await page.waitForTimeout(700);
        const box = page.locator(`${row} .ag-open .effort-control`);
        assert.equal(await box.locator(".effort-ticks i").count(), 3);
        assert.equal(await box.locator(".effort-ends span").first().textContent(), w.def);
        await box.locator(".eslide").press("ArrowRight");
        await page.waitForTimeout(300);
        assert.deepEqual(sets, [{ agent: "agy", field: "effort", value: "low" }]);
        assert(await noSideScroll(page), "the panel scrolls sideways");
      });

      assert.deepEqual(errors, []);
    });
  }
}
