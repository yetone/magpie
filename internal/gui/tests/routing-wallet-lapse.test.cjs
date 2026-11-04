// Run with Node's test runner and Playwright on the module path; see README.md.
// #3964 (MYCF711): a prepaid wallet's points come in batches that are lost
// on their own days. SuperMagic's plugin (magpie-magicrew-auth daca18e)
// sends its balance window with the soonest batch (runsOutAt, runsOut:
// 185 points at 23:59 today) and the day's, timed and permanent points as
// rows set aside. The Usage card said all of it; the Routing page's rows
// said only the total, "6,291 积分 left". Now the row says what goes
// first too, as the Usage card words it: "6,291 积分 left · 185 积分
// expire 23:59", on the stage and in the accounts listed below, and the
// tooltips of the stage and the list name the four rows. A wallet whose card tells no
// lapse, and an account with a share, read as before. Every language, at
// a window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1000).toISOString();
const later = (h) => new Date(now.getTime() + h * 3600e3).toISOString();
// as the gateway sends them (Weighed)
const seat = (who, balance) => ({ id: `magicrew#${who}`, provider: "magicrew", name: "SuperMagic", who, kind: "account", agent: "magicrew", model: "magic-1", plan: "积分制",
  known: true, used: 0, balance, unit: "积分", renews: ["0001-01-01T00:00:00Z"], pace: 100 / 168 });
const share = { id: "claude#fine@example.com", provider: "claude", name: "Claude", who: "fine@example.com", kind: "account", agent: "claude", model: "magic-1", plan: "Max",
  known: true, used: 40, renews: [later(30)], pace: 1 };
// as /api/usage/quotas sends the plugin's reading: the balance window with
// its soonest batch, then the rows set aside as text (quotaOfPlugin)
const text = (name, display) => ({ name, used: 0, remaining: 100, display, unit: "积分", text: true });
const quotas = [
  { kind: "subscription", provider: "magicrew", name: "SuperMagic", user: "big@example.com", plan: "积分制", windows: [
    { name: "积分余额", used: 0, remaining: 100, display: "6,291 积分", unit: "积分", balance: 6291, runsOutAt: later(3), runsOut: 185 },
    text("今日积分", "185 / 200 积分 · 今日 23:59 到期"),
    text("限时积分", "1,106 积分 · 最早 2026-11-03 到期"),
    text("永久积分", "5,000 积分"),
  ] },
  { kind: "subscription", provider: "magicrew", name: "SuperMagic", user: "small@example.com", plan: "积分制", windows: [
    { name: "积分余额", used: 0, remaining: 100, display: "750 积分", unit: "积分", balance: 750, runsOutAt: later(30), runsOut: 50 },
  ] },
  { kind: "subscription", provider: "magicrew", name: "SuperMagic", user: "none@example.com", plan: "积分制", windows: [
    { name: "积分余额", used: 0, remaining: 100, display: "300 积分", unit: "积分", balance: 300 },
  ] },
  { kind: "subscription", provider: "claude", name: "Claude", user: "fine@example.com", windows: [{ name: "5 hours", used: 40, remaining: 60, resetsAt: later(30) }] },
];

function serve(lang) {
  const order = [seat("big@example.com", 6291), seat("small@example.com", 750), seat("none@example.com", 300), share];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "magic-1", provider: "magicrew", done: true, status: 200, ms: 900, tokens: 1200, order,
    tries: [{ id: order[0].id, model: "magic-1", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a wallet's row says what of it is lost first, beside its total`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          try {
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (error) => errors.push(error.message));
            await page.route("**/*", serve(lang));
            await page.goto("http://magpie.test/?view=routing");
            const req = page.locator(".rt-req").first();
            await req.waitFor();
            if (await req.getAttribute("aria-pressed") !== "true") await req.click();
            const tr = (key, vars) => page.evaluate(([lang, key, vars]) => {
              const s = (lang !== "en" && I18N[lang]?.[key]) || key;
              return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
            }, [lang, key, vars]);
            // the Usage card's own words for a window's soonest lapse
            const lapse = (i) => page.evaluate((w) => runsOutText(w), quotas[i].windows[0]);
            const bigOut = await lapse(0), smallOut = await lapse(1);
            assert.ok(bigOut.includes("185 积分") && smallOut.includes("50 积分"), `${bigOut} / ${smallOut}`);
            const big = await tr("{n} left", { n: "6,291 积分" }) + " · " + bigOut;
            const small = await tr("{n} left", { n: "750 积分" }) + " · " + smallOut;
            const none = await tr("{n} left", { n: "300 积分" });
            // the stage's rows: the one behind the first says its figure and its lapse
            const row = (id) => page.locator(`.rt-accts > li[title^="${id}"]`).first();
            // the Usage page's readings come in beside the trace, and the stage is
            // drawn again with them: read the row until it says what it should,
            // or until it has had time to, and compare what it said last
            await page.waitForFunction(() => [...document.querySelectorAll(".rt-act .st")].some((e) => e.title));
            const settled = async (id, want) => {
              let got = "";
              for (let i = 0; i < 20 && got !== want; i++) {
                if (i) await page.waitForTimeout(100);
                got = (await row(id).locator("em").textContent()) || "";
              }
              return got;
            };
            assert.equal(await settled("magicrew#small@example.com", small), small);
            assert.equal(await settled("magicrew#none@example.com", none), none, "a wallet with no lapse told reads as before");
            assert.ok(!(await row("claude#fine@example.com").locator("em").textContent()).includes("积分"), "an account with a share has no lapse");
            // the tooltip names the four rows the Usage card shows
            const tip = await row("magicrew#big@example.com").getAttribute("title");
            for (const s of ["185 / 200 积分 · 今日 23:59 到期", "1,106 积分 · 最早 2026-11-03 到期", "5,000 积分"]) assert.ok(tip.includes(s), tip);
            // the accounts listed below: the figure and its lapse, then when it
            // was read, each window on hover over it
            const acts = page.locator(".rt-act");
            await acts.filter({ hasText: "6,291" }).first().waitFor();
            const list = await acts.evaluateAll((as) => as.map((a) => ({ st: a.querySelector(".st")?.textContent || "", wins: a.querySelector(".st")?.title || "" })));
            const b = list.find((a) => a.st.startsWith(big + " · "));
            assert.ok(b, `${big}: ${JSON.stringify(list)}`);
            assert.ok(b.wins.includes("185 / 200 积分 · 今日 23:59 到期") && b.wins.includes("5,000 积分"), b.wins);
            assert.ok(list.some((a) => a.st.startsWith(small + " · ")), JSON.stringify(list));
            // no lapse told: its figure, then when it was read, and nothing between
            assert.ok(list.some((a) => a.st.startsWith(none + " · ") && a.st.split(" · ").length === 2), JSON.stringify(list));
            const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${over}px past`);
            assert.deepEqual(errors, []);
          } finally {
            await page.close();
          }
        });
      }
    }
  });
}
