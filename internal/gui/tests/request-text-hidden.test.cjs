// Run with Node's test runner and Playwright on the module path; see README.md.
// Show request content (llxth1 on X): Settings › Privacy has a switch for
// what the last requests said, on by default. Off, it saves
// hideRequestText, and the gateway keeps none of it: the Routing page's
// Request and response, the Gateway page's Recent calls and the context
// window card's text view each say so, with a way back to the switch,
// and none of them reads the request archive for it. The Routing page
// lets go of the bodies it read before once the switch is saved. Both
// engines, a narrow window, every language; no left-border accent, and
// the page doesn't scroll sideways. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-chat" };
// 101 has an archived copy too: hidden, it isn't read
const routes = [{ id: 101, seq: 101, time: at(0), agent: "codex", model: "deepseek/deepseek-chat", provider: "deepseek", order: [key],
  tries: [{ id: key.id, model: "deepseek-chat", start: at(0), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 300, served: "deepseek-chat", archive: day + "/abc101" }];
const calls = [{ id: 30, time: at(0), agent: "codex", model: "deepseek/deepseek-chat", from: "chat", to: "chat", status: 200, ms: 900, route: 101 }];
const headers = { request: { "User-Agent": ["codex"] }, upstream: [{ status: 200, request: { "Content-Type": ["application/json"] }, response: { "Content-Type": ["application/json"] } }] };
const bodies = { requestBody: JSON.stringify({ messages: [{ role: "user", content: "REQ-101 the secret question" }] }), responseBody: JSON.stringify({ choices: [{ message: { content: "RES-101 the secret reply" } }] }), headers };

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    noStats: false, trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function serve(lang, seen) {
  let cur = settingsPayload(lang, {});
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    const hidden = !!cur.hideRequestText;
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: cur, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { const b = req.postDataJSON(); seen.saves.push(b); cur = { ...cur, ...b }; }
      return json(cur);
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: routes.length }], routes: url.searchParams.get("day") ? routes : [] });
    if (url.pathname === "/api/gateway/call") {
      seen.calls.push(url.search);
      return json(hidden ? { hidden: true, headers } : bodies);
    }
    if (url.pathname === "/api/context/text" || url.pathname === "/api/context/reply") {
      seen.texts.push(url.pathname);
      return hidden ? json({ archive: "hidden" }, 404) : json({ archive: "off" }, 404);
    }
    if (url.pathname === "/api/archive") { seen.archive.push(url.search); return json({}); }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls, groups: [], archive: { on: true, folder: "~/archive" } } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { name: "Show request content", sub: "kept in memory only", note: "Show request content is off", go: "Settings › Privacy", open: "Request and response" },
  zh: { name: "显示请求内容", sub: "仅保存在内存中", note: "“显示请求内容”已关闭", go: "设置 › 隐私", open: "请求与响应" },
  "zh-TW": { name: "顯示請求內容", sub: "僅保存在記憶體中", note: "「顯示請求內容」已關閉", go: "設定 › 隱私", open: "請求與回應" },
  ja: { name: "リクエスト内容を表示", sub: "メモリ内にのみ保持", note: "「リクエスト内容を表示」がオフ", go: "設定 › プライバシー", open: "リクエストとレスポンス" },
  de: { name: "Anfrageinhalte anzeigen", sub: "nur im Arbeitsspeicher", note: "„Anfrageinhalte anzeigen“ ist in Einstellungen › Datenschutz aus", go: "Einstellungen › Datenschutz", open: "Anfrage und Antwort" },
};

// no left-border accent in it, nor anything running out of the window
const look = (loc) => loc.evaluate((b) => ({
  accent: [b, ...b.querySelectorAll("*")].filter((e) => { const s = getComputedStyle(e); return s.borderLeftWidth !== "0px" && s.borderLeftWidth !== s.borderRightWidth; }).map((e) => e.className),
  out: [b, ...b.querySelectorAll("*")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.right > document.documentElement.clientWidth + 0.5; }).map((e) => e.className),
  sideways: document.scrollingElement.scrollWidth > document.documentElement.clientWidth,
}));
const clean = { accent: [], out: [], sideways: false };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: Show request content off hides what requests said`, async (t) => {
      const w = want[lang];
      const seen = { saves: [], calls: [], texts: [], archive: [] };
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 440, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, seen));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-request-text-hidden.png`), fullPage: true });
        }
        await browser.close();
      });
      const tap = async (loc) => {
        await reach(page, loc);
        const [x, y] = await loc.evaluate((e) => { const r = e.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; });
        await page.mouse.click(x, y);
      };

      // shown, as before: the Routing page's request opens to its bodies
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").first().waitFor();
      await page.locator(".rt-req").first().click();
      const b = page.locator(".rt-log-head .rt-bodies-b");
      await b.waitFor();
      assert.equal(await b.textContent(), w.open);
      await tap(b);
      await page.locator(".rt-bodies .call-body").nth(1).waitFor();
      assert.match(await page.locator(".rt-bodies").innerText(), /REQ-101/);

      // the switch, in Settings › Privacy, on by default
      await page.locator("#prefs").click();
      await openPart(page, "privacy");
      const row = page.locator("#hideRequestTextRow");
      await row.waitFor();
      assert.equal((await row.locator(".name").textContent()).trim(), w.name);
      assert((await row.locator(".sub").textContent()).includes(w.sub), await row.locator(".sub").textContent());
      const opts = row.locator(".opt");
      assert.equal(await opts.nth(1).evaluate((o) => o.classList.contains("on")), true, "on by default");
      assert.deepEqual(await look(row), clean);
      await tap(opts.nth(0));
      await page.waitForFunction(() => document.querySelector("#hideRequestTextRow .opt.on")?.textContent === t("Off"));
      assert.equal(seen.saves.at(-1)?.hideRequestText, true, JSON.stringify(seen.saves));

      // back on Routing: the bodies read before are gone, and it says why
      const calls = seen.calls.length;
      await page.evaluate(() => show("routing"));
      const note = page.locator(".rt-bodies .call-hidden");
      await note.waitFor();
      assert(seen.calls.length > calls, "the bodies read before were shown again without asking");
      assert((await note.innerText()).includes(w.note), await note.innerText());
      assert.equal(await page.locator(".rt-bodies .call-body").count(), 0);
      assert.doesNotMatch(await page.locator(".rt-bodies").innerText(), /REQ-101|RES-101/);
      assert.equal(await page.locator(".rt-bodies .call-wire").count(), 1, "the headers are still there");
      assert.deepEqual(await look(page.locator(".rt-bodies")), clean);

      // the context card's text view says so too
      await page.waitForFunction(() => typeof window.openCtxText === "function");
      await page.evaluate((r) => window.openCtxText(r, "tools"), { id: 101, time: routes[0].time, archive: routes[0].archive });
      const gone = page.locator("#modal .ctx-view .ctx-view-gone");
      await gone.waitFor();
      assert((await gone.innerText()).includes(w.note), await gone.innerText());
      assert.equal(await gone.locator("button").textContent(), w.go);
      // its button goes to the switch
      await gone.locator("button").click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await page.locator("#hideRequestTextRow").waitFor();
      assert(await page.locator("#view-settings").isVisible());

      // the Gateway page's Recent calls
      await page.evaluate(() => show("gateway"));
      const item = page.locator("#activity .call-item").first();
      await item.waitFor();
      await item.locator(".call").click();
      const hiddenCall = item.locator(".call-hidden");
      await hiddenCall.waitFor();
      assert((await hiddenCall.innerText()).includes(w.note));
      assert.equal(await item.locator(".call-body").count(), 0);
      assert.equal(await hiddenCall.locator("button").textContent(), w.go);
      assert.deepEqual(await look(item), clean);

      assert.deepEqual(seen.archive, [], "hidden, the archive was read");
      assert.deepEqual(errors, []);
    });
  }
}
