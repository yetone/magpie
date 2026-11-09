// Run with Node's test runner and Playwright on the module path; see README.md.
// GitHub answers after the rest of the plugins page, seconds after on a
// slow line, and its plugins weren't shown until it did, so it read as
// there being no unofficial plugins (yetone: 当还没加载完的时候，压根就不显示，
// 会让那些网慢的用户以为没有非官方插件). While /api/plugins/github is held
// here, Discover's browse bar says it is looking on GitHub, and picking
// the GitHub origin shows cards still to come; let go, its cards come in
// and the word goes. A search while it waits says the same. When the ask
// fails, a line says so with Try again, which asks again. magpie
// answering with an error beside the list (GitHub unreachable from the
// reader's desktop: till on Discord) is said too: with no list, why and
// Try again, which has magpie ask GitHub at once (?again=1); with the
// cards it could give, how many more couldn't be read, and why. English,
// Chinese, Japanese and German, wide and 440px, in Chromium and WebKit;
// nothing scrolls on a click. MAGPIE_WAIT_ASSETS names an older assets
// directory, to see this fail on the code before it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_WAIT_ASSETS || path.resolve(__dirname, "../assets"));

const REPOS = [
  { spec: "opencode-claude-auth", repo: "griffinmartin/opencode-claude-auth", url: "https://github.com/griffinmartin/opencode-claude-auth", owner: "griffinmartin", description: "OpenCode plugin that uses your existing Claude Code credentials — no separate login needed. ", stars: 1301, license: "MIT", pushed: "2026-09-22T23:47:15Z", package: "opencode-claude-auth", version: "2.2.1" },
  { spec: "github:keli-wen/agy-staff", repo: "keli-wen/agy-staff", url: "https://github.com/keli-wen/agy-staff", owner: "keli-wen", description: "Hire Google's Antigravity CLI (agy) as a fast Gemini staffer for Claude Code and OpenAI Codex.", stars: 723, license: "MIT", pushed: "2026-10-06T16:29:15Z", package: "agy-staff", version: "0.7.4" },
];
const LISTINGS = [
  { package: "@magpie-community/opencode-zed-auth", name: "Zed", community: true, kind: "pv", npm: { version: "0.1.8", weekly: 90, license: "MIT" } },
];

// gh: how /api/plugins/github answers each time it is asked, in turn: a
// promise held until let go, "fail", or { json } as magpie's answer;
// urls: each ask's URL
function server(lang, gh, urls = []) {
  let asked = 0;
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: LISTINGS });
    if (url.pathname === "/api/plugins/github") {
      const next = gh[asked++] ?? gh.at(-1);
      urls.push(url.pathname + url.search);
      if (next?.json) return json(next.json);
      if (next === "fail") return route.fulfill({ status: 502, contentType: "text/plain", body: "GitHub didn't answer" });
      await next;
      return json({ repos: REPOS, topic: "magpie-plugin" }).catch(() => {});
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { section: "Unofficial, on GitHub", look: "Looking on GitHub", failed: /Couldn't ask GitHub/, again: "Try again", none: /GitHub found 12 repositories, but none of them could be read/, more: /12 more on GitHub couldn't be read/, slow: /7 more on GitHub couldn't be read: GitHub didn't answer in time/ },
  zh: { section: "非官方插件 · GitHub", look: "正在 GitHub 上查找", failed: /无法连接 GitHub/, again: "重试", none: /GitHub 找到了 12 个仓库，但一个都没能读取/, more: /GitHub 上还有 12 个插件没能读取/, slow: /GitHub 上还有 7 个插件没能读取：GitHub 响应超时/ },
  ja: { section: "非公式 · GitHub", look: "GitHub を検索中", failed: /GitHub に接続できませんでした/, again: "再試行", none: /GitHub で 12 件のリポジトリが見つかりましたが/, more: /GitHub 上のほか 12 件を読み込めませんでした/, slow: /GitHub の応答がタイムアウトしました/ },
  de: { section: "Inoffiziell, auf GitHub", look: "Suche auf GitHub", failed: /GitHub nicht erreichbar/, again: "Erneut versuchen", none: /GitHub hat 12 Repositories gefunden/, more: /12 weitere auf GitHub konnten nicht gelesen werden/, slow: /GitHub hat nicht rechtzeitig geantwortet/ },
};

const shot = async (loc, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await loc.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};
const held = () => { let go; const p = new Promise((r) => { go = r; }); p.go = go; return p; };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the GitHub section while GitHub hasn't answered", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [980, 440]) {
        await t.test(lang + " " + width, async () => {
          const w = L[lang];
          const open = async (gh, urls) => {
            const ctx = await browser.newContext({ viewport: { width, height: 860 } });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, gh, urls));
            await page.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "discover"); } catch {} });
            await page.goto("http://magpie.test/?view=plugins");
            const view = page.locator("#view-plugins");
            await view.waitFor({ state: "visible" });
            await view.locator(".pm-card", { hasText: "Zed" }).waitFor();
            return { ctx, page, view, errors };
          };
          // the unofficial cards: each says it is unreviewed
          const ghCards = (view) => view.locator(".pm-card:not(.ghost)", { has: view.page().locator(".pm-by .pm-chip.warn") });
          const scrolls = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => (e.id || e.className) + ":" + e.scrollTop)].join());

          // held: the bar says it is looking
          const wait = held();
          const { ctx, page, view, errors } = await open([wait]);
          t.after(() => ctx.close());
          const look = view.locator(".pm-bar .pm-ghlook");
          await look.waitFor();
          assert.equal((await look.innerText()).trim(), w.look);
          assert.equal(await look.getAttribute("role"), "status");
          // the GitHub origin picked: cards still to come
          await view.locator(".pm-bar .pm-pop").first().click();
          await page.locator(".proto-menu .pm-item", { hasText: w.section }).click();
          await view.locator(".pm-card.ghost").first().waitFor();
          assert.equal(await view.locator(".pm-card.ghost").count(), 3);
          // the light moves: a ghost's bar is animated, and each card a beat after the last
          const anim = await view.locator(".pm-card.ghost .sk").evaluateAll((es) => es.map((e) => [getComputedStyle(e).animationName, getComputedStyle(e).animationDelay]));
          assert.ok(anim.every(([n]) => n === "pm-sheen"), "sheen: " + JSON.stringify(anim));
          assert.notEqual(anim[0][1], anim.at(-1)[1], "a beat apart");
          // a ghost is laid out as a card: nothing spills at this width
          for (const g of await view.locator(".pm-card.ghost").all()) {
            assert.ok(await g.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && e.getBoundingClientRect().height > 70), "ghost fits");
          }
          await shot(view, `wait-${engine}-${lang}-${width}`);

          // a search while it waits: still looking
          const q = view.locator(".pm-head .pm-find input");
          await q.fill("claude");
          await view.locator(".pm-ghlook").waitFor();
          await q.fill("");
          await look.waitFor();

          // let go: its cards in the ghosts' place, the word gone
          wait.go();
          await ghCards(view).first().waitFor();
          assert.equal(await view.locator(".pm-card.ghost").count(), 0);
          assert.equal(await view.locator(".pm-ghlook").count(), 0);
          assert.deepEqual((await ghCards(view).locator(".pm-name b").allInnerTexts()).sort(), ["agy-staff", "opencode-claude-auth"]);
          await page.waitForFunction(() => !document.querySelector(".pm-arrive"));
          await shot(view, `came-${engine}-${lang}-${width}`);
          assert.deepEqual(errors, []);

          // failed: said so, and Try again asks again
          const again = held();
          const f = await open(["fail", again]);
          t.after(() => f.ctx.close());
          const fLine = f.view.locator(".pm-ghwarn");
          await fLine.waitFor();
          assert.match(await fLine.innerText(), w.failed);
          assert.equal(await ghCards(f.view).count(), 0);
          await shot(f.view, `failed-${engine}-${lang}-${width}`);
          const before = await scrolls(f.page);
          await fLine.getByRole("button", { name: w.again }).click();
          await f.view.locator(".pm-bar .pm-ghlook").waitFor();
          assert.equal(await scrolls(f.page), before, "nothing scrolls on Try again");
          again.go();
          await ghCards(f.view).first().waitFor();
          assert.equal(await f.view.locator(".pm-ghwarn").count(), 0);
          assert.deepEqual(f.errors, []);

          // magpie answers, but GitHub couldn't give it the list: said
          // with why
          const why = 'http://raw.githubusercontent.com/a/b/main/package.json: Get "https://raw.githubusercontent.com/a/b/main/package.json": dial tcp: i/o timeout';
          const urls = [];
          const later = held();
          const e = await open([{ json: { repos: [], topic: "magpie-plugin", error: why, missing: 12 } }, later], urls);
          t.after(() => e.ctx.close());
          const eLine = e.view.locator(".pm-ghwarn");
          await eLine.waitFor();
          const said = await eLine.innerText();
          assert.match(said, w.none);
          assert.ok(said.includes("i/o timeout"), said);
          // its reason wraps: nothing spills at this width
          assert.ok(await eLine.evaluate((s) => s.scrollWidth <= s.clientWidth + 1), "the reason fits");
          assert.ok(await e.view.evaluate((s) => s.scrollWidth <= s.clientWidth + 1), "the page fits");
          await shot(e.view, `unread-${engine}-${lang}-${width}`);
          // the search field's results say it too
          const eq = e.view.locator(".pm-head .pm-find input");
          await eq.fill("claude");
          await eLine.waitFor();
          await eq.fill("");
          await eLine.waitFor();
          // Try again has magpie ask GitHub at once
          await eLine.getByRole("button", { name: w.again }).click();
          await e.view.locator(".pm-ghlook").waitFor();
          later.go();
          await ghCards(e.view).first().waitFor();
          assert.deepEqual(urls, ["/api/plugins/github", "/api/plugins/github?again=1"]);
          assert.equal(await e.view.locator(".pm-ghwarn").count(), 0);
          assert.deepEqual(e.errors, []);

          // some came, some couldn't be read: the cards, and a line over
          // them saying so with Try again, which keeps the cards meanwhile
          const urls2 = [];
          const later2 = held();
          const m = await open([{ json: { repos: REPOS, topic: "magpie-plugin", error: why, missing: 12 } }, later2], urls2);
          t.after(() => m.ctx.close());
          const mLine = m.view.locator(".pm-ghwarn");
          await mLine.waitFor();
          assert.match(await mLine.innerText(), w.more);
          assert.equal(await ghCards(m.view).count(), 2);
          assert.ok(await mLine.evaluate((s) => s.scrollWidth <= s.clientWidth + 1), "the line fits");
          await shot(m.view, `partial-${engine}-${lang}-${width}`);
          await mLine.getByRole("button", { name: w.again }).click();
          await m.view.locator(".pm-bar .pm-ghlook").waitFor();
          assert.equal(await ghCards(m.view).count(), 2, "cards kept while asking");
          later2.go();
          await m.view.locator(".pm-ghwarn").waitFor({ state: "detached" });
          await m.view.locator(".pm-ghlook").waitFor({ state: "detached" });
          assert.equal(await ghCards(m.view).count(), 2);
          assert.deepEqual(urls2, ["/api/plugins/github", "/api/plugins/github?again=1"]);
          assert.deepEqual(m.errors, []);

          // GitHub too slow, as magpie tells it (why: "slow"): the line
          // says so in the reader's language, not Go's "no answer in 8s",
          // and the error as it came is its hover (the Kiko03O1 reply on
          // X: ui 设计能不能好看点); Try again reads as a button, in the accent
          const s = await open([{ json: { repos: REPOS, topic: "magpie-plugin", error: "no answer in 8s", why: "slow", missing: 7 } }]);
          t.after(() => s.ctx.close());
          const sLine = s.view.locator(".pm-ghwarn");
          await sLine.waitFor();
          assert.match(await sLine.innerText(), w.slow);
          assert.doesNotMatch(await sLine.innerText(), /no answer/);
          assert.equal(await sLine.locator("span").first().getAttribute("title"), "no answer in 8s");
          const tone = (v) => s.page.evaluate((v) => { const d = document.createElement("div"); d.style.color = `var(${v})`; document.body.append(d); const c = getComputedStyle(d).color; d.remove(); return c; }, v);
          assert.equal(await sLine.getByRole("button", { name: w.again }).evaluate((b) => getComputedStyle(b).color), await tone("--accent"));
          assert.deepEqual(s.errors, []);

          // a failed ask after a list was had: the list stands, and the
          // line says it is from before
          const k = await open([{ json: { repos: REPOS, topic: "magpie-plugin" } }, { json: { repos: [], topic: "magpie-plugin", error: "GitHub's search: 403 rate limit" } }]);
          t.after(() => k.ctx.close());
          await ghCards(k.view).first().waitFor();
          await k.page.evaluate(() => window.loadPlugins());
          await k.view.locator(".pm-ghwarn").waitFor();
          assert.match(await k.view.locator(".pm-ghwarn").innerText(), /403 rate limit/);
          assert.equal(await ghCards(k.view).count(), 2);
          assert.deepEqual(k.errors, []);
        });
      }
    }
  });
}
