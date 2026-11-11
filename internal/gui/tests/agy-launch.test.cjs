// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity CLI (agy) takes magpie's gateway only from its environment, so
// its row on the Agents page has a square that copies the command starting
// it on magpie, once it is connected (right before the model picker beside
// its switch, agy picking no model once started): the command in its tooltip, one click copying it (posted to /api/copy) and saying so, with the
// page left where it was; none on an agent without one; in the tray panel's
// opened row too; the words in Chinese. Once magpie has given the user's
// shells a function agy (typed), the square and the opened row say plain
// agy starts it in a new terminal, and the command is for one open now.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LAUNCH = "GEMINI_API_KEY=magpie-agy GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425 agy --settings '/home/u/.gemini/antigravity-cli/magpie-settings.json' --model 'magpie/deepseek/pro'";
const models = [{ value: "magpie/deepseek/pro", label: "magpie/deepseek/pro", ref: "deepseek/pro" }, { value: "model-b", label: "model-b" }];
const agent = (id, name, launch) => ({
  id, name, path: "/test/" + id, launch, wired: true, // on a magpie model (#726: a connected one is in view)
  fields: [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: models }],
});
let typed = false;
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
    if (url.pathname === "/api/state") {
      const agents = state.agents.map((a) => (typed && a.id === "agy" ? { ...a, typed: "agy" } : a));
      return route.fulfill({ json: { ...state, agents, settings: { lang, theme: "light" } } });
    }
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
      page.setDefaultNavigationTimeout(60000);
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

    await t.test("typed in a new terminal", async () => {
      typed = true;
      try {
        for (const [lang, square, start, now] of [
          ["en", /^Type agy in a new terminal to start Antigravity CLI on magpie · click to copy the command for a terminal open now:\n/, "Start it with", "In a terminal open now"],
          ["zh", /^在新终端输入 agy 即可让 Antigravity CLI 走 magpie · 点击复制在已打开终端中用的命令：\n/, "启动命令", "已打开的终端"],
        ]) {
          const copies = [];
          const page = await open("http://magpie.test/", lang, copies);
          const b = page.locator(`${row("agy")} .field.launch`);
          const title = await b.getAttribute("title");
          assert.match(title, square);
          assert(title.endsWith(LAUNCH), title);
          await b.click();
          await page.waitForTimeout(300);
          assert.deepEqual(copies, [LAUNCH], "the square still copies the command, for a terminal open now");
          // the opened row: agy, then the command
          await page.locator(`${row("agy")} .ag-link`).click();
          await page.locator(`${row("agy")} .ag-kv`).first().waitFor();
          const kvs = await page.evaluate((r) => [...document.querySelectorAll(r + " .ag-kv")].map((k) => [k.querySelector(".ag-k").textContent, k.querySelector(".ag-v").textContent]), row("agy"));
          const i = kvs.findIndex(([k]) => k === start);
          assert(i >= 0, JSON.stringify(kvs));
          assert.equal(kvs[i][1], "agy" + (lang === "en" ? "in a new terminal" : "在新打开的终端中"));
          assert.equal(kvs[i + 1][0], now);
          assert(kvs[i + 1][1].startsWith(LAUNCH), kvs[i + 1][1]);
          await page.close();
        }
      } finally {
        typed = false;
      }
    });

    assert.deepEqual(errors, []);
  });
}
