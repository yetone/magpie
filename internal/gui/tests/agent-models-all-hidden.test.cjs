// Run with Node's test runner and Playwright on the module path; see README.md.
// Every model taken out of an agent's lists (#356, DeepSeek Harness): its
// opened row still says how many are hidden, "3 hidden", and its Pick opens
// the list to put them back. Its pickers then list only the agent's own
// models, and the way in had gone with the catalog entries, leaving no way
// back.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const list = ["deepseek-v4-pro", "deepseek-v4-flash", "deepseek-r2"].map((m) =>
    ({ id: "deepseek/" + m, name: m, group: "DeepSeek", icon: "deepseek-color", context: 1e6 }));
  const posts = [];
  let stateCalls = 0;
  const count = () => ({ shown: list.filter((m) => !m.hidden).length, listed: list.length });
  const state = () => {
    stateCalls++;
    // its own models, and the catalog's shown ones
    const options = [{ value: "deepseek-v4-pro", label: "DeepSeek-V4-Pro", group: "DeepSeek Harness" },
      ...list.filter((m) => !m.hidden).map((m) => ({ value: "magpie/" + m.id, label: m.name, ref: m.id }))];
    return {
      agents: [{
        id: "dsh", name: "DeepSeek Harness", path: "/test/.dsh/config.json", icon: "deepseek-color", wired: true,
        fields: [{ key: "model", label: "model", value: "deepseek-v4-pro", options }],
        models: count(),
      }],
      profiles: [], settings: { lang, theme: "light" },
    };
  };
  async function serve(route) {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agent-models/dsh") {
      if (req.method() === "POST") {
        const { hidden } = req.postDataJSON();
        posts.push(hidden);
        for (const m of list) m.hidden = hidden.includes(m.id);
        return json({ models: list, count: count() });
      }
      return json({ models: list });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts, stateCalls: () => stateCalls };
}

const W = {
  en: { none: "3 hidden", hideAll: "Hide all", showAll: "Show all" },
  zh: { none: "已隐藏 3 个", hideAll: "全部隐藏", showAll: "全部显示" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: every model hidden, the line stays to put them back`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const fx = fixture(lang);
      await page.route("**/*", fx.serve);
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-all-hidden.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      const row = page.locator('.row.agent[data-id="dsh"]');
      await row.locator(".ag-link").click();
      const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
      const hint = row.locator(".ag-exp .ag-chips .ag-hint");
      const shown = () => row.locator(".ag-st-t").innerText();
      await entry.waitFor();
      assert.equal(await hint.count(), 0, "none hidden");
      assert.match(await shown(), /^(Connected|已接入) · (3 models|.* 里有 3 个模型)/);
      const scroll = () => page.evaluate(() => [scrollY, document.scrollingElement.scrollTop, $("#view-agents").scrollTop]);
      const was = await scroll();

      await entry.click();
      const pop = page.locator(".am-pop:not(.leaving)");
      await pop.waitFor();
      await pop.locator(".am-hide").click();
      await page.waitForTimeout(150);
      assert.equal(fx.posts.at(-1).length, 3);
      assert.equal((await hint.innerText()).trim(), w.none);
      assert.match(await shown(), /^(Connected|已接入) · (0 models|.* 里有 0 个模型)/);
      assert.equal(await row.locator(".ag-chip").count(), 0, "no provider gives one");
      assert.equal(await pop.count(), 1, "the list stays open as the row is drawn again");

      // closed, the agents are drawn again from the state: the way back stays
      const before = fx.stateCalls();
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "detached" });
      await page.waitForTimeout(300);
      assert(fx.stateCalls() > before, "the state is asked for again");
      await entry.waitFor();
      assert.equal((await hint.innerText()).trim(), w.none);

      // and opens the list to show them all again
      await entry.click();
      await pop.waitFor();
      const showAll = pop.locator(".am-reset:not(.am-hide)");
      assert.equal(await showAll.innerText(), w.showAll);
      await showAll.click();
      await page.waitForTimeout(150);
      assert.deepEqual(fx.posts.at(-1), []);
      assert.equal(await hint.count(), 0);
      assert.equal(await row.locator(".ag-chip").innerText().then((s) => s.replace(/\s+/g, " ")), "DeepSeek 3");
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "detached" });
      await page.waitForTimeout(300);
      assert.equal(await hint.count(), 0);
      assert.deepEqual(await scroll(), was, "no click moved the page");
      assert.deepEqual(errors, []);
    });
  }
}
