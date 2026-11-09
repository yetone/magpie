// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie's web page on a phone (#4943, precis1onz: iOS Safari, dark, the
// Usage page). It went sideways: Requests' "Split by" and its five choices
// ran past the screen and the view scrolled with it. And a dark band stood
// at the screen's foot: the Requests table's stand-in scrollbar (#799),
// which a touch screen draws empty. Every top-level view, Usage's tabs and
// Settings' tabs, filled with long names, fits the phone's width, and
// nothing is held to the view's foot. At 375px in every language and at
// 390 and 430 in English, Chromium and WebKit; no backend, the API is
// faked. MAGPIE_ASSETS runs it on older assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit, devices } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const now = Date.now();
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const MODELS = ["deepseek-v4-flash", "claude-opus-5-5", "gpt-6.1-sol", "qwen/qwen3-coder-480b-a35b-instruct:free"];
const R = (i) => {
  const model = MODELS[i % MODELS.length];
  return { route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
    provider: "openrouter", providerName: "OpenRouter", host: "openrouter.ai as someone.with.a.long.name@example.com", providerAccount: "someone.with.a.long.name@example.com",
    ep: "/v1/responses", req: model, model, served: model, effort: "medium", key: "phone-key",
    in: 4000 + i, out: 200 + i, cache_read: 110000, cache_write: 0, ms: 11000, ttft_ms: 6400, status: i % 9 === 4 ? 402 : 200, retried: i % 7 === 3,
    rid: "r" + i, cost: i % 5 ? 0.00175 : 0, priced: i % 5 !== 0 };
};
const ROWS = Array.from({ length: 30 }, (_, i) => R(i));
const M = (id, calls, out) => ({ id, calls, input: 1, output: out, cache_read: 0, cache_write: 0, cost: 0.003, timed: calls, ttft_ms: 9000, decode_ms: 6000, decode_out: out });
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
function HOURS() {
  const m = new Date(); m.setHours(0, 0, 0, 0);
  return Array.from({ length: 24 }, (_, h) => {
    const k = (h % 5) + 1;
    return { label: String(h).padStart(2, "0"), time: new Date(m.getTime() + h * 3600e3).toISOString(), calls: k * 3, input: k * 100, output: k * 500, cache_read: k * 90000, cache_write: 0, cost: k * 0.01,
      timed: k, ttft_ms: k * 900, decode_ms: k * 4000, decode_out: k * 400,
      by: { model: Object.fromEntries(MODELS.map((id, j) => [id, part(M(id, k + j, k * 300))])) } };
  });
}
const LEDGER = (period) => ({
  period, rows: ROWS, offset: 0, total: 25264, calls: 25264, errors: 970, input: 499e6, output: 22e6, cache_read: 1.98e9, cache_write: 0,
  cost: 99.28, unpriced: 10453, timed: 1160, ttft_ms: 3.4e6, decode_ms: 12000e3, decode_out: 612000,
  by: { model: MODELS.map((id, i) => M(id, 10 + i, 478)) }, bucket: "hour", series: HOURS(),
  agents: [{ id: "codex", name: "Codex", icon: "codex-color" }, { id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  providers: [{ id: "openrouter", name: "OpenRouter" }, { id: "deepseek", name: "DeepSeek" }],
  accounts: [{ id: "someone.with.a.long.name@example.com", providers: ["OpenRouter"] }, { id: "b@example.com", providers: ["DeepSeek"] }],
  callerKeys: [{ id: "phone-key", name: "phone-key" }, { id: "laptop", name: "laptop" }],
});
function HEAT() {
  const to = new Date(), from = new Date(); from.setDate(from.getDate() - 364);
  const days = [];
  for (let i = 0; i < 300; i++) { const d = new Date(); d.setDate(d.getDate() - i); days.push({ date: iso(d), calls: 3 + i, tokens: 1000 * (i + 1), cost: 0.01 * i }); }
  return { from: iso(from), to: iso(to), days };
}
const G = { calls: 123, errors: 2, input: 123456, output: 67890, cache_read: 543210, cache_write: 1234, reasoning: 12345, cost: 12.34, unpriced: 0 };
const USAGE = { ...G, path: "/test/usage.jsonl", bucket: "day",
  agents: [{ ...G, name: "Claude Code", icon: "claudecode-color" }, { ...G, name: "Codex", icon: "codex-color" }],
  models: MODELS.map((name) => ({ ...G, name })),
  accounts: [{ id: "codex@someone.with.a.long.name@example.com", provider: "codex", account: "someone.with.a.long.name@example.com", name: "someone.with.a.long.name@example.com", sub: "Codex", icon: "generic", ...G }],
  callerKeys: [],
  series: Array.from({ length: 30 }, (_, i) => ({ ...G, label: "10-" + String(i + 1).padStart(2, "0") })) };

const PROVIDER = () => ({
  id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true }, { id: "qwen/qwen3-coder-480b-a35b-instruct:free", name: "qwen/qwen3-coder-480b-a35b-instruct:free", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "someone.with.a.long.name@example.com", plan: "team",
    logins: [
      { user: "someone.with.a.long.name@example.com", org: "A Rather Long Organisation Name", plan: "team", active: true, on: true },
      { user: "spare.account.with.long.name@example.com", plan: "max", on: true },
    ],
  },
});
const GROUP = { id: "sol", name: "A group with a rather long name", members: ["claude/claude-opus-5-5", "claude/qwen/qwen3-coder-480b-a35b-instruct:free"], routing: "order", ready: true,
  memberInfo: [{ id: "claude/claude-opus-5-5", ready: true }, { id: "claude/qwen/qwen3-coder-480b-a35b-instruct:free", ready: true }] };
const SESSIONS = Array.from({ length: 6 }, (_, i) => ({ id: "s" + i, agent: "claude", name: "Claude Code", icon: "claudecode-color",
  cwd: "/Users/someone/workspace/projects/a-project-with-a-rather-long-folder-name/" + i, title: "Find every element wider than the viewport at phone widths and make it wrap " + i,
  updated: new Date(now - i * 3600e3).toISOString() }));
function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/codex.toml", icon: "codex-color", wired: true, fields: [] }], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/usage/requests") return json(LEDGER(url.searchParams.get("period")));
    if (url.pathname === "/api/usage/heatmap") return json(HEAT());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(USAGE);
    if (url.pathname === "/api/providers") return json({ providers: [PROVIDER()], presets: [], excluded: [], models: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/sessions") return json({ sessions: SESSIONS, dirs: [] });
    if (url.pathname === "/api/groups") return json({ groups: [GROUP], models: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, plugins: [], movable: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, plugins: [] } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// what runs past the phone's sides and isn't inside a box of its own that
// scrolls or clips within the screen: the outermost such element of each
const WIDE = () => {
  const vw = innerWidth, out = [];
  const open = [...document.querySelectorAll(".view")].find((v) => !v.hidden && v.offsetWidth);
  const kept = (e) => {
    for (let a = e.parentElement; a && a !== document.body; a = a.parentElement) {
      if (a.classList.contains("view")) return false;
      const s = getComputedStyle(a);
      if (s.overflowX !== "visible") { const r = a.getBoundingClientRect(); if (r.left >= -1 && r.right <= vw + 1) return true; }
    }
    return false;
  };
  const shown = (e) => { const s = getComputedStyle(e); return s.visibility !== "hidden" && e.getClientRects().length; };
  for (const e of document.body.querySelectorAll("*")) {
    if (!shown(e) || e.closest(".pop, .proto-menu, #modal, .rail-tip, .view-ends, .mask-shelf")) continue;
    const s = getComputedStyle(e);
    if (s.position === "fixed") continue;
    const r = e.getBoundingClientRect();
    if (!r.width) continue;
    if (r.right <= vw + 1 && r.left >= -1) continue;
    if (kept(e)) continue;
    const p = e.parentElement, pr = p.getBoundingClientRect();
    if ((pr.right > vw + 1 || pr.left < -1) && !kept(p) && p !== document.body && !p.classList.contains("view")) continue;
    out.push(`${e.tagName.toLowerCase()}${e.id ? "#" + e.id : ""}${typeof e.className === "string" && e.className ? "." + e.className.trim().split(/\s+/).join(".") : ""} [${Math.round(r.left)},${Math.round(r.right)}]`);
  }
  // a band held to the view's foot as the view scrolls with nothing on it
  // (a bar of buttons held there, as Providers' and Plugins' are, is meant)
  const foot = open ? [...open.querySelectorAll("*")].filter((e) => {
    const s = getComputedStyle(e);
    return s.position === "sticky" && s.bottom !== "auto" && e.getClientRects().length && e.getBoundingClientRect().height > 0 && !e.innerText.trim();
  }).map((e) => e.id || e.className) : [];
  return { doc: [document.documentElement.scrollWidth, vw], view: open ? [open.id, open.scrollWidth, open.clientWidth] : null, out, foot };
};

const stops = [
  ["agents"], ["sessions"], ["providers"], ["routing"], ["groups"], ["gateway"], ["library"], ["plugins"], ["masking"],
  ["usage", 0], ["usage", 1], ["usage", 2], ["usage", 3],
  ...["general", "usage", "network", "models", "privacy", "otel", "sync", "plugins", "about"].map((s) => ["settings", s]),
];

const runs = [...["en", "zh", "zh-TW", "ja", "de"].map((lang) => [lang, 375]), ["en", 390], ["en", 430]];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every view on a phone fits its width, with nothing held at its foot", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const phone = { ...devices["iPhone 13"] };
    delete phone.defaultBrowserType;
    for (const [lang, width] of runs) {
      await t.test(lang + " " + width, async () => {
        const context = await browser.newContext({ ...phone, viewport: { width, height: 760 }, reducedMotion: "reduce", colorScheme: "dark" });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator("#view-agents").waitFor();
        const bad = [];
        for (const [view, sub] of stops) {
          await page.evaluate(([view, sub]) => {
            document.querySelector(view === "settings" ? "#prefs" : `#nav [data-view="${view}"]`)?.click();
            if (view === "usage") document.querySelectorAll("#usageTab .opt")[sub]?.click();
            if (view === "settings") document.querySelector("#setTab-" + sub)?.click();
          }, [view, sub]);
          if (view === "usage" && sub === 1) await page.locator("#ledWrap .led tbody tr").first().waitFor();
          await page.waitForTimeout(view === "usage" ? 900 : 400);
          const w = await page.evaluate(WIDE);
          const at = view + (sub === undefined ? "" : " " + sub);
          if (w.doc[0] > w.doc[1] || (w.view && w.view[1] > w.view[2]) || w.out.length || w.foot.length) bad.push(at + " " + JSON.stringify(w));
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${view}-${sub ?? ""}.png`) });
        }
        // the Requests table, wider than the phone, scrolls in its own box
        await page.locator("#nav [data-view=usage]").click();
        await page.locator("#usageTab .opt").nth(1).click();
        assert(await page.locator("#ledWrap").evaluate((e) => e.scrollWidth > e.clientWidth + 100), "the table is wider than the phone");
        assert.deepEqual(bad, [], "views wider than the phone, or a band held at its foot");
        assert.equal(await page.locator("#ledHScroll").isHidden(), true, "no stand-in scrollbar at a touch screen's foot");
        assert.deepEqual(errors, []);
      });
    }
  });
}
