// Run with Node's test runner and Playwright on the module path; see README.md.
// A base URL typed in a custom provider's editor is saved as typed and opens
// as typed, for each API (JNDliuliuM on X: 点击保存之后再重新打开看，还是会被
// 添加上/v1beta). The Save sends the URL as typed, and the editor opened
// again shows what the saved provider has. Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(posts, providers) {
  const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/save") {
      const body = route.request().postDataJSON();
      posts.push(body);
      // the server keeps what it is sent
      const at = providers.findIndex((p) => p.id === body.id);
      if (at >= 0) providers[at] = { ...providers[at], chat: body.chat || "", responses: body.responses || "", anthropic: body.anthropic || "", gemini: body.gemini || "", baseAPI: body.baseAPI };
      return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

const CASES = [
  { api: "gemini", field: "gemini", typed: "https://relay.example.com" },
  { api: "gemini", field: "gemini", typed: "https://relay.example.com/v1" },
  { api: "openai", field: "chat", typed: "https://relay.example.com" },
  { api: "openai", field: "chat", typed: "https://relay.example.com/api" },
  { api: "responses", field: "responses", typed: "https://relay.example.com" },
  { api: "anthropic", field: "anthropic", typed: "https://relay.example.com/claude" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a typed base URL is saved and reopened as typed`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [], posts = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const models = [{ id: "m", name: "m", on: true }];
    const providers = CASES.map((c, i) => ({
      id: "p" + i, name: "P" + i, icon: "generic", chat: "", responses: "", anthropic: "", gemini: "",
      baseAPI: c.api === "openai" ? "" : c.api, models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true,
      [c.field]: "https://old.example.com/x",
    }));
    await page.route("**/*", serve(posts, providers));
    await page.goto("http://magpie.test/?view=providers");
    const ed = page.locator(".editor");
    const open = async (id) => {
      if (await ed.count()) { await page.goto("http://magpie.test/?view=providers"); await ed.waitFor({ state: "detached" }); }
      await page.locator(`#providers .row[data-id="${id}"]`).click();
      await ed.locator(".base-url").waitFor();
    };
    for (const [i, c] of CASES.entries()) {
      await open("p" + i);
      await ed.locator(".base-url").fill(c.typed);
      const n = posts.length;
      await ed.locator(":scope > .bar").getByRole("button", { name: "Save", exact: true }).click();
      for (let k = 0; k < 60 && posts.length === n; k++) await page.waitForTimeout(50);
      assert.equal(posts.length, n + 1, "saved " + c.typed);
      assert.equal(posts[n][c.field], c.typed, `${c.api} sent as typed`);
      await open("p" + i);
      assert.equal(await ed.locator(".base-url").inputValue(), c.typed, `${c.api} reopens as typed`);
    }
    assert.deepEqual(errors, []);
  });
}
