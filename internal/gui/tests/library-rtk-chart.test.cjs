// Run with Node's test runner and Playwright on the module path; see README.md.
// jasonchia58 on X: the number at the top right of Library → RTK's chart
// (1.6M) stopped changing after some day, while the total under the chart
// kept up. That number is the bars' scale, the busiest day in the range: it
// changes only when a day passes the old busiest. It now says so ("peak
// day 1.2M"), in every language, and is gone when the range has nothing.
// The days are what real rtk 1.0.1 printed for rtk gain --daily --format
// json (internal/library/testdata/rtk-gain-daily.json), moved to end
// yesterday. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/jason";
const real = require("../../library/testdata/rtk-gain-daily.json");
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const parse = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };
// as internal/library parseGain gives them, the last one ago days back
const daysEnding = (ago) => {
  const y = new Date(); y.setHours(0, 0, 0, 0); y.setDate(y.getDate() - ago);
  const shift = Math.round((y - parse(real.daily.at(-1).date)) / 864e5);
  return real.daily.map((d) => {
    const at = parse(d.date); at.setDate(at.getDate() + shift);
    return { date: iso(at), commands: d.commands, input: d.input_tokens, saved: d.saved_tokens, pct: d.savings_pct };
  });
};
const s = real.summary;
const gain = { commands: s.total_commands, input: s.total_input, saved: s.total_saved, pct: s.avg_savings_pct };

function server(lang, view) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: { dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME, agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }], instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [] } });
    if (url.pathname === "/api/library/rtk") return route.fulfill({ json: { path: "/opt/homebrew/bin/rtk", version: "1.0.1", url: "https://www.rtk-ai.app", agents: [{ id: "claude", name: "Claude Code", icon: "", on: true }], ...view } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the busiest day is 1,205,000 tokens; the range's total 632 commands
const words = {
  en: { peak: "peak day 1.2M", foot: /^865\.0k tokens saved over 632 commands — 27%$/ },
  zh: { peak: "单日最高 1.2M", foot: /632 条命令/ },
  "zh-TW": { peak: "單日最高 1.2M", foot: /632/ },
  ja: { peak: "1日の最大 1.2M", foot: /632/ },
  de: { peak: "Stärkster Tag 1.2M", foot: /632/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": RTK's chart says its top number is the busiest day, not the total", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, width, range, view) => {
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript((r) => { try { localStorage.setItem("magpie.libTab", "rtk"); localStorage.setItem("magpie.rtkRange", r); } catch {} }, range);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, view));
      await page.goto("http://magpie.test/");
      await page.evaluate(() => document.querySelector('button[data-view="library"]').click());
      const chart = page.locator("#view-library .lib-rtk-chart");
      await chart.waitFor();
      return { ctx, chart };
    };
    for (const lang of Object.keys(words)) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const w = words[lang];
        const { ctx, chart } = await open(lang, width, "0", { gain, days: daysEnding(1) });
        assert.equal(await chart.locator(".sess-chart-head .peak").textContent(), w.peak);
        assert.match(await chart.locator(".lib-rtk-foot > span:last-child").textContent(), w.foot);
        // the busiest bar fills the chart: the number is its height
        const full = await chart.locator(".bars .bar").evaluateAll((bs) => bs.filter((b) => [...b.children].reduce((h, i) => h + parseFloat(i.style.height || 0), 0) > 99.9).length);
        assert.equal(full, 1);
        await ctx.close();
      });
    }
    // a range with nothing in it has no scale to tell
    await t.test("nothing in the last 30 days", async () => {
      const { ctx, chart } = await open("en", 980, "30", { gain, days: daysEnding(40) });
      assert.equal(await chart.locator(".lib-rtk-foot > span:last-child").textContent(), "Nothing run through RTK in this time");
      assert.equal(await chart.locator(".peak").count(), 0);
      await ctx.close();
    });
    assert.deepEqual(errors, []);
  });
}
