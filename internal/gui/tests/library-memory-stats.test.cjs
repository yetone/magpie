// Run with Node's test runner and Playwright on the module path; see README.md.
// Library › Memory's numbers (the owner, 11-25: 创建率、召回率、更新率、清洗率
// and their trend): four tiles over the last 7 days, each named in the
// reader's language with what it counts said on hover, a value and its
// days as small columns, read from /api/memory/stats. A tile picked opens
// its chart: a column a counted day, none for a day nothing was counted
// (before counting began, or no searches that day), the day's exact
// numbers on hover. 30 days reads the counts again. Every language, wide
// and narrow, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const HOME = "/Users/mem";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md` }],
  instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: [] },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const memories = {
  memories: [{ id: "a1", text: "Use pnpm, never npm, in this repo", kind: "rule", scope: "global", origin: "user", status: "active", created: "2026-11-01T10:00:00Z", updated: "2026-11-02T10:00:00Z" }],
  projects: {}, digest: true, agents: [],
};

// the last n local days, oldest first, as memory.Stats names them
const dayNames = (n) => Array.from({ length: n }, (_, i) => {
  const d = new Date();
  d.setDate(d.getDate() - (n - 1 - i));
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
});
// 7 days: counted from the third; the first dated by the memories' files
function week() {
  const n = dayNames(7);
  return {
    since: n[2], active: 20, candidates: 2, used: 6,
    days: [
      { day: n[0], recorded: false, backfilled: true, created: { user: 1 } },
      { day: n[1], recorded: false },
      { day: n[2], recorded: true, created: { agent: 2 }, updated: 1, searches: 4, found: 3, recalled: 5 },
      { day: n[3], recorded: true },
      { day: n[4], recorded: true, created: { inferred: 3 }, updated: 2, cleaned: { tidy: 2, you: 1 }, searches: 2, found: 1, recalled: 1 },
      { day: n[5], recorded: true, searches: 2, found: 2, recalled: 3 },
      { day: n[6], recorded: true, created: { user: 1 }, cleaned: { expiry: 1 } },
    ],
  };
}
function month() {
  const n = dayNames(30);
  return {
    since: n[25], active: 20, candidates: 2, used: 9,
    days: n.map((day, i) => (i >= 25 ? { day, recorded: true, created: { agent: 1 }, searches: 2, found: 1, recalled: 1 } : i % 6 ? { day, recorded: false } : { day, recorded: false, backfilled: true, created: { user: 2 } })),
  };
}

function server(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname === "/api/memory/stats") {
      const days = url.searchParams.get("days");
      asked.push(days);
      return route.fulfill({ json: days === "30" ? month() : week() });
    }
    if (url.pathname === "/api/memory/semantic" || url.pathname === "/api/memory/distill") return route.fulfill({ json: null });
    if (url.pathname === "/api/memory") return route.fulfill({ json: memories });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-library";
const words = {
  en: { names: ["Creation rate", "Recall rate", "Update rate", "Cleanup rate"], recallAbout: /share that returned at least one memory/, subs: [/7 new/, /6 of 8 searches/, /3 updated/, /4 cleaned up/], searches: /Agent searches/, since: /Counted from/ },
  zh: { names: ["创建率", "召回率", "更新率", "清洗率"], recallAbout: /至少找到一条记忆的比例/, subs: [/7/, /8 次搜索中找到 6 次/, /更新 3 次/, /清洗 4 条/], searches: /Agent 搜索/, since: /开始统计/ },
  "zh-TW": { names: ["建立率", "召回率", "更新率", "清理率"], recallAbout: /至少找到一則記憶的比例/, subs: [/7/, /8 次搜尋中找到 6 次/, /更新 3 次/, /清理 4 則/], searches: /Agent 搜尋/, since: /開始統計/ },
  ja: { names: ["作成率", "リコール率", "更新率", "整理率"], recallAbout: /1件以上見つかった割合/, subs: [/7/, /8 回の検索中 6 回ヒット/, /3 回更新/, /4 件整理/], searches: /エージェントの検索/, since: /から集計/ },
  de: { names: ["Neuzugänge", "Trefferquote", "Aktualisierungsrate", "Aufräumrate"], recallAbout: /mindestens eine Erinnerung gefunden/, subs: [/7 neue/, /6 von 8 Suchen/, /3 aktualisiert/, /4 aufgeräumt/], searches: /Suchen von Agenten/, since: /Gezählt seit/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Memory tab shows how memory is made, recalled, updated and cleaned, and its trend", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const w = words[lang];
        // what was made is an average a day; the rest share of the memories in use
        const values = ["1" + { en: "new/day", zh: "条/天", "zh-TW": "則/天", ja: "件/日", de: "/Tag" }[lang], "75%", "15%", "20%"];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const mine = [];
        page.on("pageerror", (e) => { errors.push(e.message); mine.push(e.message); });
        const asked = [];
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const card = page.locator(`${view} .lib-memstats:not(.wait)`);
        await card.waitFor();
        assert.deepEqual(asked, ["7"]);

        // four tiles, in order, named in the reader's language, above the switches
        const tiles = card.locator(".ms-tile");
        assert.equal(await tiles.count(), 4);
        const box = await card.boundingBox(), switches = await page.locator(`${view} .lib-memcard`).boundingBox();
        assert.ok(box.y + box.height <= switches.y, "the numbers aren't above the switches");
        for (let i = 0; i < 4; i++) {
          const tile = tiles.nth(i);
          assert.equal((await tile.locator(".ms-name").textContent()).trim(), w.names[i]);
          assert.equal((await tile.locator(".ms-value").textContent()).replace(/\s/g, ""), values[i]);
          assert.match(await tile.locator(".ms-sub").textContent(), w.subs[i]);
          // a column a day, the first one dated by the files, the second not counted
          assert.equal(await tile.locator(".ms-spark > span").count(), 7);
          const r = await tile.boundingBox();
          assert.ok(r.x >= box.x - 1 && r.x + r.width <= box.x + box.width + 1, `tile ${i} is cut off`);
          for (const part of await tile.locator(".ms-name, .ms-value, .ms-sub").all()) {
            assert.ok(await part.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), `tile ${i}'s text overflows`);
          }
        }
        assert.match(await tiles.nth(1).getAttribute("title"), w.recallAbout);
        assert.equal(await tiles.nth(0).locator(".ms-spark > span:not(:has(i))").count(), 1);
        // wide, a row of four; narrow, two rows of two
        const ys = new Set();
        for (const tile of await tiles.all()) ys.add(Math.round((await tile.boundingBox()).y));
        assert.equal(ys.size, width > 560 ? 1 : 2);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");

        // nothing is charted until a tile is picked
        assert.equal(await card.locator(".ms-chart").count(), 0);
        await tiles.nth(1).click();
        const chart = page.locator(`${view} .lib-memstats .ms-chart[data-metric="recall"]`);
        await chart.waitFor();
        assert.equal(await page.locator(`${view} .lib-memstats .ms-tile.on`).getAttribute("data-metric"), "recall");
        const cols = chart.locator(".ms-col");
        assert.equal(await cols.count(), 7);
        // a column for each day with searches; none before counting, none on a day without searches
        assert.deepEqual(await cols.evaluateAll((cs) => cs.map((c) => !!c.querySelector("i"))), [false, false, true, false, true, true, false]);
        assert.match(await chart.locator(".ms-note").textContent(), w.since);
        // the day's exact numbers on hover, read out over the plot, never on a bar
        const read = chart.locator(".ms-read");
        assert.match(await read.textContent(), w.recallAbout);
        const c4 = await cols.nth(4).boundingBox();
        await page.mouse.move(c4.x + c4.width / 2, c4.y + c4.height - 4);
        await chart.locator(".ms-read.on").waitFor();
        assert.match(await read.textContent(), /50\s?%/);
        assert.match(await read.textContent(), w.searches);
        const rb = await read.boundingBox(), pb = await chart.locator(".ms-plot").boundingBox(), cb = await chart.boundingBox();
        assert.ok(rb.y + rb.height <= pb.y + 1, "the day's numbers are on the plot");
        assert.ok(rb.x >= cb.x - 1 && rb.x + rb.width <= cb.x + cb.width + 1, "the day's numbers are cut off");
        await page.mouse.move(1, 1);
        await chart.locator(".ms-read:not(.on)").waitFor();
        assert.match(await read.textContent(), w.recallAbout);

        // 30 days reads the counts again; the chart stays on its metric
        await card.locator('.ms-range .opt[data-seg="30"]').click();
        await page.locator(`${view} .lib-memstats .ms-chart[data-metric="recall"] .ms-col >> nth=29`).waitFor();
        assert.deepEqual(asked, ["7", "30"]);
        assert.equal(await page.locator(`${view} .lib-memstats .ms-tile >> nth=0`).locator(".ms-spark > span").count(), 30);
        // what was made: the days before counting, dated by the files, drawn fainter
        await page.locator(`${view} .lib-memstats .ms-tile >> nth=0`).click();
        const made = page.locator(`${view} .lib-memstats .ms-chart[data-metric="created"]`);
        await made.waitFor();
        assert.equal(await made.locator(".ms-col.back i").count(), 5);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the chart scrolls the page sideways");
        // picked again, it folds away
        await page.locator(`${view} .lib-memstats .ms-tile >> nth=0`).click();
        await page.locator(`${view} .lib-memstats .ms-chart`).waitFor({ state: "detached" });
        assert.deepEqual(mine, []);
        await ctx.close();
      });
    }
  });
}
