// Run with Node's test runner and Playwright on the module path; see README.md.
// tjhhhh on Discord: with several sessions running through magpie at once,
// the Routing page's context window went from one session's context to the
// other's, and which was which couldn't be told. Each session sending now
// has a small block of its own under the card, named as its agent named it
// (not its id), with its own newest reading; the newest session comes
// first and the blocks keep their places while the sessions take turns.
// Two Codex conversations' requests interleave here; every frame's blocks
// are watched. A block clicked tells of that session's newest request. One
// session alone shows none. Every language, both engines, a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
const t0 = Date.now();
const seat = { id: "codex@me", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true, plan: "Pro" };
const SESS = {
  a: { id: "019a7c2e-aaaa-7000-8000-000000000001", title: "Fix the login redirect", folder: "/Users/me/work/web", base: 40000, window: 272000 },
  b: { id: "019a7c2e-bbbb-7000-8000-000000000002", title: "Write the release notes", folder: "/Users/me/work/docs", base: 150000, window: 272000 },
};
// a request of session s, its prompt growing by n
const prompt = (s, n) => ({
  window: SESS[s].window, tokens: SESS[s].base + n * 1000, counted: true, turns: n,
  parts: [
    { kind: "system", tokens: 9000, items: [{ name: "prompt", tokens: 9000 }] },
    { kind: "chat", tokens: SESS[s].base - 9000 + n * 1000, items: [{ name: "turn", tag: "turns", n, tokens: SESS[s].base - 9000 + n * 1000 }] },
  ],
});
let seq = 10;
function req(id, s, done = true, titled = true) {
  const at = t0 - (200 - id) * 1000;
  return {
    id, seq: ++seq, time: iso(at), agent: "codex", session: SESS[s].id, folder: SESS[s].folder, model: "gpt-6.1-sol", provider: "codex",
    ...(titled ? { sessionTitle: SESS[s].title } : {}),
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(at), done, ...(done ? { status: 200, ms: 3000 } : {}) }],
    done, ...(done ? { status: 200, ms: 3000, tokens: 900 } : {}), prompt: prompt(s, id - 100),
  };
}

function serve(lang, first, feed) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      const trace = (routes) => json({ mine: true, now: new Date().toISOString(), seq: routes.length ? routes[routes.length - 1].seq : Number(url.searchParams.get("after")), totals: { requests: 0, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return trace(first);
      const rs = await new Promise((res) => { feed.next = res; });
      return trace(rs);
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(browser, lang, width, first) {
  const context = await browser.newContext({ viewport: { width, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const feed = { next: null };
  await page.route("**/*", serve(lang, first, feed));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-req").first().waitFor();
  await page.locator(".rt-ctx .ctx-card").waitFor();
  const send = async (...rs) => {
    for (let i = 0; i < 100 && !feed.next; i++) await page.waitForTimeout(50);
    const next = feed.next;
    assert(next, "the page asks for the next trace");
    feed.next = null;
    next(rs);
    await page.waitForTimeout(300);
  };
  return { context, page, errors, send };
}

const blocks = (page) => page.$$eval(".rt-ctx-sess:not([hidden]) .rt-ctx-s", (bs) => bs.map((b) => ({
  name: b.querySelector(".nm").textContent, sub: b.querySelector(".sub").textContent, on: b.getAttribute("aria-pressed") === "true",
})));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: each session sending at once has its own context window, by its name`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}px`, async () => {
          // session a began first, then b: b is the newer session
          const { context, page, errors, send } = await open(browser, lang, width, [req(101, "a"), req(102, "b")]);
          t.after(() => context.close());
          await page.locator(".rt-ctx-sess:not([hidden]) .rt-ctx-s").nth(1).waitFor();
          const label = await page.evaluate(([l]) => (l === "en" ? "Each session's context window" : I18N[l]?.["Each session's context window"]), [lang]);
          assert.ok(label, `the strip's label has its ${lang}`);
          assert.equal(await page.locator(".rt-ctx-sess").getAttribute("aria-label"), label);

          // watch every frame's blocks from here on
          await page.evaluate(() => {
            window.__frames = [];
            window.__on = true;
            const tick = () => {
              window.__frames.push([...document.querySelectorAll(".rt-ctx-sess:not([hidden]) .rt-ctx-s")].map((b) => [b.querySelector(".nm").textContent, b.querySelector(".sub").textContent]));
              if (window.__on) requestAnimationFrame(tick);
            };
            requestAnimationFrame(tick);
          });
          // the sessions take turns, a's and b's requests interleaved
          await send(req(103, "a", false));
          await send(req(103, "a"), req(104, "b", false));
          await send(req(104, "b"), req(105, "a"));
          await send(req(106, "b"));
          const frames = await page.evaluate(() => { window.__on = false; return window.__frames; });
          assert(frames.length > 10, `frames were watched: ${frames.length}`);
          const k = (n) => Math.round(n / 1000) + "K";
          for (const f of frames) {
            // the newest session first, each in its place, by its title
            assert.deepEqual(f.map((x) => x[0]), [SESS.b.title, SESS.a.title], `the blocks kept their places: ${JSON.stringify(f)}`);
            // each block says its own session's reading, never the other's
            assert.match(f[0][1], new RegExp(`^Codex · (${k(SESS.b.base + 2000)}|${k(SESS.b.base + 4000)}|${k(SESS.b.base + 6000)}) / 272K`), JSON.stringify(f));
            assert.match(f[1][1], new RegExp(`^Codex · (${k(SESS.a.base + 1000)}|${k(SESS.a.base + 3000)}|${k(SESS.a.base + 5000)}) / 272K`), JSON.stringify(f));
          }
          let bs = await blocks(page);
          assert.deepEqual(bs.map((b) => b.sub), [`Codex · ${k(SESS.b.base + 6000)} / 272K · ${Math.round((SESS.b.base + 6000) / 2720)}%`, `Codex · ${k(SESS.a.base + 5000)} / 272K · ${Math.round((SESS.a.base + 5000) / 2720)}%`]);
          // the card tells of b's 106; its block is the one lit, and the
          // card names the session by its title, not its id
          assert.deepEqual(bs.map((b) => b.on), [true, false]);
          const crumbs = await page.locator(".rt-ctx .ctx-crumbs").textContent();
          assert.ok(crumbs.includes(SESS.b.title.slice(0, 20)), crumbs);
          assert.ok(!crumbs.includes(SESS.b.id.slice(0, 8)), crumbs);

          // a's block clicked: the card tells of a's newest, 105
          await page.locator(".rt-ctx-s").nth(1).click();
          await page.waitForFunction(() => document.querySelectorAll(".rt-ctx-s")[1]?.getAttribute("aria-pressed") === "true");
          assert.ok((await page.locator(".rt-ctx .ctx-crumbs").textContent()).includes("#105"));
          assert.ok((await page.locator(".rt-ctx .ctx-crumbs").textContent()).includes(SESS.a.title.slice(0, 20)));
          bs = await blocks(page);
          assert.deepEqual(bs.map((b) => b.name), [SESS.b.title, SESS.a.title], "a click doesn't move the blocks");

          // at a phone's width the blocks fit the page: nothing runs off it
          const over = await page.evaluate(() => {
            const s = document.querySelector(".rt-ctx-sess").getBoundingClientRect();
            return [...document.querySelectorAll(".rt-ctx-s")].some((b) => b.getBoundingClientRect().right > s.right + 0.5) || document.documentElement.scrollWidth > innerWidth;
          });
          assert.equal(over, false, "the blocks stay inside the page");
          assert.deepEqual(errors, []);
        });
      }
    }
  });

  test(`${engine}: one session alone shows no blocks, and one with no title is named by its folder`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { context, page, errors, send } = await open(browser, "en", 1100, [req(101, "a"), req(102, "a")]);
    t.after(() => context.close());
    await page.waitForTimeout(300);
    assert.equal(await page.locator(".rt-ctx-sess").isHidden(), true, "one session: the card is its own");
    // b comes in without a title yet: named by its folder, never its id
    await send(req(103, "b", true, false));
    await page.locator(".rt-ctx-sess:not([hidden]) .rt-ctx-s").nth(1).waitFor();
    assert.deepEqual((await blocks(page)).map((b) => b.name), ["docs", SESS.a.title]);
    assert.deepEqual(errors, []);
  });
  // tz_188 on Discord: ZCode's sessions are named by their first prompt,
  // a long Chinese one of several lines. WebKit breaks a line at U+2028
  // and U+2029 even where a name is kept to one, so each block stacked
  // the prompt's lines. Its bar took context.css's flex: 1 1 0, so in a
  // block as tall as its lines it was 0px high, and in a shorter block
  // beside a taller one it stretched to fill it. Each name is one line in
  // every block, at every width, in every language, and every bar is 4px.
  test(`${engine}: a session named by a prompt of many lines is one line in its block`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const prompt = "你的唯一任务是采集并分析用户提供的所有网页内容，逐条提取关键信息。\u2028要求：\u2029一、严格按照格式输出；\r\n二、保留原文中的数字与链接；\u0085三、不要遗漏任何一条。\n\n";
    const titles = { a: SESS.a.title, b: SESS.b.title };
    t.after(() => { SESS.a.title = titles.a; SESS.b.title = titles.b; });
    SESS.a.title = prompt.repeat(40);
    SESS.b.title = prompt;
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 700, 390]) {
        await t.test(`${lang} ${width}px`, async () => {
          const { context, page, errors } = await open(browser, lang, width, [req(101, "a"), req(102, "b"), req(103, "a")]);
          t.after(() => context.close());
          await page.locator(".rt-ctx-sess:not([hidden]) .rt-ctx-s").nth(1).waitFor();
          const got = await page.$$eval(".rt-ctx-s", (bs) => bs.map((b) => {
            const h = (q) => b.querySelector(q).getBoundingClientRect().height;
            return { name: b.querySelector(".nm").textContent, nm: h(".nm"), sub: h(".sub"), bar: h(".ctx-stack") };
          }));
          assert.equal(got.length, 2);
          for (const g of got) {
            assert.ok(!/[\n\r\u2028\u2029\u0085]/.test(g.name), `the name is one line of text: ${JSON.stringify(g.name.slice(0, 80))}`);
            assert.ok(g.name.startsWith("你的唯一任务是采集并分析用户提供的所有网页内容，逐条提取关键信息。 要求： 一、"), g.name.slice(0, 80));
            assert.ok(g.nm < 2 * g.sub, `the name is drawn on one line: ${g.nm}px against the sub-line's ${g.sub}px`);
            assert.equal(Math.round(g.bar), 4, `the bar keeps its 4px: ${g.bar}`);
          }
          const crumbs = await page.locator(".rt-ctx .ctx-crumbs").textContent();
          assert.ok(!/[\n\r\u2028\u2029\u0085]/.test(crumbs), "the card's crumbs name it on one line too");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
