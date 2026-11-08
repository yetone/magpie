// Run with Node's test runner and Playwright on the module path; see README.md.
// Magpie Browser's sheet looked at from a browser magpie's folders are out
// of reach of: magpie in a container (Docker) or on another computer. The
// folder it writes can't be loaded there, so the sheet offers Download the
// extension, which posts the page's own address and gets the extension as
// a zip pointed at it with a gateway key of its own, and the steps to load
// the unzipped folder. No folder is shown, and nothing is written here.
// Once the browser connects the sheet says so. Every language, Chromium
// and WebKit, at 1100 and 420 wide, nothing running off to the side; no
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
const tr = (lang, s, vars = {}) => Object.entries(vars).reduce((o, [k, v]) => o.replace(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const HOME = "/Users/tester";
const DIR = `${HOME}/Magpie Browser Extension`;
const ABOUT = "Use your own browser: your tabs, signed in to the sites you use. Open pages, click, type, upload, read and take screenshots, through the Magpie Browser extension.";

function fixture(lang) {
  const st = { connected: false, setups: 0, downloads: [], ext: "0.1.7" };
  const browser = () => ({ dir: DIR, written: false, version: "0.1.7", connected: st.connected, away: true, ...(st.connected ? { extension: st.ext, browser: "Chrome/141", ...(st.ext !== "0.1.7" ? { outdated: true } : {}) } : {}) });
  const route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") {
      return json({
        dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
        agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", mcp: `${HOME}/.claude.json`, skills: `${HOME}/.claude/skills` }],
        instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
      });
    }
    if (url.pathname === "/api/library/market/servers") {
      return json({ items: [
        { id: "magpie-browser", name: "magpie-browser", title: "Magpie Browser", publisher: "Magpie", description: ABOUT, transport: "stdio", runs: "magpie", featured: true, optIn: true, have: "" },
        { id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true, have: "" },
      ] });
    }
    if (url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/library/browser") return json(browser());
    if (url.pathname === "/api/library/browser/setup") { st.setups++; return json(browser()); }
    if (url.pathname === "/api/library/browser/extension" && req.method() === "POST") {
      st.downloads.push(JSON.parse(req.postData() || "{}"));
      return route.fulfill({ contentType: "application/zip", headers: { "Content-Disposition": 'attachment; filename="Magpie Browser Extension.zip"' }, body: Buffer.from("PK\x05\x06" + "\0".repeat(18), "binary") });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
  return { route, st };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": away from magpie, Magpie Browser's sheet downloads the extension", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-library-browser-away-${name}.png`), buf);
      }
      await browser.close();
    });
    for (const lang of LANGS) {
      const w = (s, vars) => tr(lang, s, vars);
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px: download, then connected`, async () => {
          const f = fixture(lang);
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", acceptDownloads: true });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", f.route);
          await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
          await page.goto("http://magpie.test:3430/?view=library");
          await page.locator('#view-library .mk[data-market="mcp"] .mk-card[data-id="magpie-browser"]').click();
          const sheet = page.locator("#modal .mk-sheet");
          await sheet.waitFor();
          const box = sheet.locator(".mk-browser");
          const state = box.locator(".mk-browser-state");
          const go = box.locator("button");
          await page.waitForFunction((s) => document.querySelector("#modal .mk-browser-state")?.textContent === s,
            w("Not connected: download the extension and load it in your browser"));
          assert.equal((await go.textContent()).trim(), w("Download the extension"));
          assert.equal(await box.locator(".mk-browser-steps").isVisible(), false);
          assert.equal(await box.locator(".mk-browser-folder").isVisible(), false);
          assert.equal(await wide(page), 0);
          shots.push([`${lang}-${width}-new`, await sheet.screenshot()]);

          const [download] = await Promise.all([page.waitForEvent("download"), go.click()]);
          assert.equal(download.suggestedFilename(), "Magpie Browser Extension.zip");
          // the page's own address, whose port hands the gateway's routes on
          assert.deepEqual(f.st.downloads, [{ at: "http://magpie.test:3430" }]);
          assert.equal(f.st.setups, 0, "nothing is written on magpie's side");
          assert.deepEqual(await box.locator(".mk-browser-steps li").allTextContents(), [
            w("Unzip the download."),
            w("Open chrome://extensions (edge://extensions in Edge) in the browser you use every day."),
            w("Turn on Developer mode."),
            w("Click Load unpacked and choose the unzipped Magpie Browser Extension folder."),
          ]);
          assert.equal(await box.locator(".mk-browser-steps").isVisible(), true);
          assert.equal(await box.locator(".mk-browser-folder").isVisible(), false);
          assert.equal(await wide(page), 0);
          assert.equal(await sheet.evaluate((e) => e.scrollWidth - e.clientWidth), 0);
          shots.push([`${lang}-${width}-downloaded`, await sheet.screenshot()]);

          f.st.connected = true;
          await page.waitForFunction((s) => document.querySelector("#modal .mk-browser-state")?.textContent === s,
            w("Connected to your browser") + " · 0.1.7", { timeout: 10000 });
          assert.equal(await state.locator(".mk-browser-dot.on").count(), 1);
          assert.equal(await box.locator(".mk-browser-steps").isVisible(), false);
          assert.equal(await box.locator(".mk-browser-update").isVisible(), false);
          assert.equal(await wide(page), 0);

          // an older one connected: downloaded again, over the old folder
          f.st.ext = "0.1.6";
          await page.waitForFunction((s) => document.querySelector("#modal .mk-browser-update:not([hidden])")?.textContent === s,
            w("Magpie Browser {v} is ready: download the extension again, unzip it over the old folder, and reload Magpie Browser on chrome://extensions (edge://extensions in Edge).", { v: "0.1.7" }), { timeout: 10000 });
          assert.equal((await go.textContent()).trim(), w("Download the extension"));
          assert.equal(await wide(page), 0);
          assert.equal(await sheet.evaluate((e) => e.scrollWidth - e.clientWidth), 0);
          shots.push([`${lang}-${width}-outdated`, await sheet.screenshot()]);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
