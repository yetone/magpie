// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugins that give tools on the Plugins page: the row lists each tool by
// the name magpie's plugin tools MCP server gives it, its description on
// hover, and a button that copies the server's address. A plugin of tools
// alone wears no Provider chip and isn't said to sign in to nothing; one
// that signs in too keeps its line; one switched off lists none. One with
// a server in the Library hides its tools from every agent and gives them
// back from its line (chengming0v0 on Discord). Every language, both
// engines, at a narrow width too; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const WEATHER = "/Users/x/pl/weather-tools", BOTH = "/Users/x/pl/both", OFF = "/Users/x/pl/off", VIDEO = "/Users/x/pl/video";
const video = { spec: VIDEO, providers: ["Vid"], moved: [], named: true, tools: [{ name: "make", mcp: "video_make" }], toolsServer: { name: "video", plugin: VIDEO, on: true } };
const state = { bun: true, bunVersion: "1.3.0", named: true, plugins: [
  video,
  { spec: WEATHER, providers: [], moved: [], named: true, tools: [
    { name: "now", description: "The weather now in a city.", mcp: "weather-tools_now" },
    { name: "week", description: "Seven days ahead.", mcp: "weather-tools_week" },
  ] },
  { spec: BOTH, providers: ["Acme"], moved: [], named: true, tools: [{ name: "lookup", mcp: "both_lookup" }] },
  { spec: OFF, off: true, providers: [], moved: [], tools: [{ name: "x", mcp: "off_x" }] },
] };

const shown = []; // what each Hide or Give asked the Library
function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/library/servers/plugin-on") {
      const b = route.request().postDataJSON();
      shown.push(b);
      video.toolsServer = { ...video.toolsServer, on: b.on };
      return json({});
    }
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", tools: "Tools for every agent: weather-tools_now, weather-tools_week", nothing: "Signs in to nothing", provider: "Provider", given: "Tools for agents: video_make", hidden: "Tools, hidden from agents: video_make", hide: "Hide from agents", give: "Give to agents" },
  zh: { installed: "已安装", tools: "给每个智能体的工具：weather-tools_now、weather-tools_week", nothing: "不能登录", provider: "供应商", given: "给智能体的工具：video_make", hidden: "工具，已对智能体隐藏：video_make", hide: "对智能体隐藏", give: "交给智能体" },
  "zh-TW": { installed: "已安裝", tools: /^給每個代理的工具：weather-tools_now/, given: "給代理的工具：video_make", hidden: "工具，已對代理隱藏：video_make", hide: "對代理隱藏", give: "交給代理" },
  ja: { installed: "インストール済み", tools: /^すべてのエージェントのツール：weather-tools_now/, given: "エージェントのツール：video_make", hidden: "ツール（エージェントには非表示）：video_make", hide: "エージェントに隠す", give: "エージェントに渡す" },
  de: { installed: "Installiert", tools: /^Werkzeuge für jeden Agenten: weather-tools_now/, given: "Werkzeuge für Agenten: video_make", hidden: "Werkzeuge, vor Agenten verborgen: video_make", hide: "Vor Agenten verbergen", give: "Agenten geben" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": plugin tools in Installed", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const ctx = await browser.newContext({ viewport: { width, height: 820 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        // what Copy put on the clipboard, without the system's
        await page.addInitScript(() => {
          window.__copied = [];
          Object.defineProperty(navigator, "clipboard", { value: { writeText: async (s) => { window.__copied.push(s); } }, configurable: true });
        });
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.page().locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: w.installed }).click();
        const row = (name) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: name }) });
        const weather = row("weather-tools");
        await weather.waitFor();
        const line = weather.locator(".pm-tools");
        const said = (await line.locator("span").first().innerText()).trim();
        if (typeof w.tools === "string") assert.equal(said, w.tools);
        else assert.match(said, w.tools);
        assert.match(await line.getAttribute("title"), /weather-tools_now — The weather now in a city\.\nweather-tools_week — Seven days ahead\./);
        if (w.nothing) assert.ok(!(await weather.innerText()).includes(w.nothing), "a plugin of tools isn't said to sign in to nothing");
        if (w.provider) assert.ok(!(await weather.locator(".pm-chip.kind").allInnerTexts()).map((x) => x.trim()).includes(w.provider), "no Provider chip on a plugin of tools alone");
        await line.locator(".copy").click();
        await page.waitForFunction(() => window.__copied.length > 0 || document.querySelector(".toast, #status"));
        const copied = await page.evaluate(() => window.__copied);
        if (copied.length) assert.equal(copied[0], "http://127.0.0.1:3425/mcp/magpie/tools");

        const both = row("both");
        assert.ok((await both.innerText()).includes("Acme"));
        assert.ok((await both.locator(".pm-tools").innerText()).includes("both_lookup"));
        assert.equal(await row("off").locator(".pm-tools").count(), 0, "a plugin off lists no tools");
        assert.equal(await weather.locator(".pm-tools-flip").count(), 0, "no server in the Library, nothing to hide");

        // hidden from every agent, and given back, from the row
        video.toolsServer.on = true;
        shown.length = 0;
        const vline = row("video").locator(".pm-tools");
        assert.equal((await vline.locator("span").first().innerText()).trim(), w.given);
        const flip = vline.locator(".pm-tools-flip");
        assert.equal((await flip.innerText()).trim(), w.hide);
        await flip.click();
        await vline.locator(".pm-tools-flip", { hasText: w.give }).waitFor();
        assert.equal((await vline.locator("span").first().innerText()).trim(), w.hidden);
        assert.deepEqual(shown, [{ name: VIDEO, on: false }]);
        assert.ok(!(await row("video").innerText()).includes("Off\n"), "the plugin itself stays on");
        await vline.locator(".pm-tools-flip").click();
        await vline.locator(".pm-tools-flip", { hasText: w.hide }).waitFor();
        assert.deepEqual(shown, [{ name: VIDEO, on: false }, { name: VIDEO, on: true }]);
        const [fb, vb] = await Promise.all([vline.locator(".pm-tools-flip").boundingBox(), row("video").boundingBox()]);
        assert.ok(fb.x + fb.width <= vb.x + vb.width + 1, "the line's button overflows its row");

        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(over <= 0, "page scrolls sideways by " + over);
        const [lb, rb] = await Promise.all([line.boundingBox(), weather.boundingBox()]);
        assert.ok(lb.x + lb.width <= rb.x + rb.width + 1, "the tools line overflows its row");
        if (process.env.ARTIFACT_DIR) await view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-tools-${engine}-${lang}-${width}.png`) });
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
