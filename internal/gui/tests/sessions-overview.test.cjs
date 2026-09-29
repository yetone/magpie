// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions tab's overview (v: 感觉我们的Session可观测性页面也要做的这么的
// 丰富、优雅、简洁): six figures with the sessions' median and p90 and the
// projects' spread, the range as bars up to 100 days and a calendar past them, up to
// a year, counted in tokens, cost, sessions or active time; the week by hour;
// projects and models as bars that filter; and the top sessions, opened in
// place from their files.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const to = new Date(2026, 8, 28);
const back = (i) => { const d = new Date(to); d.setDate(d.getDate() - i); return d; };
// 200 days of use: every third day idle, two projects and two models
const allDays = Array.from({ length: 200 }, (_, i) => back(199 - i)).map((d, i) => {
  const date = iso(d);
  if (i % 3 === 2) return { date, usage: [], active: [] };
  const n = 1 + (i % 7);
  const hours = new Array(24).fill(0);
  hours[9] = 600 * n; hours[14] = 300 * n; hours[22] = 60;
  return {
    date,
    usage: [
      { agent: "claude", cwd: "/work/alpha", model: "claude-opus-4", input: 30000 * n, output: 4000 * n, cache_read: 90000 * n, cache_write: 1000, cost: 1.5 * n, priced: true },
      { agent: "codex", cwd: "/work/beta", model: "gpt-5", input: 10000 * n, output: 2000 * n, cache_read: 0, cache_write: 0, cost: 0.4 * n, priced: true },
    ],
    active: [
      { agent: "claude", cwd: "/work/alpha", seconds: hours.reduce((a, b) => a + b, 0), hours },
      { agent: "codex", cwd: "/work/beta", seconds: 120, hours: Object.assign(new Array(24).fill(0), { 10: 120 }) },
    ],
  };
});
const top = (i, cwd) => ({
  key: "claude:s" + i, agent: "claude", id: "s" + i, cwd, title: "Top session " + i, last: to.toISOString(),
  input: 900000 - i * 1000, output: 5000, cache_read: 0, cache_write: 0, cost: 12 - i, priced: true, active: 3600 - i * 60, models: ["claude-opus-4"], days: [iso(to)],
});
const full = (key) => ({
  key, agent: "claude", name: "Claude Code", icon: "claude", id: key.split(":")[1], cwd: "/work/alpha", title: "Top session", start: back(1).toISOString(), last: to.toISOString(),
  input: 900000, output: 5000, cache_read: 0, cache_write: 0, cost: 12, priced: true, models: [{ model: "claude-opus-4", input: 900000, output: 5000, cost: 12, priced: true }],
  resume: "claude --resume " + key.split(":")[1],
});
const sessions = [0, 1, 2].map((i) => ({ ...full("claude:s" + i), title: "Latest " + i }));

function serve(lang, seen, ctl = {}) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const q = url.searchParams;
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/sessions/progress") return json(ctl.progress ? ctl.progress() : { indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions, dirs: ["/test/sessions"] });
    const n = +q.get("days") || allDays.length;
    const days = allDays.slice(-n);
    if (url.pathname === "/api/sessions/stats" && ctl.delay) await new Promise((r) => setTimeout(r, ctl.delay));
    if (url.pathname === "/api/sessions/stats") return json({ from: days[0].date, to: iso(to), days, agents: { claude: "Claude Code", codex: "Codex" } });
    if (url.pathname === "/api/sessions/overview") {
      seen.push(q.toString());
      const cwd = q.get("cwd");
      const tops = [0, 1, 2].map((i) => top(i, cwd || (i ? "/work/beta" : "/work/alpha")));
      return json({ count: cwd ? 7 : 42, median: 120000, p90: 880000, days: days.map((d) => d.usage.length ? 2 : 0),
        top: { tokens: tops, cost: [...tops].reverse(), active: tops.slice(0, 2) },
        messages: days.map((d) => d.usage.length ? 30 : 0), output: days.map(() => 0),
        shape: {
          messages: { edges: [1, 6, 16, 31, 61, 121], counts: [3, 10, 14, 9, 4, 2], total: 42 },
          minutes: { edges: [1, 6, 16, 31, 61, 121], counts: [20, 12, 6, 3, 1, 0], total: 42 },
          autonomy: { edges: [0, 1, 3, 6, 11, 21], counts: [5, 7, 10, 12, 6, 2], total: 42 },
        },
        tools: {
          calls: 1000, sessions: 40,
          top: [["Bash", "Bash", 400, 38], ["Read", "Read", 250, 30], ["Edit", "Edit", 200, 25], ["mcp__slack__send_message_with_a_very_long_name", "Tool", 100, 3], ["WebFetch", "Other", 50, 9]]
            .map(([name, category, calls, sessions]) => ({ name, category, calls, sessions })),
          categories: [["Bash", 400], ["Read", 250], ["Edit", 200], ["Tool", 100], ["Other", 50]].map(([name, calls]) => ({ name, calls, sessions: 1 })),
          weeks: ["2026-08-31", "2026-09-07", "2026-09-14", "2026-09-21"].map((start, i) => ({ start, calls: { Bash: 100 * i + 10, Read: 60, Edit: 50 } })),
        },
        skills: {
          calls: 30, count: 12,
          top: [{ name: "artifact-design", calls: 20, sessions: 8, last: "2026-09-27", agents: { claude: 15, codex: 5 }, projects: [{ name: "/work/alpha", calls: 18, sessions: 7 }] },
            { name: "pdf", calls: 10, sessions: 2, last: "2026-09-01", agents: { claude: 10 }, projects: [{ name: "/work/beta", calls: 10, sessions: 2 }] }],
        } });
    }
    if (url.pathname === "/api/sessions/one") { seen.push("one " + q.get("key")); return json(full(q.get("key"))); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { median: "median 120K · p90 880K", share: /\d+% in alpha/, busiest: /^Busiest at \S+ 09:00$/, detail: "Time", none: "Active time isn't kept by model." },
  zh: { median: "中位 120K · p90 880K", share: /alpha 占 \d+%/, busiest: /^最忙：\S+ 09:00$/, detail: "时间", none: "活跃时长不按模型统计。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the sessions overview`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], seen = [], ctl = {};
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, seen, ctl));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#sessGrid:not([hidden])").waitFor();
      await page.waitForFunction(() => !document.querySelector("#sessStats .kpi.stale") && document.querySelectorAll("#sessTop .sess-top").length === 3);

      await t.test("six figures", async () => {
        const kpis = await page.locator("#sessStats .kpi").allInnerTexts();
        assert.equal(kpis.length, 6);
        assert.match(kpis[0], /^42\n/);
        assert(kpis[0].includes(want[lang].median), kpis[0]);
        assert.match(kpis[5], /^2\n/);
        assert.match(kpis[5], want[lang].share);
      });

      await t.test("the calendar, and the metric", async () => {
        assert.equal(await page.locator("#sessChart .sess-cal i").count(), 200);
        assert(await page.locator("#sessChart .sess-cal .mo").count() >= 6);
        assert.equal(await page.locator("#sessChart .sess-cal-side .fact").count(), 3);
        const levels = await page.locator("#sessChart .sess-cal i").evaluateAll((es) => es.map((e) => e.className));
        assert(levels.includes("l0") && levels.includes("l4"), levels.join());
        await page.locator("#sessChart .segs .opt").nth(3).click();
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.sessMetric")), "sessions");
        const title = await page.locator("#sessChart .sess-cal i.l4").first().getAttribute("title");
        assert.match(title, lang === "en" ? /2 sessions/ : /2 个会话/);
        // messages and output tokens, the heatmap's other ways
        assert.deepEqual((await page.locator("#sessChart .segs .opt").allInnerTexts()).slice(0, 3), lang === "en" ? ["Tokens", "Output tokens", "Messages"] : ["Token", "输出 Token", "消息"]);
        await page.locator("#sessChart .segs .opt").nth(2).click();
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.sessMetric")), "messages");
        assert.match(await page.locator("#sessChart .sess-cal i.l4").first().getAttribute("title"), lang === "en" ? /30 messages/ : /30 条消息/);
        await page.locator("#sessChart .segs .opt").nth(1).click();
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.sessMetric")), "output");
        await page.locator("#sessChart .segs .opt").nth(0).click();
      });

      await t.test("by hour", async () => {
        assert.equal(await page.locator("#sessHours .sess-hours i").count(), 168);
        assert.equal(await page.locator("#sessHours .tz").innerText(), "Asia/Shanghai");
        assert.match(await page.locator("#sessHours .sess-card-foot > span").first().innerText(), want[lang].busiest);
      });

      await t.test("session shape, by messages, time and tool calls", async () => {
        const shape = page.locator("#sessShape");
        assert.equal(await shape.locator(".label").innerText(), lang === "en" ? "SESSION SHAPE" : "会话形态");
        assert.equal(await shape.locator(".tz").innerText(), lang === "en" ? "42 sessions" : "42 个会话");
        assert.deepEqual(await shape.locator(".col > span").allInnerTexts(), ["1–5", "6–15", "16–30", "31–60", "61–120", "121+"]);
        assert.deepEqual(await shape.locator(".col .plot > b").allInnerTexts(), ["3", "10", "14", "9", "4", "2"]);
        // no grey column behind the bars, the busiest bin in full, the axes said
        assert.equal(await shape.locator(".track").count(), 0);
        assert.deepEqual(await shape.locator(".col").evaluateAll((cs) => cs.map((c) => c.classList.contains("peak"))), [false, false, true, false, false, false]);
        assert.equal((await shape.locator(".sess-card-foot").innerText()).trim(), lang === "en" ? "Across: messages in a session, prompts and replies · Height: sessions" : "横轴：一个会话里的消息数（提示加回复） · 柱高：会话数");
        const hs = await shape.locator(".plot > i").evaluateAll((is) => is.map((i) => i.getBoundingClientRect().height));
        assert.ok(hs[2] > hs[1] && hs[1] > hs[3] && hs[3] > hs[0], String(hs));
        // wheeled to, as a person would: WebKit paints a scroll set from script late
        await page.mouse.move(500, 400);
        for (let i = 0; i < 20 && (await shape.boundingBox()).y > 300; i++) await page.mouse.wheel(0, 200);
        await page.waitForTimeout(300);
        const y = (await shape.boundingBox()).y;
        await shape.locator(".segs .opt").nth(2).click();
        assert.equal(await shape.locator(".col > span").first().innerText(), "<1");
        assert.equal(await shape.locator(".segs .opt").nth(2).innerText(), lang === "en" ? "Tool calls" : "工具调用");
        assert.match(await shape.locator(".sess-card-foot").innerText(), lang === "en" ? /tool calls the agent made on its own for each prompt/ : /你每发一条提示，Agent 自己调用工具的次数/);
        assert.deepEqual(await shape.locator(".col .plot > b").allInnerTexts(), ["5", "7", "10", "12", "6", "2"]);
        await shape.locator(".segs .opt").nth(1).click();
        assert.equal(await shape.locator(".col > span").first().innerText(), lang === "en" ? "1–5m" : "1–5 分钟");
        // an empty bin says 0 over a stub
        assert.equal(await shape.locator(".col .plot > b").last().innerText(), "0");
        assert.equal(await shape.locator(".plot > i").last().getAttribute("class"), "none");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.sessShape")), "minutes");
        assert.equal((await shape.boundingBox()).y, y);
        await shape.locator(".segs .opt").nth(0).click();
      });

      await t.test("tool use: the top tools, by kind and by week", async () => {
        const tools = page.locator("#sessTools");
        assert.equal(await tools.locator(".tz").innerText(), lang === "en" ? "1.0K calls · 40 sessions" : "1.0K 次调用 · 40 个会话");
        const rows = tools.locator(".sess-bar.tool");
        assert.equal(await rows.count(), 5);
        assert.deepEqual(await rows.first().locator("span").evaluateAll((es) => es.map((e) => e.textContent)).then((x) => x.filter(Boolean)),
          ["Bash", "Bash", "400", lang === "en" ? "38 sessions" : "38 个会话", "40%"]);
        const dots = await rows.locator(".dot").evaluateAll((es) => es.map((e) => getComputedStyle(e).backgroundColor));
        assert.equal(new Set(dots).size, 5);
        assert.deepEqual((await tools.locator(".sess-kinds .kind").allInnerTexts()).map((s) => s.trim()), ["Bash\n400", "Read\n250", "Edit\n200", "Tool\n100", (lang === "en" ? "Other" : "其他") + "\n50"].map((s) => s.replace("\\n", "\n")));
        assert.equal(await tools.locator(".sess-mix i").count(), 5);
        assert.equal(await tools.locator(".sess-weeks .bar").count(), 4);
        const hs = await tools.locator(".sess-weeks .stack").evaluateAll((es) => es.map((e) => e.getBoundingClientRect().height));
        assert(hs[3] > hs[0], hs.join());
        assert.equal(await tools.locator(".sess-weeks .stack").last().locator("i").count(), 3);
        // borders are hairlines, never a stripe down the left
        const left = await page.locator("#sessTools, #sessShape, #sessSkills").evaluateAll((es) => es.map((e) => getComputedStyle(e).borderLeftWidth));
        assert.deepEqual(left, ["1px", "1px", "1px"]);
      });

      await t.test("top skills", async () => {
        const skills = page.locator("#sessSkills");
        assert.equal(await skills.locator(".label").innerText(), lang === "en" ? "TOP SKILLS" : "常用 Skills".toUpperCase());
        assert.equal(await skills.locator(".tz").innerText(), lang === "en" ? "30 calls · 12 skills" : "30 次调用 · 12 个技能");
        assert.equal(await skills.locator(".sess-skill").count(), 2);
        assert.equal(await skills.locator(".sess-skill .n").first().innerText(), "artifact-design");
        const sub = await skills.locator(".sess-skill .sub").first().innerText();
        assert.match(sub, lang === "en" ? /^8 sessions · last Sep 27 · Claude Code 75% · Codex 25% · alpha$/ : /^8 个会话 · 最近 9月27日 · Claude Code 75% · Codex 25% · alpha$/);
        assert.equal(await skills.locator(".sess-more").innerText(), lang === "en" ? "+10 more" : "还有 10 个");
        await page.mouse.move(500, 400);
        for (let i = 0; i < 20; i++) await page.mouse.wheel(0, -400);
        await page.waitForFunction(() => document.querySelector("#view-usage").scrollTop === 0);
      });

      await t.test("top sessions open in place", async () => {
        const first = page.locator("#sessTop .sess-top").first();
        assert.equal(await first.locator(".name").innerText(), "Top session 0");
        // in sight first: a click's own scroll to reach it isn't the page's
        await first.scrollIntoViewIfNeeded();
        const y = (await first.boundingBox()).y;
        await first.click();
        await page.locator("#sessTop .sess-detail .sess-line").first().waitFor();
        assert(seen.includes("one claude:s0"));
        assert.equal(await page.locator("#sessTop .sess-detail .sess-line .k").first().innerText(), want[lang].detail);
        assert.equal((await page.locator("#sessTop .sess-top").first().boundingBox()).y, y);
        await page.locator("#sessTop .sess-top").first().click();
        assert.equal(await page.locator("#sessTop .sess-detail").count(), 0);
        await page.locator("#sessTop .segs .opt").nth(1).click();
        assert.equal(await page.locator("#sessTop .sess-top .name").first().innerText(), "Top session 2");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.sessTop")), "cost");
      });

      await t.test("a project picked from its bar, the page left where it was", async () => {
        await page.locator("#sessProjects").scrollIntoViewIfNeeded();
        const bar = page.locator("#sessProjects .sess-bar", { hasText: "beta" });
        const y = (await bar.boundingBox()).y;
        await bar.click();
        await page.waitForFunction(() => document.querySelector("#sessProjects .sess-bar.on"));
        assert.equal((await page.locator("#sessProjects .sess-bar.on").boundingBox()).y, y);
        assert.match(await page.locator("#sessFolder").innerText(), /beta/);
        await page.waitForFunction(() => document.querySelector("#sessStats .kpi b")?.textContent === "7");
        assert(seen.some((s) => s.includes("cwd=%2Fwork%2Fbeta")), seen.join("\n"));
        assert.equal(await page.locator("#sessProjects .sess-bar").count(), 2);
        await page.locator("#sessProjects .sess-bar.on").click();
        await page.waitForFunction(() => !document.querySelector("#sessProjects .sess-bar.on"));
      });

      await t.test("a model picked: no active time by hour", async () => {
        await page.locator("#sessModels .sess-bar", { hasText: "gpt-5" }).click();
        assert.equal(await page.locator("#sessHours .sess-none").innerText(), want[lang].none);
        assert(await page.locator("#sessChart .segs .opt").nth(5).isDisabled());
        await page.locator("#sessModels .sess-bar.on").click();
      });

      await t.test("a week and 90 days as bars", async () => {
        await page.locator("#sessRange .opt").nth(1).click();
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .bars .bar").length === 7);
        assert.equal(await page.locator("#sessChart .sess-cal").count(), 0);
        await page.locator("#sessRange .opt").nth(3).click();
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .bars .bar").length === 90);
        await page.locator("#sessRange .opt").nth(4).click();
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .sess-cal i").length === 200);
      });

      await t.test("the search sits by the latest sessions it filters, and stays with no match", async () => {
        const q = page.locator("#sessListHead #sessQ");
        assert.equal(await q.count(), 1);
        assert.equal(await page.locator(".sess-tools #sessQ").count(), 0);
        const head = await page.locator("#sessListHead").boundingBox();
        const box = await q.boundingBox();
        assert(box.x + box.width >= head.x + head.width - 4, "at the heading's right: " + JSON.stringify([box, head]));
        assert(Math.abs((box.y + box.height / 2) - (head.y + head.height / 2)) <= 2, "on the heading's line");
        assert.equal(await page.locator("#sessList .row.sess").count(), 3);
        await q.fill("latest 1");
        await page.waitForFunction(() => document.querySelectorAll("#sessList .row.sess").length === 1);
        await q.fill("nothing like it");
        await page.locator("#sessList .empty-state").waitFor();
        assert.equal(await page.locator("#sessList .empty-state").innerText(), lang === "en" ? "No session matches." : "没有匹配的会话。");
        assert(await q.isVisible());
        await q.press("Escape");
        await page.waitForFunction(() => document.querySelectorAll("#sessList .row.sess").length === 3);
      });

      await t.test("the indexing show: not for a catch-up read, once for a long one", async () => {
        // how many times the show is put on the page
        await page.evaluate(() => {
          window.heroes = 0;
          new MutationObserver((ms) => { for (const m of ms) for (const n of m.addedNodes) if (n.classList?.contains("sess-indexing")) window.heroes++; })
            .observe(document.querySelector("#sessStats"), { childList: true });
        });
        // a range picked while an agent writes: a few changed files read again
        const started = Date.now();
        ctl.delay = 450;
        ctl.progress = () => ({ indexing: Date.now() - started < 400, files: 2, done: 1, bytes: 40000, read: 20000 });
        await page.locator("#sessRange .opt").nth(1).click();
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .bars .bar").length === 7);
        assert.equal(await page.evaluate(() => window.heroes), 0);
        // a first index, a while long: shown once, up to its end, never again from nought
        const at = Date.now();
        ctl.delay = 2200;
        ctl.progress = () => { const f = Math.min(1, (Date.now() - at) / 2000); return { indexing: f < 1, files: 300, done: Math.round(300 * f), bytes: 4e9, read: 4e9 * f }; };
        await page.locator("#sessRange .opt").nth(4).click();
        await page.locator("#sessStats .sess-indexing").waitFor();
        await page.waitForFunction(() => document.querySelectorAll("#sessChart .sess-cal i").length === 200, null, { timeout: 8000 });
        assert.equal(await page.evaluate(() => window.heroes), 1);
        delete ctl.delay;
        delete ctl.progress;
      });

      await t.test("nothing overflows, wide or narrow", async () => {
        for (const width of [1100, 560]) {
          await page.setViewportSize({ width, height: 760 });
          const over = await page.evaluate(() => [...document.querySelectorAll("#sessionsPane *")].filter((e) => e.offsetParent && e.getBoundingClientRect().right > document.documentElement.clientWidth + 1).map((e) => e.className));
          assert.deepEqual(over, [], "width " + width);
        }
      });
      assert.deepEqual(errors, []);
    });
  }
}
