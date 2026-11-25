// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity's app takes no more than 32 of a gateway's models and leaves
// the rest out of its menu (0xBCD18E on X: its row said 25 models while
// the Gemini ones were missing). With more than 32 picked, its row counts
// the 32 its menu has, and opened says the rest can't be picked there and
// where to choose which ones it gets; with 32 or fewer it counts them all
// and says nothing more. English, Simplified and Traditional Chinese, Japanese and
// German, wide and narrow, with no line wider than the row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const model = "magpie/ds/deepseek-v4-pro";

function server(lang, shown) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({
      agents: [{
        id: "antigravity", name: "Antigravity", path: "/test/.gemini/config/config.json", icon: "antigravity-color", wired: true, menu: true,
        fields: [{ key: "model", label: "model", value: model, options: [{ value: model, label: "DeepSeek V4 Pro", ref: "ds/deepseek-v4-pro" }] }],
        models: { shown, listed: shown + 2, menu: 32, by: [{ name: "Routing groups", n: 1 }, { name: "Google Gemini", n: 6 }, { name: "DS", n: shown - 7 }] },
      }],
      profiles: [], settings: { lang, theme: "light", guideHidden: true },
    });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// what the row says with 40 picked, and with 25
const said = {
  en: { over: "32 models in Antigravity's model menu", cap: ["Antigravity's model menu takes only the first 32 models", "the last 8"], under: "25 models in Antigravity's model menu" },
  zh: { over: "Antigravity 的模型菜单里有 32 个模型", cap: ["Antigravity 的模型菜单只收下前 32 个模型", "后面 8 个"], under: "Antigravity 的模型菜单里有 25 个模型" },
  "zh-TW": { over: "Antigravity 的模型選單裡有 32 個模型", cap: ["Antigravity 的模型選單只收下前 32 個模型", "後面 8 個"], under: "Antigravity 的模型選單裡有 25 個模型" },
  ja: { over: "Antigravity のモデルメニューに 32 モデル", cap: ["先頭の 32 モデルだけ", "残りの 8 モデル"], under: "Antigravity のモデルメニューに 25 モデル" },
  de: { over: "32 Modelle im Modellmenü von Antigravity", cap: ["nimmt nur die ersten 32 Modelle", "Die letzten 8"], under: "25 Modelle im Modellmenü von Antigravity" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(said)) {
    for (const width of [1000, 560]) {
      test(`${engine} ${lang} ${width}px: the Antigravity row says what its menu takes`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const shown of [40, 25]) {
          const page = await browser.newPage({ viewport: { width, height: 800 } });
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, shown));
          await page.goto("http://magpie.test/");
          const row = page.locator('.row.agent[data-id="antigravity"]');
          await row.waitFor();
          const rowText = await row.textContent();
          assert(rowText.includes(shown > 32 ? said[lang].over : said[lang].under), rowText);
          await row.locator(".ag-link").click();
          await row.locator(".ag-exp .ag-chips").waitFor();
          const cap = row.locator(".ag-exp .ag-menu-cap");
          if (shown <= 32) {
            assert.equal(await cap.count(), 0, `${shown} models`);
          } else {
            await cap.waitFor();
            const text = await cap.textContent();
            for (const s of said[lang].cap) assert(text.includes(s), text);
            // under the model list, in the panel, and no wider than it
            const fit = await cap.evaluate((w) => {
              const box = w.closest(".ag-exp").getBoundingClientRect(), b = w.getBoundingClientRect();
              return { inside: b.left >= box.left - 1 && b.right <= box.right + 1, list: !!w.closest(".ag-kv")?.querySelector(".ag-chips"), overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
            });
            assert.deepEqual(fit, { inside: true, list: true, overflow: 0 });
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-agy-cap.png`) });
            }
          }
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
