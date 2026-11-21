// Run with Node's test runner and Playwright on the module path; see README.md.
// An effort slider's stops go lowest first, and each stop's name, the end
// labels and the value posted agree. A value the field holds but no longer
// offers sits in its place among the levels: Claude Code's subagents at max
// while their model offers only high read max in the middle and high at the
// right end, so the middle stop said 最高 and the top one 高
// (mathfournity on X). Options an agent lists out of order (max, low, high)
// are put in order too. The Subagents row and a tier's effort (the same
// picker), in Chromium and WebKit, in every language. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const models = [{ value: "magpie/v/glm", label: "GLM", ref: "v/glm" }, { value: "magpie/v/flash", label: "Flash", ref: "v/flash" }];
const lv = (...v) => v.map((value) => ({ value }));
const tiers = ["opus", "sonnet", "haiku", "fable"];
const claude = () => ({
  id: "claude", name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/v/glm", options: models },
    { key: "effort", label: "effort", value: "high", options: lv("low", "medium", "high") },
    ...tiers.map((tier) => ({ key: tier, label: tier, value: tier === "opus" ? "magpie/v/glm" : "", options: models })),
    // opus's levels as an agent might list them, out of order
    ...tiers.map((tier) => ({ key: tier + "_effort", label: tier + " effort", value: "", options: tier === "opus" ? lv("max", "low", "high") : lv("low", "high") })),
    { key: "subagent", label: "subagents", value: "magpie/v/flash", options: models },
    // set at max while another model was picked; Flash takes high only
    { key: "subagent_effort", label: "subagent effort", value: "max", options: lv("high") },
  ],
});

function server(lang, sets) {
  let cur = { agents: [claude()], profiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "dark" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents[0].fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "dark" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: { default: "default", low: "low", high: "high", max: "max" },
  zh: { default: "默认", low: "低", high: "高", max: "最高" },
  "zh-TW": { default: "預設", low: "低", high: "高", max: "最高" },
  ja: { default: "デフォルト", low: "低", high: "高", max: "最大" },
  de: { default: "Standard", low: "niedrig", high: "hoch", max: "maximal" },
};
const cc = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    test(`${engine} ${lang}: effort stops go lowest first and their names agree`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-effort-stop-order.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(cc).waitFor();
      await page.locator(`${cc} .ag-link`).click();
      await page.locator(`${cc} .ag-exp`).waitFor();

      // each stop's name as the thumb reaches it, and the ends'
      const stops = async (key) => {
        await page.locator(`${cc} .ag-exp .field.ag-eff[data-key="${key}"]`).click();
        await page.locator("#effortControl:not([hidden])").waitFor();
        const n = await page.locator("#effortTicks i").count();
        const names = [];
        for (let i = 0; i < n; i++) {
          await page.locator("#effortRange").evaluate((r, i) => { r.value = String(i); r.dispatchEvent(new Event("input", { bubbles: true })); }, i);
          names.push(await page.locator("#effortValue").textContent());
        }
        return { names, min: await page.locator("#effortMin").textContent(), max: await page.locator("#effortMax").textContent() };
      };
      const pick = async (i, n = sets.length) => {
        await page.locator("#effortRange").evaluate((r, i) => {
          r.value = String(i);
          r.dispatchEvent(new Event("input", { bubbles: true }));
          r.dispatchEvent(new Event("change", { bubbles: true }));
        }, i);
        // the post, as the fake backend records it
        for (const end = Date.now() + 5000; sets.length < n + 1 && Date.now() < end;) await page.waitForTimeout(20);
      };

      // the subagents: max, held but not offered, above high
      const sub = await stops("subagent_effort");
      assert.deepEqual(sub, { names: [w.default, w.high, w.max], min: w.default, max: w.max });
      // the thumb starts on the value held, the top stop
      assert.equal(await page.locator("#effortRange").inputValue(), "2");
      await pick(1);
      assert.deepEqual(sets, [{ agent: "claude", field: "subagent_effort", value: "high" }]);
      await page.keyboard.press("Escape");
      await page.locator("#pop").waitFor({ state: "hidden" });

      // a tier's levels listed max, low, high go low, high, max
      const opus = await stops("opus_effort");
      assert.deepEqual(opus, { names: [w.default, w.low, w.high, w.max], min: w.default, max: w.max });
      await pick(3);
      assert.deepEqual(sets.at(-1), { agent: "claude", field: "opus_effort", value: "max" });
      assert.deepEqual(errors, []);
    });
  }
}
