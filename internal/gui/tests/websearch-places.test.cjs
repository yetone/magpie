// Run with Node's test runner and Playwright on the module path; see README.md.
// netube99 on X: web search is set in three places, Plugins › Built-in's
// Web search switch, a search server from the Library and Settings ›
// Models › Web search, and none said what the others do. Now each says it:
// Built-in's row names what searches now ("Searches with …", or that
// nothing does yet) and opens Settings at Web search (featGo, the link
// every built-in with settings has, builtin-places.test.cjs); Settings,
// with the plugin off, says so first and turns it on there (featOff), and no "Now done by"
// stands under it; a Library search server's sheet says the agents call it
// themselves, beside magpie's web search, and opens Settings, naming the
// search API there for one Settings has too. Every language, Chromium and
// WebKit, at 1100 and 420 wide, nothing running off to the side; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);
const HOME = "/Users/tester";
const OWN = "Agents call it themselves, beside magpie's web search, which doesn't use it.";
const AS_API = "As a search API in Settings → Models → Web search, {name} searches for every model, with no server to add.";

// search: the Web search plugin as builtinsState gives it: on, and what
// searches now (searchesNow)
function fixture(lang, { on = true, searches = [] } = {}) {
  const st = { on };
  const posts = [];
  const builtins = () => [
    { id: "library", name: "Library", sub: "Instructions, MCP servers, skills and hooks, kept once and given to every agent", on: true, chosen: true, source: "preset", state: "active", inUse: 0 },
    { id: "search", name: "Web search", sub: "Models that can't search the web search with a provider that can, or a search API", on: st.on, chosen: st.on, source: "preset", state: st.on ? "active" : "off", setAt: "models #searchList", inUse: searches.length, ...(searches.length ? { use: searches.length === 1 ? "1 search source set up" : "{n} search sources set up" } : {}), ...(st.on && searches.length ? { searches } : {}) },
  ];
  const settings = () => ({
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, redactWords: [],
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999", port: 3999,
    proxyNow: "none", proxySource: "none", vision: "", imageGen: "", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    searcher: "", searchChoices: [], searchProvider: "Kimi",
    searchVendors: [{ id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home" }],
    searchAPIs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    builtins: builtins(),
  });
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "pi", name: "Pi", icon: "generic", mcp: `${HOME}/.pi/mcp.json`, skills: `${HOME}/.pi/skills` }],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [], servers: [],
  });
  const route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", textSize: 100, featuresOff: st.on ? [] : ["search"] })};` });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/plugins/builtin") {
      if (req.method() !== "POST") return json(builtins());
      const b = req.postDataJSON();
      posts.push(b);
      if (b.id === "search") st.on = b.on;
      return json(builtins());
    }
    if (url.pathname === "/api/library") return json(lib());
    if (url.pathname === "/api/library/market/servers") {
      return json({ items: [
        { id: "tavily", name: "tavily", featured: true, title: "Tavily", publisher: "Tavily", description: "Search and extract from the web with Tavily.", transport: "stdio", runs: "npx", inputs: [{ key: "TAVILY_API_KEY", where: "env", label: "API key", secret: true, required: true }] },
        { id: "anysearch", name: "anysearch", featured: true, title: "AnySearch", publisher: "AnySearch", description: "Real-time web search.", transport: "http", inputs: [] },
        { id: "fetch", name: "fetch", featured: true, title: "Fetch", publisher: "Model Context Protocol", description: "Fetch a web page and hand it over as Markdown.", transport: "stdio", runs: "uvx", inputs: [] },
      ] });
    }
    if (url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], gateway: { running: true } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
  return { route, posts };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
const searchRow = (page) => page.locator('#builtinList .feat-row[data-id="search"]');

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": web search's three places say what each does and lead to the others", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-websearch-places-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, width, where, opts) => {
      const f = fixture(lang, opts);
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
      await page.route("**/*", f.route);
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
      await page.goto("http://magpie.test/" + where);
      return { page, posts: f.posts };
    };
    const builtinTab = async (page) => {
      const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
      await page.locator(side ? ".nav-sub:not(.leaving) .nav-s" : "#view-plugins .lib-tabs .opt").first().click();
      await searchRow(page).waitFor();
    };
    for (const lang of LANGS) {
      const w = (s, v) => { let x = tr(lang, s); for (const [k, val] of Object.entries(v || {})) x = x.replaceAll(`{${k}}`, val); return x; };
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px: Built-in's Web search names what searches and opens Settings there`, async () => {
          const { page } = await open(lang, width, "?view=plugins", { searches: ["Kimi", "Tavily"] });
          await builtinTab(page);
          const line = searchRow(page).locator(".who .feat-go");
          assert.equal((await line.textContent()).trim(), w("Searches with {names}", { names: "Kimi → Tavily" }) + " · " + w("Settings") + " › " + w("Models"));
          // the names, not how many are set up
          assert.equal(await searchRow(page).locator(".feat-why").count(), 0);
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-builtin`, await searchRow(page).screenshot()]);
          await line.locator("button.link").click();
          await page.locator("#view-settings #searchList .row.flash").waitFor();
          assert.equal(await page.locator("#view-settings").isVisible(), true);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: on with nothing to search with, Built-in says so`, async () => {
          const { page } = await open(lang, width, "?view=plugins", { searches: [] });
          await builtinTab(page);
          const line = searchRow(page).locator(".who .feat-go");
          assert.equal((await line.locator(".warn").textContent()).trim(), w("Nothing to search with yet"));
          assert.equal(await line.locator("button.link").count(), 1);
          assert.equal(await wide(page), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: off, Settings says so first and Turn on brings it back`, async () => {
          const { page, posts } = await open(lang, width, "?view=settings&tab=models", { on: false });
          const off = page.locator("#searchList .row.feat-off");
          await off.waitFor();
          assert.equal(await page.locator("#searchList .row").first().evaluate((r) => r.classList.contains("feat-off")), true);
          assert.equal((await off.locator(".name").textContent()).trim(), w("Off in Plugins › Built-in"));
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-settings-off`, await off.screenshot()]);
          // no "Now done by" while nothing searches
          const now = w("Now done by {who}; these come after it", { who: "Kimi" });
          assert.equal(await page.locator("#searchList").evaluate((b, s) => b.textContent.includes(s), now), false);
          await off.locator("button", { hasText: w("Turn on") }).click();
          await page.locator("#searchList .row.feat-off").waitFor({ state: "detached" });
          assert.deepEqual(posts, [{ id: "search", on: true }]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: on, Settings has no off row`, async () => {
          const { page } = await open(lang, width, "?view=settings&tab=models", { searches: ["Kimi"] });
          await page.locator("#searchList .searcher-row").waitFor();
          assert.equal(await page.locator("#searchList .row.feat-off").count(), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        for (const [id, name, api] of [["tavily", "Tavily", true], ["anysearch", "AnySearch", false]]) {
          await t.test(`${lang} at ${width}px: the Library's ${name} says it is the agents' own, and opens Settings`, async () => {
            const { page } = await open(lang, width, "?view=library");
            const card = page.locator(`#view-library .mk[data-market="mcp"] .mk-card[data-id="${id}"]`);
            await card.waitFor();
            await card.click();
            const sheet = page.locator("#modal .mk-sheet");
            await sheet.waitFor();
            const hint = sheet.locator(".mk-hint", { has: page.locator("button.link") });
            const want = w(OWN) + (api ? " " + w(AS_API, { name }) : "") + " " + w("Set up in Settings");
            assert.equal((await hint.textContent()).trim(), want);
            assert.equal(await wide(page), 0);
            shots.push([`${lang}-${width}-${id}`, await sheet.screenshot()]);
            await hint.locator("button.link").click();
            await page.locator("#view-settings #searchList .row.flash").waitFor();
            await page.locator("#modal").waitFor({ state: "hidden" }); // the sheet closes
            assert.deepEqual(page.errors, []);
            await page.context().close();
          });
        }
        await t.test(`${lang} at ${width}px: a server that doesn't search says none of it`, async () => {
          const { page } = await open(lang, width, "?view=library");
          const card = page.locator('#view-library .mk[data-market="mcp"] .mk-card[data-id="fetch"]');
          await card.waitFor();
          await card.click();
          const sheet = page.locator("#modal .mk-sheet");
          await sheet.waitFor();
          assert.equal(await sheet.evaluate((s, own) => s.textContent.includes(own), w(OWN)), false);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
  });
}
