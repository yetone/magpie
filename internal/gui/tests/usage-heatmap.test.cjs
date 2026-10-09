// Run with Node's test runner and Playwright on the module path; see README.md.
// The Requests tab's heatmap (#1369): the last 53 weeks a day each, Monday
// on top, month names over the weeks and the days of the week beside them, by
// tokens, requests or cost, with a five-shade Less…More legend. It is of the
// requests the filters keep, whatever the period or day picked; the pointer on
// a day says its date and what it had; a switch moves nothing on the page; a
// narrow window scrolls it sideways inside its card with the newest week in
// sight. Every language, both widths, light and dark; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");

// the pointer onto an element where it is: a locator's hover would scroll
// the card to it first, as a reader's pointer doesn't
async function pointTo(p, loc) {
  const b = await loc.boundingBox();
  await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 2 });
}
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const today = new Date();
today.setHours(0, 0, 0, 0);
const from = new Date(today);
from.setDate(from.getDate() - ((today.getDay() + 6) % 7) - 7 * 52);
const SPAN = Math.round((today - from) / 864e5) + 1; // 7 × 52 + this week's days so far
// a day of every fifth quiet; the busiest by tokens is today, by requests ten days ago
const DAYS = [];
for (let i = 0; i < SPAN; i++) {
  const d = new Date(from);
  d.setDate(d.getDate() + i);
  if (i % 5 === 0 && i !== SPAN - 1) continue;
  const calls = i === SPAN - 11 ? 500 : (i % 13) + 1;
  DAYS.push({ date: iso(d), calls, tokens: i === SPAN - 1 ? 9e9 : calls * 1e5 * (1 + (i % 7)), cost: calls * 0.5 });
}
const ROW = { t: new Date().toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "anthropic", providerName: "Claude", model: "claude-sonnet-5", req: "sonnet", in: 2, out: 600, cache_write: 900, cache_read: 390000, ms: 2380, status: 200, cost: 0.087, priced: true };
const share = (id, name) => ({ id, name, icon: "generic", calls: 1, errors: 0, input: 2, output: 600, cache_write: 900, cache_read: 390000, cost: 0.087 });
const PAGE = {
  period: "30d", rows: [ROW], offset: 0, total: 1, calls: 1, errors: 0, input: 2, output: 600, cache_write: 900, cache_read: 390000, reasoning: 0, cost: 0.087, unpriced: 0,
  bucket: "day", series: [], by: { provider: [share("anthropic", "Claude")], agent: [share("claude", "Claude Code")], model: [share("claude-sonnet-5", "claude-sonnet-5")] },
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "anthropic", name: "Claude", icon: "generic" }],
};

function server(lang, theme, variant, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/usage/requests") return json(PAGE);
    if (url.pathname === "/api/usage/heatmap") {
      asked.push(url.searchParams);
      return json({ from: iso(from), to: iso(today), days: variant === "none" ? [] : DAYS });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { title: "Usage heatmap", sub: "Daily usage over the last 53 weeks", metric: ["Tokens", "Requests", "Cost"], less: "Less", more: "More", tokens: "tokens", none: "No requests", loc: "en" },
  zh: { title: "用量热力图", sub: "最近 53 周的每日用量", metric: ["Token", "请求", "费用"], less: "少", more: "多", tokens: "token", none: "无请求", loc: "zh-CN" },
  "zh-TW": { title: "用量熱力圖", sub: "最近 53 週的每日用量", metric: ["Token", "請求", "費用"], less: "少", more: "多", tokens: "token", none: "無請求", loc: "zh-TW" },
  ja: { title: "使用量ヒートマップ", sub: "直近 53 週間の日ごとの使用量", metric: ["トークン", "リクエスト", "費用"], less: "少", more: "多", tokens: "トークン", none: "リクエストなし", loc: "ja-JP" },
  de: { title: "Nutzungs-Heatmap", sub: "Tägliche Nutzung der letzten 53 Wochen", metric: ["Token", "Anfragen", "Kosten"], less: "Weniger", more: "Mehr", tokens: "Token", none: "Keine Anfragen", loc: "de-DE" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Requests tab's heatmap", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch());
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });
    const open = async (lang, { width, theme = "light", variant = "" }) => {
      const errors = [], asked = [];
      const context = await browser.newContext({ viewport: { width, height: 820 }, reducedMotion: "reduce" });
      const p = await context.newPage();
      p.setDefaultTimeout(5000);
      p.on("pageerror", (e) => errors.push(e.message));
      await p.route("**/*", server(lang, theme, variant, asked));
      await p.goto("http://magpie.test/");
      await p.locator('[data-view="usage"]').first().click();
      await p.locator("#usageTab .opt").nth(1).click();
      await p.locator("#ledRank .rk").first().waitFor();
      return { p, errors, asked, context };
    };

    for (const lang of Object.keys(L)) for (const width of [420, 1100]) {
      const w = L[lang];
      await t.test(`${lang} at ${width}`, async () => {
        const { p, errors, asked, context } = await open(lang, { width });
        const heat = p.locator("#ledHeat");
        await heat.locator(".heat-grid i").first().waitFor();
        assert.equal(await heat.locator(".heat-title b").textContent(), w.title);
        assert.equal(await heat.locator(".heat-title span").textContent(), w.sub);
        assert.deepEqual(await heat.locator("#ledHeatMetric .opt").allTextContents(), w.metric);
        assert.equal(await heat.locator("#ledHeatMetric .opt.on").textContent(), w.metric[0]);
        // a cell a day, from the Monday 52 weeks before this week's to today
        const cells = heat.locator(".heat-grid i");
        assert.equal(await cells.count(), SPAN);
        assert.equal(await cells.first().getAttribute("data-date"), iso(from));
        assert.equal(await cells.last().getAttribute("data-date"), iso(today));
        // Monday on top, in the days' own column: the first cell's row is the first week's
        const rows = await cells.evaluateAll((cs) => cs.slice(0, 7).map((c) => Math.round(c.getBoundingClientRect().top)));
        assert(rows.every((y, i) => !i || y > rows[i - 1]), "a week's days go down: " + rows);
        // the days of the week and the months, in the language
        const wd = (await heat.locator(".heat-grid .wd").allTextContents()).filter(Boolean);
        const want = await p.evaluate((loc) => [0, 2, 4, 6].map((i) => new Date(2024, 0, 1 + i).toLocaleDateString(loc, { weekday: "short" })), w.loc);
        assert.deepEqual(wd, want);
        const months = await heat.locator(".heat-grid .mo").allTextContents();
        assert(months.length >= 11 && months.length <= 13, "months: " + months);
        const mon = await p.evaluate((loc) => new Date().toLocaleDateString(loc, { month: "short" }), w.loc);
        assert(months.includes(mon) || today.getDate() < 8, "this month named: " + months);
        // five shades, Less to More; today busiest, a quiet day the lightest
        assert.equal(await heat.locator(".sess-legend i").count(), 5);
        assert.deepEqual(await heat.locator(".sess-legend span").allTextContents(), [w.less, w.more]);
        assert.equal(await cells.last().getAttribute("class"), "l4");
        assert.equal(await cells.first().getAttribute("class"), "l0");
        const shades = await heat.locator(".sess-legend i").evaluateAll((is) => is.map((i) => getComputedStyle(i).backgroundColor));
        assert.equal(new Set(shades).size, 5, "five shades: " + shades);

        // the newest week is in sight, inside the card; only the card scrolls sideways
        const sc = await p.evaluate(() => {
          const s = document.querySelector("#ledHeatScroll"), r = s.getBoundingClientRect(), last = [...s.querySelectorAll(".heat-grid i")].at(-1).getBoundingClientRect();
          const card = document.querySelector("#ledHeat").getBoundingClientRect();
          return { over: s.scrollWidth > s.clientWidth + 1, left: s.scrollLeft, sw: s.scrollWidth, cw: s.clientWidth, lr: [last.left, last.right, r.left, r.right], inView: last.right <= r.right + 1 && last.left >= r.left - 1, inCard: r.left >= card.left && r.right <= card.right,
            page: document.documentElement.scrollWidth <= window.innerWidth + 1, view: document.querySelector("#view-usage").scrollWidth <= document.querySelector("#view-usage").clientWidth + 1 };
        });
        assert(sc.inView, "the newest day in sight: " + JSON.stringify(sc));
        assert(sc.inCard && sc.page && sc.view, "no sideways scroll outside the card: " + JSON.stringify(sc));
        if (width === 420) assert(sc.over && sc.left > 0, "a narrow card scrolls, at its end: " + JSON.stringify(sc));
        else assert(!sc.over, "a wide card holds every week");

        // the pointer on a day: its date and what it had, inside the card
        const busy = DAYS.find((d) => d.date === iso(new Date(today.getTime() - 10 * 864e5)));
        const cell = heat.locator(`.heat-grid i[data-date="${busy.date}"]`);
        // the reader wheels the page to the card (magpie puts back a scroll by
        // code), and the pointer then rests on a day
        await p.mouse.move(width / 2, 300);
        for (let i = 0; i < 20; i++) {
          const b = await heat.boundingBox();
          if (b.y >= 60 && b.y + b.height <= 800) break;
          await p.mouse.wheel(0, b.y < 60 ? -120 : 120);
          await p.waitForTimeout(60);
        }
        await pointTo(p, cell);
        const tip = heat.locator("#ledHeatTip");
        await tip.waitFor();
        const text = await tip.textContent();
        // the browser's own words for the day: its ICU is not Node's
        const day = await p.evaluate(([at, loc]) => new Date(at).toLocaleDateString(loc, { year: "numeric", month: "short", day: "numeric", weekday: "short" }), [today.getTime() - 10 * 864e5, w.loc]);
        assert(text.startsWith(day), `tip: ${text} (want ${day})`);
        // the date on a line of its own, over the figures
        const [d1, d2] = await tip.locator(":scope > *").evaluateAll((es) => es.map((e) => e.getBoundingClientRect().top));
        assert(d2 > d1 + 4, "the figures under the date");
        assert(text.includes(w.tokens) && text.includes("500"), "tip: " + text);
        const tb = await tip.boundingBox(), cb = await heat.boundingBox();
        assert(tb.x >= cb.x - 1 && tb.x + tb.width <= cb.x + cb.width + 1, "the tip is inside the card");
        // a quiet day in the newest weeks, in sight without scrolling the card
        const quiet = iso(new Date(from.getFullYear(), from.getMonth(), from.getDate() + 5 * Math.floor((SPAN - 2) / 5)));
        await pointTo(p, heat.locator(`.heat-grid i[data-date="${quiet}"]`));
        assert((await tip.textContent()).endsWith(w.none), "a quiet day: " + (await tip.textContent()));
        await p.mouse.move(2, 2);
        await tip.waitFor({ state: "hidden" });
        assert(await p.evaluate(() => { const s = document.querySelector("#ledHeatScroll"); return s.scrollLeft >= s.scrollWidth - s.clientWidth - 2; }), "the card still at its newest week");

        // Requests: ten days ago is busiest; the switch moves nothing and is kept
        const at = await p.evaluate(() => [window.scrollY, document.querySelector("#view-usage").scrollTop, document.querySelector("#ledHeatScroll").scrollLeft]);
        await heat.locator("#ledHeatMetric .opt").nth(1).click();
        assert.equal(await heat.locator("#ledHeatMetric .opt.on").textContent(), w.metric[1]);
        assert.equal(await cell.getAttribute("class"), "l4");
        assert.notEqual(await cells.last().getAttribute("class"), "l4");
        assert.deepEqual(await p.evaluate(() => [window.scrollY, document.querySelector("#view-usage").scrollTop, document.querySelector("#ledHeatScroll").scrollLeft]), at, "a click scrolled");
        assert.equal(await p.evaluate(() => localStorage.getItem("magpie.ledHeatMetric")), "calls");

        // the filters: no period or day; a model picked in the ranking asks again with it
        assert(asked.length >= 1 && !asked.at(-1).has("period") && !asked.at(-1).has("day"), "asked: " + asked.at(-1));
        await p.locator("#ledRank .rk").first().click();
        for (let i = 0; i < 60 && asked.at(-1).get("model") !== "claude-sonnet-5"; i++) await p.waitForTimeout(40);
        assert.equal(asked.at(-1).get("model"), "claude-sonnet-5");
        if (shots) await p.screenshot({ path: path.join(shots, `heatmap-${engine}-${lang}-${width}.png`), fullPage: false });
        assert.deepEqual(errors, []);
        await context.close();
      });
    }

    await t.test("dark, and none", async () => {
      const { p, errors, context } = await open("en", { width: 1100, theme: "dark" });
      await p.locator("#ledHeat .heat-grid i").first().waitFor();
      // a frame for the reduced-motion transitions the shades start with
      await p.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      const [l0, l4, card] = await p.evaluate(() => [".heat-grid i.l0", ".heat-grid i.l4", "#ledHeat"].map((s) => getComputedStyle(document.querySelector(s)).backgroundColor));
      assert(l0 !== l4 && l0 !== card, `dark shades: ${l0} ${l4} on ${card}`);
      if (shots) await p.locator("#ledHeat").screenshot({ path: path.join(shots, `heatmap-${engine}-dark.png`) });
      assert.deepEqual(errors, []);
      await context.close();
      const none = await open("en", { width: 1100, variant: "none" });
      await none.p.waitForTimeout(300);
      assert(await none.p.locator("#ledHeat").isHidden(), "nothing to show: no heatmap");
      assert.deepEqual(none.errors, []);
      await none.context.close();
    });
  });
}
