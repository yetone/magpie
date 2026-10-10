// Recent calls send bodies only when expanded, independent of the S3 archive.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_RECENT_ASSETS || path.resolve(__dirname, "../assets");
// the three calls share their agent and model, and two their second
const at = new Date(Date.now() - 1000).toISOString();
const calls = [0, 1, 2].map((i) => ({ id: 30 - i, time: i ? at : new Date().toISOString(), agent: "codex", model: "fixture/m", from: "chat", to: "chat", status: i === 0 ? 400 : 200, error: i === 0 ? "vendor declined" : "", ms: 900 }));
const bodies = { requestBody: '{"messages":[{"role":"user","content":"hello"}]}', responseBody: '{"reply":"world"}', requestTruncated: true, responseTruncated: true };
const words = {
  en: { loading: "Fetching…", retry: "Try again" },
  zh: { loading: "取回中…", retry: "重试" },
  "zh-TW": { loading: "取回中…", retry: "重試" },
  ja: { loading: "取得中…", retry: "再試行" },
  de: { loading: "Wird abgerufen…", retry: "Erneut versuchen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: recent bodies are fetched once on expansion, with retry and eviction`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: lang === "en" ? 1100 : 560, height: 900 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const seen = [];
      let shown = calls;
      let release;
      const pending = new Promise((resolve) => { release = resolve; });
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const json = (data, status = 200) => route.fulfill({ json: data, status });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
        if (url.pathname === "/api/providers") return json({ presets: [], excluded: [], providers: [{ id: "fixture", name: "Fixture", icon: "generic", key: { set: true, masked: "fixture" }, models: [{ id: "m", name: "M", on: true }], agents: [] }], gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls: shown, groups: [] } });
        if (url.pathname === "/api/gateway/call") {
          const c = calls.find((c) => url.searchParams.get("id") === String(c.id));
          assert(c, "detail identifies the exact call");
          seen.push(c.id);
          if (c === calls[0]) { await pending; return json(bodies); }
          if (c === calls[2]) return json({ error: "This request is no longer in the recent calls." }, 404);
          if (seen.filter((id) => id === c.id).length === 1) return json({ error: "temporarily unavailable" }, 503);
          return json({ requestBody: "", responseBody: "" });
        }
        if (url.pathname === "/api/groups") return json({ groups: [] });
        if (url.pathname === "/api/plugins") return json({ plugins: [] });
        if (url.pathname === "/api/gateway/trace") {
          if (url.searchParams.has("wait")) await new Promise((r) => setTimeout(r, 1000));
          return json({ mine: true, now: calls[0].time, seq: 1, totals: {}, routes: [] });
        }
        if (url.pathname === "/api/archive") assert.fail("recent bodies must not depend on the archive");
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] });
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#providers .provider").first().waitFor();
      assert.equal(seen.length, 0, "Providers never fetches bodies");
      await page.locator('[data-view="gateway"]').first().click();
      const rows = page.locator("#activity .call-item");
      await rows.first().waitFor();
      await page.locator("#foldConnect").click();
      assert.equal(seen.length, 0, "the recent list is metadata only");
      await rows.first().locator(".call").click();
      await rows.first().getByText(words[lang].loading, { exact: true }).waitFor();
      assert.equal(seen.length, 1);
      assert.equal(await rows.first().locator(".call-body").count(), 0, "pending is not an empty body");
      await rows.first().locator(".call").press("Enter");
      await rows.first().locator(".call").press("Space");
      assert.equal(seen.length, 1, "reopening a pending call does not duplicate it");
      release();
      await rows.first().locator(".call-body pre").first().waitFor();
      assert((await rows.first().innerText()).includes("hello"));
      assert((await rows.first().innerText()).includes("world"));
      assert.equal(await rows.first().locator(".call-body-truncated").count(), 2);
      await page.evaluate(() => loadProviders());
      await rows.first().locator(".call-body pre").first().waitFor();
      assert.equal(seen.length, 1, "a summary refresh preserves loaded detail");
      await rows.first().locator(".call").click();
      await rows.first().locator(".call").click();
      assert.equal(seen.length, 1, "reopening a loaded body uses the in-memory copy");
      await rows.first().locator(".call").click();
      await rows.nth(1).locator(".call").click();
      await rows.nth(1).getByText("temporarily unavailable", { exact: true }).waitFor();
      await rows.nth(1).getByRole("button", { name: words[lang].retry, exact: true }).click();
      await rows.nth(1).locator(".call-body pre").first().waitFor();
      assert.equal(seen.length, 3);
      assert.equal(await rows.nth(1).locator(".call-body code.empty").count(), 2);
      await rows.nth(1).locator(".call").click();
      await rows.nth(2).locator(".call").click();
      const gone = await page.evaluate(() => t("This request is no longer in the recent calls."));
      await rows.nth(2).getByText(gone, { exact: true }).waitFor();
      assert.equal(await rows.nth(2).locator(".call-body").count(), 0, "an evicted call isn't an empty body");
      await rows.nth(2).locator(".call").click();
      shown = [calls[1]];
      await page.evaluate(() => loadProviders());
      shown = calls;
      await page.evaluate(() => loadProviders());
      await rows.first().locator(".call").click();
      await page.waitForFunction(() => document.querySelector("#activity .call-item .call-body pre"));
      assert.equal(seen.length, 5, "an evicted body's browser cache is discarded");
      assert.deepEqual(errors, []);
    });
  }
}
