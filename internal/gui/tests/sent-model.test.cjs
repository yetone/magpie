// Run with Node's test runner and Playwright on the module path; see README.md.
// The model id a request went out to the vendor under, when it is another
// than the model's (daoche_wuzui on X: was it routed to a model with a
// suffix?): Antigravity's plugin sends gemini-3.1-pro at high as
// gemini-3.1-pro-high. The Usage page's Requests say that id in the Sent
// column and in a request's details, the model's own on hover; the Routing
// page's story of the try says it went out under it. A request that went out
// as its model says nothing more. No left-border accent; every language; the
// API is faked.
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
const key = { id: "antigravity", provider: "antigravity", name: "Antigravity", kind: "provider", model: "gemini-3.1-pro" };
// newest first: one sent as the variant, then ones sent as the model
const wires = ["gemini-3.1-pro-high", "", "", ""];
const routes = wires.map((w, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "antigravity/gemini-3.1-pro", provider: "antigravity",
  order: [key], tries: [{ id: key.id, model: "gemini-3.1-pro", effort: "high", start: at(i), done: true, status: 200, ms: 900, served: "gemini-3.1-pro", ...(w ? { wire: w } : {}) }],
  done: true, status: 200, ms: 900, tokens: 1200, served: "gemini-3.1-pro",
}));
const ledRow = (i, w) => ({
  t: at(i), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "antigravity", providerName: "Antigravity",
  req: "antigravity/gemini-3.1-pro", model: "gemini-3.1-pro", served: "gemini-3.1-pro", effort: "high", in: 300, out: 40, ms: 2000, status: 200, rid: "r-" + i, ep: "/v1/chat/completions",
  ...(w ? { wire: w } : {}),
});
const ROWS = [ledRow(0, "gemini-3.1-pro-high"), ledRow(1, "")];

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
  en: { story: "Sent to the vendor as gemini-3.1-pro-high", dt: "Sent" },
  zh: { story: "以 gemini-3.1-pro-high 发给厂商", dt: "发送模型" },
  "zh-TW": { story: "以 gemini-3.1-pro-high 發給廠商", dt: "傳送模型" },
  ja: { story: "ベンダーには gemini-3.1-pro-high として送信", dt: "送信したモデル" },
  de: { story: "Beim Anbieter als gemini-3.1-pro-high gesendet", dt: "Gesendet" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: the id a request went out under is named`, async (t) => {
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
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sent-model.png`), fullPage: true });
        }
        await browser.close();
      });

      // Routing: the try's story says the id it went out under
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(wires.length - 1).waitFor();
      await page.locator(".rt-req").nth(0).click();
      const said = page.locator(".rt-steps li.wire-said");
      await said.waitFor();
      assert.equal(await said.count(), 1);
      assert.equal((await said.textContent()).trim(), want[lang].story);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sent-model-routing.png`) });
      await page.locator(".rt-req").nth(1).click();
      await page.waitForTimeout(300);
      assert.equal(await page.locator(".rt-steps li.wire-said").count(), 0);

      // Usage's Requests: the Sent column and a request's details
      await page.locator('[data-view="usage"]').first().click();
      await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      const rows = page.locator(".led tbody tr.led-row");
      await rows.nth(1).waitFor();
      const cells = await rows.evaluateAll((rs) => rs.map((r) => {
        const c = r.querySelector("td.model.model-value");
        return [c?.textContent || "", c?.title || ""];
      }));
      assert.deepEqual(cells, [["gemini-3.1-pro-high", "gemini-3.1-pro → gemini-3.1-pro-high"], ["gemini-3.1-pro", "gemini-3.1-pro"]]);
      const sent = async (i) => {
        await reader.inView(page, rows.nth(i));
        const box = await reader.openRequest(page, rows.nth(i));
        const got = await box.evaluate((b, dt) => {
          const dd = [...b.querySelectorAll("dt")].find((d) => d.textContent === dt)?.nextElementSibling;
          return dd ? [dd.textContent, dd.title, getComputedStyle(dd).borderLeftWidth] : null;
        }, want[lang].dt);
        await reader.closeRequest(page);
        return got;
      };
      assert.deepEqual(await sent(1), ["gemini-3.1-pro", "", "0px"]);
      assert.deepEqual(await sent(0), ["gemini-3.1-pro-high", "gemini-3.1-pro → gemini-3.1-pro-high", "0px"]);
      const border = await page.evaluate(() => [...document.querySelectorAll(".rt-steps li, .led-detail dd")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      for (const l of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate((l) => ["Sent to the vendor as {model}", "Sent"].filter((k) => !I18N[l][k]), l);
        assert.deepEqual(missing, [], `every string has its ${l}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
