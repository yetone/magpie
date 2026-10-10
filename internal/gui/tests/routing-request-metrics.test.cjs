// Request rows use the recorded prompt tiers and the reply's decode window,
// in live and historical lists, grouped or flat. No user state is accessed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const seat = { id: "codex", provider: "codex", name: "Codex", who: "test@example.com", kind: "account", model: "gpt-6.1-sol" };
const tier = (input, read = 0, write = 0, out = 400) => ({ provider: seat.provider, model: seat.model, in: input, out, cache_read: read, cache_write: write });
function req(id, usage, extra = {}) {
  const time = new Date(now.getTime() - (20 - id) * 60e3).toISOString();
  return { id, seq: id, time, agent: "codex", session: "metrics-chat", model: "codex/gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, effort: "xhigh", start: time, done: true, status: 200, ms: 11000, ttft: 7000 }],
    done: true, status: 200, ms: 11000, ttft: 7000, tokens: 5400, out: 400, usage, priced: true, cost: 0.01, ...extra };
}
const fixtures = [
  // The prompt is 5000 tokens: cache writes belong in its denominator.
  req(12, [tier(1000, 3000, 1000)]),
  req(11, [tier(1000)], { out: 2000, ms: 17000, cost: 0 }), // known zero hits and cost; 200 tok/s
  req(10, [tier(0, 5000)]),                      // every input token cached
  req(9, undefined),                            // older history has no tiers
  req(8, [tier(0, 0, 0)]),                       // output only: no prompt ratio
  req(7, [tier(1000, 3000, 1000)], { ttft: 0 }), // unstreamed: no speed
  req(6, [tier(1000, 3000, 1000)], { out: 8264, ms: 24360, ttft: 24359 }),
  req(5, [tier(1000, 3000, 1000)], { out: 8264, ms: 1500, ttft: 1000 }),
  // One refused try cost tokens too. The final reply is 400 tokens over
  // four seconds, after a ten-second retry delay, not 1000 over 21 seconds.
  req(4, [tier(1000, 0, 1000, 600), tier(1000, 6000, 1000)], {
    ms: 21000, ttft: 17000,
    tries: [{ id: seat.id, model: seat.model, start: now.toISOString(), done: true, status: 200, fail: "refused", ms: 10000 },
      { id: seat.id, model: seat.model, effort: "xhigh", start: now.toISOString(), done: true, status: 200, ms: 11000, ttft: 7000 }],
  }),
  req(3, [tier(1000, 3000, 1000)], { done: false }),
  req(2, [tier(1000, 3000, 1000)], { out: 0 }),
  req(1, [tier(1000, 3000, 1000)], { tries: [{ id: seat.id, start: now.toISOString(), done: true, status: 200, fail: "other", ms: 11000, ttft: 7000 }] }),
  req(0, undefined, { session: "unknown-metrics", tokens: 0, out: 0, ms: 0, ttft: 0, priced: false, cost: 0 }),
];

function serve(lang, feed) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = resolve; }) : fixtures;
      return json({ mine: true, now: now.toISOString(), seq: feed.seq || 12, totals: { requests: fixtures.length, rerouted: 1, errors: 1 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: fixtures.length }], routes: url.searchParams.get("day") ? fixtures : [] });
    if (url.pathname === "/api/gateway/session-titles") return json({ "metrics-chat": "Request metrics" });
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
    test(`${engine} ${lang}: request cache rates and output speeds survive grouping, history and live updates`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const feed = {}, errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      const rows = page.locator(".rt-req");
      await rows.nth(fixtures.length - 1).waitFor();
      const check = async () => {
        assert.equal(await rows.nth(0).locator(".cache-hit .v").textContent(), "60%");
        assert.match(await rows.nth(0).locator(".cache-hit").getAttribute("title"), lang === "zh" ? /缓存命中率.*提示词/ : /Cache hit rate.*prompt/);
        assert.match(await rows.nth(0).locator(".speed .v").textContent(), lang === "zh" ? /^100 token\/秒$/ : /^100 tok\/s$/);
        assert.match(await rows.nth(1).locator(".cache-hit .v").textContent(), /0%$/);
        assert.match(await rows.nth(2).locator(".cache-hit .v").textContent(), /100%$/);
        assert.equal(await rows.nth(1).locator(".cost").textContent(), "≈$0.000", "known zero cost remains visible");
        assert.equal(await rows.nth(3).locator(".cache-hit").count(), 0, "legacy totals cannot establish a cache rate");
        assert.equal(await rows.nth(4).locator(".cache-hit").count(), 0, "output is not part of the prompt");
        for (const i of [5, 6, 7, 9, 10, 11]) assert.equal(await rows.nth(i).locator(".speed").count(), 0, `row ${i} has no measured output speed`);
        assert.match(await rows.nth(8).locator(".cache-hit").textContent(), /60%$/, "sum the billable prompt tiers across retries");
        assert.match(await rows.nth(8).locator(".speed .v").textContent(), /^100 /, "retry wait and earlier output do not change decode speed");
        assert.equal(await rows.nth(9).locator(".cache-hit").count(), 0, "a request still underway has no final rate");
        assert.equal(await rows.nth(12).locator(".meta").isVisible(), false, "a row with no available metrics has no empty band");
        assert.equal(await rows.locator(".rt-metric").filter({ hasText: "—" }).count(), 0, "unavailable metrics leave no dash placeholders");
        assert.equal(await rows.nth(0).locator(".ttft .k").textContent(), lang === "zh" ? "首响" : "First token");
        const labels = lang === "zh" ? ["Token 明细", "输入（未缓存）", "输出", "缓存读取", "缓存写入"]
          : ["Token breakdown", "Input (uncached)", "Output", "Cache read", "Cache write"];
        assert.equal(await rows.nth(0).locator(".tokens").getAttribute("title"),
          [labels[0], labels[1] + ": 1,000", labels[2] + ": 400", labels[3] + ": 3,000", labels[4] + ": 1,000"].join("\n"),
          "the tooltip shows exact, disjoint input, output and cache tiers");
        assert.match(await rows.nth(1).locator(".tokens").getAttribute("title"), new RegExp(labels[3] + ": 0"), "recorded zero cache tiers are shown");
        assert.equal(await rows.nth(3).locator(".tokens").getAttribute("title"), lang === "zh" ? "没有记录 Token 明细" : "No token breakdown was recorded", "missing historical tiers are not invented");
        const retry = await rows.nth(8).locator(".tokens").getAttribute("title");
        assert(retry.includes(labels[1] + ": 2,000") && retry.includes(labels[2] + ": 1,000") && retry.includes(labels[3] + ": 6,000") && retry.includes(labels[4] + ": 2,000"),
          "billable retry tiers are included in the breakdown, including earlier output");
        assert.match(retry, lang === "zh" ? /计费重试.*最终尝试/ : /billable retries.*final attempts/, "retry totals are explained");
      };
      await check();
      await page.locator(".rt-group-by button").nth(1).click();
      const group = page.locator("button.rt-session").filter({ hasText: /metrics-chat|Request metrics/ });
      await group.waitFor();
      await check();
      const unknown = page.locator("button.rt-session").filter({ hasText: "unknown-metrics" });
      assert.equal(await unknown.locator(".rt-metric").count(), 0, "a session with no measured cache or speed omits both");
      assert.equal(await unknown.locator(".cost").isVisible(), false, "a session with unknown cost omits it");
      assert.doesNotMatch(await unknown.locator(".summary").textContent(), /token|—/, "a session without token counts has no fake zero or dash");
      assert.equal(await group.locator(".cache-hit .v").textContent(), "63%", "cache average is weighted by prompt tokens, not by request count");
      assert.match(await group.locator(".speed .v").textContent(), /^133 /, "speed is output divided by summed decode windows, not the mean of individual speeds");
      assert.match(await group.locator(".cache-hit").getAttribute("title"), /9.*11|11.*9/, "unknown and unfinished requests are excluded and coverage is disclosed");
      assert.match(await group.locator(".rt-token-total").getAttribute("title"), /11.*12|12.*11/, "session token breakdown discloses missing records");
      await group.click();
      assert.equal(await group.getAttribute("aria-expanded"), "false");
      assert.equal(await group.locator(".cache-hit .v").textContent(), "63%", "closed sessions retain their metrics");
      await group.click();
      await rows.nth(0).click();
      assert.match(await page.locator(".rt-brief .cache-hit").textContent(), /60%$/);
      if (lang === "zh") {
        assert.match(await page.locator(".rt-brief .ttft").textContent(), /首响/);
        assert.doesNotMatch(await page.locator(".rt-brief .ttft").textContent(), /首字/);
      }
      for (const width of [1280, 1000, 700, 480, 360]) {
        await page.setViewportSize({ width, height: 900 });
        await page.waitForTimeout(100);
        const boxes = await rows.nth(0).evaluate((row) => {
          const box = (e) => { const r = e.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }; };
          return { row: box(row), meta: box(row.querySelector(".meta")), to: box(row.querySelector(".to")),
            metrics: [...row.querySelectorAll(".cache-hit, .speed, .cost")].map((e) => ({ ...box(e), clipped: e.scrollWidth > e.clientWidth + 1 })) };
        });
        for (const b of boxes.metrics) {
          assert(!b.clipped, `metric clipped at ${width}px`);
          assert(b.left >= boxes.row.left && b.right <= boxes.row.right, `metric outside row at ${width}px: ${JSON.stringify(boxes)}`);
        }
        assert(boxes.meta.left >= boxes.to.right - 1 || boxes.meta.top >= boxes.to.bottom - 1, `metadata overlaps destination at ${width}px`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `horizontal page overflow at ${width}px`);
        if (width === 480) {
          await page.locator("#rtMetrics").click();
          const menu = page.locator(".rt-metric-menu");
          const cost = menu.getByRole("menuitemcheckbox", { name: lang === "zh" ? "费用" : "Cost", exact: true });
          await cost.click();
          assert.equal(await rows.nth(0).locator(".cost").count(), 0, "narrow layout updates before closing the menu");
          assert.equal(await menu.isVisible(), true);
          await cost.click();
          assert.equal(await rows.nth(0).locator(".cost").count(), 1);
          assert.equal(await menu.isVisible(), true);
          await page.keyboard.press("Escape");
        }
        if (process.env.ARTIFACT_DIR && [1280, 480].includes(width)) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator(".rt-filters").evaluate((e) => e.scrollIntoView({ block: "start" }));
          await page.locator(".rt-reqs").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-request-metrics.png`) });
        }
      }
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.locator(".rt-days .rt-day").nth(1).click();
      await check();
      await page.locator(".rt-days .rt-day").nth(0).click();
      await check();
      // The row signature must notice rates changing even when total tokens,
      // cost, duration and first-token timing remain unchanged.
      await page.waitForFunction((count) => document.querySelectorAll(".rt-req").length === count, fixtures.length);
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
      assert(feed.next, "live poll started");
      feed.seq = 13;
      feed.next([req(12, [tier(1920, 2080, 1200, 200)], { seq: 13, out: 200 })]);
      await page.waitForFunction(() => document.querySelector(".rt-req .cache-hit")?.textContent.endsWith("40%"));
      assert.match(await rows.nth(0).locator(".speed .v").textContent(), /^50 /);
      const updated = await rows.nth(0).locator(".tokens").getAttribute("title");
      assert.match(updated, /1,920/);
      assert.match(updated, /2,080/);
      assert.match(updated, /1,200/);
      assert.match(await group.locator(".rt-token-total").getAttribute("title"), lang === "zh" ? /输出: 4,800/ : /Output: 4,800/, "session tooltip updates even when list token totals do not change");
      await page.locator("#rtMetrics").click();
      const menu = page.locator(".rt-metric-menu");
      assert.equal(await menu.locator('[role="menuitemcheckbox"]').count(), 6);
      // Keep only cache and speed. Each click updates the list immediately,
      // with the menu open and focus on the checkbox for continued selection.
      const labels = lang === "zh" ? ["耗时", "首响", "Token", "费用"] : ["Duration", "First token", "Tokens", "Cost"];
      for (const [i, label] of labels.entries()) {
        const option = menu.getByRole("menuitemcheckbox", { name: label, exact: true });
        await option.click();
        assert.equal(await rows.nth(0).locator(".rt-metric").count(), 5 - i, "hiding a metric is immediate");
        assert.equal(await option.getAttribute("aria-checked"), "false");
        assert.equal(await menu.isVisible(), true, "menu remains open while the list updates");
        assert.equal(await option.evaluate((e) => document.activeElement === e), true, "checkbox retains focus");
      }
      const duration = menu.getByRole("menuitemcheckbox", { name: labels[0], exact: true });
      await duration.focus();
      await page.keyboard.press("Space");
      assert.equal(await rows.nth(0).locator(".duration").count(), 1, "showing a metric by keyboard is immediate");
      assert.equal(await menu.isVisible(), true);
      await page.keyboard.press("Space");
      assert.equal(await rows.nth(0).locator(".duration").count(), 0);
      assert.equal(await group.locator(".cost").isVisible(), false, "session cost hides before closing the menu");
      assert.doesNotMatch(await group.locator(".session-count").textContent(), /token/);
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#rtMetrics").getAttribute("aria-expanded"), "false");
      assert.equal(await rows.nth(0).locator(".rt-metric").count(), 2);
      assert.equal(await group.locator(".cost").isVisible(), false);
      assert.doesNotMatch(await group.locator(".session-count").textContent(), /token/);
      await page.reload();
      await rows.nth(fixtures.length - 1).waitFor();
      assert.equal(await rows.nth(0).locator(".rt-metric").count(), 2, "metric choices survive a reload");
      await page.locator("#rtMetrics").click();
      assert.equal(await menu.locator('[aria-checked="true"]').count(), 2);
      await menu.getByRole("menuitemcheckbox", { name: lang === "zh" ? "缓存命中率" : "Cache hit rate", exact: true }).click();
      await menu.getByRole("menuitemcheckbox", { name: lang === "zh" ? "输出速度" : "Output speed", exact: true }).click();
      assert.equal(await rows.nth(0).locator(".meta").isVisible(), false, "hiding the last metric is immediate");
      assert.equal(await menu.isVisible(), true);
      await page.keyboard.press("Escape");
      assert.equal(await rows.nth(0).locator(".meta").isVisible(), false, "every metric can be hidden");
      assert.deepEqual(errors, []);
    });
  }
}
