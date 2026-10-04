// Follow-up to #771: counters, error navigation and the live story share the
// selected purpose, while the tray and unfiltered lifetime totals stay global.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const now = new Date(), day = "2026-09-01";
function req(id, kind, status = 200, error = "") {
  const time = new Date(now.getTime() - (110 - id) * 1000).toISOString();
  const seat = { id: "relay", provider: "relay", name: "Relay", kind: "provider", model: "model-" + id };
  return { id, seq: id, time, agent: "codex", kind, model: seat.model, provider: "relay", order: [seat],
    tries: [{ id: seat.id, model: seat.model, start: time, done: true, status, ms: 20, error, fail: !status && error ? "other" : "" }],
    done: true, status, error, ms: 20, tokens: 12, cost: 0.01, priced: true };
}
function serve(lang, feed) {
  const initial = [req(100, "review"), req(99, "title", 200, "Codex titles are off in magpie's Settings, so magpie answered it itself"),
    req(98, "thread_title", 200, "Title reply broke off"),
    req(97, "title_generation", 0, "Title connection closed"), req(96, "guardian", 503, "Guardian failed")];
  initial[2].tries[0].fail = "other";
  // One failed try handed on, and one last failed try with nobody after it.
  const rest = { why: "rate", until: now.toISOString() };
  initial[1].tries.unshift({ ...initial[1].tries[0], status: 429, rest });
  initial[2].tries[0].rest = rest;
  return async (route) => {
    const url = new URL(route.request().url()), json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = (rs) => { feed.next = null; resolve(rs); }; }) : initial;
      return json({ mine: true, seq: ++feed.seq, now: now.toISOString(), totals: { requests: 20, rerouted: 5, errors: 7 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ days: [{ day, requests: 1 }], cut: false,
      routes: url.searchParams.get("day") ? [{ ...req(50, "review", 500, "Historical review failed"), time: day + "T10:00:00Z" }] : [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}
// Use the wheel so the app's scroll-preservation rules do not undo auto-scroll.
async function click(page, target) {
  await target.waitFor({ state: "visible" });
  const v = await page.locator("#view-routing").boundingBox();
  await page.mouse.move(v.x + 20, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const b = await target.boundingBox();
    if (b.y >= v.y && b.y + b.height <= v.y + v.height) break;
    await page.mouse.wheel(0, b.y < v.y ? -120 : 120);
    await page.waitForTimeout(40);
  }
  await page.waitForTimeout(300);
  await target.click();
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: purpose scopes Routing counts, story and subsequent live updates`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 850 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const feed = { seq: 100 }, errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      const counts = () => page.locator("#rt .rt-stats b").allTextContents();
      const story = async (id) => page.waitForFunction((id) => {
        const row = document.querySelector('.rt-req[aria-pressed="true"]');
        return row?.textContent.includes("model-" + id) && document.querySelector(".rt-steps")?.textContent.includes("model-" + id);
      }, id);
      const choose = async (value) => {
        await click(page, page.locator("#rtPurpose"));
        const name = await page.evaluate((v) => v ? purposeOptions([v], v)[0].name : t("All purposes"), value);
        await page.locator(".rt-purpose-menu .pm-item").filter({ has: page.locator(".pm-name", { hasText: name }) }).click();
        await page.waitForFunction((v) => document.querySelector(".rt-purpose-tools").classList.contains("set") === !!v, value);
      };
      const send = async (rs) => {
        for (let i = 0; i < 100 && !feed.next; i++) await page.waitForTimeout(20);
        assert(feed.next, "trace poll is waiting");
        feed.next(rs);
        // The following poll begins after all of this response was applied.
        for (let i = 0; i < 100 && !feed.next; i++) await page.waitForTimeout(20);
        assert(feed.next, "trace update was applied");
      };
      await page.goto("http://magpie.test/?view=routing");
      await story(100);
      assert.deepEqual(await counts(), ["20", "5", "7"], "unfiltered counters retain the gateway's lifetime totals");
      await choose("kind:thread_title");
      await story(99);
      assert.deepEqual(await counts(), ["3", "1", "2"], "count broken-off 200s and failures without an HTTP status, but not informational 200s");
      assert.equal(await page.locator(".rt-req").first().evaluate((el) => el.classList.contains("bad")), false, "the noted 200 is still an answer");
      assert.equal(await page.locator(".rt-errs").getAttribute("aria-disabled"), "false");
      await click(page, page.locator(".rt-errs"));
      await story(98);
      assert.equal(await page.locator('.rt-req[aria-pressed="true"]').evaluate((el) => el.classList.contains("bad")), true);
      assert.match(await page.locator(".rt-steps").textContent(), /Title reply broke off/);
      await send([req(101, "review", 500, "Unrelated failure")]);
      await story(98);
      assert.deepEqual(await counts(), ["3", "1", "2"], "unrelated live errors cannot change the filtered totals");
      // A picked matching request stays picked until the reader returns to live.
      await send([req(102, "thread_title_reconsideration")]);
      await story(98);
      await click(page, page.locator(".rt-log-head button", { hasText: lang === "zh" ? "回到实时" : "Back to live" }));
      await story(102);
      await send([req(103, "guardian")]);
      await story(102);
      assert.deepEqual(await counts(), ["4", "1", "2"]);
      // Resume after another page, then a matching new call, both keep the scope.
      await page.evaluate(() => window.show("providers"));
      await send([req(104, "review")]);
      await page.evaluate(() => window.show("routing"));
      await story(102);
      await send([req(105, "thread_title")]);
      await story(105);
      assert.deepEqual(await counts(), ["5", "1", "2"]);
      // History with no matching calls clears the old live story and counters.
      await click(page, page.locator(".rt-days .rt-day").nth(1));
      await page.waitForFunction(() => document.querySelector(".rt-log").hidden);
      assert.deepEqual(await counts(), ["0", "0", "0"]);
      assert.equal(await page.locator(".rt-errs").getAttribute("aria-disabled"), "true");
      await send([req(106, "thread_title")]);
      assert.equal(await page.locator(".rt-log").isHidden(), true, "a live call cannot replace an empty historical selection");
      await choose("kind:review");
      await story(50);
      assert.deepEqual(await counts(), ["1", "0", "1"]);
      await click(page, page.locator(".rt-errs"));
      await story(50);
      await click(page, page.locator(".rt-days .rt-day").first());
      await story(104);
      await click(page, page.locator("#rtPurposeClear"));
      await story(106);
      assert.deepEqual(await counts(), ["20", "5", "7"]);
      await choose("kind:thread_title");
      // The try can report failure even without a route-level error message.
      const broken = req(107, "title");
      broken.tries[0].fail = "other";
      await send([broken]);
      await story(107);
      assert.equal((await counts())[2], "3", "a broken-off 200 is counted even without a route-level error");
      await click(page, page.locator(".rt-errs"));
      await story(107);
      // In-flight matching calls become the latest story, but only completed
      // calls enter the counters, just as in the gateway's lifetime counters.
      const pending = req(108, "thread_title");
      pending.done = pending.tries[0].done = false;
      await send([pending]);
      await story(108);
      const beforeDone = await counts();
      await send([req(108, "thread_title")]);
      await page.waitForFunction((n) => document.querySelector("#rt .rt-stats b").textContent === String(n), Number(beforeDone[0]) + 1);
      assert.deepEqual((await counts()).slice(1), beforeDone.slice(1));
      await choose("");
      await story(108);
      assert.deepEqual(await counts(), ["20", "5", "7"], "the menu's All purposes option also clears the routing scope");
      // Browser interpretation uses explicit canonical keys, including unknown
      // values that happen to be Object.prototype property names.
      assert.deepEqual(await page.evaluate(() => ["guardian_review", "memory", "agent_job", "ambient_suggestion_safety", "constructor", "unmarked", ""].map(window.purposeOf)),
        ["kind:guardian", "kind:memory_consolidation", "kind:collab_spawn", "kind:ambient_suggestions", "kind:constructor", "kind:unmarked", "unmarked"]);
      assert.deepEqual(errors, []);
    });
  }
}
