// Run with Node's test runner and Playwright on the module path; see README.md.
// Free quota by expiry (routes.FreeExpiring, a routing group's; wowbbs on
// Discord) on the Routing page: a request's story says the first went
// first because its free quota is lost soonest, in how long, as the
// gateway's trace gives it (Weighed.free, Weighed.expires); a free one
// that tells no expiry says it has free quota left; and a paid one first
// says no free one has quota left. The mode line says what the routing
// does. In both engines and every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const ahead = (hours) => new Date(now.getTime() + hours * 36e5).toISOString();
const acct = (provider, who, more) => ({ id: provider + "#" + who, provider, name: provider, who, kind: "account", agent: provider, model: "glm-5.3", routing: "expiring", known: true, used: 30, ...more });

const scenes = {
  // MiniMax's check-in credits lapse in 26 hours, WorkBuddy's month later
  soonest: { order: [acct("minimax-code", "mm@example.com", { free: true, expires: ahead(26), plan: "Free" }), acct("workbuddy", "wb@example.com", { free: true, expires: ahead(240), plan: "Free plan" }), acct("paid", "pro@example.com", { plan: "Pro" })] },
  // a free one that tells no expiry
  untold: { order: [acct("qoder-cn", "q@example.com", { free: true, plan: "Free" }), acct("paid", "pro@example.com", { plan: "Pro" })] },
  // every free one used up: the paid one
  paid: { order: [acct("paid", "pro@example.com", { plan: "Pro" }), acct("workbuddy", "wb@example.com", { free: true, used: 100, expires: ahead(24), plan: "Free plan" })] },
};

function serve(lang, scene) {
  const s = scenes[scene];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "glm-5.3", provider: s.order[0].provider, done: true, status: 200, ms: 900, tokens: 1200, order: s.order,
    tries: [{ id: s.order[0].id, model: "glm-5.3", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a said string as a pattern, its {names} standing for anything
const pattern = (s) => new RegExp("^" + s.replace(/[.*+?^$()|[\]\\]/g, "\\$&").replace(/\\?\{\w+\\?\}/g, ".+") + "$");

async function open(t, engine, lang, scene, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/*", serve(lang, scene));
  await page.goto("http://magpie.test/?view=routing");
  const req = page.locator(".rt-req").first();
  await req.waitFor();
  if (await req.getAttribute("aria-pressed") !== "true") await req.click();
  await page.locator(".rt-steps li.why").first().waitFor();
  // what a string says in lang, its {names} filled in; a language without
  // it would say it in English, which the i18n-keys test catches
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  return { page, errors, say, why: async () => (await page.locator(".rt-steps li.why").first().textContent()).trim() };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the free quota lost soonest says when`, async (t) => {
        const { page, errors, say, why } = await open(t, engine, lang, "soonest", width);
        const said = await why();
        assert.match(said, pattern(await say("Free quota by expiry: {who}'s free quota is lost soonest, in {d}, so it's spent first.")));
        assert.ok(said.includes("mm@example.com"), said);
        assert.equal((await page.locator(".rt-mode").textContent()).trim(),
          await say("Free quota by expiry: the accounts on a free plan, or whose model costs nothing there, go first, the one whose free quota is lost soonest first — at its next reset, or when its credits expire if sooner; once it is used up, the next. A paid one takes requests only when no free one has quota left, the paid as Smart orders them."));
        if (lang !== "en") assert.notEqual(await say("Free quota by expiry"), "Free quota by expiry", `${lang} names Free quota by expiry`);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a free one with no expiry told`, async (t) => {
      const { errors, say, why } = await open(t, engine, lang, "untold");
      const said = await why();
      assert.match(said, pattern(await say("Free quota by expiry: {who} has free quota left, so it's spent before any paid one.")));
      assert.ok(said.includes("q@example.com"), said);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: no free quota left, the paid one`, async (t) => {
      const { errors, say, why } = await open(t, engine, lang, "paid");
      const said = await why();
      assert.match(said, pattern(await say("Free quota by expiry: no free one has quota left, so {who}, which is paid for, is next.")));
      assert.ok(said.includes("pro@example.com"), said);
      assert.deepEqual(errors, []);
    });
  }
}
