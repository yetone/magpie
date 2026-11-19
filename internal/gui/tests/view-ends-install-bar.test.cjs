// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugins > Discover's install box ("Have one in mind?") is held at the
// window's foot, and the Scroll to top / Scroll to bottom capsule
// (scrollends.js) sat over its right end, covering the Install button
// (WaiooT on X, zh, a ~1000px window). With the page a screen down the
// capsule is above the box and over none of its parts, and Install takes a
// click at its right end. The Library's save and pick bars are held at the
// foot the same way (a stand-in sticky bar of each class is put at the end of
// the view here, as the Library's own fixtures are heavy); the capsule steps
// over them too. Both engines, every language, at 390, 560, 980 and 1280.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const listings = Array.from({ length: 40 }, (_, i) => ({
  package: "@magpie-community/opencode-p" + i + "-auth", name: "Plan " + i, community: true, providers: ["p" + i],
  summary: { en: "Signs in to plan " + i + " and serves its models" }, npm: { version: "0.1." + i },
}));
const state = { bun: true, bunVersion: "1.3.0", plugins: [], picker: true, registry: { url: "https://registry.npmjs.org", from: "npmrc", path: "~/.npmrc" } };

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
    if (url.pathname === "/api/plugins/github") return json({ repos: [], topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


const SIZES = [[390, 700], [560, 640], [980, 620], [1280, 600]];

// the reader's wheel takes the view to a share of its scroll (magpie puts
// back a scroll no reader asked for), and waits for it to stop there
async function scrollTo(page, f) {
  const v = page.locator("#view-plugins");
  const b = await v.boundingBox();
  await page.mouse.move(b.x + b.width / 2, b.y + Math.min(b.height / 2, 200));
  const near = ([top, max]) => f >= 1 ? top >= max - 1 : Math.abs(top - max * f) <= 40;
  const read = () => v.evaluate((e) => [e.scrollTop, e.scrollHeight - e.clientHeight]);
  for (let i = 0; i < 100; i++) {
    let at = await read();
    if (near(at)) {
      await page.waitForTimeout(250);
      at = await read();
      if (near(at)) return at;
    }
    await page.mouse.wheel(0, Math.max(-600, Math.min(600, Math.round(at[1] * f) - at[0] + (f >= 1 ? 50 : 0))));
    await page.waitForTimeout(60);
  }
  return read();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the scroll capsule is above Discover's install box, not over Install", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: SIZES[0][0], height: SIZES[0][1] } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".pm-manual").waitFor();
        await view.locator(".pm-bar .pm-kinds .opt").nth(1).click();
        await page.waitForFunction((n) => document.querySelectorAll("#view-plugins .pm-card").length >= n, listings.length);
        for (const [w, h] of SIZES) {
          const size = `${w}x${h}`;
          await page.setViewportSize({ width: w, height: h });
          await scrollTo(page, 0.6);
          await page.waitForFunction(() => document.querySelector(".view-ends.on"));
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 250)))));
          const m = await page.evaluate(() => {
            const box = (e) => { const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right }; };
            const man = document.querySelector("#view-plugins .pm-manual");
            return { ends: box(document.querySelector(".view-ends")), man: box(man), parts: [...man.querySelectorAll("button, input, a")].map((e) => ({ what: e.className || e.tagName, ...box(e) })), install: box(man.querySelector("button[type=submit]")) };
          });
          const hit = (a, b) => a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
          for (const p of m.parts) assert(!hit(p, m.ends), `${size}: the capsule over ${p.what}: ${JSON.stringify([p, m.ends])}`);
          assert(m.ends.bottom <= m.man.top + 1, `${size}: the capsule above the box: ${JSON.stringify([m.ends, m.man])}`);
          // Install's right end is Install's
          const at = await page.evaluate((r) => document.elementFromPoint(r.right - 3, (r.top + r.bottom) / 2)?.closest("button")?.type, m.install);
          assert.equal(at, "submit", `${size}: Install's right end is Install's`);

          // the Library's bars, held the same way: stand-ins at the view's end
          for (const cls of ["lib-savebar", "lib-pickbar"]) {
            const r = await page.evaluate((cls) => {
              const v = document.querySelector("#view-plugins");
              const man = v.querySelector(".pm-manual");
              man.hidden = true;
              const bar = document.createElement("div");
              bar.className = cls;
              bar.style.cssText = "position:sticky;bottom:8px;height:44px;margin-top:8px;display:block";
              v.append(bar);
              return new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(() => {
                const e = document.querySelector(".view-ends").getBoundingClientRect(), b = bar.getBoundingClientRect();
                bar.remove();
                man.hidden = false;
                res({ ends: e, bar: b, on: document.querySelector(".view-ends").classList.contains("on") });
              }, 250))));
            }, cls);
            assert(r.on, `${size} ${cls}: the capsule is in`);
            assert(r.ends.bottom <= r.bar.top + 1, `${size} ${cls}: the capsule above the bar: ${JSON.stringify(r)}`);
          }
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
