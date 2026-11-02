// Run with Node's test runner and Playwright on the module path; see README.md.
// On a phone (magpie web, 420 wide) an agent's model picker may grow to the
// window's height, more than the 420 it was placed by. Opened upward from
// a row low in the page it ran off the top: its filter and first rows out
// of reach. Upward or down, it now keeps to the room it was placed in and
// its list scrolls. In five languages, chromium and webkit, at 1100 and 420
// wide. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = Array.from({ length: 24 }, (_, i) => ({ value: `p/m-${i}`, label: `Model ${i}`, note: "P · via magpie", group: "P", ref: `p/m-${i}` }));
const state = { agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/settings.json", wired: false,
  fields: [{ key: "model", label: "model", value: "p/m-0", options }] }], profiles: [] };

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...state, settings: { lang, theme: "light", guideHidden: true } } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a model picker keeps to the window", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [1100, 420]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        await t.test(`${lang} at ${width}`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 760 } });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/");
          await page.locator(row).waitFor();
          await page.locator(`${row} .field[data-key="model"]`).first().click();
          await page.locator("#pop:not([hidden]) #list li").first().waitFor();
          await page.waitForTimeout(400); // the picker grows open
          await page.locator("#q").waitFor({ state: "visible" });
          const pop = await page.locator("#pop").boundingBox(), q = await page.locator("#q").boundingBox();
          assert(pop && q, "the picker is open");
          assert(pop.y >= 0 && pop.y + pop.height <= 760 + 0.5, `the picker runs off the window: ${JSON.stringify(pop)}`);
          assert(q.y >= 0, `the filter is out of reach: ${JSON.stringify(q)}`);
          // its list scrolls to the last row, in the window; read in one
          // go, as the list may be drawn again under a slow run
          const box = await page.evaluate(() => {
            const list = document.querySelector("#list");
            list.scrollTop = list.scrollHeight;
            const li = [...list.querySelectorAll("li:not(.group)")].find((l) => l.textContent.includes("Model 23"));
            const r = li?.getBoundingClientRect();
            return r && { y: r.y, height: r.height };
          });
          assert(box, "the last row is drawn");
          assert(box.y >= 0 && box.y + box.height <= 760 + 0.5, JSON.stringify(box));
          await context.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
