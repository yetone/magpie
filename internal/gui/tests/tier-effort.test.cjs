// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's tiers can each run at an effort of their own (#536), as its
// subagents can. In the window, connected, a tier's effort sits beside its
// model in the opened row's Tiers: unset it reads "follows session" and its
// title says the tier runs at the effort Claude Code asks for; set, its title
// says the session's effort and /effort don't change it, and New sessions'
// effort says a tier with its own runs at that one (@lightwlh on X). A click opens the effort slider with Default as its
// first stop, and a level picked is posted and named in its title. (The
// tray panel keeps them as entries under the models in the tiers' square.)
// The subagents' effort is on the Subagents row, as a tier's is. Before
// Claude Code goes through magpie there are no levels: no effort entries
// and no square. No click scrolls the page.
// In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "magpie/v/glm", label: "GLM", ref: "v/glm" }, { value: "magpie/v/flash", label: "Flash", ref: "v/flash" }];
const levels = ["low", "medium", "high"].map((value) => ({ value }));
const tiers = ["opus", "sonnet", "haiku", "fable"];
const claude = (id, routed) => ({
  id, name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color", wired: routed,
  fields: [
    { key: "model", label: "model", value: routed ? "magpie/v/glm" : "opus", options: routed ? models : [{ value: "opus" }] },
    { key: "effort", label: "effort", value: "high", options: levels },
    ...tiers.map((tier) => ({ key: tier, label: tier, value: tier === "haiku" && routed ? "magpie/v/flash" : "", options: models })),
    ...tiers.map((tier) => ({ key: tier + "_effort", label: tier + " effort", value: "", options: routed ? levels : [] })),
    { key: "subagent", label: "subagents", value: "", options: routed ? models : [] },
    { key: "subagent_effort", label: "subagent effort", value: "", options: routed ? levels : [] },
  ],
});
const fresh = () => ({ agents: [claude("claude", true), claude("cc-own", false)], profiles: [] });

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
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
  en: {
    asks: "the effort Claude Code asks for", effort: (tier) => tier + " effort", title: "haiku effort", def: "default", low: "low", high: "high",
    follows: "follows session", wins: "Used whatever the session's effort or /effort is", own: "A tier with an effort of its own runs at that one",
    sep: (l, v) => `${l}: ${v}`,
  },
  zh: {
    asks: "跟随 Claude Code 请求的推理强度", effort: (tier) => tier + " 推理强度", title: "haiku 推理强度", def: "默认", low: "低", high: "高",
    follows: "跟随会话", wins: "不受会话推理强度和 /effort 影响", own: "设了自己推理强度的档位按它自己的运行",
    sep: (l, v) => `${l}：${v}`,
  },
};

const cc = '.row.agent[data-id="claude"]';
// a field's title as fieldBtn words it
const t2 = (w, label, value) => w.sep(label, value);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a Claude Code tier's effort is picked beside its model`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 700 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-tier-effort.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(cc).waitFor();
      await page.locator(`${cc} .ag-link`).click();
      await page.locator(`${cc} .ag-exp`).waitFor();

      // no tier's effort is a field of the row's own, nor the subagents': theirs
      // is on the Subagents row (claude-subagent-effort-row.test.cjs)
      assert.deepEqual(await page.locator(`${cc} .field:not(.ag-eff)[data-key$="_effort"]`).evaluateAll((es) => es.map((e) => e.dataset.key)), []);
      assert.equal(await page.locator(`${cc} .field.extra[data-key="subagent_effort"]`).count(), 0);
      // not through magpie: no levels, no square, no effort entries
      assert.equal(await page.locator('.row.agent[data-id="cc-own"] .field[data-key$="_effort"]').count(), 0);

      // each tier's row in the opened row: its model, then its effort
      const effs = page.locator(`${cc} .ag-exp .field.ag-eff`);
      assert.deepEqual(await effs.evaluateAll((es) => es.map((e) => e.dataset.key)), [...tiers.map((tier) => tier + "_effort"), "subagent_effort"]);
      for (const tier of tiers) {
        const b = page.locator(`${cc} .ag-exp .field.ag-eff[data-key="${tier}_effort"]`);
        assert.equal(await b.getAttribute("aria-label"), t2(w, w.effort(tier), w.asks));
        assert.equal(await b.locator(".v").textContent(), w.follows);
      }
      // no tier has an effort of its own yet: the session's says nothing of them
      const session = page.locator(`${cc} .field[data-key="effort"]`);
      assert.ok(!(await session.getAttribute("title")).includes(w.own));
      assert.equal(await effs.locator(".effort-ic").count(), 5, "each effort shows the bars");
      const haiku = page.locator(`${cc} .ag-exp .field.ag-eff[data-key="haiku_effort"]`);
      const y = await page.evaluate(() => scrollY);

      // haiku's effort: the slider, Default first, then its levels
      await haiku.click();
      await page.locator("#effortControl:not([hidden])").waitFor();
      assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
      assert.equal(await page.locator("#effortTitle").textContent(), w.title);
      assert.equal(await page.locator("#effortValue").textContent(), w.def);
      assert.equal(await page.locator("#effortTicks i").count(), 4);
      await page.locator("#effortRange").evaluate((r) => {
        r.value = "1";
        r.dispatchEvent(new Event("input", { bubbles: true }));
        r.dispatchEvent(new Event("change", { bubbles: true }));
      });
      assert.equal(await page.locator("#effortValue").textContent(), w.low);
      await page.waitForFunction(() => document.querySelector("#status")?.textContent);
      assert.deepEqual(sets, [{ agent: "claude", field: "haiku_effort", value: "low" }]);
      assert.equal(await haiku.getAttribute("aria-label"), t2(w, w.title, w.low) + "\n" + w.wins);
      assert.equal(await haiku.locator(".v").textContent(), w.low);
      // drawn again from the state: the same words, and the session's effort
      // now says a tier with its own runs at that one
      await page.waitForFunction((own) => document.querySelector('.row.agent[data-id="claude"] .field[data-key="effort"]')?.title.includes(own), w.own);
      assert.equal(await haiku.getAttribute("aria-label"), t2(w, w.title, w.low) + "\n" + w.wins);
      assert.equal(await page.locator(`${cc} .ag-exp .field.ag-eff[data-key="opus_effort"] .v`).textContent(), w.follows);
      assert.equal(await page.evaluate(() => scrollY), y, "a pick scrolled the page");
      // the session's effort is untouched
      assert.equal(await page.locator(`${cc} .field[data-key="effort"] .v`).textContent(), w.high);
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Claude Code tier pickers keep one width in a narrow window`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 560, height: 700 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, []));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/");
      await page.locator(cc).waitFor();
      await page.locator(`${cc} .ag-link`).click();
      await page.locator(`${cc} .ag-exp`).waitFor();

      const picks = page.locator(`${cc} .ag-exp .field.ag-pick`);
      assert.equal(await picks.count(), tiers.length + 1);
      const widths = await picks.evaluateAll((es) => es.map((e) => e.getBoundingClientRect().width));
      assert.ok(widths.every((width) => Math.abs(width - widths[0]) < 0.5), `picker widths: ${widths.join(", ")}`);
      assert.equal(Math.round(widths[0]), 220);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "narrow Agents view must not overflow horizontally");
      // "follows session", in every language, stays on its tier's line
      const lines = await page.locator(`${cc} .ag-exp .field.ag-eff`).evaluateAll((es) => es.map((e) => {
        const p = e.parentElement.querySelector(".field.ag-pick").getBoundingClientRect(), r = e.getBoundingClientRect();
        return Math.abs(r.top - p.top) < 2 && r.right <= innerWidth;
      }));
      assert.deepEqual(lines, lines.map(() => true));
      assert.deepEqual(errors, []);
    });
  }
}
