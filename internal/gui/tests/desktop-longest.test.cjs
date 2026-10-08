// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Desktop lists a model of 1M or more twice, its plain entry and a
// "1M context window" one it adds (#1272). Claude Desktop's opened row has a
// switch under its model list that lists such a model once, by its 1M entry:
// off as before, it posts the new say at once, shows it, says Desktop has to
// be reopened, and no click scrolls the page. In English and Chinese, wide
// and narrow. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const settings = { lang, theme: "light" };
  const posts = [];
  const state = () => ({
    agents: [{
      id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
      fields: [{ key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
      models: { shown: 2, listed: 2, by: [{ name: "OpenCode Go", icon: "opencode", n: 2 }], names: ["DeepSeek V4 Pro", "Exo Free"] },
    }],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/settings/desktop-longest") {
      const body = req.postDataJSON();
      posts.push(body);
      settings.desktopLongest = body.on;
      return json({ ...settings });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts };
}

const W = {
  en: { say: "Only each model's largest window", hint: "Quit and reopen Claude Desktop", details: "Details" },
  zh: { say: "只显示每个模型的最大上下文", hint: "退出并重新打开 Claude Desktop", details: "详情" },
};
const cd = '.row.agent[data-id="claude-desktop"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Claude Desktop's row switches its list to each model's largest window`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 700 } });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        t.after(() => browser.close());
        await page.goto("http://magpie.test/");
        await page.locator(cd).waitFor();
        await page.locator(`${cd} .ag-link`, { hasText: w.details }).click();
        const line = page.locator(`${cd} .ag-exp .ag-longest`);
        await line.waitFor();
        const sw = line.locator(".lib-switch");
        assert.equal(await line.locator(".ag-longest-say").textContent(), w.say);
        assert.ok((await line.locator(".ag-hint").textContent()).includes(w.hint));
        assert.equal(await sw.getAttribute("aria-checked"), "false", "on before it was switched");
        const box = await line.boundingBox();
        assert.ok(box.width <= width && box.height > 0, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await sw.click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "true", `${cd} .ag-longest .lib-switch`);
        assert.deepEqual(fx.posts, [{ on: true }]);
        // and off again, by its words
        await page.locator(`${cd} .ag-longest .ag-longest-say`).click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "false", `${cd} .ag-longest .lib-switch`);
        assert.deepEqual(fx.posts, [{ on: true }, { on: false }]);

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
  }
}
