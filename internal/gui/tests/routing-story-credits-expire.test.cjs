// Run with Node's test runner and Playwright on the module path; see README.md.
// #5646 (louisgreen0726): under Smart a WorkBuddy account whose credit
// pack ran out on 11-11 went ahead of an account renewing on 11-30, and
// the story said the two "renew within the same hour, so the order given
// stays": it compared each window's own reset (renews), while the gateway
// sorted by the credit pack's end (soon) and broke ties by when credits
// lapse (lapse). The story now goes by the keys the gateway sorted by,
// says the order given stays only when they all tie, and the row behind
// says why. The two accounts share a nickname, and each name in the line
// leads with its own provider's logo, not the first alone. In every
// language, at a window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.resolve(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3, D = 24 * H;
const month = Math.floor((now + 22 * D) / H) * H + 59 * 60e3;
const pack = Math.floor((now + 3 * D) / H) * H + 32e3;

// the reporter's: a WorkBuddy account on a credit pack, renewing with the
// month but its credits ending first; a monthly account on another
// provider, renewing with the same month. kind: "pack" (the pack ends
// before the month), "lapse" (both renew with the month, WorkBuddy's
// credits lapse sooner after it), "tie" (alike)
function routeOf(kind) {
  const wb = { id: "workbuddy#1", provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", who: "louis", kind: "account", agent: "workbuddy", model: "glm-5.3",
    known: true, used: 20, renews: [iso(month)], usedRenews: iso(month),
    soon: [iso(kind === "pack" ? pack : month)], ...(kind === "pack" ? { lapse: iso(pack) } : kind === "lapse" ? { lapse: iso(month + 5 * D) } : {}) };
  const mo = { id: "zhipu#1", provider: "zhipu", name: "Zhipu", icon: "zhipu-color", who: "louis", kind: "account", agent: "claude", model: "glm-5.3",
    known: true, used: 30, renews: [iso(month)], usedRenews: iso(month), soon: [iso(month)], ...(kind === "lapse" ? { lapse: iso(month + 50 * D) } : {}) };
  return {
    id: 9, seq: 9, time: iso(now - 2000), agent: "claude", model: "group/glm", provider: "workbuddy", done: true, status: 200, ms: 900, tokens: 1500,
    group: { id: "glm", name: "GLM", routing: "", affinity: "", members: ["workbuddy/glm-5.3", "zhipu/glm-5.3"] },
    order: [wb, mo], tries: [{ id: wb.id, model: wb.model, start: iso(now - 2000), done: true, ms: 800, status: 200 }],
  };
}

function serve(lang, route) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: true } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 9, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// what each line says, in its fixed words: the pack's end, the lapse
// breaking a tie, the order kept, and the row behind
const words = {
  en: { pack: ["goes first: its credits expire in", "renews, and what is left of them then is lost"], lapse: ["goes first: it renews within the same hour as", "but its credits expire sooner"], tie: ["so the order given stays"], row: "one whose credits expire sooner goes first" },
  zh: { pack: ["排第一：它的积分", "重置，到期时剩下的积分作废"], lapse: ["排第一：它和", "但它的积分更早到期"], tie: ["保持原顺序"], row: "先用积分更早到期的账号" },
  "zh-TW": { pack: ["排第一：它的點數", "重置，到期時剩下的點數作廢"], lapse: ["排第一：它和", "但它的點數更早到期"], tie: ["保持原順序"], row: "先用點數更早到期的帳號" },
  ja: { pack: ["が先です：そのクレジットは", "の更新より早く"], lapse: ["と同じ時間帯に更新されますが", "クレジットの期限が早く"], tie: ["設定順のままです"], row: "クレジットの期限が早いアカウントを優先" },
  de: { pack: ["kommt zuerst: Sein Guthaben läuft in", "verfällt"], lapse: ["Es erneuert sich in derselben Stunde wie", "aber sein Guthaben läuft früher ab"], tie: ["bleibt die vorgegebene Reihenfolge"], row: "eines, dessen Guthaben früher abläuft, geht vor" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the story goes by the keys Smart sorted by`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        for (const kind of ["pack", "lapse", "tie"]) {
          await t.test(`${lang} at ${width}px, ${kind}`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            try {
              const page = await ctx.newPage();
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang, routeOf(kind)));
              await page.goto("http://magpie.test/?view=routing");
              const why = page.locator(".rt-steps li.why").first();
              await why.waitFor();
              const said = (await why.textContent()).trim();
              const w = words[lang];
              for (const f of w[kind]) assert.ok(said.includes(f), `${lang} ${kind}: ${JSON.stringify(f)} not in ${JSON.stringify(said)}`);
              // the order given stays only when every key ties
              if (kind !== "tie") for (const f of w.tie) assert.ok(!said.includes(f), `${lang} ${kind}: says the order stays: ${said}`);
              // both named "louis", each with its own provider's logo
              const named = await why.locator(".rt-named").evaluateAll((ns) => ns.map((n) => [n.querySelector(".ic")?.dataset.icon, n.textContent]));
              assert.deepEqual(named, [["workbuddy-color", "louis"], ["zhipu-color", "louis"]], `${lang} ${kind}: ${said}`);
              // the row behind says why the other went first
              const rows = page.locator(".rt-accts > li");
              await rows.nth(1).waitFor();
              const behind = (await rows.nth(1).textContent()).trim(), first = (await rows.nth(0).textContent()).trim();
              assert.equal(behind.includes(w.row), kind !== "tie", `${lang} ${kind}: ${behind}`);
              assert.ok(!first.includes(w.row), `${lang} ${kind}: ${first}`);
              const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
              assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
              assert.deepEqual(errors, []);
            } finally {
              await ctx.close();
            }
          });
        }
      }
    }
  });
}
