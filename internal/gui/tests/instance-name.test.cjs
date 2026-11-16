// Run with Node's test runner and Playwright on the module path; see README.md.
// A browser tab's title names the magpie it shows (starballl on X: several
// deployed, every tab titled "magpie"): "<name> · magpie" from the first
// paint (boot.js), MAGPIE_NAME's while Settings has none, and the name set
// in Settings › Network and sharing › Advanced › Name once saved. The
// field sits under Advanced, closed until a name is set, the variable's
// name its placeholder. The app's own window keeps "magpie". Every
// language, Chromium and WebKit, nothing wider than a 360px window; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, web, saved, posts) {
  const settings = () => ({ lang, theme: "light", gateway: "http://127.0.0.1:3425", lan: false, lanURLs: [], version: "0.1.900", dir: "/config/magpie", nameEnv: "nas", ...(saved.name ? { name: saved.name } : {}), fx: { rate: 7.2, at: new Date().toISOString(), stale: false } });
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    // as api.go's boot.js: a browser tab is titled before app.js runs
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web, name: saved.name || "nas", nameEnv: "nas", sideGateway: { url: "http://127.0.0.1:3425", running: true, boot: true } })};\n` + (web ? `document.title = ${JSON.stringify((saved.name || "nas") + " · magpie")};\n` : "") });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", nameEnv: "nas", ...(saved.name ? { name: saved.name } : {}) }, web });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = JSON.parse(req.postData());
        posts.push(body);
        if ("name" in body) saved.name = body.name;
      }
      return json(settings());
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a browser tab's title names this magpie", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-instance-name-${i}.png`) });
      }
      await browser.close();
    });
    const open = async (lang, web, width = 900) => {
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], saved = { name: "" };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, web, saved, posts));
      await watchSettingsReads(page);
      await page.goto("http://localhost:3430/?view=settings&tab=network");
      await settingsDrawn(page);
      const tr = (s) => page.evaluate((s) => t(s), s);
      return { page, errors, posts, tr };
    };

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const { page, errors, posts, tr } = await open(lang, true);
        assert.equal(await page.title(), "nas · magpie", "MAGPIE_NAME's name from the first paint");
        const more = page.locator("#netMore");
        const field = page.locator("#netMoreList input.instance-name");
        await field.waitFor({ state: "attached" });
        assert.equal(await more.evaluate((d) => d.open), false, "under Advanced, closed while no name is set");
        await more.locator("summary").click();
        await field.waitFor();
        assert.equal(await field.inputValue(), "");
        assert.equal(await field.getAttribute("placeholder"), "nas");
        const row = page.locator("#netMoreList .instance-name-row");
        assert.equal(await row.locator(".name").textContent(), await tr("Name"));
        assert.equal(await row.locator(".sub").textContent(), await tr("Shown in the browser tab’s title"));
        if (lang !== "en") assert.notEqual(await tr("Shown in the browser tab’s title"), "Shown in the browser tab’s title", "translated");
        await field.fill("  Tokyo   box ");
        await field.press("Enter");
        await page.waitForFunction(() => document.title === "Tokyo box · magpie");
        assert.equal(posts.at(-1).name, "Tokyo box");
        assert.equal(await page.locator("#netMoreList input.instance-name").inputValue(), "Tokyo box");
        // cleared: the variable's name again
        await page.locator("#netMoreList input.instance-name").fill("");
        await page.locator("#netMoreList input.instance-name").press("Enter");
        await page.waitForFunction(() => document.title === "nas · magpie");
        assert.equal(posts.at(-1).name, "");
        assert.deepEqual(errors, []);
      });
    }

    await t.test("a name set opens Advanced, and fits a narrow window", async () => {
      const { page, errors } = await open("en", true, 360);
      await page.locator("#netMore summary").click();
      const field = page.locator("#netMoreList input.instance-name");
      await field.fill("A rather long name for a server in a closet");
      await field.press("Enter");
      await page.waitForFunction(() => document.title.startsWith("A rather long"));
      await page.reload();
      await settingsDrawn(page);
      await page.locator("#netMoreList input.instance-name").waitFor();
      assert.equal(await page.locator("#netMore").evaluate((d) => d.open), true);
      assert.equal(await page.title(), "A rather long name for a server in a closet · magpie");
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(over <= 0, `wider than the window by ${over}px`);
      const box = await page.locator("#netMoreList .instance-name-row").boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 360, `row at ${box.x}+${box.width}`);
      assert.deepEqual(errors, []);
    });

    await t.test("the app's window keeps its title", async () => {
      const { page, errors } = await open("en", false);
      assert.equal(await page.title(), "magpie");
      assert.deepEqual(errors, []);
    });
  });
}
