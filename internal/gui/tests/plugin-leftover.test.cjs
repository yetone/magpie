// Run with Node's test runner and Playwright on the module path; see README.md.
// What a failed install left in plugins/ that no plugin is installed as is
// listed on Installed, each with a Remove (CHENGJIAHEGOXIS on X:
// agency-agents, left by a failed add, failed every install after it with
// "failed copying files from cache to destination", and nothing on the page
// named it). It shows with no plugin installed too, the empty state's case,
// and its Remove asks /api/plugins/leftover for that name.
// No backend here: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const LEFT = "agency-agents";
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [], instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function server(lang, state) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins/leftover" && req.method() === "POST") {
      state.asked.push(JSON.parse(req.postData() || "{}"));
      state.plugins.leftovers = [];
      return route.fulfill({ json: state.plugins });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: state.plugins });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { head: "Not used by any plugin", remove: "Remove" },
  zh: { head: "没有插件在用", remove: "移除" },
  "zh-TW": { head: "沒有外掛在用", remove: "移除" },
  ja: { head: "どのプラグインも使っていないもの", remove: "削除" },
  de: { head: "Von keiner Erweiterung verwendet", remove: "Entfernen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": what a failed install left is listed and removable", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) {
      for (const width of [980, 420]) {
        for (const installed of [false, true]) {
          await t.test(`${lang} ${width}px ${installed ? "with a plugin" : "no plugin"}`, async () => {
            const w = words[lang];
            const state = {
              asked: [],
              plugins: {
                plugins: installed ? [{ spec: "opencode-copilot-auth", providers: ["GitHub Copilot"], named: true, version: "0.1.0", moved: [] }] : [],
                bun: true, bunVersion: "1.4.2", picker: false, mirror: false,
                leftovers: [{ name: LEFT, spec: "github:msitarzewski/agency-agents" }],
              },
            };
            const ctx = await browser.newContext({ viewport: { width, height: 800 } });
            await ctx.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "installed"); } catch {} });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, state));
            await page.goto("http://magpie.test/?view=plugins");
            const v = page.locator("#view-plugins");
            const sec = v.locator(".pm-leftovers");
            await sec.waitFor();
            assert.equal((await sec.locator("h3").innerText()).trim(), w.head);
            const row = sec.locator(`[data-leftover="${LEFT}"]`);
            assert((await row.innerText()).includes(LEFT), "the row names it");
            const rm = row.locator("button");
            assert.equal((await rm.innerText()).trim(), w.remove);
            // the row fits the page: its Remove is on screen
            const box = await rm.boundingBox();
            assert(box && box.x >= 0 && box.x + box.width <= width, `Remove off screen at ${width}px: ${JSON.stringify(box)}`);
            assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, "no sideways scroll");
            await rm.click();
            await sec.waitFor({ state: "detached" });
            assert.deepEqual(state.asked, [{ spec: LEFT }]);
            await ctx.close();
          });
        }
      }
    }
    assert.deepEqual(errors, []);
  });
}
