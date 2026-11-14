// Run with Node's test runner and Playwright on the module path; see README.md.
// The caption over the Routing stage tells a try that answered as figures,
// each in a place of its own (Puremilk_chun on X: "Codex's own sign-in
// (gpt-6.1-sol) 以 high 推理强度 用 41 秒 完成回答 · 197.5k token · 35 秒
// 后出首字 · 40 token/秒 · 请求缓存命中率 98%" was one sentence whose
// length and line breaks moved with every value, so the reader looked for
// each one again). Who answered, its model and its effort head it; under
// them the time it took, its tokens, its first token, its speed and its
// cache hit rate, each a value over its name. The next request's figures
// take the same places, whatever their length, and a figure it doesn't
// tell is a dash in its place, not a gap the others close up. The sentence
// is still on hover. English, Chinese (simplified and traditional),
// Japanese and German, at 1100px and 420px, in both engines; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ago = (s) => new Date(Date.now() - s * 1e3).toISOString();
const M = "gpt-6.1-sol";
const order = [
  { id: "codex", provider: "codex", name: "Codex", who: "me@example.com", kind: "account", agent: "codex", model: M, known: true, used: 20 },
  { id: "codex@team", provider: "codex", name: "Codex", who: "a-much-longer-name@team.example.com", kind: "account", agent: "codex", model: M, known: true, used: 20 },
];
// the reporter's request: 41 s, 197.5k tokens, its first token at 35 s,
// 240 written in the 6 s after it (40 tok/s), 98% of its prompt cached
const first = (done) => ({
  id: 1, seq: 1, time: ago(42), agent: "codex", model: M, provider: "codex", done, status: done ? 200 : 0, ms: done ? 41000 : 0,
  tokens: done ? 197500 : 0, out: done ? 240 : 0, effort: "high",
  usage: done ? [{ in: 3950, cache_read: 193310, out: 240 }] : [],
  order,
  tries: [{ id: "codex", model: M, effort: "high", start: ago(41), done, ...(done ? { status: 200, ms: 41000, ttft: 35000 } : {}) }],
});
// the next: another account, no first token told (so no speed), nothing
// cached that the vendor counted
const second = (done) => ({
  id: 2, seq: 2, time: ago(3), agent: "codex", model: M, provider: "codex", done, status: done ? 200 : 0, ms: done ? 2100 : 0,
  tokens: done ? 900 : 0, out: done ? 60 : 0, effort: "low",
  usage: done ? [{ in: 840, out: 60, cache_unknown: true }] : [],
  order: [order[1], order[0]],
  tries: [{ id: "codex@team", model: M, effort: "low", start: ago(2), done, ...(done ? { status: 200, ms: 2100 } : {}) }],
});

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: an answer's figures each keep a place of their own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator(".rt-cap").evaluate((e) => e.scrollIntoView({ block: "center" })).catch(() => {});
            await page.waitForTimeout(800);
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `answer-${engine}-${lang}-${width}.png`) }).catch(() => {});
          }
          await browser.close();
        });
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const live = { pending: [], queued: [], seq: 0 };
        await page.route("**/*", serve(lang, live));
        const send = (...routes) => {
          const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq, rerouted: 0, errors: 0 }, routes };
          const r = live.pending.shift();
          if (r) r.fulfill({ json: d }); else live.queued.push(d);
        };
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const card = () => page.evaluate(() => {
          const box = document.querySelector(".rt-cap .rt-done");
          if (!box) return null;
          const left = box.getBoundingClientRect().left;
          const at = (e) => Math.round(e.getBoundingClientRect().left - left);
          return {
            who: box.querySelector(".who").textContent, mdl: box.querySelector(".mdl").textContent, eff: box.querySelector(".effort-of").textContent,
            figs: [...box.querySelectorAll(".fig")].map((f) => [f.querySelector("b").textContent, f.querySelector("span").textContent, f.classList.contains("none")]),
            at: [...box.children].map(at),
            top: [...box.querySelectorAll(".fig")].map((f) => Math.round(f.getBoundingClientRect().top - box.getBoundingClientRect().top)),
            head: [".who", ".effort-of"].map((q) => { const r = box.querySelector(q).getBoundingClientRect(); return [Math.round(r.top), Math.round(r.height)]; }),
            cut: [...box.querySelectorAll(".fig b")].filter((b) => b.scrollWidth > b.clientWidth + 1).map((b) => b.textContent),
            tip: box.parentElement.title,
            capH: Math.round(box.parentElement.getBoundingClientRect().height),
            w: Math.round(box.getBoundingClientRect().width),
          };
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.waitForFunction(() => document.querySelector(".rt-cap"));
        await page.waitForTimeout(300); // the first trace in, the long poll waiting

        send(first(false));
        await page.waitForTimeout(1500);
        send(first(true));
        await page.waitForFunction(() => document.querySelector(".rt-cap .rt-done .who")?.textContent.includes("me@example.com"));
        const a = await card();
        assert.equal(a.mdl, M);
        assert.equal(a.eff, "high");
        // the effort on the account's line, as tall as it
        assert.deepEqual(a.head[1], a.head[0], `effort at ${a.head[1]}, account at ${a.head[0]}`);
        assert.deepEqual(a.figs, [
          [await say("{n} s", { n: "41" }), await say("Duration"), false],
          ["197.5k", await say("Tokens"), false],
          [await say("{n} s", { n: "35" }), await say("First token"), false],
          [await say("{n} tok/s", { n: 40 }), await say("Speed"), false],
          ["98%", await say("Cache"), false],
        ]);
        assert.deepEqual(a.cut, [], `${lang} at ${width}: cut ${a.cut.join(", ")} in ${a.w}px`);
        // the five figures on one line where the stage is wide; three to a
        // line on a narrow one
        if (width === 1100) assert.equal(new Set(a.top).size, 1, `figures on more than one line: ${a.top}`);
        else assert.deepEqual([a.top[0] === a.top[2], a.top[2] < a.top[3], a.top[3] === a.top[4]], [true, true, true], `${a.top}`);
        // the sentence on hover
        assert.ok(a.tip && a.tip.includes("me@example.com"), a.tip);
        // the whole sentence in the list of steps still
        assert.ok(a.tip.length > 20);

        send(second(false));
        await page.waitForTimeout(1500);
        send(second(true));
        await page.waitForFunction(() => document.querySelector(".rt-cap .rt-done .who")?.textContent.includes("a-much-longer-name"));
        const b = await card();
        assert.equal(b.eff, "low");
        assert.deepEqual(b.figs.map((f) => f[2]), [false, false, true, true, true], JSON.stringify(b.figs));
        assert.deepEqual(b.figs.slice(2).map((f) => f[0]), ["—", "—", "—"]);
        // every part where it was, whatever the values' lengths
        assert.deepEqual(b.at, a.at);
        assert.deepEqual(b.top, a.top);
        assert.equal(b.capH, a.capH, "the caption kept its height");
        // nothing scrolls sideways
        const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
