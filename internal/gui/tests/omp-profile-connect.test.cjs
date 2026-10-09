// Run with Node's test runner and Playwright on the module path; see README.md.
// An omp named profile is omp#<name>. Left raw, the browser reads #work as
// the fragment and the connect reaches the default omp row (#1187). The
// switch must post /api/agents/connect/omp%23work. Encoding the whole path
// would also encode ? = &, so a query on the same page must stay a query.
// Without the # encoding this fails. English; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "gpt-5.4", label: "GPT-5.4" }, { value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const fresh = () => ({
  agents: [
    { id: "omp", name: "omp", icon: "generic", path: "/fixture/omp", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] },
    { id: "omp#work", name: "omp · work", icon: "generic", path: "/fixture/omp/profiles/work", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] },
  ],
  profiles: [],
});

function server(posts) {
  let cur = fresh();
  const reply = (route) => route.fulfill({ json: { ...cur, settings: { lang: "en", theme: "light" } } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return reply(route);
    if (url.pathname === "/api/usage" && url.search === "?period=today") {
      posts.push(url.pathname + url.search);
      return json([]);
    }
    if (url.pathname === "/api/agents/connect/omp%23work" || url.pathname === "/api/agents/connect/omp") {
      posts.push(url.pathname);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === decodeURIComponent(url.pathname.split("/").pop()));
      if (!a) return route.fulfill({ status: 404, json: { error: "unknown agent" } });
      a.wired = true;
      a.fields[0].value = "relay/m1";
      return reply(route);
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
  test(`${engine}: connecting omp#work posts the encoded id and leaves a query a query`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 980, height: 520 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    const errors = [], posts = [], dialogs = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
    await page.route("**/*", server(posts));
    await page.goto("http://magpie.test/?view=agents");
    const row = '.row.agent[data-id="omp#work"] .ag-conn';
    await page.locator(row).waitFor();
    await page.evaluate(() => api("usage?period=today"));
    await page.locator(row).click();
    await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "true", row);
    assert.ok(posts.includes("/api/usage?period=today"), `query was encoded: ${posts}`);
    assert.ok(!posts.some((p) => p.includes("%3F") || p.includes("%3D")), `a query character was encoded: ${posts}`);
    assert.deepEqual(posts.filter((p) => p.startsWith("/api/agents/")), ["/api/agents/connect/omp%23work"]);
    assert.equal(await page.locator('.row.agent[data-id="omp"] .ag-conn').getAttribute("aria-checked"), "false", "the default omp row stayed off");
    assert.deepEqual(dialogs, []);
    assert.deepEqual(errors, []);
  });
}
