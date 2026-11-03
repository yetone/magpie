// Run with Node's test runner and Playwright on the module path; see README.md.
// A try that fails tags its row on the Routing page's stage ("504 · slow to
// start") over the row's top edge for 1.8s. The row was drawn whole only
// while it shook (.hit, 0.45s): after that content-visibility: auto, which
// paint-contains, cut the tag's top ~10px off until it faded (yuancjun on
// Discord, Chrome 128 on macOS, v0.1.2784, who found the cause). The tag is
// whole for as long as it shows, and while it fades. Motion is on here, as
// the shake has to end for the cut to show. English, Chinese (both),
// Japanese and German; 1100 and 420 wide; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
const acct = { id: "relay-1", provider: "relay", name: "Relay", who: "key-1", kind: "account", model: "glm-5", known: true, used: 10 };
const other = { id: "relay-2", provider: "relay", name: "Relay", who: "key-2", kind: "account", model: "glm-5", known: true, used: 20 };

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const t0 = Date.now();
  const base = { id: 1, time: iso(t0), agent: "codex", model: acct.model, provider: acct.provider, order: [other, acct] };
  // under way at the second account, then failed there: the answer, so
  // no one is asked again
  const going = { ...base, seq: 2, tries: [{ id: acct.id, model: acct.model, start: iso(t0) }], done: false };
  const failed = { ...base, seq: 3, tries: [{ id: acct.id, model: acct.model, start: iso(t0), done: true, status: 504, ms: 1200, fail: "slow", error: "no first token" }], done: true, status: 504, error: "no first token", ms: 1200 };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data }).catch(() => {});
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      const after = Number(url.searchParams.get("after") || 0);
      const now = () => new Date().toISOString();
      if (!url.searchParams.get("wait")) return json({ mine: true, now: now(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
      if (after < 2) { await new Promise((r) => setTimeout(r, 300)); return json({ mine: true, now: now(), seq: 2, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [going] }); }
      if (after < 3) { await new Promise((r) => setTimeout(r, 1500)); return json({ mine: true, now: now(), seq: 3, totals: { requests: 1, rerouted: 0, errors: 1 }, routes: [failed] }); }
      await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now(), seq: 3, totals: { requests: 1, rerouted: 0, errors: 1 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the share of the tag's band over the row's top edge that is painted in
// the tag's own red: the band is screenshotted and read back in the page
async function redOverEdge(page) {
  const box = await page.evaluate(() => {
    const li = document.querySelector(".rt-accts li.tagged");
    const tag = li.querySelector(".tag"), t = tag.getBoundingClientRect(), l = li.getBoundingClientRect();
    return { x: t.left + 3, y: t.top + 1, width: t.width - 6, height: l.top - t.top - 2, bg: getComputedStyle(tag).backgroundColor };
  });
  assert(box.height >= 4, `the tag stands over the row's top edge: ${JSON.stringify(box)}`);
  const png = await page.screenshot({ clip: { x: box.x, y: box.y, width: box.width, height: box.height } });
  return page.evaluate(async ([src, bg]) => {
    const img = new Image();
    img.src = src;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth; c.height = img.naturalHeight;
    const g = c.getContext("2d");
    g.drawImage(img, 0, 0);
    // the tag's red as a canvas paints it, whatever colour space it was given in
    g.fillStyle = bg; g.fillRect(0, 0, 1, 1);
    const want = g.getImageData(0, 0, 1, 1).data;
    g.drawImage(img, 0, 0);
    const px = g.getImageData(0, 0, c.width, c.height).data;
    let red = 0;
    for (let i = 0; i < px.length; i += 4) if (Math.abs(px[i] - want[0]) + Math.abs(px[i + 1] - want[1]) + Math.abs(px[i + 2] - want[2]) < 60) red++;
    return red / (px.length / 4);
  }, ["data:image/png;base64," + png.toString("base64"), box.bg]);
}

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = ["en", "zh", "zh-TW", "ja", "de"];
const widths = [1100, 420];
for (const engine of engines) for (const lang of langs) for (const width of widths) {
  test(`${engine} ${lang} ${width}px: a failed try's tag over its row stays whole until it fades`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width, height: 800 } });
    const page = await context.newPage();
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(lang));
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-tag-whole.png`) });
      }
      await browser.close();
    });
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-accts li.tagged").waitFor({ timeout: 15000 });
    const word = await page.evaluate((lang) => (lang !== "en" && I18N[lang]?.["slow to start"]) || "slow to start", lang);
    assert.equal(await page.locator(".rt-accts li.tagged .tag").textContent(), `504 · ${word}`);
    // the shake is over (0.45s), the tag up for 1.8s: whole
    await page.waitForFunction(() => document.querySelector(".rt-accts li.tagged:not(.hit)"));
    await page.waitForTimeout(350);
    assert(await page.locator(".rt-accts li.tagged").count(), "the tag is still up");
    assert.equal(await page.locator(".rt-accts li.tagged").evaluate((li) => getComputedStyle(li).contentVisibility), "visible", "a tagged row is drawn whole");
    const red = await redOverEdge(page);
    assert(red > 0.5, `the tag's top over the row's edge is drawn: ${(red * 100).toFixed(0)}% of it red`);
    // taken down: drawn whole while it fades, then cut back to sight
    await page.waitForFunction(() => !document.querySelector(".rt-accts li.tagged"));
    const fading = await page.locator(".rt-accts li.lift").evaluateAll((lis) => lis.map((li) => getComputedStyle(li).contentVisibility));
    assert.deepEqual(fading, ["visible"], "the row the tag fades over is drawn whole");
    await page.waitForFunction(() => !document.querySelector(".rt-accts li.lift"));
    const rows = await page.locator(".rt-accts > li:not(.rt-gap)").evaluateAll((lis) => lis.map((li) => getComputedStyle(li).contentVisibility));
    assert(rows.length >= 2 && rows.every((v) => v === "auto"), `rows out of sight are skipped again: ${rows}`);
    assert.deepEqual(errors, []);
  });
}
