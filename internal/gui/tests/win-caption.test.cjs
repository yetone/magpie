// Run with Node's test runner and Playwright on the module path; see README.md.
// 258a14 on X: with a wallpaper, Windows' own white title bar sat over the
// page, cut off from it. The main window on Windows is frameless now
// (mainwindow.go mainFrameless) and its header is the title bar, as on the
// Mac: the page draws minimise, maximise and close at its right (#wincap).
// They sit in the header's height at the window's top-right corner, cover
// none of the header's own tools (search, refresh, pin, Settings), stay
// clicks while the rest of the bar drags, stay reachable over a dialog, and
// call the window's own Minimise, ToggleMaximise and Close. Maximise turns
// into restore when the app says the window is maximised (captionJS). A
// double click on what drags maximises. The Mac, Linux, the panel and a
// browser page show none. No backend: Wails' runtime is a stand-in that
// keeps what it was asked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, web) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd" };
  const relay = { id: "relay", name: "Relay", icon: "generic", host: "relay.test", chat: "https://relay.test/v1",
    responses: "", anthropic: "", models: [{ id: "fixture-model", name: "Fixture model", on: true }], agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},pin:true,onTop:false};` });
    if (url.pathname === "/wails/runtime.js") {
      return route.fulfill({ contentType: "text/javascript", body: "window.__calls = []; const say = (n) => () => { window.__calls.push(n); return Promise.resolve(); };" +
        "export const Window = { Minimise: say('Minimise'), ToggleMaximise: say('ToggleMaximise'), Close: say('Close'), IsMaximised: () => Promise.resolve(false) }; window.__runtimeReady = true;" });
    }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json({ dir: "/home/u/.magpie/library", backups: "/home/u/.magpie/backups", home: "/home/u", agents: [],
      instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const MAXIMIZE = { en: "Maximize", zh: "最大化", "zh-TW": "最大化", ja: "最大化", de: "Maximieren" };
const RESTORE = { en: "Restore Down", zh: "向下还原", "zh-TW": "向下還原", ja: "元に戻す (縮小)", de: "Verkleinern" };

// every control the bar shows (the header's, and a page's tools pinned in
// it beside the sidebar): where it is, and whether it drags
function barControls(page) {
  return page.evaluate(() => {
    const top = document.querySelector("header.top").getBoundingClientRect();
    return [...document.querySelectorAll("header.top :is(button, input, a, [role=tab]), .pv-head :is(button, input), .lib-head :is(button, input), .pm-head :is(button, input), .set-head :is(button, input, [role=tab])")]
      .filter((e) => e.getClientRects().length && getComputedStyle(e).visibility !== "hidden")
      .map((e) => {
        // a narrow header's tabs scroll in their own box (app.css .packed):
        // what is out of it is not there to click
        const r = e.getBoundingClientRect(), clip = e.closest("#nav")?.getBoundingClientRect();
        const left = clip ? Math.max(r.left, clip.left) : r.left, right = clip ? Math.min(r.right, clip.right) : r.right;
        return { e, r, left, right };
      })
      .filter(({ r, left, right }) => right - left > 4 && r.top < top.bottom && r.bottom > 0)
      .map(({ e, r, left, right }) => ({ id: e.id || e.getAttribute("aria-label") || e.className || e.tagName, cls: String(e.className), left, right,
        hit: (() => { const h = document.elementFromPoint((left + right) / 2, r.top + r.height / 2); return !!h && (h === e || e.contains(h)); })(),
        drag: getComputedStyle(e).getPropertyValue("--wails-draggable").trim() }));
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Windows' header is the title bar, with its caption buttons`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => { await browser.close(); });
      const open = async ({ platform = "Win32", mode = "window", web = false, width = 1100, view = "providers" } = {}) => {
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        await context.addInitScript((p) => Object.defineProperty(Navigator.prototype, "platform", { get: () => p }), platform);
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto(`http://magpie.test/?mode=${mode}`);
        await page.waitForFunction(() => document.querySelector("header.top"));
        if (!web) await page.waitForFunction(() => window.__runtimeReady === true);
        if (mode === "window") await page.waitForSelector("#nav button.on");
        if (mode === "window" && view !== "agents") {
          await page.evaluate((v) => show(v), view);
          await page.waitForSelector(`#view-${view}:not([hidden])`);
        }
        await page.waitForTimeout(300);
        return { page, errors, context };
      };

      for (const width of [1100, 640]) for (const view of ["providers", "library", "plugins", "settings"]) {
        const where = `${width}px ${view}`;
        const { page, errors, context } = await open({ width, view });
        const cap = await page.evaluate(() => {
          const c = document.querySelector("#wincap"), top = document.querySelector("header.top").getBoundingClientRect();
          if (!c || !c.getClientRects().length) return null;
          return { top: top.height, drag: getComputedStyle(c).getPropertyValue("--wails-draggable").trim(),
            buttons: [...c.querySelectorAll("button")].map((b) => { const r = b.getBoundingClientRect(); const h = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
              return { cap: b.dataset.cap, left: r.left, right: r.right, top: r.top, bottom: r.bottom, title: b.title, glyph: b.textContent, hit: h === b }; }) };
        });
        assert.ok(cap, `${where}: the caption buttons show`);
        assert.deepEqual(cap.buttons.map((b) => b.cap), ["min", "max", "close"], `${where}: minimise, maximise, close`);
        assert.equal(cap.drag, "no-drag", `${where}: the caption buttons are clicks, not the window's drag`);
        const [min, , close] = cap.buttons;
        assert.ok(Math.abs(close.right - width) < 0.5 && close.top === 0, `${where}: close is at the window's top-right corner: ${JSON.stringify(close)}`);
        for (const b of cap.buttons) {
          assert.ok(b.hit, `${where}: ${b.cap} is under the pointer, nothing over it`);
          assert.ok(b.top === 0 && Math.abs(b.bottom - cap.top) < 0.5, `${where}: ${b.cap} fills the header's height: ${JSON.stringify(b)} in ${cap.top}`);
        }
        assert.equal(cap.buttons[1].title, MAXIMIZE[lang], `${where}: maximise's tooltip in ${lang}`);
        assert.equal(cap.buttons[1].glyph, "");
        // the header's own tools are left of the caption buttons and stay clicks
        const controls = await barControls(page);
        assert.ok(controls.some((c) => c.id === "sync"), `${where}: refresh is in the bar`);
        assert.ok(controls.some((c) => c.id === "pin"), `${where}: the pin is in the bar`);
        for (const c of controls) {
          assert.ok(c.right <= min.left + 0.5, `${where}: ${c.id} runs under the caption buttons: ${JSON.stringify(c)} vs ${min.left}`);
          assert.ok(c.hit, `${where}: ${c.id} is covered`);
          assert.equal(c.drag, "no-drag", `${where}: ${c.id} drags the window`);
        }
        // beside the sidebar the page's own tools are in the bar too: the
        // search boxes, the Library folder
        if (width === 1100) {
          const own = { settings: "setQ", library: "lib-more" }[view];
          if (own) assert.ok(controls.some((c) => c.id === own || c.cls.split(" ").includes(own)), `${where}: ${own} is in the bar: ${JSON.stringify(controls.map((c) => c.id))}`);
        }
        // the bar itself drags, between its tools
        assert.equal(await page.locator("header.top").evaluate((h) => getComputedStyle(h).getPropertyValue("--wails-draggable").trim()), "drag", `${where}: the header drags`);
        // (a header packed as narrow as it goes leaves the brand out, as on
        // every system)
        if (!(await page.locator(".top.packed").count())) assert.ok(await page.locator(width >= 1024 ? "#side .side-top .brand" : ".top .brand").isVisible(), `${where}: the logo and name show`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }

      // the buttons ask the window; maximise becomes restore and back
      {
        const { page, errors, context } = await open();
        for (const cap of ["min", "max", "close"]) await page.locator(`#wincap [data-cap="${cap}"]`).click();
        await page.waitForFunction(() => window.__calls.length >= 3);
        assert.deepEqual(await page.evaluate(() => window.__calls), ["Minimise", "ToggleMaximise", "Close"]);
        await page.evaluate(() => window.magpieCaption(true));
        const max = page.locator('#wincap [data-cap="max"]');
        assert.equal(await max.textContent(), "", "restore's glyph");
        assert.equal(await max.getAttribute("title"), RESTORE[lang], "restore's tooltip");
        await page.evaluate(() => window.magpieCaption(false));
        assert.equal(await max.textContent(), "", "maximise's glyph again");
        assert.equal(await max.getAttribute("title"), MAXIMIZE[lang]);
        // a double click on the bar maximises; on a tool it doesn't
        await page.evaluate(() => { window.__calls.length = 0; });
        const bar = await page.evaluate(() => {
          const top = document.querySelector("header.top").getBoundingClientRect();
          for (let x = top.right - 160; x > top.left; x -= 4) {
            const e = document.elementFromPoint(x, top.top + top.height / 2);
            if (e && !e.closest("button, nav, input, a") && getComputedStyle(e).getPropertyValue("--wails-draggable").trim() === "drag") return { x, y: top.top + top.height / 2 };
          }
          return null;
        });
        assert.ok(bar, "an empty stretch of the bar");
        await page.mouse.dblclick(bar.x, bar.y);
        await page.locator("#sync").dblclick();
        await page.waitForTimeout(100);
        assert.deepEqual(await page.evaluate(() => window.__calls), ["ToggleMaximise"], "only the bar's double click maximises");
        // a dialog open: the buttons stay over its scrim, and still close
        await page.locator('#providers .row.provider[data-id="relay"]').click();
        await page.getByRole("dialog").first().waitFor();
        await page.waitForTimeout(400);
        assert.ok(await page.evaluate(() => document.querySelector("header.top").inert), "the header is inert behind the dialog");
        assert.ok(!(await page.evaluate(() => document.querySelector("#wincap").inert)), "the caption buttons aren't");
        await page.evaluate(() => { window.__calls.length = 0; });
        const box = await page.locator('#wincap [data-cap="close"]').boundingBox();
        assert.ok(await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.dataset.cap === "close", [box.x + box.width / 2, box.y + box.height / 2]), "close is over the scrim");
        await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
        await page.waitForFunction(() => window.__calls.length);
        assert.deepEqual(await page.evaluate(() => window.__calls), ["Close"]);
        assert.deepEqual(errors, []);
        await context.close();
      }

      // the app may say the window is maximised before app.js is up
      {
        const context = await browser.newContext({ viewport: { width: 1100, height: 700 } });
        await context.addInitScript(() => { Object.defineProperty(Navigator.prototype, "platform", { get: () => "Win32" }); window.magpieMaximised = true; });
        const page = await context.newPage();
        await page.route("**/*", serve(lang, false));
        await page.goto("http://magpie.test/");
        await page.waitForSelector("#nav button.on");
        assert.equal(await page.locator('#wincap [data-cap="max"]').textContent(), "", "restore's glyph from the first paint");
        await context.close();
      }

      // the Mac and Linux keep their own buttons, the panel and a browser page have none
      for (const o of [{ platform: "MacIntel" }, { platform: "Linux x86_64" }, { mode: "panel", width: 440 }, { web: true }]) {
        const { page, errors, context } = await open(o);
        assert.ok(!(await page.locator("#wincap").isVisible()), `${JSON.stringify(o)}: no caption buttons`);
        assert.notEqual(await page.evaluate(() => getComputedStyle(document.querySelector("header.top")).paddingRight), "146px", `${JSON.stringify(o)}: no room kept for them`);
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }
}
