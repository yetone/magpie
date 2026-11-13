// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider editor of many accounts opens without a layout, or a slow
// read, an account (#5310). Each account has a control (a segmented strip)
// whose thumb is put under the option picked. Each control placed itself
// and its thumb when drawn, a read after the last one's write, so every
// control laid the editor out again; and the thumb's place was read from
// offsetLeft, offsetTop and computed style, which WebKit answers by walking
// every sibling before the element. At 600 accounts WebKit took 7.9s to open
// the editor. This counts, while the editor opens and takes its readings,
// the reads that lay the page out after a write (forced layouts) and the
// reads of those sibling-walking getters, in both engines and every
// language, and holds each to the same at 160 accounts as at 40; and every
// thumb under its option. MAGPIE_THUMB_ASSETS points the page at an older
// asset tree, to watch this go red there.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_THUMB_ASSETS || "../assets");
const at = (ms) => new Date(Date.now() + ms).toISOString();

function serve(lang, n) {
  const settings = { lang, theme: "light", currency: "usd" };
  const users = Array.from({ length: n }, (_, i) => `user${i}@example.com`);
  const quotas = users.map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [
    { name: "5 hours", used: (i * 7) % 100, resetsAt: at(36e5 * ((i % 5) + 1)) },
    { name: "7 days", used: (i * 13) % 100, resetsAt: at(864e5 * ((i % 7) + 1)) },
  ] }));
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [{
      id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
      account: { agent: "claude", agentName: "Claude Code", user: users[0], plan: "max",
        logins: users.map((user, i) => ({ user, plan: "max", active: i === 0, on: true })) },
    }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(quotas.map((q) => [q.user, q])));
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// a count of the reads of a layout made after the page changed (a node
// added, a style or class written: the observer's records are taken in
// each read, so a read with any waiting lays the page out again), and of
// the reads of the getters WebKit answers by walking the siblings before
const watch = () => {
  const w = (window.__thumbs = { on: false, forced: 0, slow: 0 });
  const mo = new MutationObserver(() => {});
  mo.observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["style", "class"] });
  w.take = () => mo.takeRecords().length;
  const read = (slow) => () => {
    if (!w.on) return;
    if (w.take()) w.forced++;
    if (slow) w.slow++;
  };
  const wrap = (proto, name, slow) => {
    const d = Object.getOwnPropertyDescriptor(proto, name), seen = read(slow);
    Object.defineProperty(proto, name, { ...d, get() { seen(); return d.get.call(this); } });
  };
  for (const name of ["offsetHeight", "offsetWidth"]) wrap(HTMLElement.prototype, name, false);
  for (const name of ["offsetLeft", "offsetTop", "offsetParent"]) wrap(HTMLElement.prototype, name, true);
  for (const name of ["clientWidth", "clientHeight", "scrollWidth", "scrollHeight"]) wrap(Element.prototype, name, false);
  for (const name of ["scrollLeft", "scrollTop"]) wrap(Element.prototype, name, true);
  const rect = Element.prototype.getBoundingClientRect, fast = read(false);
  Element.prototype.getBoundingClientRect = function () { fast(); return rect.call(this); };
  const cs = window.getComputedStyle, slow = read(true);
  window.getComputedStyle = function (...a) { slow(); return cs.apply(this, a); };
};

// the reads made while the editor of n accounts opens, until its readings
// are in and the frames after them drawn; and each control's thumb, as
// far off its option as it is
async function opening(browser, lang, n) {
  const context = await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.addInitScript(watch);
  await page.route("**/*", serve(lang, n));
  await page.goto("http://magpie.test/?view=providers");
  const row = page.locator(".row.provider", { hasText: "Claude" }).first();
  await row.waitFor();
  await page.evaluate(async () => {
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    window.__thumbs.take();
    window.__thumbs.on = true;
  });
  await row.click();
  await page.waitForFunction((n) => document.querySelectorAll(".editor .acc.with-aq .aq-w").length >= n * 2, n);
  const seen = await page.evaluate(async () => {
    const w = window.__thumbs;
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    w.on = false;
    // where each shown thumb is drawn, against the option picked under it;
    // then Advanced opened and every one again (#5886: a control in a
    // closed Advanced is put when it opens, and Chromium answers
    // offsetWidth inside the closed one, so shown is checkVisibility's)
    const boxes = [...document.querySelectorAll(".editor .segs")];
    const off = [];
    const check = (when) => {
      for (const box of boxes) {
        const on = box.querySelector(":scope > .on"), th = box.querySelector(":scope > .thumb");
        if (!on || !on.checkVisibility()) continue;
        const a = on.getBoundingClientRect(), b = th?.getBoundingClientRect();
        if (!b || Math.abs(a.left - b.left) > 1 || Math.abs(a.width - b.width) > 1) off.push(`${when} ${box.dataset.kind}: option ${a.left.toFixed(1)}+${a.width.toFixed(1)}, thumb ${b ? b.left.toFixed(1) + "+" + b.width.toFixed(1) : "none"}`);
      }
    };
    check("shown");
    for (const d of document.querySelectorAll(".editor details:not([open])")) d.open = true;
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    check("opened");
    return { forced: w.forced, slow: w.slow, boxes: boxes.length, off };
  });
  await context.close();
  assert.deepEqual(errors, []);
  return seen;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: an editor of many accounts opens in no more layouts or slow reads at 160 than at 40`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const few = await opening(browser, lang, 40);
      const many = await opening(browser, lang, 160);
      // what the count is about happened: a control an account, at least,
      // each thumb under its option
      assert.ok(few.boxes >= 40 && many.boxes >= 160, `controls: ${few.boxes} at 40 accounts, ${many.boxes} at 160`);
      assert.deepEqual(few.off, []);
      assert.deepEqual(many.off, []);
      assert.ok(few.forced > 0, "opening lays the editor out");
      // a frame's observer can land on either side of the count's end, and
      // the page's own reads (about 200 slow ones in WebKit at either count)
      // vary by the frames the open takes: holdModal reads a few each
      // frame, and WebKit's 40 sometimes ends ~5 frames sooner (188 against
      // 208, up to 36 apart on mote). Room for that, none for a read an
      // account, 120 more here
      assert.ok(many.forced <= few.forced + 24, `forced layouts: ${few.forced} at 40 accounts, ${many.forced} at 160`);
      assert.ok(many.slow <= few.slow + 60, `offsetLeft/offsetTop/offsetParent/scroll/computed-style reads: ${few.slow} at 40 accounts, ${many.slow} at 160`);
    });
  }
}
