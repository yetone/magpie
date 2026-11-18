// Run with Node's test runner and Playwright on the module path; see README.md.
// #1984: Settings → the menu bar can show magpie's own tokens and cost for
// today or the last 7 days, beside the allowances or alone. The picker lists
// both after the subscriptions, ticked and saved as a card is ("usage:today",
// "usage:7d"); the pill names a total alone, and counts a mix of them without
// calling it subscriptions. A total clicked in the menu bar opens the Usage
// page's Requests tab, or the panel's Usage tab, at its period. Every language,
// both engines, wide and narrow; no click moves the page; no backend, the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart, reach } = require("./reader.cjs"); // reach: wheeled to as a reader would, rows taller since their lines wrap whole (arculongus on Discord); a Playwright scroll is undone

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max",
    windows: [{ name: "5-hour", used: 42 }, { name: "Weekly", used: 18 }] },
  { provider: "copilot", name: "Copilot", icon: "githubcopilot", windows: [{ name: "Premium", used: 63 }] },
];

const want = {
  en: { off: "Off", today: "Tokens and cost today", week: "Tokens and cost, last 7 days", note: "Gateway and session-log calls, as Usage › Requests counts them", two: "2 shown", subs: "2 subscriptions", days: "7 days" },
  zh: { off: "关闭", today: "今天的 token 数和费用", week: "最近 7 天的 token 数和费用", note: "网关调用与会话日志调用，与用量 › 请求的统计一致", two: "已选 2 项", subs: "2 个订阅", days: "7 天" },
  "zh-TW": { off: "關閉", today: "今天的 token 數和費用", week: "最近 7 天的 token 數和費用", note: "閘道呼叫與會話日誌呼叫，與用量 › 請求的統計一致", two: "已選 2 項", subs: "2 個訂閱", days: "7 天" },
  ja: { off: "オフ", today: "今日のトークン数と費用", week: "直近 7 日間のトークン数と費用", note: "ゲートウェイとセッションログの呼び出し（使用量 › リクエストと同じ集計）", two: "2 件を表示", subs: "2 件のサブスクリプション", days: "7 日" },
  de: { off: "Aus", today: "Tokens und Kosten heute", week: "Tokens und Kosten, letzte 7 Tage", note: "Aufrufe über den Zugangspunkt und aus Sitzungslogs, wie Nutzung › Anfragen sie zählt", two: "2 angezeigt", subs: "2 Abos", days: "7 Tage" },
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

const empty = (period) => ({
  period, rows: [], offset: 0, total: 0, calls: 0, errors: 0, input: 0, output: 0, cache_write: 0, cache_read: 0, reasoning: 0, cost: 0, unpriced: 0,
  bucket: period === "today" ? "hour" : "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [], providers: [],
});

// server answers as magpie does: trayUsage kept as the first of trayUsages
function server(lang, posts, asked, web = false) {
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body, trayUsage: (body.trayUsages || [])[0] || "" };
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/requests" || url.pathname === "/api/usage") {
      asked.push(url.pathname + "?" + url.searchParams);
      return json(empty(url.searchParams.get("period")));
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);
const pillIs = (page, s) => page.waitForFunction((s) => document.querySelector("#trayUsagePick button").textContent.trim() === s, s);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(want))) {
    test(`${engine} ${lang}: today's and the week's tokens in the menu bar (#1984)`, async (t) => {
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      // kept where ARTIFACT_DIR says, before each page is closed
      const shot = async (page, name) => {
        if (!process.env.ARTIFACT_DIR) return;
        await page.waitForTimeout(400); // the menu's, the tab's glide settled
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-tray-totals-${name}.png`) });
      };

      for (const width of [1100, 420]) {
        await t.test(`${width} wide`, async () => {
          const errors = [], posts = [], asked = [];
          const context = await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, posts, asked));

          await page.goto("http://magpie.test/?view=usage");
          await page.locator("#prefs").click();
          await openPart(page, "usage");
          const pill = page.locator("#trayUsagePick button");
          await pill.waitFor();
          await pillIs(page, w.off);
          await reach(page, pill);
          const before = await view(page);
          const pillY = (await pill.boundingBox()).y;

          // the two totals follow the subscriptions, each with a box to tick
          await pill.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          const items = menu.locator(".pm-item");
          assert.deepEqual((await items.locator(".pm-name").allTextContents()).map((s) => s.trim()), [w.off, "Claude Code", "Copilot", w.today, w.week]);
          assert.deepEqual((await items.locator(".pm-note").allTextContents()).slice(3).map((s) => s.trim()), [w.note, w.note]);
          assert.deepEqual(await items.evaluateAll((bs) => bs.slice(3).map((b) => b.getAttribute("role"))), ["menuitemcheckbox", "menuitemcheckbox"]);
          // nothing runs out of a narrow window
          const box = await menu.boundingBox();
          assert(box.x >= 0 && box.x + box.width <= width + 0.5, `the menu at ${box.x}+${box.width} in ${width}`);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await shot(page, `menu-${width}`);

          // today's alone: saved as its id, the pill named for it
          await items.nth(3).click();
          await page.keyboard.press("Escape");
          await pillIs(page, w.today);
          assert.deepEqual(posts.at(-1).trayUsages, ["usage:today"]);
          assert.equal(await page.locator("#trayEveryRow").isVisible(), true, "how often it is read again shows with a total picked");

          // with a subscription beside it: counted, not called subscriptions
          await pill.click();
          await items.nth(2).click();
          await page.keyboard.press("Escape");
          await pillIs(page, w.two);
          assert.deepEqual(posts.at(-1).trayUsages, ["copilot", "usage:today"], "in the menu's order");

          // two subscriptions alone are still subscriptions
          await pill.click();
          await items.nth(3).click();
          await items.nth(1).click();
          await page.keyboard.press("Escape");
          await pillIs(page, w.subs);
          assert.deepEqual(posts.at(-1).trayUsages, ["claude|a@b.c", "copilot"]);

          // the week's alone
          await pill.click();
          await items.nth(1).click();
          await items.nth(2).click();
          await items.nth(4).click();
          await page.keyboard.press("Escape");
          await pillIs(page, w.week);
          assert.deepEqual(posts.at(-1).trayUsages, ["usage:7d"]);

          await page.waitForTimeout(300);
          // the pill's name changes its width, and the row's line, wrapped
          // whole beside it (arculongus on Discord), can take a line more or
          // less: the page then moves only to keep the pill under the pointer (WebKit
          // scrolls by whole pixels, so four holds can leave it ~1.3px off)
          if (await view(page) !== before) assert.ok(Math.abs((await pill.boundingBox()).y - pillY) <= 2, "a click moved the page, and the pill with it");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }

      // clicked in the menu bar with the window as the tray's: the Requests
      // tab, which sums what the total does, at its period (quotaView), the
      // address left clean
      await t.test("window", async () => {
        const errors = [], asked = [];
        const context = await browser.newContext({ viewport: { width: 1100, height: 560 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [], asked));
        await page.goto("http://magpie.test/?view=usage&tab=requests&period=7d");
        await page.waitForFunction((s) => document.querySelector("#period .opt.on")?.textContent.trim() === s, w.days);
        await page.waitForFunction(() => document.querySelector("#ledDash") || document.querySelector("#ledKpi"));
        assert(asked.some((a) => a.startsWith("/api/usage/requests?") && a.includes("period=7d")), "asked: " + asked.join(" "));
        assert.equal(new URL(page.url()).searchParams.get("period"), null);
        await shot(page, "window");
        assert.deepEqual(errors, []);
        await context.close();
      });

      // and with the panel as the tray's: its Usage tab, at the period
      await t.test("panel", async () => {
        const errors = [], asked = [];
        const context = await browser.newContext({ viewport: { width: 440, height: 400 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [], asked, true));
        await page.goto("http://magpie.test/?mode=panel");
        await page.locator('[data-ptab="agents"].on').waitFor();
        await page.evaluate(() => panelUseFocus("7d"));
        await page.waitForFunction(() => document.body.dataset.ptab === "stats");
        for (let i = 0; i < 60 && !asked.some((a) => a.includes("period=7d")); i++) await page.waitForTimeout(40);
        assert(asked.some((a) => a.startsWith("/api/usage/requests?") && a.includes("period=7d")), "asked: " + asked.join(" "));
        await shot(page, "panel");
        // a period that isn't the panel's does nothing
        const n = asked.length;
        await page.evaluate(() => panelUseFocus("3d"));
        await page.waitForTimeout(200);
        assert.equal(asked.length, n);
        assert.deepEqual(errors, []);
        await context.close();
      });
    });
  }
}
