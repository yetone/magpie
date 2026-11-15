// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing groups' quota lines fold (Sdsi134a on X): each group listed every
// provider's "WorkBuddy AI, 4 accounts: …" under its members, so with many
// plans the page grew long. The toolbar has Hide plans, before Templates:
// it folds those lines under every group (the name, its id and the members'
// line stay), says Show plans then, and the choice is kept across a
// reload. Shown, as before, until it is clicked; no button when no group
// has such a line. Every language, Chromium and WebKit, wide and a phone's
// width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/n", name: "n", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true, provider: id.split("/")[0], name: id.split("/")[0].toUpperCase() }));
const groups = [
  { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  { id: "mine", name: "Mine", members: ["a/n", "b/m"], ready: true, memberInfo: info(["a/n", "b/m"]) },
];
// the whole toolbar: Templates too, beside Import, Select and New group
const templates = [{ kind: "fast", group: { id: "fastest-first", name: "Fastest first", members: ["a/m", "b/m"], routing: "fast" } }];
// two providers, two accounts each: a line for each under both groups
const quotas = ["a", "b"].flatMap((provider) => ["1@example.com", "2@example.com"].map((user) => ({
  provider, name: provider.toUpperCase(), kind: "subscription", icon: "generic", plan: "Pro", user, readAt: new Date().toISOString(),
  windows: [{ name: "Credits", amount: 400, limit: 500, unit: "credits", used: 20, resetsAt: new Date(Date.now() + 9 * 864e5).toISOString() }],
})));

const words = {
  en: { hide: "Hide plans", show: "Show plans" },
  zh: { hide: "隐藏套餐", show: "显示套餐" },
  "zh-TW": { hide: "隱藏套餐", show: "顯示套餐" },
  ja: { hide: "プランを隠す", show: "プランを表示" },
  de: { hide: "Tarife ausblenden", show: "Tarife zeigen" },
};

function serve(lang, web, withQuotas, extraGroups = []) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups: [...groups, ...extraGroups], templates });
    if (url.pathname === "/api/usage/quotas") return json(withQuotas ? quotas : []);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: filtering updates the plans button while preserving cached cards and the folded choice`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1280, height: 700 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const extra = [{ id: "plain", name: "Plain group", members: ["c/x"], ready: true, memberInfo: info(["c/x"]) }];
    await page.route("**/*", serve("en", false, true, extra));
    await page.goto("http://magpie.test/?view=groups");
    const card = page.locator('.rt-group[data-id="mine"]'), btn = page.locator(".rt-gplans"), q = page.locator("input.rt-gfilter");
    await card.locator(".rt-gquota").first().waitFor({ timeout: boot });
    await card.evaluate((e) => { window.keptPlanCard = e; });
    await btn.click();
    assert.equal(await card.locator(".rt-gquota").first().isVisible(), false);
    await q.fill("Plain group");
    assert.equal(await btn.isVisible(), false);
    await q.fill("Mine");
    assert.equal(await btn.isVisible(), true);
    assert.equal((await btn.textContent()).trim(), "Show plans");
    assert.equal(await card.evaluate((e) => e === window.keptPlanCard), true);
    assert.equal(await card.locator(".rt-gquota").first().isVisible(), false);
    await btn.click();
    assert.equal(await card.locator(".rt-gquota").first().isVisible(), true);
    assert.deepEqual(errors, []);
  });
}

// the page's boot under the whole suite's load (#3478)
const boot = 15000;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const { width, web } of [{ width: 1280, web: false }, { width: 420, web: true }]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: Hide plans folds every group's quota lines, kept across a reload`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-routing-group-plans.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web, true));
        await page.goto("http://magpie.test/?view=groups");
        const view = page.locator("#view-groups");
        const lines = view.locator(".rt-group .rt-gquota");
        await lines.first().waitFor({ timeout: boot });
        assert.equal(await lines.count(), 4, "a line a provider under each group");
        const head = view.locator(".rt-gsec > .row-head").first();
        const btn = head.locator(".rt-gplans");

        // shown at first, as before; the button says what a click does,
        // before Templates, Import and Select
        assert.equal(await btn.isVisible(), true);
        assert.equal((await btn.textContent()).trim(), w.hide);
        assert.equal(await btn.evaluate((b) => b.nextElementSibling?.classList.contains("rt-gtplbtn")), true, "before Templates");
        for (const c of ["rt-gtplbtn", "rt-gimportbtn", "rt-gselect", "rt-gnew"]) assert.equal(await head.locator("." + c).isVisible(), true, c);
        for (const l of await lines.all()) assert.equal(await l.isVisible(), true);
        assert.equal(await head.locator("select").count(), 0, "no native select");
        const headOver = await head.evaluate((h) => h.scrollWidth - h.clientWidth);
        assert(headOver <= 0, `nothing overflows the toolbar (${headOver}px)`);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        assert(over <= 0, `no sideways scroll (${over}px)`);
        const border = await btn.evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; });
        assert.equal(border, false, "no left-border accent");

        // folded: every line gone, the rest of each card stays; the button
        // where it was
        const at = await btn.boundingBox();
        await btn.click();
        assert.equal((await btn.textContent()).trim(), w.show);
        for (const l of await lines.all()) assert.equal(await l.isVisible(), false);
        const mine = view.locator('.rt-group[data-id="mine"]');
        assert.equal(await mine.locator(".nm b").isVisible(), true, "its name");
        assert.equal(await mine.locator(".nm code.mdl").isVisible(), true, "its id");
        assert.equal(await mine.locator(".main > .mem:not(.rt-gquota)").first().isVisible(), true, "its members");
        const after = await btn.boundingBox();
        assert(Math.abs(after.y - at.y) < 1, "the button stayed put");

        // new quotas draw the list again: still folded
        await page.evaluate(() => { quotas = quotas.map((q) => ({ ...q })); });
        await page.waitForTimeout(1300);
        for (const l of await lines.all()) assert.equal(await l.isVisible(), false);

        // kept across a reload
        await page.reload();
        await lines.first().waitFor({ state: "attached", timeout: boot });
        assert.equal((await btn.textContent()).trim(), w.show);
        for (const l of await lines.all()) assert.equal(await l.isVisible(), false);

        // and shown again
        await btn.click();
        assert.equal((await btn.textContent()).trim(), w.hide);
        for (const l of await lines.all()) assert.equal(await l.isVisible(), true);
        await page.reload();
        await lines.first().waitFor({ timeout: boot });
        assert.equal((await btn.textContent()).trim(), w.hide);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// no line under any group: nothing to fold, no button
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: no quota line, no Hide plans`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1280, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", false, false));
    await page.goto("http://magpie.test/?view=groups");
    await page.locator('#view-groups .rt-group[data-id="mine"]').waitFor({ timeout: boot });
    await page.waitForFunction(() => Array.isArray(quotas), null, { timeout: boot });
    await page.waitForTimeout(1300);
    assert.equal(await page.locator("#view-groups .rt-gquota").count(), 0);
    assert.equal(await page.locator("#view-groups .rt-gplans").isVisible(), false);
    assert.deepEqual(errors, []);
  });
}
