// Run with Node's test runner and Playwright on the module path; see README.md.
// #2091 (nailuoGG): under the Essentials preset the Library is switched off
// and leaves the window, but Settings › Models › Images' Image generation still said
// "Give an agent the tool from Library → MCP servers → Discover → Magpie
// Image", a page that isn't there. The row now says where the tool comes
// from as things are: with the Library off it says it is off and offers
// Turn on the Library, which switches it on (Plugins › Built-in's switch)
// and brings its page back; with it on, Give agents Magpie Image… opens the
// Library on MCP servers with Magpie Image's sheet up, to pick the agents.
// One already in the library opens the tab with nothing over it. Gateway
// mode, which has no Library, and an Image generation turned Off offer no
// way there. Every language, Chromium and WebKit, at 1100 and 420 wide,
// nothing running off to the side; no backend, the API is faked here.
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

function fixture(lang, { libOn, have = false, imageGen = "", gateway = false }) {
  const st = { libOn };
  const posts = [];
  const builtins = () => [{ id: "library", name: "Library", sub: "Instructions, MCP servers and skills for every agent", on: st.libOn, chosen: st.libOn, source: "preset", state: st.libOn ? "active" : "off", inUse: 0 }];
  const settings = () => ({
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", vision: "", imageGen,
    visionModels: [], workbuddyCheckins: [], lanURLs: [],
    imageGenModels: [{ id: "codex/gpt-image-2", name: "GPT Image 2", provider: "codex", providerName: "Codex", icon: "openai" }],
    imageGenAuto: "codex/gpt-image-2",
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    builtins: builtins(),
    ...(gateway ? { web: true, gatewayMode: "on", gatewayOn: true } : {}),
  });
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "pi", name: "Pi", icon: "generic", mcp: `${HOME}/.pi/mcp.json`, skills: `${HOME}/.pi/skills` }],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
    servers: have ? [{ name: "magpie-image", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "image"], agents: ["pi"] }] : [],
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
        { id: "magpie-image", name: "magpie-image", title: "Magpie Image", publisher: "Magpie", description: "Generate and edit images with the image model set in Magpie.", transport: "stdio", runs: "magpie", featured: true, optIn: true, have: have ? "magpie-image" : "" },
        { id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true, have: "" },
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

const row = (page) => page.locator("#imageGenRow");
const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
// the Library's tab in the nav, there or switched off (at 420 the nav
// itself folds away, so its own display is what tells)
const libNav = (page) => page.evaluate(() => {
  const b = document.querySelector('#nav button[data-view="library"]');
  return !!b && !b.hidden && getComputedStyle(b).display !== "none";
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Image generation says the way to the Magpie Image tool as the Library is", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-images-library-way-${name}.png`), buf);
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
      // Settings is drawn from each read of the settings, and a page opened
      // on it reads them twice (its own read and load()'s): the row is held
      // for a screenshot once the last of them is drawn, or the one it
      // waited on is replaced under it (#4469)
      await settingsDrawn(page);
      await row(page).waitFor();
      return { page, posts: f.posts };
    };
    for (const lang of LANGS) {
      const w = (s) => tr(lang, s);
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px: the Library off is said, and Turn on the Library brings it back`, async () => {
          const { page, posts } = await open(lang, width, { libOn: false });
          const sub = await row(page).locator(".who > .sub").first().textContent();
          assert.equal(sub, w("The model Magpie Image draws with. Agents get the Magpie Image tool from the Library, which is turned off in Plugins › Built-in; images are saved in their project"));
          assert.doesNotMatch(sub, /Discover|发现|發現|発見|Entdecken/);
          assert.equal(await libNav(page), false);
          const go = row(page).locator("button.image-gen-tool");
          assert.equal((await go.textContent()).trim(), w("Turn on the Library"));
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-off`, await row(page).screenshot()]);
          await go.click();
          await page.waitForFunction(() => { const b = document.querySelector('#nav button[data-view="library"]'); return !!b && !b.hidden && getComputedStyle(b).display !== "none"; });
          assert.deepEqual(posts, [{ id: "library", on: true }]);
          await page.waitForFunction((s) => document.querySelector("#imageGenRow button.image-gen-tool")?.textContent.trim() === s, w("Give agents Magpie Image…"));
          assert.equal(await row(page).locator(".who > .sub").first().textContent(), w("The model Magpie Image draws with. Agents get the Magpie Image tool from the Library; images are saved in their project"));
          assert.equal(await wide(page), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: Give agents Magpie Image… opens its sheet in the Library`, async () => {
          const { page } = await open(lang, width, { libOn: true });
          assert.equal(await libNav(page), true);
          const go = row(page).locator("button.image-gen-tool");
          assert.equal((await go.textContent()).trim(), w("Give agents Magpie Image…"));
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-on`, await row(page).screenshot()]);
          await go.click();
          const sheet = page.locator("#modal .mk-sheet");
          await sheet.waitFor();
          assert.equal(await page.locator("#view-library").isVisible(), true);
          assert.equal((await sheet.locator(".mk-name.big").textContent()).trim(), "Magpie Image");
          // on MCP servers, whose market is under it
          assert.equal(await page.locator('#view-library .mk[data-market="mcp"]').count(), 1);
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-sheet`, await page.screenshot()]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
      await t.test(`${lang}: one already in the library opens MCP servers with nothing over it`, async () => {
        const { page } = await open(lang, 1100, { libOn: true, have: true });
        await row(page).locator("button.image-gen-tool").click();
        await page.locator('#view-library .mk[data-market="mcp"] .mk-card[data-id="magpie-image"]').waitFor();
        assert.equal(await page.locator("#view-library").isVisible(), true);
        assert.equal(await page.locator("#modal .mk-sheet").count(), 0);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
      await t.test(`${lang}: Off, and gateway mode, offer no way to a Library`, async () => {
        for (const opts of [{ libOn: true, imageGen: "off" }, { libOn: true, gateway: true }]) {
          const { page } = await open(lang, 1100, opts);
          assert.equal(await row(page).locator("button.image-gen-tool").count(), 0);
          const sub = await row(page).locator(".who > .sub").first().textContent();
          assert.equal(sub, opts.gateway
            ? w("The model the Image generation MCP server below draws with, for the agents that add it; images come back in their answer")
            : w("Agents given Magpie Image can't generate images or videos: the tool says it is off"));
          assert.deepEqual(page.errors, []);
          await page.context().close();
        }
      });
    }
  });
}
