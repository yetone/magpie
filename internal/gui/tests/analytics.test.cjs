// Run with Node's test runner and Playwright on the module path; see README.md.
// Tests Analytics UI behavior:
// 1. Navigation & initial load with period preservation from Usage, back navigation with scroll/tabs preserved
// 2. Mode switching and mutually exclusive filter behavior (actual selection & assertion of request params)
// 3. Stale response mitigation (disordered network responses)
// 4. Drilldown to independent page view (compact chart on left, 50 calls list on right)
// 5. A call's route opens the real Routing stage/story inline in the right pane: the Analytics
//    page stays put (#view-analytics visible, #view-routing hidden, left ranking kept); the
//    return button restores the calls list with period, dimension, filters, entity, scroll and
//    masking, and locale/currency re-renders keep the open detail
// 6. Return from drilldown to dashboard preserving mode, filters, period and scroll position
// 7. 659px desktop width keeps two columns with no horizontal overflow, the inline graph fits
//    its right column, and graph nodes keep their widths without overlapping
// 8. Visible error state on calls fetch failure (no fake empty)
// 9. Zero data handling ('—' display, cost KPI empty/unknown states, no fake bars)
// 10. Locale switching (zh/en) covering notes, chips, tail notes, details, and shared account masking
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const mockAnalyticsData = {
  period: "30d",
  since: "2026-09-01T00:00:00Z",
  bucket: "day",
  summary: {
    calls: 120,
    errors: 6,
    input: 150000,
    output: 45000,
    cache_read: 80000,
    cache_write: 12000,
    reasoning: 3500,
    cost: 14.50,
    unpriced: 2,
    timed: 85,
    ttft_ms: 125000,
    decode_ms: 220000,
    decode_out: 42000,
    success_rate: 0.95,
    error_rate: 0.05,
    rate_limited: 3,
    server_err: 2,
    other_err: 1,
    canceled: 1,
    cancel_rate: 0.0083,
    ttft_p50: 850,
    ttft_p95: 1950,
    decode_calls: 80,
    speed: 52.4,
    cache_hit_rate: 0.3478,
  },
  rankings: {
    model: {
      summaries: {
        "gpt-5.5": {
          calls: 60,
          errors: 5,
          rate_limited: 3,
          server_err: 1,
          other_err: 1,
          canceled: 0,
          error_rate: 0.08,
          timed: 45,
          ttft_p50: 820,
          ttft_p95: 1950,
          decode_calls: 40,
          speed: 68.0,
          cost: 8.50,
          unpriced: 0,
          input: 90000,
          output: 25000,
          cache_read: 60000,
          cache_write: 7000,
          cache_hit_rate: 0.40,
        },
        "claude-3-7-sonnet": {
          calls: 40,
          errors: 1,
          rate_limited: 0,
          server_err: 1,
          other_err: 0,
          canceled: 1,
          error_rate: 0.025,
          timed: 35,
          ttft_p50: 650,
          ttft_p95: 1200,
          decode_calls: 35,
          speed: 45.2,
          cost: 6.00,
          unpriced: 1,
          input: 60000,
          output: 20000,
          cache_read: 20000,
          cache_write: 5000,
          cache_hit_rate: 0.25,
        },
        "zero-err-model": {
          calls: 20,
          errors: 0,
          rate_limited: 0,
          server_err: 0,
          other_err: 0,
          canceled: 0,
          error_rate: 0.0,
          timed: 20,
          ttft_p50: 500,
          ttft_p95: 900,
          decode_calls: 20,
          speed: 80.0,
          cost: 1.00,
          unpriced: 0,
          input: 10000,
          output: 5000,
          cache_read: 5000,
          cache_write: 1000,
          cache_hit_rate: 0.33,
        },
        "tiny-model": {
          calls: 2,
          errors: 0,
          rate_limited: 0,
          server_err: 0,
          other_err: 0,
          canceled: 0,
          error_rate: 0.0,
          timed: 2,
          ttft_p50: 300,
          ttft_p95: 400,
          decode_calls: 2,
          speed: 100.0,
          cost: 0.0,
          unpriced: 0,
          input: 500,
          output: 200,
          cache_read: 0,
          cache_write: 0,
          cache_hit_rate: 0.0,
        },
        "zero-cache-model": {
          calls: 10,
          errors: 0,
          rate_limited: 0,
          server_err: 0,
          other_err: 0,
          canceled: 0,
          error_rate: 0.0,
          timed: 10,
          ttft_p50: 400,
          ttft_p95: 800,
          decode_calls: 10,
          speed: 60.0,
          cost: 0.50,
          unpriced: 0,
          input: 12000,
          output: 2000,
          cache_read: 0,
          cache_write: 0,
          cache_hit_rate: 0.0,
        },
      },
      by_error_rate: [
        { key: "gpt-5.5", metric_val: 0.08, insufficient: false, share: 0.6 },
        { key: "claude-3-7-sonnet", metric_val: 0.025, insufficient: false, share: 0.4 },
        { key: "zero-err-model", metric_val: 0.0, insufficient: false, share: 0.2 },
        { key: "tiny-model", metric_val: 0.0, insufficient: true, share: 0.0 },
      ],
      by_ttft: [
        { key: "gpt-5.5", metric_val: 1950, insufficient: false, share: 0.6 },
        { key: "claude-3-7-sonnet", metric_val: 1200, insufficient: false, share: 0.4 },
      ],
      by_speed: [
        { key: "claude-3-7-sonnet", metric_val: 45.2, insufficient: false, share: 0.4 },
        { key: "gpt-5.5", metric_val: 68.0, insufficient: false, share: 0.6 },
      ],
      by_cost: [
        { key: "gpt-5.5", metric_val: 8.50, insufficient: false, has_unpriced: false, share: 0.586 },
        { key: "claude-3-7-sonnet", metric_val: 6.00, insufficient: false, has_unpriced: true, share: 0.414 },
      ],
      by_cache_rate: [
        { key: "claude-3-7-sonnet", metric_val: 0.25, insufficient: false, unknown_cache: false, share: 0.4 },
        { key: "gpt-5.5", metric_val: 0.40, insufficient: false, unknown_cache: false, share: 0.6 },
        { key: "zero-cache-model", metric_val: null, insufficient: false, unknown_cache: true, share: 0.0 },
      ],
    },
    provider: { summaries: {}, by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
    agent: { summaries: {}, by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
  },
  error_trend: [
    { time: "2026-09-29T10:00:00Z", label: "09-29", rate_limited: 2, server_err: 1, other_err: 0 },
    { time: "2026-09-30T10:00:00Z", label: "09-30", rate_limited: 1, server_err: 1, other_err: 1 },
  ],
  filters: {
    model: ["gpt-5.5", "claude-3-7-sonnet", "tiny-model"],
    provider: ["openai", "anthropic", "pick.owner@corp.example"],
    agent: ["codex", "claude-code"],
  },
};

const mockCallsData = {
  calls: Array.from({ length: 50 }, (_, i) => {
    const hasStreamErr = i === 1; // 2nd item has HTTP 200 + stream error
    // Give items valid route_ids; item 4 has no route_id (missing route record); item 7 has route_id 404 (non-existent route)
    let route_id = 123;
    if (i === 4) {
      route_id = undefined;
    } else if (i === 7) {
      route_id = 404;
    } else if (i === 8) {
      route_id = 999;
    } else {
      route_id = 123;
    }
    return {
      route_id,
      t: new Date(Date.parse("2026-09-30T10:14:22.318Z") - i * 60000).toISOString(),
      agent: i % 2 === 0 ? "codex" : "claude-code",
      provider: i % 2 === 0 ? "openai" : "anthropic",
      host: i === 0 ? "extremely-long-custom-subdomain-host-name-for-enterprise-compliance.proxy.openai.internal.cloud" : i === 5 ? "api.openai.com as host.owner@corp.example" : (i % 2 === 0 ? "api.openai.com" : "api.anthropic.com"),
      model: i % 2 === 0 ? "gpt-5.5" : "claude-3-7-sonnet",
      in: 4210 + i * 50,
      out: 850 + i * 10,
      cache_read: 3200 + i * 40,
      cache_write: 500,
      reasoning: i % 2 === 0 ? 320 : 0,
      effort: i % 2 === 0 ? "medium" : "none",
      ms: i === 2 ? 2000 : (2840 + i * 20),
      ttft_ms: i === 2 ? 1950 : (1950 + i * 15),
      status: i % 5 === 0 ? 429 : 200,
      err: hasStreamErr ? "synthetic stream failure" : undefined,
      session: i === 0
        ? "sess_user@test.org_very_long_enterprise_session_identifier_with_deep_nested_task_uuids_and_audit_tokens_xyz9876543210_abcdef"
        : `sess_user@test.org_${100 + i}`,
      kind: i % 2 === 0 ? "review" : "edit",
      cost: 0.05 + i * 0.002,
    };
  }),
};

const emptyAnalyticsData = {
  period: "30d",
  since: "2026-09-01T00:00:00Z",
  bucket: "day",
  summary: {
    calls: 0,
    errors: 0,
    in: 0,
    out: 0,
    input: 0,
    output: 0,
    cache_read: 0,
    cache_write: 0,
    reasoning: 0,
    cost: 0,
    unpriced: 0,
    timed: 0,
    decode_calls: 0,
    success_rate: null,
    error_rate: null,
    rate_limited: 0,
    server_err: 0,
    other_err: 0,
    canceled: 0,
    ttft_p50: null,
    ttft_p95: null,
    speed: null,
    cache_hit_rate: null,
  },
  rankings: {
    model: { by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
    provider: { by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
    agent: { by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
  },
  error_trend: [],
  filters: { model: [], provider: [], agent: [] },
};

const state = {
  agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }],
  profiles: [],
  settings: { lang: "en", theme: "light" },
};

async function launchBrowser(engine) {
  if (engine === "webkit") return webkit.launch();
  const options = { channel: "chromium" };
  if (process.env.CHROMIUM_PATH) options.executablePath = process.env.CHROMIUM_PATH;
  try {
    return await chromium.launch(options);
  } catch {
    return await chromium.launch();
  }
}

function createServer(customData = mockAnalyticsData, delay = 0) {
  return async function serve(route) {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });

    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(state.settings);
    if (url.pathname === "/api/gateway/trace") {
      return json({ mine: false, seq: 0, routes: [], totals: { requests: 0, rerouted: 0, errors: 0 }, now: new Date().toISOString() });
    }
    if (url.pathname === "/api/gateway/history") return json({ days: [], routes: [], cut: false });
    if (url.pathname === "/api/gateway/route") {
      const routeId = url.searchParams.get("id");
      if (routeId === "123") {
        return json({
          id: 123,
          time: "2026-09-30T10:14:22.318Z",
          agent: "codex",
          model: "gpt-5.5",
          provider: "openai",
          order: [
            { id: "openai", provider: "openai", name: "OpenAI", model: "gpt-5.5", kind: "provider", routing: "order" },
            { id: "acct-long", provider: "openai", name: "Enterprise account", model: "gpt-5.5", kind: "account", who: "extremely-long-enterprise-account-name-for-stress@corp.example", routing: "order" },
          ],
          tries: [{ id: "openai", model: "gpt-5.5", start: "2026-09-30T10:14:22.318Z", done: true, status: 200, ms: 50 }],
          done: true,
          status: 200,
          ms: 50,
        });
      }
      if (routeId === "999") {
        return json({
          id: 999,
          time: "2026-09-30T10:06:22.318Z",
          agent: "claude-code",
          model: "claude-3-7-sonnet",
          provider: "anthropic",
          order: [{ id: "anthropic", provider: "anthropic", name: "Claude", model: "claude-3-7-sonnet", kind: "provider", routing: "order" }],
          tries: [{ id: "anthropic", model: "claude-3-7-sonnet", start: "2026-09-30T10:06:22.318Z", done: true, status: 200, ms: 80 }],
          done: true,
          status: 200,
          ms: 80,
        });
      }
      return route.fulfill({ status: 404, body: "Routing history for this request is no longer available." });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ totals: { calls: 120, cost: 14.50 }, agents: [], models: [] });
    if (url.pathname === "/api/usage/requests") {
      return json({
        period: url.searchParams.get("period") || "30d",
        rows: [],
        offset: 0,
        total: 0,
        calls: 0,
        errors: 0,
        input: 0,
        output: 0,
        cache_read: 0,
        cache_write: 0,
        reasoning: 0,
        cost: 0,
        unpriced: 0,
        agents: [],
        providers: [],
        accounts: [],
        callerKeys: [],
        computers: [],
      });
    }
    if (url.pathname === "/api/analytics") {
      if (delay > 0) await new Promise((r) => setTimeout(r, delay));
      return json(customData);
    }
    if (url.pathname === "/api/analytics/calls") {
      return json(mockCallsData);
    }
    if (url.pathname.startsWith("/api/")) return json({});

    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// True when no inline route is on screen: either its story is gone, or its wrapper is hidden.
async function inlineReleased(page) {
  if (await page.locator("#anDrillRight .rt-steps").count() === 0) return true;
  return page.locator("#anDrillRouting").isHidden();
}

// Drill into the calls list from whatever the current dimension shows: a KPI tile in All mode,
// or the first ranking bar in a By Model/By Provider view.
async function openDrill(page) {
  await page.locator('#view-analytics').hover();
  await page.mouse.wheel(0, -10000);
  await page.waitForFunction(() => document.querySelector('#view-analytics').scrollTop === 0);
  let entry = page.locator('#anBody button[data-chart-id]').first();
  if (!(await entry.isVisible().catch(() => false))) {
    await page.locator('#anDim button.opt').first().click();
    entry = page.locator('#anBody button[data-chart-id]').first();
  }
  await entry.click();
  await page.locator("#anDrillPage:not([hidden])").waitFor();
}

// The inline return control, in the loaded routing (#rtBackAnalytics) or the error state.
function inlineBack(page) {
  return page.locator("#anDrillRouting #rtBackAnalytics, #anDrillRight .an-drill-route-back");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": analytics Routing story and return state", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const routeLookups = [];
    const historyLookups = [];
    page.on("request", (req) => {
      const url = new URL(req.url());
      if (url.pathname === "/api/gateway/route") routeLookups.push(url);
      if (url.pathname === "/api/gateway/history") historyLookups.push(url);
    });
    await page.route("**/*", createServer());

    t.after(async () => {
      await browser.close();
    });

    // 1. Navigation from Usage page, verifying period inheritance
    await page.goto("http://magpie.test/?view=usage");
    await page.locator("#openAnalytics").waitFor();
    await page.locator("#openAnalytics").click();
    await page.locator("#view-analytics:not([hidden])").waitFor();

    assert(await page.locator("#view-analytics").isVisible(), "analytics page must be visible");
    assert(await page.locator("#anHead").isVisible(), "anHead must be rendered");
    assert(await page.locator("#anControls").isVisible(), "anControls must be rendered");
    assert(await page.locator("#anBack").isVisible(), "anBack must be rendered");

    // 2. Set Period to 7 days, Mode to By Model, and Filter provider to openai
    const period7dBtn = page.locator("#anPeriod button").filter({ hasText: "7 days" });
    await period7dBtn.click();
    await page.waitForFunction(() => document.querySelector("#anPeriod button.opt.on")?.textContent?.includes("7 days"));
    assert.match(await page.locator("#anPeriod button.opt.on").textContent(), /7 days/, "7 days period must be active");

    const modelDimBtn = page.locator("#anDim button").filter({ hasText: "By Model" });
    await modelDimBtn.click();
    await page.locator(".an-bars").first().waitFor();
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "By Model", "By Model dimension must be active");

    const provFilterWrap = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    await provFilterWrap.locator(".an-filter-btn").click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    await page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "openai" }).click();
    await page.waitForFunction(() => {
      const txt = document.querySelector('.an-filter-wrap[data-filter-dim="provider"] .an-filter-btn')?.textContent || "";
      return txt.includes("openai");
    });
    assert.match(await provFilterWrap.locator(".an-filter-btn").textContent(), /openai/, "provider filter must be openai");

    const barRows = page.locator(".an-bar-row");
    await barRows.first().waitFor();
    assert((await barRows.count()) > 0, "must render bar rows in By Model mode");

    // Click bar to enter drilldown page
    await barRows.first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // Left compact chart & right calls list
    assert(await page.locator("#anDrillLeft").isVisible(), "left compact chart must be visible");
    assert(await page.locator("#anDrillRight").isVisible(), "right calls list pane must be visible");

    // Top 50 calls items rendered in right pane
    const callItems = page.locator(".an-call-item");
    await callItems.first().waitFor();
    assert((await callItems.count()) > 0, "calls items must be listed in right pane");
    assert.equal(routeLookups.length, 0, "loading the usage list must not fetch Routing records");
    // 4. Test real non-zero scrolling on 50 calls list:
    // Scroll down the view to non-zero scrollTop (e.g. 250px)
    await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      v.scrollTop = 250;
    });
    await page.waitForTimeout(50);
    const scrollBeforeDetail = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert(scrollBeforeDetail > 100, `view must have scrolled to non-zero, got ${scrollBeforeDetail}`);

    // 4b. Verify calls item with missing route_id (i=4): disabled with title/aria-label, click does nothing
    const noRouteItem = callItems.nth(4);
    const noRouteTitle = await noRouteItem.getAttribute("title");
    const noRouteAria = await noRouteItem.getAttribute("aria-disabled");
    assert.equal(noRouteAria, "true", "item with missing route_id must have aria-disabled='true'");
    assert.match(noRouteTitle, /This call has no linked routing record\.|该记录没有关联的路由信息/, "missing route_id title check");


    // Clicking disabled item sends no route query and stays on calls list
    await noRouteItem.click({ force: true });
    assert.equal(routeLookups.length, 0, "clicking item without route_id must send no route query");
    assert(await page.locator("#view-routing").isHidden(), "must stay on analytics calls list when route_id is missing");

    // 4c. Verify 404 route handling (i=7): the right pane shows the inline error with Retry and a
    // way back, then Back restores the calls list; the page never leaves Analytics.
    const route404Item = callItems.nth(7);
    await route404Item.click();
    const routeErrBox = page.locator("#anDrillRight .an-drill-state-box.an-err");
    await routeErrBox.waitFor();
    assert(await page.locator("#view-routing").isHidden(), "404 route lookup must not navigate away from analytics");
    assert(await page.locator("#anDrillRight .an-drill-route-back").isVisible(), "404 must offer a way back to the calls list");
    assert(await page.locator("#anDrillRight .an-drill-retry-btn").isVisible(), "404 must offer a retry");
    assert.match(await routeErrBox.textContent(), /Routing history for this request is no longer available|路由历史已不可用/, "inline error should report routing unavailable");
    await page.locator("#anDrillRight .an-drill-route-back").click();
    await page.locator(".an-calls-list").waitFor();
    assert(await page.locator(".an-calls-list").isVisible(), "calls list must be restored after a 404");

    // The target is absent from the history list; the single-record lookup still opens it.
    let analyticsFetches = 0;
    let callsFetches = 0;
    page.on("request", (req) => {
      const u = req.url();
      if (u.includes("/api/analytics?")) analyticsFetches++;
      if (u.includes("/api/analytics/calls?")) callsFetches++;
    });

    const firstCallItem = callItems.nth(5);
    const scrollAtClick = await page.locator("#view-analytics").evaluate((v) => v.scrollTop);
    await firstCallItem.focus();
    await firstCallItem.press("Enter");

    // The route opens in place: the Analytics page stays put, the Routing page stays hidden, and
    // the real Routing stage/story is mounted inside the right pane.
    const inlineRouting = page.locator("#anDrillRouting");
    await inlineRouting.waitFor();
    assert(await page.locator("#view-analytics").isVisible(), "analytics page must stay visible while a route is open");
    assert(await page.locator("#view-routing").isHidden(), "the Routing page must not be shown for an inline route");
    assert(await page.locator("#anDrillLeft").isVisible(), "left ranking must stay visible while a route is open");
    assert.match(await page.locator("#anDrillLeft").textContent(), /gpt-5\.5/, "left ranking keeps the selected entity while a route is open");
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert(await page.locator("#anDrillRight .rt-stage").isVisible(), "the inline Routing stage must render in the right pane");
    const inlineSteps = page.locator("#anDrillRight .rt-steps");
    assert.match(await inlineSteps.textContent(), /gpt-5\.5/, "inline routing story must display the model from the route fixture");
    const activeWhileOpen = await page.locator(".an-call-item.active").count();
    const listWhileOpen = await page.locator("#anDrillRight .an-calls-list").count();
    assert(activeWhileOpen === 1 || listWhileOpen === 0, "the opened row is marked active, or the inline route replaces the list");
    assert.equal(routeLookups.at(-1).searchParams.get("id"), "123", "inline route lookup uses the call's route id");
    assert.equal(routeLookups.at(-1).searchParams.get("day"), "2026-09-30", "inline route lookup names the call's day");
    assert(!historyLookups.some((url) => url.searchParams.get("day")), "an inline route needs no whole-day history read");

    // The return button lives in the inline view.
    const backToAnalyticsBtn = inlineBack(page);
    await backToAnalyticsBtn.waitFor();
    assert(await backToAnalyticsBtn.isVisible(), "inline routing must show the 'Back to analytics calls' return button");

    // A replay inside the inline drill keeps the selected story and stays mounted.
    const inlineReplay = page.locator("#anDrillRouting button").filter({ hasText: /^Replay$/ });
    if (await inlineReplay.count()) {
      await inlineReplay.first().click();
      const inlineStop = page.locator("#anDrillRouting button").filter({ hasText: /^Stop replay$/ });
      if (await inlineStop.count()) await inlineStop.first().click();
      assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "stopping a replay keeps the selected story inline");
      assert(await page.locator("#anDrillRouting").isVisible(), "the inline drill stays mounted after a replay");
    }

    const preReturnAnalyticsFetches = analyticsFetches;
    const preReturnCallsFetches = callsFetches;

    // Clicking the return button restores the calls list in place, without leaving the page.
    await backToAnalyticsBtn.click();
    await page.locator(".an-calls-list").waitFor();
    assert(await page.locator("#view-analytics").isVisible(), "analytics page must remain visible after return");
    assert(await page.locator("#view-routing").isHidden(), "Routing page stays hidden after an inline return");
    const stepsAfterReturn = await page.locator("#anDrillRight .rt-steps").count();
    const wrapperHidden = await page.locator("#anDrillRouting").isHidden();
    assert(stepsAfterReturn === 0 || wrapperHidden, "the inline route must be released after return");
    assert(await page.locator(".an-calls-list").isVisible(), "calls list must be restored");
    assert.equal(await page.locator(".an-call-item.active").count(), 1, "return must keep the opened call row marked active");

    // Must NOT re-fetch analytics or calls when returning to the list
    assert.equal(analyticsFetches, preReturnAnalyticsFetches, "return from an inline route must not re-fetch analytics");
    assert.equal(callsFetches, preReturnCallsFetches, "return from an inline route must not re-fetch calls");

    // Verify period (7 days), dimension (By Model), filter (provider = openai) and selected entity are strictly preserved
    assert.match(await page.locator("#anPeriod button.opt.on").textContent(), /7 days/, "7 days period must remain active after return");
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "By Model", "By Model dimension must remain active after return");
    assert.match(await provFilterWrap.locator(".an-filter-btn").textContent(), /openai/, "provider filter must remain openai after return");
    assert(await page.locator("#anDrillLeft").isVisible(), "drill left panel with selected entity must remain active after return");
    assert.match(await page.locator("#anDrillLeft").textContent(), /gpt-5\.5/, "selected entity gpt-5.5 must remain active after return");

    // Verify scroll position was restored
    await page.waitForFunction((expected) => Math.abs(document.querySelector("#view-analytics").scrollTop - expected) < 5, scrollAtClick);
    assert(Math.abs(await page.locator("#view-analytics").evaluate((v) => v.scrollTop) - scrollAtClick) < 5);
    // Verify mini chart: zero-err-model (error_rate: 0, calls: 20) renders seg width 0% while positive models have > 0%
    const miniRows = page.locator(".an-drill-mini-row");
    const zeroErrMiniRow = miniRows.filter({ hasText: "zero-err-model" });
    assert.equal(await zeroErrMiniRow.count(), 1, "zero-err-model must appear in mini chart rows");
    const zeroErrSegWidth = await zeroErrMiniRow.locator(".an-drill-mini-seg").evaluate((el) => el.style.width);
    assert.equal(zeroErrSegWidth, "0%", "entity with metric 0 must have mini chart segment width 0%");
    const positiveMiniRow = miniRows.filter({ hasText: "gpt-5.5" });
    const posSegWidth = await positiveMiniRow.locator(".an-drill-mini-seg").evaluate((el) => el.style.width);
    assert.notEqual(posSegWidth, "0%", "entity with positive metric must have mini chart segment width > 0%");

    // 5b. Verify HTTP 200 stream error badge (i=1)
    const secondItem = callItems.nth(1);
    const badge2 = secondItem.locator(".an-st-badge");
    assert.equal(await badge2.textContent(), "200");
    assert(await badge2.evaluate((el) => el.classList.contains("st-err") && !el.classList.contains("st-5xx")), "HTTP 200 with stream error must have error badge class st-err and not st-5xx");

    // Also assert call items bottom do not contain literal 'null' text
    const bottomText = await secondItem.locator(".an-call-item-bottom").textContent();
    assert(!bottomText.includes("null"), `call item bottom must not render literal 'null' string: ${bottomText}`);
    // 5d. Masking on the drill page is shared, and an inline route is masked in place.
    const maskBtn = page.locator("#anDrillMask");
    const headBox = await page.locator(".an-drill-page-head").boundingBox();
    const maskBox = await maskBtn.boundingBox();
    assert(Math.abs(headBox.x + headBox.width - (maskBox.x + maskBox.width)) < 2, "drill mask button must sit at the header row's right end");
    await maskBtn.click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "true");
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), "1", "Analytics uses Usage's existing masking preference");

    // Open the inline route again with masking on: its accounts are masked, and the state survives return.
    await firstCallItem.click();
    await inlineRouting.waitFor();
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert(await page.locator("#anDrillRight .pii").count() > 0, "inline routing must mask the account it draws");
    assert.equal(await maskBtn.getAttribute("aria-pressed"), "true", "drill mask stays on while the inline route is open");
    await backToAnalyticsBtn.click();
    await page.locator(".an-calls-list").waitFor();
    assert.equal(await maskBtn.getAttribute("aria-pressed"), "true", "mask must be preserved across an inline route return");
    assert.match(await maskBtn.getAttribute("title"), /click to show them|点一下即可显示/, "mask button keeps its 'how to show them' tooltip");

    // Locale switch while the inline route is open: the return button and the detail re-render in place.
    await firstCallItem.click();
    await inlineRouting.waitFor();
    await page.locator("#anDrillRight .rt-steps").waitFor();
    await page.evaluate(() => window.setLocale("zh"));
    assert.match(await page.locator("#anDrillRouting .rt-back-analytics, #anDrillRouting #rtBackAnalytics").textContent(), /返回分析列表/, "return button must render in the active locale");
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "locale switch must keep the open route detail");
    await backToAnalyticsBtn.click();
    await page.locator(".an-calls-list").waitFor();
    assert.equal(await callItems.nth(4).getAttribute("title"), "该记录没有关联的路由信息", "disabled call tooltip follows the locale");
    await page.evaluate(() => window.setLocale("en"));
    await maskBtn.click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "false");
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), "0", "unmasking updates the existing shared preference");

    // 5e. Leaving Analytics for another page releases the inline route, and the Usage page still
    // opens the full Routing page through openRoute (no inline mount there, no return button).
    await firstCallItem.click();
    await inlineRouting.waitFor();
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();
    assert(await inlineReleased(page), "leaving Analytics must release the inline route");
    assert.equal(await page.locator(".rt-back-analytics, #rtBackAnalytics").count(), 0, "the standalone Routing page shows no analytics return button");

    // The Routing page must not keep the drilled story once the inline drill is left.
    await page.evaluate(() => window.show("routing"));
    await page.locator("#view-routing:not([hidden])").waitFor();
    await page.waitForTimeout(50);
    assert(await page.locator('#view-routing .rt-log').isHidden(), "an empty Routing page hides the drilled story");
    assert.equal(await page.locator('#view-routing .rt-accts li.idle').count(), 1, "Routing restores its empty stage");
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();

    await page.evaluate(() => window.openRoute(123, "2026-09-30T10:14:22.318Z"));
    await page.locator("#view-routing:not([hidden])").waitFor();
    assert(await page.locator("#view-analytics").isHidden(), "Usage openRoute still switches to the Routing page");
    await page.locator("#view-routing .rt-steps").waitFor();
    assert.match(await page.locator("#view-routing .rt-steps").textContent(), /gpt-5\.5/, "Usage openRoute still shows the route story");
    assert.equal(await page.locator(".rt-back-analytics, #rtBackAnalytics").count(), 0, "Usage openRoute must not show the analytics return button");
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();

    // Re-entering analytics fresh from Usage resets drill; re-drill and verify dashboard return
    await page.evaluate(() => window.show("analytics"));
    await page.locator("#view-analytics:not([hidden])").waitFor();
    // In fresh analytics, By Model is selected; drill into first bar
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // 7. Return to main dashboard: preserves mode, filters, period and scroll
    const backToDashBtn = page.locator("#anBack");
    await backToDashBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();
    assert(await page.locator("#anBody").isVisible(), "dashboard body must be restored");
    assert(await page.locator("#anControls").isVisible(), "dashboard controls must be restored");
    assert(await page.locator("#anDrillPage").isHidden(), "drill page must be hidden");

    // Verify mode is still "By Model"
    const activeDim = await page.locator("#anDim button.on").textContent();
    assert.equal(activeDim.trim(), "By Model", "dashboard dimension mode must be preserved");

    // 8. Return back to Usage page
    const backBtn = page.locator("#anBack");
    await backBtn.click();
    await page.locator("#view-usage:not([hidden])").waitFor();
    assert(await page.locator("#view-usage").isVisible(), "must navigate back to usage view");
    assert.equal(errors.length, 0, `no page errors occurred: ${errors.join(", ")}`);
  });

  test(engine + ": inline error counter mismatch does not steal story from analytics inline route", async (t) => {
    const browser = await launchBrowser(engine);
    t.after(() => browser.close());
    const page = await browser.newPage();
    page.setDefaultTimeout(6000);

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/gateway/trace") {
        if (url.searchParams.has("wait")) await new Promise((resolve) => setTimeout(resolve, 100));
        return route.fulfill({
          json: {
            mine: true,
            seq: 1,
            totals: { requests: 1, rerouted: 0, errors: 1 },
            routes: [
              {
                id: 456,
                time: new Date().toISOString(),
                agent: "codex",
                model: "gpt-failed-b",
                provider: "openai",
                order: [{ id: "openai", provider: "openai", name: "OpenAI", model: "gpt-failed-b", kind: "provider", routing: "order" }],
                tries: [{ id: "openai", model: "gpt-failed-b", start: new Date().toISOString(), done: true, status: 500, ms: 100 }],
                done: true,
                status: 500,
                ms: 100,
              },
            ],
            now: new Date().toISOString(),
          },
        });
      }
      if (url.pathname === "/api/gateway/route") {
        const id = url.searchParams.get("id");
        if (id === "456") {
          return route.fulfill({
            json: {
              id: 456,
              time: new Date().toISOString(),
              agent: "codex",
              model: "gpt-failed-b",
              provider: "openai",
              order: [{ id: "openai", provider: "openai", name: "OpenAI", model: "gpt-failed-b", kind: "provider", routing: "order" }],
              tries: [{ id: "openai", model: "gpt-failed-b", start: new Date().toISOString(), done: true, status: 500, ms: 100 }],
              done: true,
              status: 500,
              ms: 100,
            },
          });
        }
      }
      return createServer()(route);
    });

    await page.goto("http://magpie.test/?view=analytics");
    await openDrill(page);
    const callA = page.locator(".an-call-item").first();
    await callA.click();

    // Story A (id 123, gpt-5.5) mounts inline inside right pane
    const inlineRouting = page.locator("#anDrillRouting");
    await inlineRouting.waitFor();
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "inline routing story must show route A");

    // Clicking the errors counter while inlineMounted must be ignored: story A preserved
    const errCounter = page.locator("#anDrillRouting .rt-errs");
    await errCounter.click();
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "story remains A after clicking error counter inline");

    // Return to calls list preserves selected A
    await page.locator("#anDrillRouting #rtBackAnalytics").click();
    await page.locator(".an-calls-list").waitFor();
    assert.equal(await page.locator(".an-call-item.active").count(), 1, "call A remains selected");

    // Standalone routing still allows clicking errors counter to open failed B
    await page.evaluate(() => window.show("routing"));
    await page.locator("#view-routing:not([hidden])").waitFor();
    const standaloneErrCounter = page.locator("#view-routing .rt-errs");
    await standaloneErrCounter.waitFor();
    await standaloneErrCounter.click();
    await page.locator("#view-routing .rt-steps").waitFor();
    assert.match(await page.locator("#view-routing .rt-steps").textContent(), /gpt-failed-b/, "standalone routing errors counter opens failed B");
  });

  test(engine + ": 659px desktop width prioritizes two columns without horizontal overflow", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    // Test 659px desktop width constraint
    const context = await browser.newContext({ viewport: { width: 659, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    await page.route("**/*", createServer());

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    // Ensure initial analytics data is loaded before clicking dimension
    await page.locator(".an-kpi-val").first().waitFor();
    // Verify first row at 659px desktop width: all elements in single row, vertically centered (centerY within ±2px)
    const headHorizScroll = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth > v.clientWidth;
    });
    assert(!headHorizScroll, "659px main dashboard head must not trigger horizontal scrollbar");

    // Check vertical center alignment across Back, Period, Dim and Filters
    const centerDiff = await page.evaluate(() => {
      const back = document.querySelector("#anBack");
      const period = document.querySelector("#anPeriod");
      const dim = document.querySelector("#anDim");
      const rBack = back.getBoundingClientRect();
      const rPeriod = period.getBoundingClientRect();
      const rDim = dim.getBoundingClientRect();
      const cBack = rBack.top + rBack.height / 2;
      const cPeriod = rPeriod.top + rPeriod.height / 2;
      const cDim = rDim.top + rDim.height / 2;
      return Math.max(Math.abs(cBack - cPeriod), Math.abs(cBack - cDim));
    });
    assert(centerDiff <= 3, `first row components must be vertically centered together, max diff: ${centerDiff}px`);
    // Drilldown to drill page
    const modelDimBtn = page.locator("#anDim button").filter({ hasText: "By Model" });
    await modelDimBtn.click();
    await page.locator(".an-bar-row").first().waitFor();
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    // At 659px, layout must still have 2 columns (left between 180px and 220px)
    const leftBox = page.locator("#anDrillLeft");
    const rightBox = page.locator("#anDrillRight");

    const leftWidth = await leftBox.evaluate((e) => e.getBoundingClientRect().width);
    const rightWidth = await rightBox.evaluate((e) => e.getBoundingClientRect().width);

    assert(leftWidth >= 170 && leftWidth <= 230, `left column width should be around 180-220px, got ${leftWidth}`);
    assert(rightWidth > 300, `right column should take remainder width, got ${rightWidth}`);
    // Assert calls list has no horizontal scroll
    const listHorizScroll = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth > v.clientWidth;
    });
    assert(!listHorizScroll, "659px calls list must not trigger horizontal scrollbar");

    // 659px check: the route opens inline in the right column; the graph must fit its narrow pane
    // (about 401px) with real node widths, no overlap and no clipping.
    await page.locator(".an-call-item").first().click();
    await page.locator("#anDrillRouting").waitFor();
    assert(await page.locator("#view-analytics").isVisible(), "659px analytics page must stay visible for an inline route");
    assert(await page.locator("#view-routing").isHidden(), "659px Routing page must stay hidden for an inline route");
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "659px inline routing story must show the model");
    // A long account label is drawn, so the narrow column is really exercised.
    assert.match(await page.locator("#anDrillRight .rt-accts").textContent(), /extremely-long-enterprise-account-name-for-stress/, "659px graph must draw the long account label");

    const pageOverflow = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth - v.clientWidth;
    });
    assert(pageOverflow <= 0, `659px analytics page must not overflow horizontally, got ${pageOverflow}`);
    const rightOverflow = await rightBox.evaluate((e) => e.scrollWidth - e.clientWidth);
    assert(rightOverflow <= 0, `659px right pane must not overflow horizontally, got ${rightOverflow}`);

    const geom = await rightBox.evaluate((pane) => {
      const stage = pane.querySelector(".rt-stage");
      const nodes = [
        ...pane.querySelectorAll(".rt-srcs .rt-node"),
        ...pane.querySelectorAll(".rt-hub"),
        ...pane.querySelectorAll(".rt-accts li"),
      ].map((n) => { const r = n.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom, w: r.width }; });
      const sr = stage.getBoundingClientRect();
      return { pane: pane.clientWidth, stage: { l: sr.left, r: sr.right, w: sr.width }, nodes };
    });
    assert(geom.nodes.length >= 2, `inline graph must draw more than one node, got ${geom.nodes.length}`);
    assert(geom.stage.w <= geom.pane + 1, `inline stage must fit the right pane, stage ${geom.stage.w} pane ${geom.pane}`);
    for (const n of geom.nodes) {
      assert(n.w > 0, `every inline graph node must keep a positive width, got ${n.w}`);
      assert(n.l >= geom.stage.l - 1 && n.r <= geom.stage.r + 1, `node must stay inside the stage: ${JSON.stringify(n)}`);
    }
    // No two nodes may overlap unless they stack vertically (a very narrow stage falls back to rows).
    for (let i = 0; i < geom.nodes.length; i++) {
      for (let j = i + 1; j < geom.nodes.length; j++) {
        const a = geom.nodes[i], b = geom.nodes[j];
        const hOverlap = a.l < b.r - 1 && b.l < a.r - 1;
        const vOverlap = a.t < b.b - 1 && b.t < a.b - 1;
        assert(!(hOverlap && vOverlap), `inline graph nodes must not overlap: ${JSON.stringify(a)} vs ${JSON.stringify(b)}`);
      }
    }

    // Return to the analytics calls list and verify no horizontal scroll.
    await page.locator("#anDrillRouting .rt-back-analytics, #anDrillRouting #rtBackAnalytics").click();
    await page.locator(".an-calls-list").waitFor();
    const restoredOverflow = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth - v.clientWidth;
    });
    assert(restoredOverflow <= 0, "659px restored calls list must not trigger a horizontal scrollbar");
  });

  test(engine + ": a route read failure keeps the analytics calls and a retry opens the inline route", async (t) => {
    const browser = await launchBrowser(engine);
    t.after(() => browser.close());
    const page = await browser.newPage();
    let failing = true;
    await page.route("**/*", (route) => {
      const url = new URL(route.request().url());
      if (failing && url.pathname === "/api/gateway/route") {
        return route.fulfill({ status: 503, body: "synthetic routing read failure" });
      }
      return createServer()(route);
    });
    await page.goto("http://magpie.test/?view=analytics");
    await openDrill(page);
    const call = page.locator(".an-call-item").first();

    // The failure is shown inline, replacing the calls list, without leaving Analytics.
    await call.click();
    const errBox = page.locator("#anDrillRight .an-drill-state-box.an-err");
    await errBox.waitFor();
    assert(await page.locator("#view-analytics").isVisible(), "a route failure must keep the analytics page");
    assert(await page.locator("#view-routing").isHidden(), "a route failure must not show the Routing page");
    assert.match(await errBox.textContent(), /synthetic routing read failure/, "the route failure is shown inline");
    assert(await page.locator("#anDrillRight .an-drill-retry-btn").isVisible(), "the inline error offers a retry");
    assert(await page.locator("#anDrillRight .an-drill-route-back").isVisible(), "the inline error offers a way back");

    // A retry opens the inline route.
    failing = false;
    await page.locator("#anDrillRight .an-drill-retry-btn").click();
    await page.locator("#anDrillRouting").waitFor();
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert(await page.locator("#view-analytics").isVisible(), "the retried route stays on the analytics page");
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /gpt-5\.5/, "the retried route draws its story inline");
  });

  test(engine + ": out-of-order calls responses during entity switching and navigation cancellation", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    let entityRace = true;
    let slowEntityCallsResolve;
    let signalEntityStarted;
    const entityStarted = new Promise((resolve) => { signalEntityStarted = resolve; });
    let slowRouteLookupResolve;
    let signalRouteStarted;
    const routeRequests = [];

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/gateway/route") {
        const id = url.searchParams.get("id");
        routeRequests.push(id);
        if (id === "123") {
          const pending = new Promise((resolve) => { slowRouteLookupResolve = resolve; });
          signalRouteStarted();
          await pending;
          return route.fulfill({
            json: {
              id: 123,
              time: "2026-09-30T10:14:22.318Z",
              agent: "codex",
              model: "gpt-5.5",
              provider: "openai",
              order: [{ id: "openai", provider: "openai", name: "OpenAI", model: "gpt-5.5", kind: "provider", routing: "order" }],
              tries: [{ id: "openai", model: "gpt-5.5", start: "2026-09-30T10:14:22.318Z", done: true, status: 200, ms: 50 }],
              done: true,
              status: 200,
              ms: 50,
            },
          });
        }
        if (id === "999") {
          // Fast route lookup for request B
          return route.fulfill({
            json: {
              id: 999,
              time: "2026-09-30T10:06:22.318Z",
              agent: "claude-code",
              model: "claude-3-7-sonnet",
              provider: "anthropic",
              order: [{ id: "anthropic", provider: "anthropic", name: "Claude", model: "claude-3-7-sonnet", kind: "provider", routing: "order" }],
              tries: [{ id: "anthropic", model: "claude-3-7-sonnet", start: "2026-09-30T10:06:22.318Z", done: true, status: 200, ms: 80 }],
              done: true,
              status: 200,
              ms: 80,
            },
          });
        }
      }
      if (url.pathname === "/api/analytics/calls") {
        const modelParam = url.searchParams.get("model");
        if (modelParam === "gpt-5.5" && entityRace) {
          const pending = new Promise((resolve) => { slowEntityCallsResolve = resolve; });
          signalEntityStarted();
          await pending;
          return route.fulfill({
            json: {
              calls: [{ ...mockCallsData.calls[0], agent: "stale-gpt-agent" }],
            },
          });
        }
        if (modelParam === "claude-3-7-sonnet" && entityRace) {
          // Fast response for second entity
          return route.fulfill({
            json: {
              calls: [{ ...mockCallsData.calls[1], agent: "fast-correct-agent" }],
            },
          });
        }
        return route.fulfill({ json: mockCallsData });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // Switch to By Model and drilldown into first entity (gpt-5.5)
    await page.locator("#anDim button").filter({ hasText: "By Model" }).click();
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // The older entity result must not replace the selected entity's calls.
    await entityStarted;
    const miniRows = page.locator(".an-drill-mini-row");
    await miniRows.nth(1).click();
    await page.waitForFunction(() => document.querySelector(".an-call-item-agent")?.textContent === "fast-correct-agent");
    slowEntityCallsResolve();
    await page.waitForTimeout(100);
    assert.equal(await page.locator(".an-call-item-agent").first().textContent(), "fast-correct-agent");
    entityRace = false;
    await miniRows.first().click();
    const callItems = page.locator(".an-call-item");
    await callItems.nth(8).waitFor();

    // A route in flight shows its return control immediately, so the reader can leave while it loads.
    let routeAStartedPromise = new Promise((resolve) => { signalRouteStarted = resolve; });
    await callItems.first().click();
    await routeAStartedPromise;
    assert.equal(routeRequests.length, 1, "one route lookup per click");
    await page.locator("#anDrillRouting #rtBackAnalytics").waitFor();
    assert(await page.locator("#anDrillRouting #rtBackAnalytics").isVisible(), "a loading route offers its return button");

    // Leaving via the inline return cancels route A.
    await page.locator("#anDrillRouting #rtBackAnalytics").click();
    await page.locator(".an-calls-list").waitFor();

    // B (route 999) opens fast; the canceled A resolving late must not overwrite it.
    await callItems.nth(8).click();
    await page.locator("#anDrillRouting").waitFor();
    await page.locator("#anDrillRight .rt-steps").waitFor();
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /claude-3-7-sonnet/, "the fast route must draw its story inline");
    assert(await page.locator("#view-analytics").isVisible(), "an inline route keeps the analytics page");
    assert(await page.locator("#view-routing").isHidden(), "an inline route never shows the Routing page");

    slowRouteLookupResolve();
    await page.waitForTimeout(200);
    assert.match(await page.locator("#anDrillRight .rt-steps").textContent(), /claude-3-7-sonnet/, "a canceled route resolving late must not overwrite the active inline route");

    // Return to the calls list (still on the analytics page).
    await page.locator("#anDrillRouting #rtBackAnalytics").click();
    await page.locator(".an-calls-list").waitFor();

    // 3. Navigating away while a route lookup is pending cancels it: no inline route appears later.
    routeAStartedPromise = new Promise((resolve) => { signalRouteStarted = resolve; });
    await callItems.first().click();
    await routeAStartedPromise;
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();
    slowRouteLookupResolve();
    await page.waitForTimeout(100);
    assert(await page.locator("#view-usage").isVisible(), "a late route lookup must not navigate away from Usage");
    assert(await page.locator("#view-routing").isHidden(), "routing must remain hidden after navigating away");
    assert(await inlineReleased(page), "a late route lookup must not mount an inline route");

    // 4. Changing the drilled entity also cancels a pending route lookup.
    await page.evaluate(() => window.show("analytics"));
    await page.locator("#view-analytics:not([hidden])").waitFor();
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    await page.locator(".an-call-item").nth(8).waitFor();
    routeAStartedPromise = new Promise((resolve) => { signalRouteStarted = resolve; });
    await page.locator(".an-call-item").first().click();
    await routeAStartedPromise;
    // Switch the entity while route A is pending.
    await page.locator(".an-drill-mini-row").nth(1).click();
    await page.waitForTimeout(50);
    slowRouteLookupResolve();
    await page.waitForTimeout(150);
    assert(await page.locator(".an-calls-list").isVisible(), "changing the entity keeps the calls list");
    assert(await inlineReleased(page), "changing the entity must not mount a late inline route");
    assert(await page.locator("#view-routing").isHidden(), "changing the entity must not show the Routing page");
  });

  test(engine + ": delayed analytics fetch failure does not pop error after navigating away", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    let delayedAnalyticsReject = null;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics") {
        await new Promise((r) => { delayedAnalyticsReject = r; });
        return route.fulfill({ status: 500, body: "delayed analytics failure" });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    // Start loading analytics
    await page.goto("http://magpie.test/?view=usage");
    await page.locator("#openAnalytics").waitFor();
    await page.locator("#openAnalytics").click();
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // Immediately navigate away to Usage
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();

    // Now reject the delayed analytics response
    if (delayedAnalyticsReject) delayedAnalyticsReject();
    await page.waitForTimeout(200);

    // Global status banner must NOT contain error
    const statusMsg = await page.evaluate(() => document.querySelector("#status")?.textContent || "");
    assert(!statusMsg.includes("delayed analytics failure"), `error from departed analytics page must not leak to status: ${statusMsg}`);
  });

  test(engine + ": calls fetch failure shows visible error state instead of fake empty", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics/calls") {
        return route.fulfill({ status: 500, body: "upstream calls database timeout" });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // Trigger drilldown in All mode
    const ttftBtn = page.locator("button.an-kpi-tile").filter({ hasText: "End-to-End TTFT P95" });
    await ttftBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // Verify error state box is displayed with Retry button, not fake empty
    const errBox = page.locator(".an-drill-state-box.an-err");
    await errBox.waitFor();
    assert(await errBox.isVisible(), "visible error box must appear on fetch failure");
    assert(await page.locator(".an-drill-retry-btn").isVisible(), "retry button must be visible");
  });

  test(engine + ": empty data state, cost KPI distinction, and localization", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    await page.route("**/*", createServer(emptyAnalyticsData));

    t.after(async () => {
      await browser.close();
    });

    // Direct ?view=analytics URL navigation
    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();
    await page.locator(".an-kpi-val").first().waitFor();

    // Verify zero data renders '—' for rates and cost
    const kpiVal = await page.locator(".an-kpi-val").first().textContent();
    assert.equal(kpiVal.trim(), "—", "empty success rate should display '—'");

    const costTile = page.locator(".an-kpi-tile").filter({ hasText: "Total Cost" });
    const costVal = await costTile.locator(".an-kpi-val").textContent();
    assert.equal(costVal.trim(), "—", "empty cost KPI should display '—'");

    // Drill into cost in empty period: left panel must also render '—', not '$0.00'
    await costTile.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    const leftCostVal = await page.locator(".an-drill-left-kpi-val").textContent();
    assert.equal(leftCostVal.trim(), "—", "empty period in drill left card must display '—' instead of $0.00");
    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // While in analytics (English), switch to another view so the page becomes hidden.
    await page.evaluate(() => window.show("routing"));
    await page.locator("#view-routing:not([hidden])").waitFor();
    assert(await page.locator("#view-analytics").isHidden(), "analytics page must be hidden");

    // Change the locale while analytics is hidden.
    await page.evaluate(() => window.setLocale("zh"));
    await page.waitForTimeout(100);

    // Return with restore = true (the view is shown again without re-fetching).
    await page.evaluate(() => window.show("analytics", true));
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // Line 1303: load(p, restore=true) returns early without re-fetching,
    // but view must properly display updated locale (e.g. 可靠性)
    const titleZhAfterReturn = await page.locator(".an-theme-title").first().textContent();
    assert.equal(titleZhAfterReturn.trim(), "可靠性", "analytics view restored with restore=true must render in the newly set locale");
  });

  test(engine + ": mutually exclusive filters, real picker selection, and stale response mitigation", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    const requestedParams = [];
    let slowResponseResolve = null;

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics") {
        requestedParams.push(url.search);
        // Stale test scenario: create a delayed response for first request
        if (url.searchParams.get("provider") === "openai") {
          await new Promise((r) => { slowResponseResolve = r; });
          return route.fulfill({
            json: {
              ...mockAnalyticsData,
              summary: { ...mockAnalyticsData.summary, calls: 999999 }, // Stale payload
            },
          });
        }
        return route.fulfill({ json: mockAnalyticsData });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // 1. In All mode: 3 filter dropdowns available
    const filterBtns = page.locator(".an-filter-btn");
    assert.equal(await filterBtns.count(), 3, "All mode must have 3 filter dropdown buttons");

    // 2. Real selection via proto-menu popover
    const providerBtn = filterBtns.nth(1); // Provider filter
    await providerBtn.click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();

    // Second click on the same filter button closes the menu
    await providerBtn.click();
    assert.equal(await page.locator(".proto-menu").count(), 0, "a second click closes filter menu");

    // Re-open and select "openai" from proto-menu list
    await providerBtn.click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    const openaiOpt = page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "openai" });
    await openaiOpt.click();
    assert.equal(await page.locator(".proto-menu").count(), 0, "picking an option closes the menu");
    // 3. Immediately switch dimension to "By Provider" to trigger a fast second request (stale race)
    const providerDimBtn = page.locator("#anDim button").filter({ hasText: "By Provider" });
    await providerDimBtn.click();

    // Now resolve the older slow response
    if (slowResponseResolve) slowResponseResolve();
    await page.waitForTimeout(300);

    // Verify latest response won: total calls should NOT be the stale 999999
    const callsSub = await page.locator(".an-kpi-sub").first().textContent();
    assert(!callsSub.includes("999999"), `stale response must be discarded by loadSeq: ${callsSub}`);

    // Verify mutually exclusive rule: By Provider clears Provider filter, retains other filters
    assert(requestedParams.some((q) => q.includes("provider=openai")), "first request must have included provider filter");
    assert(requestedParams.some((q) => !q.includes("provider=")), "switching to By Provider must clear provider filter");

    // 4. Hide accounts on: the drill page's shared mask button turns masking on for every view.
    await openDrill(page);
    await page.locator("#anDrillMask").click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "true");
    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // Switch to By Model so Provider filter is visible in controls
    await page.locator("#anDim button").filter({ hasText: "By Model" }).click();
    await page.locator('.an-filter-wrap[data-filter-dim="provider"]').waitFor();

    const filterWrapProv = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    await filterWrapProv.locator(".an-filter-btn").click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();

    const popListText = await page.locator(".proto-menu.an-filter-menu").innerText();
    assert(!popListText.includes("pick.owner@corp.example"), "filter picker options must not display raw email when accounts are hidden");
    assert(popListText.includes("@"), "filter picker option should show masked email stand-in");

    // Click the masked email option (contains @)
    const emailOpt = page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "@" }).first();
    await emailOpt.click();
    await page.waitForTimeout(100);

    const lastFilterReq = requestedParams[requestedParams.length - 1];
    assert(lastFilterReq.includes("provider=pick.owner%40corp.example") || lastFilterReq.includes("provider=pick.owner@corp.example"),
      `selecting masked option must still request raw value, got: ${lastFilterReq}`);

    // Turn the mask off again from the drill page's button.
    await openDrill(page);
    await page.locator("#anDrillMask").click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "false");
    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // 5. Dashboard request failure after successful load: roll back period and filters to shown query
    let rejectAnalytics = false;
    let shownPeriod = null;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics") {
        if (rejectAnalytics) {
          return route.fulfill({ status: 500, body: "analytics update failed" });
        }
        shownPeriod = url.searchParams.get("period");
        return route.fulfill({ json: mockAnalyticsData });
      }
      return createServer()(route);
    });
    // one successful load through this route (a dimension switch need not
    // fetch again), so the shown period is known
    const loaded = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/analytics" && r.ok());
    const onLabel = (await page.locator("#anPeriod button.opt.on").textContent()).trim();
    await page.locator("#anPeriod button").filter({ hasText: onLabel === "30 days" ? "Today" : "30 days" }).click();
    await loaded;
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));
    assert(shownPeriod && shownPeriod !== "7d", `shown period must be known and not 7d, got ${shownPeriod}`);
    const shownLabel = (await page.locator("#anPeriod button.opt.on").textContent()).trim();

    // Fail the next request when changing period to 7d
    rejectAnalytics = true;
    await page.locator("#anPeriod button").filter({ hasText: "7 days" }).click();
    await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));

    // Controls must have rolled back to the shown period
    const activePeriodBtn = await page.locator("#anPeriod button.opt.on").textContent();
    assert.equal(activePeriodBtn.trim(), shownLabel, "period segment must revert to previously shown period on fetch failure");

    // Subsequent drilldown must request the shown period, not the failed 7d
    let lastDrillCallsUrl = null;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics/calls") {
        lastDrillCallsUrl = url.toString();
        return route.fulfill({ json: mockCallsData });
      }
      return route.fallback();
    });

    // the dimension here is By Model: drill from a ranking row
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    assert(lastDrillCallsUrl, "drill calls request must be made");
    assert.equal(new URL(lastDrillCallsUrl).searchParams.get("period"), shownPeriod, "drill calls must use the rolled-back shown period, not the failed 7d");

    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // 6. Dimension change with filter clear triggers fetch; failure rolls back dimension & filters
    // Start in All dimension with provider=openai set
    rejectAnalytics = false;
    await page.locator("#anDim button").filter({ hasText: "All" }).click();
    // Set provider filter to openai
    const provFilterWrap = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    await provFilterWrap.locator(".an-filter-btn").click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    const provOption = page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "openai" });
    const provLoaded = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/analytics" && r.ok());
    await provOption.click();
    await provLoaded;
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "All");
    assert(await provFilterWrap.locator(".an-filter-clear").isVisible(), "provider clear button should be visible");

    // Now switch to By Provider while rejecting requests: clearing provider requires fetch, which fails
    rejectAnalytics = true;
    await page.locator("#anDim button").filter({ hasText: "By Provider" }).click();
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));

    // Dimension must roll back to All, provider filter restored and visible, and clearing works
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "All", "dimension must revert to All on failure");
    const rolledBackProvWrap = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    assert(await rolledBackProvWrap.isVisible(), "provider filter control must remain visible after rollback");
    const rolledBackClear = rolledBackProvWrap.locator(".an-filter-clear");
    assert(await rolledBackClear.isVisible(), "provider filter clear button must remain visible after rollback");

    // Clearing filter works when requests succeed
    rejectAnalytics = false;
    await rolledBackProvWrap.hover();
    await page.waitForFunction(() => getComputedStyle(document.querySelector('.an-filter-wrap[data-filter-dim="provider"] .an-filter-clear')).opacity === "1");
    const clearLoaded = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/analytics" && r.ok());
    await rolledBackClear.click();
    await clearLoaded;
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));
    assert.equal(await rolledBackProvWrap.locator(".an-filter-clear").count(), 0, "provider filter should be cleared");

    // 7. Dimension switch without fetch then failed switch: avoids stale shown.dim
    // Set provider in All first with successful fetch
    const provFilterWrapAll = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    await provFilterWrapAll.locator(".an-filter-btn").click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    const provOptAll = page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "openai" });
    const provLoadedAll = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/analytics" && r.ok());
    await provOptAll.click();
    await provLoadedAll;
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "All");

    // Switch to By Model without fetch (no model filter active)
    await page.locator("#anDim button").filter({ hasText: "By Model" }).click();
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "By Model");

    // Now try switching to By Provider with failure (clears provider filter -> fetch fails)
    rejectAnalytics = true;
    await page.locator("#anDim button").filter({ hasText: "By Provider" }).click();
    await page.waitForFunction(() => !document.querySelector("#view-analytics").classList.contains("loading"));

    // Dimension must roll back to By Model (proving the no-fetch switch updated shown.dim from All to By Model)
    assert.equal((await page.locator("#anDim button.opt.on").textContent()).trim(), "By Model", "dimension must revert to By Model, not stale All");
    rejectAnalytics = false;
  });

  test(engine + ": All mode KPI drilldown opens independent page, closes cleanly, and survives clicks", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    let callsRequests = 0;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics/calls") {
        callsRequests++;
        return route.fulfill({ json: mockCallsData });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // In All mode: click TTFT P95 KPI button
    const ttftKpiBtn = page.locator("button.an-kpi-tile").filter({ hasText: "End-to-End TTFT P95" });
    await ttftKpiBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    assert.equal(callsRequests, 1, "drilldown should trigger exactly one calls request");

    // Verify drill page is shown and main dashboard hidden
    assert(await page.locator("#anDrillPage").isVisible(), "drill page should be open");
    assert(await page.locator("#anBody").isHidden(), "dashboard body should be hidden");

    // Left panel shows overall KPI card in All mode
    const leftKpi = page.locator(".an-drill-left-kpi");
    assert(await leftKpi.isVisible(), "overall KPI card must be visible in left panel");

    // Clicking back button cleanly returns to dashboard
    const backBtn = page.locator("#anBack");
    await backBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();

    assert(await page.locator("#anBody").isVisible(), "dashboard body must be visible again");
    assert(await page.locator("#anDrillPage").isHidden(), "drill page must be hidden");
    assert.equal(callsRequests, 1, "no extra calls requests triggered by clicking back");
  });

  test(engine + ": drilldown loading spinner, aria-busy, dedup, delayed empty/error cleanup and stale completion", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({
      viewport: { width: 1100, height: 750 },
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    let callsRequestsCount = 0;
    let slowCallsResolve;
    let signalSlowCallsStarted;
    let slowCallsStarted = new Promise((resolve) => { signalSlowCallsStarted = resolve; });
    let delayCalls = true;
    let slowEmptyResolve;
    let signalSlowEmptyStarted;
    let slowErrorResolve;
    let signalSlowErrorStarted;

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics/calls") {
        callsRequestsCount++;
        const chartId = url.searchParams.get("chart_id");
        if (delayCalls && chartId === "ttft") {
          signalSlowCallsStarted();
          await new Promise((resolve) => { slowCallsResolve = resolve; });
          return route.fulfill({
            json: {
              calls: [{ ...mockCallsData.calls[0], agent: "slow-stale-call-agent" }],
            },
          });
        }
        if (chartId === "error_rate") {
          return route.fulfill({
            json: {
              calls: [{ ...mockCallsData.calls[1], agent: "fast-active-call-agent" }],
            },
          });
        }
        if (chartId === "cost") {
          if (signalSlowEmptyStarted) signalSlowEmptyStarted();
          await new Promise((resolve) => { slowEmptyResolve = resolve; });
          return route.fulfill({ json: { calls: [] } });
        }
        if (chartId === "cache_hit_rate") {
          if (signalSlowErrorStarted) signalSlowErrorStarted();
          await new Promise((resolve) => { slowErrorResolve = resolve; });
          return route.fulfill({ status: 500, body: "delayed upstream failure" });
        }
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // 1. Click TTFT P95 KPI button (chart ttft) -> slow response
    const ttftBtn = page.locator("button.an-kpi-tile[data-chart-id='ttft']");
    await ttftBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    await slowCallsStarted;

    // Verify visible spinner, aria-hidden on spinner, role=status on loadingBox, and aria-busy on #anDrillRight
    const rightPane = page.locator("#anDrillRight");
    assert.equal(await rightPane.getAttribute("aria-busy"), "true", "right pane must have aria-busy='true' while loading");

    const loadingBox = page.locator(".an-drill-state-box[role='status']");
    assert(await loadingBox.isVisible(), "loading box with role='status' must be visible while loading");

    const spinner = loadingBox.locator(".an-spinner");
    assert(await spinner.isVisible(), "visible spinner must appear inside loading state box");
    assert.equal(await spinner.getAttribute("aria-hidden"), "true", "spinner must be aria-hidden decorative");

    // Reduced motion verification: under reduced-motion: reduce, spinner animation is none
    const spinnerAnim = await spinner.evaluate((el) => window.getComputedStyle(el).animationName);
    assert.equal(spinnerAnim, "none", "spinner animation must be none under reduced motion");

    // Verify no old call rows and no empty message before response arrives
    assert.equal(await page.locator(".an-call-item").count(), 0, "no call items should be displayed during pending load");
    assert.equal(await page.locator(".an-drill-state-box:not([role='status'])").count(), 0, "no empty or error box before response");

    // 2. Click back button remains usable during loading
    const backBtn = page.locator("#anBack");
    assert(await backBtn.isVisible(), "back button must remain visible and usable during loading");

    // 3. Same period+filter+chart pending key dedup: clicking the same pending chart KPI or retry must NOT trigger extra request
    const reqCountBeforeDup = callsRequestsCount;
    // Assert tile exists before click
    const tileExists = await page.evaluate(() => Boolean(document.querySelector("button.an-kpi-tile[data-chart-id='ttft']")));
    assert(tileExists, "chart tile ttft must exist in DOM");
    await page.evaluate(() => {
      document.querySelector("button.an-kpi-tile[data-chart-id='ttft']").click();
    });
    await page.waitForTimeout(100);
    assert.equal(callsRequestsCount, reqCountBeforeDup, "duplicate pending request with same parameters must be deduped");

    // 4. Delayed A/B stale completion:
    // Return to dashboard and click Error Rate (chart 1.1, fast B)
    await backBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();
    const errRateBtn = page.locator("button.an-single-card[data-chart-id='error_rate']");
    await errRateBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // Wait for fast chart B (1.1) to complete and render
    await page.locator(".an-call-item").first().waitFor();
    assert.equal(await rightPane.getAttribute("aria-busy"), null, "aria-busy must be cleared after successful load");
    assert.equal(await page.locator(".an-call-item-agent").first().textContent(), "fast-active-call-agent");

    // Now resolve slow chart A (2.1)
    slowCallsResolve();
    await page.waitForTimeout(150);

    // Stale slow A response must NOT overwrite active B response
    assert.equal(await page.locator(".an-call-item-agent").first().textContent(), "fast-active-call-agent", "stale delayed A response must not overwrite active B calls list");
    assert.equal(await rightPane.getAttribute("aria-busy"), null, "aria-busy must remain cleared");

    // 5. Delayed Empty: test that pending spinner clears and empty state renders correctly after response
    await backBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();

    const emptyStartedPromise = new Promise((resolve) => { signalSlowEmptyStarted = resolve; });
    const costBtn = page.locator("button.an-kpi-tile").filter({ hasText: "Total Cost" });
    await costBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    await emptyStartedPromise;

    assert.equal(await rightPane.getAttribute("aria-busy"), "true", "busy while empty request is pending");
    assert(await page.locator(".an-spinner").isVisible(), "spinner must be visible during pending empty request");

    // Resolve empty response
    slowEmptyResolve();
    await page.waitForFunction(() => !document.querySelector("#anDrillRight")?.getAttribute("aria-busy"));
    assert.equal(await page.locator(".an-spinner").count(), 0, "spinner must be removed once empty response resolves");
    assert.equal(await rightPane.getAttribute("aria-busy"), null, "aria-busy cleared on empty");
    assert.match(await page.locator(".an-drill-state-box").textContent(), /No matching calls found/, "empty state shown");

    // 6. Delayed Error: test that pending spinner clears and error state renders correctly after response
    await backBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();

    const errorStartedPromise = new Promise((resolve) => { signalSlowErrorStarted = resolve; });
    const cacheBtn = page.locator("button.an-kpi-tile").filter({ hasText: "Cache Hit Rate" });
    await cacheBtn.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    await errorStartedPromise;

    assert.equal(await rightPane.getAttribute("aria-busy"), "true", "busy while error request is pending");
    assert(await page.locator(".an-spinner").isVisible(), "spinner must be visible during pending error request");

    // Resolve error response
    slowErrorResolve();
    await page.waitForFunction(() => !document.querySelector("#anDrillRight")?.getAttribute("aria-busy"));
    assert.equal(await page.locator(".an-spinner").count(), 0, "spinner must be removed once error response resolves");
    assert.equal(await rightPane.getAttribute("aria-busy"), null, "aria-busy cleared on error");
    assert(await page.locator(".an-drill-state-box.an-err").isVisible(), "error state displayed with retry button");
    assert(await page.locator(".an-drill-retry-btn").isVisible(), "manual retry button preserved");
  });

  test(engine + ": bar chart geometry, tail width stability, filter clear button, and accessible tooltips", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    const requestedParams = [];
    let currentAnalyticsData = mockAnalyticsData;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics") {
        requestedParams.push(url.search);
        return route.fulfill({ json: currentAnalyticsData });
      }
      return createServer()(route);
    });

    t.after(async () => {
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=analytics");
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // 1. Check Usage entry button & i18n
    await page.evaluate(() => window.setLocale("zh"));
    await page.waitForTimeout(100);
    const usageBtnZh = await page.locator("#openAnalytics span[data-t]").textContent();
    assert.equal(usageBtnZh.trim(), "分析", `Usage button should be translated to '分析', got '${usageBtnZh}'`);

    // Switch back to en for metric assertions
    await page.evaluate(() => window.setLocale("en"));
    await page.waitForTimeout(100);

    // 2. Check TPS labels in All mode (KPI tile)
    const tpsKpiLabel = await page.locator(".an-kpi-label").filter({ hasText: "TPS" }).first().textContent();
    assert.equal(tpsKpiLabel.trim(), "TPS", "speed KPI label in All mode must be TPS");

    // 3. Switch to By Model to verify bar chart title and track geometry
    const modelDimBtn = page.locator("#anDim button").filter({ hasText: "By Model" });
    await modelDimBtn.click();
    await page.locator(".an-bar-row").first().waitFor();

    const tpsCardTitle = await page.locator(".an-chart-title").filter({ hasText: "TPS" }).first().textContent();
    assert.equal(tpsCardTitle.trim(), "TPS", "speed chart title in By Model mode must be TPS");

    // 4. Verify bar track geometry and filled segment monotonicity with 94% vs 95% and short/long tails
    const geometryData = {
      ...mockAnalyticsData,
      rankings: {
        ...mockAnalyticsData.rankings,
        model: {
          summaries: {
            "model-short-94": {
              calls: 100,
              errors: 90,
              rate_limited: 90,
              server_err: 0,
              other_err: 0,
              canceled: 4,
              error_rate: 0.94,
              timed: 50,
              ttft_p50: 800,
              ttft_p95: 1500,
              decode_calls: 50,
              speed: 40.0,
              cost: 1.0,
              unpriced: 0,
            },
            "model-very-long-name-identifier-95": {
              calls: 100,
              errors: 95,
              rate_limited: 95,
              server_err: 0,
              other_err: 0,
              canceled: 0,
              error_rate: 0.95,
              timed: 50,
              ttft_p50: 800,
              ttft_p95: 1500,
              decode_calls: 50,
              speed: 40.0,
              cost: 1.0,
              unpriced: 0,
            },
          },
          by_error_rate: [
            {
              key: "model-short-94",
              metric_val: 0.94,
              insufficient: false,
              share: 0.5,
            },
            {
              key: "model-very-long-name-identifier-95",
              metric_val: 0.95,
              insufficient: false,
              share: 0.5,
            },
          ],
        },
      },
    };
    // Update mutable analytics fixture to geometryData and reload
    currentAnalyticsData = geometryData;
    await page.evaluate(() => window.loadAnalytics("30d"));
    await page.locator(".an-bar-row").first().waitFor();

    const trackGeometries = await page.evaluate(() => {
      const chart = document.querySelector(".an-chart-card .an-bars");
      if (!chart) return [];
      const rows = [...chart.querySelectorAll(".an-bar-row")];
      return rows.map((r) => {
        const track = r.querySelector(".an-bar-track");
        const seg = r.querySelector(".an-bar-seg");
        const num = r.querySelector(".an-bar-num");
        const rTrack = track.getBoundingClientRect();
        const rSeg = seg.getBoundingClientRect();
        return {
          left: Math.round(rTrack.left),
          width: Math.round(rTrack.width),
          segWidth: rSeg.width,
          numText: num.textContent.trim(),
        };
      });
    });

    assert.equal(trackGeometries.length, 2, "must render 2 rows for geometry test");
    // Both rows must share identical track start position and track width
    assert.equal(trackGeometries[0].left, trackGeometries[1].left, "track left start must be identical regardless of label length");
    assert.equal(trackGeometries[0].width, trackGeometries[1].width, "track width must be identical regardless of tail text");

    // Monotonicity assertion: 95% filled segment width must be strictly greater than 94%
    assert(trackGeometries[1].segWidth > trackGeometries[0].segWidth,
      `95% filled segment width (${trackGeometries[1].segWidth}px) must be greater than 94% (${trackGeometries[0].segWidth}px)`);
    // 4. Verify tooltips on bar row and focus accessibility (metrics resolved from summaries)
    const firstBar = page.locator(".an-bar-row").first();
    const tooltipTitle = await firstBar.getAttribute("title");
    const ariaLabel = await firstBar.getAttribute("aria-label");
    // Check statistical content & cancel conservation rather than pinned wording
    assert(tooltipTitle, "bar row must have tooltip");
    assert(ariaLabel, "bar row must have accessible aria-label");
    const matchCalls = tooltipTitle.match(/: (\d+)/);
    assert(matchCalls, `tooltip must contain call count: ${tooltipTitle}`);
    const totalCalls = parseInt(matchCalls[1], 10);
    assert.equal(totalCalls, 100, "resolved call count should match summary");
    const matchDetail = tooltipTitle.match(/\(([^)]+)\)/);
    assert(matchDetail, `tooltip must contain breakdown details: ${tooltipTitle}`);
    const parts = matchDetail[1].split(",").map((s) => s.trim());
    const nums = parts.map((p) => {
      const m = p.match(/:\s*(\d+)/);
      return m ? parseInt(m[1], 10) : NaN;
    });
    assert.equal(nums.length, 3, `breakdown must contain exactly 3 components (success, errors, canceled): ${matchDetail[1]}`);
    const [success, errs, canceled] = nums;
    assert(!isNaN(success) && !isNaN(errs) && !isNaN(canceled), "breakdown counts must be valid integers");
    assert(canceled > 0, `canceled count must be > 0 to verify non-trivial conservation, got ${canceled}`);
    assert.equal(success + errs + canceled, totalCalls, `conservation: success (${success}) + errors (${errs}) + canceled (${canceled}) must equal total calls (${totalCalls})`);
    assert(tooltipTitle.includes("94.0%"), `bar row tooltip must contain error rate: ${tooltipTitle}`);
    assert(ariaLabel.includes("94.0%"), `aria-label must contain error rate: ${ariaLabel}`);
    // 5. Test filter clear button: select provider then click clear x
    const providerWrap = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    const providerBtn = providerWrap.locator(".an-filter-btn");
    await providerBtn.click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    await page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: "openai" }).click();
    await page.waitForTimeout(100);

    // Verify clear button exists in DOM
    const clearBtn = providerWrap.locator(".an-filter-clear");
    assert((await clearBtn.count()) > 0, "clear button must exist on filter with value");

    // Before hover: opacity must be 0 and pointer-events none (no layout shift)
    const opacityBeforeHover = await clearBtn.evaluate((el) => window.getComputedStyle(el).opacity);
    assert.equal(opacityBeforeHover, "0", "clear button should be transparent before hover");
    const wrapWidthBeforeHover = await providerWrap.evaluate((el) => el.getBoundingClientRect().width);

    // Hover wrapper: clear button opacity becomes 1 without layout shift (using waitForFunction)
    await providerWrap.hover();
    await page.waitForFunction((el) => window.getComputedStyle(el).opacity === "1", await clearBtn.elementHandle());
    const wrapWidthAfterHover = await providerWrap.evaluate((el) => el.getBoundingClientRect().width);
    assert.equal(wrapWidthAfterHover, wrapWidthBeforeHover, "hovering filter wrap must not cause layout shift");

    // Click clear button
    const reqsBeforeClear = requestedParams.length;
    await clearBtn.click();
    await page.waitForTimeout(200);

    // Verify filter cleared and triggered fresh fetch
    assert(requestedParams.length > reqsBeforeClear, "clicking clear button must trigger fresh fetch");
    const lastReq = requestedParams[requestedParams.length - 1];
    assert(!lastReq.includes("provider="), `cleared provider should not be in request params: ${lastReq}`);

    // 6. 150-character model filter header constraint: wrapper max-width <= 180px, no 659px horizontal overflow
    // Set viewport to 659px desktop width constraint
    await page.setViewportSize({ width: 659, height: 750 });
    await page.waitForTimeout(50);

    const longModelName = "enterprise-finetuned-deepseek-v3-multimodal-chain-of-thought-reasoning-system-ultra-high-throughput-preview-release-2026-distributed-cluster-region-us-east-1-corp";
    const longFilterData = {
      ...mockAnalyticsData,
      filters: {
        ...mockAnalyticsData.filters,
        model: [longModelName],
      },
    };
    currentAnalyticsData = longFilterData;
    await page.evaluate(() => window.loadAnalytics("30d"));
    // Switch to By Provider mode so Model filter is visible
    const provDimBtn = page.locator("#anDim button").filter({ hasText: "By Provider" });
    await provDimBtn.click();

    const modelWrap = page.locator('.an-filter-wrap[data-filter-dim="model"]');
    await modelWrap.waitFor();
    const modelBtn = modelWrap.locator(".an-filter-btn");
    await modelBtn.click();
    await page.locator(".proto-menu.an-filter-menu:not([hidden])").waitFor();
    await page.locator(".proto-menu.an-filter-menu .pm-item").filter({ hasText: longModelName }).click();
    await page.waitForTimeout(100);

    const modelWrapWidth = await modelWrap.evaluate((el) => el.getBoundingClientRect().width);
    assert(modelWrapWidth <= 180.5, `long filter wrap width should be bounded <= 180px, got ${modelWrapWidth}`);

    // Verify view has no horizontal scroll at 659px with long filter selected
    const viewHorizScroll = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth > v.clientWidth;
    });
    assert(!viewHorizScroll, "150-char model filter must not cause horizontal scroll at 659px");

    // 7. Error trend column tooltip contains full formatted date/time (Intl)
    const trendCol = page.locator(".an-trend-col").first();
    const trendTitle = await trendCol.getAttribute("title");
    const trendAria = await trendCol.getAttribute("aria-label");
    assert(trendTitle && (trendTitle.includes("2026") || trendTitle.includes("09")), `trend tooltip must include full datetime: ${trendTitle}`);
    assert.equal(trendAria, trendTitle, "trend col aria-label must match title for screen readers");

    // 8. Mask emails: both title and aria-label attributes must be masked
    // Add real element with email inside #view-analytics without page reloads
    await page.evaluate(() => {
      const testEl = document.createElement("button");
      testEl.id = "testMaskAttr";
      testEl.title = "User user@test.org completed task";
      testEl.setAttribute("aria-label", "User user@test.org completed task");
      document.querySelector("#view-analytics").append(testEl);
    });

    // Toggle masking on in place from the drill page's shared button (no view switch).
    await openDrill(page);
    await page.locator("#anDrillMask").click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "true");
    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // Wait for MutationObserver to apply mask to test element
    await page.waitForFunction(() => {
      const el = document.querySelector("#testMaskAttr");
      return el && el.title && !el.title.includes("test.org") && el.getAttribute("aria-label") && !el.getAttribute("aria-label").includes("test.org");
    });

    const maskedAttrs = await page.evaluate(() => {
      const el = document.querySelector("#testMaskAttr");
      if (!el) return null;
      return {
        title: el.getAttribute("title"),
        ariaLabel: el.getAttribute("aria-label"),
        rawTitle: el.dataset.piiTitle,
        rawAria: el.dataset.piiAriaLabel,
      };
    });

    assert(maskedAttrs && !maskedAttrs.title.includes("test.org"), `title must be masked: ${maskedAttrs?.title}`);
    assert(maskedAttrs && !maskedAttrs.ariaLabel.includes("test.org"), `aria-label must be masked: ${maskedAttrs?.ariaLabel}`);
    assert(maskedAttrs.title.includes("@") && maskedAttrs.ariaLabel.includes("@"), "masked email should preserve @ structure");

    // Unmask from the drill page's button.
    await openDrill(page);
    await page.locator("#anDrillMask").click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "false");
    await page.locator("#anBack").click();
    await page.locator("#anBody:not([hidden])").waitFor();
  });

  test(engine + ": Analytics and Usage share the existing account mask preference", async (t) => {
    const browser = await launchBrowser(engine);
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    await context.route("**/*", (route) => {
      if (new URL(route.request().url()).pathname === "/api/usage/quotas") {
        return route.fulfill({ json: [{ provider: "openai", name: "OpenAI", user: "pick.owner@corp.example", windows: [] }] });
      }
      return createServer()(route);
    });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);
    await page.goto("http://magpie.test/?view=analytics");
    await openDrill(page);
    await page.locator("#anDrillMask").click();
    assert.equal(await page.locator("#usageMask").getAttribute("aria-pressed"), "true", "Analytics immediately updates Usage's toggle");
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), "1");

    // The existing choice is restored on reload and inherited by new same-origin windows.
    await page.reload();
    await openDrill(page);
    assert.equal(await page.locator("#anDrillMask").getAttribute("aria-pressed"), "true");
    await page.locator(".an-call-item").first().click();
    await page.locator("#anDrillRight .pii").first().waitFor();
    const other = await context.newPage();
    await other.goto("http://magpie.test/?view=usage");
    await other.locator("#usageMask").waitFor();
    assert.equal(await other.locator("#usageMask").getAttribute("aria-pressed"), "true");

    // Usage turns it off; the already mounted Analytics route follows the storage event.
    await other.locator("#usageMask").click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "false");
    assert.equal(await page.locator("#anDrillRight .pii").count(), 0, "Usage reveals the existing Analytics story too");
    await page.locator("#rtBackAnalytics").click();
    await page.locator(".an-calls-list").waitFor();
    await page.evaluate(() => window.show("usage"));
    await page.locator("#usageMask").click();
    assert.equal(await page.locator("#anDrillMask").getAttribute("aria-pressed"), "true", "Usage immediately updates Analytics's toggle");
    await context.close();
  });
}

