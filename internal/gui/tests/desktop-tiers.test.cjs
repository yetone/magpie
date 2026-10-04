// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Desktop's Code tab runs Claude Code, whose opus/sonnet/haiku/fable
// tiers each take a model of their own from magpie (WilianWeng). Desktop has
// no model field here for them to follow: the tiers' square says what an
// unset one runs on instead, and its picker's reset entry is "Not set", not
// "Same as model". Connected to magpie, the square is in the row opened from
// its link. In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "magpie/v/glm", label: "GLM", ref: "v/glm" }, { value: "magpie/v/flash", label: "Flash", ref: "v/flash" }];
const tiers = ["opus", "sonnet", "haiku", "fable"];
const fresh = () => ({
  agents: [{
    id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
    fields: [
      { key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie" }] },
      ...tiers.map((tier) => ({ key: tier, label: tier, value: tier === "haiku" ? "magpie/v/flash" : "", options: models })),
    ],
  }],
  profiles: [],
});

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

const W = {
  en: { unset: "a Claude model of the tier, else the chat's model", reset: "Not set", summary: "tiers: haiku" },
  zh: { unset: "该档位的 Claude 模型，没有则用对话的模型", reset: "未设置", summary: "分档：haiku" },
};

const cd = '.row.agent[data-id="claude-desktop"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Claude Desktop's tiers are picked from their square`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/");
      await page.locator(cd).waitFor();
      await page.locator(`${cd} .ag-link`).click();

      const square = page.locator(`${cd} .ag-exp .field.extra[data-key="tiers"]`);
      assert.equal(await square.count(), 1, "no tiers square");
      assert.ok((await square.getAttribute("aria-label")).startsWith(w.summary), await square.getAttribute("aria-label"));
      const y = await page.evaluate(() => scrollY);
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const rows = await page.locator("#list li").evaluateAll((es) => es.map((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent || ""]));
      assert.deepEqual(rows.map((r) => r[0]), tiers);
      assert.equal(rows[0][1], w.unset);
      assert.equal(rows[2][1], "Flash");

      // sonnet's picker: the reset entry, then magpie's models; GLM picked
      await page.locator("#list li").nth(1).click();
      await page.locator("#pop:not([hidden]) #list li", { hasText: "GLM" }).first().waitFor();
      const first = await page.locator("#list li").first().evaluate((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent || ""]);
      assert.deepEqual(first, [w.reset, w.unset]);
      await page.locator("#list li", { hasText: "GLM" }).first().click();
      await page.waitForFunction(() => document.querySelector("#status")?.textContent);
      assert.deepEqual(sets, [{ agent: "claude-desktop", field: "sonnet", value: "magpie/v/glm" }]);
      assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
      assert.deepEqual(errors, []);
    });
  }
}
