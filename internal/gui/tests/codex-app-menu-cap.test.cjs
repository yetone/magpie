// Run with Node's test runner and Playwright on the module path; see README.md.
// The Codex app's model menu reaches only the first 100 models Codex lists
// (it asks model/list for 100 and never for the next page), so with more
// than that the Codex row says how many it can't pick and where to choose
// which ones it gets (#1262); with 100 or fewer it says nothing. In English
// and Chinese, wide and narrow, with no line wider than the row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, shown) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({
      agents: [{
        id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
        fields: [{ key: "model", label: "model", value: "pqh/gpt-6-sol", options: [{ value: "pqh/gpt-6-sol", label: "6 Sol", ref: "pqh/gpt-6-sol" }] }],
        models: { shown, listed: shown + 3, by: [{ name: "Routing groups", n: 26 }, { name: "pqh", n: 19 }, { name: "ZCode", n: shown - 45 }] },
      }],
      profiles: [], settings: { lang, theme: "light" },
    });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const said = {
  en: ["The Codex app's model menu shows only the first 100", "the last 107 models"],
  zh: ["Codex App 的模型菜单只显示前 100 个", "后面 107 个模型"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1000, 560]) {
      test(`${engine} ${lang} ${width}px: the Codex row says what its app's menu can't reach`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const [shown, warned] of [[207, true], [100, false]]) {
          const page = await browser.newPage({ viewport: { width, height: 800 } });
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, shown));
          await page.goto("http://magpie.test/");
          const row = page.locator('.row.agent[data-id="codex"]');
          await row.locator(".ag-link").click();
          await row.locator(".ag-exp .ag-chips").waitFor();
          const cap = row.locator(".ag-exp .ag-menu-cap");
          if (!warned) {
            assert.equal(await cap.count(), 0, `${shown} models`);
          } else {
            await cap.waitFor();
            const text = await cap.textContent();
            for (const s of said[lang]) assert(text.includes(s), text);
            // under the model list, in the panel, and no wider than it
            const fit = await cap.evaluate((w) => {
              const box = w.closest(".ag-exp").getBoundingClientRect(), b = w.getBoundingClientRect();
              return { inside: b.left >= box.left - 1 && b.right <= box.right + 1, list: !!w.closest(".ag-kv")?.querySelector(".ag-chips"), overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
            });
            assert.deepEqual(fit, { inside: true, list: true, overflow: 0 });
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-cap.png`) });
            }
          }
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
