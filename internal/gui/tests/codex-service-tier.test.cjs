// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's service tier (JackyFill on X): a row in Codex's settings on the
// Agents page sets the tier every request of Codex's goes at. Its button
// says the choice, "As Codex asks" by default; the app's menu (no native
// select) offers As Codex asks, Standard, Fast and Ultrafast, and a pick
// posts settings/codex-service-tier on its own: "", "default", "priority"
// or "ultrafast". No click scrolls the page. Every language, Chromium and
// WebKit, a wide and a narrow window; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, posted) {
  let cur = { theme: "light", lang, version: "0.1.400", titleModels: [], codexServiceTier: "" };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    // agents below Codex, so its opened row is not the list's end: a choice
    // that rewraps its row at the very bottom must move the list
    if (url.pathname === "/api/state") return json({ agents: [agentRow("codex", "Codex"), agentRow("claude", "Claude Code"), agentRow("pi", "Pi"), agentRow("opencode", "OpenCode"), agentRow("gemini", "Gemini CLI"), agentRow("kimi", "Kimi CLI"), agentRow("droid", "Droid"), agentRow("cline", "Cline"), agentRow("qwen", "Qwen Code"), agentRow("crush", "Crush")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/settings/codex-service-tier") {
      const body = req.postDataJSON();
      posted.push(["codex-service-tier", body]);
      cur = { ...cur, codexServiceTier: body.tier };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posted.push(["settings", req.postDataJSON()]);
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

// the words as i18n.js has them
const W = {
  en: { name: "Codex service tier", asks: "As Codex asks", standard: "Standard", fast: "Fast", ultra: "Ultrafast" },
  zh: { name: "Codex 服务层级", asks: "按 Codex 的请求", standard: "标准", fast: "快速", ultra: "极速" },
  "zh-TW": { name: "Codex 服務層級", asks: "依 Codex 的請求", standard: "標準", fast: "快速", ultra: "極速" },
  ja: { name: "Codex のサービスティア", asks: "Codex の指定どおり", standard: "標準", fast: "高速", ultra: "超高速" },
  de: { name: "Codex-Servicestufe", asks: "Wie Codex es anfragt", standard: "Standard", fast: "Schnell", ultra: "Ultrafast" },
};
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [900, 400]) {
      test(`${engine} ${lang} ${width}px: Codex's service tier is picked from the app menu in its settings`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-service-tier.png`) });
          }
          await browser.close();
        });
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page);
        const row = page.locator('.row.agent[data-id="codex"] #codexServiceTierRow');
        const pick = page.locator("#codexServiceTierPick button");
        await pick.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        assert.equal((await pick.textContent()).trim(), w.asks, "as Codex asks by default");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        // only Codex's settings have it
        await page.locator('.row.agent[data-id="claude"] .ag-link').click();
        await page.locator('.row.agent[data-id="claude"] .ag-prefs').waitFor();
        assert.equal(await page.locator('.row.agent[data-id="claude"] #codexServiceTierRow').count(), 0, "the row in Claude Code's settings");
        await openAgentSettings(page);
        await pick.waitFor();
        await pick.scrollIntoViewIfNeeded();
        const box = await pick.boundingBox();
        assert(box && box.x >= 0 && box.x + box.width <= width, `the button runs off the window: ${JSON.stringify(box)}`);
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "the page scrolls sideways");

        // where the button sits on screen: a pick that moved the page would
        // move it. At a narrow width the row rewraps as the choice's words
        // change (the button under the name or beside it), which moves the
        // button with no scroll at all, so it is held to its place only
        // while the row keeps its height.
        const top = () => pick.evaluate((b) => Math.round(b.getBoundingClientRect().top));
        const tall = () => row.evaluate((r) => Math.round(r.getBoundingClientRect().height));
        for (const [label, tier] of [[w.ultra, "ultrafast"], [w.standard, "default"], [w.fast, "priority"], [w.asks, ""]]) {
          const y = await top(), h = await tall();
          await pick.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          const items = (await menu.locator(".pm-item .pm-name").allTextContents()).map((s) => s.trim());
          assert.deepEqual(items, [w.asks, w.standard, w.fast, w.ultra]);
          const mb = await menu.boundingBox();
          assert(mb.x >= 0 && mb.x + mb.width <= width, `the menu runs off the window: ${JSON.stringify(mb)}`);
          await menu.locator(".pm-item").filter({ has: page.locator(".pm-name", { hasText: new RegExp("^" + label + "$") }) }).click();
          await page.locator("#codexServiceTierPick button", { hasText: new RegExp("^\\s*" + label + "\\s*$") }).waitFor();
          assert.deepEqual(posted.at(-1), ["codex-service-tier", { tier }]);
          await page.waitForTimeout(200);
          if (await tall() === h) assert(Math.abs((await top()) - y) <= 1, `picking ${label} moved the button from ${y} to ${await top()}`);
        }
        assert.equal(posted.length, 4, JSON.stringify(posted));
        assert.deepEqual(errors, []);
      });
    }
  }
}
