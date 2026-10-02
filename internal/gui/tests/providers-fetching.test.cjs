// Run with Node's test runner and Playwright on the module path; see README.md.
// #541: the Providers tab no longer waits for the accounts' lists from their
// vendors. While the backend says they are on their way (fetching), the
// page asks again every few seconds, draws the answer once it changed, and
// stops asking once they are in. Chromium and WebKit; no backend, the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const prov = (id) => ({
  id, name: id.toUpperCase(), icon: "generic", host: id + ".example.com", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});

function fixture() {
  let asked = 0;
  const route = async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/providers") {
      asked++;
      // the account's list comes in on the third ask
      const done = asked >= 3;
      return json({ providers: done ? [prov("a"), prov("b")] : [prov("a")], presets: [], excluded: [], gateway: { running: true, window: true }, fetching: !done });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return { route, asked: () => asked };
}

const order = (page) => page.locator("#providers > .row.provider").evaluateAll((rs) => rs.map((r) => r.dataset.id));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the list is drawn at once and again when the accounts' lists are in", { timeout: 60000 }, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      await browser.close();
      assert.deepEqual(errors, []);
    });
    const ctx = await browser.newContext({ viewport: { width: 1000, height: 640 }, reducedMotion: "reduce" });
    const page = await ctx.newPage(), f = fixture();
    page.setDefaultTimeout(15000);
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", f.route);
    await page.goto("http://magpie.test/?view=providers");
    await page.locator("#providers > .row.provider").first().waitFor();
    await page.locator('#providers > .row.provider[data-id="b"]').waitFor();
    assert.deepEqual(await order(page), ["a", "b"]);
    const n = f.asked();
    await new Promise((r) => setTimeout(r, 4000));
    assert.equal(f.asked(), n, "asked again after the lists were in");
  });
}
