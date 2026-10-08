// Run with Node's test runner and Playwright on the module path; see README.md.
// A card's resets, hovered, list each one left and when it runs out, a line
// each, soonest first (#960, Magixyne: the tooltip only restated the first
// one's date the card already shows). A Codex account's are numbered, one
// that never runs out last; a GLM team's say which window each resets,
// and so do a GLM Coding Plan key's own (#1191), each named for whose plan
// it is; a plugin's, whose list isn't told, keep the one line. The Usage page's card
// and the menu bar panel's, at 900px and 440px. English, Chinese, Japanese
// and German, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const day = 864e5, base = Date.now();
const a = new Date(base + 2 * day).toISOString(), b = new Date(base + 9 * day).toISOString(), c = new Date(base + 20 * day).toISOString();
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "me@example.com", plan: "Plus",
    windows: [{ name: "5 hours", used: 40, resetsAt: b }, { name: "7 days", used: 60, resetsAt: b }],
    resets: { count: 3, until: a, each: [{ until: a }, { until: c }, {}] } },
  { provider: "zhipu", name: "GLM Coding", icon: "zhipu-color", user: "team@example.com", plan: "Team",
    windows: [{ name: "5 hours", used: 30 }],
    resets: { count: 3, byWindow: true, team: true, fiveHour: 2, weekly: 1, until: a,
      each: [{ until: a, window: "fiveHour" }, { until: b, window: "weekly" }, { until: c, window: "fiveHour" }] } },
  { provider: "codex-plugin", name: "Codex (plugin)", user: "p@example.com", plan: "Plus",
    windows: [{ name: "7 days", used: 10, resetsAt: b }], resets: { count: 2, until: b } },
  { provider: "glm", name: "GLM", icon: "zhipu-color", user: "work", plan: "pro",
    windows: [{ name: "5 hours", used: 20 }],
    resets: { count: 2, byWindow: true, fiveHour: 1, weekly: 1, until: b,
      each: [{ until: b, window: "weekly" }, { until: c, window: "fiveHour" }] } },
];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["codex", "zhipu", "codex-plugin", "glm"], trayUsageEvery: 3 };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    nth: (n, w) => `Reset ${n} runs out ${w}`, never: (n) => `Reset ${n} never runs out`,
    five: (w) => `Five-hour reset runs out ${w}`, week: (w) => `Weekly reset runs out ${w}`,
    first: (w) => `The first runs out ${w}`,
    team: "The team plan's resets, used on bigmodel.cn or z.ai",
    own: "The plan's resets, used on bigmodel.cn or z.ai",
  },
  zh: {
    nth: (n, w) => `第 ${n} 张 ${w} 到期`, never: (n) => `第 ${n} 张永不过期`,
    five: (w) => `5 小时重置卡 ${w} 到期`, week: (w) => `每周重置卡 ${w} 到期`,
    first: (w) => `最早的一张 ${w} 到期`,
    team: "团队套餐的重置卡，在 bigmodel.cn 或 z.ai 上使用",
    own: "套餐的重置卡，在 bigmodel.cn 或 z.ai 上使用",
  },
  ja: {
    nth: (n, w) => `${n}つ目のリセットは${w}に期限切れ`, never: (n) => `${n}つ目のリセットは期限なし`,
    five: (w) => `5時間リセットは${w}に期限切れ`, week: (w) => `週次リセットは${w}に期限切れ`,
    first: (w) => `最初のものは${w}に期限切れ`,
    team: "チームプランのリセット。bigmodel.cn または z.ai で使用します",
    own: "プランのリセット。bigmodel.cn または z.ai で使用します",
  },
  de: {
    nth: (n, w) => `Reset ${n} läuft ${w} ab`, never: (n) => `Reset ${n} läuft nie ab`,
    five: (w) => `Fünf-Stunden-Reset läuft ${w} ab`, week: (w) => `Wochen-Reset läuft ${w} ab`,
    first: (w) => `Der erste läuft ${w} ab`,
    team: "Die Resets des Team-Tarifs, einlösbar auf bigmodel.cn oder z.ai",
    own: "Die Resets des Tarifs, einlösbar auf bigmodel.cn oder z.ai",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a card's resets, hovered, list each one and when it runs out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (url, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto(url);
        return page;
      };
      // the titles the three cards' resets should carry, dates as the page says them
      const want = async (page) => {
        const [wa, wb, wc] = await page.evaluate((ds) => ds.map((d) => new Date(d).toLocaleString()), [a, b, c]);
        return {
          codex: [w.nth(1, wa), w.nth(2, wc), w.never(3)],
          team: [w.five(wa), w.week(wb), w.five(wc)],
          plugin: w.first(wb),
          own: [w.week(wb), w.five(wc)],
        };
      };
      const check = async (page, sel) => {
        await page.locator(sel).first().waitFor();
        const titles = await page.locator(sel + " .resets-words").evaluateAll((es) => es.map((e) => e.title));
        assert.equal(titles.length, 4, "every card's resets are told");
        const ww = await want(page);
        const [codex, team, plugin, own] = titles.map((s) => s.split("\n"));
        assert.deepEqual(codex.slice(0, 3), ww.codex, "a Codex account's, numbered, soonest first, the one that never runs out last");
        assert.equal(codex.length, 4, "the Auto-use line follows them");
        assert.equal(team.length, 4, "the team plan's line, then each reset");
        assert.deepEqual(team.slice(1), ww.team, "a team's, each its window, soonest first");
        assert.deepEqual(plugin, [ww.plugin], "a plugin's, not listed, keeps the one line");
        assert.equal(team[0], w.team, "a team's say they are the team plan's");
        assert.deepEqual(own, [w.own, ...ww.own], "a GLM key's own say they are the plan's, then each, soonest first");
        // the card itself says what it did
        const shown = await page.locator(sel + " .resets-words .resets-n").allTextContents();
        assert.equal(shown.length, 4);
        // nothing cut off: the resets' words stay inside their card
        const fits = await page.locator(sel + " .resets-words").evaluateAll((es) => es.every((e) => {
          const card = e.closest(".pq-card, .subscription-card, .subscription-account") || document.body;
          return e.getBoundingClientRect().right <= card.getBoundingClientRect().right + 0.5;
        }));
        assert(fits, "the resets' words fit their card");
      };

      for (const width of [900, 440]) {
        const page = await open("http://magpie.test/?view=usage", { width, height: 800 });
        await check(page, ".quota-resets");
      }
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 600 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      await check(panel, ".pq-resets");
      assert.deepEqual(errors, []);
    });
  }
}
