// Run with Node's test runner and Playwright on the module path; see README.md.
// A Google Vertex AI provider with two keys ticked or more keeps its
// project's fields in its editor: Project ID, Location, Credentials file,
// Service account and Base URL drew only while one key was ticked, and
// went once Routing's spread over the keys showed. Save still posts them
// with the edit. Both engines, every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const save = { en: "Save", zh: "保存", "zh-TW": "儲存", ja: "保存", de: "Speichern" };

function server(lang, keys, posts) {
  const vertex = { id: "google-vertex", name: "Google Vertex AI", icon: "vertexai-color", preset: "google-vertex", chat: "", responses: "", anthropic: "", catalog: "",
    host: "aiplatform.googleapis.com", models: [{ id: "gemini-3.8-flash", name: "gemini-3.8-flash", on: true }], agents: [], fallback: [], headers: {},
    key: { set: true, masked: "AQ.…one" }, balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    vertex: { project: "my-project-123", location: "us", credentials: "~/keys/sa.json", baseUrl: "https://proxy.example.com" },
    keyList: [{ id: "aaaaaaaaaa", name: "Team", masked: "AQ.…one", on: true, active: true }, { id: "bbbbbbbbbb", name: "", masked: "AQ.…two", on: keys > 1 }] };
  const presets = [{ id: "google-vertex", name: "Google Vertex AI", short: "Vertex AI", icon: "vertexai-color", kind: "vendor", added: true, noList: true, vertex: true }];
  const providers = { providers: [vertex], presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && req.method() === "POST") { posts.push({ path: url.pathname, body: req.postDataJSON() }); return json(providers); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Vertex AI provider keeps its project's fields with two keys", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(save)) for (const keys of [1, 2]) await t.test(`${lang} ${keys} key(s) ticked`, async () => {
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      t.after(() => page.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, keys, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#providers .row.provider[data-id="google-vertex"]').click();
      const modal = page.locator("#modal");
      await modal.locator(".vertex-project").waitFor();
      for (const [cls, v] of [["project", "my-project-123"], ["location", "us"], ["credentials", "~/keys/sa.json"], ["base-url", "https://proxy.example.com"]])
        assert.equal(await modal.locator(".vertex-" + cls).inputValue(), v, cls);
      assert.equal(await modal.locator(".vertex-impersonate").count(), 1);
      await modal.locator(".vertex-location").fill("eu");
      await modal.getByRole("button", { name: save[lang], exact: true }).click();
      await modal.waitFor({ state: "hidden" });
      const body = posts.find((p) => p.path === "/api/provider/save")?.body;
      assert.equal(body?.vertex?.location, "eu");
      assert.equal(body?.vertex?.project, "my-project-123");
      assert.deepEqual(errors, []);
    });
  });
}
