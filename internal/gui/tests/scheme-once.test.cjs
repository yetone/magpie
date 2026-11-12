// Run with Node's test runner and Playwright on the module path; see README.md.
// An address that already has its scheme is never given another (eachg0 on
// X: a Remote magpie's "http://…" became "http://http://…"): http or https,
// in any letter case, with or without a trailing slash. One saved twice
// reads as one. The helpers are tried in the page, and a local server's
// Server address is typed each way and the Endpoints follow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.com/v1", added: false },
  { id: "ollama", name: "Ollama", icon: "ollama", kind: "local", chat: "http://localhost:11434/v1", anthropic: "http://localhost:11434", noKey: true, note: "your local models", added: false },
  { id: "lmstudio", name: "LM Studio", icon: "lmstudio", kind: "local", chat: "http://localhost:1234/v1", noKey: true, note: "local server, port 1234 by default", added: true },
  { id: "omlx", name: "oMLX", icon: "omlx", kind: "local", chat: "http://localhost:8000/v1", responses: "http://localhost:8000/v1", anthropic: "http://localhost:8000", decide: "http://localhost:8000/v1", noKey: true, note: "local server, port 8000 by default", added: true },
];
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
// an oMLX on another computer, and one whose URLs were set apart (another
// app's import): its Anthropic URL on another host than its chat one
const studio = { ...base, id: "omlx", name: "oMLX", icon: "omlx", preset: "omlx", host: "192.168.1.5:8000", chat: "http://192.168.1.5:8000/v1", responses: "http://192.168.1.5:8000/v1", anthropic: "http://192.168.1.5:8000", decide: "http://192.168.1.5:8000/v1" };
// an LM Studio where the preset puts it
const home = { ...base, id: "lmstudio", name: "LM Studio", icon: "lmstudio", preset: "lmstudio", host: "localhost:1234", chat: "http://localhost:1234/v1" };
const apart = { ...base, id: "omlx-2", name: "oMLX Apart", icon: "omlx", preset: "omlx", host: "10.0.0.2:8000", chat: "http://10.0.0.2:8000/v1", anthropic: "http://10.0.0.3:9000" };

function server(lang, saves, asks) {
  const list = { providers: [studio, apart, home], presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") {
      saves.push(route.request().postDataJSON());
      return json(list);
    }
    // a Test asks with the URLs as typed, before a Save
    if (url.pathname === "/api/provider/test") {
      asks.push(route.request().postDataJSON());
      return json({ results: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a scheme already there is kept once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const saves = [], asks = [];
    const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server("en", saves, asks));
    await page.goto("http://magpie.test/?view=providers");

    const ws = await page.evaluate(() => [
      "192.168.1.20:3425", "http://192.168.1.20:3425", "HTTP://192.168.1.20:3425/", "Http://h:1/v1/",
      "http://http://192.168.1.20:3425", "https://http://h", "HTTPS://HTTP://h:1/", "  http://h:1  ", "", "ftp://box",
    ].map((u) => withScheme(u, "http")));
    assert.deepEqual(ws, [
      "http://192.168.1.20:3425", "http://192.168.1.20:3425", "http://192.168.1.20:3425/", "http://h:1/v1/",
      "http://192.168.1.20:3425", "http://h", "http://h:1/", "http://h:1", "", "ftp://box",
    ]);
    const las = await page.evaluate(() => ["http://h:1", "HTTP://h:1/", "http://http://h:1", "https://h", "h:1", ""].map(localAddress));
    assert.deepEqual(las, ["http://h:1", "http://h:1", "http://h:1", "https://h", "http://h:1", ""]);
    const rs = await page.evaluate(() => [respellURL("HTTP://h:1/", "openai"), respellURL("http://http://h/", "anthropic"), respellURL("Http://h", "gemini"), respellURL("http://http://h:1/systemone", "decide")]);
    assert.deepEqual(rs, ["http://h:1/v1", "http://h", "http://h/v1beta", "http://h:1/v1"]);

    // a saved oMLX on another computer, its Server address typed each way
    const ed = page.locator(".editor");
    await page.locator(".row.provider", { hasText: /^oMLX$|oMLX(?! Apart)/ }).first().click();
    await ed.locator(".ehead").waitFor();
    const address = ed.locator("input.address");
    const eps = async () => (await ed.locator(".eps code").allTextContents()).join(" ");
    for (const typed of ["http://10.0.0.9:9000", "HTTP://10.0.0.9:9000/", "http://http://10.0.0.9:9000", "https://10.0.0.9:9000"]) {
      await address.fill(typed);
      const scheme = typed.toLowerCase().startsWith("https") ? "https" : "http";
      assert.equal(await eps(), `${scheme}://10.0.0.9:9000/v1 ${scheme}://10.0.0.9:9000/v1 ${scheme}://10.0.0.9:9000 ${scheme}://10.0.0.9:9000/v1`, typed);
    }
    assert.deepEqual(errors, []);
  });
}
