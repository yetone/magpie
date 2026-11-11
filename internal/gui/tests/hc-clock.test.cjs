// Run with Node's test runner and Playwright on the module path; see README.md.
// The clock the system is set to (boot.js hourCycle) reaches every time the
// GUI shows: an en-US Mac set to 24 hours showed 12 (iXerol on X), and a
// 12-hour system showed the GUI's fixed 24.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(hc) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    const boot = `window.bootPrefs = {lang:"en",theme:"light",web:false${hc ? `,hourCycle:"${hc}"` : ""}};`;
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: boot });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const cases = [
  // the region says 12 hours; the system says 24
  { hc: "h23", locale: "en-US", plain: "15:05", fixed: "15:05" },
  // the region says 24 hours; the system says 12
  { hc: "h12", locale: "en-GB", plain: "3:05", fixed: "3:05" },
  // the system says nothing: the region's own, and the GUI's 24-hour places
  { hc: "", locale: "en-US", plain: "3:05", fixed: "15:05" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const c of cases) {
    test(`${engine} ${c.locale} system ${c.hc || "silent"}: times follow the system's clock`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ locale: c.locale, timezoneId: "UTC", viewport: { width: 1100, height: 800 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(c.hc));
      await page.goto("http://magpie.test/");
      await page.waitForFunction(() => typeof intlLang === "function");
      const out = await page.evaluate(() => {
        const at = new Date(Date.UTC(2026, 9, 12, 15, 5, 7));
        return {
          plain: at.toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit" }),
          fixed: at.toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit", ...hourClock() }),
        };
      });
      if (c.hc === "h12") { assert.match(out.plain, /3:05/); assert.match(out.plain, /pm/i); assert.match(out.fixed, /pm/i); }
      if (c.hc === "h23") { assert.match(out.plain, /^15:05/); assert.match(out.fixed, /^15:05/); }
      if (c.hc === "") { assert.match(out.plain, /3:05.*pm/i); assert.match(out.fixed, /^15:05/); }
      assert.deepEqual(errors, []);
    });
  }
}
