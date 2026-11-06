// A Mac window hidden with orderOut keeps its page's layer buffers
// (#5530: 1133 MB in 335 buffers for a reporter). Out of sight 30s, the
// window's page is put asleep (content-visibility: hidden on body), so
// WebKit lets go of every layer but the root's; shown, it draws again with
// its content and scroll positions as they were.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const alternateAppJS = process.env.MAGPIE_APP_JS;

function serve() {
  const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs={lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname === "/app.js" && alternateAppJS) {
      return route.fulfill({ body: await fs.readFile(alternateAppJS), contentType: "text/javascript" });
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  async function open(t, mode) {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.clock.install();
    await page.route("**/*", serve());
    await page.goto(`http://magpie.test/?view=providers${mode ? "&mode=" + mode : ""}`);
    await page.waitForFunction(() => typeof window.magpieWindowVisibility === "function");
    return page;
  }
  const look = (page) => page.evaluate(() => ({
    asleep: document.documentElement.classList.contains("magpie-asleep"),
    drawn: getComputedStyle(document.body).contentVisibility !== "hidden",
    scrolled: document.querySelector("#sleepProbe")?.scrollTop,
  }));

  test(`${engine}: a window out of sight 30s lets go of its layers, and draws again shown`, async (t) => {
    const page = await open(t);
    await page.evaluate(() => {
      const box = document.createElement("div");
      box.id = "sleepProbe";
      box.style.cssText = "position:fixed;top:0;left:0;width:200px;height:100px;overflow:auto";
      box.innerHTML = '<div style="height:2000px">deep</div>';
      document.body.append(box);
      box.scrollTop = 600;
      window.magpieWindowVisibility(false);
    });
    await page.clock.runFor(29000);
    assert.deepEqual(await look(page), { asleep: false, drawn: true, scrolled: 600 });
    await page.clock.runFor(1500);
    const asleep = await look(page);
    assert.equal(asleep.asleep, true);
    if (await page.evaluate(() => CSS.supports("content-visibility", "hidden"))) assert.equal(asleep.drawn, false);

    await page.evaluate(() => window.magpieWindowVisibility(true));
    assert.deepEqual(await look(page), { asleep: false, drawn: true, scrolled: 600 });
    assert.equal(await page.evaluate(() => document.querySelector("#sleepProbe").textContent), "deep");
  });

  test(`${engine}: a window shown again within 30s is never put asleep`, async (t) => {
    const page = await open(t);
    await page.evaluate(() => window.magpieWindowVisibility(false));
    await page.clock.runFor(20000);
    await page.evaluate(() => window.magpieWindowVisibility(true));
    await page.clock.runFor(60000);
    assert.equal((await look(page)).asleep, false);
    // hidden again, the 30s start over
    await page.evaluate(() => window.magpieWindowVisibility(false));
    await page.clock.runFor(20000);
    assert.equal((await look(page)).asleep, false);
  });

  test(`${engine}: the tray panel is never put asleep`, async (t) => {
    const page = await open(t, "panel");
    await page.evaluate(() => window.magpieWindowVisibility(false));
    await page.clock.runFor(60000);
    assert.equal((await look(page)).asleep, false);
  });
}
