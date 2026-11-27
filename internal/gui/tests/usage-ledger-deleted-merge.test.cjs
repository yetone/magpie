// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage › Requests with a deleted provider picked in the Provider filter
// (CyberCaius on Discord): a "…" beside it merges its requests into a
// provider there is now, or names it, each in one step; with the merged
// provider picked, the same "…" parts them again. The page asks the
// gateway (POST /api/usage/deleted) and never rewrites a row itself. A
// provider that is not deleted and has none merged has no "…". Every
// language, both engines, a wide and a narrow window; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const hour = new Date(Math.floor(now / H) * H).toISOString();
const GONE = new Date(now - 5 * 86400e3).toISOString();
const base = { agent: "claude", agentName: "Claude Code", icon: "claudecode-color", req: "m", model: "m", in: 1000, out: 100, cache_read: 0, cache_write: 0, ms: 2000, status: 200, ep: "/v1/messages", cost: 0.01, priced: true };

function ledger(q, s) {
  const named = s.name || "relay";
  const rows = [
    { ...base, t: new Date(now - 60e3).toISOString(), rid: "req_main", provider: "main", providerName: "Main" },
    { ...base, t: new Date(now - 120e3).toISOString(), rid: "req_relay", provider: s.into ? "main" : "relay", providerName: s.into ? "Main" : named },
  ].filter((r) => !q.get("provider") || r.provider === q.get("provider"));
  const providers = s.into
    ? [{ id: "main", name: "Main", icon: "generic", merged: [{ id: "relay", name: named }] }]
    : [{ id: "main", name: "Main", icon: "generic" }, { id: "relay", name: named, icon: "generic", deleted: GONE }];
  const part = { calls: rows.length, tokens: rows.length * 1100, cost: rows.length * 0.01 };
  return {
    period: q.get("period"), rows, offset: 0, total: rows.length, calls: rows.length, errors: 0,
    input: rows.length * 1000, output: rows.length * 100, cache_read: 0, cache_write: 0, cost: rows.length * 0.01, unpriced: 0,
    by: { model: [{ id: "m", name: "m", icon: "generic", calls: rows.length, input: 0, output: 0, cache_read: 0, cache_write: 0, cost: part.cost }] },
    bucket: "hour",
    series: [{ label: "", time: hour, calls: rows.length, input: rows.length * 1000, output: rows.length * 100, by: { model: { m: part } } }],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
    providers, callerKeys: [],
  };
}

function server(lang, s) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") { s.asked.push(url.searchParams); return json(ledger(url.searchParams, s)); }
    if (url.pathname === "/api/usage/deleted/into") return json([{ id: "main", name: "Main", icon: "generic" }]);
    if (url.pathname === "/api/usage/deleted") {
      assert.equal(req.method(), "POST");
      const b = JSON.parse(req.postData());
      s.posted.push(b);
      if ("into" in b) s.into = b.into;
      if ("name" in b) s.name = b.name.trim();
      return json({ ok: true });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { merge: "Merge into…", rename: "Rename…", separate: "Separate Old relay", merged: "Old relay's requests are listed under Main now" },
  zh: { merge: "合并到…", rename: "重命名…", separate: "分开 Old relay", merged: "Old relay 的请求现在列在 Main 下" },
  "zh-TW": { merge: "合併到…", rename: "重新命名…", separate: "分開 Old relay", merged: "Old relay 的請求現在列在 Main 下" },
  ja: { merge: "統合先…", rename: "名前を変更…", separate: "Old relay を分ける", merged: "Old relay のリクエストは Main の下に表示されます" },
  de: { merge: "Zusammenführen mit…", rename: "Umbenennen…", separate: "Old relay trennen", merged: "Die Anfragen von Old relay stehen jetzt unter Main" },
};

async function open(browser, lang, width) {
  const errors = [];
  const s = { asked: [], posted: [], into: "", name: "" };
  const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  const p = await ctx.newPage();
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  p.on("pageerror", (e) => errors.push(e.message));
  await p.route("**/*", server(lang, s));
  await p.goto("http://magpie.test/");
  await p.locator('[data-view="usage"]').first().click();
  await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
  await p.locator("#ledWrap .led tbody tr").first().waitFor();
  await p.waitForTimeout(300);
  return { ctx, p, errors, s };
}

const pickProvider = async (p, name) => {
  await reader.click(p, p.locator("#ledProvider"));
  await reader.click(p, p.locator(".pc-pop [role=option]").filter({ hasText: name }).first());
};
const rows = (p, n) => p.waitForFunction((n) => document.querySelectorAll("#ledWrap .led tbody tr.led-row").length === n, n);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a deleted provider merged, named and parted", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      for (const width of [1400, 760]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const { ctx, p, errors, s } = await open(browser, lang, width);
          t.after(() => ctx.close());
          const more = p.locator("#ledProviderMore");
          assert.equal(await more.isVisible(), false, "no … with every provider listed");
          await pickProvider(p, "Main");
          await rows(p, 1);
          assert.equal(await more.isVisible(), false, "no … beside a provider with nothing merged");

          // rename: the filter's button turns into the name, Escape keeps it
          await pickProvider(p, "relay");
          await p.waitForFunction(() => !document.querySelector("#ledProviderMore").hidden);
          await reader.click(p, more);
          const items = await p.locator(".sess-menu .pm-item").allTextContents();
          assert(items.some((x) => x.includes(w.merge)) && items.some((x) => x.includes(w.rename)), items.join(" | "));
          await reader.click(p, p.locator(".sess-menu .pm-item").filter({ hasText: w.rename }));
          const field = p.locator("#ledgerPane input.led-rename");
          await field.waitFor();
          assert.equal(await field.inputValue(), "relay");
          assert.equal(await p.locator("#ledProvider").isVisible(), false);
          await p.keyboard.press("Escape");
          await p.waitForFunction(() => !document.querySelector("input.led-rename"));
          assert.equal(s.posted.length, 0, "Escape sends nothing");
          await reader.click(p, more);
          await reader.click(p, p.locator(".sess-menu .pm-item").filter({ hasText: w.rename }));
          await field.fill("  Old relay ");
          await p.keyboard.press("Enter");
          await p.waitForFunction(() => document.querySelector("#ledProvider").textContent.includes("Old relay"));
          assert.deepEqual(s.posted.at(-1), { id: "relay", name: "  Old relay " });
          assert.equal(await p.locator("#ledProvider").isVisible(), true);

          // merge: a provider there is now picked from the same …
          await p.waitForFunction(() => !document.querySelector("#ledProviderMore").hidden);
          await reader.click(p, more);
          await reader.click(p, p.locator(".sess-menu .pm-item").filter({ hasText: w.merge }));
          // the providers to merge into: the provider combobox, a logo on each
          const into = p.locator(".pc-pop [role=option]");
          await into.first().waitFor();
          assert.equal(await into.locator(".ic").count(), await into.count());
          await reader.click(p, into.filter({ hasText: "Main" }));
          await p.waitForFunction(() => document.querySelector("#ledProvider").textContent.includes("Main"));
          assert.deepEqual(s.posted.at(-1), { id: "relay", into: "main" });
          assert.equal(s.asked.at(-1).get("provider"), "main", "the page moves to the provider merged into");
          await rows(p, 2);
          assert.equal((await p.locator("#status").textContent()).trim(), w.merged);

          // the merged provider's … parts them again
          await p.waitForFunction(() => !document.querySelector("#ledProviderMore").hidden);
          await reader.click(p, more);
          await reader.click(p, p.locator(".pc-pop [role=option]").filter({ hasText: w.separate }));
          await p.waitForFunction(() => document.querySelector("#ledProvider").textContent.includes("Old relay"));
          assert.deepEqual(s.posted.at(-1), { id: "relay", into: "" });
          assert.equal(s.asked.at(-1).get("provider"), "relay");
          await rows(p, 1);
          assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), true, "the page doesn't scroll sideways");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
