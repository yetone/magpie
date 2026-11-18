// Run with Node's test runner and Playwright on the module path; see README.md.
// A request from an agent magpie knows, sent with a named gateway key, is
// drawn with the agent's icon and reads "agent · key" (Yayoi_no_yume on X:
// DimCode on another computer, calling a Docker magpie by a key named
// "Magpie", read as the cube and "Magpie" alone). A script's key keeps the
// key's name alone, as before (acezan). The request's story names the
// agent, and the tray panel's stage and rows read the same. In every
// language, at 1100 and 420; the API is faked, and the icon is the file
// the web build serves.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "sf", provider: "stepfun-cn", name: "StepFun (China)", who: "k1", kind: "key", model: "step-5-preview" };
  const req = (id, agent, key) => ({ id, seq: id, time: at(5 - id), agent,
    ...(key ? { callerKey: key.id, callerKeyName: key.name } : {}),
    model: "auto/step-5-preview", provider: "stepfun-cn", order: [seat], tries: [{ id: seat.id, model: "step-5-preview", start: at(4), done: false }],
    group: { name: "Step 5 Preview", members: [] },
    done: false, status: 0 });
  return [
    req(1, "dimcode", { id: "k-magpie", name: "Magpie" }),
    req(2, "Python-urllib", { id: "k-nightly", name: "nightly" }),
  ];
}
const keys = [{ id: "k-magpie", name: "Magpie" }, { id: "k-nightly", name: "nightly" }];
const clients = [{ id: "claude", name: "Claude Code", icon: "claude-color" }, { id: "dimcode", name: "DimCode", icon: "dim-color" }];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], clients, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait") || Number(url.searchParams.get("after"))) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 2, totals: { requests: 2, rerouted: 0, errors: 0 }, routes: fixture() });
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
      test(`${engine} ${lang} ${width}: an agent's request with a key reads as the agent and the key`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        await page.waitForFunction(() => document.querySelectorAll(".rt-src").length === 2);
        // the icon is drawn once its file has loaded
        await page.waitForFunction(() => [...document.querySelectorAll(".rt-src .ic img")].some((i) => i.complete && i.naturalWidth > 0));
        const nodes = await page.locator(".rt-src").evaluateAll((es) => es.map((e) => ({
          name: e.querySelector("b").textContent, title: e.title, icon: e.querySelector(".ic")?.dataset.icon,
          loaded: [...e.querySelectorAll(".ic img")].some((i) => i.naturalWidth > 0),
        })));
        const dim = nodes.find((n) => n.icon === "dim-color");
        assert(dim, "DimCode's node has its icon: " + JSON.stringify(nodes));
        assert(dim.loaded, "dim-color.svg is served");
        assert.equal(dim.name, "DimCode · Magpie");
        assert.match(dim.title, /^DimCode · Magpie\nauto\/step-5-preview/, "the agent once under the pointer");
        const script = nodes.find((n) => n !== dim);
        assert.equal(script.name, "nightly", "a script's key alone");
        assert.equal(script.icon, "generic");
        assert.match(script.title, /^nightly\nPython-urllib\n/);
        // the story names the agent
        await page.locator(".rt-req").first().click();
        await page.waitForFunction(() => /\bDimCode\b/.test(document.body.innerText));
        const body = await page.evaluate(() => document.body.innerText);
        assert.doesNotMatch(body, /DimAgent/);
        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        const inside = await page.locator(".rt-src").evaluateAll((es) => es.every((e) => {
          const b = e.querySelector("b").getBoundingClientRect(), n = e.getBoundingClientRect();
          return b.right <= n.right + 1 && b.left >= n.left - 1;
        }));
        assert(inside, "each name inside its node");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-agent-key.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: the tray panel reads an agent's keyed request as the agent and the key`, async (t) => {
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
    await page.locator("#panelRouting .pr-req").nth(1).waitFor();
    const who = await page.locator("#panelRouting .pr-who").allTextContents();
    assert.deepEqual(who.slice().sort(), ["DimCode · Magpie", "nightly"].sort(), JSON.stringify(who));
    await page.waitForFunction(() => document.querySelectorAll("#panelRouting .ps-ag").length === 2);
    const ag = await page.locator("#panelRouting .ps-ag").evaluateAll((es) => es.map((e) => [e.querySelector(".ps-name").textContent, e.querySelector(".ic")?.dataset.icon]));
    assert.deepEqual(ag.slice().sort(), [["DimCode · Magpie", "dim-color"], ["nightly", "generic"]].sort(), JSON.stringify(ag));
    assert.deepEqual(errors, []);
  });
}
