// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's details on the Sessions page say how full its context was,
// request by request (liang_yongxiang on Discord: Routing told it for one
// request, the Sessions page for none). A session the gateway sent calls of
// (via) asks /api/context/session for its line once, with its first and
// last, and draws the Context tab's line of it: a point a request, each
// telling its share of the window, and the fullest one's share beside it.
// A point clicked opens that request in Routing, with its card. A session
// the gateway never saw asks nothing and has no Context line, and one it
// kept no prompt of has none either. Opened again, the line is drawn at
// once, not asked again. In five languages, Chromium and WebKit, at 1100
// and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const VIA = [{ provider: "anthropic", model: "claude-sonnet-5", calls: 3, tokens: 270000 }];
const POINTS = [
  { id: 11, time: at(30), tokens: 30000, window: 200000 },
  { id: 12, time: at(20), tokens: 80000, window: 200000 },
  { id: 13, time: at(10), tokens: 160000, window: 200000 },
];

function serve(lang, asked) {
  const base = { agent: "claude", cwd: "/work/c", models: [{ model: "claude-sonnet-5", input: 270000, output: 9000 }], size: 4096, messages: 6, files: 1, deletable: true };
  const sessions = [
    { ...base, id: "seen-1", title: "Seen by the gateway", start: at(40), last: at(10), via: VIA },
    { ...base, id: "kept-none", title: "Nothing kept", start: at(50), last: at(45), via: VIA },
    { ...base, id: "own-only", title: "Never through magpie", start: at(60), last: at(55) },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "claude", count: sessions.length, deletable: true, name: "Claude Code", icon: "claude" }],
        agent: "claude", sessions, terminal: true, trash: [], trashDir: "~/trash" });
    }
    if (url.pathname === "/api/context/session") {
      const q = Object.fromEntries(url.searchParams);
      asked.push(q);
      if (q.id !== "seen-1") return json({ session: null });
      return json({ session: { agent: "claude", key: "seen-1", model: "claude-sonnet-5", first: POINTS[0].time, last: POINTS[2].time,
        requests: 3, peak: 160000, window: 200000, latestId: 13, points: POINTS } });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404 }));
  };
}

const NAV = { en: "Sessions", zh: "会话", "zh-TW": "工作階段", ja: "セッション", de: "Sitzungen" };
const CONTEXT = { en: "Context", zh: "上下文", "zh-TW": "上下文", ja: "コンテキスト", de: "Kontext" };
const TIP = {
  en: "Request 2: 80K of 200K (40%)",
  zh: "第 2 个请求：80K / 200K（40%）",
  "zh-TW": "第 2 個請求：80K / 200K（40%）",
  ja: "リクエスト 2：80K / 200K（40%）",
  de: "Anfrage 2: 80K von 200K (40%)",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a session's details show how full its context was, request by request`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-context-line.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const asked = [];
        await page.route("**/*", serve(lang, asked));
        await page.goto("http://magpie.test/?view=sessions");
        const view = page.locator("#view-sessions");
        if (!(await view.isVisible())) {
          const nav = page.locator("#nav").getByRole("button", { name: NAV[lang], exact: true });
          if (await nav.count()) await nav.click();
        }
        const open = async (id) => {
          const row = view.locator(`.row.sm-sess[data-id="${id}"]`);
          await row.waitFor();
          await row.locator(".who").click();
          const item = row.locator("xpath=..");
          await item.locator(".sess-detail").waitFor();
          return item.locator(".sess-detail");
        };

        // the session the gateway saw
        let detail = await open("seen-1");
        const line = detail.locator(".sess-line.sess-ctx");
        await line.locator(".ctx-spark svg").waitFor();
        assert.equal(await line.locator(".k").textContent(), CONTEXT[lang]);
        assert.equal(asked.length, 1);
        assert.equal(asked[0].agent, "claude");
        assert.equal(asked[0].id, "seen-1");
        assert.ok(asked[0].from && asked[0].to && Date.parse(asked[0].from) < Date.parse(asked[0].to), `its first and last: ${JSON.stringify(asked[0])}`);
        const hits = line.locator(".ctx-spark rect.hit");
        assert.equal(await hits.count(), POINTS.length, "a point a request");
        assert.equal(await hits.nth(1).locator("title").textContent(), TIP[lang]);
        const peak = line.locator(".ctx-sess-peak");
        assert.equal(await peak.textContent(), "80%");
        assert.match(await peak.getAttribute("class"), /\bwarn\b/);

        // inside the details, and the page never scrolls sideways
        const box = await line.evaluate((l) => {
          const r = l.getBoundingClientRect(), d = l.closest(".sess-detail").getBoundingClientRect();
          const s = l.querySelector(".ctx-spark").getBoundingClientRect();
          return { inside: r.right <= d.right + 0.5, spark: s.width, h: s.height };
        });
        assert.ok(box.inside, `the line stays inside the details at ${width}: ${JSON.stringify(box)}`);
        assert.ok(box.spark >= 60 && box.h > 10, `the line can be read: ${JSON.stringify(box)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");

        // a point opens its request in Routing, with its card
        await page.evaluate(() => { window.__opened = []; window.openRoute = (id, time, o) => { window.__opened.push([id, time, o]); return Promise.resolve(); }; });
        await hits.nth(1).dispatchEvent("click");
        assert.deepEqual(await page.evaluate(() => window.__opened), [[12, POINTS[1].time, { context: true }]]);

        // a session the gateway kept no prompt of: asked, no line
        const answered = page.waitForResponse((r) => r.url().includes("/api/context/session") && r.url().includes("id=kept-none"));
        detail = await open("kept-none");
        await answered;
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r())));
        assert.equal(await detail.locator(".sess-line.sess-ctx:not([hidden])").count(), 0);
        // one never through magpie: not asked, no line
        detail = await open("own-only");
        assert.equal(await detail.locator(".sess-line.sess-ctx").count(), 0);
        assert.equal(asked.filter((q) => q.id === "own-only").length, 0);

        // opened again, drawn at once from what was read
        detail = await open("seen-1");
        assert.equal(await detail.locator(".sess-line.sess-ctx .ctx-spark rect.hit").count(), POINTS.length);
        assert.equal(asked.filter((q) => q.id === "seen-1").length, 1, "not asked again");
        assert.deepEqual(errors, []);
      });
    }
  }
}

