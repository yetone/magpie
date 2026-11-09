// Run with Node's test runner and Playwright on the module path; see README.md.
// The warm-ups and check-in, each in its service's provider editor (they
// were a tab per service in Settings › Usage, #124 Mrhe525, #471; the owner
// moved them to the provider on 11-07): Codex's and Claude Code's Warm up on
// reset and Daily warm-up, WorkBuddy's and Trae CN's Daily check-in, the
// lines under them short, with how the last warm-up and today's check-in
// went still on them. Settings' Usage part keeps none of them. WorkBuddy's
// list is there only with an account signed in; a daily warm-up's time
// field only while it is on. Every control posts the same setting it did,
// and a click on a control moves nothing. English and Chinese; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

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
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex", "claude", "workbuddy", "trae-cn"], { codex: "Codex", claude: "Claude Code", workbuddy: "WorkBuddy", "trae-cn": "Trae CN" }));
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
    lean: "Short Claude system prompt", reset: "Warm up on reset", daily: "Daily warm-up", checkin: "Daily check-in",
    on: "On", off: "Off", started: "last started", checked: "Ann checked in today +2, 3-day streak",
  },
  zh: {
    lean: "精简 Claude 系统提示词", reset: "窗口重置时预热", daily: "每日定时预热", checkin: "每日自动签到",
    on: "开启", off: "关闭", started: "上次启动于", checked: "Ann 今日已签到 +2, 连续 3 天",
  },
};

const LISTS = { codex: "codexWarmList", claude: "claudeWarmList", wb: "wbList", trae: "traeList" };
const EDIT = { codex: "codex", claude: "claude", wb: "workbuddy", trae: "trae-cn" };
// scrolls: where every box that scrolls is, so a click that moved one shows
const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));
// still: till no box has scrolled for ten frames
const still = (page) => page.evaluate(() => new Promise((done) => {
  let last = "", same = 0;
  const at = () => [scrollY, ...[...document.querySelectorAll("*")].map((e) => e.scrollTop)].join(",");
  const f = () => { const now = at(); same = now === last ? same + 1 : 0; last = now; if (same >= 10) done(); else requestAnimationFrame(f); };
  requestAnimationFrame(f);
}));
// the services' lists the open editor shows, as the page says them
const panes = (page) => page.evaluate((lists) => Object.entries(lists).filter(([, id]) => {
  const e = document.getElementById(id);
  return !!e.closest(".editor .prov-prefs") && getComputedStyle(e).visibility === "visible" && e.getBoundingClientRect().height > 0;
}).map(([k]) => k), LISTS);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": warm-ups and check-in in each service's provider editor", async (t) => {
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
    const open = async (lang, posts, over) => {
      const errors = [];
      const ctx = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
      const page = await ctx.newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => {
        // a goto in WebKit can cut off a fetch "due to access control
        // checks" (#1350): not this test's
        if (engine === "webkit" && /due to access control checks/.test(e.message)) return;
        errors.push(e.message);
      });
      await page.route("**/*", server(lang, posts, over));
      // edit: the service's editor opened, its lists drawn
      const edit = async (k) => {
        await openProvPrefs(page, EDIT[k]);
        await page.locator(`.editor .prov-prefs #${LISTS[k]} .segs .opt`).first().waitFor();
      };
      return { page, errors, edit };
    };

    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const posts = [];
        const { page, errors, edit } = await open(lang, posts, {
          codexWarmed: new Date(Date.now() - 3600e3).toISOString(),
          workbuddy: true,
          workbuddyCheckins: [{ user: "Ann", day: today, at: new Date().toISOString(), outcome: "claimed", credit: 2, streak: 3 }],
        });

        // Settings' Usage part: its list and the Alerts, no service's rows
        await page.goto("http://magpie.test/?view=settings&tab=usage");
        await page.locator("#setPage-usage #currencySegs .opt").first().waitFor();
        const parts = await page.evaluate(() => [...document.querySelectorAll("#setPage-usage > *")]
          .map((h) => h.querySelector("#currencySegs") ? "usage" : h.id));
        assert.deepEqual(parts, ["usage", "alertHead", "alertList"]);
        assert.equal(await page.locator("#warmTabs, .warm-panes").count(), 0, "no tabs left");
        for (const id of ["#warmSegs", "#warmAtSegs", "#claudeWarmSegs", "#claudeWarmAtSegs", "#wbCheckinSegs", "#traeCheckinSegs"]) {
          assert.equal(await page.locator("#setPage-usage").locator(id).count(), 0, id + " is out of Settings");
        }

        // each editor shows its own service's rows alone
        const visibleRows = () => page.locator(".editor .prov-prefs .row.pref:visible .who .name").allTextContents();
        // each pill shown has its thumb under the option picked, drawn
        // while its list was in the hidden holder or not. A ResizeObserver
        // refits a thumb after the editor sizes its pill, so wait for that
        // (on a busy machine it lands after the editor shows, #5839)
        const thumbsOff = (l) => [...l.querySelectorAll(".segs")].filter((s) => {
          const th = s.querySelector(":scope > .thumb").getBoundingClientRect(), on = s.querySelector(":scope > .on").getBoundingClientRect();
          return !(Math.abs(th.left - on.left) <= 1 && Math.abs(th.width - on.width) <= 1);
        }).length;
        const thumbsUnderPicks = async (k) => {
          const list = await page.locator("#" + LISTS[k]).elementHandle();
          await page.waitForFunction((l) => [...l.querySelectorAll(".segs")].every((s) => {
            const th = s.querySelector(":scope > .thumb").getBoundingClientRect(), on = s.querySelector(":scope > .on").getBoundingClientRect();
            return Math.abs(th.left - on.left) <= 1 && Math.abs(th.width - on.width) <= 1;
          }), list, { timeout: 3000 }).catch(() => {});
          assert.equal(await list.evaluate(thumbsOff), 0, k + ": the pills' thumbs under their picks");
        };
        // no coloured stripe down a list's or a row's left side
        const stripes = () => page.evaluate(() => [...document.querySelectorAll(".editor .prov-prefs .list, .editor .prov-prefs .row")]
          .filter((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; }).length);
        // a control pressed where the reader sees it; then it has to be
        // where it was, under the pointer (the editor may scroll to keep
        // it there when a time's field opens before it)
        let before = 0, pressed = null;
        const top = (loc) => loc.evaluate((e) => Math.round(e.getBoundingClientRect().top));
        const press = async (loc) => {
          await inEditor(page, loc);
          pressed = loc;
          before = await top(loc);
          await loc.click();
        };
        const last = async (key, want) => {
          for (let i = 0; i < 50 && !(posts.length && JSON.stringify(posts.at(-1)[key]) === JSON.stringify(want)); i++) await page.waitForTimeout(40);
          assert.deepEqual(posts.at(-1)?.[key], want, key);
          await still(page);
          assert(Math.abs(await top(pressed) - before) <= 1, key + ": what was clicked stays under the pointer");
        };
        const pick = (box, name) => press(page.locator(`.editor ${box} .opt`, { hasText: name }).first());

        await edit("codex");
        assert.deepEqual(await panes(page), ["codex"]);
        assert.deepEqual(await visibleRows(), [w.reset, w.daily]);
        await thumbsUnderPicks("codex");

        // short lines, the status still on them
        const warmSub = await page.locator("#warmSub").textContent();
        assert(warmSub.includes(w.started), `the last warm-up is shown: ${warmSub}`);
        assert((await page.locator("#warmAtSub").textContent()).length < 60, "the daily line is short");
        assert.equal(await stripes(), 0, "no left-border accents");

        // no time field while a daily warm-up is off
        assert.equal(await page.locator("#warmAtSegs input.at").count(), 0);
        assert.equal(await page.locator("#claudeWarmAtSegs input.at").count(), 0);

        await press(page.locator(".editor #warmSegs .opt").nth(1));
        await last("codexWarmup", "week");
        await pick("#warmAtSegs", w.on);
        await last("codexWarmAt", "06:00");
        const field = page.locator(".editor #warmAtSegs input.at");
        await field.waitFor();
        assert.equal(await field.inputValue(), "06:00", "the time field comes with the daily warm-up on");
        await inEditor(page, field);
        await field.focus();
        pressed = field;
        before = await top(field);
        await field.fill("07:30");
        await field.dispatchEvent("change");
        await last("codexWarmAt", "07:30");

        await edit("claude");
        assert.deepEqual(await panes(page), ["claude"]);
        // Claude Code's own run row is Claude's too
        assert.deepEqual(await visibleRows(), [w.lean, w.reset, w.daily]);
        await thumbsUnderPicks("claude");
        await press(page.locator(".editor #claudeWarmSegs .opt").nth(2));
        await last("claudeWarmup", "all");
        await pick("#claudeWarmAtSegs", w.on);
        await last("claudeWarmAt", "06:00");
        await page.locator(".editor #claudeWarmAtSegs input.at").waitFor();

        await edit("wb");
        assert.deepEqual(await panes(page), ["wb"]);
        assert.deepEqual(await visibleRows(), [w.checkin]);
        const wbSub = await page.locator("#wbCheckinSub").textContent();
        assert(wbSub.includes(w.checked), `today's check-in is shown: ${wbSub}`);
        assert.equal(await stripes(), 0, "no left-border accents");
        await thumbsUnderPicks("wb");
        await pick("#wbCheckinSegs", w.on);
        await last("workbuddyCheckin", true);

        // back in Codex's editor, its rows as they were left
        await edit("codex");
        assert.equal(await field.inputValue(), "07:30");
        await pick("#warmAtSegs", w.off);
        await last("codexWarmAt", "");
        await page.waitForFunction(() => !document.querySelector("#warmAtSegs input.at"));
        // the others kept as they were set
        assert.equal(posts.at(-1).codexWarmup, "week");
        assert.equal(posts.at(-1).claudeWarmup, "all");
        assert.equal(posts.at(-1).claudeWarmAt, "06:00");
        assert.equal(posts.at(-1).workbuddyCheckin, true);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": no WorkBuddy signed in", async () => {
        // no account and its check-in off: no WorkBuddy rows anywhere
        const { page, errors, edit } = await open(lang, [], {});
        await edit("codex");
        assert.deepEqual(await panes(page), ["codex"]);
        assert.equal(await page.locator("#wbList").isVisible(), false, "no WorkBuddy rows");
        assert.equal(await page.locator("#wbList").evaluate((e) => e.hidden), true, "WorkBuddy's list hidden");
        await edit("claude");
        assert.deepEqual(await panes(page), ["claude"]);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": a Trae CN account signed in (#694)", async () => {
        // Trae CN's own Daily check-in in its editor, saved as traeCheckin;
        // WorkBuddy's left alone
        const posts = [];
        const { page, errors, edit } = await open(lang, posts, {
          trae: true,
          traeCheckins: [{ user: "Ann", by: "trae", day: today, at: new Date().toISOString(), outcome: "claimed", credit: 2, streak: 3 }],
        });
        await edit("trae");
        assert.deepEqual(await panes(page), ["trae"]);
        assert.deepEqual(await page.locator(".editor .prov-prefs .row.pref:visible .who .name").allTextContents(), [w.checkin]);
        assert((await page.locator("#traeCheckinSub").textContent()).includes(w.checked), "today's check-in is shown");
        const on = page.locator(".editor #traeCheckinSegs .opt", { hasText: w.on }).first();
        await inEditor(page, on);
        const before = await scrolls(page);
        await on.click();
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(40);
        assert.equal(posts.at(-1)?.traeCheckin, true);
        assert.equal(posts.at(-1).workbuddyCheckin, false);
        await still(page);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        assert.deepEqual(errors, []);
      });
    }
  });
}
