// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions page of an agent whose sessions each ran in a folder of their
// own (an app that runs Codex once per picture left 11,358 folders of one
// session each, and drawing every folder made 114,000 elements: every click
// to the page held it for over a second, and the Library page after it).
// The folders are drawn 100 at a time with a "Show N more folders" that adds
// the next page under them, leaving what is drawn, and where the reader is,
// as it was; a filter and another agent start again at a page, unfolding a
// folder keeps the folders drawn, and a session opened from the Usage page
// is drawn however far down the folders it is. Every language, Chromium and
// WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const N = 250; // two pages of folders and a half
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sessions = (agent) => Array.from({ length: agent === "codex" ? N : 3 }, (_, i) => ({
  agent, id: `${agent}-${i}`, cwd: `/pictures/inputs/${String(i).padStart(4, "0")}`, title: `${agent} picture ${i}`,
  start: at(i + 5), last: at(i + 1), input: 1000, output: 100, cost: 0.1, priced: true,
  models: [{ model: "gpt-5.5", input: 1000, output: 100, cost: 0.1, priced: true }],
  messages: 6, files: 1, size: 4096, deletable: true, resume: `${agent} resume ${agent}-${i}`,
}));
const agents = [{ agent: "codex", name: "Codex", icon: "openai", count: N, deletable: true }, { agent: "claude", name: "Claude Code", icon: "claudecode-color", count: 3, deletable: true }];
const words = {
  en: { more: "Show 100 more folders", rest: "Show 50 more folders", all: `${N} selected` },
  zh: { more: "显示其余 100 个文件夹", rest: "显示其余 50 个文件夹", all: `已选 ${N} 个` },
  "zh-TW": { more: "顯示其餘 100 個資料夾", rest: "顯示其餘 50 個資料夾" },
  ja: { more: "さらに 100 個のフォルダを表示", rest: "さらに 50 個のフォルダを表示" },
  de: { more: "100 weitere Ordner anzeigen", rest: "50 weitere Ordner anzeigen" },
};

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "claude" ? "claude" : "codex";
      return json({ agents, agent, sessions: sessions(agent), trash: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: thousands of folders are drawn a page at a time`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = words[lang];
      for (const width of [1000, 380]) {
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(10000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message + " " + e.stack));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/");
        await page.locator('#nav [data-view="sessions"]').click();
        const view = page.locator("#view-sessions");
        const folders = view.locator(".row.sm-folder");
        const more = view.locator(".sm-more-folders");
        await folders.first().waitFor();
        assert.equal(await folders.count(), 100, "one page of folders is drawn");
        assert.equal(await more.textContent(), w.more);
        const box = await more.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width, `${width}px: Show more folders fits`);

        // the next page goes under the folders drawn; they and the scroll stay
        await page.mouse.move(width / 2, 320);
        const inSight = () => folders.nth(99).evaluate((e) => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; });
        for (let i = 0; i < 300 && !(await inSight()); i++) await page.mouse.wheel(0, 200);
        await page.waitForTimeout(300);
        assert(await inSight(), "the reader's wheel brings the last folder drawn into sight");
        await folders.first().evaluate((e) => { e.kept = true; });
        const y = () => folders.nth(99).evaluate((e) => Math.round(e.getBoundingClientRect().top));
        const before = await y();
        await more.evaluate((e) => e.click());
        assert.equal(await folders.count(), 200);
        assert.equal(await y(), before, "Show more folders doesn't move the folders in sight");
        assert(await folders.first().evaluate((e) => e.kept === true), "the folders drawn are kept, not drawn again");
        assert.equal(await more.textContent(), w.rest);

        // unfolding a folder keeps as many folders drawn
        await folders.nth(150).locator(".fold").evaluate((e) => e.click());
        await view.locator('.row.sm-sess[data-id="codex-150"]').waitFor();
        assert.equal(await folders.count(), 200, "unfolding a folder keeps the folders drawn");
        await more.evaluate((e) => e.click());
        assert.equal(await folders.count(), N);
        assert.equal(await more.count(), 0, "the last page leaves no Show more folders");

        // the bar picks every session, its folder drawn or not
        if (w.all) {
          await page.locator('#nav [data-view="agents"]').click();
          await page.locator('#nav [data-view="sessions"]').click();
          assert.equal(await folders.count(), N, "coming back to the page keeps the folders drawn");
          // back to the top as the reader goes there: a scroll of code's own
          // is put back (WebKit)
          await page.mouse.move(width / 2, 320);
          await page.mouse.wheel(0, -40000);
          await page.waitForFunction(() => document.querySelector("#view-sessions").scrollTop === 0);
          await view.locator(".sm-select").click();
          await view.locator(".sm-bar .sm-check").check();
          assert.equal(await view.locator(".sm-bar .sm-count").textContent(), w.all);
          await view.locator(".sm-bar .sm-check").uncheck();
        }

        // a filter starts again at a page; one that finds a few draws those
        const filter = view.locator(".sm-filter");
        await filter.fill("picture");
        await filter.press("Enter"); // typing waits for a pause; Enter filters at once
        assert.equal(await folders.count(), 100, "a filter starts again at a page of folders");
        await filter.fill("picture 249");
        await filter.press("Enter");
        assert.equal(await folders.count(), 1);
        assert.equal(await more.count(), 0);
        await filter.fill("");
        await filter.press("Enter");
        assert.equal(await folders.count(), 100);

        // another agent starts at a page; a session opened from the Usage
        // page, far down the folders, is drawn
        await page.evaluate(() => magpie.call("sessions", "reveal", { agent: "claude" }));
        await view.locator('.row.sm-sess[data-id^="claude"]').first().waitFor();
        assert.equal(await folders.count(), 3);
        await page.evaluate(() => magpie.call("sessions", "reveal", { agent: "codex", id: "codex-230", cwd: "/pictures/inputs/0230" }));
        await view.locator('.sm-item.open .row.sm-sess[data-id="codex-230"]').waitFor();
        assert.equal(await folders.count(), 231, "drawn down to the folder of the session opened");
        assert(await view.locator(".row.sm-sess").count() <= 1, "only that folder is unfolded");

        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-folder-paging.png`) });
        }
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
