// Run with Node's test runner and Playwright on the module path; see README.md.
// The one provider combobox (provcombo.js; the owner, 11-27: "最好是个
// Combo Box，另外要有供应商的Icon"), at three of the places it is used:
// - Routing › Settings › Leave out of found groups: a field of chips,
//   typed into to narrow its list, Enter ticking the row the arrows are
//   on, Backspace taking the last chip off; groups/skip posts the same
//   body as the menu before it, once, as the list closes;
// - Usage › requests › Provider: the filter's button shows the picked
//   provider's logo, its list has a search field, and typing narrows it;
//   the ledger is asked with the same provider= as before;
// - Usage › a deleted provider › Merge into…: the providers there are now,
//   each with its logo; POST /api/usage/deleted gets the same body.
// Every row and every chip has a logo, nothing runs past its row or the
// window, in every language, Chromium and WebKit, at 1180 and 420px.
// The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

const file = async (route, pathname) => {
  const f = path.join(assets, pathname === "/" ? "index.html" : pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
  try { return await route.fulfill({ body: await fs.readFile(f), contentType }); } catch { return route.fulfill({ status: 404, body: "" }); }
};
const common = (lang, url, json, route) => {
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  return null;
};

// Routing: three providers to leave out, one with a mask logo
const skippable = [{ id: "openai", name: "OpenAI", icon: "openai" }, { id: "trae-cn", name: "Trae CN", icon: "trae" }, { id: "deepseek", name: "DeepSeek", icon: "deepseek-color" }];
const groups = (skip) => {
  const members = skippable.map((p) => p.id + "/m").filter((id) => !skip.includes(id.split("/")[0]));
  return { models: skippable.map((p) => ({ id: p.id + "/m", name: "m", providerName: p.name, icon: p.icon })), pools: [], deciders: [], found: true, skip, skippable,
    groups: [{ id: "auto-m", name: "Model M", members, auto: true, ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }] };
};
function routing(lang, s) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const done = common(lang, url, json, route);
    if (done) return done;
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(s.skip));
    if (url.pathname === "/api/groups/skip") {
      const b = JSON.parse(route.request().postData() || "{}");
      s.posted.push(b);
      s.skip = b.skip;
      return json(groups(s.skip));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    return file(route, url.pathname);
  };
}

// Usage: the ledger's providers, one deleted
const GONE = new Date(now - 5 * 86400e3).toISOString();
const base = { agent: "claude", agentName: "Claude Code", icon: "claudecode-color", req: "m", model: "m", in: 1000, out: 100, cache_read: 0, cache_write: 0, ms: 2000, status: 200, ep: "/v1/messages", cost: 0.01, priced: true };
function ledger(q, s) {
  const all = [
    { ...base, t: new Date(now - 60e3).toISOString(), rid: "r1", provider: "openai", providerName: "OpenAI" },
    { ...base, t: new Date(now - 90e3).toISOString(), rid: "r2", provider: "deepseek", providerName: "DeepSeek" },
    { ...base, t: new Date(now - 120e3).toISOString(), rid: "r3", provider: s.into ? "openai" : "relay", providerName: s.into ? "OpenAI" : "relay" },
  ];
  const rows = all.filter((r) => !q.get("provider") || r.provider === q.get("provider"));
  const providers = [{ id: "openai", name: "OpenAI", icon: "openai" }, { id: "deepseek", name: "DeepSeek", icon: "deepseek-color" }, { id: "kimi", name: "Kimi", icon: "kimi" }];
  if (!s.into) providers.push({ id: "relay", name: "relay", icon: "generic", deleted: GONE });
  const part = { calls: rows.length, tokens: rows.length * 1100, cost: rows.length * 0.01 };
  return {
    period: q.get("period"), rows, offset: 0, total: rows.length, calls: rows.length, errors: 0,
    input: rows.length * 1000, output: rows.length * 100, cache_read: 0, cache_write: 0, cost: rows.length * 0.01, unpriced: 0,
    by: { model: [{ id: "m", name: "m", icon: "generic", calls: rows.length, input: 0, output: 0, cache_read: 0, cache_write: 0, cost: part.cost }] },
    bucket: "hour",
    series: [{ label: "", time: new Date(Math.floor(now / H) * H).toISOString(), calls: rows.length, input: rows.length * 1000, output: rows.length * 100, by: { model: { m: part } } }],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
    providers, callerKeys: [],
  };
}
function usage(lang, s) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const done = common(lang, url, json, route);
    if (done) return done;
    if (url.pathname === "/api/usage/requests") { s.asked.push(url.searchParams); return json(ledger(url.searchParams, s)); }
    if (url.pathname === "/api/usage/deleted/into") return json([{ id: "openai", name: "OpenAI", icon: "openai" }, { id: "deepseek", name: "DeepSeek", icon: "deepseek-color" }]);
    if (url.pathname === "/api/usage/deleted") {
      const b = JSON.parse(req.postData());
      s.posted.push(b);
      if ("into" in b) s.into = b.into;
      return json({ ok: true });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    return file(route, url.pathname);
  };
}

// every option and chip in sight has its logo, and the list is in the window
async function logos(p, width) {
  const out = await p.evaluate((width) => {
    const bad = [];
    for (const r of document.querySelectorAll(".pc-pop [role=option], .pc-chip, .pc-trig.set")) if (!r.querySelector(".ic")) bad.push("no logo: " + r.textContent);
    for (const b of document.querySelectorAll(".pc-pop")) {
      const r = b.getBoundingClientRect();
      if (r.left < 0 || r.right > width + 0.5) bad.push("list past the window: " + r.left + "–" + r.right);
    }
    return bad;
  }, width);
  assert.deepEqual(out, []);
}
const names = (p) => p.locator(".pc-pop [role=option] .v").allTextContents();
const waitFor = async (p, f, what) => { for (let i = 0; i < 150 && !f(); i++) await p.waitForTimeout(20); assert.ok(f(), what); };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": providers are picked in the provider combobox", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const width of [1180, 420]) {
        await t.test(`${lang} ${width}px`, async () => {
          // Routing › Leave out of found groups
          {
            const s = { skip: [], posted: [] }, errors = [];
            const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.setDefaultNavigationTimeout(60000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", routing(lang, s));
            await p.goto("http://magpie.test/?view=groups");
            const field = p.locator(".rt-gfound .rt-skip-pick.pc-field");
            await field.waitFor();
            const input = field.locator("input");
            assert.equal(await input.getAttribute("role"), "combobox");
            assert.equal(await input.getAttribute("placeholder"), await p.evaluate(() => t("Leave out a provider…")));
            await reader.click(p, field);
            const list = p.locator(".pc-pop [role=listbox]");
            await list.waitFor();
            assert.equal(await input.getAttribute("aria-controls"), await list.getAttribute("id"));
            assert.equal(await list.getAttribute("aria-multiselectable"), "true");
            assert.deepEqual(await names(p), ["OpenAI", "Trae CN", "DeepSeek"]);
            await logos(p, width);
            // typed: narrowed, the arrows and Enter tick
            await p.keyboard.type("deep");
            assert.deepEqual(await names(p), ["DeepSeek"]);
            assert.equal(await input.getAttribute("aria-activedescendant"), await p.locator(".pc-pop [role=option]").first().getAttribute("id"));
            await p.keyboard.press("Enter");
            await p.keyboard.type("tr");
            await p.keyboard.press("Enter");
            assert.equal(await input.inputValue(), "", "a pick clears what was typed");
            await p.keyboard.press("ArrowDown"); // OpenAI → Trae CN
            await p.keyboard.press("Enter"); // Trae CN ticked off again
            assert.deepEqual(await field.locator(".pc-chip").allTextContents(), ["DeepSeek"]);
            await p.keyboard.press("ArrowUp"); // back on OpenAI
            await p.keyboard.press("Enter");
            assert.deepEqual(await field.locator(".pc-chip").allTextContents(), ["DeepSeek", "OpenAI"]);
            await p.keyboard.press("Backspace"); // the last chip off
            assert.deepEqual(await field.locator(".pc-chip").allTextContents(), ["DeepSeek"]);
            await logos(p, width);
            assert.deepEqual(s.posted, [], "nothing saved while the list is open");
            await p.keyboard.press("Escape");
            await list.waitFor({ state: "detached" });
            await waitFor(p, () => s.posted.length, "saved as the list closed");
            assert.deepEqual(s.posted, [{ skip: ["deepseek"] }]);
            await p.waitForFunction(() => !document.querySelector(".rt-group .mem")?.textContent.includes("DeepSeek"));
            assert.equal(await field.locator(".pc-chip .ic").count(), 1);
            const fits = await p.evaluate(() => {
              const row = document.querySelector(".rt-gfound"), f = document.querySelector(".rt-skip-pick").getBoundingClientRect();
              return row.scrollWidth <= row.clientWidth + 1 && f.right <= row.getBoundingClientRect().right + 0.5 && f.left >= 0;
            });
            assert.equal(fits, true, "the field fits its row");
            // a chip's × with the list closed saves at once
            await reader.click(p, field.locator(".pc-chip .pc-x"));
            await waitFor(p, () => s.posted.length === 2, "the × saved");
            assert.deepEqual(s.posted[1], { skip: [] });
            assert.deepEqual(errors, []);
            await ctx.close();
          }

          // Usage › requests › Provider, and Merge into…
          {
            const s = { asked: [], posted: [], into: "" }, errors = [];
            const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.setDefaultNavigationTimeout(60000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", usage(lang, s));
            await p.goto("http://magpie.test/?view=usage");
            await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
            await p.locator("#ledWrap .led tbody tr").first().waitFor();
            await p.waitForTimeout(300);
            const all = await p.evaluate(() => t("All providers"));
            const btn = p.locator("#ledProvider");
            assert.equal((await btn.textContent()).trim(), all);
            assert.equal(await btn.getAttribute("aria-haspopup"), "listbox");
            await reader.click(p, btn);
            const q = p.locator(".pc-pop.pc-search input[role=combobox]");
            await q.waitFor();
            assert.equal(await p.evaluate(() => document.activeElement?.matches(".pc-pop input")), true, "the search field takes the keys");
            assert.equal(await q.getAttribute("placeholder"), await p.evaluate(() => t("Search providers…")));
            assert.deepEqual(await names(p), [all, "OpenAI", "DeepSeek", "Kimi", "relay"]);
            await logos(p, width);
            await p.keyboard.type("seek");
            assert.deepEqual(await names(p), ["DeepSeek"]);
            await p.keyboard.press("Enter");
            await p.locator(".pc-pop").waitFor({ state: "detached" });
            await p.waitForFunction(() => document.querySelector("#ledProvider")?.textContent.includes("DeepSeek"));
            await waitFor(p, () => s.asked.at(-1)?.get("provider") === "deepseek", "the ledger asked for provider=deepseek");
            assert.equal(s.asked.at(-1).get("offset"), "0");
            await logos(p, width);

            // a deleted one, merged into a provider there is now
            await reader.click(p, btn);
            await reader.click(p, p.locator(".pc-pop [role=option]").filter({ hasText: "relay" }));
            await waitFor(p, () => s.asked.at(-1)?.get("provider") === "relay", "the ledger asked for provider=relay");
            await p.waitForFunction(() => !document.querySelector("#ledProviderMore").hidden);
            await reader.click(p, p.locator("#ledProviderMore"));
            await reader.click(p, p.locator(".sess-menu .pm-item").filter({ hasText: await p.evaluate(() => t("Merge into…")) }));
            await p.locator(".pc-pop [role=listbox]").waitFor();
            assert.equal(await p.locator(".pc-pop [role=listbox]").getAttribute("aria-label"), await p.evaluate(() => t("Merge into")));
            assert.deepEqual(await names(p), ["OpenAI", "DeepSeek"]);
            await logos(p, width);
            await reader.click(p, p.locator(".pc-pop [role=option]").filter({ hasText: "OpenAI" }));
            await waitFor(p, () => s.posted.length === 1, "merged");
            assert.deepEqual(s.posted, [{ id: "relay", into: "openai" }]);
            await waitFor(p, () => s.asked.at(-1)?.get("provider") === "openai", "the page moved to the provider merged into");
            assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), true, "the page doesn't scroll sideways");
            assert.deepEqual(errors, []);
            await ctx.close();
          }
        });
      }
    }
  });
}
