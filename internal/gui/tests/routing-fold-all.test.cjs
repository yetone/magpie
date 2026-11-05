// Run with Node's test runner and Playwright on the module path; see README.md.
// zzzkl on Discord: grouped by session, the "No session ID" group couldn't be
// folded while every other group could. Now it folds like the rest, and one
// icon button in the list's head folds every group, or opens them all when
// none is open. Only shown while the list is grouped and has two groups or
// more. In Chromium and WebKit, at 1100 and 420 wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, process.env.MAGPIE_ROUTING_ASSETS || "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const stamp = (d) => {
  const o = -d.getTimezoneOffset(), p = (n) => String(Math.floor(Math.abs(n))).padStart(2, "0");
  return new Date(d.getTime() + o * 60e3).toISOString().slice(0, 19) + (o < 0 ? "-" : "+") + p(o / 60) + ":" + p(o % 60);
};
function req(id, session) {
  const time = stamp(new Date(now.getTime() - (10 - id) * 60e3));
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "Test account", kind: "account", model: "model-a" };
  return { id, seq: id, time, agent: "claude", session, model: "relay/model-a", provider: "relay", cost: 0.01, priced: true,
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: time, done: true, status: 200, ms: 4500 }],
    done: true, status: 200, ms: 4500, tokens: 2000, ttft: 1200 };
}
const routes = [req(1, "chat-a"), req(2, ""), req(3, "chat-b"), req(4, "chat-a"), req(5, "")];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {}); // no live update
      return json({ mine: true, now: now.toISOString(), seq: 5, totals: { requests: 5, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: routes.length }], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") return json({});
    if (url.pathname === "/api/clis") return json({ agents: [], providers: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["No session ID", "Collapse all", "Expand all"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: every session group folds, the one with no session id too, and all fold at once`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-routing-fold-all.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const view = page.locator("#view-routing");
        await view.locator(".rt-req").nth(4).waitFor();
        await view.locator(".rt-group-by button").nth(1).click();
        await view.locator(".rt-session").nth(2).waitFor();

        const w = await page.evaluate((keys) => Object.fromEntries(keys.map((k) => [k, t(k)])), KEYS);
        if (lang !== "en") assert.deepEqual(KEYS.filter((k) => w[k] === k), [], `${lang} lacks these`);
        const rows = () => view.locator(".rt-req").count();
        const groups = view.locator(".rt-session");
        const bare = groups.filter({ hasText: w["No session ID"] });
        const fold = view.locator(".rt-req-head .rt-fold-all");
        const open = () => groups.evaluateAll((gs) => gs.map((g) => g.getAttribute("aria-expanded")));
        // where the view is scrolled, and the furthest it can be: a fold
        // that makes the page shorter than where it was can't stay put
        const y = () => view.evaluate((v) => [v.scrollTop, v.scrollHeight - v.clientHeight, document.scrollingElement.scrollTop]);

        // the reporter's case: the group with no session id folds and opens
        assert.equal(await groups.count(), 3);
        assert.equal(await bare.count(), 1);
        assert.equal(await bare.getAttribute("aria-expanded"), "true", "the no-session group can't be folded");
        await reach(page, bare);
        await bare.click();
        assert.equal(await bare.getAttribute("aria-expanded"), "false");
        assert.equal(await rows(), 3, "its two requests are still shown");
        await bare.click();
        assert.equal(await rows(), 5);

        // one button folds them all, then opens them all; it says which
        await fold.waitFor();
        assert.equal(await fold.getAttribute("aria-label"), w["Collapse all"]);
        assert.equal(await fold.getAttribute("title"), w["Collapse all"]);
        assert.equal((await fold.textContent()).trim(), "", "the button has words beside its icon");
        const box = await fold.boundingBox(), head = await view.locator(".rt-req-head").boundingBox();
        assert(box.x >= head.x - 1 && box.x + box.width <= head.x + head.width + 1, "the button is out of the head row");
        await reach(page, fold);
        const before = await y();
        await fold.click();
        assert.deepEqual(await open(), ["false", "false", "false"]);
        assert.equal(await rows(), 0);
        assert.equal(await fold.getAttribute("aria-label"), w["Expand all"]);
        const [top, most, doc] = await y();
        assert(top === before[0] || top < before[0] && top >= most - 1, `the click scrolled: ${before} to ${[top, most]}`);
        assert.equal(doc, before[2], "the click scrolled the window");
        await reach(page, fold);
        await fold.click();
        assert.deepEqual(await open(), ["true", "true", "true"]);
        assert.equal(await rows(), 5);
        // one open is enough for it to fold them all
        await reach(page, groups.first());
        await groups.first().click();
        await reach(page, bare);
        await bare.click();
        assert.equal(await fold.getAttribute("aria-label"), w["Collapse all"]);
        await reach(page, fold);
        await fold.click();
        assert.deepEqual(await open(), ["false", "false", "false"]);

        // by request there are no groups, and no button
        await reach(page, view.locator(".rt-group-by button").first());
        await view.locator(".rt-group-by button").first().click();
        assert.equal(await rows(), 5);
        assert(await fold.isHidden(), "the button shows with no groups");
        assert.deepEqual(errors, []);
      });
    }
  }
}
