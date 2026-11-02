// Run with Node's test runner and Playwright on the module path; see README.md.
// A model several providers serve shows, by each provider's row in an
// agent's model picker, how fast it answered this week (/api/model-speeds,
// from the usage ledger), and a search for it lists the fastest first:
// axuhere on X asked for the speed beside each provider and a sort by it.
// A provider with no timed reply has no tag and comes last. In Chromium and
// WebKit, at a narrow window, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (group, icon, id, label) => ({ value: id, ref: id, label, group, icon });
const options = [
  row("Z.ai", "zai", "zai/glm-5", "GLM-5"),
  row("OpenRouter", "openrouter", "openrouter/glm-5", "GLM-5"),
  row("SiliconFlow", "siliconcloud-color", "siliconflow/glm-5", "GLM-5"),
  row("Kimi", "kimi", "kimi/k3", "Kimi K3"),
];
const speeds = {
  "zai/glm-5": { ttft: 3000, tps: 20, n: 4 },
  "siliconflow/glm-5": { ttft: 500, tps: 120, n: 9 },
  "kimi/k3": { ttft: 1500, n: 1 },
};
const said = {
  en: ["120 tok/s", "first token in 0.5s, 120 tokens a second", "first token in 1.5s"],
  zh: ["120 token/秒", "首字 0.5 秒，每秒 120 个 token", "首字 1.5 秒"],
  "zh-TW": ["120 token/秒", "首字 0.5 秒，每秒 120 個 token", "首字 1.5 秒"],
  ja: ["120 トークン/秒", "最初のトークンまで 0.5 秒、毎秒 120 トークン", "最初のトークンまで 1.5 秒"],
  de: ["120 Token/s", "erstes Token nach 0.5 s, 120 Tokens pro Sekunde", "erstes Token nach 1.5 s"],
};

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: false, fields: [{ key: "model", label: "model", value: "kimi/k3", options }] }], profiles: [], settings: { lang, theme: "light", guideHidden: true } } });
    if (url.pathname === "/api/model-speeds") return route.fulfill({ json: speeds });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const rows = (page) => page.evaluate(() => [...document.querySelectorAll("#list li:not(.group):not(.custom)")].map((li) => {
  const tag = li.querySelector(".badge.speed");
  const r = li.getBoundingClientRect(), b = tag?.getBoundingClientRect();
  return { ref: li.querySelector(".n")?.textContent || li.querySelector(".v")?.textContent, text: li.textContent, tag: tag?.textContent || "", title: tag?.title || "", inside: !b || (b.width > 0 && b.left >= r.left && b.right <= r.right + 0.5) };
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 440]) {
    for (const lang of Object.keys(said)) {
      test(`${engine} ${width}px ${lang}: each provider's row says its speed, fastest first under a search`, async () => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        try {
          const page = await browser.newPage({ viewport: { width, height: 760 } });
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("http://magpie.test/**", serve(lang));
          await page.goto("http://magpie.test/");
          await page.locator('#nav [data-view="agents"]').click();
          await page.locator('#agents [data-key="model"]').first().click();
          await page.locator("#list .badge.speed").first().waitFor();
          const [tps, both, first] = said[lang];
          let all = await rows(page);
          const k3 = all.find((r) => r.text.includes("Kimi K3"));
          assert.equal(k3.tag, "1.5s", "only the first token timed: its wait");
          assert.equal(k3.title, first);

          await page.locator("#q").fill("glm-5");
          // (with the "use as typed" row after them, left out here)
          await page.waitForFunction(() => document.querySelectorAll("#list li:not(.group):not(.custom)").length === 3);
          all = await rows(page);
          // a search lists each row's provider as its note
          assert.deepEqual(all.map((r) => r.ref), ["SiliconFlow", "Z.ai", "OpenRouter"], "fastest first, the untimed last");
          assert.equal(all[0].tag, tps);
          assert.equal(all[0].title, both);
          assert.equal(all[2].tag, "", "no tag without a timed reply");
          assert(all.every((r) => r.inside), `each tag inside its row: ${JSON.stringify(all)}`);
          assert.deepEqual(errors, [], "page runtime errors");
        } finally {
          await browser.close();
        }
      });
    }
  }
}
