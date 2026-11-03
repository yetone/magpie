// Run with Node's test runner and Playwright on the module path; see README.md.
// JetBrains Air's row picks the model of the Magpie agent magpie adds to
// Air, and nothing else. jumy_18634 on Discord picked one there and found
// Air's own Codex still on its own model. Air's IDE plugin runs the codex
// and claude on this computer, so its Codex and Claude Agent are on magpie
// exactly while Codex and Claude Code are connected here; the Air app's run
// on its own sign-in and never are. Each row, opened, says which, in every
// language, wide and narrow. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const say = (lang, k) => (lang === "en" ? k : I18N[lang][k]);

const model = (v) => [{ key: "model", label: "model", value: v, options: [{ value: v, ref: "qoder/qoder-fast", label: "qoder-fast", icon: "magpie" }] }];
function fixture(lang) {
  const settings = { lang, theme: "light" };
  const state = () => ({
    agents: [
      { id: "air-ide", name: "Air (JetBrains IDEs)", path: "/test/.jetbrains/acp.json", icon: "air", wired: true, fields: model("qoder/qoder-fast") },
      { id: "air", name: "JetBrains Air", path: "/test/Air/acp.json", icon: "air", wired: true, fields: model("qoder/qoder-fast") },
      { id: "codex", name: "Codex", path: "/test/.codex/config.toml", icon: "openai", wired: true, fields: model("qoder/qoder-fast") },
      { id: "claude", name: "Claude Code", path: "/test/.claude/settings.json", icon: "claude-color", wired: false, fields: model("") },
    ],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
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
  return { serve };
}

const DETAILS = { en: "Details", zh: "详情", "zh-TW": "詳情", ja: "詳細", de: "Details" };
const row = (id) => `.row.agent[data-id="${id}"]`;
const text = async (loc) => (await loc.allTextContents()).map((s) => s.trim());

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(DETAILS)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Air's rows say whether Air's own Codex and Claude Agent go through magpie`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang).serve);
        t.after(() => browser.close());
        await page.goto("http://magpie.test/");

        // the IDE plugin's: Codex connected here, Claude Code not
        const ide = row("air-ide");
        await page.locator(ide).waitFor();
        await page.locator(`${ide} .ag-link`, { hasText: DETAILS[lang] }).click();
        const lines = page.locator(`${ide} .ag-exp .ag-vl`);
        await lines.first().waitFor();
        const all = await text(lines);
        for (const k of [
          "In Air's agent menu, pick Magpie: the model here is Magpie's, and magpie's models are in its model menu.",
          "Air's own Codex and Claude Agent don't take the model here: they run the Codex and Claude Code on this computer, with those agents' own settings.",
          "Codex is connected here, so Air's Codex lists magpie's models too: pick one in its model menu, in a new Codex task.",
        ]) assert.ok(all.includes(say(lang, k)), `${JSON.stringify(say(lang, k))} not in ${JSON.stringify(all)}`);
        assert.deepEqual(await text(page.locator(`${ide} .ag-exp .ag-air-own`)), [say(lang, "Claude Code isn't connected here, so Air's Claude Agent doesn't go through magpie.")]);
        const fits = async (loc) => {
          const box = await loc.first().boundingBox();
          assert.ok(box.width <= width && box.height > 0, JSON.stringify(box));
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        };
        await fits(page.locator(`${ide} .ag-exp .ag-air-own`));

        // the app's: never through magpie, whatever is connected here
        const app = row("air");
        await page.locator(`${app} .ag-link`, { hasText: DETAILS[lang] }).click();
        const own = page.locator(`${app} .ag-exp .ag-air-own`);
        await own.first().waitFor();
        assert.deepEqual(await text(own), [say(lang, "Air's own agents (Codex, Claude Agent…) run on Air's own sign-in and settings, not through magpie: only Magpie in its agent menu takes magpie's models.")]);
        await fits(own);
        assert.deepEqual(errors, []);
      });
    }
  }
}
