// Run with Node's test runner and Playwright on the module path; see README.md.
// A call's HTTP headers both ways (usrname1833 on Discord: 查看原始请求和原始
// 响应的http头, to debug what a capture would show): folded under its bodies
// on the Gateway page's recent calls and on the Routing page, the client's
// headers, then each request magpie sent upstream and the head of its
// answer, an error in place of one that had none, a plugin's hand-off said
// as one, and the tries not kept counted. The request archive's copy shows
// what went upstream too. Opening the bodies doesn't open them; the bodies'
// own panels stay two. Both engines, a narrow window, every language;
// nothing runs out of the call, no left-border accent. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const headers = {
  request: { "User-Agent": ["claude-cli/2.1.0"], Authorization: ["[REDACTED]"], "Content-Type": ["application/json"] },
  upstream: [
    { method: "POST", url: "https://api.example.com/v1/chat/completions", request: { Authorization: ["[REDACTED]"], "Content-Type": ["application/json"] }, ms: 812, status: 429, proto: "HTTP/2.0", response: { "Retry-After": ["3"], "X-Request-Id": ["req-UP-1"] } },
    { method: "POST", url: "https://plugin.invalid/v1/messages", plugin: "cursor", request: { "X-Api-Key": ["[REDACTED]"] }, ms: 1200, status: 200, response: { "Content-Type": ["text/event-stream"] } },
    { method: "POST", url: "https://down.example.com/v1/chat/completions", request: {}, ms: 30, error: "dial tcp: connection refused" },
  ],
  more: 2,
};
const bodies = { requestBody: '{"messages":[{"role":"user","content":"hello"}]}', responseBody: '{"reply":"world"}', headers };
const call = { id: 7, time: now.toISOString(), agent: "codex", model: "fixture/m", from: "chat", to: "chat", status: 200, ms: 900 };
const key = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-chat" };
const route = (id, i, extra) => ({ id, seq: id, time: new Date(now.getTime() - (i + 1) * 60e3).toISOString(), agent: "codex", kind: "chat", model: "deepseek/deepseek-chat", provider: "deepseek", order: [key],
  tries: [{ id: key.id, model: "deepseek-chat", start: now.toISOString(), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 300, served: "deepseek-chat", ...extra });
const routes = [route(101, 0), route(100, 1, { archive: day + "/abc100" })];

const want = {
  en: { toggle: "HTTP headers", client: "From the client", sent: "Sent upstream", plugin: "Handed to plugin cursor", answer: "Upstream response" },
  zh: { toggle: "HTTP 头", client: "客户端发来的", sent: "发往上游", plugin: "交给插件 cursor", answer: "上游响应" },
  "zh-TW": { toggle: "HTTP 標頭", client: "用戶端送來的", sent: "送往上游", plugin: "交給外掛 cursor", answer: "上游回應" },
  ja: { toggle: "HTTP ヘッダー", client: "クライアントから", sent: "上流へ送信", plugin: "プラグイン cursor に渡した", answer: "上流の応答" },
  de: { toggle: "HTTP-Header", client: "Vom Client", sent: "An den Anbieter gesendet", plugin: "An Plugin cursor übergeben", answer: "Antwort des Anbieters" },
};

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ presets: [], excluded: [], providers: [{ id: "fixture", name: "Fixture", icon: "generic", key: { set: true, masked: "fixture" }, models: [{ id: "m", name: "M", on: true }], agents: [] }], gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls: [call], groups: [] } });
    if (url.pathname === "/api/gateway/call") {
      if (url.searchParams.get("id") === "7" || url.searchParams.get("route") === "101") return json(bodies);
      return json({ error: "This request is no longer in the recent calls." }, 404);
    }
    if (url.pathname === "/api/archive") return json({ request: { method: "POST", path: "/v1/chat/completions", headers: { "Content-Type": ["application/json"] }, body: "{}", size: 2 }, response: { status: 200, headers: {}, body: "{}", size: 2 },
      upstream: [{ method: "POST", url: "https://archived.example.com/v1/chat/completions", request: { Authorization: ["[REDACTED]"] }, ms: 400, status: 200, proto: "HTTP/1.1", response: { "X-Request-Id": ["req-ARCH-9"] } }] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((res) => setTimeout(res, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: routes.length }], routes: url.searchParams.get("day") ? routes : [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    return r.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

// nothing under box runs past it or the window, and nothing has a
// left-border accent
const fits = (box) => box.evaluate((b) => {
  const r0 = b.getBoundingClientRect();
  const right = Math.min(r0.right, document.documentElement.clientWidth);
  return {
    out: [...b.querySelectorAll("*")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.left < r0.left - 0.5 || r.right > right + 0.5); }).map((e) => e.className),
    accent: [...b.querySelectorAll("*")].filter((e) => { const s = getComputedStyle(e); return s.borderLeftWidth !== "0px" && s.borderLeftWidth !== s.borderRightWidth; }).map((e) => e.className),
    scroll: document.scrollingElement.scrollWidth > document.documentElement.clientWidth + 0.5,
  };
});

async function open(engine, lang, t, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang));
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-call-headers.png`), fullPage: true });
    }
    await browser.close();
  });
  return { page, errors };
}

// the panels the headers show, by label, and their text
async function shown(box) {
  return { labels: await box.locator(".call-wire-grid .call-body-label").allTextContents(), text: await box.locator(".call-wire-grid").innerText() };
}

function checkPanels(w, { labels, text }) {
  assert.deepEqual(labels, [w.client, w.sent, w.answer, w.plugin, w.answer, w.sent, w.answer]);
  for (const s of ["User-Agent: claude-cli/2.1.0", "POST https://api.example.com/v1/chat/completions", "Authorization: [REDACTED]", "HTTP/2.0 429", "Retry-After: 3",
    "X-Request-Id: req-UP-1", "POST https://plugin.invalid/v1/messages", "X-Api-Key: [REDACTED]", "Content-Type: text/event-stream", "dial tcp: connection refused"]) assert(text.includes(s), `${s} in\n${text}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    const w = want[lang];
    test(`${engine} ${lang}: a recent call's HTTP headers fold open under its bodies`, async (t) => {
      const { page, errors } = await open(engine, lang, t, lang === "en" ? 1100 : 380);
      await page.goto("http://magpie.test/?view=gateway");
      const item = page.locator("#activity .call-item").first();
      await item.waitFor();
      await item.locator(".call").click();
      await item.locator(".call-body pre").first().waitFor();
      assert.equal(await item.locator(".call-body").count(), 2, "bodies open, headers folded");
      const toggle = item.locator(".call-wire-toggle");
      assert.equal(await toggle.innerText(), w.toggle);
      assert.equal(await toggle.getAttribute("aria-expanded"), "false");
      await toggle.click();
      await item.locator(".call-wire-grid").waitFor();
      assert.equal(await toggle.getAttribute("aria-expanded"), "true");
      checkPanels(w, await shown(item));
      assert.equal(await item.locator(".call-wire-note").count(), 2, "the tries not kept, and the plugin's own headers");
      if (process.env.ARTIFACT_DIR) await item.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-call-headers-open.png`) });
      assert.deepEqual(await fits(item), { out: [], accent: [], scroll: false });
      // a refresh of the list keeps it open
      await page.evaluate(() => loadProviders());
      await item.locator(".call-wire-grid").waitFor();
      await item.locator(".call-wire-toggle").click();
      await item.locator(".call-wire-grid").waitFor({ state: "detached" });
      assert.equal(await item.locator(".call-body").count(), 2);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Routing request's HTTP headers, and the archive's`, async (t) => {
      const { page, errors } = await open(engine, lang, t, 440);
      const tap = async (loc) => {
        await reach(page, loc);
        const [x, y] = await loc.evaluate((e) => { const r = e.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; });
        await page.mouse.click(x, y);
      };
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      await page.locator(".rt-req").nth(0).click();
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      const box = page.locator(".rt-bodies");
      await box.locator(".call-body").nth(1).waitFor();
      assert.equal(await box.locator(".call-body").count(), 2);
      const toggle = box.locator(".call-wire-toggle");
      assert.equal(await toggle.getAttribute("aria-expanded"), "false");
      await tap(toggle);
      await box.locator(".call-wire-grid").waitFor();
      checkPanels(w, await shown(box));
      assert.deepEqual(await fits(box), { out: [], accent: [], scroll: false });
      if (process.env.ARTIFACT_DIR) await box.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-headers-open.png`) });
      await tap(box.locator(".call-wire-toggle"));
      await box.locator(".call-wire-grid").waitFor({ state: "detached" });

      // the archive's copy: what went upstream, under its bodies
      await tap(page.locator(".rt-req").nth(1));
      await page.locator(".rt-req").nth(1).and(page.locator('[aria-pressed="true"]')).waitFor();
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      const fetch = box.locator(".call-archive button.text").first();
      await fetch.waitFor();
      await tap(fetch);
      await box.locator(".call-archive .call-wire-grid").waitFor();
      const a = await shown(box);
      assert.deepEqual(a.labels, [w.sent, w.answer]);
      assert(a.text.includes("POST https://archived.example.com/v1/chat/completions") && a.text.includes("X-Request-Id: req-ARCH-9") && a.text.includes("HTTP/1.1 200"), a.text);
      assert.deepEqual(await fits(box), { out: [], accent: [], scroll: false });
      assert.deepEqual(errors, []);
    });
  }
}
