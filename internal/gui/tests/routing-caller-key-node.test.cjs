// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing stage names who sent a request by the gateway key it came
// with (acezan on Discord: a script's requests through an Antigravity group
// read "Python-urllib", its User-Agent, where the key's name says whose
// they are). A request with a named key is drawn as that key, by the name
// Gateway keys has for it now, else the name the route kept; two keys whose
// scripts say one User-Agent are two callers, each its own colour; a
// request with no key keeps its agent's name. The agent stays under the
// pointer. The tray panel's stage and rows name it the same way. In every
// language, at 1100 and 420; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "agy", provider: "agy", name: "Antigravity", who: "me@example.com", kind: "account", model: "gemini-3-pro" };
  // under way, so each stays on the stage
  const req = (id, agent, key) => ({ id, seq: id, time: at(5 - id), agent,
    ...(key ? { callerKey: key.id, callerKeyName: key.name } : {}),
    model: "group/agy", provider: "agy", order: [seat], tries: [{ id: seat.id, model: "gemini-3-pro", start: at(4), done: false }],
    done: false, status: 0 });
  return [
    req(1, "Python-urllib", { id: "k-nightly", name: "爬虫 nightly" }),
    req(2, "Python-urllib", { id: "k-server", name: "Old box" }), // renamed since: Server
    req(3, "Python-urllib", null), // no key: its User-Agent
    req(4, "claude", null),
  ];
}
// the keys as Gateway keys lists them now
const keys = [{ id: "k-nightly", name: "爬虫 nightly" }, { id: "k-server", name: "Server" }];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait") || Number(url.searchParams.get("after"))) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 4, totals: { requests: 4, rerouted: 0, errors: 0 }, routes: fixture() });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/caller-keys") return json({ keys, accountNames: {} });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/window/")) return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: the stage names a request's caller by its gateway key`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        // Server is the name /api/caller-keys gives once it is read
        await page.waitForFunction(() => [...document.querySelectorAll(".rt-src b")].some((b) => b.textContent === "Server"));
        const nodes = await page.locator(".rt-src").evaluateAll((es) => es.map((e) => ({
          name: e.querySelector("b").textContent, title: e.title, hue: e.style.getPropertyValue("--agent").trim(),
        })));
        assert.deepEqual(nodes.map((n) => n.name).sort(), ["Claude Code", "Python-urllib", "Server", "爬虫 nightly"].sort(), JSON.stringify(nodes));
        const of = (name) => nodes.find((n) => n.name === name);
        // the agent behind a key stays under the pointer
        assert.match(of("爬虫 nightly").title, /^爬虫 nightly\nPython-urllib\n/);
        assert.match(of("Server").title, /^Server\nPython-urllib\n/);
        assert.match(of("Python-urllib").title, /^Python-urllib\n/);
        assert.doesNotMatch(of("Python-urllib").title, /^Python-urllib\nPython-urllib/, "no key: the agent once");
        assert.equal(new Set(nodes.map((n) => n.hue)).size, 4, "each caller its own colour: " + nodes.map((n) => n.hue));
        // a key's request row is in its node's colour
        const rowHue = await page.locator(".rt-req").evaluateAll((es) => es.map((e) => e.querySelector(".asked .ag").style.getPropertyValue("--agent").trim()));
        assert.equal(new Set(rowHue).size, 4, "rows: " + rowHue);
        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        const inside = await page.locator(".rt-src").evaluateAll((es) => es.every((e) => {
          const b = e.querySelector("b").getBoundingClientRect(), n = e.getBoundingClientRect();
          return b.right <= n.right + 1 && b.left >= n.left - 1;
        }));
        assert(inside, "each name inside its node");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-caller-key.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: the tray panel names a request's caller by its gateway key`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 360, height: 560 }, reducedMotion: "no-preference" });
    await ctx.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "routing"); } catch {} });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("en"));
    await page.goto("http://magpie.test/?mode=panel");
    await page.locator("#panelRouting .pr-req").nth(3).waitFor();
    const who = await page.locator("#panelRouting .pr-who").allTextContents();
    // the panel reads no key list: a key by the name its route kept
    assert.deepEqual(who.slice().sort(), ["Claude Code", "Old box", "Python-urllib", "爬虫 nightly"].sort(), JSON.stringify(who));
    await page.waitForFunction(() => document.querySelectorAll("#panelRouting .ps-ag").length === 3);
    const names = await page.locator("#panelRouting .ps-ag .ps-name").allTextContents();
    // three a side: the newest three callers, each its own
    assert.equal(new Set(names).size, 3, JSON.stringify(names));
    for (const n of names) assert(["Claude Code", "Old box", "Python-urllib", "爬虫 nightly"].includes(n), n);
    assert.deepEqual(errors, []);
  });
}
