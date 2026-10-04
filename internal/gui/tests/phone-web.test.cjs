// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie web on a phone (jiakun_zhao on X: 移动端的显示效果非常不友好，甚至滚动
//会抽搐). The header is two rows, the magpie and the icons over the tabs at
// their size, sliding sideways, the one open in sight; a setting's control
// too wide beside its name goes under it rather than cut the name; iOS isn't
// let zoom into a field it focuses. And a flick's scroll, which goes on
// after the finger is lifted with no touch event, is the reader's: the page
// was put back to where the finger left it, frame after frame. A desktop
// browser and the app's window keep their one-row header.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit, devices } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(web) {
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang: "zh", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"zh",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: [], picker: false } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(browser, opts, web, view) {
  const context = await browser.newContext({ reducedMotion: "reduce", ...opts });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  await page.route("**/*", serve(web));
  await page.goto("http://magpie.test/?view=" + view);
  await page.waitForSelector("#view-" + view, { state: "visible" });
  await page.waitForTimeout(300);
  return page;
}
const box = (page, s) => page.locator(s).first().boundingBox();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const phone = { ...devices["iPhone 13"] };
  if (engine === "chromium") delete phone.defaultBrowserType;
  test(`${engine}: phone layout is ready while app.js is still downloading`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ ...phone, reducedMotion: "reduce" });
    const page = await context.newPage();
    let release;
    const pending = new Promise((resolve) => { release = resolve; });
    t.after(() => release());
    const handle = serve(true);
    await page.route("**/*", async (route) => {
      if (new URL(route.request().url()).pathname === "/app.js") await pending;
      await handle(route);
    });
    const loaded = page.goto("http://magpie.test/");
    try {
      await page.waitForSelector("#nav", { state: "visible" });
      const [nav, actions] = [await box(page, "#nav"), await box(page, ".top .actions")];
      assert.ok(nav.y >= actions.y + actions.height - 1, "tabs already have their own row before app.js runs");
      assert.equal(await page.locator("#open").isVisible(), false, "the window button never flashes");
      assert.equal(await page.locator("#winclose").isVisible(), false, "the close button never flashes");
      assert.match(await page.locator('meta[name="viewport"]').getAttribute("content"), /maximum-scale=1/, "the existing iOS viewport policy applies before app.js runs");
      release();
      await loaded;
      const readyNav = await box(page, "#nav");
      assert.ok(Math.abs(readyNav.y - nav.y) <= 1, "the navigation stays in its initial row after startup");
    } finally {
      release();
      await loaded;
    }
  });

  test(`${engine}: magpie web on a phone`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    // the header: two rows, the tabs at their size, the one open in sight
    let page = await open(browser, phone, true, "plugins");
    const [top, nav, actions, brand] = [await box(page, ".top"), await box(page, "#nav"), await box(page, ".top .actions"), await box(page, ".top .brand")];
    assert.ok(nav.y >= actions.y + actions.height - 1 && nav.y >= brand.y + brand.height - 1, "the tabs are under the magpie and the icons");
    assert.ok(nav.width >= top.width - 30, `the tabs take the row: ${nav.width} of ${top.width}`);
    const tab = await page.locator("#nav button").first().evaluate((b) => [parseFloat(getComputedStyle(b).fontSize), b.getBoundingClientRect().height]);
    assert.ok(tab[0] >= 13 && tab[1] >= 30, `a tab is readable and tappable: ${tab}`);
    const on = await box(page, "#nav button.on");
    assert.ok(on.x >= nav.x - 1 && on.x + on.width <= nav.x + nav.width + 1, "Plugins, open, is in sight");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 390, "nothing runs off the side");
    assert.equal(await page.locator(".top").getAttribute("class"), "top", "none of the narrow window's squeezing");
    assert.match(await page.locator('meta[name="viewport"]').getAttribute("content"), /maximum-scale=1/, "iOS doesn't zoom into a field");
    assert.deepEqual(page.errors, []);
    await page.context().close();

    // a setting's name isn't cut to a few letters
    page = await open(browser, phone, true, "settings");
    const cut = await page.$$eval(".prefs .row.pref:not(.lan-address-row) .who .name", (ns) => ns.filter((n) => n.offsetParent && n.scrollWidth > n.clientWidth + 1).map((n) => n.textContent));
    assert.deepEqual(cut, [], "every setting's name whole");
    await page.context().close();

    // a flick: its scrolls after the finger is lifted stay
    page = await open(browser, phone, true, "settings");
    await page.waitForSelector("#view-settings .row.pref");
    const at = await page.evaluate(async () => {
      const v = document.querySelector("#view-settings");
      v.append(Object.assign(document.createElement("div"), { style: "height:3000px;flex:none" }));
      const frame = () => new Promise((r) => requestAnimationFrame(r));
      // magpie reads only that a touch happened (WebKit makes no Touch here)
      const touch = (type) => v.dispatchEvent(new Event(type, { bubbles: true }));
      touch("touchstart");
      for (let i = 1; i <= 5; i++) { touch("touchmove"); v.scrollTop += 20; await frame(); }
      touch("touchend");
      // the flick coasts on, slowing, for well past a touch's 250ms
      let went = 100;
      for (let i = 0; i < 60; i++) { const d = Math.max(1, Math.round(20 * (1 - i / 60))); went += d; v.scrollTop += d; await frame(); }
      const left = v.scrollTop;
      await new Promise((r) => setTimeout(r, 400));
      return [went, left, v.scrollTop];
    });
    assert.deepEqual(at, [at[0], at[0], at[0]], "the flick goes all the way and the page stays where it left it");
    // a tap lifts the finger too, but then clicks: what the click redraws
    // doesn't scroll the page
    const tap = await page.evaluate(async () => {
      const v = document.querySelector("#view-settings");
      const frame = () => new Promise((r) => requestAnimationFrame(r));
      const was = v.scrollTop;
      v.dispatchEvent(new Event("touchstart", { bubbles: true }));
      v.dispatchEvent(new Event("touchend", { bubbles: true }));
      v.querySelector(".row.pref").click();
      for (let i = 0; i < 5; i++) { v.scrollTop -= 40; await frame(); }
      await new Promise((r) => setTimeout(r, 100));
      return [was, v.scrollTop];
    });
    assert.equal(tap[1], tap[0], "a tap's click is no flick");
    await page.context().close();
  });

  test(`${engine}: a desktop browser and the window keep one row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [web, width] of [[true, 1100], [false, 560]]) {
      const page = await open(browser, { viewport: { width, height: 700 } }, web, "agents");
      const [top, nav] = [await box(page, ".top"), await box(page, "#nav")];
      assert.ok(nav.y + nav.height <= top.y + top.height + 1, `${web ? "web" : "window"} at ${width}: the tabs in the header's row`);
      assert.ok(top.height <= 50, `one row: ${top.height}`);
      assert.doesNotMatch(await page.locator('meta[name="viewport"]').getAttribute("content"), /maximum-scale/);
      assert.deepEqual(page.errors, []);
      await page.context().close();
    }
  });
}
