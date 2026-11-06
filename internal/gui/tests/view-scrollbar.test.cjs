// Run with Node's test runner and Playwright on the module path; see README.md.
// A page that runs longer than the window shows a scrollbar (PAMI on
// Discord: no page of the Mac app had one). The pages' scrollbar was
// hidden everywhere; now on the Mac (body.mac) a page has a thin thumb of
// 8px, across a page as at its side, and elsewhere the 10px one every
// other list has. In WebKit, which
// the Mac app is, and Chromium.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve() {
  const models = Array.from({ length: 40 }, (_, i) => ({ id: `a/m${i}`, name: `m${i}`, providerName: "A", icon: "generic" }));
  const groups = Array.from({ length: 40 }, (_, i) => ({ id: `g${i}`, name: `Group ${i}`, members: [`a/m${i}`], routing: "order", ready: true, memberInfo: [{ id: `a/m${i}`, ready: true }] }));
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], groups });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a long page shows a scrollbar, on the Mac too`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
    const page = await (await browser.newContext({ viewport: { width: 900, height: 500 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve());
    await page.goto("http://magpie.test/?view=groups");
    await page.locator(".rt-group").nth(30).waitFor();
    const bar = () => page.evaluate(() => {
      const v = [...document.querySelectorAll(".view")].find((x) => x.offsetParent && x.scrollHeight > x.clientHeight);
      return v ? v.offsetWidth - v.clientWidth - parseFloat(getComputedStyle(v).borderLeftWidth) - parseFloat(getComputedStyle(v).borderRightWidth) : -1;
    });
    assert.equal(await bar(), 10, "a long page has no scrollbar off the Mac");
    // the Mac app's body is .mac from the start; WebKit keeps a scrollbar it
    // has drawn, so the page's is drawn anew
    await page.evaluate(() => {
      document.body.classList.add("mac");
      for (const v of document.querySelectorAll(".view")) { v.style.display = "none"; v.offsetWidth; v.style.display = ""; }
    });
    assert.equal(await bar(), 8, "a long page has no scrollbar on the Mac");
    // a page that does run wider than the window has as thin a bar across
    // its foot (#1623, fanbuz: Usage's was the engine's 15-17px one, twice
    // the side's, as only the side's width was set)
    const foot = await page.evaluate(() => {
      const v = document.querySelector("#view-groups"), wide = document.createElement("div");
      wide.style.cssText = "width: 3000px; height: 1px";
      v.append(wide);
      v.style.display = "none"; v.offsetWidth; v.style.display = "";
      const b = getComputedStyle(v);
      const h = v.offsetHeight - v.clientHeight - parseFloat(b.borderTopWidth) - parseFloat(b.borderBottomWidth);
      wide.remove();
      return h;
    });
    assert.equal(foot, 8, "a wide page's bar across its foot is as thin as the one at its side");
    const thumb = await page.evaluate(() => getComputedStyle(document.querySelector("#view-groups"), "::-webkit-scrollbar-thumb").backgroundColor);
    assert.notEqual(thumb, "rgba(0, 0, 0, 0)", "the thumb is drawn in no colour");
    assert.deepEqual(errors, []);
  });
}
