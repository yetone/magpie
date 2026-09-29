// Run with Node's test runner and Playwright on the module path; see README.md.
// Requests coming in never move the Routing page (Jerell.OvO on Discord
// #general: as the request log refreshed, the page kept scrolling up, so
// the routing groups further down could not be read or edited). With the
// view scrolled down to the groups and one open in its editor, a name being
// typed, new requests stream in from the trace — new agents and accounts on
// the stage, a failure and a retry in the story, the list growing — and the
// groups stay where they are on the screen, the view's scrollTop with them,
// and the field keeps its focus and what was typed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const iso = (ms) => new Date(ms).toISOString();
const acct = (n) => ({ id: `acct-${n}`, provider: `prov${n}`, name: `Provider ${n}`, who: `user${n}@example.com`, kind: "account", model: `model-${n}`, known: true, used: 10 * n, plan: "Pro" });
const agentsOf = ["codex", "claude", "opencode", "kimi"];

// a request: from an agent, weighing some accounts, answered by the last
// of them after the others failed (more tries, a longer story)
function req(id, agent, ids, fails) {
  const t0 = now.getTime() - (200 - id) * 1000;
  const order = ids.map(acct);
  const tries = order.slice(0, fails + 1).map((w, i) => ({
    id: w.id, model: w.model, start: iso(t0 + i * 100), done: true, ms: 800,
    ...(i < fails ? { status: 429, fail: "rate", again: true, error: "rate limited, try again later" } : { status: 200 }),
  }));
  return { id, seq: id, time: iso(t0), agent, model: order[0].model, provider: order[0].provider, order, tries, done: true, status: 200, ms: 900 + fails * 800, tokens: 1500 };
}
const first = [req(100, "codex", [1], 0), req(101, "codex", [1, 2], 0)];
const later = [
  req(102, "claude", [3, 4, 5], 2),
  req(103, "opencode", [6, 1, 2, 3], 3),
  req(104, "kimi", [7, 8], 1),
  req(105, "codex", [1, 2, 3, 4, 5, 6], 4),
  req(106, "claude", [2], 0),
  req(107, "opencode", [8, 7, 6, 5, 4], 3),
];

const models = [1, 2, 3].map((n) => ({ id: `prov${n}/model-${n}`, name: `model-${n}`, providerName: `Provider ${n}`, icon: "generic" }));
const groups = {
  models,
  groups: [
    { id: "opus-anywhere", name: "Opus anywhere", members: [models[0].id, models[1].id], routing: "", ready: true },
    { id: "cheap", name: "Cheap", members: [models[2].id], routing: "order", ready: true },
  ],
  pools: [1, 2, 3].map((n) => ({ provider: `prov${n}`, name: `Provider ${n}`, kind: "account", who: ["a", "b"], routing: "", affinity: "" })),
};

function serve(feed) {
  const state = { agents: agentsOf.map((id) => ({ id, name: id[0].toUpperCase() + id.slice(1), path: `/test/${id}`, fields: [] })), profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      const trace = (routes) => json({ mine: true, now: new Date().toISOString(), seq: routes.length ? routes[routes.length - 1].seq : Number(url.searchParams.get("after")), totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return trace(first);
      // the long poll: answered when the test feeds a request
      const r = await new Promise((res) => { feed.next = res; });
      return trace([r]);
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-routing";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: requests coming in leave the routing groups where they are`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 1100, height: 760 } });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const feed = { next: null };
    await page.route("**/*", serve(feed));
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-steady.png`) });
      }
      feed.next?.(req(999, "codex", [1], 0));
      await browser.close();
    });
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-req").nth(first.length - 1).waitFor();
    await page.locator(".rt-group").first().waitFor();
    await page.waitForTimeout(2000); // the first request's play

    // down to the groups with the wheel (the view moves only for the
    // reader), the second group in the middle of the view; then the first
    // opened in its editor and a name typed into it
    const dy = await page.locator(view).evaluate((v) => {
      const g = [...document.querySelectorAll(".rt-group")].pop();
      return Math.round(g.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2);
    });
    await page.mouse.move(550, 400);
    await page.mouse.wheel(0, dy);
    await page.waitForTimeout(400);
    const scrolled = await page.locator(view).evaluate((v) => v.scrollTop);
    assert(scrolled > 200, `the view must be scrolled down to the groups (${scrolled})`);
    await page.locator(".rt-group").first().locator("button", { hasText: "Edit" }).click();
    const name = page.locator(".rt-gedit input").first();
    await name.click();
    await name.press("End");
    await page.keyboard.type(" and more");
    const typed = await name.inputValue();
    assert.equal(typed, "Opus anywhere and more");
    await page.waitForTimeout(300);

    // where things are: on the screen, and the groups' place in the page
    // (which moves as what's above them grows or shrinks)
    const where = () => page.evaluate((sel) => {
      const v = document.querySelector(sel), vt = v.getBoundingClientRect().top;
      const top = (e) => e.getBoundingClientRect().top - vt;
      const gsec = document.querySelector(".rt-gsec");
      return {
        scrollTop: v.scrollTop,
        inPage: gsec.getBoundingClientRect().top - vt + v.scrollTop,
        onScreen: { head: top(gsec), editor: top(document.querySelector(".rt-gedit")), group: top([...document.querySelectorAll(".rt-group")].pop()) },
      };
    }, view);
    const before = await where();

    // every layout and every scroll while they come, after the page has
    // had its say (these observers and listeners come after the page's):
    // what the reader could see, not only where it ends
    await page.evaluate((sel) => {
      // (to a pixel and a half: WebKit scrolls by whole pixels, the
      // parts above are laid out in fractions of one)
      const v = document.querySelector(sel), ed = () => document.querySelector(".rt-gedit").getBoundingClientRect().top;
      const want = ed();
      window.__moves = [];
      const look = (why) => { const at = ed(); if (Math.abs(at - want) > 1.5) window.__moves.push(`${why}: the editor at ${at}, not ${want}`); };
      window.__ro = new ResizeObserver(() => look("layout"));
      for (const e of [v, ...v.children, ...document.querySelector("#rtMore").children]) window.__ro.observe(e);
      v.addEventListener("scroll", () => look("scroll"));
    }, view);
    for (const r of later) {
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(50);
      const next = feed.next;
      feed.next = null;
      next(r);
      await page.waitForTimeout(900);
    }
    await page.waitForTimeout(2500); // the plays end, the countdowns tick
    assert.equal(await page.locator(".rt-req").count(), first.length + later.length, "every request must be listed");
    const moves = await page.evaluate(() => { window.__ro.disconnect(); return window.__moves; });
    const after = await where();

    // what's above the groups did change: new agents and accounts on the
    // stage, a longer story, more rows in the lists
    assert(Math.abs(after.inPage - before.inPage) > 50, `the part above the groups must have changed (${before.inPage} → ${after.inPage})`);
    // and the groups stayed where they were on the screen, the view
    // scrolling by just what grew above them, on every frame
    for (const k of Object.keys(before.onScreen)) assert(Math.abs(after.onScreen[k] - before.onScreen[k]) <= 1.5, `the ${k} moved: ${JSON.stringify([before, after])}`);
    assert(Math.abs((after.scrollTop - before.scrollTop) - (after.inPage - before.inPage)) < 2, `scrollTop must follow the groups: ${JSON.stringify([before, after])}`);
    assert.deepEqual(moves, [], "the page moved while the requests came");
    // the field kept its focus and what was typed, and goes on taking it
    assert.equal(await name.evaluate((e) => e === document.activeElement), true, "the field lost its focus");
    assert.equal(await name.inputValue(), typed);
    await page.keyboard.type("!");
    assert.equal(await name.inputValue(), typed + "!");
    assert.deepEqual(errors, []);
  });
}
