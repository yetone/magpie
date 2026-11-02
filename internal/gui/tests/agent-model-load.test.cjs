// Run with Node's test runner and Playwright on the module path; see README.md.
// The Agents page says how busy a model's plugin says it is now, by the
// model picked on the row and by each model in its picker, the figure the
// provider editor shows (/api/model-loads, by the picker's ref lowercased):
// axelburks on Discord found it only in the provider's editor, too deep to
// pick a model by. A model with no load reported says nothing; a model@size
// has its model's. In Chromium and WebKit, at a wide and a narrow window,
// in every language. ARTIFACT_DIR keeps screenshots.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (group, icon, id, label) => ({ value: id, ref: id, label, group, icon });
const options = [
  row("Inhouse", "generic", "Inhouse/GLM-5", "GLM-5"),
  row("Inhouse", "generic", "inhouse/grok-4.7@256k", "Grok 4.7 (256K)"),
  row("Inhouse", "generic", "inhouse/quiet", "Quiet"),
  row("Kimi", "kimi", "kimi/k3", "Kimi K3"),
];
const loads = { "inhouse/glm-5": 87.4, "inhouse/grok-4.7": 130 };
const said = {
  en: "87% busy now, as its plugin says",
  zh: "插件报告当前负载 87%",
  "zh-TW": "外掛回報目前負載 87%",
  ja: "プラグインの報告では現在 87% の負荷",
  de: "laut seinem Plugin gerade zu 87 % ausgelastet",
};

function serve(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true, fields: [{ key: "model", label: "model", value: "Inhouse/GLM-5", options }] }], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/model-loads") { asked.n++; return route.fulfill({ json: loads }); }
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const inside = (tag, box) => {
  const b = tag.getBoundingClientRect(), r = box.getBoundingClientRect();
  return b.width > 0 && b.left >= r.left - 0.5 && b.right <= r.right + 0.5 && b.top >= r.top - 0.5 && b.bottom <= r.bottom + 0.5;
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 440]) {
    for (const lang of Object.keys(said)) {
      test(`${engine} ${width}px ${lang}: the picked model and each picker row say its load`, async () => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        try {
          const page = await browser.newPage({ viewport: { width, height: 760 } });
          const errors = [], asked = { n: 0 };
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("http://magpie.test/**", serve(lang, asked));
          await page.goto("http://magpie.test/");
          await page.locator('#nav [data-view="agents"]').click();
          const field = page.locator('#agents [data-key="model"]').first();
          await field.locator(".badge.load").waitFor();
          const f = await field.evaluate((b, inside) => {
            const tag = b.querySelector(".badge.load");
            return { text: tag.textContent, title: tag.title, inside: new Function("return " + inside)()(tag, b), shown: b.querySelector(".v").getBoundingClientRect().width > 0 };
          }, inside.toString());
          assert.equal(f.text, "87%");
          assert.equal(f.title, said[lang]);
          assert(f.inside, "the load sits inside the model's button");
          assert(f.shown, "the model's name still shows beside it");
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `agent-load-row-${engine}-${width}-${lang}.png`) });
          // the rows drawn again (a drift, a change) keep it, with no read between
          await page.evaluate(() => renderAgents());
          assert.equal(await field.locator(".badge.load").textContent({ timeout: 1000 }), "87%", "drawn again, the row keeps its load");

          await field.click();
          await page.locator("#list .badge.load").first().waitFor();
          const rows = await page.evaluate((inside) => {
            const within = new Function("return " + inside)();
            return [...document.querySelectorAll("#list li:not(.group):not(.custom)")].map((li) => {
              const tag = li.querySelector(".badge.load");
              return { name: li.querySelector(".v")?.textContent, tag: tag?.textContent || "", title: tag?.title || "", inside: !tag || within(tag, li) };
            });
          }, inside.toString());
          const by = Object.fromEntries(rows.map((r) => [r.name, r]));
          assert.equal(by["GLM-5"].tag, "87%");
          assert.equal(by["GLM-5"].title, said[lang]);
          assert.equal(by["Grok 4.7 (256K)"].tag, "130%", "a model@size has its model's load");
          assert.equal(by.Quiet.tag, "", "no load reported, nothing said");
          assert.equal(by["Kimi K3"].tag, "", "no load reported, nothing said");
          assert(rows.every((r) => r.inside), `each load inside its row: ${JSON.stringify(rows)}`);
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `agent-load-menu-${engine}-${width}-${lang}.png`) });
          // read once for the row and the picker both, not again at the open
          assert.equal(asked.n, 1, "model-loads read once within 30s");
          assert.deepEqual(errors, [], "page runtime errors");
        } finally {
          await browser.close();
        }
      });
    }
  }
}
