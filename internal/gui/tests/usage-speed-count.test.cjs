// Run with Node's test runner and Playwright on the module path; see README.md.
// One rule for a group's speed, in the Usage page's ranking, its Output speed
// card and the overview's lists (Fallmonkey on Discord): gpt-6.1-sol showed a
// tok/s that was the one reply of its 260 calls that used no tool (the only
// Codex turn its session file times) beside "260 requests", while Claude
// Code, whose rows on the Requests tab say "≥100 tok/s" by the dozen, showed
// none. A group with replies whose speed was measured now says that speed and
// how many replies it is of, "62 tok/s · 2 timed replies"; one with none, but
// floors, the least they wrote at, "≥100 tok/s · 30 replies"; one with both,
// both, the one of more replies first, "≥50 tok/s · 259 replies; 125 tok/s ·
// 1 timed reply", and the Output speed card that one as its figure, the other
// under it and the wait for the first token on a line of its own with its
// count; one with neither, nothing. Counts are words, never a bare "(2)". The
// rows of the Requests tab say each one's the same way. Codex's rows tell a
// floor too: a call in a turn of several, timed from what asked for it, its
// output over that time; a turn of one call that reasoned, its answer over
// the time after its first token. A Codex turn of one call is measured, and
// its answer's floor told, only over a second or more after its first token
// and 100 tokens or more; else it tells the least it wrote at over the whole
// turn (Fallmonkey on Discord: a first token at 6.8 s, the model working,
// and the rest 0.2 s later in one push, read as 1,900 tok/s), by the same
// 1,000-token rule as Claude Code's whole-time floor (Fallmonkey again: an
// 11-token astra answer in 2.75 s read ≥4 tok/s, while an 815-token Claude
// Code reply said nothing). Every
// language, a wide window and a narrow one; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (i) => new Date(now - (i + 1) * 60e3).toISOString();

const LOG = { in: 9000, cache_read: 0, cache_write: 0, status: 0, cost: 0, priced: false, source: "log", provider: "session-unknown", providerName: "Local session" };
const SOL = { ...LOG, agent: "codex", agentName: "Codex", icon: "codex-color", model: "gpt-6.1-sol", req: "gpt-6.1-sol", served: "gpt-6.1-sol" };
const CC = { ...LOG, agent: "claude", agentName: "Claude Code", icon: "claudecode-color", model: "claude-opus-5-5", req: "claude-opus-5-5", served: "claude-opus-5-5" };
const ROWS = [
  // the one sol reply its session file timed: 500 tokens in the 4 s after its first
  { ...SOL, t: at(0), out: 500, ms: 5000, ttft_ms: 1000 },
  // a sol call in a turn of several: at least 1500 tokens in 30 s
  { ...SOL, t: at(1), out: 1500, ms: 30000 },
  // a Claude Code reply: at least 2000 tokens in 20 s
  { ...CC, t: at(2), out: 2000, ms: 20000 },
  // a short one tells nothing
  { ...CC, t: at(3), out: 300, ms: 20000 },
  // a sol turn of one call that reasoned: its 272 answer tokens came in the
  // 827 ms after its first token, under a second, so the whole turn, and
  // its 496 tokens are under 1,000: nothing (not 329 from the answer alone)
  { ...SOL, t: at(4), out: 496, reasoning: 224, ms: 11055, ttft_ms: 10228 },
  // one that reasoned and wrote its 1,000-token answer over the 10 s after
  // its first token: at least 100 tok/s (not its 1,200 tokens, 120)
  { ...SOL, t: at(5), out: 1200, reasoning: 200, ms: 12000, ttft_ms: 2000 },
  // Fallmonkey's: a first token at 6.8 s and its 380 tokens 0.2 s later, one
  // push, not 1,900 tok/s, and under 1,000 over the whole turn: nothing
  { ...SOL, t: at(6), out: 380, ms: 7000, ttft_ms: 6800 },
  // Fallmonkey's astra answer: 11 tokens in 2.75 s, not ≥4 tok/s
  { ...SOL, t: at(7), model: "gpt-6-astra", req: "gpt-6-astra", served: "gpt-6-astra", out: 11, ms: 2750, ttft_ms: 2600 },
  // 1,200 tokens 0.5 s after a 9.5 s first token, one push: at least 1,200
  // in the whole 10 s, 120 tok/s
  { ...SOL, t: at(8), out: 1200, ms: 10000, ttft_ms: 9500 },
];
// the groups as usage.Breakdown sums them (TestGroupSpeedSaysHowManyRepliesItIsOf)
const G = {
  sol: { id: "gpt-6.1-sol", calls: 260, input: 2340000, output: 389000, cache_read: 0, cache_write: 0, cost: 0, timed: 1, ttft_ms: 1000, decode_ms: 4000, decode_out: 500, decode_n: 1, floor_out: 388500, floor_ms: 7770000, floor_n: 259 },
  // measured only: 992 tokens over 16 s of two replies, 62 tok/s
  astra: { id: "gpt-6-astra", calls: 9, input: 100000, output: 5000, cache_read: 0, cache_write: 0, cost: 0, timed: 2, ttft_ms: 3000, decode_ms: 16000, decode_out: 992, decode_n: 2 },
  cc: { id: "claude-opus-5-5", calls: 42, input: 210000, output: 64000, cache_read: 0, cache_write: 0, cost: 0, floor_out: 60000, floor_ms: 600000, floor_n: 30 },
  none: { id: "glm-5", calls: 12, input: 1000, output: 100, cache_read: 0, cache_write: 0, cost: 0 },
};
const SUM = ["calls", "input", "output", "timed", "ttft_ms", "decode_ms", "decode_out", "decode_n", "floor_out", "floor_ms", "floor_n"];

function ledger(period, model) {
  const groups = model ? [G[Object.keys(G).find((k) => G[k].id === model)]] : [G.sol, G.cc, G.astra, G.none];
  const tot = {};
  for (const k of SUM) tot[k] = groups.reduce((a, x) => a + (x[k] || 0), 0);
  const rows = model ? ROWS.filter((r) => r.model === model) : ROWS;
  return {
    period, rows, offset: 0, total: rows.length, errors: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0, ...tot,
    by: { model: groups }, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }],
  };
}

const USAGE = {
  calls: 262, errors: 0, input: 1, output: 1, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], path: "",
  agents: [{ ...G.sol, id: "codex", name: "Codex", icon: "codex-color" }, { ...G.none, id: "opencode", name: "OpenCode" }],
  models: [G.sol, G.none],
};

const STATE = (lang) => ({
  agents: [
    { id: "claude", name: "Claude Code", path: "/t/c.json", icon: "claudecode-color", wired: true, fields: [] },
    { id: "codex", name: "Codex", path: "/t/c.toml", icon: "codex-color", wired: true, fields: [] },
  ],
  profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() },
});

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(STATE(lang));
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period"), url.searchParams.get("model")));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(USAGE);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { speed: "Speed", kpi: "Output speed",
    m: (n, k) => `${n} tok/s · ${k} timed ${k === 1 ? "reply" : "replies"}`, f: (n, k) => `≥${n} tok/s · ${k} replies`, join: "; ", timed: (k) => ` · ${k} timed replies`,
    measured: (k, others) => (k === 1 ? `Measured on ${k} reply, from its first token to its end` : `The median of ${k} replies, each measured from its first token to its end`) + (others ? "; the others tell no speed" : ""),
    floorTip: (k) => `At least: the median of ${k} replies read from session files, which don't say when their answers began, so time before that is counted too`, row: "125 tok/s", rowFloor: "≥100 tok/s",
    cx: "≥50 tok/s", answer: "≥100 tok/s", push: "≥120 tok/s", n1200: "1,200", turnTip: "the whole turn. What came after the first token (", answerTip: "At least: 1,000 answer tokens in " },
  zh: { speed: "速度", kpi: "输出速度",
    m: (n, k) => `${n} token/秒 · ${k} 次计时`, f: (n, k) => `≥${n} token/秒 · ${k} 次回复`, join: "；", timed: (k) => ` · ${k} 次计时`,
    measured: (k, others) => (k === 1 ? `实测 ${k} 次回复：从首字到结束` : `${k} 次实测回复的中位数：每次从首字到结束`) + (others ? "；其余请求测不出速度" : ""),
    floorTip: (k) => `下限：${k} 次读自会话文件的回复的中位数。会话文件不记录正文何时开始，之前的时间也算在里面`, row: "125 token/秒", rowFloor: "≥100 token/秒",
    cx: "≥50 token/秒", answer: "≥100 token/秒", push: "≥120 token/秒", n1200: "1,200", turnTip: "首字后只有 ", answerTip: "输出 1,000 个正文 token，200 个推理 token 不计" },
  "zh-TW": { speed: "速度", kpi: null,
    m: (n, k) => `${n} token/秒 · ${k} 次計時`, f: (n, k) => `≥${n} token/秒 · ${k} 次回覆`, join: "；", timed: (k) => ` · ${k} 次計時`,
    measured: (k, others) => (k === 1 ? `實測 ${k} 次回覆：從首字到結束` : `${k} 次實測回覆的中位數：每次從首字到結束`) + (others ? "；其餘請求測不出速度" : ""),
    floorTip: (k) => `下限：${k} 次讀自會話檔案的回覆的中位數。會話檔案不記錄正文何時開始，之前的時間也算在裡面`, row: "125 token/秒", rowFloor: "≥100 token/秒",
    cx: "≥50 token/秒", answer: "≥100 token/秒", push: "≥120 token/秒", n1200: "1,200", turnTip: "首字後只有 ", answerTip: "輸出 1,000 個正文 token，200 個推理 token 不計" },
  ja: { speed: null, kpi: null,
    m: (n, k) => `${n} トークン/秒 · 計測 ${k} 件`, f: (n, k) => `≥${n} トークン/秒 · 応答 ${k} 件`, join: "／", timed: (k) => ` · 計測 ${k} 件`,
    measured: (k, others) => (k === 1 ? `${k} 件の応答で実測：最初のトークンから終わりまで` : `${k} 件の応答の中央値：それぞれ最初のトークンから終わりまで実測`) + (others ? "。ほかのリクエストは速度を計れません" : ""),
    floorTip: (k) => `下限：セッションファイルから読んだ ${k} 件の応答の中央値。回答が始まった時刻がないため、その前の時間も含まれます`, row: "125 トークン/秒", rowFloor: "≥100 トークン/秒",
    cx: "≥50 トークン/秒", answer: "≥100 トークン/秒", push: "≥120 トークン/秒", n1200: "1,200", turnTip: "最初のトークン後は ", answerTip: "で回答 1,000 トークン（推論 200 トークンは含めず）" },
  de: { speed: null, kpi: null,
    m: (n, k) => `${n} Token/s · ${k} gemessene ${k === 1 ? "Antwort" : "Antworten"}`, f: (n, k) => `≥${n} Token/s · ${k} Antworten`, join: "; ", timed: (k) => ` · ${k} gemessene Antworten`,
    measured: (k, others) => (k === 1 ? `Gemessen an ${k} Antwort, vom ersten Token bis zum Ende` : `Median aus ${k} Antworten, jede vom ersten Token bis zum Ende gemessen`) + (others ? "; die übrigen Anfragen zeigen kein Tempo" : ""),
    floorTip: (k) => `Mindestens: Median aus ${k} Antworten aus Sitzungsdateien. Sie sagen nicht, wann die Antworten begannen, also zählt die Zeit davor mit`, row: "125 Token/s", rowFloor: "≥100 Token/s",
    cx: "≥50 Token/s", answer: "≥100 Token/s", push: "≥120 Token/s", n1200: "1.200", turnTip: "der ganze Turn. Nach dem ersten Token kam nur ", answerTip: "Mindestens: 1.000 Antwort-Token in " },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a group's speed says how many replies it is of, and a floor says ≥", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      // 983: five cards in a row, the Output speed one ~158px (#1623,
      // fanbuz); 789: guzi171's window (#1634)
      for (const width of [1400, 983, 789, 700, 560]) {
        const w = L[lang];
        // sol: one measured at 125 and 259 floors at 50, the floors first;
        // astra: two measured at 62; Claude Code: 30 floors at 100; the
        // period: three measured at 75 (1,492 tokens over 20 s), 289 floors
        // at 54
        const sol = w.f(50, 259) + w.join + w.m(125, 1), solTip = w.floorTip(259) + "\n" + w.measured(1, false);
        const astra = w.m(62, 2), astraTip = w.measured(2, true);
        const cc = w.f(100, 30), ccTip = w.floorTip(30);
        await t.test(lang + " " + width, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang));
          await p.goto("http://magpie.test/");
          await p.locator('[data-view="usage"]').first().click();

          // the overview's lists: the measured speed and how many it is of
          const codex = p.locator("#usageAgents .row.stat").first().locator(".sub");
          await codex.waitFor();
          assert((await codex.textContent()).includes(sol), "overview: " + (await codex.textContent()));
          assert(!(await p.locator("#usageAgents .row.stat").nth(1).locator(".sub").textContent()).includes("/"), "a group with no speed says none");

          await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
          await p.locator("#ledWrap .led tbody tr").first().waitFor();
          await p.waitForTimeout(200);

          // the rows say each one's: measured, and at least
          const cell = (i) => p.locator(".led tbody tr.led-row").nth(i).locator("td.speed");
          assert.equal((await cell(0).textContent()).trim(), w.row);
          assert.equal((await cell(1).textContent()).trim(), w.cx);
          assert.equal((await cell(2).textContent()).trim(), w.rowFloor);
          assert.equal((await cell(3).textContent()).trim(), "—");
          assert.equal((await cell(4).textContent()).trim(), "—", "496 tokens over the whole turn tell nothing");
          assert.equal((await cell(5).textContent()).trim(), w.answer);
          assert((await cell(5).getAttribute("title")).includes(w.answerTip), "the answer's floor says so: " + (await cell(5).getAttribute("title")));
          assert.equal((await cell(6).textContent()).trim(), "—", "Fallmonkey's 380 tokens over the whole turn tell nothing");
          assert.equal((await cell(7).textContent()).trim(), "—", "Fallmonkey's 11-token astra answer tells nothing");
          assert.equal((await cell(8).textContent()).trim(), w.push);
          const pushTip = await cell(8).getAttribute("title");
          assert(pushTip.includes(w.turnTip) && pushTip.includes(w.n1200), "the whole turn's floor says so: " + pushTip);

          // the ranking, by tokens: each group's speed beside its requests
          const rk = (id) => p.locator("#ledRank .rk").filter({ has: p.locator(".rk-nm", { hasText: id }) });
          const speedOf = async (id) => (await rk(id).locator(".rk-speed").count()) ? (await rk(id).locator(".rk-speed").textContent()).trim() : "";
          assert.equal(await speedOf("gpt-6.1-sol"), sol);
          assert.equal(await rk("gpt-6.1-sol").locator(".rk-speed").getAttribute("title"), solTip);
          assert.equal(await speedOf("claude-opus-5-5"), cc);
          assert.equal(await rk("claude-opus-5-5").locator(".rk-speed").getAttribute("title"), ccTip);
          assert.equal(await speedOf("glm-5"), "", "no speed, nothing said");

          assert.equal(await speedOf("gpt-6-astra"), astra);
          assert.equal(await rk("gpt-6-astra").locator(".rk-speed").getAttribute("title"), astraTip);

          // the period's speed: the 289 floors' as its figure, the three
          // measured under it, and the wait for the first token on a line of
          // its own with how many it is of
          const kpi = p.locator("#ledKpi .blk").nth(4);
          // (in the card the count is a caption after the figure, no "·" to
          // lead it when it wraps under the figure)
          const card = (x) => x.replace(" · ", " ");
          assert.equal((await kpi.locator(".v").textContent()).trim(), card(w.f(54, 289)));
          assert.deepEqual(await kpi.locator(".v > span").allTextContents(), w.f(54, 289).split(" · "), "the speed, then its count");
          const subs = kpi.locator(".sub");
          assert.equal(await subs.count(), 2);
          assert.equal((await subs.nth(0).textContent()).trim(), w.m(75, 3));
          assert((await subs.nth(1).textContent()).endsWith(w.timed(3)), "the first token's line says its count: " + (await subs.nth(1).textContent()));
          assert(!(await subs.nth(1).textContent()).includes("/"), "no speed on the first token's line");
          assert(await subs.nth(1).evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the first token's count is read whole");
          const [b0, b1] = [await subs.nth(0).boundingBox(), await subs.nth(1).boundingBox()];
          assert(b1.y >= b0.y + b0.height - 1, "the first token's line is under the other speed");
          assert((await kpi.getAttribute("title")).startsWith(w.floorTip(289) + "\n" + w.measured(3, false)), "the card says what it is of");
          // no count is a bare number in parentheses
          assert(!/[(（]\d+[^)）]*[)）]/.test(await kpi.textContent()), "no parenthesised count: " + (await kpi.textContent()));
          if (w.kpi) assert.equal((await kpi.locator(".k").textContent()).trim(), w.kpi);
          // every card's figure, and each part of it, stays inside its card
          // (#1623: "48 token/秒（781 次）" ran 32px past it at 983px), the
          // reply count going under the speed when the two don't fit
          const outside = await p.evaluate(() => [...document.querySelectorAll(".led-strip .v")].filter((v) => v.offsetParent).flatMap((v) => {
            const box = v.closest(".blk").getBoundingClientRect();
            // the text's own box, not its element's: a block's box keeps to
            // the card while its unwrapped text runs past it
            const ink = (e) => { const r = document.createRange(); r.selectNodeContents(e); return r.getBoundingClientRect(); };
            return [v, ...v.querySelectorAll("*")].filter((e) => { const r = ink(e); return r.left < box.left - 0.5 || r.right > box.right + 0.5; }).map((e) => e.textContent);
          }));
          assert.deepEqual(outside, [], "the card's figure is read whole");
          assert(await p.evaluate(() => { const v = document.querySelector("#view-usage"); return v.scrollWidth <= v.clientWidth + 1; }), "the page doesn't scroll sideways under the cards");

          // the ranking by speed: what each row says, ordered by it, the floor's bar its floor
          await reader.click(p, p.locator("#ledMetric .opt").nth(3));
          await p.waitForTimeout(150);
          const vals = await p.locator("#ledRank .rk").evaluateAll((bs) => bs.map((b) => [b.querySelector(".rk-nm").textContent, b.querySelector(".rk-val").textContent.trim(), b.querySelector(".rk-val").title]));
          assert.deepEqual(vals, [["claude-opus-5-5", cc, ccTip], ["gpt-6-astra", astra, astraTip], ["gpt-6.1-sol", sol, solTip], ["glm-5", "—", ""]]);
          const bar = async (i) => p.locator("#ledRank .rk").nth(i).locator(".rk-bar i").evaluate((e) => parseFloat(e.style.width));
          assert.equal(await bar(0), 100);
          assert.equal(await bar(1), 62, "62 against ≥100");
          assert.equal(await bar(2), 50, "sol, with both, orders and bars by the figure it shows first, its 259 floors' ≥50");
          const clipped = await p.locator("#ledRank .rk-val").evaluateAll((vs) => vs.filter((v) => v.scrollWidth > v.clientWidth + 1).length);
          assert.equal(clipped, 0, "every figure is read whole");

          // Claude Code's model alone: only floors, so the period's speed is at least
          await reader.click(p, rk("claude-opus-5-5"));
          await p.waitForFunction(() => document.querySelectorAll(".led tbody tr.led-row").length === 2);
          await p.waitForTimeout(150);
          assert.equal((await p.locator("#ledKpi .blk").nth(4).locator(".v").textContent()).trim(), cc.replace(" · ", " "));
          assert((await p.locator("#ledKpi .blk").nth(4).getAttribute("title")).startsWith(ccTip));

          // no sideways scroll of the page: the view is what scrolls, not the document
          const wide = await p.evaluate(() => { const v = document.querySelector("#view-usage"); return [v.scrollWidth, v.clientWidth, document.documentElement.scrollWidth, window.innerWidth]; });
          assert(wide[0] <= wide[1] + 1 && wide[2] <= wide[3] + 1, "no sideways scroll: " + wide);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
