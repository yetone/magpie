// Run with Node's test runner and Playwright on the module path; see README.md.
// A request on the Routing page opens to its request and response (Fate
// on Discord: a Codex prediction there couldn't be opened to what was sent
// and what came back). Its story's head has "Request and response": the
// gateway's recent calls' copy by the route's id, read when asked; once
// they have let it go, the request archive's copy when it was on, and
// otherwise how to keep them. With the archive on, a request not in it
// says it isn't, not to turn the archive on (wangb on Discord). A masked request's head has its count of
// masked values. Another request picked closes it. Both
// engines, a narrow window, every language; nothing runs out of the story,
// no left-border accent. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const key = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-chat" };
const done = (id, i, extra) => ({ id, seq: id, time: at(i), agent: "codex", kind: "prediction", model: "deepseek/deepseek-chat", provider: "deepseek", order: [key],
  tries: [{ id: key.id, model: "deepseek-chat", start: at(i), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 300, served: "deepseek-chat", ...extra });
// 101 still in the recent calls, 100 only in the archive, 99 in neither
// and 101 had three values masked: its story's head says so
const routes = [done(101, 0, { masked: { n: 3, distinct: 2, kinds: { API_KEY: 2, EMAIL: 1 }, by: { rule: 2 }, key: 5005 } }), done(100, 1, { archive: day + "/abc100" }), done(99, 2)];
let seenCalls = [];

function serve(lang, archiveOn = false) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/gateway/call") {
      seenCalls.push(url.search);
      const id = url.searchParams.get("route");
      if (id === "101") return json({ requestBody: JSON.stringify({ model: "deepseek-chat", messages: [{ role: "user", content: "REQ-101 predict the next words" }] }), responseBody: JSON.stringify({ choices: [{ message: { role: "assistant", content: "RES-101 a prediction" } }] }) });
      return route.fulfill({ status: 404, json: { error: "This request is no longer in the recent calls." } });
    }
    if (url.pathname === "/api/archive") return json({ request: { method: "POST", path: "/v1/chat/completions", headers: { "content-type": ["application/json"] }, body: JSON.stringify({ messages: [{ role: "user", content: "REQ-100 from the archive" }] }), size: 60 }, response: { status: 200, headers: {}, body: JSON.stringify({ choices: [{ message: { content: "RES-100 archived" } }] }), size: 50 } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, ...(archiveOn ? { archive: { on: true, folder: "~/archive" } } : {}) } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: [], offset: 0, total: 0, calls: 0, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 2, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 2, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { open: "Request and response", hide: "Hide request and response", req: "Request Body", res: "Response Body", gone: "only the last 40 requests", fetch: "Fetch from archive", goneOn: "Request archive is on, but this request isn" },
  zh: { open: "请求与响应", hide: "收起请求与响应", req: "请求体", res: "响应体", gone: "最近 40 个请求", fetch: "从存档取回", goneOn: "「请求存档」已开启，但存档里没有这个请求" },
  "zh-TW": { open: "請求與回應", hide: "收起請求與回應", req: "請求體", res: "回應體", gone: "最近 40 個請求", fetch: "從存檔取回", goneOn: "「請求存檔」已開啟，但存檔裡沒有這個請求" },
  ja: { open: "リクエストとレスポンス", hide: "リクエストとレスポンスを閉じる", req: "リクエスト本文", res: "レスポンス本文", gone: "直近 40 件", fetch: "アーカイブから取得", goneOn: "「リクエストアーカイブ」はオンですが" },
  de: { open: "Anfrage und Antwort", hide: "Anfrage und Antwort ausblenden", req: "Anfrage-Body", res: "Antwort-Body", gone: "der letzten 40 Anfragen", fetch: "Aus Archiv abrufen", goneOn: "Das Anfragearchiv ist an, aber diese Anfrage" },
};

// nothing in the bodies' box, nor the head's buttons, runs past the
// story, and none of it has a left-border accent
const fits = (page) => page.locator(".rt-bodies").evaluate((b) => {
  const story = b.closest(".rt-log").getBoundingClientRect();
  const head = b.closest(".rt-log").querySelector(".rt-log-head");
  return {
    head: [...head.children].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.left < story.left - 0.5 || r.right > story.right + 0.5); }).map((e) => e.textContent),
    out: [...b.querySelectorAll("*")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.left < story.left - 0.5 || r.right > story.right + 0.5); }).map((e) => e.className),
    accent: [...b.querySelectorAll("*")].filter((e) => { const s = getComputedStyle(e); return s.borderLeftWidth !== "0px" && s.borderLeftWidth !== s.borderRightWidth; }).map((e) => e.className),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: a request on the Routing page opens to its request and response`, async (t) => {
      seenCalls = [];
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 440, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-bodies.png`), fullPage: true });
        }
        await browser.close();
      });
      // a click at the middle of what is shown: with the page scrolled down
      // to the story, Playwright's WebKit places what sits in a scroller
      // ~700px lower than it shows, and clicks the footer instead. The
      // reader wheels it into sight first (reach): the page puts back any
      // scroll that isn't the reader's, and Playwright's
      // scrollIntoViewIfNeeded is code, so the row it scrolled to was put
      // back under the footer or out of the window before the click (#2559).
      const tap = async (loc) => {
        await reach(page, loc);
        const [x, y] = await loc.evaluate((e) => { const r = e.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; });
        await page.mouse.click(x, y);
      };
      const pick = (i) => tap(page.locator(".rt-req").nth(i));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();

      // in the recent calls: both bodies, read only when asked
      await page.locator(".rt-req").nth(0).click();
      const b = page.locator(".rt-log-head .rt-bodies-b");
      await b.waitFor();
      assert.equal(await b.textContent(), w.open);
      assert.equal(await page.locator(".rt-log-head .mask-badge").textContent(), "3", "the masked count in the head");
      assert.match(await page.locator(".rt-log-head .mask-badge").getAttribute("aria-label"), /3/);
      assert.equal(await b.getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator(".rt-bodies").isVisible(), false);
      assert.deepEqual(seenCalls, [], "bodies read before they were asked for");
      await tap(b);
      await page.locator(".rt-bodies .call-body").nth(1).waitFor();
      assert.deepEqual(seenCalls, ["?route=101"]);
      assert.equal(await b.textContent(), w.hide);
      assert.equal(await b.getAttribute("aria-expanded"), "true");
      assert.deepEqual(await page.locator(".rt-bodies .call-body-label").allTextContents(), [w.req, w.res]);
      const text = await page.locator(".rt-bodies").innerText();
      assert.match(text, /REQ-101/);
      assert.match(text, /RES-101/);
      assert.deepEqual(await fits(page), { head: [], out: [], accent: [] });
      if (process.env.ARTIFACT_DIR) await page.locator(".rt-log").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-bodies-story.png`) });
      // closed again by the same button
      await tap(b);
      await page.locator(".rt-bodies").waitFor({ state: "hidden" });
      assert.equal(await page.locator(".rt-log-head .rt-bodies-b").textContent(), w.open);

      // another request picked: closed, and opened it reads its own
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      await page.locator(".rt-bodies .call-body").nth(1).waitFor();
      await pick(1);
      await page.locator(".rt-req").nth(1).and(page.locator('[aria-pressed="true"]')).waitFor();
      await page.locator(".rt-bodies").waitFor({ state: "hidden" });
      assert.equal(await page.locator(".rt-log-head .rt-bodies-b").textContent(), w.open);
      assert.equal(await page.locator(".rt-log-head .mask-badge").count(), 0, "nothing masked, no badge");

      // gone from the recent calls, kept by the archive: read back from it
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      const fetch = page.locator(".rt-bodies .call-archive button.text").first();
      await fetch.waitFor();
      assert.equal(await fetch.textContent(), w.fetch);
      await tap(fetch);
      await page.locator(".rt-bodies .call-archive .call-body").nth(1).waitFor();
      const archived = await page.locator(".rt-bodies").innerText();
      assert.match(archived, /REQ-100/);
      assert.match(archived, /RES-100/);
      assert.deepEqual(await fits(page), { head: [], out: [], accent: [] });

      // in neither: says how to keep them
      await pick(2);
      await page.locator(".rt-req").nth(2).and(page.locator('[aria-pressed="true"]')).waitFor();
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      const gone = page.locator(".rt-bodies .rt-bodies-gone");
      await gone.waitFor();
      assert((await gone.textContent()).includes(w.gone), await gone.textContent());
      assert(!(await gone.textContent()).includes(w.goneOn), "the archive is off: says to turn it on");
      assert.deepEqual(await fits(page), { head: [], out: [], accent: [] });
      assert.deepEqual(seenCalls, ["?route=101", "?route=100", "?route=99"]);
      assert.deepEqual(errors, []);
    });
  }
}

// the archive on, a request in neither: the hint says the archive is on and
// this request isn't in it, not to turn it on (wangb on Discord)
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: with the archive on, a request not in it doesn't say to turn it on`, async (t) => {
      seenCalls = [];
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 440, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, true));
      t.after(() => browser.close());
      const tap = async (loc) => {
        await reach(page, loc);
        const [x, y] = await loc.evaluate((e) => { const r = e.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; });
        await page.mouse.click(x, y);
      };
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      await tap(page.locator(".rt-req").nth(2));
      await page.locator(".rt-req").nth(2).and(page.locator('[aria-pressed="true"]')).waitFor();
      await tap(page.locator(".rt-log-head .rt-bodies-b"));
      const gone = page.locator(".rt-bodies .rt-bodies-gone");
      await gone.waitFor();
      const text = await gone.textContent();
      assert(text.includes(w.gone), text);
      assert(text.includes(w.goneOn), text);
      assert.deepEqual(await fits(page), { head: [], out: [], accent: [] });
      assert.deepEqual(seenCalls, ["?route=99"]);
      assert.deepEqual(errors, []);
    });
  }
}
