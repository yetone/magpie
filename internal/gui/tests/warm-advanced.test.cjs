// Run with Node's test runner and Playwright on the module path; see README.md.
// "我的预热去哪里了？？" (ChrisYagao on X): a subscription's warm-up and
// daily check-in moved from Settings into its provider's editor (11-09) and
// then under the editor's Advanced, folded (11-10), so a user who had one
// on opened the editor and saw nothing of it. Advanced now opens on its own
// when the provider's warm-up (on reset or at times of day) or its daily
// check-in is on, as it does for Routing; with them off it stays folded,
// and a fold the reader closed by hand stays closed. Codex, Claude Code and
// a plugin that checks in; en, zh, zh-TW, ja, de; both engines; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const names = { codex: "Codex", claude: "Claude Code", "trae-cn": "Trae CN" };

function serve(lang, settings) {
  const s = { theme: "light", lang, currency: "usd", codexWarmup: "", claudeWarmup: "", codexUsers: ["ann@example.test"], claudeUsers: ["ann@example.test"],
    checkinPlugins: [{ id: "trae-cn", name: "Trae CN", on: false }], ...settings };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: s });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/providers") return json(provPrefsProviders(Object.keys(names), names));
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// provider, what is set, whether Advanced opens and the list that shows
const CASES = [
  ["codex", "nothing", {}, false, "#codexWarmList"],
  ["codex", "warm up on reset", { codexWarmup: "week" }, true, "#codexWarmList"],
  ["codex", "daily warm-up", { codexWarmAts: ["06:00"], codexWarmAt: "06:00" }, true, "#codexWarmList"],
  ["claude", "nothing", {}, false, "#claudeWarmList"],
  ["claude", "warm up on reset", { claudeWarmup: "all" }, true, "#claudeWarmList"],
  ["claude", "daily warm-up", { claudeWarmAts: ["07:30"], claudeWarmAt: "07:30" }, true, "#claudeWarmList"],
  ["claude", "Codex's warm-up only", { codexWarmup: "week" }, false, "#claudeWarmList"],
  ["trae-cn", "nothing", {}, false, ".ci-plugin"],
  ["trae-cn", "daily check-in", { checkinPlugins: [{ id: "trae-cn", name: "Trae CN", on: true }] }, true, ".ci-plugin"],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a provider's warm-up or check-in that is on opens its editor's Advanced (ChrisYagao)`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [id, set, settings, opens, list] of CASES) {
        const at = `${id}, ${set}`;
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // #1350
        await page.route("**/*", serve(lang, settings));
        await page.goto(`http://magpie.test/?view=providers&edit=${encodeURIComponent(id)}`);
        const adv = page.locator(".editor details.advanced");
        await adv.waitFor({ timeout: 60000 });
        // the lists are mounted once Settings are read
        await page.waitForFunction((list) => !!document.querySelector(`.editor .prov-prefs ${list}`), list);
        assert.equal(await adv.evaluate((d) => d.open), opens, `${at}: Advanced open`);
        assert.equal(await page.locator(`.editor .prov-prefs ${list}`).first().isVisible(), opens, `${at}: its list shown`);
        if (opens) {
          // closed by hand, a redraw of Settings leaves it closed
          await adv.locator(":scope > summary").click();
          await page.waitForFunction(() => !document.querySelector(".editor details.advanced").open);
          await page.evaluate(() => renderSettings());
          await page.waitForTimeout(100);
          assert.equal(await adv.evaluate((d) => d.open), false, `${at}: closed by hand stays closed`);
        }
        assert.deepEqual(errors, [], at);
        await page.context().close();
      }
    });
  }
}
