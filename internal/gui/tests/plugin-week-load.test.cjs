// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover's week (the owner, 11-27: 「这个加载特别慢，而且加载的过程中也没有
// 小喜鹊的动画」). npm is asked of the listings without waiting for GitHub's
// list of tagged repositories, which took ~20s on a first look. A list
// older than its ten minutes comes at once, marked stale; the page draws
// it and asks again with wait, and GitHub's newer answer is drawn when it
// comes. While the week waits, magpie's bird lands on its skeleton, and
// with reduced motion no bird comes. Every language, both engines; the
// API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const A = "@magpie-community/opencode-alpha-auth", B = "@magpie-community/opencode-beta-auth";
const OLD = "github:someone/old-one", NEW = "github:someone/new-one";
const listings = () => [
  { package: A, name: "Alpha", providers: ["alpha"], community: true, summary: { en: "Alpha." } },
  { package: B, name: "Beta", providers: ["beta"], community: true, summary: { en: "Beta." } },
];
const npm = { [A]: { version: "0.1.0", weekly: 10 }, [B]: { version: "0.1.0", weekly: 20 } };
const repo = (spec) => ({ spec, repo: spec.slice(7), url: "https://github.com/" + spec.slice(7), owner: "someone", description: "A repository.", stars: 3, kind: "provider" });
// the repository GitHub's newer answer adds leads Most used
const counts = { [A]: { users: 50, likes: 2 }, [B]: { users: 20, likes: 1 }, [NEW]: { users: 900 } };
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };

function server(lang, h) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state });
    if (url.pathname === "/api/plugins/counts") return json({ plugins: counts, liked: [] });
    if (url.pathname === "/api/plugins/npm") {
      h.npmAsked?.();
      await h.npm;
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/github") {
      if (url.searchParams.get("wait")) { h.waited = true; return json({ repos: [repo(OLD), repo(NEW)], topic: "magpie-plugin" }); }
      await h.github;
      return json({ repos: [repo(OLD)], topic: "magpie-plugin", stale: true });
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, h, reduced, width = 1180) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: reduced ? "reduce" : "no-preference" });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.addInitScript(() => {
    // the bars that dipped under the bird, and every bird that came
    window.seen = { birds: 0, weekDips: 0 };
    new MutationObserver((ms) => { for (const m of ms) for (const n of m.addedNodes) if (n.classList?.contains("sk-bird")) seen.birds++; })
      .observe(document, { childList: true, subtree: true });
    const step = () => {
      seen.weekDips = Math.max(seen.weekDips, document.getAnimations().filter((a) => a.effect?.target?.matches?.(".pm-rec.ghost .sk")
        && a.effect.getKeyframes().some((k) => /translateY/.test(k.transform || ""))).length);
      requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
  });
  await page.route("**/*", server(lang, h));
  await page.goto("http://magpie.test/?view=plugins");
  return { ctx, page, errors };
}

const held = () => { let open; const p = new Promise((r) => { open = r; }); return [p, open]; };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));

  test(engine + ": npm is asked before GitHub's list is in, and a stale list is drawn, then GitHub's newer one", async (t) => {
    const browser = await launch();
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1180, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        // GitHub's list waits for npm to be asked: before, npm waited for
        // GitHub's list, and neither came
        const [github, letGitHub] = held();
        const h = { github, npm: null, npmAsked: letGitHub, waited: false };
        const { ctx, page, errors } = await open(browser, lang, h, true, width);
        const view = page.locator("#view-plugins");
        await view.locator(".pm-recs .pm-hero[data-pkg]").waitFor();
        // the stale list was drawn, then asked again with wait, and the
        // repository GitHub's newer answer has leads Most used
        await page.waitForFunction((spec) => document.querySelector("#view-plugins .pm-recs .pm-rank .pm-rrow")?.dataset.pkg === spec, NEW);
        assert.equal(h.waited, true);
        assert.ok(await view.locator(`.pm-card[data-pkg="${OLD}"]`).count(), "the stale list's repository is drawn");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });

  test(engine + ": while the week waits, magpie's bird lands on its skeleton; with reduced motion none comes", async (t) => {
    const browser = await launch();
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const [npmIn, letNPM] = held();
        const h = { github: Promise.resolve(), npm: npmIn };
        const { ctx, page, errors } = await open(browser, lang, h, false);
        const view = page.locator("#view-plugins");
        await view.locator(".pm-recs .pm-rec.ghost").waitFor();
        await page.locator(".sk-bird.roam").waitFor({ state: "attached", timeout: 4000 });
        await page.waitForFunction(() => seen.weekDips > 0, null, { timeout: 4000 });
        letNPM();
        await view.locator(".pm-recs .pm-hero[data-pkg]").waitFor();
        await page.waitForFunction(() => !document.querySelector(".sk-bird"), null, { timeout: 4000 });
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
    const [npmIn, letNPM] = held();
    const { ctx, page, errors } = await open(browser, "en", { github: Promise.resolve(), npm: npmIn }, true);
    await page.locator("#view-plugins .pm-recs .pm-rec.ghost").waitFor();
    await page.waitForTimeout(1500);
    assert.equal(await page.evaluate(() => seen.birds), 0, "reduced motion: no bird");
    letNPM();
    await page.locator("#view-plugins .pm-recs .pm-hero[data-pkg]").waitFor();
    assert.deepEqual(errors, []);
    await ctx.close();
  });

  // #7810: the view keeps its scroll through a draw, so the week's skeleton
  // giving way to its picks moved every shelf under it; the one the reader
  // is on stays where it was
  test(engine + ": the shelf in sight stays where it is as the week comes in above it", async (t) => {
    const browser = await launch();
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1180, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const [npmIn, letNPM] = held();
        const { ctx, page, errors } = await open(browser, lang, { github: Promise.resolve(), npm: npmIn }, true, width);
        await page.setViewportSize({ width, height: 420 });
        const view = page.locator("#view-plugins");
        await view.locator(".pm-recs .pm-rec.ghost").waitFor();
        const shelf = view.locator(".pm-body > .pm-sec[data-kind]").first();
        await shelf.waitFor();
        const top = () => shelf.evaluate((s) => s.getBoundingClientRect().top - document.querySelector("#view-plugins").getBoundingClientRect().top);
        // the shelf brought near the top by a wheel, as a reader would
        await view.hover();
        for (let i = 0; i < 30 && (await top()) > 90; i++) { await page.mouse.wheel(0, Math.min(200, (await top()) - 60)); await page.waitForTimeout(60); }
        await page.waitForTimeout(400);
        const before = await top();
        assert(before < 120 && before > -40, `the shelf near the top: ${before}`);
        letNPM();
        await view.locator(".pm-recs .pm-hero[data-pkg]").waitFor();
        await page.waitForTimeout(400);
        assert(Math.abs((await top()) - before) <= 1, `the shelf moved: ${before} → ${await top()}`);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
