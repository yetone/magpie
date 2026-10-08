// #1016: current allowances stay visible beside a single Kimi key's live
// request, independently of answering/resting. Historical requests retain
// their recorded routing facts, never today's quota. Fixtures use the two
// windows reported in the issue; all API calls are intercepted.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.MAGPIE_ROUTING_ASSETS || path.resolve(__dirname, "../assets");
const windows = [
  { name: "5 hours", used: 49, resetsAt: "2026-10-06T15:03:20.95408Z" },
  { name: "7 days", used: 45, resetsAt: "2026-10-07T17:03:20.95408Z" },
];
const kimi = { provider: "kimi-code-cn", name: "Kimi Code (China)", icon: "kimi", windows };

async function open(t, engine, lang, cards = [kimi], resting = false, options = {}) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: options.viewport || { width: 560, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  await page.clock.install({ time: new Date("2026-10-06T12:00:00Z") });
  const errors = [], calls = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const time = "2026-10-06T11:59:58Z";
  const provider = options.provider || kimi.provider;
  const model = provider === "deepseek" ? "deepseek-chat" : "kimi-for-coding";
  const order = [{ id: provider, provider, name: provider === "deepseek" ? "DeepSeek" : kimi.name, kind: "provider", model }];
  const keys = (Array.isArray(cards) ? cards : []).filter((q) => q.provider === kimi.provider && q.user);
  if (keys.length > 1) order.splice(0, 1, ...keys.map((q, i) => ({ ...order[0], id: `kimi-code-cn#${i}`, kind: "key", who: q.user })));
  if (resting) order[0].rest = { why: "rate", until: "2026-10-06T13:00:00Z" };
  const live = { id: 2, seq: 2, time, agent: "codex", model, provider, order, tries: [{ id: order[0].id, model, start: time }] };
  if (resting) live.tries = [];
  const old = { ...live, id: 1, seq: 1, time: "2026-10-06T11:00:00Z", done: true, status: 200, tries: [{ ...live.tries[0], done: true, status: 200 }] };
  if (options.failed) Object.assign(old, { order: null, tries: [], status: 400, error: "model unavailable" });
  const state = { cards, fail: !!options.fail, reading: !!options.reading, requests: 0, history: 0, live };
  await page.route("**/*", async (r) => {
    const u = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (u.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (u.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (u.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: true } });
    if (u.pathname === "/api/settings/quota-left") return json({ lang, theme: "light", quotaLeft: JSON.parse(r.request().postData()).on });
    if (u.pathname === "/api/gateway/trace") {
      if (u.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: "2026-10-06T12:00:00Z", seq: 2, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [old, live] });
    }
    if (u.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (u.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (u.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (u.pathname === "/api/usage/quotas") {
      state.requests++;
      return state.fail ? r.fulfill({ status: 503, body: "temporarily unavailable" }) : r.fulfill({ json: state.cards, headers: { "X-Magpie-Reading": state.reading ? "1" : "0" } });
    }
    if (u.pathname === "/api/usage/quotas/history") { state.history++; return json([]); }
    if (u.pathname.startsWith("/api/")) { calls.push(u.pathname); return json({}); }
    const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  });
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-accts li em").first().waitFor();
  const say = (key, vars = {}) => page.evaluate(([key, vars]) => t(key, vars), [key, vars]);
  return { page, state, errors, calls, say, quota: page.locator(".rt-quotas") };
}

async function scrollTo(page, locator) {
  const box = await locator.boundingBox();
  await page.mouse.move(280, 400);
  await page.mouse.wheel(0, box.y - 420);
  await page.clock.runFor(500);
  await page.waitForTimeout(300);
}

async function pickInPlace(page, row) {
  const before = await row.boundingBox();
  assert.ok(before.y >= 0 && before.y + before.height < page.viewportSize().height - 32, "measure a visible row, before Playwright could scroll it into view");
  await row.click();
  await page.clock.runFor(500);
  const after = await row.boundingBox();
  assert.ok(Math.abs(after.y - before.y) <= 1, `picking a request must keep the row under the pointer: ${before.y} -> ${after.y}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: selecting a failed request with real quotas keeps its position at desktop width`, async (t) => {
    const { page, quota, errors } = await open(t, engine, "en", [kimi], false, { failed: true, viewport: { width: 1100, height: 1100 } });
    await quota.locator(".quota-track").first().waitFor();
    await page.clock.runFor(500);
    await pickInPlace(page, page.locator(".rt-req").last());
    assert.equal(await quota.isVisible(), false);
    await pickInPlace(page, page.locator(".rt-req").first());
    assert.equal(await quota.isVisible(), true);
    assert.deepEqual(errors, []);
  });

  for (const fail of [true, false]) test(`${engine}: ${fail ? "failed" : "malformed"} quota response never invents allowances for a provider without them`, async (t) => {
    const { page, quota, state, errors } = await open(t, engine, "en", {}, false, { fail, provider: "deepseek" });
    await page.waitForFunction(() => document.querySelector(".rt-req"));
    await page.clock.runFor(1000);
    assert.equal(state.requests, 1);
    assert.equal(await quota.isVisible(), false);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: shared quota reads coalesce, retry an in-progress reading and reuse fresh cache`, async (t) => {
    const { page, quota, state, errors } = await open(t, engine, "en", [kimi], false, { reading: true });
    await quota.getByText("51% left", { exact: true }).waitFor();
    assert.equal(state.requests, 1);
    assert.equal(state.history, 1, "Routing uses the shared loader");
    state.cards = [{ ...kimi, windows: [{ ...windows[0], used: 60 }] }];
    state.reading = false;
    await page.clock.runFor(1600);
    await quota.getByText("40% left", { exact: true }).waitFor();
    assert.equal(state.requests, 2, "an in-progress vendor read is revisited within 1.5 seconds");
    await page.evaluate(() => show("providers"));
    await page.evaluate(() => Promise.all([loadQuotas(), loadQuotas()]));
    assert.equal(state.requests, 3, "simultaneous callers share one request");
    await page.evaluate(() => show("routing"));
    await quota.getByText("40% left", { exact: true }).waitFor();
    assert.equal(state.requests, 3, "returning to Routing reuses the shared fresh cache");
    assert.deepEqual(errors, []);
  });

  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: single Kimi key shows both windows while answering, follows preference, fits narrow and stays out of history`, async (t) => {
      const { page, quota, state, errors, say } = await open(t, engine, lang);
      await quota.getByText(await say("{n} left", { n: "51%" }), { exact: true }).waitFor();
      assert.match(await page.locator(".rt-accts li em").first().textContent(), new RegExp(await say("answering…")));
      assert.ok((await quota.textContent()).includes(await say("{n} left", { n: "55%" })));
      assert.equal(await quota.locator(".quota-reset").count(), 2);
      assert.equal(await quota.locator(".quota-track i").first().evaluate((e) => e.style.width), "51%");
      assert.equal(state.requests, 1);
      const scroll = await page.locator("#view-routing").evaluate((v) => v.scrollTop);
      await quota.locator(".quota-n").first().click();
      await page.clock.runFor(1100);
      await quota.getByText(await say("{n} used", { n: "49%" }), { exact: true }).waitFor();
      assert.equal(await quota.locator(".quota-track i").first().evaluate((e) => e.style.width), "49%");
      assert.equal(await page.locator("#view-routing").evaluate((v) => v.scrollTop), scroll, "switching Used / Left must not scroll");
      assert.equal(await quota.evaluate((e) => e.scrollWidth <= e.clientWidth), true);
      assert.equal(await quota.locator(".quota-reset").evaluateAll((es) => es.every((e) => e.scrollWidth <= e.clientWidth)), true);
      if (process.env.ARTIFACT_DIR) { await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true }); await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}.png`), fullPage: true }); }
      await scrollTo(page, page.locator(".rt-req").last());
      await pickInPlace(page, page.locator(".rt-req").last());
      assert.equal(await quota.isVisible(), false, "past request must not acquire current quota");
      const before = state.requests;
      await page.clock.fastForward(61000);
      assert.equal(state.requests, before, "historical requests must not poll current quota");
      await scrollTo(page, page.locator(".rt-req").first());
      await pickInPlace(page, page.locator(".rt-req").first());
      assert.equal(await quota.isVisible(), true);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: resting key retains quota, a hidden page does not poll, first read errors never claim zero`, async (t) => {
      const { page, quota, state, errors, say } = await open(t, engine, lang, [{ ...kimi, windows: [], error: "HTTP 503 Service Unavailable" }], true);
      await quota.locator(".subscription-error").waitFor();
      assert.equal(await quota.locator(".quota-track").count(), 0);
      assert.ok((await page.locator(".rt-accts li em").first().textContent()).includes(await say("rate limited")));
      state.cards = [kimi];
      await page.clock.fastForward(61000);
      await quota.getByText(await say("{n} left", { n: "51%" }), { exact: true }).waitFor();
      const before = state.requests;
      await page.evaluate(() => show("providers"));
      await page.clock.fastForward(120000);
      assert.equal(state.requests, before, "hidden Routing must not keep polling quotas");
      await page.evaluate(() => show("routing"));
      await quota.getByText(await say("{n} left", { n: "51%" }), { exact: true }).waitFor();
      await page.waitForFunction(() => !document.querySelector("#view-routing").hidden);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: separate key cards keep cached values, flag elapsed resets, refresh and clear empty data`, async (t) => {
      const stale = { ...kimi, user: "work", asOf: "2026-10-06T09:00:00Z", windows: [{ ...windows[0], resetsAt: "2026-10-06T10:00:00Z" }] };
      const spare = { ...kimi, user: "spare", windows: [{ ...windows[1], used: 12 }] };
      const { page, quota, state, errors, say } = await open(t, engine, lang, [stale, spare, { ...kimi, provider: "other", name: "Other" }]);
      await quota.getByText("spare", { exact: true }).waitFor();
      assert.equal(await quota.locator(".rt-quota-card").count(), 2);
      assert.ok((await quota.textContent()).includes(await say("{n} left", { n: "88%" })));
      const reset = await page.evaluate((at) => t("Reset time passed {when}", { when: new Date(at).toLocaleString() }), stale.windows[0].resetsAt);
      assert.ok((await quota.textContent()).includes(reset));
      assert.equal(await quota.locator(".quota-read.stale").count(), 1);
      await page.setViewportSize({ width: 440, height: 900 });
      await page.clock.runFor(100);
      assert.equal(await quota.locator(".quota-reset").evaluateAll((es) => es.every((e) => e.scrollWidth <= e.clientWidth)), true);
      state.fail = true;
      await page.clock.fastForward(61000);
      await quota.getByText(await say("Usage unavailable"), { exact: true }).waitFor();
      assert.ok((await quota.textContent()).includes("88%"), "a failed fetch retains the last reading");
      state.fail = false;
      state.cards = {};
      await page.clock.fastForward(61000);
      await page.waitForFunction(() => !quotasLoading);
      assert.ok((await quota.textContent()).includes("88%"), "a malformed response also retains the last reading");
      state.cards = [{ ...kimi, windows: [{ ...windows[0], used: 60 }] }];
      await page.clock.fastForward(61000);
      await quota.getByText(await say("{n} left", { n: "40%" }), { exact: true }).waitFor();
      assert.equal(await quota.locator(".rt-quota-card").count(), 1);
      state.cards = [];
      await page.clock.fastForward(61000);
      await page.waitForFunction(() => document.querySelector(".rt-quotas").hidden);
      assert.equal(await quota.isVisible(), false, "no quota is not zero usage");
      assert.deepEqual(errors, []);
    });
  }
}
