// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's current model is listed first in its picker, out of its
// provider's rows. With that provider picked in the rail it is still there,
// ticked: under OpenAI, Codex on gpt-6.1-sol showed every OpenAI model but
// gpt-6.1-sol, as if magpie had dropped it (#1229). A provider whose only
// model is the current one keeps its rail icon, in catalog order. Codex
// connected or not, in Chromium and WebKit, at a narrow window too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (group, icon, id, label, note) => ({ value: id, ref: id, label, group, icon, note });
const options = [
  row("Routing groups", "", "group/flash", "DeepSeek V4.1 Flash"),
  row("OpenAI", "openai", "openai/gpt-6.1-sol", "gpt-6.1-sol", "GPT-6.1-Sol"),
  row("OpenAI", "openai", "openai/gpt-6-astra", "gpt-6-astra", "GPT-6-Astra"),
  row("OpenAI", "openai", "openai/gpt-6-sol", "gpt-6-sol", "GPT-6-Sol"),
  row("Kimi", "kimi", "kimi/k3", "Kimi K3"),
  row("DeepSeek", "deepseek-color", "deepseek/v4", "DeepSeek V4"),
];
const state = (wired, value) => ({
  agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired, fields: [{ key: "model", label: "model", value, options }] }],
  profiles: [], settings: { lang: "en", theme: "light" },
});

function serve(st) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const rows = (page) => page.evaluate(() => [...document.querySelectorAll("#list li:not(.group)")].map((li) => ({ text: li.textContent, cur: li.classList.contains("cur") })));
const rail = (page) => page.evaluate(() => [...document.querySelectorAll("#pickerRail .rail-item")].map((b) => b.getAttribute("aria-label")));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1000, 440]) {
    for (const wired of [true, false]) {
      test(`${engine} ${width}px ${wired ? "connected" : "not connected"}: the current model stays under its provider in the rail`, async () => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        try {
          const page = await browser.newPage({ viewport: { width, height: 760 } });
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const open = async (value) => {
            await page.unroute("http://magpie.test/**").catch(() => {});
            await page.route("http://magpie.test/**", serve(state(wired, value)));
            await page.goto("http://magpie.test/");
            await page.locator('#nav [data-view="agents"]').click();
            if (wired) await page.locator("#agents .ag-link").first().click();
            await page.locator('#agents [data-key="model"]').first().click();
            await page.locator("#pickerRail .rail-item").first().waitFor();
          };
          const item = (n) => page.locator(`#pickerRail .rail-item[aria-label="${n}"]`);

          await open("openai/gpt-6.1-sol");
          let all = await rows(page);
          assert(all.some((r) => r.cur && r.text.includes("gpt-6.1-sol")), "listed under All, ticked");
          await item("OpenAI").click();
          await page.waitForTimeout(400);
          all = await rows(page);
          const cur = all.filter((r) => r.text.includes("gpt-6.1-sol"));
          assert.equal(cur.length, 1, `gpt-6.1-sol under OpenAI once: ${JSON.stringify(all)}`);
          assert(cur[0].cur, "ticked as the current model");
          assert(all.some((r) => r.text.includes("gpt-6-astra")) && !all.some((r) => r.text.includes("Kimi K3")), "the rest are OpenAI's");
          await item("Kimi").click();
          await page.waitForTimeout(400);
          assert(!(await rows(page)).some((r) => r.text.includes("gpt-6.1-sol")), "not under another provider");

          // the current model is Kimi's only one: Kimi keeps its icon, in its place
          await open("kimi/k3");
          assert.deepEqual(await rail(page), ["All models", "Favorites", "Routing groups", "OpenAI", "Kimi", "DeepSeek"]);
          await item("Kimi").click();
          await page.waitForTimeout(400);
          assert((await rows(page)).some((r) => r.cur && r.text.includes("Kimi K3")), "Kimi K3 under Kimi");
          assert.deepEqual(errors, [], "page runtime errors");
        } finally {
          await browser.close();
        }
      });
    }
  }
}
