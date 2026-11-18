// Run with Node's test runner and Playwright on the module path; see README.md.
// matrix0487 on Discord: of magpie's three own MCP servers, image
// recognition, image generation and search, the Library's Discover had
// Magpie Image and Magpie Web Search but not image recognition. It now has
// Magpie Image Recognition beside them, given only to the agents picked
// for it, and Settings' Image recognition row says the way there as the
// Library is: Give agents Magpie Image Recognition… opens its sheet, which
// says where each image goes; with the Library off, Turn on the Library
// switches it on; with recognition off, or in gateway mode, there is none.
// One that can't answer yet says why on its Discover card and sheet, so
// no tool is added dead without a word. Every language, Chromium and
// WebKit, at 1100 and 420 wide, nothing running off to the side; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);
const HOME = "/Users/tester";
const HINT = "Pick the agents that may read images with it: each image goes to the Image recognition model set in Settings › Models › Images, and costs what that model charges.";
const UNREADY = "No model that sees images is set up yet: it reads images once a provider with one is added, and says so until then";
const ABOUT = "Read an image (a file, a URL, a screenshot) with the image recognition model set in Magpie (Settings › Models › Images), for a model that can't see.";
const SEARCH = "Search the web with the search set in Magpie (Settings › Models › Web search), and read a page by its URL.";

function fixture(lang, { libOn, have = false, gateway = false, unready = "", vision = "" }) {
  const st = { libOn };
  const posts = [];
  const builtins = () => [{ id: "library", name: "Library", sub: "Instructions, MCP servers and skills for every agent", on: st.libOn, chosen: st.libOn, source: "preset", state: st.libOn ? "active" : "off", inUse: 0 }];
  const settings = () => ({
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, redactWords: [],
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999", port: 3999,
    proxyNow: "none", proxySource: "none", vision, imageGen: "", lanURLs: [], visionAuto: "probe/eye",
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    searcher: "", searchChoices: [],
    searchVendors: [{ id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home" }],
    searchAPIs: [{ vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    builtins: builtins(),
    ...(gateway ? { web: true, gatewayMode: "on", gatewayOn: true } : {}),
  });
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "pi", name: "Pi", icon: "generic", mcp: `${HOME}/.pi/mcp.json`, skills: `${HOME}/.pi/skills` }],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
    servers: have ? [{ name: "magpie-vision", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "vision"], agents: ["pi"] }] : [],
  });
  const route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", textSize: 100, featuresOff: st.libOn ? [] : ["library"], ...(gateway ? { web: true, gateway: true } : {}) })};` });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/plugins/builtin" && req.method() === "POST") {
      const b = req.postDataJSON();
      posts.push(b);
      if (b.id === "library") st.libOn = b.on;
      return json(builtins());
    }
    if (url.pathname === "/api/library") return json(lib());
    if (url.pathname === "/api/library/market/servers") {
      return json({ items: [
        { id: "magpie-image", name: "magpie-image", title: "Magpie Image", publisher: "Magpie", description: "Generate and edit images with the image model set in Magpie.", transport: "stdio", runs: "magpie", featured: true, optIn: true, have: "" },
        { id: "magpie-web-search", name: "magpie-web-search", title: "Magpie Web Search", publisher: "Magpie", description: SEARCH, transport: "stdio", runs: "magpie", featured: true, optIn: true, have: "" },
        { id: "magpie-vision", name: "magpie-vision", title: "Magpie Image Recognition", publisher: "Magpie", description: ABOUT, transport: "stdio", runs: "magpie", featured: true, optIn: true, have: have ? "magpie-vision" : "", ...(unready ? { unready } : {}) },
      ] });
    }
    if (url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], gateway: { running: true } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
  return { route, posts };
}

const row = (page) => page.locator("#imagesList > .row").first();
const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Image recognition's MCP server is in the Library, and its row says the way there", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-vision-mcp-library-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, width, opts) => {
      const f = fixture(lang, opts);
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", f.route);
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      await row(page).waitFor();
      return { page, posts: f.posts };
    };
    for (const lang of LANGS) {
      const w = (s) => tr(lang, s);
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px: Give agents Magpie Image Recognition… opens its sheet in the Library`, async () => {
          const { page } = await open(lang, width, { libOn: true, unready: UNREADY });
          const go = row(page).locator(".who button.vision-mcp-lib");
          assert.equal((await go.textContent()).trim(), w("Give agents Magpie Image Recognition…"));
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-row`, await row(page).screenshot()]);
          await go.click();
          const sheet = page.locator("#modal .mk-sheet");
          await sheet.waitFor();
          assert.equal(await page.locator("#view-library").isVisible(), true);
          assert.equal((await sheet.locator(".mk-name.big").textContent()).trim(), "Magpie Image Recognition");
          assert.equal((await sheet.locator(".mk-about").textContent()).trim(), w(ABOUT));
          assert.equal((await sheet.locator(".mk-hint").first().textContent()).trim(), w(HINT));
          // nothing sees yet: the sheet says so, and still adds it
          assert.equal((await sheet.locator(".mk-hint.mk-unready").textContent()).trim(), w(UNREADY));
          // opt in: no agent is ticked until it is picked
          assert.equal(await page.locator('#view-library .mk[data-market="mcp"]').count(), 1);
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-sheet`, await page.screenshot()]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: with the Library off, Turn on the Library brings it back`, async () => {
          const { page, posts } = await open(lang, width, { libOn: false });
          const go = row(page).locator(".who button.vision-mcp-lib");
          assert.equal((await go.textContent()).trim(), w("Turn on the Library"));
          assert.equal(await wide(page), 0);
          await go.click();
          await page.waitForFunction((s) => document.querySelector("#imagesList button.vision-mcp-lib")?.textContent.trim() === s, w("Give agents Magpie Image Recognition…"));
          assert.deepEqual(posts, [{ id: "library", on: true }]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
      for (const width of [1100, 420]) await t.test(`${lang} at ${width}px: one already in the library opens Discover, all three of magpie's own there, the one not set up saying so`, async () => {
        const { page } = await open(lang, width, { libOn: true, have: true, unready: UNREADY });
        await row(page).locator("button.vision-mcp-lib").click();
        const card = (id) => page.locator(`#view-library .mk[data-market="mcp"] .mk-card[data-id="${id}"]`);
        await card("magpie-vision").waitFor();
        assert.equal(await page.locator("#modal .mk-sheet").count(), 0);
        for (const id of ["magpie-image", "magpie-web-search"]) {
          assert.equal(await card(id).count(), 1, id);
          assert.equal(await card(id).locator(".mk-unready").count(), 0, id);
        }
        const badge = card("magpie-vision").locator(".mk-badge.mk-unready");
        assert.equal((await badge.textContent()).trim(), w("Not set up yet"));
        assert.equal(await badge.getAttribute("title"), w(UNREADY));
        // the badge gives way inside its card
        const [c, b] = await Promise.all([card("magpie-vision").boundingBox(), badge.boundingBox()]);
        assert(b.x + b.width <= c.x + c.width + 0.5, `badge ${JSON.stringify(b)} past card ${JSON.stringify(c)}`);
        assert.equal(await wide(page), 0);
        shots.push([`${lang}-${width}-discover`, await page.screenshot()]);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
      await t.test(`${lang}: gateway mode, with no Library, offers no way to one`, async () => {
        const { page } = await open(lang, 1100, { libOn: true, gateway: true });
        assert.equal(await row(page).locator("button.vision-mcp-lib").count(), 0);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
      await t.test(`${lang}: with image recognition off there is nothing to give`, async () => {
        const { page } = await open(lang, 1100, { libOn: true, vision: "off" });
        assert.equal(await row(page).locator("button.vision-mcp-lib").count(), 0);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }
  });
}
