// Run with Node's test runner and Playwright on the module path; see README.md.
// Reduce motion in Settings → General (#3813, linull24 and CupOfBread: the
// moving Routing page kept a phone and a computer busy, and only the
// system's own switch stilled it). In both engines, with the API faked:
// - saved on, the page is still from the first paint: every stylesheet's
//   (prefers-reduced-motion: reduce) rule applies, and calm() says so, as
//   with the system's switch;
// - turned off in Settings, the rules are the system's again, and the
//   Routing page's live dot pulses; turned on, it stops, with no reload;
// - a spinner stands still while motion is reduced, and turns otherwise: an
//   endless animation cut to .01ms restarted every frame at a random angle
//   and spun faster than ever (_yukirock on X, 0.1.3036);
// - the choice is saved as reduceMotion.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(st, posted) {
  const settings = () => ({ lang: "en", theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang: "en", theme: "light", web: false, ...(st.reduceMotion ? { reduceMotion: true } : {}) })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// how many of the stylesheets' reduced-motion rules apply now, of all of
// them; what a live dot, put in the page, does; and whether a sign-in
// spinner turns on and on (1) or ends (0)
const looks = (page) => page.evaluate(async () => {
  let reduce = 0, all = 0;
  for (const sh of document.styleSheets) for (const r of sh.cssRules) {
    if (!r.media) continue;
    if (/prefers-reduced-motion:\s*reduce/.test(r.media.mediaText)) reduce++;
    else if (r.media.mediaText === "all") all++;
  }
  const dot = document.createElement("i");
  dot.className = "rt-livedot on";
  document.body.append(dot);
  const anim = getComputedStyle(dot).animationName;
  dot.remove();
  const box = document.createElement("div");
  box.className = "signing";
  box.innerHTML = '<i class="spinner"></i>';
  document.body.append(box);
  const sp = box.firstChild;
  // an endless turn never finishes; a single .01ms one finishes as soon
  // as it starts, which in Chromium's compositor can take a few frames
  const ends = Promise.all(sp.getAnimations().map((a) => a.finished)).then(() => true);
  const spin = (await Promise.race([ends, new Promise((go) => setTimeout(go, 1000, false))])) ? 0 : 1;
  box.remove();
  return { reduce, all, anim, spin, calm: calm(), motion: document.documentElement.dataset.motion || "" };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Reduce motion stills magpie as the system's switch does`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 }, reducedMotion: "no-preference" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const st = { reduceMotion: true }, posted = [];
    await page.route("**/*", serve(st, posted));
    await page.goto("http://magpie.test/?view=settings");
    const on = page.locator("#reduceMotionSegs .opt", { hasText: /^On$/ });
    const off = page.locator("#reduceMotionSegs .opt", { hasText: /^Off$/ });
    await on.waitFor();

    const first = await looks(page);
    assert(first.all > 20, `the reduced-motion rules must apply from the start (${JSON.stringify(first)})`);
    assert.deepEqual({ ...first, all: 0 }, { reduce: 0, all: 0, anim: "none", spin: 0, calm: true, motion: "reduce" });
    assert.match(await on.getAttribute("class"), /\bon\b/);

    await off.click();
    await page.waitForFunction(() => !document.documentElement.dataset.motion);
    const moving = await looks(page);
    assert.deepEqual({ ...moving, reduce: 0 }, { reduce: 0, all: 0, anim: "rtlive", spin: 1, calm: false, motion: "" });
    assert.equal(moving.reduce, first.all, "every rule held must be the system's again");
    assert.equal(posted.at(-1).reduceMotion, false);

    await on.click();
    await page.waitForFunction(() => document.documentElement.dataset.motion === "reduce");
    assert.deepEqual(await looks(page), first, "turned on again, the page is as still as it started");
    assert.equal(posted.at(-1).reduceMotion, true);
    assert.deepEqual(errors, []);
  });
}
