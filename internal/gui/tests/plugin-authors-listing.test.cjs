// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin its author publishes from their own repository is listed in
// registry.json without "community": true (the owner on
// magpie-community/plugins#479). Discover doesn't browse it: not in
// Agents, nor any section. A search finds it, by its name or its
// publisher, under its publisher's name and never with the magpie
// community badge, on its card and on its page. PinK's listing is
// registry.json's before plugins#617. Every language, both engines, at a
// narrow width too; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const PINK = "magpie-plugin-pink";
const RIVER = "@magpie-community/opencode-river-auth";
const KILO = "@magpie-community/agent-kilo";
// as /api/plugins/listings gives them: Listing's fields, community left out
// when false (omitempty), and no npm yet
const listings = () => [
  { package: RIVER, name: "River", providers: ["river"], community: true,
    summary: { en: "River (river.ai) pay-as-you-go inference with your API key: per-account models from /v1/models, documented reasoning efforts and live prices from River's public pricing endpoint.", zh: "用你的 API key 接入 River (river.ai) 按量计费推理：按账号从 /v1/models 列出模型，带官方推理档位与实时价格。" } },
  { package: KILO, name: "Kilo Code", kind: "agent", community: true, summary: { en: "Kilo Code on magpie's Agents page.", zh: "把 Kilo Code 加到 Agent 页。" } },
  { package: PINK, name: "PinK", kind: "agent", icon: "https://raw.githubusercontent.com/YuchenWell/magpie-plugin-pink/main/assets/pink.png",
    summary: { en: "Connect PinK to Magpie's models from the Agents page and track gateway usage and cost. Disconnecting restores the managed configuration.", zh: "在 Agent 页面连接 PinK，使用 Magpie 中配置的模型并查看调用用量与费用；断开时恢复插件管理的配置。" } },
];
const npm = {
  [PINK]: { version: "0.1.1", publisher: "zhouzhenzhou", license: "MIT", weekly: 12 },
  [RIVER]: { version: "0.1.3", publisher: "GitHub Actions", weekly: 40 },
  [KILO]: { version: "0.1.0", weekly: 3 },
};
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    // npm's own search has it too: still one card
    if (url.pathname === "/api/plugins/search") return json({ hits: [{ package: PINK, version: "0.1.1", publisher: "zhouzhenzhou" }] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/plugins/page") return json({ readme: "# PinK", updated: "2026-10-30T00:00:00Z" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { agents: "Agents", community: "magpie community" },
  zh: { agents: "Agent", community: "magpie 社区" },
  "zh-TW": { agents: "Agent", community: "magpie 社群" },
  ja: { agents: "エージェント", community: "magpie コミュニティ" },
  de: { agents: "Agenten", community: "magpie-Community" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an author's own listing is found by a search, under its publisher", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const card = (pkg) => view.locator(`.pm-card[data-pkg="${pkg}"]`);
        const shot = (name) => process.env.ARTIFACT_DIR && view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-authors-${name}-${engine}-${lang}-${width}.png`) });

        // browsed: the community's agent in Agents, PinK nowhere, after
        // npm has answered too (a redraw)
        const agents = view.locator(".pm-sec.pm-agents");
        await agents.locator(".pm-sechead h3", { hasText: new RegExp("^" + w.agents + "$") }).waitFor();
        await card(KILO).locator(".pm-ver").waitFor();
        await card(RIVER).waitFor();
        assert.equal(await card(PINK).count(), 0, "PinK is browsed");
        assert.doesNotMatch(await view.innerText(), /PinK/);
        assert.equal(await card(RIVER).locator(".pm-verified").count(), 1);
        assert.equal((await card(RIVER).locator(".pm-verified").innerText()).trim(), w.community);
        await shot("browse");

        // searched by its name: its card once, by its publisher, no badge
        const q = view.locator(".pm-find input");
        await q.fill("pink");
        await card(PINK).waitFor();
        await card(PINK).locator(".pm-ver").waitFor();
        assert.equal(await card(PINK).count(), 1, "the listing and npm's hit are one card");
        assert.equal(await card(PINK).locator(".pm-verified").count(), 0);
        assert.equal((await card(PINK).locator(".pm-by").innerText()).trim(), "zhouzhenzhou");
        assert.doesNotMatch(await card(PINK).innerText(), new RegExp(w.community));
        assert.equal(await card(RIVER).count(), 0);
        await shot("search");

        // the search cleared: browsed again, PinK gone
        await q.fill("");
        await card(KILO).waitFor();
        assert.equal(await card(PINK).count(), 0);

        // found by its publisher's name too
        await q.fill("zhouzhen");
        await card(PINK).waitFor();

        // its page says the same: publisher · package, never the badge
        await card(PINK).locator(".pm-sum").click();
        const dlg = page.locator("#modal .pm-detail");
        await dlg.waitFor();
        assert.equal((await dlg.locator(".pm-dhead .pm-by").innerText()).trim(), "zhouzhenzhou · " + PINK);
        assert.doesNotMatch(await dlg.locator(".pm-dhead").innerText(), new RegExp(w.community));
        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(over <= 0, "page scrolls sideways by " + over);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
