// Run with Node's test runner and Playwright on the module path; see README.md.
// Which agents' session history magpie reads is set in Settings › Privacy,
// and the Sessions page it is about didn't lead there once its first ask was
// answered (the scattered settings audit after netube99 on X). Its head now
// has a Settings › that opens Privacy at those rows, lit, with Back to
// Sessions; gateway mode, which reads no agent's history, has none. Every
// language, Chromium and WebKit, at 1100 and 420 wide, nothing past the
// window's edge or over the head's other buttons.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();

function serve(lang, gateway) {
  const settings = { lang, theme: "light", gatewayOn: gateway, sessionReads: { asked: true, all: true }, lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString() } };
  const reads = { asked: true, all: true, found: gateway ? [] : [{ agent: "claude", name: "Claude Code", icon: "claude", on: true }], gateway };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [] });
    if (url.pathname === "/api/sessions/reads") return json(reads);
    if (url.pathname === "/api/sessions/manage") {
      const rows = [{ agent: "claude", id: "s1", cwd: "/srv/app", title: "rename foo to bar", start: at(10), last: at(5), models: [], resume: "", size: 10, messages: 2, files: 0, deletable: true, gateway }];
      return json({ agents: [{ agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claude" }], agent: "claude", sessions: rows, terminal: false, trash: [], trashDir: "", recording: true, reads });
    }
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Sessions leads to Settings › Privacy's session history`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (gateway) => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          page.errors = [];
          page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
          await page.route("**/*", serve(lang, gateway));
          await page.goto("http://magpie.test/?view=sessions");
          await page.locator(".sm-head .sm-trash-btn").waitFor();
          return page;
        };
        const tr = (page, s) => page.evaluate((s) => t(s), s);

        let page = await open(false);
        const go = page.locator(".sm-head .sm-toset");
        await go.waitFor();
        assert.equal((await go.textContent()).trim(), (await tr(page, "Settings")) + " ›");
        assert(await fits(page), "the Sessions page fits");
        const gb = await go.boundingBox(), hb = await page.locator(".sm-head").boundingBox();
        assert(gb.x >= hb.x - 1 && gb.x + gb.width <= hb.x + hb.width + 1, "inside the head");
        for (const sel of [".sm-filter", ".sm-trash-btn"]) {
          const b = await page.locator(".sm-head " + sel).boundingBox();
          assert(!b || b.x + b.width <= gb.x + 0.5 || b.x >= gb.x + gb.width - 0.5 || b.y + b.height <= gb.y + 0.5 || b.y >= gb.y + gb.height - 0.5, sel + " lies over Settings ›");
        }
        await go.click();
        await page.locator("#view-settings #sessionReadsList .row.flash").first().waitFor();
        // the page stays where it is: Settings' held-out lines sat against the
        // window, so reaching the row scrolled the whole page and cut its top
        // off (kalicyh on X)
        await page.waitForTimeout(600);
        const place = await page.evaluate(() => ({ doc: document.scrollingElement.scrollTop, top: document.querySelector("#view-settings").getBoundingClientRect().top, tall: document.documentElement.scrollHeight - innerHeight }));
        assert.equal(place.doc, 0, "the window itself is not scrolled");
        assert(place.top >= 0, "Settings starts inside the window");
        assert(place.tall <= 1, "nothing of Settings runs past the window");
        const back = page.locator("#navBack");
        await back.waitFor();
        await back.click();
        await page.locator("#view-sessions .sm-head").waitFor();
        assert.deepEqual(page.errors, []);

        // gateway mode reads no agent's history: no link
        page = await open(true);
        assert.equal(await page.locator(".sm-head .sm-toset").isVisible(), false, "no Settings › in gateway mode");
        assert.deepEqual(page.errors, []);
      });
    }
  }
}
