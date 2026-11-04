// Run with Node's test runner and Playwright on the module path; see README.md.
// Agent plugins on the Plugins page: a plugin that adds an agent wears the
// Agent chip, not Provider, and says which agent it adds to the Agents
// page (or why it didn't load, in red); one switched off is an Agent still;
// one that is middleware too wears both chips. One with a Bun module of its
// own (main) beside its agent, which needs a runtime, is an Agent too, not
// a Provider, on or off (miaocell, plugins#737); one whose module serves a
// provider as well wears both. Discover lists the market's
// agent plugins in their own section with the Agent chip, wearing the
// picture its package gives (npm's magpie.icon, kept by magpie as a file)
// when the market gives it none. Every language,
// both engines, at a narrow width too; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const AIDER = "/Users/x/ag/aider", BROKEN = "/Users/x/ag/broken", OFF = "/Users/x/ag/off", BOTH = "/Users/x/ag/both";
const DEVA = "/Users/x/ag/deva", DEVAOFF = "/Users/x/ag/idle", PROV = "/Users/x/ag/prov";
const state = { bun: true, bunVersion: "1.3.0", plugins: [
  { spec: AIDER, providers: [], moved: [], isAgent: true, inMagpieOnly: true, agent: { id: "aider", name: "Aider" } },
  { spec: BROKEN, providers: [], moved: [], isAgent: true, inMagpieOnly: true, agent: { error: `agent id "codex" is magpie's own` } },
  { spec: OFF, off: true, providers: [], moved: [], isAgent: true, inMagpieOnly: true },
  { spec: BOTH, providers: [], moved: [], isAgent: true, inMagpieOnly: true, isMiddleware: true, agent: { id: "jot", name: "Jot" }, middleware: { hooks: ["onRequest"], calls: 0, avgMicros: 0, failures: 0 } },
  { spec: DEVA, providers: [], moved: [], named: true, isAgent: true, agent: { id: "deva", name: "Deva" } },
  { spec: DEVAOFF, off: true, providers: [], moved: [], isAgent: true },
  { spec: PROV, providers: ["provx"], moved: [], named: true, isAgent: true, agent: { id: "provx", name: "Provx" } },
] };
// a 1x1 picture
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");
const listings = [
  { package: "@magpie-community/agent-aider", name: "Aider", kind: "agent", community: true, summary: { en: "Aider through magpie" }, npm: { version: "0.1.0", icon: "file:aiderpic.png" } },
  { package: "@magpie-community/middleware-block-patterns", name: "Block patterns", kind: "middleware", community: true, summary: { en: "Blocks patterns" }, npm: { version: "0.1.0" } },
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/market") return json({ listings, state });
    if (url.pathname === "/api/icons/aiderpic.png") return route.fulfill({ contentType: "image/png", body: PNG });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { addsDeva: "Adds Deva to Agents", installed: "Installed", agent: "Agent", mw: "Middleware", provider: "Provider", adds: "Adds Aider to Agents", addsJot: "Adds Jot to Agents", broken: /^Agent didn't load: agent id "codex"/, off: "Off", nothing: "Signs in to nothing", sec: "Agents" },
  zh: { addsDeva: "把 Deva 加到 Agent 页", installed: "已安装", agent: "Agent", mw: "中间件", provider: "供应商", adds: "把 Aider 加到 Agent 页", addsJot: "把 Jot 加到 Agent 页", broken: /^Agent 没有加载：agent id "codex"/, off: "关闭", nothing: "不能登录", sec: "Agent" },
  "zh-TW": { addsDeva: "把 Deva 加到 Agent 頁", installed: "已安裝", agent: "Agent", adds: "把 Aider 加到 Agent 頁", addsJot: "把 Jot 加到 Agent 頁", broken: /^Agent 沒有載入：/, sec: "Agent" },
  ja: { addsDeva: "Deva をエージェントページに追加します", installed: "インストール済み", agent: "エージェント", adds: "Aider をエージェントページに追加します", addsJot: "Jot をエージェントページに追加します", broken: /^エージェントを読み込めませんでした：/, sec: "エージェント" },
  de: { addsDeva: "Fügt Deva zu Agenten hinzu", installed: "Installiert", agent: "Agent", adds: "Fügt Aider zu Agenten hinzu", addsJot: "Fügt Jot zu Agenten hinzu", broken: /^Agent nicht geladen: /, sec: "Agenten" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": agent plugins in Installed and Discover", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const kinds = (r) => r.locator(".pm-chip.kind").allInnerTexts().then((a) => a.map((x) => x.trim()));

        // Discover: the agent plugin in a section of its own, as an Agent
        const sec = view.locator(".pm-sec").filter({ has: page.locator(".pm-sechead h3", { hasText: new RegExp("^" + w.sec + "$") }) });
        await sec.waitFor();
        const card = sec.locator(".pm-card");
        assert.equal(await card.count(), 1);
        assert.ok((await card.innerText()).includes("Aider"));
        assert.deepEqual(await kinds(card), [w.agent]);
        assert.ok(await card.locator(".pm-chip.kind.ag").count());
        // yetone: 这里的 agent plugin 为什么没有 logo
        assert.equal(await card.locator(".pm-logo img").getAttribute("src"), "/api/icons/aiderpic.png");
        assert.equal(await card.locator(".pm-logo svg").count(), 0);

        await view.page().locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: w.installed }).click();
        const row = (name) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: name }) });
        const aider = row("aider");
        await aider.waitFor();
        assert.deepEqual(await kinds(aider), [w.agent]);
        assert.equal((await aider.locator(".pm-mw").innerText()).trim(), w.adds);
        if (w.nothing) assert.ok(!(await aider.innerText()).includes(w.nothing), "an agent plugin isn't said to sign in to nothing");

        const broken = row("broken");
        assert.match((await broken.locator(".pm-mw").innerText()).trim(), w.broken);
        assert.ok(await broken.locator(".pm-mw").evaluate((e) => e.classList.contains("bad")));
        assert.deepEqual(await kinds(broken), [w.agent]);

        const off = row("off");
        if (w.off) assert.equal((await off.locator(".who .sub").innerText()).trim(), w.off);
        assert.equal(await off.locator(".pm-mw").count(), 0);
        assert.deepEqual(await kinds(off), [w.agent], "one switched off is an agent still, not a provider");

        const both = row("both");
        if (w.mw) assert.deepEqual(await kinds(both), [w.mw, w.agent]);
        assert.ok((await both.locator(".pm-mw").allInnerTexts()).map((x) => x.trim()).includes(w.addsJot));

        // an agent with a module of its own (main): an Agent, not a Provider
        const deva = row("deva");
        assert.deepEqual(await kinds(deva), [w.agent]);
        assert.equal((await deva.locator(".pm-mw").innerText()).trim(), w.addsDeva);
        if (w.nothing) assert.ok(!(await deva.innerText()).includes(w.nothing));
        assert.deepEqual(await kinds(row("idle")), [w.agent], "off, an agent with a main is an agent still");
        if (w.provider) assert.deepEqual(await kinds(row("prov")), [w.agent, w.provider], "an agent whose module serves a provider is both");

        // the row's text fits: nothing wider than the page
        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(over <= 0, "page scrolls sideways by " + over);
        for (const r of [aider, broken]) {
          const [line, box] = await Promise.all([r.locator(".pm-mw").boundingBox(), r.boundingBox()]);
          assert.ok(line.x + line.width <= box.x + box.width + 1, "the agent line overflows its row");
        }
        if (process.env.ARTIFACT_DIR) await view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-agent-${engine}-${lang}-${width}.png`) });
        assert.deepEqual(errors, []);
      });
    }
  });
}
