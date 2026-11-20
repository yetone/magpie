// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group the agent's model picker doesn't offer is still listed
// in its Routing groups section, with why (@CHENGJIAHEGOXIS on X:
// DeepSeek-API-Agents, on on the Routing page, was in no section of
// Codex's picker, without a word). One switched off is greyed and can't be
// picked; one only the agent's own lists leave out (Codex shown only its
// picks) is picked as any other: put back in the lists first, then set.
// Chromium and WebKit, wide and narrow, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const OFF = "switched off on the Routing page";
const NOT_PICKED = "not picked for this agent, which is shown only the models picked for it";
const options = [
  { value: "group/x-grok-agent", ref: "group/x-grok-agent", label: "X-Grok-Agent", group: "Routing groups", icons: ["xai"], note: "routing group · via magpie" },
  { value: "deepseek-agents/deepseek-v4-pro", ref: "deepseek-agents/deepseek-v4-pro", label: "deepseek-v4-pro", group: "DeepSeek-Agents", icon: "deepseek-color", note: "DeepSeek-Agents · via magpie" },
  { value: "xai/grok-5", ref: "xai/grok-5", label: "grok-5", group: "xAI", icon: "xai", note: "xAI · via magpie" },
];
const unserved = [
  { value: "group/deepseek-api-agents", ref: "group/deepseek-api-agents", label: "DeepSeek-API-Agents", icons: ["deepseek-color"], why: NOT_PICKED, pick: true },
  { value: "group/open-chatgpt", ref: "group/open-chatgpt", label: "Open-ChatGPT", icons: ["openai"], why: OFF },
];
const state = (lang) => ({
  agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex", wired: true,
    fields: [{ key: "model", label: "model", value: "group/x-grok-agent", options, unserved }] }],
  profiles: [], settings: { lang, theme: "light", guideHidden: true },
});

function serve(st, lang, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push([url.pathname, req.postDataJSON()]);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st });
    if (url.pathname === "/api/set") return route.fulfill({ json: st });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await route.fulfill(body ? { body, contentType } : { status: 404, body: "" });
  };
}

// the list's rows, each with the section header it is under
const rows = (page) => page.evaluate(() => {
  let section = "";
  const out = [];
  for (const li of document.querySelectorAll("#list li")) {
    if (li.classList.contains("group")) { section = li.textContent; continue; }
    const v = li.querySelector(".v");
    if (v) out.push({ section, name: v.textContent, unserved: li.classList.contains("unserved"), note: li.querySelector(".n")?.textContent || "", star: !!li.querySelector(".favorite") });
  }
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 440]) {
      test(`${engine} ${lang} ${width}px: a group the picker lacks is listed with why`, async () => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        try {
          const page = await browser.newPage({ viewport: { width, height: 760 } });
          const errors = [], posts = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("http://magpie.test/**", serve(state(lang), lang, posts));
          await page.goto("http://magpie.test/");
          await page.locator('#nav [data-view="agents"]').click();
          await page.locator("#agents .ag-link").first().click();
          const field = page.locator('#agents [data-key="model"]').first();
          await field.click();
          await page.locator("#pickerRail .rail-item").first().waitFor();
          const said = await page.evaluate((w) => w.map((x) => t(x)), [OFF, NOT_PICKED]);
          if (lang !== "en") assert.notDeepEqual(said, [OFF, NOT_PICKED], "said in the page's language");

          const all = await rows(page);
          const groups = all.filter((r) => r.name !== "X-Grok-Agent" && /Agents|ChatGPT/.test(r.name));
          assert.deepEqual(groups.map((r) => r.name), ["DeepSeek-API-Agents", "Open-ChatGPT"], `both listed: ${JSON.stringify(all)}`);
          const sec = all.find((r) => r.name === "deepseek-v4-pro").section;
          for (const r of groups) assert.notEqual(r.section, sec, "in the groups' section, not a provider's");
          assert.equal(groups[0].section, groups[1].section, "one section");
          assert.deepEqual(groups.map((r) => [r.unserved, r.note, r.star]), [[false, said[1], false], [true, said[0], false]]);
          // the whole reason reads on hover, though narrow cuts it
          assert.equal(await page.locator("#list li.unserved .n").getAttribute("title"), said[0]);

          // switched off: a click picks nothing and the picker stays open
          await page.locator("#list li.unserved").click();
          assert(await page.locator("#pop").isVisible(), "the picker stays open");
          assert.deepEqual(posts.filter(([p]) => p === "/api/set"), [], "nothing set");

          // left out of Codex's lists only: put back in them, then set
          await page.locator("#list li:not(.unserved)", { hasText: "DeepSeek-API-Agents" }).click();
          for (let i = 0; i < 100 && !posts.some(([p]) => p === "/api/set"); i++) await page.waitForTimeout(50);
          const asked = posts.filter(([p]) => p === "/api/set" || p.startsWith("/api/agent-models/"));
          assert.deepEqual(asked, [
            ["/api/agent-models/codex", { show: "group/deepseek-api-agents" }],
            ["/api/set", { agent: "codex", field: "model", value: "group/deepseek-api-agents" }],
          ]);
          assert.deepEqual(errors, [], "page runtime errors");
        } finally {
          await browser.close();
        }
      });
    }
  }
}
