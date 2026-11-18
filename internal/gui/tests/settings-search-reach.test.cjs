// Run with Node's test runner and Playwright on the module path; see README.md.
// What Settings' search reaches beyond the rows drawn in its page:
// - a tab's id is not a word of it: "usage" doesn't bring all of
//   Allowances (its id), only the rows that say it;
// - a hit under a folded Advanced (Network and sharing's Name) is
//   unfolded while the search lasts, and folded again when it ends;
// - the settings an agent's sheet builds as it opens (Claude Code's
//   prompt cache and commit signature, its own fields) and the Claude
//   editor's run mode (claude -p) are found as links, and Open reaches
//   the row, lit.
// Every language, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, agentRow } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, posts) {
  const cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    proxy: "", redact: false, redactPersonal: false, redactWords: [], trayUsage: "", trayUsageEvery: 3,
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    claudeCache: [{ knob: "cache_ttl", value: "1h" }, { knob: "subagent_cache_ttl" }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  const claude = { ...agentRow("claude", "Claude Code"), own: [{ key: "permissions.defaultMode", label: "permission mode", value: "", options: [{ value: "plan" }] }] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() !== "GET" && url.pathname !== "/api/update/using" && url.pathname !== "/api/page-away") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [claude, agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/claude/mode") return json({ why: "", tui: true });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex", "claude"], { codex: "ChatGPT", claude: "Claude" }));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const parts = (page) => page.evaluate(() => [...document.querySelectorAll("#view-settings > [role=tabpanel]")].filter((p) => p.getClientRects().length).map((p) => p.id.replace("setPage-", "")));
const seen = (page, sel) => page.locator(sel).first().evaluate((e) => !!e.getClientRects().length && getComputedStyle(e).visibility !== "hidden");

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": what Settings' search reaches", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, posts) => {
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await page.locator("#themeSegs .opt").first().waitFor();
      await page.locator("#netMoreList .instance-name-row").waitFor({ state: "attached" });
      return page;
    };
    const type = async (page, s) => {
      await page.locator("#setQ").fill(s);
      await page.waitForTimeout(150);
    };
    const link = (page, name) => page.locator("#provPrefLinks .agent-pref-link:visible", { has: page.locator(".name", { hasText: name }) });

    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await open(lang, posts);
        const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);

        // "usage" is Allowances' id, not a word of it: its rows that don't
        // say it stay out
        await type(page, "usage");
        assert.equal(await seen(page, "#trayCostSegs"), false, "Menu bar cost isn't brought by the tab's id");

        // a hit under Network and sharing's folded Advanced is unfolded
        assert.equal(await page.locator("#netMore").evaluate((d) => d.open), false);
        await type(page, await tr("Shown in the browser tab’s title"));
        assert((await parts(page)).includes("network"), "under Network and sharing");
        assert.equal(await page.locator("#netMore").evaluate((d) => d.open), true, "its Advanced unfolded");
        assert(await seen(page, "#netMoreList .instance-name-row"), "the Name row shows");
        // and folded again when the search ends
        await type(page, "");
        assert.equal(await page.locator("#netMore").evaluate((d) => d.open), false, "folded again after the search");

        // Claude Code's sheet's rows, found as links
        for (const [word, name] of [["Co-Authored", "Commit signature (Co-Authored-By)"], [(await tr("Prompt cache · subagents")).split(" · ")[0], "Prompt cache · subagents"], [await tr("Permission mode"), "Permission mode"]]) {
          await type(page, word);
          const l = link(page, await tr(name));
          assert.equal(await l.count(), 1, `"${word}" finds ${name}`);
          assert.equal(await l.locator(".sub").textContent(), await tr("In {agent}'s settings, on the Agents page", { agent: "Claude Code" }));
        }
        // and the Claude editor's run mode, by claude -p
        await type(page, "claude -p");
        assert.equal(await link(page, await tr("Claude Code runs as")).count(), 1, "claude -p finds Claude Code's run mode");
        assert.deepEqual(posts, [], "searching posts nothing");

        // Open reaches the row in the sheet, lit
        await type(page, "Co-Authored");
        await link(page, await tr("Commit signature (Co-Authored-By)")).locator("button").click();
        await page.locator("#modal .claude-attribution.flash").waitFor();
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        // its own field, under the sheet's folded Advanced, unfolded
        await page.goto("http://magpie.test/?view=settings&tab=general");
        await page.locator("#themeSegs .opt").first().waitFor();
        await type(page, await tr("Permission mode"));
        await link(page, await tr("Permission mode")).locator("button").click();
        await page.locator("#modal .own-fields[open] .own-field.flash").waitFor();
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        // the run mode's Open reaches the Claude editor's row
        await page.goto("http://magpie.test/?view=settings&tab=general");
        await page.locator("#themeSegs .opt").first().waitFor();
        await type(page, "claude -p");
        await link(page, await tr("Claude Code runs as")).locator("button").click();
        await page.locator(".editor .claude-mode.flash").waitFor();
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }
  });
}
