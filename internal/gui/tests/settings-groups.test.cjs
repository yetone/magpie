// Run with Node's test runner and Playwright on the module path; see README.md.
// The Settings page's warm-ups and check-in, one section with a tab per
// service (#124 Mrhe525: tabs rather than three stacked groups): in the
// Settings page's Usage part (#471), after its list, where a heading would
// be, the tabs Codex, Claude Code
// and WorkBuddy, and under them one card with the picked service's rows —
// Codex's and Claude Code's Warm up on reset and Daily warm-up, WorkBuddy's
// Daily check-in — the lines under them short, with how the last warm-up and
// today's check-in went still on them. The WorkBuddy tab is there only with
// an account signed in; a daily warm-up's time field only while it is on.
// The tab is remembered across a reload (Codex when the remembered one is
// gone), the arrows move along the tabs, every control posts the same
// setting it did, and a click on a tab or a control with the page scrolled
// down moves nothing. English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posts, over) {
  let cur = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body };
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: {
    reset: "Warm up on reset", daily: "Daily warm-up", checkin: "Daily check-in",
    on: "On", off: "Off", started: "last started", checked: "Ann checked in today +2, 3-day streak", tabs: "Warm-up and check-in",
  },
  zh: {
    reset: "窗口重置时预热", daily: "每日定时预热", checkin: "每日自动签到",
    on: "开启", off: "关闭", started: "上次启动于", checked: "Ann 今日已签到 +2, 连续 3 天", tabs: "预热与签到",
  },
};

const LISTS = { codex: "codexWarmList", claude: "claudeWarmList", wb: "wbList" };
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);
// the tab picked and the pane shown, both as the page says them
async function shown(page) {
  return page.evaluate((lists) => {
    const on = [...document.querySelectorAll("#warmTabs [role=tab]")].filter((b) => b.getAttribute("aria-selected") === "true").map((b) => b.dataset.warm);
    const panes = Object.entries(lists).filter(([, id]) => {
      const e = document.getElementById(id);
      return getComputedStyle(e).visibility === "visible" && !e.inert && e.getBoundingClientRect().height > 0;
    }).map(([k]) => k);
    return { on, panes, focus: document.activeElement?.dataset?.warm || "" };
  }, LISTS);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": warm-ups and check-in under a tab per service", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-settings-groups-${i}.png`) });
      }
      await browser.close();
    });
    const open = async (lang, posts, over, stored) => {
      const errors = [];
      const ctx = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
      if (stored) await ctx.addInitScript((k) => { if (!sessionStorage.getItem("seeded")) { localStorage.setItem("magpie.warmTab", k); sessionStorage.setItem("seeded", "1"); } }, stored);
      const page = await ctx.newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, over));
      const go = async () => {
        await page.locator("#prefs").click();
        await page.locator("#setTab-usage").click();
        await page.locator("#warmSegs .opt").first().waitFor({ state: "attached" });
      };
      await page.goto("http://magpie.test/");
      await go();
      return { page, errors, reload: async () => { await page.reload(); await go(); } };
    };

    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const posts = [];
        const { page, errors, reload } = await open(lang, posts, {
          codexWarmed: new Date(Date.now() - 3600e3).toISOString(),
          workbuddy: true,
          workbuddyCheckins: [{ user: "Ann", day: today, at: new Date().toISOString(), outcome: "claimed", credit: 2, streak: 3 }],
        });

        // the Usage part: its list, then the tabs where a heading would be,
        // then the card
        const parts = await page.evaluate(() => [...document.querySelectorAll("#setPage-usage > *")]
          .map((h) => h.querySelector("#warmTabs") ? "tabs" : h.querySelector("#currencySegs") ? "usage" : h.className));
        assert.deepEqual(parts, ["usage", "tabs", "warm-panes"]);
        assert.deepEqual(await page.locator("#warmTabs [role=tab]:visible").allTextContents(), ["Codex", "Claude Code", "WorkBuddy"]);
        assert.equal(await page.locator("#warmTabs").getAttribute("role"), "tablist");
        assert.equal(await page.locator("#warmTabs").getAttribute("aria-label"), w.tabs);
        // no service rows left in Preferences
        for (const id of ["#warmSegs", "#warmAtSegs", "#claudeWarmSegs", "#claudeWarmAtSegs", "#wbCheckinSegs"]) {
          assert.equal(await page.locator("#setPage-usage > .list.prefs").first().locator(id).count(), 0, id + " is out of the Usage list");
        }

        // Codex to begin with, its rows alone
        const visibleRows = () => page.locator(".warm-panes .row.pref:visible .who .name").allTextContents();
        // each pill of the card shown has its thumb under the option picked,
        // drawn while the card was hidden or not
        const thumbsUnderPicks = async (tab) => assert.equal(await page.locator("#" + LISTS[tab]).evaluate((l) => [...l.querySelectorAll(".segs")].filter((s) => {
          const th = s.querySelector(":scope > .thumb").getBoundingClientRect(), on = s.querySelector(":scope > .on").getBoundingClientRect();
          return !(Math.abs(th.left - on.left) <= 1 && Math.abs(th.width - on.width) <= 1);
        }).length), 0, tab + ": the pills' thumbs under their picks");
        assert.deepEqual(await shown(page), { on: ["codex"], panes: ["codex"], focus: "" });
        assert.deepEqual(await visibleRows(), [w.reset, w.daily]);

        // short lines, the status still on them
        const warmSub = await page.locator("#warmSub").textContent();
        assert(warmSub.includes(w.started), `the last warm-up is shown: ${warmSub}`);
        assert((await page.locator("#warmAtSub").textContent()).length < 60, "the daily line is short");
        const wbSub = await page.locator("#wbCheckinSub").textContent();
        assert(wbSub.includes(w.checked), `today's check-in is shown: ${wbSub}`);
        // no coloured stripe down a card's, a row's or a tab's left side
        const stripes = await page.evaluate(() => [...document.querySelectorAll(".warm-panes .list, .warm-panes .row, #warmTabs button")]
          .filter((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; }).length);
        assert.equal(stripes, 0, "no left-border accents");

        // no time field while a daily warm-up is off
        assert.equal(await page.locator("#warmAtSegs input.at").count(), 0);
        assert.equal(await page.locator("#claudeWarmAtSegs input.at").count(), 0);

        // scrolled down (a real wheel), the Usage part not at its end (that
        // is the next test's): each tab shows its rows alone, each control
        // posts its setting, and nothing moves the page
        // (a window a little shorter than the part, the card in sight)
        const spare = await page.locator("#view-settings").evaluate((v) => v.scrollHeight - v.clientHeight);
        await page.setViewportSize({ width: 900, height: page.viewportSize().height + spare - 40 });
        await page.mouse.move(450, 200);
        await page.mouse.wheel(0, 20);
        await page.waitForTimeout(300);
        const before = await view(page);
        assert(before > 0, "the settings page must be long enough to scroll");
        const still = async (what) => {
          await page.waitForTimeout(250);
          assert.equal(await view(page), before, what + ": the click must not scroll the page");
        };
        const last = async (key, want) => {
          for (let i = 0; i < 50 && !(posts.length && JSON.stringify(posts.at(-1)[key]) === JSON.stringify(want)); i++) await page.waitForTimeout(40);
          assert.deepEqual(posts.at(-1)[key], want, key);
          await still(key);
        };
        const pick = (box, name) => page.locator(`${box} .opt`, { hasText: name }).first().click();

        await page.locator("#warmSegs .opt").nth(1).click();
        await last("codexWarmup", "week");
        await pick("#warmAtSegs", w.on);
        await last("codexWarmAt", "06:00");
        const field = page.locator("#warmAtSegs input.at");
        await field.waitFor();
        assert.equal(await field.inputValue(), "06:00", "the time field comes with the daily warm-up on");
        await field.fill("07:30");
        await field.dispatchEvent("change");
        await last("codexWarmAt", "07:30");

        const posted = posts.length;
        await page.locator("#warmTab-claude").click();
        await still("the Claude Code tab");
        assert.equal(posts.length, posted, "a tab posts nothing");
        assert.deepEqual((await shown(page)).panes, ["claude"]);
        assert.deepEqual((await shown(page)).on, ["claude"]);
        assert.deepEqual(await visibleRows(), [w.reset, w.daily]);
        await thumbsUnderPicks("claude");
        assert(await page.locator("#claudeWarmSegs").isVisible() && !(await page.locator("#warmSegs").isVisible()));
        await page.locator("#claudeWarmSegs .opt").nth(2).click();
        await last("claudeWarmup", "all");
        await pick("#claudeWarmAtSegs", w.on);
        await last("claudeWarmAt", "06:00");
        await page.locator("#claudeWarmAtSegs input.at").waitFor();

        await page.locator("#warmTab-wb").click();
        await still("the WorkBuddy tab");
        assert.deepEqual((await shown(page)).panes, ["wb"]);
        assert.deepEqual(await visibleRows(), [w.checkin]);
        await thumbsUnderPicks("wb");
        await pick("#wbCheckinSegs", w.on);
        await last("workbuddyCheckin", true);

        await page.locator("#warmTab-codex").click();
        await still("the Codex tab");
        assert.deepEqual((await shown(page)).panes, ["codex"]);
        assert.equal(await field.inputValue(), "07:30", "Codex's rows as they were left");
        await pick("#warmAtSegs", w.off);
        await last("codexWarmAt", "");
        await page.waitForFunction(() => !document.querySelector("#warmAtSegs input.at"));
        // the others kept as they were set
        assert.equal(posts.at(-1).codexWarmup, "week");
        assert.equal(posts.at(-1).claudeWarmup, "all");
        assert.equal(posts.at(-1).claudeWarmAt, "06:00");
        assert.equal(posts.at(-1).workbuddyCheckin, true);

        // the keyboard: the arrows, Home and End move along the tabs
        await page.locator("#warmTab-codex").focus();
        await page.keyboard.press("ArrowRight");
        assert.deepEqual(await shown(page), { on: ["claude"], panes: ["claude"], focus: "claude" });
        await page.keyboard.press("End");
        assert.deepEqual(await shown(page), { on: ["wb"], panes: ["wb"], focus: "wb" });
        await page.keyboard.press("ArrowRight");
        assert.deepEqual(await shown(page), { on: ["codex"], panes: ["codex"], focus: "codex" });
        await page.keyboard.press("ArrowLeft");
        assert.deepEqual((await shown(page)).on, ["wb"]);
        assert.equal(await page.locator("#warmTab-wb").getAttribute("tabindex"), "0");
        assert.equal(await page.locator("#warmTab-codex").getAttribute("tabindex"), "-1");
        await still("the keyboard");

        // WorkBuddy's tab remembered across a reload
        await reload();
        assert.deepEqual((await shown(page)).on, ["wb"]);
        assert.deepEqual((await shown(page)).panes, ["wb"]);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": at the page's end", async () => {
        const { page, errors } = await open(lang, [], { workbuddy: true });
        // the page scrolled to its very end with the tabs in sight: WorkBuddy's
        // shorter card, by a click or a key, leaves the tabs where they were
        // (room kept at the view's foot), and Codex's taller one too
        const room = await page.evaluate(() => {
          const v = document.querySelector("#view-settings"), tabs = document.querySelector("#warmTabs");
          return v.scrollHeight - (tabs.getBoundingClientRect().top - v.getBoundingClientRect().top + v.scrollTop);
        });
        await page.setViewportSize({ width: 900, height: Math.ceil(room) + 160 });
        await page.mouse.move(450, 200);
        const atEnd = () => page.locator("#view-settings").evaluate((v) => v.scrollHeight - v.clientHeight - v.scrollTop);
        for (let i = 0; i < 120 && (await atEnd()) > 0.5; i++) { await page.mouse.wheel(0, 60); await page.waitForTimeout(15); }
        await page.waitForTimeout(300);
        assert((await atEnd()) <= 0.5, "scrolled to the end");
        assert(await page.locator("#warmTab-wb").isVisible() && (await page.locator("#warmTab-wb").boundingBox()).y > 60, "the tabs in sight");
        const tabY = () => page.locator("#warmTab-wb").evaluate((b) => b.getBoundingClientRect().top);
        const y0 = await tabY();
        await page.locator("#warmTab-wb").click();
        await page.waitForTimeout(250);
        assert.deepEqual((await shown(page)).panes, ["wb"]);
        assert(Math.abs((await tabY()) - y0) <= 1, "a shorter card at the page's end moves nothing");
        await page.locator("#warmTab-codex").click();
        await page.waitForTimeout(250);
        assert.deepEqual((await shown(page)).panes, ["codex"]);
        assert(Math.abs((await tabY()) - y0) <= 1, "back to the taller card moves nothing");
        await page.locator("#warmTab-codex").focus();
        await page.keyboard.press("End");
        await page.waitForTimeout(250);
        assert.deepEqual((await shown(page)).panes, ["wb"]);
        assert(Math.abs((await tabY()) - y0) <= 1, "a key on a tab moves nothing either");

        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": no WorkBuddy signed in", async () => {
        // WorkBuddy's tab remembered, but no account: no tab, Codex shown
        const { page, errors } = await open(lang, [], {}, "wb");
        assert.equal(await page.locator("#warmTab-wb").isVisible(), false, "no WorkBuddy tab");
        assert.deepEqual(await page.locator("#warmTabs [role=tab]:visible").allTextContents(), ["Codex", "Claude Code"]);
        assert.deepEqual(await shown(page), { on: ["codex"], panes: ["codex"], focus: "" });
        assert.equal(await page.locator("#wbList").isVisible(), false, "no WorkBuddy rows");
        await page.setViewportSize({ width: 900, height: 1000 });
        await page.locator("#warmTab-claude").click();
        assert.deepEqual((await shown(page)).panes, ["claude"]);
        assert.deepEqual(errors, []);
      });
    }
  });
}
