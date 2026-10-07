// Run with Node's test runner and Playwright on the module path; see README.md.
// The protocol each request was sent upstream in (蓝猫 on Discord: 「请求」
// 列表里每条请求显示它用的是 Anthropic / Responses / Chat 哪种协议): the
// Usage page's Requests list tags each row with it, "Chat → Anthropic"
// for one translated from the agent's own, Gemini for one on Gemini's
// streamGenerateContent (Gemini CLI's, Vertex AI's), and a request's
// details name it beside the upstream's own stop reason. A row read from
// a session file, which has no path, has no tag. No click moves the page,
// no left-border accent. English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const row = (i, ep, stop, extra) => ({
  t: at(i), agent: "opencode", agentName: "OpenCode", icon: "opencode", provider: "relay", providerName: "Relay",
  req: "relay/grok-4", model: "grok-4", served: "grok-4", in: 300, out: 40, ms: 2000, status: 200, rid: "r-" + i,
  ...(ep ? { ep } : {}), ...(stop ? { stop } : {}), ...extra,
});
const ROWS = [
  row(0, "/v1/chat/completions → /v1/messages", "end_turn"),
  row(1, "/v1/messages", "tool_use"),
  row(2, "/v1/responses", ""),
  row(3, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", "STOP",
    { provider: "google", providerName: "Google Gemini", req: "google/gemini-2.5-pro", model: "gemini-2.5-pro", served: "gemini-2.5-pro" }),
  row(4, "/v1/chat/completions → /publishers/google/models/gemini-3.8-flash:streamGenerateContent?alt=sse", "STOP",
    { provider: "google-vertex", providerName: "Google Vertex AI", req: "google-vertex/gemini-3.8-flash", model: "gemini-3.8-flash", served: "gemini-3.8-flash" }),
  row(5, "", "", { source: "log", status: 0, provider: "", providerName: "" }),
];

function serve(lang) {
  const state = { agents: [{ id: "opencode", name: "OpenCode", path: "/test/opencode.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1200, output: 160, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: ROWS.length, agents: [{ id: "opencode", name: "OpenCode", icon: "opencode" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 4, errors: 0, input: 1200, output: 160, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 4, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { proto: "Protocol", stop: "Upstream stop reason", translated: /The agent spoke Chat; sent upstream as Anthropic/ },
  zh: { proto: "协议", stop: "上游结束原因", translated: /agent 用 Chat 协议请求；以 Anthropic 协议发往上游/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: each request names its protocol`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1280, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-protocol.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#usageTab .opt").nth(1).click();
      const rows = page.locator(".led tbody tr.led-row");
      await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });

      // the list: every request the gateway logged says its protocol
      const tags = await rows.evaluateAll((rs) => rs.map((r) => r.querySelector(".src.proto")?.textContent || ""));
      assert.deepEqual(tags, ["Chat → Anthropic", "Anthropic", "Responses", "Gemini", "Chat → Gemini", ""]);
      const look = await page.locator(".led .src.proto").evaluateAll((es) => es.map((e) => {
        const cell = e.closest("td").getBoundingClientRect(), b = e.getBoundingClientRect(), cs = getComputedStyle(e);
        return { inCell: b.left >= cell.left - 0.5 && b.right <= cell.right + 0.5 && b.width > 0, full: e.clientWidth >= e.scrollWidth, border: cs.borderLeftWidth, top: cs.borderTopWidth, title: e.title };
      }));
      for (const l of look) {
        assert(l.inCell && l.full, JSON.stringify(l));
        assert.equal(l.border, l.top, "no left-border accent");
      }
      assert.match(look[0].title, want[lang].translated);

      // a request's details: the protocol, and what the upstream said of its end
      const top = () => page.locator("#view-usage").evaluate((v) => v.scrollTop);
      const dd = async (i, dt) => {
        await reader.inView(page, rows.nth(i));
        const was = await top();
        const scrolled = await page.evaluate(() => [scrollX, scrollY]);
        await rows.nth(i).click();
        await page.waitForTimeout(200);
        assert.equal(await top(), was, "a click moved the page");
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY]), scrolled);
        const box = page.locator(".led-detail").last();
        await box.waitFor();
        const v = await box.evaluate((b, dts) => dts.map((dt) => [...b.querySelectorAll("dt")].find((d) => d.textContent === dt)?.nextElementSibling?.textContent || ""), dt);
        await rows.nth(i).click(); // closed again
        return v;
      };
      assert.deepEqual(await dd(0, [want[lang].proto, want[lang].stop]), ["Chat → Anthropic", "end_turn"]);
      assert.deepEqual(await dd(1, [want[lang].proto, want[lang].stop]), ["Anthropic", "tool_use"]);
      assert.deepEqual(await dd(2, [want[lang].proto, want[lang].stop]), ["Responses", ""]);
      assert.deepEqual(await dd(3, [want[lang].proto, want[lang].stop]), ["Gemini", "STOP"]);
      assert.deepEqual(await dd(4, [want[lang].proto, want[lang].stop]), ["Chat → Gemini", "STOP"]);
      assert.deepEqual(await dd(5, [want[lang].proto, want[lang].stop]), ["", ""]);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-protocol-list.png`) });
      for (const l of ["zh", "ja", "de"]) {
        const missing = await page.evaluate((l) => ["Protocol", "Upstream stop reason",
          "The agent spoke {from}; sent upstream as {to}", "Sent upstream as {to}, as the agent spoke it"].filter((k) => !I18N[l][k]), l);
        assert.deepEqual(missing, [], `every string has its ${l}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
