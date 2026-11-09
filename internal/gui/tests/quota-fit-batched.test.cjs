// Run with Node's test runner and Playwright on the module path; see README.md.
// A card of many accounts is drawn again without laying the page out once
// an account (#5310). fitQuota stacks each window's count and gives the
// names beside each other one height; it read each group's names after
// resetting them, and every such read laid out the whole card again: at
// 600 accounts in full, names wrapped, a render took WebKit seconds and
// held the WebContent process at 100% CPU. Every group's sizes are read
// before any is changed now. This counts the reads of a layout made after
// a write a render has made (a forced layout), in both engines and every
// language, and holds the count to the same at 40 accounts as at 160.
// MAGPIE_FIT_ASSETS points the page at an older asset tree, to watch this
// go red there.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_FIT_ASSETS || "../assets");
const at = (ms) => new Date(Date.now() + ms).toISOString();
const quotasOf = (n) => Array.from({ length: n }, (_, i) => ({
  provider: "claude", name: "Claude", icon: "claudecode-color", user: `user${i}@example.com`,
  windows: [
    { name: "5 hours", used: (i * 7) % 100, resetsAt: at(36e5 * ((i % 5) + 1)) },
    // wraps beside the short one at this width: the names get a height
    { name: "7 days of every Opus and Sonnet model together", used: (i * 13) % 100, resetsAt: at(864e5 * ((i % 6) + 1)) },
  ],
}));

function serve(lang, quotas) {
  const settings = { lang, theme: "light", currency: "usd" };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
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

// every account in full, none folded away; and a count of the reads of a
// layout made after the page changed (a node added, a style or class
// written): the observer's records are taken in each read, so a read with
// any waiting is one that lays the page out again
const watch = () => {
  try {
    localStorage.setItem("magpie.usageOpen", JSON.stringify(["claude"]));
    localStorage.setItem("magpie.usageAcctsAll", JSON.stringify(["claude"]));
  } catch {}
  const w = (window.__fit = { on: false, forced: 0, heights: 0 });
  const mo = new MutationObserver(() => {});
  mo.observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["style", "class"] });
  const take = () => {
    const rs = mo.takeRecords();
    if (w.on) w.heights += rs.filter((r) => r.attributeName === "style" && r.target.classList?.contains("quota-labels")).length;
    return rs.length;
  };
  w.take = take;
  const read = () => { if (take() && w.on) w.forced++; };
  for (const name of ["offsetTop", "offsetHeight", "offsetWidth", "clientWidth", "clientHeight"]) {
    const proto = name.startsWith("offset") ? HTMLElement.prototype : Element.prototype;
    const d = Object.getOwnPropertyDescriptor(proto, name);
    Object.defineProperty(proto, name, { ...d, get() { read(); return d.get.call(this); } });
  }
  const rect = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function () { read(); return rect.call(this); };
};

// the forced layouts of one render of the Usage page's cards, with the
// frame after it that fits what its observer saw
async function forcedLayouts(browser, lang, n) {
  const context = await browser.newContext({ viewport: { width: 520, height: 800 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(30000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.addInitScript(watch);
  await page.route("**/*", serve(lang, quotasOf(n)));
  await page.goto("http://magpie.test/?view=usage&tab=usage");
  await page.waitForFunction((n) => document.querySelectorAll("#subscriptionUsage .quota-windows:not(.brief)").length >= n, n);
  const seen = await page.evaluate(async () => {
    const w = window.__fit;
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    w.take();
    w.on = true; w.forced = 0; w.heights = 0;
    renderQuotas(true);
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    w.take();
    w.on = false;
    const groups = [...document.querySelectorAll("#subscriptionUsage .quota-windows")];
    return {
      forced: w.forced, heights: w.heights,
      groups: groups.length,
      stacked: groups.filter((g) => g.classList.contains("stacked")).length,
      evened: document.querySelectorAll("#subscriptionUsage .quota-labels[style*='min-height']").length,
    };
  });
  await context.close();
  assert.deepEqual(errors, []);
  return seen;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a card of many accounts is drawn again in no more layouts at 160 than at 40`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const few = await forcedLayouts(browser, lang, 40);
      const many = await forcedLayouts(browser, lang, 160);
      // what the count is about happened: every account's windows drawn,
      // the names' heights written, in both renders
      assert.equal(few.groups, 40);
      assert.equal(many.groups, 160);
      assert.ok(few.heights >= 40 && many.heights >= 160, `names' heights written: ${few.heights}, ${many.heights}`);
      // the names wrapped and were given one height, as at any count
      assert.equal(few.evened, 40);
      assert.equal(many.evened, 160);
      assert.ok(few.forced > 0, "a render lays the card out");
      // a frame's observer can land on either side of the count's end: room
      // for that, none for a layout an account
      assert.ok(many.forced <= few.forced + 4, `forced layouts: ${few.forced} at 40 accounts, ${many.forced} at 160`);
    });
  }
}
