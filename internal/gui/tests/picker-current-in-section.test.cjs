// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's current model is listed first in its picker, out of its
// provider's section (#1229), and says whose it is there. Without it,
// WorkBuddy AI's Deepseek-V4.1-Flash on top read as WorkBuddy's
// DeepSeek-V4.1-Flash, and WorkBuddy AI's section, Hy4 alone, as if magpie
// had dropped it (ADNANYURTSEVER3 on X). The two spellings, which differ
// only in case, stay two rows. In the rail's provider and for a query the
// model is listed once, in its place. Chromium and WebKit, wide and narrow,
// in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (group, icon, id, label, note) => ({ value: "magpie/" + id, ref: id, label, group, icon, note });
const options = [
  row("Routing groups", "workbuddy-color", "auto/DeepSeek-V4.1-Flash", "DeepSeek V4.1 Flash", "routing group · via magpie"),
  row("Routing groups", "workbuddy-color", "auto/Hy4", "Hy4", "routing group · via magpie"),
  row("DeepSeek", "deepseek-color", "deepseek/deepseek-v4-flash", "DeepSeek V4 Flash", "DeepSeek · via magpie"),
  row("WorkBuddy", "workbuddy-color", "workbuddy/Hy4", "Hy4", "a@example.com · via magpie"),
  row("WorkBuddy", "workbuddy-color", "workbuddy/DeepSeek-V4.1-Flash", "DeepSeek-V4.1-Flash", "a@example.com · via magpie"),
  row("WorkBuddy AI", "workbuddy-color", "workbuddy-ai/Hy4", "Hy4", "b@example.com · via magpie"),
  row("WorkBuddy AI", "workbuddy-color", "workbuddy-ai/Deepseek-V4.1-Flash", "Deepseek-V4.1-Flash", "b@example.com · via magpie"),
];
const CUR = "magpie/workbuddy-ai/Deepseek-V4.1-Flash";
const state = (lang) => ({
  agents: [{ id: "opencode", name: "OpenCode", path: "/test/opencode.json", icon: "opencode", wired: true, fields: [{ key: "model", label: "model", value: CUR, options }] }],
  profiles: [], settings: { lang, theme: "light", guideHidden: true },
});

function serve(st, lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
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

// the list's rows, each with the section header it is under
const rows = (page) => page.evaluate(() => {
  let section = "";
  const out = [];
  for (const li of document.querySelectorAll("#list li")) {
    if (li.classList.contains("group")) { section = li.textContent; continue; }
    const v = li.querySelector(".v");
    if (v) out.push({ section, name: v.textContent, cur: li.classList.contains("cur"), note: li.querySelector(".n")?.textContent || "" });
  }
  return out;
});
const named = (all, name) => all.filter((r) => r.name === name);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 440]) {
      test(`${engine} ${lang} ${width}px: the current model, listed first, says whose it is`, async () => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        try {
          const page = await browser.newPage({ viewport: { width, height: 760 } });
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("http://magpie.test/**", serve(state(lang), lang));
          await page.goto("http://magpie.test/");
          await page.locator('#nav [data-view="agents"]').click();
          await page.locator("#agents .ag-link").first().click();
          await page.locator('#agents [data-key="model"]').first().click();
          await page.locator("#pickerRail .rail-item").first().waitFor();

          let all = await rows(page);
          const cur = named(all, "Deepseek-V4.1-Flash");
          assert.equal(cur.length, 1, `listed once: ${JSON.stringify(all)}`);
          assert(cur[0].cur && cur[0].section === "", "first, out of the sections, ticked");
          assert(all.indexOf(cur[0]) < all.findIndex((r) => r.section), "before every section");
          assert.equal(cur[0].note.split(" · ")[0], "WorkBuddy AI", `it says whose it is: ${cur[0].note}`);
          assert.deepEqual(all.filter((r) => r.section === "WorkBuddy").map((r) => [r.name, r.cur, r.note]), [["Hy4", false, "a@example.com"], ["DeepSeek-V4.1-Flash", false, "a@example.com"]], "WorkBuddy's own spelling stays its own row, not ticked, and says no provider");
          assert.deepEqual(all.filter((r) => r.section === "WorkBuddy AI").map((r) => r.name), ["Hy4"]);

          // in the provider picked in the rail: once, ticked
          await page.locator('#pickerRail .rail-item[data-group="WorkBuddy AI"]').click();
          await page.waitForTimeout(400);
          all = await rows(page);
          assert.deepEqual(all.map((r) => [r.name, r.cur]).filter(([n]) => /Hy4|Flash/.test(n)).sort(), [["Deepseek-V4.1-Flash", true], ["Hy4", false]], `under WorkBuddy AI, its two: ${JSON.stringify(all)}`);
          assert.equal(named(all, "Deepseek-V4.1-Flash")[0].note.split(" · ")[0], "b@example.com", "the rail says whose it is already");

          // found by a query: once, beside WorkBuddy's
          await page.locator('#pickerRail .rail-item[data-group="all"]').click();
          await page.waitForTimeout(400);
          await page.locator("#q").fill("v4.1-flash");
          all = await rows(page);
          assert.equal(named(all, "Deepseek-V4.1-Flash").length, 1, `found once: ${JSON.stringify(all)}`);
          assert.equal(named(all, "DeepSeek-V4.1-Flash").length, 1, "WorkBuddy's found too");
          assert.deepEqual(errors, [], "page runtime errors");
        } finally {
          await browser.close();
        }
      });
    }
  }
}
