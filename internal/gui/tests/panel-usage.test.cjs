// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Usage tab (the window's Requests, made small): what the
// requests of a period add up to — four totals, a small chart of them by the
// hour or day, and who they were of, seven at most — for today, seven or thirty
// days, over one provider when one is picked (the chart then tells its models
// apart); a click on a provider in the ranking, or the picker beside the
// period, switches to it; the metric can be tokens, cost or requests. Open
// Usage takes the window to the Requests of the provider shown, and the window
// opened so has that provider and agent picked. Nothing runs out of a narrow
// panel and a click leaves the panel where it is; the allowances keep their own
// tab. English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

const MODELS = { relay: ["gpt-6-sol"], anthropic: ["claude-sonnet-5", "claude-opus-5"] };
const WHO = [
  { id: "anthropic", name: "Claude", icon: "claudecode-color", hours: [8, 20], tokens: 900000, calls: 2, cost: 0.9 },
  { id: "relay", name: "Relay", icon: "generic", hours: [10, 14], tokens: 400000, calls: 3, cost: 0.4 },
];
const shareOf = (id, name, icon, calls, tokens, cost, errors = 0) => ({ id, name, icon, calls, errors, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost });

function page(q) {
  if (q.has("day")) {
    const whole = new URLSearchParams(q);
    whole.delete("day");
    const l = page(whole), point = l.series.find((p) => p.time.slice(0, 10) === q.get("day"));
    const by = Object.fromEntries(Object.entries(l.by).map(([dim, shares]) => [dim, shares.flatMap((s) => {
      const part = point?.by[dim][s.id];
      return part ? [shareOf(s.id, s.name, s.icon, part.calls, part.tokens, part.cost)] : [];
    })]));
    return { ...l, calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0,
      ...point, series: l.series, day: q.get("day"), chartBy: l.by, by, total: point?.calls ? 1 : 0 };
  }
  const only = q.get("provider");
  const who = WHO.filter((w) => !only || w.id === only);
  const days = q.get("period") === "today" ? 1 : q.get("period") === "7d" ? 7 : 30;
  const hourly = days === 1;
  const n = hourly ? 24 : days;
  const series = Array.from({ length: n }, (_, i) => {
    const time = new Date(hourly ? midnight.getTime() + i * 3600e3 : midnight.getTime() - (n - 1 - i) * 864e5);
    const on = (w) => (hourly ? i >= w.hours[0] && i <= w.hours[1] : i % 2 === 0) ? 1 : 0;
    const by = { provider: {}, agent: {}, model: {} };
    let calls = 0, tokens = 0, cost = 0;
    for (const w of who) {
      if (!on(w)) continue;
      const models = MODELS[w.id];
      for (const m of models) {
        const part = { calls: w.calls / models.length, tokens: w.tokens / models.length, cost: w.cost / models.length };
        by.model[m] = part;
      }
      by.provider[w.id] = { calls: w.calls, tokens: w.tokens, cost: w.cost };
      calls += w.calls; tokens += w.tokens; cost += w.cost;
    }
    return { label: String(i), time: time.toISOString(), calls, errors: 0, input: tokens * 0.01, output: tokens * 0.005, cache_write: tokens * 0.05, cache_read: tokens * 0.935, cost, by };
  });
  const sum = (k) => series.reduce((a, p) => a + p[k], 0);
  const tot = (pick) => { const m = {}; for (const p of series) for (const [k, v] of Object.entries(pick(p))) { m[k] = m[k] || { calls: 0, tokens: 0, cost: 0 }; m[k].calls += v.calls; m[k].tokens += v.tokens; m[k].cost += v.cost; } return m; };
  const facet = (w) => {
    const hours = hourly ? w.hours[1] - w.hours[0] + 1 : Math.ceil(n / 2);
    return shareOf(w.id, w.name, w.icon, w.calls * hours, w.tokens * hours, w.cost * hours, w.id === "relay" ? 3 : 0);
  };
  const models = Object.entries(tot((p) => p.by.model)).map(([id, x]) => shareOf(id, id, "", x.calls, x.tokens, x.cost));
  models.sort((a, b) => b.cache_read - a.cache_read);
  return {
    period: q.get("period"), rows: [], offset: 0, total: sum("calls") ? 1 : 0, calls: sum("calls"), errors: only === "relay" || !only ? 3 : 0,
    input: sum("input"), output: sum("output"), cache_write: sum("cache_write"), cache_read: sum("cache_read"), reasoning: 0, cost: sum("cost"), unpriced: 0,
    bucket: hourly ? "hour" : "day", series,
    by: { provider: WHO.map(facet).sort((a, b) => b.cache_read - a.cache_read), agent: [], model: models },
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }], providers: WHO.map((w) => ({ id: w.id, name: w.name, icon: w.icon })),
  };
}

function serve(lang, asked, opened, fitted = [], resize) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(page(url.searchParams)); }
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage") return json({ calls: 1, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, bucket: "day", series: [], agents: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/window/main") { opened.push(url.search); return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/window/fit") {
      const h = Number(url.searchParams.get("h"));
      fitted.push(h);
      if (resize) await resize(h);
      return route.fulfill({ status: 204 });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { tab: "Usage", quota: "Allowances", periods: ["Today", "7 days", "30 days"], blocks: ["Tokens", "Requests", "Cost", "Cache hit rate"], all: "All providers", open: "Open Usage", metrics: ["Tokens", "Cost", "Requests"] },
  zh: { tab: "用量", quota: "额度", periods: ["今天", "7 天", "30 天"], blocks: ["Token", "请求", "费用", "缓存命中率"], all: "全部供应商", open: "打开用量", metrics: ["Token", "费用", "请求"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the tray panel's Usage tab`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [], pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-panel-usage-${i}.png`) });
        }
        await browser.close();
      });
      const w = want[lang];
      const fitted = [];
      const open = async (url, viewport, asked, opened) => {
        const p = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(p);
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, asked, opened, fitted, (h) => p.setViewportSize({width: p.viewportSize().width, height:Math.max(220,Math.min(560,h))})));
        await p.goto(url);
        return p;
      };
      const settled = async (asked, ok) => {
        for (let i = 0; i < 60 && !(asked.length && ok(asked.at(-1))); i++) await new Promise((r) => setTimeout(r, 40));
        assert(asked.length && ok(asked.at(-1)), "asked: " + (asked.at(-1) || ""));
      };

      const asked = [], opened = [];
      const p = await open("http://magpie.test/?mode=panel", { width: 440, height: 340 }, asked, opened);
      assert.equal((await p.locator('[data-ptab="stats"]').textContent()).trim(), w.tab);
      assert.equal((await p.locator('[data-ptab="usage"]').textContent()).trim(), w.quota, "the allowances keep their tab");
      await p.locator('[data-ptab="usage"][hidden]').waitFor({state:"attached"});
      assert.equal(await p.locator("#ptabs button:not([hidden])").count(), 3, "no allowances means no allowances tab");
      await p.locator('[data-ptab="stats"]').click();
      await p.locator("#panelUsage .pu-tot").waitFor();
      assert(await p.locator("#panelUsage").isVisible());
      assert.equal(await p.locator("#panelUsage > .usage-note").textContent(), lang === "zh" ? "统计网关调用与会话日志调用；本地拒绝的请求不计入汇总。" : "Gateway and session-log calls; local rejections excluded from totals.");
      // read again on asking (#546), and as the panel is opened again
      const refresh = p.locator("#panelUsage .pu-again");
      assert.equal(await refresh.getAttribute("aria-label"), lang === "zh" ? "立即刷新" : "Refresh now");
      let n = asked.length;
      await refresh.click();
      for (let i = 0; i < 60 && asked.length === n; i++) await new Promise((r) => setTimeout(r, 40));
      assert.equal(asked.length, n + 1, "Refresh reads the usage again");
      await new Promise((r) => setTimeout(r, 2100));
      n = asked.length;
      await p.evaluate(() => window.dispatchEvent(new Event("focus")));
      for (let i = 0; i < 60 && asked.length === n; i++) await new Promise((r) => setTimeout(r, 40));
      assert.equal(asked.length, n + 1, "the panel focused reads the usage again");
      n = asked.length;
      await p.evaluate(() => window.dispatchEvent(new Event("focus")));
      await new Promise((r) => setTimeout(r, 300));
      assert.equal(asked.length, n, "not again at once");
      assert(!(await p.locator("#agents").isVisible()) && !(await p.locator("#panelRouting").isVisible()), "the others are other tabs");
      assert.equal(asked[0].get("period"), "today");
      assert.equal(asked[0].get("limit"), "1", "a page of one row is all it asks for");

      // the period, the four totals, the chart, and the ranking of who
      assert.deepEqual(await p.locator("#panelUsage .pu-bar .segs .opt").allTextContents(), w.periods);
      assert.equal(await p.locator("#panelUsage .pu-bar .segs .opt.on").textContent(), w.periods[0]);
      assert.deepEqual(await p.locator("#panelUsage .pu-tot .k").allTextContents(), w.blocks);
      assert((await p.locator("#panelUsage .pu-tot .blk").nth(1).locator(".sub").textContent()).includes("3"), "the failures under the requests");
      assert(await p.locator("#panelUsage rect.col").count() > 10, "columns");
      assert.deepEqual(await p.locator("#panelUsage .led-rank .rk-nm").allTextContents(), ["Claude", "Relay"]);
      assert.equal((await p.locator("#panelUsage .sess-pick").textContent()).trim(), w.all);
      assert.deepEqual(await p.locator("#panelUsage .pu-card .segs .opt").allTextContents(), w.metrics);
      // Labels in the 440px panel must be readable, not clipped by ellipsis.
      assert(await p.locator("#panelUsage .pu-tot .blk").first().locator(".sub").evaluate(e =>
        e.scrollWidth <= e.clientWidth + 1 && e.scrollHeight <= e.clientHeight + 1 && getComputedStyle(e).textOverflow !== "ellipsis"), "token input/output is fully visible");
      // the total counts the cache, so the line under it names the cache too, or in + out doesn't add up to it
      assert((await p.locator("#panelUsage .pu-tot .blk").first().locator(".sub").textContent()).includes(lang === "zh" ? "缓存" : "cached"), "the cache under the token total");
      // nothing runs out of the panel
      const over = await p.evaluate(() => [...document.querySelectorAll("#panelUsage *")].filter((e) => e.scrollWidth > e.clientWidth + 1 && getComputedStyle(e).overflow === "visible" && e.children.length === 0 && e.tagName !== "text").length);
      assert.equal(over, 0, "an element wider than itself");
      assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the panel doesn't scroll sideways");

      // a column under the pointer
      const svg = await p.locator("#panelUsage .led-chart svg").boundingBox();
      await p.mouse.move(svg.x + svg.width * (12.5 / 24), svg.y + svg.height * 0.5);
      assert(await p.locator("#panelUsage .tip").isVisible());
      const tip = await p.locator("#panelUsage .tip").innerText();
      assert(tip.includes("Claude") && tip.includes("Relay"), tip);
      await p.mouse.move(2, 2);

      // Native fit includes the Usage tab, and all controls fit without scrolling.
      const height = await p.evaluate(() => {
        const body = document.body, tab = body.dataset.ptab;
        delete body.dataset.ptab;
        const h = document.querySelector('.top').offsetHeight + document.querySelector('#ptabs').offsetHeight + Math.max(...['agents','panelQuota','panelRouting','panelUsage'].map(id => document.getElementById(id).offsetHeight)) + document.querySelector('.foot').offsetHeight + 4;
        body.dataset.ptab = tab;
        return h;
      });
      for (let i=0;i<40 && fitted.at(-1) !== height;i++) await p.waitForTimeout(25);
      assert.equal(fitted.at(-1),height,"the panel fits its tallest tab including Usage");
      const scroll = () => p.locator("#view-agents").evaluate(v => v.scrollTop);
      const before = await scroll();
      await p.locator("#panelUsage .pu-card .segs .opt").nth(1).click();
      assert((await p.locator("#panelUsage svg .axis").allTextContents()).some(s => /^[$¥]/.test(s)), "the cost's axis");
      assert.equal(await scroll(),before,"a click moved the panel");
      await p.locator("#panelUsage .pu-card .segs .opt").nth(0).click();
      assert.equal(await scroll(),before,"a click moved the panel");

      // the period: seven days is by the day
      await p.locator("#panelUsage .pu-bar .segs .opt").nth(1).click();
      await settled(asked, (q) => q.get("period") === "7d");
      await p.locator("#panelUsage .pu-bar .segs .opt.on", { hasText: w.periods[1] }).waitFor();
      await p.locator("#panelUsage:not(.pu-loading)").waitFor(); // the answer is in, not only asked for
      assert((await p.locator("#panelUsage svg .axis").allTextContents()).some((s) => /\d\/\d|\d月/.test(s)), "days along the bottom");

      // Pick a day without losing the rest of the period, then an empty day.
      const days = p.locator("#panelUsage .led-day");
      const label = await days.nth(4).getAttribute("aria-label");
      const original = await p.locator("#panelUsage rect.col").evaluateAll((rs) => rs.map((r) => [r.dataset.day, r.dataset.k, r.getAttribute("height"), r.dataset.color]));
      await days.nth(4).click();
      await settled(asked, (q) => q.has("day"));
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      const selectedDay = asked.at(-1).get("day");
      assert.equal(await p.locator('#panelUsage .led-day[aria-pressed="true"]').getAttribute("aria-label"), label);
      assert.equal(await p.locator("#panelUsage .led-rank .rk").first().locator(".rk-val").textContent(), "900,000");
      assert.deepEqual(await p.locator("#panelUsage rect.col").evaluateAll((rs) => rs.map((r) => [r.dataset.day, r.dataset.k, r.getAttribute("height"), r.dataset.color])), original, "the chart keeps its heights and colors");
      const dimmed = () => p.locator("#panelUsage rect.col").evaluateAll((rs, day) => rs.every((r) => r.dataset.day === day ? r.style.opacity === "" && r.style.fill === r.dataset.color : r.style.opacity === "0.22" && r.style.fill === "var(--faint)"), selectedDay);
      assert(await dimmed(), "the other days are gray");
      await p.mouse.move(2, 2);
      assert(await dimmed(), "selection survives leaving the chart");
      await refresh.click();
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.equal(asked.at(-1).get("day"), selectedDay, "refresh keeps the selected day");
      await p.locator("#panelUsage .pu-card .segs .opt").nth(1).click();
      assert.equal(parseFloat((await p.locator("#panelUsage .led-rank .rk").first().locator(".rk-val").textContent()).slice(1)), 0.9);
      await p.locator("#panelUsage .pu-card .segs .opt").nth(0).click();
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        const context = await browser.newContext({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
        const shot = await context.newPage();
        shot.setDefaultTimeout(5000);
        await shot.addInitScript(() => { localStorage.setItem("magpie.panelTab", "stats"); localStorage.setItem("magpie.panelUsePeriod", "7d"); });
        await shot.route("**/*", serve(lang, [], []));
        await shot.goto("http://magpie.test/?mode=panel");
        await shot.locator("#panelUsage .led-day").nth(4).click();
        await shot.locator('#panelUsage .led-day[aria-pressed="true"]').waitFor();
        await shot.mouse.move(2, 2);
        await shot.locator("#panelUsage .pu-card").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-panel-day.png`) });
        await context.close();
      }
      await days.nth(1).click();
      await settled(asked, (q) => q.has("day") && q.get("day") !== selectedDay);
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.equal(await p.locator("#panelUsage .led-rank .rk").count(), 0, "an empty day clears the ranking");
      assert.equal(await days.count(), 7, "an empty day keeps every day available");
      const emptyDay = asked.at(-1).get("day");
      await days.nth(1).click();
      await settled(asked, (q) => !q.has("day"));
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.equal(await p.locator('#panelUsage .led-day[aria-pressed="true"]').count(), 0);
      // Hold the first response so the second click happens before it arrives.
      let release, held, blocked;
      const delay = () => {
        held = new Promise((resolve) => { release = resolve; });
        blocked = false;
      };
      const hold = async (route) => {
        const q = new URL(route.request().url()).searchParams;
        if (!blocked && q.has("day")) {
          blocked = true;
          asked.push(q);
          await held;
          await route.fulfill({ json: page(q) });
          return;
        }
        await route.fallback();
      };
      const reply = (day) => p.waitForResponse((r) => {
        const url = new URL(r.url());
        return url.pathname === "/api/usage/requests" && (url.searchParams.get("day") || "") === day;
      }).then((r) => r.finished());
      await p.route("**/api/usage/requests?*", hold);
      delay();
      const beforeClicks = asked.length, clickedDay = await days.nth(4).getAttribute("data-day");
      const requested = p.waitForRequest((r) => new URL(r.url()).searchParams.get("day") === clickedDay);
      const selectedReply = reply(clickedDay), clearedReply = reply("");
      await days.nth(4).dispatchEvent("click");
      await requested;
      await days.nth(4).dispatchEvent("click");
      release();
      await Promise.all([selectedReply, clearedReply]);
      await p.evaluate(() => new Promise(requestAnimationFrame));
      await settled(asked, (q) => asked.length > beforeClicks && !q.has("day"));
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.equal(await p.locator('#panelUsage .led-day[aria-pressed="true"]').count(), 0, "two quick clicks clear selection");

      // A delayed redraw must not take focus back after the user moves it.
      delay();
      const movedRequest = p.waitForRequest((r) => new URL(r.url()).searchParams.get("day") === clickedDay);
      const movedReply = reply(clickedDay);
      await days.nth(4).focus();
      await p.keyboard.press("Enter");
      await movedRequest;
      const away = p.locator('#ptabs [data-ptab="stats"]');
      await away.focus();
      release();
      await movedReply;
      await p.locator('#panelUsage .led-day[aria-pressed="true"]').waitFor();
      assert(await away.evaluate((e) => e === document.activeElement), "the redraw does not steal focus");
      const resetReply = reply("");
      await days.nth(4).dispatchEvent("click");
      await resetReply;
      await p.waitForFunction(() => !document.querySelector('#panelUsage .led-day[aria-pressed="true"]'));
      await p.unroute("**/api/usage/requests?*", hold);
      const keyboardDay = await days.nth(1).getAttribute("data-day");
      await days.nth(1).focus();
      await p.keyboard.press("Enter");
      await settled(asked, (q) => q.get("day") === emptyDay);
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();

      await p.waitForFunction((day) => document.activeElement?.matches('#panelUsage .led-day[aria-pressed="true"]') && document.activeElement.dataset.day === day, keyboardDay);
      await p.keyboard.press("Space");
      await settled(asked, (q) => !q.has("day"));
      await p.waitForFunction((day) => document.activeElement?.matches('#panelUsage .led-day[aria-pressed="false"]') && document.activeElement.dataset.day === day, keyboardDay);
      // a click on a provider switches to it: its requests, its models in the chart
      await p.locator("#panelUsage .pu-bar .segs .opt").nth(0).click();
      await settled(asked, (q) => q.get("period") === "today");
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert(!asked.at(-1).has("day"), "changing the period clears day selection");
      // half under the footer's edge, WebKit takes it as in view and clicks the footer: scroll it in whole, as a reader would
      await p.locator("#panelUsage .pu-tot").hover();
      await p.mouse.wheel(0, 400);
      await p.waitForFunction(() => document.querySelector("#panelUsage .led-rank .rk").getBoundingClientRect().bottom <= document.querySelector("footer.foot").getBoundingClientRect().top);
      await p.locator("#panelUsage .led-rank .rk").first().click();
      await settled(asked, (q) => q.get("provider") === "anthropic");
      await p.locator("#panelUsage .sess-pick", { hasText: "Claude" }).waitFor();
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.deepEqual(await p.locator("#panelUsage .led-rank .rk-nm").allTextContents(), ["claude-sonnet-5", "claude-opus-5"], "its models");
      assert.equal(await p.locator("#panelUsage rect.col").evaluateAll((r) => [...new Set(r.map((x) => x.dataset.k))].sort().join()), "claude-opus-5,claude-sonnet-5");

      // Open Usage takes the window to that provider's requests
      await p.mouse.wheel(0, -400);
      await p.locator("#panelUsage .pu-bar .text").click();
      for (let i = 0; i < 50 && !opened.length; i++) await p.waitForTimeout(40);
      assert.equal(opened.at(-1), "?view=usage&tab=requests&provider=anthropic");
      assert.equal((await p.locator("#panelUsage .pu-bar .text").textContent()).trim(), w.open);

      // the picker lists the providers, and all of them again
      await p.locator("#panelUsage .sess-pick").click();
      await p.locator(".sess-menu button, .sess-menu [role=option], .sess-menu .opt").filter({ hasText: w.all }).first().click();
      await settled(asked, (q) => !q.get("provider"));
      await p.locator("#panelUsage .sess-pick", { hasText: w.all }).waitFor();
      await p.locator("#panelUsage:not(.pu-loading)").waitFor();
      assert.deepEqual(await p.locator("#panelUsage .led-rank .rk-nm").allTextContents(), ["Claude", "Relay"]);

      // a narrow panel
      await p.setViewportSize({ width: 320, height: 640 });
      await p.waitForTimeout(250);
      assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "no sideways scroll at 320");
      const narrow = await p.locator("#panelUsage .led-chart svg").boundingBox(), card = await p.locator("#panelUsage .pu-card").boundingBox();
      assert(narrow.x + narrow.width <= card.x + card.width + 1, "the chart fits at 320");
      // nothing is cut off at the panel's edge: every control of the bar is whole, and the totals two to a row
      const edge = await p.evaluate(() => [...document.querySelectorAll("#panelUsage .pu-bar > *")].filter((e) => e.getBoundingClientRect().right > innerWidth - 4).length);
      assert.equal(edge, 0, "a control of the bar runs out of the panel");
      assert.equal(await p.locator("#panelUsage .pu-tot").evaluate((k) => getComputedStyle(k).gridTemplateColumns.split(" ").length), 2, "two totals to a row when narrow");

      // a panel that opens on the Usage tab, as the reader left it: the page draws it
      // as it loads, before what it draws with is all set, and must not fail on it
      const again = [];
      const q = await (await browser.newContext({ viewport: { width: 440, height: 500 }, reducedMotion: "reduce" })).newPage();
      pages.push(q);
      q.setDefaultTimeout(5000);
      q.on("pageerror", (e) => again.push(e.message));
      await q.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "stats"); } catch {} });
      await q.route("**/*", serve(lang, [], []));
      await q.goto("http://magpie.test/?mode=panel");
      await q.locator("#panelUsage .pu-tot").waitFor();
      assert.equal(await q.locator('#ptabs [data-ptab="stats"]').getAttribute("aria-selected"), "true");
      assert.deepEqual(again, [], "the panel failed to load on the Usage tab");
      // and its other tabs still work from there
      await q.locator('#ptabs [data-ptab="agents"]').click();
      assert(await q.locator("#agents").isVisible() && !(await q.locator("#panelUsage").isVisible()));
      // whichever tab the reader left it on, it loads without an error
      for (const tab of ["agents", "usage", "stats", "routing", "profiles"]) {
        const failed = [];
        const x = await (await browser.newContext({ viewport: { width: 440, height: 500 }, reducedMotion: "reduce" })).newPage();
        pages.push(x);
        x.on("pageerror", (e) => failed.push(e.message));
        await x.addInitScript((t) => { try { localStorage.setItem("magpie.panelTab", t); } catch {} }, tab);
        await x.route("**/*", serve(lang, [], []));
        await x.goto("http://magpie.test/?mode=panel");
        await x.waitForTimeout(400);
        assert.deepEqual(failed, [], `the panel failed to load on the ${tab} tab`);
        assert.equal(await x.locator("#ptabs button:not([hidden])").count(), 3, `the tabs are there on ${tab}`);
      }

      // the window opened from the panel: the Requests tab, on that provider and agent
      const w2 = await open("http://magpie.test/?view=usage&tab=requests&provider=relay&agent=codex", { width: 1100, height: 700 }, (asked.length = 0, asked), opened);
      await w2.locator("#ledWrap, #ledDash").first().waitFor({ state: "attached" });
      await settled(asked, (q) => q.get("provider") === "relay" && q.get("agent") === "codex");
      assert.equal(await w2.locator("#usageTab .opt.on").textContent(), lang === "zh" ? "请求" : "Requests");
      assert(!/provider=|tab=/.test(w2.url()), "the address is clean: " + w2.url());
      // Every distinct chart color has a named legend row, with the rest grouped.
      const ranked = await p.evaluate(() => {
        const rows = Array.from({length:9}, (_,i) => ({id:"p"+i,name:"Provider "+i,calls:9-i,input:(9-i)*100,output:0,cache_read:0,cache_write:0,cost:0}));
        const rank = document.createElement("div");
        drawLedRank(rank,{by:{provider:rows}},"provider","tokens","",()=>{},true);
        return {names:[...rank.querySelectorAll(".rk-nm")].map(x=>x.textContent), colors:[...rank.querySelectorAll(".rk-sw")].map(x=>x.style.background)};
      });
      assert.deepEqual(ranked.names.slice(0,7),Array.from({length:7},(_,i)=>"Provider "+i));
      assert.equal(ranked.names.length,8);
      assert.equal(new Set(ranked.colors.slice(0,7)).size,7);
      assert.deepEqual(errors, []);
    });
  }
}
