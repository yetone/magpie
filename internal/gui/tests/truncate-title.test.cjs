// Run with Node's test runner and Playwright on the module path; see README.md.
// thedavidweng (#721): the Agents page cut a status line short — "已接入 · 在
// /model 里切换 opus · son…" — and hovering it showed nothing more. Text cut
// with an ellipsis now gets its full text as a title on hover; text that fits
// gets none, and a title the app wrote itself is kept.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const line = "Connected · switch the opus · sonnet · haiku tiers in /model";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": text cut with an ellipsis shows its full text on hover", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 420, height: 600 } });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(() => browser.close());
    await page.goto("http://magpie.test/");
    await page.waitForFunction(() => document.readyState === "complete");
    // a narrow column of single lines, as a status line sits in an agent's row
    await page.evaluate((line) => {
      const box = document.createElement("div");
      box.id = "fixture";
      box.style.cssText = "position:fixed;top:120px;left:20px;width:160px;z-index:99999;background:#fff";
      const cut = "white-space:nowrap;overflow:hidden;text-overflow:ellipsis;display:block";
      box.innerHTML = `
        <div id="cut" style="${cut}"><span id="cut-inner">${line}</span></div>
        <div id="fits" style="${cut}">Connected</div>
        <div id="own" style="${cut}" title="the app's own words">${line}</div>`;
      document.body.append(box);
    }, line);
    const title = (sel) => page.locator(sel).getAttribute("title");
    const hover = async (sel) => {
      await page.mouse.move(0, 0);
      await page.locator(sel).hover({ position: { x: 4, y: 4 } }); // the cut text's start, in sight
      await page.waitForTimeout(50);
    };

    await t.test("a cut line, hovered on its inner text, gets its full text", async () => {
      assert(await page.locator("#cut").evaluate((e) => e.scrollWidth > e.clientWidth), "the line must be cut");
      await hover("#cut-inner");
      assert.equal(await title("#cut"), line);
    });

    await t.test("a line that fits gets no title", async () => {
      await hover("#fits");
      assert.equal(await title("#fits"), null);
    });

    await t.test("a title the app wrote is kept", async () => {
      await hover("#own");
      assert.equal(await title("#own"), "the app's own words");
    });

    await t.test("once the line fits again, the title put there goes", async () => {
      await page.locator("#fixture").evaluate((e) => { e.style.width = "600px"; });
      await hover("#cut-inner");
      assert.equal(await title("#cut"), null);
    });

    assert.deepEqual(errors, []);
  });
}
