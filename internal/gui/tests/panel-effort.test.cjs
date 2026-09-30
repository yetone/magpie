// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's effort slider shows an agent's effort as it is when that
// isn't one of the levels offered — omp at auto before magpie listed it, an
// agent with none set — rather than as the lowest level, which a touch on
// the slider then wrote over it. A touch where it stands posts nothing; the
// next stop is the lowest level. In English and Chinese. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["minimal", "low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
const agent = (id, name, effort) => ({
  id, name, path: "/test/" + id,
  fields: [{ key: "model", label: "model", value: "anthropic/claude-opus-5-5:max", options: [] },
    { key: "effort", label: "thinking", value: effort, options: levels }],
});
const state = { agents: [agent("omp", "omp", "auto"), agent("pi", "Pi", "")], profiles: [] };

function server(lang, sets) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...state, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") { sets.push(req.postDataJSON()); return route.fulfill({ json: { ...state, settings: { lang, theme: "light" } } }); }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the panel's effort as it is", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    // the row opened, its slider, and a click on the slider's first stop
    const open = async (lang, sets, id) => {
      const page = await (await browser.newContext({ viewport: { width: 440, height: 560 } })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator(`${row(id)} .ag-sum`).click();
      await page.waitForTimeout(700);
      const box = page.locator(`${row(id)} .ag-open .effort-control`);
      const slider = box.locator(".eslide");
      const first = async () => {
        const r = await box.locator(".rail").evaluate((e) => e.getBoundingClientRect().toJSON());
        await page.mouse.click(r.left + 1, r.top + r.height / 2);
        await page.waitForTimeout(300);
      };
      return { page, box, slider, first };
    };

    await t.test("omp at auto", async () => {
      const sets = [];
      const { box, slider, first } = await open("en", sets, "omp");
      assert.equal(await box.locator(".effort-head b").textContent(), "auto");
      assert.equal(await slider.getAttribute("aria-valuetext"), "auto");
      assert.equal(await box.locator(".effort-ends span").first().textContent(), "auto");
      await first();
      assert.deepEqual(sets, [], "a touch where it stands wrote a level over auto");
      await slider.press("ArrowRight");
      await slider.page().waitForTimeout(300);
      assert.deepEqual(sets, [{ agent: "omp", field: "effort", value: "minimal" }]);
    });

    await t.test("none set", async () => {
      const sets = [];
      const { box, first } = await open("en", sets, "pi");
      assert.equal(await box.locator(".effort-head b").textContent(), "default");
      await first();
      assert.deepEqual(sets, []);
    });

    await t.test("in Chinese", async () => {
      const sets = [];
      const { box, first } = await open("zh", sets, "omp");
      assert.equal(await box.locator(".effort-head b").textContent(), "自动");
      await first();
      assert.deepEqual(sets, []);
    });

    assert.deepEqual(errors, []);
  });
}
