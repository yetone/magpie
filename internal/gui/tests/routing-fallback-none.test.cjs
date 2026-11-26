// Run with Node's test runner and Playwright on the module path; see README.md.
// jimlee0824v2 on X: Alma asked for OpenCode Zen Free's free DeepSeek, the
// one key answered 429 ("Free usage limit reached"), nothing else was there
// to try and Alma stopped. They asked for a group to go on to; that is the
// provider's Fallback, under Advanced, which they didn't find. The Routing
// page's story of such a request now says the provider has no fallback, and
// Set a fallback opens its editor at the Fallback, Advanced open, where a
// routing group can be picked. A request with a fallback in its order, or
// one for a routing group, says nothing of it. Every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
// the reporter's words, from their screenshot
const said = "OpenCode Zen Free: OpenCode Zen Free: Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go (out of its allowance: not asked again before 17:36 UTC+8)";
const zen = { id: "opencode-zen-free", provider: "opencode-zen-free", name: "OpenCode Zen Free", kind: "provider", model: "deepseek-v4.1-flash-free" };
const acme = { id: "acme", provider: "acme", name: "Acme", kind: "provider", model: "a1" };
const wb = { id: "wb", provider: "wb", name: "WorkBuddy", kind: "provider", model: "m1", fallback: true };
const failed = (w, i) => ({ id: w.id, model: w.model, start: at(i), done: true, status: 429, ms: 40, fail: "quota", error: said });
const routes = [
  // the reporter's: one key, 429, nobody left
  { id: 300, seq: 300, time: at(0), agent: "alma", model: "opencode-zen-free/deepseek-v4.1-flash-free", provider: "opencode-zen-free", order: [zen],
    tries: [failed(zen, 0)], done: true, status: 429, ms: 50, error: said },
  // a fallback in its order (resting, so not tried): one is set
  { id: 200, seq: 200, time: at(1), agent: "alma", model: "acme/a1", provider: "acme", order: [acme, wb],
    tries: [failed(acme, 1)], done: true, status: 429, ms: 90, error: said },
  // a routing group: its members are its own backups
  { id: 100, seq: 100, time: at(2), agent: "alma", model: "group/pair", provider: "acme", group: { id: "pair", name: "Pair", members: ["acme/a1"] }, order: [acme],
    tries: [failed(acme, 2)], done: true, status: 429, ms: 50, error: said },
];
const base = { chat: "https://zen.test/v1", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], key: { set: true }, ready: true };
const zenProvider = { ...base, id: "opencode-zen-free", name: "OpenCode Zen Free", icon: "generic", models: [{ id: "deepseek-v4.1-flash-free", name: "DeepSeek V4.1 Flash Free", on: true }] };
const acmeProvider = { ...base, id: "acme", name: "Acme", icon: "generic", models: [{ id: "a1", name: "A1", on: true }] };
const wbProvider = { ...base, id: "wb", name: "WorkBuddy", icon: "generic", models: [{ id: "m1", name: "WB Model 1", on: true }] };
const backup = { id: "group/backup", name: "Backup Lane", icons: ["generic"], providers: ["WorkBuddy"], members: ["wb/m1"], efforts: [] };

function serve(lang) {
  const state = { agents: [{ id: "alma", name: "Alma", path: "/test/alma.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [zenProvider, acmeProvider, wbProvider], presets: [], excluded: [], gateway: { running: true, window: true, groups: [backup] } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { none: "OpenCode Zen Free has no fallback, so nothing took over.", set: "Set a fallback" },
  zh: { none: "OpenCode Zen Free 没有设置备用，没有谁接手。", set: "设置备用" },
  "zh-TW": { none: "OpenCode Zen Free 沒有設定備用，沒有誰接手。", set: "設定備用" },
  ja: { none: "OpenCode Zen Free にはフォールバックが設定されていないため、引き継ぐものがありませんでした。", set: "フォールバックを設定" },
  de: { none: "Für OpenCode Zen Free ist kein Fallback eingestellt, also hat nichts übernommen.", set: "Fallback einstellen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a request that ended with no fallback says so, and opens the Fallback`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-fallback-none.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-days .rt-day").nth(1).click();
        const reqs = page.locator(".rt-req");
        await reqs.first().waitFor();
        const stepsOf = async (n) => {
          await reqs.nth(n).click();
          await page.locator(".rt-steps li").first().waitFor();
          await page.waitForFunction((w) => document.querySelector(".rt-steps li")?.textContent.includes(w), ["deepseek-v4.1-flash-free", "acme/a1", "Pair"][n]);
          return page.locator(".rt-steps li").allTextContents();
        };
        // a fallback that failed too, and a group: nothing said of a fallback
        for (const n of [1, 2]) {
          const steps = await stepsOf(n);
          assert.equal(await page.locator(".rt-steps li.fallback-none").count(), 0, JSON.stringify(steps));
        }
        // the reporter's: said after the vendor's words, with the button
        const steps = await stepsOf(0);
        const line = page.locator(".rt-steps li.fallback-none");
        assert.equal(await line.count(), 1, JSON.stringify(steps));
        const text = (await line.textContent()).trim();
        assert(text.startsWith(want[lang].none), text);
        const ix = steps.findIndex((s) => s.includes("Free usage limit reached"));
        assert(ix >= 0 && ix < steps.findIndex((s) => s.includes(want[lang].none)), JSON.stringify(steps));
        const btn = line.getByRole("button", { name: want[lang].set, exact: true });
        await btn.click();
        // the provider's editor, Advanced open, its Fallback's box focused,
        // the routing group there to pick
        const box = page.locator(".editor .fallback");
        await box.locator("input").waitFor();
        assert(await page.locator(".editor details.advanced").evaluate((d) => d.open), "Advanced open");
        await page.waitForFunction(() => document.activeElement === document.querySelector(".editor .fallback input"));
        const chip = box.locator(".mchips .mchip", { hasText: "Backup Lane" });
        await chip.waitFor();
        const inView = await box.locator("input").evaluate((e) => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; });
        assert(inView, "the Fallback's box in view");
        assert.deepEqual(errors, []);
      });
    }
  }
}
