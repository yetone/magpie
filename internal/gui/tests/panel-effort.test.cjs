// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's effort slider shows an agent's effort as it is when that
// isn't one of the levels offered — omp at auto before magpie listed it, an
// agent with none set — rather than as the lowest level, which a touch on
// the slider then wrote over it. A touch where it stands posts nothing; the
// next stop is the lowest level. A level beyond those offered (max, the
// model's going to high) stands in its place, after high, and lights the
// bars in the list and the effort icon full; one between two offered lights
// them as far as its stop. auto, none and the default light no bar, where
// minimal lights one, and none counts for no level: Hermes at low, second
// of five, lights one of three; the window's icon is read in a connected
// agent's opened row. In English and Chinese. No backend: the API
// is faked here. ARTIFACT_DIR gets the slider and the panel's list.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["minimal", "low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
const agent = (id, name, effort, options = levels) => ({
  id, name, path: "/test/" + id, wired: true,
  fields: [{ key: "model", label: "model", value: "anthropic/claude-opus-5-5:max", options: [{ value: "anthropic/claude-opus-5-5:max", ref: "anthropic/claude-opus-5-5:max" }] },
    { key: "effort", label: "thinking", value: effort, options }],
});
const some = (...vs) => vs.map((value) => ({ value }));
const state = { agents: [agent("omp", "omp", "auto"), agent("pi", "Pi", ""),
  agent("codex", "Codex", "max", some("low", "medium", "high")),
  // omp as magpie lists it now, auto first, and one at minimal beside it
  agent("omp-listed", "omp", "auto", [{ value: "auto" }, ...levels]),
  agent("pi-minimal", "Pi", "minimal"),
  agent("kimi", "Kimi", "medium", some("low", "high")),
  agent("grok", "Grok", "none", some("none", "minimal", "low", "medium", "high", "xhigh", "max")),
  agent("hermes", "Hermes", "low", some("none", "minimal", "low", "medium", "high", "xhigh"))], profiles: [] };

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
    // the page, a row opened in the panel with its slider and a click on the
    // slider's first or last stop
    const load = async (lang, sets, url, viewport = { width: 440, height: 560 }) => {
      const page = await (await browser.newContext({ viewport })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      await page.goto(url);
      return page;
    };
    const open = async (lang, sets, id) => {
      const page = await load(lang, sets, "http://magpie.test/?mode=panel");
      await page.locator(`${row(id)} .ag-sum`).click();
      await page.waitForTimeout(700);
      const box = page.locator(`${row(id)} .ag-open .effort-control`);
      const slider = box.locator(".eslide");
      const click = async (end) => {
        const r = await box.locator(".rail").evaluate((e) => e.getBoundingClientRect().toJSON());
        await page.mouse.click(end ? r.right - 1 : r.left + 1, r.top + r.height / 2);
        await page.waitForTimeout(300);
      };
      const shot = async (name) => {
        if (!process.env.ARTIFACT_DIR) return;
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await box.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${name}-panel-effort.png`) });
      };
      return { page, box, slider, first: () => click(false), end: () => click(true), shot };
    };
    const ends = (box) => box.locator(".effort-ends span").allTextContents();

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

    await t.test("max, the levels going to high", async () => {
      const sets = [];
      const { box, slider, end, shot } = await open("en", sets, "codex");
      assert.deepEqual(await ends(box), ["low", "max"], "max stood before low");
      assert.equal(await slider.getAttribute("aria-valuenow"), "3");
      await shot("en");
      await end();
      assert.deepEqual(sets, [], "a touch where it stands wrote a level over max");
      await slider.press("ArrowLeft");
      await slider.page().waitForTimeout(300);
      assert.deepEqual(sets, [{ agent: "codex", field: "effort", value: "high" }]);
    });

    await t.test("as bars", async () => {
      const lit = (page, id) => page.locator(`${row(id)} .field[data-key="effort"] .effort-ic rect[opacity="1"]`).count();
      const ids = ["omp-listed", "pi-minimal", "codex", "kimi", "grok", "hermes"];
      const page = await load("en", [], "http://magpie.test/", { width: 980, height: 900 });
      await page.locator(row("hermes")).waitFor();
      const icons = [];
      for (const id of ids) {
        // Hermes picks no model once started: its pickers stay in the row
        const more = page.locator(`${row(id)} .ag-link`);
        // the one open slides shut before this one opens
        if (await more.count()) { await more.click(); await page.locator(`${row(id)} .ag-exp`).waitFor(); }
        icons.push(await lit(page, id));
      }
      const panel = await load("en", [], "http://magpie.test/?mode=panel");
      await panel.locator(row("hermes")).waitFor();
      const bars = [];
      for (const id of ids) bars.push(Number(await panel.locator(`${row(id)} .ag-sum .eff`).getAttribute("data-l")));
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await panel.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-en-panel-effort-list.png`) });
      }
      // auto, minimal, max past high, medium between low and high, none, and
      // low second of five (none not counted): of four in the icon, of three
      // in the list
      assert.deepEqual({ icons, bars }, { icons: [0, 1, 4, 3, 0, 2], bars: [0, 1, 3, 2, 0, 1] });
    });

    await t.test("in Chinese", async () => {
      const sets = [];
      const { box, first } = await open("zh", sets, "omp");
      assert.equal(await box.locator(".effort-head b").textContent(), "自动");
      await first();
      assert.deepEqual(sets, []);
      const codex = await open("zh", sets, "codex");
      assert.deepEqual(await ends(codex.box), ["低", "最高"]);
      await codex.shot("zh");
    });

    assert.deepEqual(errors, []);
  });
}
