// Run with Node's test runner and Playwright on the module path; see README.md.
// A model picked for an agent shows on its row at once, while /api/set is
// still out (it answers with the whole state, which took seconds: Claude
// Code's model took 5–10s to show, in the window and the tray panel alike).
// The answer then draws what the config says; a refused pick puts the old
// model back and says why. The page never moves. In the window the picker
// is in the connected agent's opened row. In English and Chinese.
// No backend: the API is faked here, its /api/set held for SLOW ms.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SLOW = 2500;
const models = ["claude-sonnet-5-5", "claude-opus-5-5", "magpie/deepseek/pro", "magpie/kimi/k3"].map((m) => ({ value: m, ref: m.replace(/^magpie\//, ""), label: "Label " + m }));
const agent = (id, name) => ({
  id, name, path: "/test/" + id, wired: true, // on magpie models (#726: a connected one is in view)
  fields: [{ key: "model", label: "model", value: "claude-sonnet-5-5", options: models }],
});
const fresh = () => ({
  // Claude Code down the list, so it is picked with the list scrolled
  agents: [...Array.from({ length: 5 }, (_, i) => agent("agent-" + i, "Agent " + i)), agent("claude", "Claude Code"),
    ...Array.from({ length: 8 }, (_, i) => agent("more-" + i, "More " + i))],
  profiles: [],
});

function server(lang, sets, refuse) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      await new Promise((r) => setTimeout(r, SLOW));
      if (refuse) return route.fulfill({ status: 400, json: { error: "Claude Code's settings.json is not valid JSON" } });
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } }).catch(() => {});
    }
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
  test(engine + ": a picked model shows at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (url, lang, sets, { refuse = false, viewport = { width: 980, height: 420 } } = {}) => {
      const page = await (await browser.newContext({ viewport })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets, refuse));
      await page.goto(url);
      await page.locator(row).waitFor();
      return page;
    };
    // the window: scrolled down to the connected agent's picker, in its row,
    // as a reader would (the page puts back any scroll that isn't
    // the reader's), by at least `least` turns of the wheel
    const toPicker = async (page, field, least = 0) => {
      const view = page.locator("#view-agents");
      await page.mouse.move(400, 300);
      const shown = async () => { const f = await field.boundingBox(), v = await view.boundingBox(); return f.y + f.height + 8 <= v.y + v.height; };
      for (let i = 0; i < least || !(await shown()); i++) { await page.mouse.wheel(0, 30); await page.waitForTimeout(i < least ? 20 : 80); }
      await page.waitForTimeout(300);
    };
    // the picker's row for a model, clicked
    const pickModel = async (page, value) => {
      const li = page.locator("#list li").filter({ hasText: "Label " + value });
      await li.first().click();
      return Date.now();
    };

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the window", async () => {
        const sets = [];
        const page = await open("http://magpie.test/", lang, sets);
        const view = page.locator("#view-agents");
        const field = page.locator(`${row} > .field.ag-start[data-key="model"]`);
        await toPicker(page, field, 2);
        const top = await view.evaluate((v) => v.scrollTop);
        assert(top > 0, "the list must be scrolled");
        await field.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        const at = await pickModel(page, "magpie/deepseek/pro");
        await page.waitForFunction((r) => document.querySelector(r + ' .field[data-key="model"] .v')?.textContent === "Label magpie/deepseek/pro", row, { timeout: 600 });
        assert(Date.now() - at < SLOW, "shown before the answer came");
        assert.equal(sets.length, 1);
        assert.deepEqual(sets[0], { agent: "claude", field: "model", value: "magpie/deepseek/pro" });
        assert.equal(await view.evaluate((v) => v.scrollTop), top, "the pick moved the page");
        // the answer, drawn, keeps it
        await page.waitForTimeout(SLOW + 300);
        assert.equal(await field.locator(".v").textContent(), "Label magpie/deepseek/pro");
        assert.match(await page.locator("#status").textContent(), /Claude Code .*Label magpie\/deepseek\/pro/);
        assert.equal(await view.evaluate((v) => v.scrollTop), top, "the answer moved the page");
      });
    }

    await t.test("the tray panel: in the opened row and its line", async () => {
      const sets = [];
      const page = await open("http://magpie.test/?mode=panel", "en", sets, { viewport: { width: 440, height: 560 } });
      await page.locator(`${row} .ag-sum`).click();
      await page.waitForTimeout(700);
      await page.locator(`${row} .ag-open .field[data-key="model"]`).click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const at = await pickModel(page, "magpie/kimi/k3");
      await page.waitForFunction((r) => document.querySelector(r + " .ag-sum .vt")?.textContent === "Label magpie/kimi/k3", row, { timeout: 600 });
      assert(Date.now() - at < SLOW, "shown before the answer came");
      assert.equal(await page.locator(`${row} .ag-open .field[data-key="model"] .v`).textContent(), "Label magpie/kimi/k3");
      assert.equal(await page.locator(`${row}.open`).count(), 1, "the row stays open");
      await page.waitForTimeout(SLOW + 300);
      assert.equal(await page.locator(`${row} .ag-sum .vt`).textContent(), "Label magpie/kimi/k3");
      assert.equal(sets.length, 1);
    });

    await t.test("a refused pick puts the old model back", async () => {
      const sets = [];
      const page = await open("http://magpie.test/", "en", sets, { refuse: true });
      const field = page.locator(`${row} > .field.ag-start[data-key="model"]`);
      await toPicker(page, field);
      await field.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await pickModel(page, "claude-opus-5-5");
      await page.waitForFunction((r) => document.querySelector(r + ' .field[data-key="model"] .v')?.textContent === "Label claude-opus-5-5", row, { timeout: 600 });
      await page.waitForFunction((r) => document.querySelector(r + ' .field[data-key="model"] .v')?.textContent === "Label claude-sonnet-5-5", row, { timeout: SLOW + 2000 });
      assert.match(await page.locator("#status").textContent(), /not valid JSON/);
    });

    assert.deepEqual(errors, []);
  });
}
