// Run with Node's test runner and Playwright on the module path; see README.md.
// jzing.draw on Discord: agents on two other computers of the network, served
// through this one's gateway, drew as one "DeepSeek Harness" node, and which
// computer asked was nowhere. A request from another computer is that
// computer's agent: its own node, with the computer on the node's small line
// (its name when the gateway has learnt it, else its address) and its
// address under the pointer; a request made here is drawn as before. The
// request rows say it under the pointer, in the node's colour, and the tray
// panel as its stage's small line and its rows' names. In every language, at 1100
// and 420; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "ds", provider: "deepseek", name: "DeepSeek", who: "key", kind: "key", model: "deepseek-v4" };
  // under way, so each stays on the stage
  const req = (id, client, clientName) => ({ id, seq: id, time: at(5 - id), agent: "dsh",
    ...(client ? { client } : {}), ...(clientName ? { clientName } : {}),
    model: "deepseek-v4", provider: "deepseek", order: [seat], tries: [{ id: seat.id, model: "deepseek-v4", start: at(4), done: false }],
    done: false, status: 0 });
  return [
    req(1, "192.168.1.20", "studio"),
    req(2, "192.168.1.21", ""), // no name learnt: its address
    req(3, "", ""), // made on this computer
  ];
}

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "dsh", name: "DeepSeek Harness", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait") || Number(url.searchParams.get("after"))) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 3, totals: { requests: 3, rerouted: 0, errors: 0 }, routes: fixture() });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/caller-keys") return json({ keys: [], accountNames: {} });
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
      test(`${engine} ${lang} ${width}: an agent on another computer is drawn as that computer's`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        await page.waitForFunction(() => document.querySelectorAll(".rt-src").length === 3);
        const nodes = await page.locator(".rt-src").evaluateAll((es) => es.map((e) => ({
          name: e.querySelector("b").textContent, sub: e.querySelector("small").textContent, title: e.title,
          hue: e.style.getPropertyValue("--agent").trim(),
        })));
        assert.deepEqual(nodes.map((n) => n.name), ["DeepSeek Harness", "DeepSeek Harness", "DeepSeek Harness"], JSON.stringify(nodes));
        assert.deepEqual(nodes.map((n) => n.sub).sort(), ["192.168.1.21", "deepseek-v4", "studio"], JSON.stringify(nodes));
        const of = (sub) => nodes.find((n) => n.sub === sub);
        // the name's computer by its address under the pointer; the
        // address alone isn't said twice; this computer's as before
        assert.match(of("studio").title, /^DeepSeek Harness · studio\n192\.168\.1\.20\n/);
        assert.match(of("192.168.1.21").title, /^DeepSeek Harness · 192\.168\.1\.21\ndeepseek-v4/);
        assert.match(of("deepseek-v4").title, /^DeepSeek Harness\ndeepseek-v4/);
        assert.equal(new Set(nodes.map((n) => n.hue)).size, 3, "each computer's agent its own colour: " + nodes.map((n) => n.hue));
        // the computer isn't cut off a narrow node
        const whole = await page.locator(".rt-src small").evaluateAll((es) => es.every((e) => e.scrollWidth <= e.clientWidth + 1));
        assert(whole, "each small line whole");
        // each row in its computer's agent's colour, the computer under
        // the pointer
        await page.locator(".rt-req").nth(2).waitFor();
        const rows = await page.locator(".rt-req").evaluateAll((es) => es.map((e) => ({
          title: e.title, hue: e.querySelector(".asked .ag").style.getPropertyValue("--agent").trim(),
        })));
        const row = (re) => rows.filter((r) => re.test(r.title));
        assert.equal(row(/: studio \(192\.168\.1\.20\)$/m).length, 1, JSON.stringify(rows));
        assert.equal(row(/: 192\.168\.1\.21$/m).length, 1, JSON.stringify(rows));
        assert.equal(row(/192\.168/).length, 2, JSON.stringify(rows));
        assert.equal(row(/studio/)[0].hue, of("studio").hue);
        assert.equal(row(/1\.21/)[0].hue, of("192.168.1.21").hue);
        assert.equal(rows.find((r) => !/192\.168/.test(r.title)).hue, of("deepseek-v4").hue);
        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-lan-client.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: the tray panel draws each computer's agent apart`, async (t) => {
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
    await page.locator("#panelRouting .pr-req").nth(2).waitFor();
    const who = await page.locator("#panelRouting .pr-who").allTextContents();
    assert.deepEqual(who.slice().sort(), ["DeepSeek Harness", "DeepSeek Harness · 192.168.1.21", "DeepSeek Harness · studio"], JSON.stringify(who));
    await page.waitForFunction(() => document.querySelectorAll("#panelRouting .ps-ag").length === 3);
    const on = await page.locator("#panelRouting .ps-ag .ps-name").evaluateAll((es) => es.map((e) => e.querySelector("small")?.textContent || ""));
    assert.deepEqual(on.slice().sort(), ["", "192.168.1.21", "studio"], JSON.stringify(on));
    assert.deepEqual(errors, []);
  });
}
