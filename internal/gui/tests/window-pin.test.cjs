// Run with Node's test runner and Playwright on the module path; see README.md.
// NovaStudiosss on X: a pin at the top right of the main window keeps it
// above other apps' windows, to watch Usage and Routing while working
// elsewhere. The pin (#pin, app.js renderOnTop) shows only where boot.js
// says the window can be pinned (bootPrefs.pin: the app's own window, not
// a browser page, not Wayland), never in the panel. A click posts
// window/ontop?on=1 or ?on=0 and the pin is pressed (.on, aria-pressed)
// with its tooltip saying what the next click does; a refused post puts
// it back. bootPrefs.onTop has it pressed from the first paint. In every
// language and both engines; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const keys = ["Keep on top", "Stop keeping on top"];

// until waits for f to hold (the posts the page makes), at most 5s
const until = async (f) => { for (let i = 0; i < 250 && !f(); i++) await new Promise((r) => setTimeout(r, 20)); };

function server(lang, posts, o) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: !!o.web, ...o.boot })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (req.method() === "POST" && url.pathname === "/api/window/ontop") {
      posts.push(url.pathname + url.search);
      if (o.refuse) return json({ error: "this window can't stay on top here" }, 400);
      return json({ onTop: url.searchParams.get("on") === "1" });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const open = async (t, o, url = "/", width = 980) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, o));
      await page.goto("http://magpie.test" + url);
      await page.locator("#status").waitFor({ state: "attached" });
      const tr = (k) => page.evaluate((k) => t(k), k);
      t.after(() => assert.deepEqual(errors, []));
      return { page, posts, tr };
    };

    test(`${engine} ${lang}: the pin keeps the window on top, and lets it go`, async (t) => {
      const { page, posts, tr } = await open(t, { boot: { pin: true, onTop: false } });
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang][k]), [lang, keys]);
        assert.deepEqual(missing, [], "every string has its " + lang);
      }
      const pin = page.locator("header .actions #pin");
      await pin.waitFor();
      assert.equal(await pin.getAttribute("aria-pressed"), "false");
      assert.equal(await pin.getAttribute("title"), await tr("Keep on top"));
      assert.ok(!(await pin.evaluate((b) => b.classList.contains("on"))));
      // as big as the toolbar's other icons
      const [a, b] = await Promise.all([pin.boundingBox(), page.locator("header .actions #sync").boundingBox()]);
      assert.ok(Math.abs(a.width - b.width) < 1 && Math.abs(a.height - b.height) < 1, `pin ${a.width}x${a.height}, sync ${b.width}x${b.height}`);

      await pin.click();
      await until(() => posts.length === 1);
      assert.deepEqual(posts, ["/api/window/ontop?on=1"]);
      assert.equal(await pin.getAttribute("aria-pressed"), "true");
      assert.ok(await pin.evaluate((b) => b.classList.contains("on")), "pressed");
      assert.equal(await pin.getAttribute("title"), await tr("Stop keeping on top"));

      await pin.click();
      await until(() => posts.length === 2);
      assert.deepEqual(posts, ["/api/window/ontop?on=1", "/api/window/ontop?on=0"]);
      assert.equal(await pin.getAttribute("aria-pressed"), "false");
      assert.equal(await pin.getAttribute("title"), await tr("Keep on top"));
    });

    test(`${engine} ${lang}: a pinned window shows its pin pressed from the start`, async (t) => {
      const { page, tr } = await open(t, { boot: { pin: true, onTop: true } });
      const pin = page.locator("#pin");
      await pin.waitFor();
      assert.equal(await pin.getAttribute("aria-pressed"), "true");
      assert.equal(await pin.getAttribute("title"), await tr("Stop keeping on top"));
    });

    test(`${engine} ${lang}: a refused pin goes back up`, async (t) => {
      const { page, posts } = await open(t, { boot: { pin: true, onTop: false }, refuse: true });
      const pin = page.locator("#pin");
      await pin.click();
      await until(() => posts.length === 1);
      await page.waitForFunction(() => document.querySelector("#pin").getAttribute("aria-pressed") === "false");
      assert.ok(!(await pin.evaluate((b) => b.classList.contains("on"))));
    });
  }

  test(`${engine}: no pin where the window can't be pinned, nor in the panel or a browser`, async (t) => {
    for (const [o, url, why] of [
      [{ boot: {} }, "/", "the server didn't offer one (Wayland)"],
      [{ boot: { pin: true } }, "/?mode=panel", "the panel"],
      [{ boot: { pin: true }, web: true }, "/", "a browser page"],
    ]) {
      const { page } = await open0(engine, t, o, url);
      assert.equal(await page.locator("#pin").count(), 0, why);
    }
  });
}

// open0 is open for a test outside the language loop, in English
async function open0(engine, t, o, url) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width: 980, height: 560 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server("en", [], o));
  await page.goto("http://magpie.test" + url);
  await page.locator("#status").waitFor({ state: "attached" });
  t.after(() => assert.deepEqual(errors, []));
  return { page };
}
