// Run with Node's test runner and Playwright on the module path; see README.md.
// The model speed ranking on the Usage page's Requests tab (@flying_crp on X:
// a ranking of how fast each model answers): each model's median speed and
// first-token wait and its request count, the fastest first, sortable by each
// column, a model with nothing measured always last, in every language, at a
// phone's width without scrolling sideways, and gone when no request counted.
// By provider (skingstyle48487 on X, with a dozen relays): each model on each
// provider with its tries, the share that failed and the replies that named
// another model, and a Check… that sends its calls only on the dialog's
// Check, saying what each found and how many answers and tool calls were
// right, kept as the provider's Answers and Tool calls beside the others'.
// No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const row = (i, model, out, ttft) => ({
  route_id: 700 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", host: "me@example.com", req: "claude-opus-4-1-20250805", model, served: model,
  effort: "high", in: 1000, out, cache_read: 0, cache_write: 0, ms: 3000, ttft_ms: ttft, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});
const ROWS = [row(0, "glm-fast", 135, 600), row(1, "kimi-slow", 120, 2100), row(2, "burst-model", 200, 400)];
// the ranking the ledger sends: glm-fast 75 tok/s, kimi-slow 10, and one whose
// replies came in a burst, a first token and no speed
const SPEEDS = [
  { model: "glm-fast", name: "GLM Fast", requests: 28, tps: 75.4, tps_n: 27, ttft_ms: 600 },
  { model: "kimi-slow", name: "kimi-slow", requests: 3, tps: 10.2, tps_n: 3, ttft_ms: 2100 },
  { model: "burst-model", name: "burst-model", requests: 5, ttft_ms: 400 },
];
// the same models by provider: glm-fast on two, one of which failed 1 of 4
// tries and once answered with another model
const PROV = [
  { model: "glm-fast", name: "GLM Fast", provider: "zai", provider_name: "Z.ai Coding Plan", requests: 20, tries: 20, tps: 80, tps_n: 20, ttft_ms: 550 },
  { model: "glm-fast", name: "GLM Fast", provider: "relay9", provider_name: "Relay Nine", requests: 3, tries: 4, failed: 1, swapped: 1, tps: 40, tps_n: 3, ttft_ms: 900 },
  { model: "kimi-slow", name: "kimi-slow", provider: "relay9", provider_name: "Relay Nine", requests: 3, tries: 3, tps: 10.2, tps_n: 3, ttft_ms: 2100 },
];
const CHECK = {
  provider: "relay9", model: "glm-fast", parts: [
    { kind: "count", ok: true, match: true, want: "1 2 3", got: "1 2 3", ms: 2400, ttft_ms: 600, tps: 42, input: 4200, output: 120, prompt_chars: 118, tries: 1 },
    { kind: "math", ask: "1234 × 5678", ok: true, match: false, want: "7006652", got: "7006552", served: "qwen3-8b", ms: 900, ttft_ms: 500, input: 30, output: 5, tries: 1 },
    { kind: "math", ask: "987 × 654", ok: true, match: true, want: "645498", got: "645498", ms: 700, ttft_ms: 400, input: 30, output: 5, tries: 1 },
    { kind: "tool", ask: "get_weather", ok: false, match: false, want: "get_weather", status: 403, error: "this key has no access to tools", ms: 80, tries: 1 },
    { kind: "tool", ask: "get_time", ok: true, match: true, want: "get_time", got: 'get_time({"city":"Tokyo"})', ms: 800, ttft_ms: 600, input: 90, output: 12, tries: 1 },
    { kind: "steps", ask: "(37 × 43 + 1289) × 7 − 2468", ok: true, match: false, want: "17692", got: "17682", ms: 900, ttft_ms: 500, input: 40, output: 5, tries: 1 },
    { kind: "exact", ask: "sort", ok: true, match: true, want: "apple-banana-cherry-fig-pear", got: "apple-banana-cherry-fig-pear", ms: 700, ttft_ms: 400, input: 50, output: 9, tries: 1 },
  ],
};
let checks = 0;
const FAST = { id: "glm-fast", calls: 28, input: 1, output: 3780, cache_read: 0, cache_write: 0, cost: 0.3, timed: 27, ttft_ms: 27 * 600, decode_ms: 27 * 1800, decode_out: 27 * 135 };

let speeds = SPEEDS;
function ledger(period) {
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0,
    input: 1, output: 3780, cache_read: 0, cache_write: 0, cost: 0.3, unpriced: 0,
    timed: 27, ttft_ms: 27 * 600, decode_ms: 27 * 1800, decode_out: 27 * 135,
    by: { model: [FAST] }, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
    speeds, provider_speeds: speeds.length ? PROV : [], speed_min: 100,
  };
}

const STATE = (lang) => ({
  agents: [
    { id: "claude", name: "Claude Code", path: "/t/c.json", icon: "claudecode-color", wired: true, fields: [{ key: "model", label: "model", value: "a", options: [{ value: "a", label: "Claude Sonnet 4.5", ref: "claude/a" }] }] },
    { id: "codex", name: "Codex", path: "/t/c.toml", icon: "codex-color", wired: true, fields: [{ key: "model", label: "model", value: "b", options: [{ value: "b", label: "GPT", ref: "openai/b" }] }] },
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
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period")));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/relay-check") {
      checks++;
      assert.deepEqual(route.request().postDataJSON(), { provider: "relay9", model: "glm-fast" });
      return json(CHECK);
    }
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
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
  en: { title: "Model speed", heads: ["Model", "Speed", "First token", "Requests"], fast: "75 tok/s", slow: "10 tok/s", wait: "600 ms", by: ["Models", "Providers"], more: ["Failed", "Other model"], check: "Check…", run: "Check", asked: "Expected answer", tokens: "4,200", sum: ["Answers right: 3 of 5", "Right tool calls: 1 of 2"], checked: ["Answers", "Tool calls"], sort: "Sort five words" },
  zh: { title: "模型速度", heads: ["模型", "速度", "首字耗时", "请求"], fast: "75 token/秒", slow: "10 token/秒", wait: "600 毫秒", by: ["模型", "供应商"], more: ["失败", "其他模型"], check: "检查…", run: "检查", asked: "符合预期", tokens: "4,200", sum: ["答对 3 / 5", "工具调用正确 1 / 2"], checked: ["答对", "工具调用"], sort: "给五个词排序" },
  "zh-TW": { title: "模型速度", heads: ["模型", "速度", "首字耗時", "請求"], fast: "75 token/秒", slow: "10 token/秒", wait: "600 毫秒", by: ["模型", "供應商"], more: ["失敗", "其他模型"], check: "檢查…", run: "檢查", asked: "符合預期", tokens: "4,200", sum: ["答對 3 / 5", "工具呼叫正確 1 / 2"], checked: ["答對", "工具呼叫"], sort: "為五個詞排序" },
  ja: { title: "モデルの速度", heads: ["モデル", "速度", "最初のトークン", "リクエスト"], fast: "75", slow: "10", wait: "600", by: ["モデル", "プロバイダ"], more: ["失敗", "別のモデル"], check: "チェック…", run: "チェック", asked: "想定どおり", tokens: "4,200", sum: ["正解 3 / 5", "正しいツール呼び出し 1 / 2"], checked: ["正解", "ツール呼び出し"], sort: "5つの単語を並べ替える" },
  de: { title: "Modelltempo", heads: ["Modell", "Tempo", "Erstes Token", "Anfragen"], fast: "75", slow: "10", wait: "600", by: ["Modelle", "Anbieter"], more: ["Fehlgeschlagen", "Anderes Modell"], check: "Prüfen…", run: "Prüfen", asked: "Wie erwartet", tokens: "4.200", sum: ["Richtige Antworten: 3 von 5", "Richtige Werkzeugaufrufe: 1 von 2"], checked: ["Richtig", "Tool-Aufrufe"], sort: "Fünf Wörter sortieren" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the model speed ranking", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      await t.test(lang, async () => {
        speeds = SPEEDS;
        const errors = [];
        // a phone's width, then the window's
        const ctx = await browser.newContext({ viewport: { width: 400, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledSpeed .led-spd tbody tr").first().waitFor();
        await p.waitForTimeout(200);

        const card = p.locator("#ledSpeed");
        assert.equal((await card.locator(".led-spd-t").textContent()).trim(), w.title);
        const heads = await card.locator("thead th").allTextContents();
        assert.deepEqual(heads.map((x) => x.replace(/[↑↓]/g, "").trim()), w.heads);
        const names = () => card.locator("tbody td.nm").allTextContents();
        const sortOf = () => card.locator("thead th").evaluateAll((ths) => ths.map((th) => th.getAttribute("aria-sort")));
        const head = (i) => card.locator("thead th").nth(i).locator("button");

        // the fastest first; the one with no speed last
        assert.deepEqual(await names(), ["GLM Fast", "kimi-slow", "burst-model"]);
        assert.deepEqual(await sortOf(), ["none", "descending", "none", "none"]);
        const first = await card.locator("tbody tr").first().locator("td").allTextContents();
        assert(first[1].includes(w.fast), "the speed: " + first[1]);
        assert(first[2].includes(w.wait), "the first token: " + first[2]);
        assert.equal(first[3].trim(), "28");
        assert.equal((await card.locator("tbody tr").nth(2).locator("td").nth(1).textContent()).trim(), "—", "burst-model: no speed measured");

        // the same column again turns it: slowest first, the unmeasured still last
        await head(1).click();
        assert.deepEqual(await names(), ["kimi-slow", "GLM Fast", "burst-model"]);
        assert.deepEqual(await sortOf(), ["none", "ascending", "none", "none"]);
        // the first token's wait: shortest first
        await head(2).click();
        assert.deepEqual(await names(), ["burst-model", "GLM Fast", "kimi-slow"]);
        // the request count: most first
        await head(3).click();
        assert.deepEqual(await names(), ["GLM Fast", "burst-model", "kimi-slow"]);
        assert.deepEqual(await sortOf(), ["none", "none", "none", "descending"]);
        // the name: A to Z
        await head(0).click();
        assert.deepEqual(await names(), ["burst-model", "GLM Fast", "kimi-slow"]);
        assert.equal(await p.evaluate(() => document.activeElement?.classList.contains("led-spd-h")), true, "the focus stays on the header");

        // at a phone's width the card and the page don't scroll sideways
        const fits = await card.evaluate((e) => ({ card: e.scrollWidth <= e.clientWidth + 1, scroll: e.querySelector(".led-spd-scroll").scrollWidth <= e.querySelector(".led-spd-scroll").clientWidth + 1, doc: document.documentElement.scrollWidth <= window.innerWidth + 1 }));
        assert.deepEqual(fits, { card: true, scroll: true, doc: true });
        // a long model name is cut, not the numbers
        const cut = await card.locator("tbody td.nm").first().evaluate((e) => getComputedStyle(e).textOverflow);
        assert.equal(cut, "ellipsis");

        // by provider: each model on each provider, with its tries, the
        // share that failed and the replies that named another model
        const by = card.locator(".led-spd-by .opt");
        assert.deepEqual((await by.allTextContents()).map((x) => x.trim()), w.by);
        await by.nth(1).click();
        await card.locator(".led-spd.by-prov").waitFor();
        // the sort is kept across the switch: by name, then the fastest first
        assert.deepEqual(await card.locator("tbody .led-spd-prov").allTextContents(), ["Relay Nine", "Z.ai Coding Plan", "Relay Nine"]);
        await head(1).click();
        const pheads = (await card.locator("thead th").allTextContents()).map((x) => x.replace(/[↑↓]/g, "").trim());
        assert.deepEqual(pheads, [...w.heads, ...w.more, ""]);
        assert.deepEqual(await card.locator("tbody .led-spd-prov").allTextContents(), ["Z.ai Coding Plan", "Relay Nine", "Relay Nine"]);
        const relay = await card.locator("tbody tr").nth(1).locator("td").allTextContents();
        assert.equal(relay[3].trim(), "4", "tries");
        assert.equal(relay[4].trim(), "25%", "1 of 4 failed");
        assert.equal(relay[5].trim(), "1", "one reply named another model");
        // the failed share sorts, most first
        await head(4).click();
        assert.equal((await card.locator("tbody tr").first().locator(".led-spd-prov").textContent()), "Relay Nine");
        await card.locator("thead th").nth(1).locator("button").click();
        const fitsProv = await card.evaluate((e) => ({ card: e.scrollWidth <= e.clientWidth + 1, doc: document.documentElement.scrollWidth <= window.innerWidth + 1 }));
        assert.deepEqual(fitsProv, { card: true, doc: true });
        if (lang === "en" && process.env.SHOTS) {
          // the page scrolls inside the window: a tall one shows the card
          await p.setViewportSize({ width: 400, height: 3000 });
          await p.waitForTimeout(300);
          await card.screenshot({ path: path.join(process.env.SHOTS, engine + "-providers-400.png") });
          await p.setViewportSize({ width: 1000, height: 3000 });
          await p.waitForTimeout(300);
          await card.screenshot({ path: path.join(process.env.SHOTS, engine + "-providers-1000.png") });
          await p.setViewportSize({ width: 400, height: 800 });
        }

        // Check… opens a dialog and sends nothing; only its Check does
        checks = 0;
        const go = card.locator("tbody tr").nth(1).locator(".led-spd-check");
        assert.equal((await go.textContent()).trim(), w.check);
        // found without pointing at its row first
        await p.mouse.move(0, 0);
        await p.waitForTimeout(200);
        assert.equal(await go.evaluate((b) => getComputedStyle(b).opacity), "1", "Check… shows without hover");
        assert(await go.isVisible());
        await go.click();
        const dlg = p.locator("#modal .relay-check");
        await dlg.waitFor();
        await p.waitForTimeout(150);
        assert.equal(checks, 0, "opening the dialog sends no call");
        await dlg.locator(".bar button.primary").click();
        await dlg.locator(".relay-part").nth(6).waitFor();
        assert.equal(checks, 1);
        const parts = dlg.locator(".relay-part");
        // the right answers counted apart from the right tool calls
        assert.deepEqual((await dlg.locator(".relay-sum span").allTextContents()).map((x) => x.trim()), w.sum);
        assert.equal((await parts.nth(0).locator(".relay-said").textContent()).trim(), w.asked);
        assert.deepEqual(await dlg.locator(".relay-mark").evaluateAll((ms) => ms.map((m) => m.className.replace("relay-mark ", ""))), ["ok", "off", "ok", "err", "ok", "off", "ok"]);
        assert((await parts.nth(1).locator(".relay-name").textContent()).includes("1234 × 5678"));
        assert((await parts.nth(4).locator(".relay-name").textContent()).includes("get_time"));
        // the harder ones by name: a longer sum, and an exact item by its id
        assert((await parts.nth(5).locator(".relay-name").textContent()).includes("(37 × 43 + 1289) × 7 − 2468"));
        assert((await parts.nth(5).locator(".relay-line.mono").textContent()).includes("17682"));
        assert.equal((await parts.nth(6).locator(".relay-name").textContent()).trim(), w.sort);
        assert.equal(await parts.nth(2).locator(".relay-line").count(), 0, "an answer as asked says nothing more");
        const count = await parts.nth(0).locator(".relay-line").allTextContents();
        assert(count.some((x) => x.includes("118") && x.includes(w.tokens)), "the prompt's count: " + count);
        const math = (await parts.nth(1).locator(".relay-line").allTextContents()).join("\n");
        assert(math.includes("7006552") && math.includes("qwen3-8b"), math);
        assert((await parts.nth(3).locator(".relay-line.err").textContent()).includes("403 · this key has no access to tools"));
        const fitsDlg = await dlg.evaluate((e) => ({ doc: document.documentElement.scrollWidth <= window.innerWidth + 1, dlg: e.parentElement.scrollWidth <= e.parentElement.clientWidth + 1 }));
        assert.deepEqual(fitsDlg, { doc: true, dlg: true }, "the dialog fits a phone's width");
        if (lang === "en" && process.env.SHOTS) await p.locator("#modal > *").first().screenshot({ path: path.join(process.env.SHOTS, engine + "-check-400.png") });
        await p.keyboard.press("Escape");
        await dlg.waitFor({ state: "hidden" });
        // the check is kept beside the other providers: Answers and Tool
        // calls, for the one checked, and nothing for the others
        const checkedHeads = async () => (await card.locator("thead th").allTextContents()).map((x) => x.replace(/[↑↓]/g, "").trim());
        assert.deepEqual(await checkedHeads(), [...w.heads, ...w.more, ...w.checked, ""]);
        const cells = (i) => card.locator("tbody tr").nth(i).locator("td").allTextContents();
        assert.deepEqual((await cells(1)).slice(6, 8).map((x) => x.trim()), ["3/5", "1/2"]);
        assert.deepEqual((await cells(0)).slice(6, 8).map((x) => x.trim()), ["—", "—"]);
        // the checked one first by Answers, the ones not checked last
        await head(6).click();
        assert.equal(await card.locator("tbody tr").first().locator(".led-spd-prov").textContent(), "Relay Nine");
        assert.equal(await card.locator("tbody tr").first().locator("td").nth(6).getAttribute("class"), "n off");
        if (lang === "en" && process.env.SHOTS) {
          await p.setViewportSize({ width: 1000, height: 3000 });
          await p.waitForTimeout(300);
          await card.screenshot({ path: path.join(process.env.SHOTS, engine + "-providers-checked-1000.png") });
          await p.setViewportSize({ width: 400, height: 800 });
        }
        // the choice is kept: Providers again after a reload
        await p.reload();
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await card.locator(".led-spd.by-prov").waitFor();
        assert.deepEqual(await checkedHeads(), [...w.heads, ...w.more, ...w.checked, ""], "the check is kept after a reload");
        await card.locator(".led-spd-by .opt").first().click();
        await card.locator(".led-spd:not(.by-prov)").waitFor();

        // nothing counted: the card is gone
        speeds = [];
        await p.reload();
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(200);
        assert.equal(await card.isVisible(), false, "no ranking without a counted request");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
