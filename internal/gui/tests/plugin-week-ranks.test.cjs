// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover's week (the owner, 11-23: 「为什么没有好评榜」, and npm's downloads
// aren't downloads in magpie, while GitHub's stars are easy to game: a
// plugin in a repository starred for something else leads forever). The
// two ranks beside Today's pick go by what magpie counts: Most used (who
// used it in magpie last week) and Most liked (likes, given only where it
// is installed). A repository with 47k stars and little use stays where
// its use puts it, in the ranks and under the Most liked sort. With no
// counts from usemagpie.ai, npm's downloads stand in and there is no Most
// liked. Every language, wide and narrow, both engines; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const A = "@magpie-community/opencode-alpha-auth", B = "@magpie-community/opencode-beta-auth", C = "@magpie-community/opencode-gamma-auth";
const GH = "github:someone/rsshub-ish";
const listings = () => [
  { package: A, name: "Alpha", providers: ["alpha"], community: true, summary: { en: "Alpha." } },
  { package: B, name: "Beta", providers: ["beta"], community: true, summary: { en: "Beta." } },
  { package: C, name: "Gamma", providers: ["gamma"], community: true, summary: { en: "Gamma." } },
];
// Gamma is npm's most downloaded; magpie's users rank Alpha first
const npm = { [A]: { version: "0.1.0", weekly: 10 }, [B]: { version: "0.1.0", weekly: 20 }, [C]: { version: "0.1.0", weekly: 90000 } };
const repos = [{ spec: GH, repo: "someone/rsshub-ish", url: "https://github.com/someone/rsshub-ish", owner: "someone", description: "Everything", stars: 47000, kind: "provider" }];
const counts = { [A]: { users: 900, likes: 2 }, [B]: { users: 50, likes: 30 }, [C]: { users: 300 }, [GH]: { users: 5 } };
const state = { bun: true, bunVersion: "1.3.0", plugins: [] };

function server(lang, live) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state });
    if (url.pathname === "/api/plugins/counts") return live ? json({ plugins: counts, liked: [] }) : route.fulfill({ status: 500, json: { error: "offline" } });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/github") return json({ repos, topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { used: "Most used", liked: "Most liked", npm: "Most downloaded", sort: "Sort" },
  zh: { used: "用的人最多", liked: "最受好评", npm: "下载最多", sort: "排序" },
  "zh-TW": { used: "用的人最多", liked: "最受好評", npm: "下載最多", sort: "排序" },
  ja: { used: "利用者数上位", liked: "高評価順", npm: "ダウンロード数上位", sort: "並べ替え" },
  de: { used: "Am meisten genutzt", liked: "Beliebt bei Nutzern", npm: "Am meisten geladen", sort: "Sortierung" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the week's ranks go by use and likes in magpie, never by GitHub's stars", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) for (const width of [1180, 420]) for (const live of [true, false]) {
      await t.test(`${lang} ${width} ${live ? "counted" : "no counts"}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, live));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.locator(".pm-recs .pm-hero[data-pkg]").waitFor();
        await view.locator(".pm-recs .pm-rrow").first().waitFor();
        const ranks = () => view.locator(".pm-recs .pm-rank").evaluateAll((bs) => bs.map((b) => ({
          head: b.querySelector(".pm-rhead b").textContent.trim(),
          rows: [...b.querySelectorAll(".pm-rrow")].map((r) => r.dataset.pkg),
        })));
        const w = words[lang];
        if (live) {
          assert.deepEqual(await ranks(), [{ head: w.used, rows: [A, C, B, GH] }, { head: w.liked, rows: [B, A] }]);
          // the cards: Most popular goes by use too, then Most liked by likes,
          // the 47k-star repository after Gamma, which more people use
          const order = () => view.locator(".pm-sec:not(.pm-recs) .pm-card[data-pkg]").evaluateAll((cs) => cs.map((c) => c.dataset.pkg));
          assert.deepEqual((await order()).filter((k) => [A, B, C, GH].includes(k)), [A, C, B, GH]);
          await view.locator(".pm-pop").filter({ has: page.locator("span", { hasText: new RegExp("^" + w.sort + "$") }) }).click();
          await page.locator(".pm-sortmenu .pm-item", { hasText: w.liked }).click();
          await page.waitForFunction(() => document.querySelector("#view-plugins .pm-sec:not(.pm-recs) .pm-card[data-pkg]")?.dataset.pkg?.includes("beta"));
          assert.deepEqual((await order()).filter((k) => [A, B, C, GH].includes(k)), [B, A, C, GH]);
        } else {
          assert.deepEqual(await ranks(), [{ head: w.npm, rows: [C, B, A] }]);
          assert.ok(await view.locator(".pm-recs .pm-rec.two").count());
        }
        // a rank's rows stay inside their box, narrow or wide
        const spill = await view.locator(".pm-recs .pm-rrow").evaluateAll((rs) => rs.filter((r) => {
          const box = r.closest(".pm-rank").getBoundingClientRect(), b = r.getBoundingClientRect();
          return b.left < box.left - 0.5 || b.right > box.right + 0.5;
        }).length);
        assert.equal(spill, 0);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
