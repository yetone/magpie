const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
function req(id, agent, session, cost, priced = true) {
  const time = new Date(now.getTime() - (10 - id) * 60e3).toISOString();
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "Test account", kind: "account", model: id === 1 ? "model-a" : "model-b" };
  return { id, seq: id, time, agent, session, model: `relay/${seat.model}`, provider: "relay", cost, priced,
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: time, done: true, status: 200, ms: 4500, effort: "medium" }],
    done: true, status: 200, ms: 4500, tokens: 2000, ttft: 1200 };
}
const initial = [req(1, "codex", "conversation-a", 0.01), req(2, "claude", "conversation-a", 0),
  req(3, "codex", "conversation-a", 0, false), req(4, "codex", "conversation-b", 0.02),
  req(5, "codex", "conversation-a", 0.02), req(6, "codex", "", 0, false)];

function serve(lang, feed, fixture = initial) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }, { id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = (value) => { feed.next = null; resolve(value); }; }) : fixture;
      return json({ mine: true, now: now.toISOString(), seq: routes.at(-1)?.seq || 1, totals: { requests: 6, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: fixture.length }], routes: url.searchParams.get("day") ? fixture : [] });
    if (url.pathname === "/api/gateway/session-titles") return json(feed.names || {});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: routing groups by agent and session, totals known costs and keeps folds on live updates`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [], feed = {};
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group-by button").nth(1).click();
      await page.locator(".rt-session").nth(3).waitFor();
      const group = page.locator("button.rt-session").filter({ hasText: "Codex · conversation-a" });
      assert.equal(await page.locator(".rt-session").count(), 4, "identical IDs from different agents must not merge");
      assert.match(await group.locator(".summary").textContent(), lang === "zh" ? /3 个请求.*6.0k/ : /3 requests.*6.0k/);
      assert.equal(await group.locator(".cost").textContent(), "≈$0.030+", "partial totals must be marked");
      assert.equal(await page.locator("button.rt-session").filter({ hasText: "Claude Code" }).locator(".cost").textContent(), "≈$0.000", "zero price is known");
      assert.equal(await page.locator("div.rt-session .cost").textContent(), "—", "legacy requests are unknown");
      assert.equal(await page.locator(".rt-req").count(), 6);

      await group.click();
      assert.equal(await group.getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator(".rt-req").count(), 3);
      const handle = await group.elementHandle();
      await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 3);
      // Add one more call to the folded conversation through the real poll path.
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
      assert(feed.next, "long poll started");
      feed.next([req(7, "codex", "conversation-a", 0.01)]);
      await page.waitForFunction(() => [...document.querySelectorAll("button.rt-session")].some((e) => e.textContent.includes("≈$0.040+")));
      assert.equal(await group.getAttribute("aria-expanded"), "false");
      assert(await group.evaluate((e, previous) => e === previous, handle), "live update replaced the heading");

      // Currency changes redraw routing costs using the existing formatter.
      await page.evaluate(() => { currency = "cny"; fx = { rate: 7, at: null, stale: false }; renderCosts(); });
      assert.equal(await group.locator(".cost").textContent(), "≈¥0.280+");
      await group.click();
      assert.equal(await page.locator(".rt-req").count(), 7);
      await page.locator(".rt-group-by button").first().click();
      assert.equal(await page.locator(".rt-session").count(), 0);
      assert.equal(await page.locator(".rt-req").count(), 7);
      await page.locator(".rt-day").filter({ hasText: lang === "zh" ? "今天" : "today" }).click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 6);
      await page.locator(".rt-group-by button").nth(1).click();
      assert.equal(await page.locator(".rt-session").count(), 4);
      // Original request selection and its routing story still work.
      await page.locator(".rt-req").filter({ hasText: "model-a" }).click();
      await page.waitForFunction(() => document.querySelector(".rt-steps").textContent.includes("model-a"));
      assert.match(await page.locator(".rt-steps").textContent(), /model-a/);
      for (const width of [1440, 560]) {
        await page.setViewportSize({ width, height: 800 });
        await page.waitForTimeout(150);
        const fit = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `request list overflows at ${width}: ${JSON.stringify(fit)}`);
      }
      await page.setViewportSize({ width: 1100, height: 1200 });
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".rt-reqs").hover();
        await page.mouse.wheel(0, -3000);
        await page.waitForTimeout(200);
        await page.locator(".rt-cols").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-sessions.png`) });
      }
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: title helpers belong to the named parent, including totals and title-first delivery`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    const feed = {};
    const title = { ...req(1, "codex", "hidden-title", 0.013), kind: "thread_title", parentSession: "main-a" };
    const fixture = [title, req(2, "codex", "main-b", 0.2), req(3, "codex", "main-a", 0.029),
      { ...req(4, "codex", "unrelated-title", 0.1), kind: "thread_title" }];
    await page.route("**/*", serve("zh", feed, fixture));
    t.after(async () => { feed.next?.([]); await browser.close(); });
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-group-by button").nth(1).click();
    const group = page.locator("button.rt-session").filter({ hasText: "Codex · main-a" });
    await group.waitFor();
    assert.equal(await page.locator(".rt-session").count(), 3);
    assert.match(await group.locator(".summary").textContent(), /2 个请求/);
    assert.equal(await group.locator(".cost").textContent(), "≈$0.042");
    assert.equal(await page.locator(".rt-req .kind").filter({ hasText: "标题" }).count(), 2);
    await group.click();
    assert.equal(await page.locator(".rt-req").count(), 2, "only this parent and its title fold together");
    const handle = await group.elementHandle();
    for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
    assert(feed.next);
    feed.next(fixture.map((r) => r.session === "main-a" || r.parentSession === "main-a" ? { ...r, sessionTitle: "查询长沙天气" } : r));
    const named = page.locator("button.rt-session").filter({ hasText: "查询长沙天气" });
    await named.waitFor();
    assert.equal(await named.getAttribute("aria-expanded"), "false", "late title must not reset folding");
    assert(await named.evaluate((e, previous) => e === previous, handle));
    assert.match(await named.locator(".nm").getAttribute("title"), /main-a/);
    assert.equal(await named.locator(".cost").textContent(), "≈$0.042");
    assert.equal(await page.locator("button.rt-session").filter({ hasText: "main-b" }).count(), 1, "unknown names retain ID");
    await page.waitForTimeout(50);
    for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
    feed.next(fixture.map((r) => r.session === "main-a" || r.parentSession === "main-a" ? { ...r, sessionTitle: "长沙天气更新" } : r));
    await page.locator("button.rt-session").filter({ hasText: "长沙天气更新" }).waitFor();
    feed.names = { "main-a": "独立刷新名称" };
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    const refreshed = page.locator("button.rt-session").filter({ hasText: "独立刷新名称" });
    await refreshed.waitFor();
    assert.equal(await refreshed.getAttribute("aria-expanded"), "false", "names refresh without new routing requests");
    assert.equal(await page.locator("button.rt-session").filter({ hasText: "Claude" }).count(), 0);
    await page.locator(".rt-group-by button").first().click();
    assert.equal(await page.locator(".rt-req").count(), 4, "by-request view preserves all original requests");
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: request grouping is the default and either choice survives a reload`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage();
      page.setDefaultTimeout(5000);
      const feed = {};
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      const buttons = page.locator(".rt-group-by button");
      await page.locator(".rt-req").nth(5).waitFor();
      assert.equal(await buttons.first().getAttribute("aria-pressed"), "true");
      assert.equal(await page.locator(".rt-session").count(), 0);
      await buttons.nth(1).click();
      await page.locator(".rt-session").nth(3).waitFor();
      feed.next?.([]);
      await page.reload();
      await page.locator(".rt-session").nth(3).waitFor();
      assert.equal(await buttons.nth(1).getAttribute("aria-pressed"), "true");
      await buttons.first().click();
      feed.next?.([]);
      await page.reload();
      await page.locator(".rt-req").nth(5).waitFor();
      assert.equal(await buttons.first().getAttribute("aria-pressed"), "true");
      assert.equal(await page.locator(".rt-session").count(), 0);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: zero partial estimates keep the amount and one request uses the singular label`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage();
      page.setDefaultTimeout(5000);
      const feed = {};
      const fixture = [{ ...req(1, "codex", "partial", 0), unpriced: 1 },
        req(2, "codex", "mixed", 0), req(3, "codex", "mixed", 0, false),
        req(4, "claude", "unknown", 0, false)];
      await page.route("**/*", serve(lang, feed, fixture));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(3).waitFor();
      assert.equal(await page.locator(".rt-req").filter({ hasText: "model-a" }).locator(".cost").textContent(), "≈$0.000+", "a zero-priced attempt plus an unknown attempt retains its amount");
      await page.locator(".rt-group-by button").nth(1).click();
      const group = (name) => page.locator("button.rt-session").filter({ hasText: name });
      assert.equal(await group("Codex · partial").locator(".cost").textContent(), "≈$0.000+");
      assert.match(await group("Codex · partial").locator(".summary").textContent(), lang === "zh" ? /^1 个请求/ : /^1 request ·/);
      assert.equal(await group("Codex · mixed").locator(".cost").textContent(), "≈$0.000+", "a free request plus an unknown request is a partial zero estimate");
      assert.match(await group("Codex · mixed").locator(".summary").textContent(), lang === "zh" ? /^2 个请求/ : /^2 requests ·/);
      assert.equal(await group("Claude Code · unknown").locator(".cost").textContent(), "—", "all unknown stays unknown");
      await page.evaluate(() => { currency = "cny"; fx = { rate: 7, at: null, stale: false }; renderCosts(); });
      assert.equal(await group("Codex · mixed").locator(".cost").textContent(), "≈¥0.000+");
      assert.equal(await page.locator(".rt-req").filter({ hasText: "model-a" }).locator(".cost").textContent(), "≈¥0.000+");
    });
  }
}
