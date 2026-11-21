// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions page with a history of thousands (ChildhoodAndy on X: switching
// to it, or to another agent, and scrolling it stuttered). An open folder draws
// its latest 100 sessions and a "Show 100 more" that adds the next page under
// them, leaving what is drawn, and where the reader is, as it was; a filter and
// another agent start again at a page. Opening a session draws that session
// again, not the list. A folder's box and the bar's still pick every session,
// drawn or not, and a session opened from the Usage page is drawn however far
// down its folder it is. English and Chinese, Chromium and WebKit, wide and
// narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const N = 250; // two pages and a half in the big folder
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sessions = (agent) => Array.from({ length: agent === "claude" ? N + 3 : 140 }, (_, i) => ({
  agent, id: `${agent}-${i}`, cwd: i < N || agent !== "claude" ? "/work/big" : "/work/small", title: `${agent} task ${i}`,
  start: at(i + 5), last: at(i + 1), input: 1000, output: 100, cost: 0.1, priced: true,
  models: [{ model: "claude-sonnet-5", input: 1000, output: 100, cost: 0.1, priced: true }],
  messages: 6, files: 1, size: 4096, deletable: true, resume: `${agent} --resume ${agent}-${i}`,
}));
const agents = [{ agent: "claude", name: "Claude Code", icon: "claudecode-color", count: N + 3, deletable: true }, { agent: "codex", name: "Codex", icon: "openai", count: 140, deletable: true }];
const words = {
  en: { more: "Show 100 more", rest: "Show 50 more", picked: `${N} selected`, all: `${N + 3} selected` },
  zh: { more: "显示其余 100 个", rest: "显示其余 50 个", picked: `已选 ${N} 个`, all: `已选 ${N + 3} 个` },
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
      const agent = url.searchParams.get("agent") === "codex" ? "codex" : "claude";
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
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a big folder of sessions is drawn a page at a time`, async (t) => {
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
        const rows = view.locator(".row.sm-sess");
        const more = view.locator(".sm-more");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 100, "the open folder draws one page");
        assert.equal(await more.textContent(), w.more);
        const box = await more.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width, `${width}px: Show more fits`);

        // the next page goes under the rows drawn; they and the scroll stay
        // the reader scrolls there: a scroll by code (scrollIntoViewIfNeeded)
        // is put back at the next frame, and a click before that frame now
        // puts it back too (#1554)
        await page.mouse.move(width / 2, 320);
        const inSight = () => rows.nth(99).evaluate((e) => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; });
        for (let i = 0; i < 300 && !(await inSight()); i++) await page.mouse.wheel(0, 200);
        await page.waitForTimeout(300);
        assert(await inSight(), "the reader's wheel brings the last row drawn into sight");
        await rows.first().evaluate((e) => { e.kept = true; });
        const y = () => rows.nth(99).evaluate((e) => Math.round(e.getBoundingClientRect().top));
        const before = await y();
        // clicked where it is, as the reader does, without scrolling to it
        await more.evaluate((e) => e.click());
        assert.equal(await rows.count(), 200);
        assert.equal(await y(), before, "Show more doesn't move the rows in sight");
        assert(await rows.first().evaluate((e) => e.kept === true), "the rows drawn are kept, not drawn again");
        assert.equal(await more.textContent(), w.rest);
        await more.evaluate((e) => e.click());
        assert.equal(await rows.count(), N);
        assert.equal(await more.count(), 0, "the last page leaves no Show more");

        // opening a session draws it alone
        // back to the top as the reader goes there: a scroll of code's own
        // is put back (WebKit)
        await page.mouse.move(width / 2, 320);
        await page.mouse.wheel(0, -40000);
        await page.waitForFunction(() => document.querySelector("#view-sessions").scrollTop === 0);
        await rows.nth(5).click();
        await view.locator(".sm-item.open .sess-detail").waitFor();
        assert(await rows.first().evaluate((e) => e.kept === true), "opening a session leaves the other rows");
        assert.equal(await view.locator(".sm-item.open .row.sm-sess").getAttribute("data-id"), "claude-5");
        await rows.nth(7).evaluate((e) => e.click()); // under the details, out of sight in a short window
        assert.equal(await view.locator(".sm-item.open").count(), 1, "the one opened before closes");
        assert.equal(await view.locator(".sm-item.open .row.sm-sess").getAttribute("data-id"), "claude-7");
        await view.locator('.row.sm-sess[data-id="claude-7"]').evaluate((e) => e.click());
        assert.equal(await view.locator(".sm-item.open").count(), 0);

        // a filter starts again at a page; clearing it keeps a page
        const filter = view.locator(".sm-filter");
        await filter.fill("task");
        await filter.press("Enter"); // typing waits for a pause; Enter filters at once
        assert.equal(await rows.count(), 100 + 3, "every folder opens to a page while filtering");
        await filter.fill("task 249");
        await filter.press("Enter");
        assert.equal(await rows.count(), 1);
        await filter.fill("");
        await filter.press("Enter");
        assert.equal(await rows.count(), 100);

        // the folder's box and the bar pick every session, drawn or not
        await view.locator(".sm-select").click();
        await view.locator(".sm-folder-check").first().check();
        assert.equal(await view.locator(".sm-bar .sm-count").textContent(), w.picked);
        await view.locator(".sm-bar .sm-check").check();
        assert.equal(await view.locator(".sm-bar .sm-count").textContent(), w.all);
        await view.locator(".sm-bar .sm-check").uncheck();

        // another agent starts at a page, and so does coming back
        await page.evaluate(() => magpie.call("sessions", "reveal", { agent: "codex" }));
        await view.locator('.row.sm-sess[data-id^="codex"]').first().waitFor();
        assert.equal(await rows.count(), 100);
        // a session opened from the Usage page, far down its folder, is drawn
        await page.evaluate(() => magpie.call("sessions", "reveal", { agent: "claude", id: "claude-180", cwd: "/work/big" }));
        await view.locator('.sm-item.open .row.sm-sess[data-id="claude-180"]').waitFor();
        assert.equal(await rows.count(), 181);
        assert.equal(await more.textContent(), lang === "en" ? "Show 69 more" : "显示其余 69 个");

        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.mouse.wheel(0, 40000);
          await page.waitForTimeout(300);
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-manage-paging.png`) });
        }
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}
