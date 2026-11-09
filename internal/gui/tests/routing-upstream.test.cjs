// Run with Node's test runner and Playwright on the module path; see README.md.
// The provider an aggregator says answered behind it (leslie_luo on
// Discord: 指出实际供应商, as CPA does, for ClinePass and OpenRouter): a
// request OpenRouter passed on to DeepInfra says "Upstream: DeepInfra" in
// the Routing page's Requests list, whole and inside its row, its story
// says so, and a request's details on the Usage page name it; one whose
// reply named none says nothing. No click moves the page, no left-border
// accent. English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "openrouter", provider: "openrouter", name: "OpenRouter", kind: "provider", model: "deepseek/deepseek-chat" };
// newest first: one passed on to DeepInfra, then ones that named none
const ups = ["DeepInfra", "", "", "", "", ""];
const routes = ups.map((u, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "openrouter/deepseek/deepseek-chat", provider: "openrouter",
  order: [key], tries: [{ id: key.id, model: "deepseek/deepseek-chat", start: at(i), done: true, status: 200, ms: 900, served: "deepseek/deepseek-chat", ...(u ? { upstream: u } : {}) }],
  done: true, status: 200, ms: 900, tokens: 1200, served: "deepseek/deepseek-chat", ...(u ? { upstream: u } : {}),
}));
const ledRow = (i, u) => ({
  t: at(i), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "openrouter", providerName: "OpenRouter",
  req: "openrouter/deepseek/deepseek-chat", model: "deepseek/deepseek-chat", served: "deepseek/deepseek-chat", in: 300, out: 40, ms: 2000, status: 200, rid: "gen-" + i, ep: "/v1/chat/completions",
  ...(u ? { upstream: u } : {}),
});
const ROWS = [ledRow(0, "DeepInfra"), ledRow(1, "")];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: 2, calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 2, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 2, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { tag: "Upstream: DeepInfra", story: /passed the request on to DeepInfra/, dt: "Upstream provider" },
  zh: { tag: "上游：DeepInfra", story: /把这条请求转给了 DeepInfra/, dt: "上游" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the provider behind an aggregator is named`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-upstream.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      // a browser started cold draws the days late
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(ups.length - 1).waitFor();

      // the list: the one passed on says where, whole, inside its row
      const tags = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.querySelector(".to .upstream")?.textContent || ""));
      assert.deepEqual(tags, [want[lang].tag, "", "", "", "", ""]);
      const look = await page.locator(".rt-req .to .upstream").evaluate((e) => {
        const row = e.closest(".rt-req").getBoundingClientRect(), b = e.getBoundingClientRect(), cs = getComputedStyle(e);
        return { inRow: b.left >= row.left && b.right <= row.right + 0.5 && b.width > 0, full: e.clientWidth >= e.scrollWidth, border: cs.borderLeftWidth, title: e.title };
      });
      assert(look.inRow && look.full, JSON.stringify(look));
      assert.equal(look.border, "0px");
      assert.match(look.title, want[lang].story);

      // its story says so, and the click leaves the row where it is
      const row = page.locator(".rt-req").nth(0);
      // measure once the page holds still: the day's rows arriving grow the
      // page above the list, and the day button's click is kept in place by
      // scrolling the view, which can land after the last row is in
      const was = await row.evaluate(async (e) => {
        let last = NaN;
        for (let i = 0; i < 60; i++) {
          await new Promise((r) => requestAnimationFrame(r));
          const top = e.getBoundingClientRect().top;
          if (top === last) return top;
          last = top;
        }
        return last;
      });
      const scrolled = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      await row.click();
      await page.waitForTimeout(600);
      assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), scrolled);
      const story = page.locator(".rt-steps li.upstream-said");
      assert.equal(await story.count(), 1);
      assert.match(await story.textContent(), want[lang].story);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-upstream-routing.png`) });
      await page.locator(".rt-req").nth(1).click();
      await page.waitForTimeout(300);
      assert.equal(await page.locator(".rt-steps li.upstream-said").count(), 0);

      // the Usage page's Requests: a request's details name it
      await page.locator('[data-view="usage"]').first().click();
      await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      const rows = page.locator(".led tbody tr.led-row");
      await rows.nth(1).waitFor();
      const top = () => page.locator("#view-usage").evaluate((v) => v.scrollTop);
      const dd = async (i) => {
        // brought into view by the wheel; back from the request's page is
        // the list where it was
        await reader.inView(page, rows.nth(i));
        const was = await top();
        const box = await reader.openRequest(page, rows.nth(i));
        const said = await box.evaluate((b, dt) => [...b.querySelectorAll("dt")].find((d) => d.textContent === dt)?.nextElementSibling?.textContent || "", want[lang].dt);
        if (i === 0) return said; // the next looks at the page's border
        await reader.closeRequest(page);
        assert.equal(await top(), was, "back to the list where it was left");
        return said;
      };
      assert.equal(await dd(1), "");
      assert.equal(await dd(0), "DeepInfra");
      const border = await page.evaluate(() => [...document.querySelectorAll(".rt-req .upstream, .led-detail dd")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      for (const l of ["zh", "ja", "de"]) {
        const missing = await page.evaluate((l) => ["Upstream: {upstream}", "Upstream provider",
          "The aggregator passed the request on to {upstream}, as its reply says: the provider that actually answered it."].filter((k) => !I18N[l][k]), l);
        assert.deepEqual(missing, [], `every string has its ${l}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
