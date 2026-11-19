// Run with Node's test runner and Playwright on the module path; see README.md.
// An Azure key that answered before and is refused 401 — its subscription
// disabled for its credit spent (canlee886 on X) — rests suspended, asked
// again in the background and back as soon as it answers
// (gateway/azure_suspended.go). Its rest says the vendor suspended it, not
// that it failed, in the provider's key list and in the Routing page's words
// for the try; a key 401 from its first request still reads "failed", with
// no word of the background. In every language, at a wide and a narrow
// window, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_AZURE_SUSPENDED_ASSETS || path.join(__dirname, "../assets"));
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const start = new Date(now.getTime() - 60e3).toISOString();
const until = new Date(now.getTime() + 24 * 3600e3).toISOString();
const soon = new Date(now.getTime() + 4 * 60e3).toISOString();
const a = { id: "az#bbbbbbbbbb", provider: "az", name: "Azure", kind: "key", who: "Team", model: "big" };
const b = { id: "ww2", provider: "ww2", name: "WW2", kind: "provider", model: "big" };
const rest = { why: "suspended", status: 401, until, by: "suspended", failures: 1, key: "az#bbbbbbbbbb", probed: true };
const routes = [{
  id: 100, seq: 100, time: start, agent: "codex", model: "group/g", provider: "az",
  order: [a, b],
  tries: [
    { id: a.id, model: a.model, start, done: true, status: 401, ms: 300, fail: "suspended", rest, error: "Azure: Access denied due to invalid subscription key or wrong API endpoint." },
    { id: b.id, model: b.model, start, done: true, status: 200, ms: 900 },
  ],
  done: true, status: 200, ms: 1300,
}];
const azure = () => ({
  id: "az", name: "Azure", icon: "azure-color", preset: "azure", chat: "https://res.openai.azure.com/openai/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "New", masked: "sk-…one", on: true, active: true, rest: { why: "other", status: 401, until: soon, by: "backoff", key: "az#aaaaaaaaaa" } },
    { id: "bbbbbbbbbb", name: "Student", masked: "sk-…two", on: true, rest: { why: "suspended", status: 401, until, by: "suspended", key: "az#bbbbbbbbbb", probed: true } },
  ],
});

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [azure()], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const probed = {
  en: "checked in the background, back as soon as it answers",
  zh: "后台自动检测，恢复即用",
  "zh-TW": "背景自動偵測，恢復即用",
  ja: "バックグラウンドで自動確認し、応答すれば復帰",
  de: "wird im Hintergrund geprüft, zurück sobald erreichbar",
};
const suspended = { en: "suspended by the vendor", zh: "被服务商停用", "zh-TW": "被服務商停用", ja: "ベンダーが停止中", de: "vom Anbieter ausgesetzt" };
const failed = { en: "failed", zh: "失败", "zh-TW": "失敗", ja: "失敗", de: "fehlgeschlagen" };
const likely = {
  en: "the vendor has most likely suspended it",
  zh: "多半是服务商停用了它",
  "zh-TW": "多半是服務商停用了它",
  ja: "ベンダーが停止した可能性が高いです",
  de: "hat der Anbieter ihn wohl ausgesetzt",
};

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(probed)) {
    for (const width of [1100, 420]) {
      const open = async (t, name, view) => {
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
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=" + view);
        return { page, errors };
      };

      test(`${engine} ${lang} ${width}px: a suspended key says the vendor suspended it, a bad key that it failed`, async (t) => {
        const { page, errors } = await open(t, "keys-suspended", "providers");
        await page.locator(".row.provider", { hasText: "Azure" }).first().click();
        const student = page.locator('.acc[data-account-id="bbbbbbbbbb"] .key-rest');
        const fresh = page.locator('.acc[data-account-id="aaaaaaaaaa"] .key-rest');
        await student.waitFor();
        await fresh.waitFor();
        const said = await student.textContent(), bad = await fresh.textContent();
        assert(said.includes("401 " + suspended[lang]) && said.endsWith(" · " + probed[lang]), `the suspended key says: ${said}`);
        assert(bad.includes("401 " + failed[lang]) && !bad.includes(suspended[lang]) && !bad.includes(probed[lang]), `the bad key says: ${bad}`);
        assert(!(await sideways(page)), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: Routing's words for the try say the vendor suspended it`, async (t) => {
        const { page, errors } = await open(t, "routing-suspended", "routing");
        await page.locator(".rt-days .rt-day").nth(1).click();
        await page.locator(".rt-req").first().waitFor();
        await page.locator(".rt-req").first().click();
        await page.locator(".rt-steps li").first().waitFor();
        const steps = (await page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => l.textContent.replace(/\s+/g, " ")))).join("\n");
        assert(steps.includes(suspended[lang]) && steps.includes(likely[lang]) && steps.includes(probed[lang]), `the steps say:\n${steps}`);
        assert(!(await sideways(page)), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
