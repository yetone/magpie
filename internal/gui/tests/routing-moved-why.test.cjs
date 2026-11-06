// Run with Node's test runner and Playwright on the module path; see README.md.
// Neo on Discord read a Requests row, "chatflowsai/claude-opus-5-5 →
// AgentRouter · gpt-6.1-sol", as Claude Desktop asking for another
// provider's model, with nothing saying why. A route magpie put on another
// model than the agent named (Route.Moved) is tagged in the list by request
// and by session, in the replay and the tray panel, and says why under the
// pointer, in the row's title and once in the story. A kind's tag wins, and
// its story says the move too, but for Desktop's connection check, whose
// own story says where it went; a route served as asked has no tag. All
// APIs are faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (min) => new Date(now - min * 60e3).toISOString();
const seat = (provider, model) => ({ id: provider, provider, name: provider, who: "Test key", kind: "key", model });
const req = (id, agent, session, model, provider, sent, extra = {}) => ({ id, seq: id, time: at(9 - id), agent, session, model, provider,
  order: [seat(provider, sent)], tries: [{ id: provider, model: sent, start: at(9 - id), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 1200, ...extra });
const opus = "chatflowsai/claude-opus-5-5", gpt = "agentrouter/gpt-6.1-sol";
// Newest first, as the list shows them. The oldest replays first.
const routes = [
  req(6, "codex", "cx", "gpt-6.1-terra", "fake", "m2", { kind: "memory_consolidation", moved: { to: "fake/m2", why: "codex-follow" } }),
  req(5, "claude-desktop", "desk", gpt, "agentrouter", "gpt-6.1-sol"),
  req(4, "claude-desktop", "", opus, "agentrouter", "gpt-6.1-sol", { kind: "connection_check", moved: { to: gpt, why: "desktop-check" } }),
  req(3, "claude", "term", "fake/m1", "fake", "m2", { moved: { to: "fake/m2", why: "privacy" } }),
  req(2, "codex", "cx", "fake/m1", "fake", "m2", { kind: "review", moved: { to: "fake/m2", why: "codex-turn" } }),
  req(1, "claude-desktop", "", opus, "agentrouter", "gpt-6.1-sol", { moved: { to: gpt, why: "desktop-title" } }),
];

function serve(lang) {
  const state = { agents: [{ id: "claude-desktop", name: "Claude Desktop", fields: [] }, { id: "claude", name: "Claude Code", fields: [] }, { id: "codex", name: "Codex", fields: [] }],
    profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 6, totals: { requests: 6, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") return json({ titles: {} });
    if (url.pathname === "/api/usage" && url.searchParams.get("period") === "today") return json({ calls: 5, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, series: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// Expected reader-facing words, independent of the translation lookup.
const W = {
  en: { memory: "Memory", follow: "Codex asked for gpt-6.1-terra, one of its own default models, for its memories, and its config names no model for them; magpie served it on fake/m2, the model of its conversation.",
    title: "Title", check: "Connection check", stood: "Stand-in", review: "Review",
    titleWhy: "Claude Desktop picked chatflowsai/claude-opus-5-5 by itself for a short request without tools, such as a chat's title; magpie served it on agentrouter/gpt-6.1-sol, the model of its latest chat turn, so it stays with the provider the chat is on.",
    privacy: "Masking would have changed this request, so magpie sent it as written to fake/m2, the model set for that in Settings › Privacy.",
    codex: "Codex asked for fake/m1 for a call of its own, which magpie doesn't serve; magpie served it on fake/m2, the model its last turn was answered on." },
  zh: { memory: "记忆", follow: "Codex 为它的记忆请求了它自己的默认模型 gpt-6.1-terra，而它的配置没有为记忆指定模型；magpie 让 fake/m2 来处理，即它这段对话所用的模型。",
    title: "标题", check: "连接检查", stood: "替代", review: "审查",
    titleWhy: "Claude Desktop 自行选了 chatflowsai/claude-opus-5-5 来发一个不带工具的短请求（比如对话标题）；magpie 让 agentrouter/gpt-6.1-sol 来处理，即它最近一轮对话所用的模型，这样它留在对话所在的服务商。",
    privacy: "脱敏会改动这个请求，所以 magpie 把它原样发给了 fake/m2，即在 设置 › 隐私 里为此设定的模型。",
    codex: "Codex 为它自己的调用请求了 fake/m1，但 magpie 不提供它；magpie 让 fake/m2 来处理，即它上一轮所用的模型。" },
  "zh-TW": { memory: "記憶", follow: "Codex 為它的記憶請求了它自己的預設模型 gpt-6.1-terra，而它的設定沒有為記憶指定模型；magpie 讓 fake/m2 來處理，即它這段對話所用的模型。",
    title: "標題", check: "連線檢查", stood: "替代", review: "審查",
    titleWhy: "Claude Desktop 自行選了 chatflowsai/claude-opus-5-5 來發一個不帶工具的短請求（例如對話標題）；magpie 讓 agentrouter/gpt-6.1-sol 來處理，即它最近一輪對話所用的模型，這樣它留在對話所在的服務商。",
    privacy: "去識別化會改動這個請求，所以 magpie 把它原樣送給了 fake/m2，即在 設定 › 隱私 裡為此設定的模型。",
    codex: "Codex 為它自己的呼叫請求了 fake/m1，但 magpie 不提供它；magpie 讓 fake/m2 來處理，即它上一輪所用的模型。" },
  ja: { memory: "メモリ", follow: "Codex はメモリのために自身のデフォルトモデル gpt-6.1-terra をリクエストしましたが、設定でメモリ用のモデルが指定されていません。magpie は fake/m2（会話のモデル）で応答しました。",
    title: "タイトル", check: "接続チェック", stood: "代替", review: "レビュー",
    titleWhy: "Claude Desktop はツールなしの短いリクエスト（チャットのタイトルなど）に chatflowsai/claude-opus-5-5 を自分で選びました。magpie は agentrouter/gpt-6.1-sol（直近のチャットのターンのモデル）で応答し、チャットと同じプロバイダーに留めました。",
    privacy: "マスキングでこのリクエストが変わるため、magpie はそのまま fake/m2（設定 › プライバシーで指定されたモデル）に送りました。",
    codex: "Codex は自身の呼び出しに magpie が提供していない fake/m1 をリクエストしました。magpie は fake/m2（直前のターンに応答したモデル）で応答しました。" },
  de: { memory: "Memory", follow: "Codex hat für seine Erinnerungen gpt-6.1-terra angefordert, eines seiner eigenen Standardmodelle, und seine Konfiguration legt dafür kein Modell fest; magpie hat sie mit fake/m2 beantwortet, dem Modell seiner Unterhaltung.",
    title: "Titel", check: "Verbindungstest", stood: "Ersatz", review: "Review",
    titleWhy: "Claude Desktop hat chatflowsai/claude-opus-5-5 selbst für eine kurze Anfrage ohne Werkzeuge gewählt, etwa den Titel eines Chats; magpie hat sie mit agentrouter/gpt-6.1-sol beantwortet, dem Modell seines letzten Chat-Zugs, damit sie beim Anbieter des Chats bleibt.",
    privacy: "Die Maskierung hätte diese Anfrage verändert, daher hat magpie sie unverändert an fake/m2 gesendet, das dafür unter Einstellungen › Datenschutz festgelegte Modell.",
    codex: "Codex hat für einen eigenen Aufruf fake/m1 angefordert, das magpie nicht anbietet; magpie hat ihn mit fake/m2 beantwortet, dem Modell, das seinen letzten Zug beantwortet hat." },
};

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a request magpie put on another model says why`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const errors = [], pages = [];
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-moved-why-${i}.png`) });
          }
          await browser.close();
        });
        const open = async (url, viewport, bySession = false) => {
          const page = await browser.newPage({ viewport, reducedMotion: "reduce" });
          pages.push(page);
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.addInitScript((on) => localStorage.setItem("magpie.routingBySession", on ? "1" : "0"), bySession);
          await page.goto(url);
          return page;
        };
        const tags = (page, sel) => page.locator(sel).evaluateAll((rows) => rows.map((r) => {
          const k = r.querySelectorAll(".kind");
          return k.length > 1 ? "two tags" : k[0] ? [k[0].textContent, k[0].title] : ["", ""];
        }));
        const want = [[w.memory], ["", ""], [w.check], [w.stood, w.privacy], [w.review], [w.title, w.titleWhy]];
        const same = (got, msg) => assert.deepEqual(got.map((x, i) => want[i].length === 1 ? [x[0]] : x), want, msg);
        const said = [w.follow, null, null, w.privacy, w.codex, w.titleWhy];

        // the list, by request: the tag beside the model, the reason under
        // the pointer and in the row's title, nothing pushed out of the row
        let page = await open("http://magpie.test/?view=routing", { width, height: 760 });
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        same(await tags(page, ".rt-req"), "by request");
        const titles = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.title));
        for (const [i, s] of said.entries()) {
          if (s) assert(titles[i].includes("\n" + s), `row ${i} title: ${titles[i]}`);
          else assert(!/magpie served|magpie sent it as written/.test(titles[i]), `row ${i} title: ${titles[i]}`);
        }
        const fits = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => r.scrollWidth <= r.clientWidth + 1));
        assert.deepEqual(fits, routes.map(() => true), "a row overflows");

        // the story: the reason said once, with the tag when no kind has one
        for (const [i, s] of said.entries()) {
          if (i) {
            await page.close();
            page = await open("http://magpie.test/?view=routing", { width, height: 760 });
            await page.locator(".rt-req").nth(routes.length - 1).waitFor();
          }
          await page.locator(".rt-req").nth(i).evaluate((e) => e.click());
          await page.waitForFunction((i) => document.querySelectorAll(".rt-req")[i]?.getAttribute("aria-pressed") === "true", i);
          const story = await page.locator(".rt-steps").textContent();
          const stood = await page.locator(".rt-steps li.stood .kind").allTextContents();
          if (!s) {
            // served as asked, or Desktop's check, whose own story says it
            assert.deepEqual(stood, [], `row ${i} has a stand-in line`);
            continue;
          }
          assert.equal(story.split(s).length - 1, 1, `row ${i}: ${s}\nonce in\n${story}`);
          assert.deepEqual(stood, i === 0 || i === 4 ? [] : [want[i][0]], `story tag of row ${i}`);
        }

        // the replay: the oldest, Desktop's title, goes first with its tag
        await page.close();
        page = await open("http://magpie.test/?view=routing", { width, height: 760 });
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        const replay = await page.evaluate(() => t("Replay them all"));
        await page.getByRole("button", { name: replay, exact: true }).evaluate((e) => e.click());
        const played = await page.waitForFunction(() => {
          const e = document.querySelector(".rp-what .rp-req");
          return e && e.querySelector(".mdl") && { tag: e.querySelector(".kind")?.textContent || "", title: e.querySelector(".kind")?.title || "" };
        }).then((h) => h.jsonValue());
        assert.deepEqual(played, { tag: w.title, title: w.titleWhy });
        await page.getByRole("button", { name: await page.evaluate(() => t("Stop replay")), exact: true }).evaluate((e) => e.click());
        await page.waitForFunction(() => document.querySelector(".rt-replay").hidden);

        // by session: the same tags and reasons
        await page.close();
        page = await open("http://magpie.test/?view=routing", { width, height: 760 }, true);
        await page.locator(".rt-req").nth(routes.length - 1).waitFor();
        const sorted = (xs) => xs.map((x) => x[0] === w.review || x[0] === w.check || x[0] === w.memory ? [x[0]] : x).map((x) => JSON.stringify(x)).sort();
        assert.deepEqual(sorted(await tags(page, ".rt-req")), sorted(want), "by session");

        // the tray panel's rows, from the live trace
        const panel = await open("http://magpie.test/?mode=panel", { width: Math.min(width, 440), height: 600 });
        await panel.locator('[data-ptab="routing"]').click();
        await panel.locator(".pr-req").nth(routes.length - 1).waitFor();
        const pr = await panel.locator(".pr-req").evaluateAll((rows) => rows.map((r) => [r.querySelector(".pr-a .kind")?.textContent || "", r.querySelector(".pr-a .kind")?.title || ""]));
        same(pr, "panel");
        const pt = await panel.locator(".pr-req").evaluateAll((rows) => rows.map((r) => r.title));
        assert(pt[5].includes(w.titleWhy), `panel title: ${pt[5]}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
