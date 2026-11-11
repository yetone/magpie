// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's request row says how Claude Code ran a Claude
// subscription's request, in its interactive mode ("TUI") or as claude -p,
// which the plan counts differently (Anotiaww on X): a chip beside the
// effort, its title saying what it is and, for -p, why. A request that ran
// no Claude Code (Try.entry empty) has none. Chromium and WebKit, 1000 and
// 480 wide, in every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 9e3).toISOString();
const seat = { id: "claude", provider: "claude", name: "Claude", who: "ann@example.com", kind: "account", agent: "claude", model: "claude-opus-4-5" };
// newest first: interactive, -p turned off, -p for a refused prompt, no Claude Code
const RUNS = [{ entry: "tui" }, { entry: "print", entryWhy: "off" }, { entry: "print", entryWhy: "third-party" }, {}];
const routes = RUNS.map((run, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "claude", model: "claude/claude-opus-4-5-20251101", provider: "claude",
  order: [seat], done: true, status: 200, ms: 5000, ttft: 900, tokens: 1000,
  tries: [{ id: seat.id, model: "claude-opus-4-5", start: at(i), done: true, status: 200, ms: 5000, ...run }],
}));

const W = {
  en: { tui: "Claude Code ran this request in its interactive mode (TUI)", p: "Claude Code ran this request as claude -p", off: "the interactive mode is turned off", third: "the interactive mode was turned away over this agent's system prompt" },
  zh: { tui: "Claude Code 以交互模式（TUI）运行了这个请求", p: "Claude Code 以 claude -p 运行了这个请求", off: "交互模式已关闭", third: "Anthropic 在交互模式下拒绝了这个 Agent 的系统提示词" },
  "zh-TW": { tui: "Claude Code 以互動模式（TUI）執行了這個請求", p: "Claude Code 以 claude -p 執行了這個請求", off: "互動模式已關閉", third: "Anthropic 在互動模式下拒絕了這個 Agent 的系統提示詞" },
  ja: { tui: "Claude Code はこのリクエストを対話モード（TUI）で実行しました", p: "Claude Code はこのリクエストを claude -p で実行しました", off: "対話モードがオフになっています", third: "Anthropic が対話モードでこのエージェントのシステムプロンプトを拒否しました" },
  de: { tui: "Claude Code hat diese Anfrage im interaktiven Modus (TUI) ausgeführt", p: "Claude Code hat diese Anfrage als claude -p ausgeführt", off: "der interaktive Modus ist ausgeschaltet", third: "Anthropic hat den Systemprompt dieses Agents im interaktiven Modus abgelehnt" },
};

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) for (const width of [1000, 480]) {
    test(`${engine} ${lang} ${width}px: a request row says how Claude Code ran it`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const chips = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => {
        const c = [...r.querySelectorAll(".ef")].find((e) => /^(TUI|-p)$/.test(e.textContent.trim()));
        const b = c?.getBoundingClientRect(), rb = r.getBoundingClientRect();
        return c ? { text: c.textContent.trim(), title: c.title, inside: b.width > 0 && b.left >= rb.left - 1 && b.right <= rb.right + 1 } : null;
      }));
      assert.deepEqual(chips.map((c) => c && [c.text, c.title]), [
        ["TUI", w.tui], ["-p", `${w.p}: ${w.off}`], ["-p", `${w.p}: ${w.third}`], null,
      ]);
      assert(chips.every((c) => !c || c.inside), "each chip sits inside its row");
      assert.deepEqual(errors, []);
    });
  }
}
