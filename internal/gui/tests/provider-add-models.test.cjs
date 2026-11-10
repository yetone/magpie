// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's models picked from its vendor's list while it is added
// (#578: 添加供应商的时候，希望添加可以获取全模型的按钮或者下拉框？像cc-switch
// 那样，可以选): the custom provider's add form has Fetch models beside its
// Models field, which posts provider/list with the URL and key typed — no id,
// nothing saved — and shows the list as chips. A chip ticked is in the
// field, typing in the field ticks its chip, a filter narrows a long list,
// Pick all / Pick none act on what is shown, and the Add sends the models
// picked. A vendor that refuses says why in the form. No click moves the
// page, and nothing has a left-border accent. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const URL_ = "https://relay.example.com/v1";
const listed = Array.from({ length: 30 }, (_, i) => ({ id: i < 3 ? ["gpt-5.5", "claude-sonnet-5", "deepseek-v4"][i] : `m-${String(i).padStart(2, "0")}`, ...(i === 1 ? { name: "Claude Sonnet 5" } : {}) }));

function serve(lang, posts, refuse) {
  const providers = { providers: [{ id: "anthropic", name: "Anthropic", icon: "anthropic", preset: "anthropic", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      if (url.pathname === "/api/provider/list") return refuse() ? json({ error: "401 · invalid api key" }, 400) : json({ models: listed });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Fetch models in the add form`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator(".add-models").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-add-models.png`) }).catch(() => {});
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      let refusing = true;
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, () => refusing));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = {
        custom: zh ? "自定义供应商" : "Custom provider", fetch: zh ? "获取模型" : "Fetch models", all: zh ? "全选" : "Pick all",
        shown: zh ? "选中筛选结果" : "Pick those shown", none: zh ? "全不选" : "Pick none", add: zh ? "添加" : "Add",
        picked: (n, all) => zh ? `已选 ${n} / ${all}` : `${n} of ${all} picked`,
        hint: zh ? "可选：用「获取模型」从供应商列表中勾选，或直接填写模型 ID；不选则保存后自动获取模型列表。" : "Optional: Fetch models to pick from the vendor's list, or type ids; none picked, magpie asks for the list after saving.",
      };
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .row-head .custom").click();
      const ed = page.locator(".editor.new");
      await ed.locator(".add-models").waitFor();
      const field = ed.locator(".add-models .detect-row input");
      const go = ed.getByRole("button", { name: L.fetch, exact: true });
      assert.equal(await ed.locator(".add-models + .hint").textContent(), L.hint);

      // a click that leaves the page, and the button, where they were
      const still = async (loc, what) => {
        await loc.scrollIntoViewIfNeeded();
        await page.waitForTimeout(120);
        const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
        const y = await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
        await loc.click();
        await page.waitForTimeout(200);
        if (await loc.count()) assert.equal(await loc.evaluate((e) => e.getBoundingClientRect().top), before, what + " moved");
        assert.equal(await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join()), y, what + " scrolled the page");
      };

      // no URL yet: nothing asked
      await go.click();
      await page.waitForTimeout(150);
      assert.equal(posts.length, 0, "nothing is asked without a URL");

      await ed.locator('input[type="url"][placeholder="https://…/v1"]').first().fill(URL_);
      await ed.locator('input[type="password"]').first().fill("sk-typed");

      // the vendor refuses: said in the form
      await still(go, "Fetch models (refused)");
      await ed.locator(".add-models-list .hint.err").waitFor();
      assert.match(await ed.locator(".add-models-list .hint.err").textContent(), /401 · invalid api key/);
      assert.equal(posts.length, 1);
      const sent = posts[0].body;
      assert.equal(posts[0].path, "/api/provider/list");
      assert.equal(sent.chat, URL_, "the URL typed");
      assert.equal(sent.key, "sk-typed", "the key typed");
      assert.equal(sent.typed, true);
      assert(!sent.id, "no id: nothing saved is asked");

      // it answers: the list, filtered as it is long
      refusing = false;
      await still(go, "Fetch models");
      const chips = ed.locator(".add-models-list .mchip");
      await chips.first().waitFor();
      assert.equal(await chips.count(), 30);
      assert.equal(await chips.nth(1).textContent(), "Claude Sonnet 5", "a model's name, its id in the title");
      assert.equal(await chips.nth(1).getAttribute("title"), "claude-sonnet-5");
      assert.equal(await ed.locator(".add-models-list .detect-acts .hint").textContent(), L.picked(0, 30));

      // a chip ticked is in the field; typing ticks a chip
      await still(chips.nth(0), "a chip");
      assert.equal(await field.inputValue(), "gpt-5.5");
      assert.equal(await chips.nth(0).getAttribute("class"), "mchip pick on");
      await field.fill("gpt-5.5, deepseek-v4, own-model");
      assert.equal(await ed.locator('.add-models-list .mchip.on').count(), 2);
      assert.equal(await ed.locator(".add-models-list .detect-acts .hint").textContent(), L.picked(2, 30));

      // a filter, and Pick those shown
      await ed.locator(".add-models-filter").fill("m-1");
      assert.equal(await chips.count(), 10);
      await still(ed.getByRole("button", { name: L.shown, exact: true }), "Pick those shown");
      assert.equal(await field.inputValue(), "gpt-5.5, deepseek-v4, own-model, " + Array.from({ length: 10 }, (_, i) => `m-1${i}`).join(", "));
      await ed.locator(".add-models-filter").fill("");
      await still(ed.getByRole("button", { name: L.none, exact: true }), "Pick none");
      assert.equal(await field.inputValue(), "own-model", "one typed by hand stays");
      await still(ed.getByRole("button", { name: L.all, exact: true }), "Pick all");
      assert.equal(await ed.locator(".add-models-list .detect-acts .hint").textContent(), L.picked(30, 30));
      await ed.locator(".add-models-filter").fill("");
      await still(ed.locator(".add-models-list .mchip", { hasText: /^m-29$/ }), "a chip off");

      const border = await page.evaluate(() => [...document.querySelectorAll(".add-models, .add-models *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the Add carries the models picked
      await ed.locator('input[placeholder="e.g. My Relay"], input[placeholder="例如 My Relay"]').first().fill("Relay");
      await ed.getByRole("button", { name: L.add, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/provider/save");
      assert(save, "Add posts provider/save");
      assert.deepEqual(save.body.models, ["own-model", ...listed.map((m) => m.id).filter((id) => id !== "m-29")]);
      assert.equal(posts.filter((p) => p.path === "/api/provider/list").length, 2, "nothing else asks the vendor");
      assert.deepEqual(errors, []);
    });
  }
}
