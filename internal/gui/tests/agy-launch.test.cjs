// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity CLI (agy) takes magpie's gateway only from its environment, so
// its row on the Agents page has a square that copies the command starting
// it on magpie, once it is connected (right before the model picker beside
// its switch, agy picking no model once started): the command in its tooltip, one click copying it (posted to /api/copy) and saying so, with the
// page left where it was; none on an agent without one; in the tray panel's
// opened row too; the words in Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LAUNCH = "GEMINI_API_KEY=magpie-agy GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425 agy --model 'magpie/deepseek/pro'";
const models = [{ value: "magpie/deepseek/pro", label: "magpie/deepseek/pro", ref: "deepseek/pro" }, { value: "model-b", label: "model-b" }];
const agent = (id, name, launch) => ({
  id, name, path: "/test/" + id, launch, wired: true, // on a magpie model (#726: a connected one is in view)
  fields: [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: models }],
});
const state = {
  agents: [agent("claude", "Claude Code"), ...Array.from({ length: 4 }, (_, i) => agent("agent-" + i, "agent-" + i)),
    agent("agy", "Antigravity CLI", LAUNCH), ...Array.from({ length: 8 }, (_, i) => agent("more-" + i, "more-" + i))],
  profiles: [],
};

function server(lang, copies) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...state, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/copy") { copies.push(req.postDataJSON().text); return route.fulfill({ json: {} }); }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": agy's launch command", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (url, lang, copies, viewport = { width: 980, height: 420 }) => {
      const page = await (await browser.newContext({ viewport })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, copies));
      await page.goto(url);
      await page.locator(row("agy")).waitFor();
      return page;
    };

    await t.test("the window: a square that copies it", async () => {
      const copies = [];
      const page = await open("http://magpie.test/", "en", copies);
      const b = page.locator(`${row("agy")} .field.launch`);
      assert.equal(await b.count(), 1);
      assert.equal(await page.locator(`${row("claude")} .field.launch`).count(), 0, "only an agent with a launch command has one");
      const title = await b.getAttribute("title");
      assert.match(title, /Antigravity CLI takes magpie only from its environment/);
      assert(title.includes(LAUNCH), title);
      // on the row, right before the model it starts on, so the pickers
      // and the switches line up down the list
      const [sq, pick] = await page.evaluate((r) => [".field.launch", ".field.ag-start"].map((s) => document.querySelector(r + " " + s).getBoundingClientRect()), row("agy"));
      assert(sq.right <= pick.left + 1 && pick.left - sq.right <= 12, "the square comes right before the picker");
      assert(Math.abs((sq.top + sq.height / 2) - (pick.top + pick.height / 2)) <= 2, "the square is on the picker's line");
      // scrolled a little, so a scroll would show
      const view = page.locator("#view-agents");
      await page.mouse.move(400, 300);
      for (let i = 0; i < 2; i++) { await page.mouse.wheel(0, 30); await page.waitForTimeout(20); }
      await page.waitForTimeout(300);
      const top = await view.evaluate((v) => v.scrollTop);
      assert(top > 0, "the list must be scrolled");
      const r = await b.evaluate((e) => e.getBoundingClientRect());
      assert(r.top >= 0 && r.bottom <= 420, "the square is in view, so the click needn't scroll to it");
      await b.click();
      await page.waitForTimeout(300);
      assert.deepEqual(copies, [LAUNCH]);
      assert.match(await page.locator("#status").textContent(), /Copied — run it to start Antigravity CLI on magpie/);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the click moved the page");
      assert.equal(await page.locator(`${row("agy")} .picker, .menu.open`).count(), 0, "no picker opens");
    });

    await t.test("the tray panel: in the opened row", async () => {
      const copies = [];
      const page = await open("http://magpie.test/?mode=panel", "en", copies, { width: 440, height: 560 });
      await page.locator(`${row("agy")} .ag-sum`).click();
      await page.waitForTimeout(700);
      const b = page.locator(`${row("agy")} .ag-open .field.launch`);
      assert.equal(await b.count(), 1);
      await b.click();
      await page.waitForTimeout(300);
      assert.deepEqual(copies, [LAUNCH]);
      assert.equal(await page.locator(`${row("agy")}.open`).count(), 1, "the row stays open");
    });

    await t.test("in Chinese", async () => {
      const copies = [];
      const page = await open("http://magpie.test/", "zh", copies);
      const b = page.locator(`${row("agy")} .field.launch`);
      assert.match(await b.getAttribute("title"), /Antigravity CLI 只能从环境变量接入 magpie · 点击复制启动它的命令：/);
      await b.click();
      await page.waitForTimeout(300);
      assert.match(await page.locator("#status").textContent(), /已复制，运行它即可让 Antigravity CLI 走 magpie/);
    });

    assert.deepEqual(errors, []);
  });
}
