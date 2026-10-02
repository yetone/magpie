// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's tiers can each run at an effort of their own (#536), as its
// subagents can. A tier's effort is an entry under its model in the tiers'
// square, not a picker of its own: unset it says the tier runs at the effort
// Claude Code asks for, a click opens the effort slider with Default as its
// first stop, and a level picked is posted, lights the square and is named in
// its title. The subagents' effort is a square, as Codex's is, that says what
// unset means for Claude Code. Before Claude Code goes through magpie there
// are no levels: no effort entries and no square. No click scrolls the page.
// In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "magpie/v/glm", label: "GLM" }, { value: "magpie/v/flash", label: "Flash" }];
const levels = ["low", "medium", "high"].map((value) => ({ value }));
const tiers = ["opus", "sonnet", "haiku", "fable"];
const claude = (id, routed) => ({
  id, name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color",
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
    sub: "subagent effort: default\nthe effort Claude Code asks for", summary: "tiers: haiku (low)",
  },
  zh: {
    asks: "跟随 Claude Code 请求的推理强度", effort: (tier) => tier + " 推理强度", title: "haiku 推理强度", def: "默认", low: "低", high: "高",
    sub: "子 agent 推理强度：默认\n跟随 Claude Code 请求的推理强度", summary: "分档：haiku (低)",
  },
};

const cc = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a Claude Code tier's effort is picked from the tiers' square`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 700 } });
      page.setDefaultTimeout(5000);
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

      // no tier's effort is a field of its own; the subagents' is a square
      assert.deepEqual(await page.locator(`${cc} .field[data-key$="_effort"]`).evaluateAll((es) => es.map((e) => e.dataset.key)), ["subagent_effort"]);
      assert.equal(await page.locator(`${cc} .field.extra[data-key="subagent_effort"]`).getAttribute("aria-label"), w.sub);
      // not through magpie: no levels, no square, no effort entries
      assert.equal(await page.locator('.row.agent[data-id="cc-own"] .field[data-key$="_effort"]').count(), 0);

      const square = page.locator(`${cc} .field.extra[data-key="tiers"]`);
      assert.equal(await square.evaluate((e) => e.classList.contains("set")), true, "haiku has a model of its own");
      const y = await page.evaluate(() => scrollY);
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const rows = await page.locator("#list li").evaluateAll((es) => es.map((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent || ""]));
      assert.deepEqual(rows.map((r) => r[0]), tiers.flatMap((tier) => [tier, w.effort(tier)]));
      for (const [, note] of rows.filter((_, i) => i % 2)) assert.equal(note, w.asks);
      assert.equal(rows[4][1], "Flash", "haiku's own model");
      // each effort entry shows the bars
      assert.equal(await page.locator("#list li .effort-ic").count(), 4);

      // haiku's effort: the slider, Default first, then its levels
      await page.locator("#list li").nth(5).click();
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
      assert.ok((await square.getAttribute("aria-label")).startsWith(w.summary), await square.getAttribute("aria-label"));
      assert.equal(await page.evaluate(() => scrollY), y, "a pick scrolled the page");
      // the session's effort is untouched
      assert.equal(await page.locator(`${cc} .field[data-key="effort"] .v`).textContent(), w.high);
      assert.deepEqual(errors, []);
    });
  }
}
