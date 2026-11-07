// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Overview read again stays as it is (jiabubu on Discord):
// - its tab pressed again while it is open (at the top, halfway down, at the
//   foot) neither puts the skeleton up nor moves the page: the figures, the
//   rows and the cards' logos are the same nodes, no card leaves, scrollTop the same, and the
//   refresh beside the period turns while the read is on its way;
// - a refresh of the allowances leaves every card on the page, each card's
//   refresh turning while it is read, and keeps the logos it drew (a logo
//   made afresh blinks out while its picture loads again).
// No backend, the API is faked and its reads held.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_QUIET_ASSETS || path.resolve(__dirname, "../assets"));
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// enough rows that the page scrolls
function overview() {
  const series = Array.from({ length: 7 }, (_, i) => ({ label: "D" + i, time: iso(midnight.getTime() - (6 - i) * 864e5), calls: i + 1, errors: 0, input: 800 * (i + 1), output: 200 * (i + 1), cost: 0.01 }));
  const row = (n, icon) => ({ id: "r" + n, name: "Row " + n, icon, calls: 3, errors: 0, input: 1000 + n, output: 100, cache_read: 0, cost: 0.01 });
  return {
    calls: 28, errors: 0, input: 22400, output: 5600, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.28, bucket: "day", series,
    agents: Array.from({ length: 14 }, (_, i) => row(i, i % 2 ? "codex-color" : "claude-color")),
    models: Array.from({ length: 14 }, (_, i) => row(100 + i, "openai")),
    path: "~/.config/magpie/usage.jsonl",
  };
}
function cards(now) {
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max", user: "a@x.com", readAt: iso(now - 60e3), windows: [{ name: "5 hours", used: 28, resetsAt: iso(now + 2 * H) }] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "b@x.com", readAt: iso(now - 60e3), windows: [{ name: "5 hours", used: 50, resetsAt: iso(now + 2 * H) }] },
    { provider: "kimi", name: "Kimi", icon: "kimi-color", readAt: iso(now - 60e3), windows: [{ name: "Week", used: 10, resetsAt: iso(now + 50 * H) }] },
  ];
}

function serve(lang, st) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage") { st.usage++; await st.usageGate; return json(overview()); }
    if (url.pathname === "/api/usage/quotas") { st.quotas++; await st.quotaGate; st.quotasDone++; return json(cards(st.now)); }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

// the cards are drawn again with each read of the allowances (their resets
// count down only so), in one go: their logos are what is kept
const KEEP = "#stats .kpi, #chart .bar, #usageAgents .row, #usageModels .row, #usageAgents .ic, #subscriptionUsage .subscription-card .ic";
// tag what is drawn now, and note from now on a skeleton, a card count
// falling, or the chart hidden
function watch(p) {
  return p.evaluate((sel) => {
    let n = 0;
    for (const e of document.querySelectorAll(sel)) e.__keep = ++n;
    window.__seen = [];
    window.__cards = document.querySelectorAll("#subscriptionUsage .subscription-card:not(.skeleton-card)").length;
    const note = (w) => { if (!window.__seen.includes(w)) window.__seen.push(w); };
    new MutationObserver((ms) => {
      if (document.querySelectorAll("#subscriptionUsage .subscription-card:not(.skeleton-card)").length < window.__cards) note("a card taken out");
      for (const m of ms) {
        if (m.type === "attributes" && m.attributeName === "class" && m.target.id === "view-usage" && m.target.classList.contains("loading")) note("skeleton");
        if (m.type === "attributes" && m.attributeName === "hidden" && m.target.id === "chart" && m.target.hidden) note("chart hidden");
      }
    }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["hidden", "class"] });
  }, KEEP);
}
const lost = (p) => p.evaluate((sel) => [...document.querySelectorAll(sel)].filter((e) => !e.__keep).map((e) => e.className + " " + (e.dataset.icon || "")).slice(0, 5), KEEP);
const seen = (p) => p.evaluate(() => window.__seen);
const waitFor = async (p, fn, what) => {
  for (let i = 0; i < 150; i++) { if (await fn()) return; await p.waitForTimeout(20); }
  assert.fail("timed out waiting for " + what);
};
// true once fn is, false if it never is within ~1.5s
const comes = async (p, fn) => {
  for (let i = 0; i < 75; i++) { if (await fn()) return true; await p.waitForTimeout(20); }
  return false;
};

// the reader's wheel to a place: the page puts back a scroll it didn't ask for
async function wheelTo(p, where) {
  const v = p.locator("#view-usage");
  const b = await v.boundingBox();
  await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  const target = await v.evaluate((e, w) => Math.round(w * (e.scrollHeight - e.clientHeight)), where);
  for (let i = 0; i < 80; i++) {
    const at = await v.evaluate((e) => e.scrollTop);
    if (Math.abs(at - target) < 60 || (where === 1 && at >= target - 1)) break;
    await p.mouse.wheel(0, Math.sign(target - at) * Math.min(200, Math.abs(target - at)));
    await p.waitForTimeout(40);
  }
  await p.waitForTimeout(400);
  return v.evaluate((e) => e.scrollTop);
}

const LANGS = (process.env.LANGS || "en,zh").split(",");
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const [width, height] of [[1180, 640], [560, 640]]) {
      test(`${engine} ${lang} ${width}px: the Overview read again keeps its cards and its place`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const p = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "no-preference" })).newPage();
        p.setDefaultTimeout(8000);
        p.setDefaultNavigationTimeout(60000);
        const errors = [];
        p.on("pageerror", (e) => errors.push(e.message));
        const st = { usage: 0, quotas: 0, quotasDone: 0, now: Date.now(), usageGate: null, quotaGate: null };
        await p.route("**/*", serve(lang, st));
        await p.goto("http://magpie.test/?view=usage");
        await p.locator("#subscriptionUsage .subscription-card:not(.skeleton-card)").nth(2).waitFor();
        await p.locator("#usageModels .row").nth(13).waitFor();
        await p.waitForTimeout(500);
        const nav = p.locator('#nav button[data-view="usage"]');
        assert.equal(await nav.isVisible(), true, "the Usage tab is in the sidebar");

        // its tab pressed again, at the top, halfway down and at the foot
        for (const where of process.env.QUIET_PART === "cards" ? [] : [0, 0.5, 1]) {
          const at = await wheelTo(p, where);
          if (where) assert(at > 50, `the page scrolled (${at})`);
          await watch(p);
          let open;
          st.usageGate = new Promise((r) => { open = r; });
          const before = st.usage;
          await nav.click();
          await waitFor(p, async () => st.usage > before, "the read again");
          // on its way: the refresh beside the period turns, nothing moved
          const turned = await comes(p, () => p.locator("#usageReload.reading").count());
          assert.deepEqual(await seen(p), [], `${where}: nothing blanked while it reads`);
          assert.equal(await p.locator("#view-usage").evaluate((e) => e.scrollTop), at, `${where}: the page stays while it reads`);
          assert(turned, `${where}: the refresh turns while it reads`);
          open(); st.usageGate = null;
          await waitFor(p, async () => !(await p.locator("#usageReload.reading").count()), "the refresh stopping");
          await p.waitForTimeout(300);
          assert.deepEqual(await seen(p), [], `${where}: nothing blanked`);
          assert.deepEqual(await lost(p), [], `${where}: the same figures, rows and logos`);
          assert(Math.abs(await p.locator("#view-usage").evaluate((e) => e.scrollTop) - at) <= 1, `${where}: the page didn't move`);
        }

        // the allowances read again (Refresh): every card stays, each one's
        // refresh turns while it is read, their logos are kept
        await wheelTo(p, 0);
        await watch(p);
        let release;
        st.quotaGate = new Promise((r) => { release = r; });
        const q = st.quotas;
        await p.locator("#usageReload").click();
        await waitFor(p, async () => st.quotas > q, "the allowances read");
        const spin = p.locator("#subscriptionUsage .subscription-card .quota-refresh");
        const reading = await comes(p, () => p.locator("#subscriptionUsage.reading").count());
        const shown = reading && await comes(p, async () => (await spin.first().evaluate((b) => b.getBoundingClientRect().width)) > 0);
        const turning = shown && (engine !== "chromium" || await spin.first().evaluate((b) => b.querySelector("svg > *").getAnimations().length > 0));
        st.now += 30e3; // the answer differs: the cards are drawn again
        release(); st.quotaGate = null;
        await waitFor(p, async () => st.quotasDone > q && !(await p.locator("#subscriptionUsage.reading").count()), "the cards done");
        await p.waitForTimeout(300);
        assert.equal(await p.locator("#subscriptionUsage .subscription-card:not(.skeleton-card)").count(), 3);
        assert.deepEqual(await seen(p), [], "no card taken out, no skeleton");
        const logos = await p.evaluate(() => [...document.querySelectorAll("#subscriptionUsage .subscription-card .ic")].map((e) => !!e.__keep));
        assert(logos.length >= 3 && logos.every(Boolean), "the cards' logos are the ones drawn before: " + logos);
        assert(reading && shown && turning, `each card's refresh turns while they are read (${reading}, ${shown}, ${turning})`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
