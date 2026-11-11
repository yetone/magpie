// Run with Node's test runner and Playwright on the module path; see README.md.
// simonzyzy on X: with no provider set up, Claude Code's and Codex's own
// sessions, tokens and speed seemed impossible. magpie reads no agent's
// session files until they are allowed (sessions/reads asked false), and an
// empty Usage page only told people to route an agent through the gateway.
// Now the Overview and the Requests page say the files aren't read yet and
// offer the one button that allows the agents found; once allowed (asked
// true) the note is gone. Chromium and WebKit, every language; API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const found = () => [
  { agent: "claude", name: "Claude Code", icon: "claudecode-color", on: false },
  { agent: "codex", name: "Codex", icon: "codex-color", on: false },
];
const empty = { calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", series: [], agents: [], models: [] };

function serve(lang, store, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, calls: [], groups: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions/reads") {
      if (req.method() === "POST") { posts.push(req.postDataJSON()); store.asked = true; }
      return json({ asked: store.asked, all: false, found: found() });
    }
    if (url.pathname === "/api/speeds") return json([]);
    if (url.pathname === "/api/sessions/manage") {
      const at = new Date(Date.now() - 300e3).toISOString();
      return json({ agents: [{ agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claudecode-color" }], agent: "claude", sessions: [{ agent: "claude", id: "c-1", cwd: "/work/app", title: "a chat", start: at, last: at, models: [], resume: "", path: "/x/c", size: 10, messages: 2, files: 1, deletable: true }], terminal: false, trash: [], trashDir: "", reads: { asked: true, all: true, found: found() } });
    }
    if (url.pathname === "/api/usage/requests") return json({ ...empty, rows: [], offset: 0, total: 0, by: { provider: [], agent: [], model: [] } });
    if (url.pathname === "/api/usage") return json(empty);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/window/")) return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

const ASK = "magpie reads no agent's session files until you allow it, so {names} calls aren't counted here yet.";
const LANGS = [["en", 900], ["zh", 420], ["zh-TW", 420], ["ja", 420], ["de", 390]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of LANGS) {
    test(`${engine} ${lang} ${width}px: an empty Usage page says session files aren't read yet, and allows them`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const store = { asked: false }, posts = [];
      await page.route("**/*", serve(lang, store, posts));
      await page.goto("http://magpie.test/?view=usage");
      const names = "Claude Code, Codex";
      const ask = await page.evaluate(([s, n]) => t(s, { names: n }), [ASK, names.replace(", ", await page.evaluate(() => t(", ")))]);
      if (lang !== "en") assert.notEqual(ask, ASK.replace("{names}", names), "translated");
      // the Overview
      const note = page.locator("#stats .reads-note");
      await note.waitFor();
      assert.equal(await note.locator("p").textContent(), ask);
      // not worded as if a provider were needed
      assert.ok(!/catalog model/i.test(await page.locator("#stats").textContent()));
      const over = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
      assert.equal(over, false, "no sideways scroll");
      // the Requests tab says the same
      await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      await page.locator("#ledWrap .reads-note").waitFor();
      assert.equal(await page.locator("#ledWrap .reads-note p").textContent(), ask);
      assert.equal(posts.length, 0, "nothing allowed before the button");
      await page.locator("#ledWrap .reads-note button").click();
      await page.locator("#ledWrap .reads-note").waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ all: false, agents: { claude: true, codex: true } }]);
      assert.deepEqual(errors, []);
    });
  }
}

const SPEED = "Live output speed is measured on requests that go through magpie's gateway. For agents that don't, each call's tokens and time are in Usage › Requests, read from their session files.";
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of LANGS) {
    test(`${engine} ${lang} ${width}px: Sessions say where the output speed is when none was measured`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, { asked: true }, []));
      await page.goto("http://magpie.test/?view=sessions");
      const note = page.locator("#view-sessions .sm-speed-note");
      await note.waitFor();
      const want = await page.evaluate((s) => t(s), SPEED);
      if (lang !== "en") assert.notEqual(want, SPEED, "translated");
      assert.equal(await note.textContent(), want);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false);
      assert.deepEqual(errors, []);
    });
  }
}
