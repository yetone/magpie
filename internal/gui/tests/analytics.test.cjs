// Run with Node's test runner and Playwright on the module path; see README.md.
// Tests Issue #213 Analytics UI behavior:
// 1. Navigation & initial load with period preservation from Usage, back navigation with scroll/tabs preserved
// 2. Mode switching and mutually exclusive filter behavior (actual selection & assertion of request params)
// 3. Stale response mitigation (disordered network responses)
// 4. Drilldown to independent page view (compact chart on left, 50 calls list on right)
// 5. Right pane in-place switch to 7-group detail view and return to calls list
// 6. Return from drilldown to dashboard preserving mode, filters, period and scroll position
// 7. 659px desktop width prioritizes two columns without horizontal overflow
// 8. Visible error state on calls fetch failure (no fake empty)
// 9. Zero data handling ('—' display, cost KPI empty/unknown states, no fake bars)
// 10. Locale switching (zh/en) covering notes, chips, tail notes, details, and Hide Emails masking
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
    ttft_p50: 850,
    ttft_p95: 1950,
    decode_calls: 80,
    speed: 52.4,
    cache_hit_rate: 0.3478,
  },
  rankings: {
    model: {
      by_error_rate: [
        {
          key: "gpt-5.5",
          metric_val: 0.08,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.6,
          calls: 60,
          rate_limited: 3,
          server_err: 1,
          other_err: 1,
          error_rate: 0.08,
          cost: 8.50,
        },
        {
          key: "claude-3-7-sonnet",
          metric_val: 0.03,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: true,
          share: 0.4,
          calls: 40,
          rate_limited: 0,
          server_err: 1,
          other_err: 0,
          error_rate: 0.025,
          cost: 6.00,
        },
        {
          key: "zero-err-model",
          metric_val: 0.0,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.2,
          calls: 20,
          rate_limited: 0,
          server_err: 0,
          other_err: 0,
          error_rate: 0.0,
          cost: 1.00,
        },
        {
          key: "tiny-model",
          metric_val: 0.0,
          insufficient: true,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.0,
          calls: 2,
          rate_limited: 0,
          server_err: 0,
          other_err: 0,
          error_rate: 0.0,
          cost: 0.0,
        },
      ],
      by_ttft: [
        {
          key: "gpt-5.5",
          metric_val: 1950,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.6,
          timed: 45,
          ttft_p50: 820,
          ttft_p95: 1950,
        },
        {
          key: "claude-3-7-sonnet",
          metric_val: 1200,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: true,
          share: 0.4,
          timed: 35,
          ttft_p50: 650,
          ttft_p95: 1200,
        },
      ],
      by_speed: [
        {
          key: "claude-3-7-sonnet",
          metric_val: 45.2,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.4,
          decode_calls: 35,
          speed: 45.2,
        },
        {
          key: "gpt-5.5",
          metric_val: 68.0,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.6,
          decode_calls: 40,
          speed: 68.0,
        },
      ],
      by_cost: [
        {
          key: "gpt-5.5",
          metric_val: 8.50,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.586,
          cost: 8.50,
        },
        {
          key: "claude-3-7-sonnet",
          metric_val: 6.00,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: true,
          share: 0.414,
          cost: 6.00,
        },
      ],
      by_cache_rate: [
        {
          key: "claude-3-7-sonnet",
          metric_val: 0.25,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.4,
          input: 60000,
          cache_read: 20000,
          cache_write: 5000,
          cache_hit_rate: 0.25,
        },
        {
          key: "gpt-5.5",
          metric_val: 0.40,
          insufficient: false,
          unknown_cache: false,
          has_unpriced: false,
          share: 0.6,
          input: 90000,
          cache_read: 60000,
          cache_write: 7000,
          cache_hit_rate: 0.40,
        },
        {
          key: "zero-cache-model",
          metric_val: null,
          insufficient: false,
          unknown_cache: true,
          has_unpriced: false,
          share: 0.0,
          input: 12000,
          cache_read: 0,
          cache_write: 0,
          cache_hit_rate: 0.0,
        },
      ],
    },
    provider: { by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
    agent: { by_error_rate: [], by_ttft: [], by_speed: [], by_cost: [], by_cache_rate: [] },
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
    return {
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
      ttft_ms: i === 2 ? 1950 : (1950 + i * 15), // i === 2 has interval 50ms < 100ms
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
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": analytics navigation, rendering, drilldown page, detail switch and return state", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
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

    // 2. Mode switching: All -> By Model
    const modelDimBtn = page.locator("#anDim button").filter({ hasText: "By Model" });
    await modelDimBtn.click();
    await page.locator(".an-bars").first().waitFor();

    const barRows = page.locator(".an-bar-row");
    assert((await barRows.count()) > 0, "must render bar rows in By Model mode");

    // Verify no folded sections rendered (unpriced/insufficient/unknown_cache hidden directly)
    const foldToggles = page.locator(".an-fold-toggle");
    assert.equal(await foldToggles.count(), 0, "should not render folded toggle sections");
    const foldFooters = page.locator(".an-fold-footer");
    assert.equal(await foldFooters.count(), 0, "should not render fold footers");
    // Verify insufficient items (e.g. tiny-model) are not in ranking bars
    const tinyModelBar = page.locator(".an-bar-row").filter({ hasText: "tiny-model" });
    assert.equal(await tinyModelBar.count(), 0, "insufficient item tiny-model should not be displayed in ranking");
    // 3. Click entity to navigate to independent drill-down page (not an in-place drawer)
    const firstBar = barRows.first();
    await firstBar.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();

    // Dashboard body and controls should be hidden, drill page visible
    assert(await page.locator("#anDrillPage").isVisible(), "independent drill page must be visible");
    assert(await page.locator("#anBody").isHidden(), "main dashboard body must be hidden");
    assert(await page.locator("#anControls").isHidden(), "main dashboard controls must be hidden");

    // Left compact chart & right calls list
    assert(await page.locator("#anDrillLeft").isVisible(), "left compact chart must be visible");
    assert(await page.locator("#anDrillRight").isVisible(), "right calls list pane must be visible");

    // Top 50 calls items rendered in right pane
    const callItems = page.locator(".an-call-item");
    await callItems.first().waitFor();
    assert((await callItems.count()) > 0, "calls items must be listed in right pane");
    // 4. Test real non-zero scrolling on 50 calls list:
    // Scroll down the view to non-zero scrollTop (e.g. 250px)
    await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      v.scrollTop = 250;
    });
    await page.waitForTimeout(50);
    const scrollBeforeDetail = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert(scrollBeforeDetail > 100, `view must have scrolled to non-zero, got ${scrollBeforeDetail}`);

    // Click 6th call item: opens detail view, and detail view automatically scrolls view to top (0)
    const sixthItem = callItems.nth(5);
    await sixthItem.click();
    await page.locator(".an-call-detail-box").waitFor();
    assert(await page.locator(".an-call-detail-box").isVisible(), "call detail card must replace right pane");
    assert(await page.locator(".an-calls-list").isHidden(), "calls list must be replaced by detail");
    assert(await page.locator("#anDrillLeft").isVisible(), "left compact chart must remain visible during detail view");

    await page.waitForTimeout(50);
    const scrollInDetail = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert.equal(scrollInDetail, 0, "detail view must scroll view to top (0) so top of detail is in viewport");

    // 5. Return back to Calls list: restores actual non-zero scroll position (before any reloads)
    const backToCallsBtn = page.locator(".an-drill-return-btn");
    await backToCallsBtn.click();
    await page.locator(".an-calls-list").waitFor();
    assert(await page.locator(".an-calls-list").isVisible(), "must return to calls list");
    assert(await page.locator(".an-call-detail-box").isHidden(), "detail box must be gone");
    await page.waitForFunction((expected) => Math.abs(document.querySelector("#view-analytics").scrollTop - expected) < 5, scrollBeforeDetail);
    const scrollRestored = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert(Math.abs(scrollRestored - scrollBeforeDetail) < 5, `return to calls list must restore non-zero scroll position (expected ~${scrollBeforeDetail}, got ${scrollRestored})`);

    // Verify mini chart: zero-err-model (error_rate: 0, calls: 20) renders seg width 0% while positive models have > 0%
    const miniRows = page.locator(".an-drill-mini-row");
    const zeroErrMiniRow = miniRows.filter({ hasText: "zero-err-model" });
    assert.equal(await zeroErrMiniRow.count(), 1, "zero-err-model must appear in mini chart rows");
    const zeroErrSegWidth = await zeroErrMiniRow.locator(".an-drill-mini-seg").evaluate((el) => el.style.width);
    assert.equal(zeroErrSegWidth, "0%", "entity with metric 0 must have mini chart segment width 0%");
    const positiveMiniRow = miniRows.filter({ hasText: "gpt-5.5" });
    const posSegWidth = await positiveMiniRow.locator(".an-drill-mini-seg").evaluate((el) => el.style.width);
    assert.notEqual(posSegWidth, "0%", "entity with positive metric must have mini chart segment width > 0%");

    // Entity switch resets callsScrollTop: scroll calls list, open detail, switch to another left entity,
    // verify view scrollTop is not restored to the old list position (stays at 0/top)
    await page.evaluate(() => {
      document.querySelector("#view-analytics").scrollTop = 220;
    });
    await page.waitForTimeout(50);
    const listScrollBeforeDetail2 = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert(listScrollBeforeDetail2 > 100, `list must be scrolled before detail, got ${listScrollBeforeDetail2}`);
    // Open detail
    await callItems.nth(3).click();
    await page.locator(".an-call-detail-box").waitFor();
    await page.waitForFunction(() => document.querySelector("#view-analytics").scrollTop === 0);
    // Click another entity in left panel while in detail
    await zeroErrMiniRow.click();
    await page.locator(".an-calls-list").waitFor();
    await page.waitForTimeout(100);
    const scrollAfterEntitySwitch = await page.evaluate(() => document.querySelector("#view-analytics").scrollTop);
    assert.equal(scrollAfterEntitySwitch, 0, "switching entity must reset callsScrollTop so list is at top (0), not restored to old position");

    // 5b. Verify HTTP 200 stream error badge and detail explanation
    const secondItem = callItems.nth(1);
    const badge2 = secondItem.locator(".an-st-badge");
    assert.equal(await badge2.textContent(), "200");
    assert(await badge2.evaluate((el) => el.classList.contains("st-5xx")), "HTTP 200 with stream error must have error badge class st-5xx");
    await secondItem.click();
    await page.locator(".an-call-detail-box").waitFor();
    await page.waitForFunction(() => document.querySelector("#view-analytics").scrollTop === 0);
    const explanationRow = page.locator(".an-detail-row", { hasText: "Explanation" });
    assert.equal((await explanationRow.locator("span").nth(1).textContent()).trim(), "synthetic stream failure", "detail explanation must show stream error");
    const tpsRow = page.locator(".an-detail-row", { hasText: "TPS" });
    assert.equal((await tpsRow.locator("span").nth(1).textContent()).trim(), "—", "TPS must be blanked (—) for failed call with stream error");
    // Plain HTTP has no Clipboard API: exercise the existing command fallback.
    await page.route("**/api/copy", (route) => route.fulfill({ status: 503, body: "" }));
    await page.evaluate(() => {
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
      document.execCommand = (command) => command === "copy";
    });
    await page.locator(".an-sess-copy-btn").click();
    await page.waitForFunction(() => document.querySelector(".an-sess-copy-btn").classList.contains("done"));
    await page.evaluate(() => { document.execCommand = () => false; });
    await page.locator(".an-sess-copy-btn").click();
    await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
    assert(!(await page.locator("#status").textContent()).includes(mockCallsData.calls[1].session), "copy failure must not reveal the session ID");
    await page.locator(".an-drill-return-btn").click();
    await page.locator(".an-calls-list").waitFor();

    // Verify third item (i=2, short interval 50ms < 100ms) displays interval note
    const thirdItem = page.locator(".an-call-item").nth(2);
    await thirdItem.click();
    await page.locator(".an-call-detail-box").waitFor();
    const tpsShortRow = page.locator(".an-detail-row", { hasText: "TPS" });
    const tpsShortText = (await tpsShortRow.locator("span").nth(1).textContent()).trim();
    assert(tpsShortText.includes("100ms") || tpsShortText === "—", `TPS for short interval (<100ms) must show short interval note: ${tpsShortText}`);
    await page.locator(".an-drill-return-btn").click();
    await page.locator(".an-calls-list").waitFor();
    // 6. The drill page's own Hide accounts, at the header's right end, masks
    // an account after a call's host ("host as email") in the detail.
    await page.locator(".an-call-item").nth(5).click();
    await page.locator(".an-call-detail-box").waitFor();
    const maskBtn = page.locator("#anDrillMask");
    const headBox = await page.locator(".an-drill-page-head").boundingBox();
    const maskBox = await maskBtn.boundingBox();
    assert(Math.abs(headBox.x + headBox.width - (maskBox.x + maskBox.width)) < 2, "drill mask button must sit at the header row's right end");
    await maskBtn.click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "true");
    const hostRow = page.locator(".an-detail-row", { hasText: "api.openai.com as" });
    await hostRow.locator(".pii").waitFor();
    assert(!(await hostRow.innerText()).includes("corp.example"), "host row must not show the account email");
    assert.equal(await hostRow.locator(".pii").evaluate((e) => e.dataset.raw), "host.owner@corp.example");
    assert(!(await page.locator(".an-call-detail-box").innerText()).includes("test.org"), "session email must be masked too");

    // Same setting as Routing's: off again from the drill page
    await maskBtn.click();
    await page.waitForFunction(() => document.querySelector("#anDrillMask").getAttribute("aria-pressed") === "false");
    assert((await hostRow.innerText()).includes("host.owner@corp.example"), "unmasking restores the host account");

    // 7. Return to main dashboard: preserves mode, filters, period and scroll
    const backToDashBtn = page.locator(".an-drill-back");
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

  test(engine + ": 659px desktop width prioritizes two columns without horizontal overflow", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    // Test 659px desktop width constraint as specified in requirements
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

    // Open the detail card with long session and long host
    await page.locator(".an-call-item").first().click();
    await page.locator(".an-call-detail-box").waitFor();

    // Verify no horizontal overflow in detail view even with extremely long session / host strings
    const detailHorizScroll = await page.evaluate(() => {
      const v = document.querySelector("#view-analytics");
      return v.scrollWidth > v.clientWidth;
    });
    assert(!detailHorizScroll, "659px detail view with long session/host must not trigger horizontal scrollbar");
  });

  test(engine + ": out-of-order calls responses during entity switching and navigation cancellation", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await launchBrowser(engine);
    const context = await browser.newContext({ viewport: { width: 1100, height: 750 } });
    const page = await context.newPage();
    page.setDefaultTimeout(6000);

    let slowEntityCallsResolve = null;

    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/analytics/calls") {
        const modelParam = url.searchParams.get("model");
        if (modelParam === "gpt-5.5") {
          // Delay gpt-5.5 response
          await new Promise((r) => { slowEntityCallsResolve = r; });
          return route.fulfill({
            json: {
              calls: [{ ...mockCallsData.calls[0], agent: "stale-gpt-agent" }],
            },
          });
        }
        if (modelParam === "claude-3-7-sonnet") {
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

    // Rapidly switch entity on left panel to second entity
    const miniRows = page.locator(".an-drill-mini-row");
    await miniRows.nth(1).click();
    await page.waitForTimeout(100);

    // Now resolve the older slow response for gpt-5.5
    if (slowEntityCallsResolve) slowEntityCallsResolve();
    await page.waitForTimeout(200);

    // Verify stale response was discarded: right panel should show fast-correct-agent
    const displayedAgent = await page.locator(".an-call-item-agent").first().textContent();
    assert.equal(displayedAgent, "fast-correct-agent", "stale out-of-order calls response must be discarded by drillSeq");

    // Now navigate away from Analytics (switch to Usage view)
    await page.evaluate(() => window.show("usage"));
    await page.locator("#view-usage:not([hidden])").waitFor();

    // Verify analytics view is hidden and drill was cleanly invalidated
    assert(await page.locator("#view-analytics").isHidden(), "analytics view should be hidden");
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

    // Verify zero data renders '—' for rates and cost (Issue review item #6)
    const kpiVal = await page.locator(".an-kpi-val").first().textContent();
    assert.equal(kpiVal.trim(), "—", "empty success rate should display '—'");

    const costTile = page.locator(".an-kpi-tile").filter({ hasText: "Total Cost" });
    const costVal = await costTile.locator(".an-kpi-val").textContent();
    assert.equal(costVal.trim(), "—", "empty cost KPI should display '—'");

    // Drill into 3.1 in empty period: left panel must also render '—', not '$0.00'
    await costTile.click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    const leftCostVal = await page.locator(".an-drill-left-kpi-val").textContent();
    assert.equal(leftCostVal.trim(), "—", "empty period in drill left card must display '—' instead of $0.00");
    await page.locator(".an-drill-back").click();
    await page.locator("#anBody:not([hidden])").waitFor();

    // Switch to Chinese locale
    await page.evaluate(() => window.setLocale("zh"));
    await page.waitForTimeout(200);

    const titleZh = await page.locator(".an-theme-title").first().textContent();
    assert.equal(titleZh.trim(), "可靠性", `theme title must be translated to Chinese: ${titleZh}`);
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

    // 2. Real selection via openFilterPicker / popover (Issue review item #1)
    const providerBtn = filterBtns.nth(1); // Provider filter
    await providerBtn.click();
    await page.locator("#pop:not([hidden])").waitFor();

    // Select "openai" from picker list
    const openaiOpt = page.locator("#pop #list li").filter({ hasText: "openai" });
    await openaiOpt.click();

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

    // 4. Hide accounts on: filter picker with email shows masked label in #pop, but selection requests raw value
    await page.evaluate(() => window.show("routing"));
    await page.locator("#rtMask").waitFor();
    await page.locator("#rtMask").click();
    await page.evaluate(() => window.show("analytics"));
    await page.locator("#view-analytics:not([hidden])").waitFor();

    // Switch to By Model so Provider filter is visible in controls
    await page.locator("#anDim button").filter({ hasText: "By Model" }).click();
    await page.locator('.an-filter-wrap[data-filter-dim="provider"]').waitFor();

    const filterWrapProv = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    await filterWrapProv.locator(".an-filter-btn").click();
    await page.locator("#pop:not([hidden])").waitFor();

    const popListText = await page.locator("#pop #list").innerText();
    assert(!popListText.includes("pick.owner@corp.example"), "filter picker options must not display raw email when accounts are hidden");
    assert(popListText.includes("@"), "filter picker option should show masked email stand-in");

    // Click the masked email option (contains @)
    const emailOpt = page.locator("#pop #list li").filter({ hasText: "@" }).first();
    await emailOpt.click();
    await page.waitForTimeout(100);

    const lastFilterReq = requestedParams[requestedParams.length - 1];
    assert(lastFilterReq.includes("provider=pick.owner%40corp.example") || lastFilterReq.includes("provider=pick.owner@corp.example"),
      `selecting masked option must still request raw value, got: ${lastFilterReq}`);

    // Turn off mask
    await page.evaluate(() => window.show("routing"));
    await page.locator("#rtMask").waitFor();
    await page.locator("#rtMask").click();
    await page.evaluate(() => window.show("analytics"));
    await page.locator("#view-analytics:not([hidden])").waitFor();

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
      return createServer()(route);
    });

    // the dimension here is By Model: drill from a ranking row
    await page.locator(".an-bar-row").first().click();
    await page.locator("#anDrillPage:not([hidden])").waitFor();
    assert(lastDrillCallsUrl, "drill calls request must be made");
    assert.equal(new URL(lastDrillCallsUrl).searchParams.get("period"), shownPeriod, "drill calls must use the rolled-back shown period, not the failed 7d");

    await page.locator(".an-drill-back").click();
    await page.locator("#anBody:not([hidden])").waitFor();
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
    const backBtn = page.locator(".an-drill-back");
    await backBtn.click();
    await page.locator("#anBody:not([hidden])").waitFor();

    assert(await page.locator("#anBody").isVisible(), "dashboard body must be visible again");
    assert(await page.locator("#anDrillPage").isHidden(), "drill page must be hidden");
    assert.equal(callsRequests, 1, "no extra calls requests triggered by clicking back");
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
          ...mockAnalyticsData.rankings.model,
          by_error_rate: [
            {
              key: "model-short-94",
              metric_val: 0.94,
              insufficient: false,
              unknown_cache: false,
              has_unpriced: false,
              share: 0.5,
              calls: 100,
              rate_limited: 94,
              server_err: 0,
              other_err: 0,
              error_rate: 0.94,
              cost: 1.0,
            },
            {
              key: "model-very-long-name-identifier-95",
              metric_val: 0.95,
              insufficient: false,
              unknown_cache: false,
              has_unpriced: false,
              share: 0.5,
              calls: 100,
              rate_limited: 95,
              server_err: 0,
              other_err: 0,
              error_rate: 0.95,
              cost: 1.0,
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
    // 4. Verify tooltips on bar row and focus accessibility
    const firstBar = page.locator(".an-bar-row").first();
    const tooltipTitle = await firstBar.getAttribute("title");
    const ariaLabel = await firstBar.getAttribute("aria-label");
    assert(tooltipTitle && tooltipTitle.includes("Calls:"), `bar row must have detailed tooltip: ${tooltipTitle}`);
    assert(ariaLabel && ariaLabel.includes("Calls:"), `bar row must have accessible aria-label: ${ariaLabel}`);

    // 5. Test filter clear button: select provider then click clear x
    const providerWrap = page.locator('.an-filter-wrap[data-filter-dim="provider"]');
    const providerBtn = providerWrap.locator(".an-filter-btn");
    await providerBtn.click();
    await page.locator("#pop:not([hidden])").waitFor();
    await page.locator("#pop #list li").filter({ hasText: "openai" }).click();
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
    await page.locator("#pop:not([hidden])").waitFor();
    await page.locator("#pop #list li").filter({ hasText: longModelName }).click();
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

    // Toggle mask on in-place via Routing button by switching view within single SPA session
    await page.evaluate(() => window.show("routing"));
    await page.locator("#rtMask").waitFor();
    await page.locator("#rtMask").click();

    // Switch back to analytics in-place
    await page.evaluate(() => window.show("analytics"));
    await page.locator("#view-analytics:not([hidden])").waitFor();

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

    // Unmask in-place
    await page.evaluate(() => window.show("routing"));
    await page.locator("#rtMask").waitFor();
    await page.locator("#rtMask").click();
    await page.evaluate(() => window.show("analytics"));
  });
}
