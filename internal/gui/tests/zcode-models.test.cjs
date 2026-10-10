// Run with Node's test runner and Playwright on the module path; see README.md.
// ZCode's row picks which of magpie's models its picker lists, as every
// other agent's does (#1508, NovaTrailX: settings.hiddenModels had no
// zcode, and ZCode got every model). Its one field puts magpie in ZCode's
// own picker, so, as Cursor Private Inference's, its row has the models
// button the server says it has (menu), which opens the list; unticking
// one is posted at once and the row says so. In the window and the tray
// panel, in every language, in Chromium and WebKit. No native <select>,
// and no click scrolls the page. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const list = [
    { id: "deepseek/pro", name: "DeepSeek Pro", group: "DeepSeek", icon: "deepseek" },
    { id: "deepseek/flash", name: "DeepSeek Flash", group: "DeepSeek", icon: "deepseek" },
    { id: "kimi/k3", name: "Kimi K3", group: "Kimi", icon: "generic" },
  ];
  const posts = [];
  const count = () => {
    const on = list.filter((m) => !m.hidden);
    const by = [];
    for (const m of on) {
      let g = by.find((x) => x.name === m.group);
      if (!g) by.push(g = { name: m.group, icon: m.icon, n: 0 });
      g.n++;
    }
    return { shown: on.length, listed: list.length, by, names: on.slice(0, 3).map((m) => m.name) };
  };
  const state = () => ({
    agents: [{
      id: "zcode", name: "ZCode", path: "~/.zcode/v2/config.json", icon: "zcode", wired: true, menu: true,
      fields: [{ key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie", note: "every magpie model in ZCode's picker" }] }],
      models: count(),
    }],
    profiles: [], settings: { lang, theme: "light" },
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agent-models/zcode") {
      if (req.method() === "POST") {
        const { hidden } = req.postDataJSON();
        posts.push(hidden);
        for (const m of list) m.hidden = hidden.includes(m.id);
        return json({ models: list, count: count() });
      }
      return json({ models: list });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await route.fulfill(body ? { body, contentType } : { status: 404, body: "" });
  }
  return { serve, posts };
}

const cd = '.row.agent[data-id="zcode"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const where of ["window", "panel"]) {
      test(`${engine} ${lang} ${where}: ZCode's row picks the models its picker lists`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: where === "panel" ? { width: 420, height: 640 } : { width: 1100, height: 700 } });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        t.after(() => browser.close());
        await page.goto("http://magpie.test/" + (where === "panel" ? "?mode=panel" : ""));
        await page.locator(cd).waitFor();
        const said = (n) => page.evaluate((n) => t("Connected · {n} models in {agent}'s model menu", { n, agent: "ZCode" }), n);

        let menu;
        if (where === "window") {
          assert.equal(await page.locator(`${cd} .ag-st-t`).textContent(), await said(3));
          menu = page.locator(`${cd} > .ag-menu`);
        } else {
          assert.equal(await page.locator(`${cd} .ag-sum .vt`).textContent(), "DeepSeek Pro, DeepSeek Flash +1");
          await page.locator(`${cd} .ag-sum`).click();
          menu = page.locator(`${cd} .ag-open .ag-menu`);
          await page.waitForFunction((sel) => document.querySelector(sel)?.getBoundingClientRect().height > 0, `${cd} .ag-open .ag-menu`);
          assert.equal(await page.locator(`${cd} .ag-conn-said`).textContent(), await said(3));
        }
        assert.equal(await menu.count(), 1, "no models button");
        assert.equal(await menu.locator(".v").textContent(), "DeepSeek Pro, DeepSeek Flash +1");

        const y = await page.evaluate(() => scrollY);
        await menu.click();
        const box = page.locator(".am-pop:not(.leaving)");
        await box.waitFor();
        assert.equal(await box.locator(".am-t").textContent(), await page.evaluate(() => t("{agent}'s model list", { agent: "ZCode" })));
        const rows = await box.locator(".am-mr").evaluateAll((es) => es.map((e) => [e.querySelector(".n").textContent, e.getAttribute("aria-checked")]));
        assert.deepEqual(rows, [["DeepSeek Pro", "true"], ["DeepSeek Flash", "true"], ["Kimi K3", "true"]]);

        // DeepSeek Flash out: posted at once, the list stays open, the row says so
        await box.locator(".am-mr", { hasText: "DeepSeek Flash" }).click();
        await page.waitForFunction(() => document.querySelector(".ag-menu .v")?.textContent === "DeepSeek Pro, Kimi K3");
        await page.waitForTimeout(150);
        assert.deepEqual(fx.posts, [["deepseek/flash"]]);
        assert.equal(await box.count(), 1, "the list closed after one pick");
        assert.equal(await page.locator(where === "window" ? `${cd} .ag-st-t` : `${cd} .ag-conn-said`).textContent(), await said(2));
        // and back in
        await box.locator(".am-mr", { hasText: "DeepSeek Flash" }).click();
        await page.waitForFunction(() => document.querySelector(".ag-menu .v")?.textContent === "DeepSeek Pro, DeepSeek Flash +1");
        await page.waitForTimeout(150);
        assert.deepEqual(fx.posts.at(-1), []);

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the row runs past the window");
        if (lang !== "en") assert.ok(!(await page.locator(cd).textContent()).includes("models in"), "English in the row");
        assert.deepEqual(errors, []);
      });
    }
  }
}
