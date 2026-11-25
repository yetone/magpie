// Run with Node's test runner and Playwright on the module path; see README.md.
// Before expiry, one by one (routes.PaceDrain, guhailin3 on X): the account
// whose quota a reset would lose most of per hour is picked, as Use before
// expiry picks, and keeps every request until it is used up or fails. It is
// one more option in the pickers that are there already: a provider's
// editor (with its words and the sign-in line Until used up has), the
// Routing page's row for Codex, and a request's story on the Routing page.
// Every language, both engines, the window's width and a phone's.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ID = "pace-drain";
const NAME = "Before expiry, one by one";
const HINT = "Requests go to the subscription with the most left per hour until it is lost, as Use before expiry picks, and stay on it until it is used up or fails; then the next is picked the same way.";
const MODE = "Before expiry, one by one: the account with the most left per hour until it is lost is picked, as Use before expiry picks, and takes every request until it is used up or fails; then the next is picked the same way.";
const STORY = "Before expiry, one by one: {who} is the one in use, picked for the most left per hour until it is lost, and answers everything until it is used up.";
const SIGNIN = "Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is used up, and keeps it there till that one is used up too.";
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const base = { icon: "generic", catalog: "", models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, affinity: "", ready: true };

function serve(lang, world) {
  const codex = () => ({
    ...base, id: "codex", name: "Codex", routing: world.routing, chat: "", responses: "", anthropic: "", keyList: [],
    account: {
      agent: "codex", agentName: "Codex", user: "one@example.test", plan: "Plus",
      logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Plus", active: i === 0, on: true })),
    },
  });
  const seat = (who, more) => ({ id: "codex#" + who, provider: "codex", name: "Codex", who, kind: "account", model: "gpt-5.5", routing: ID, known: true, used: 40, ...more });
  // two has the most left per hour before its week renews, and is in use
  const order = [seat("two@example.test"), seat("one@example.test", { used: 10 })];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "gpt-5.5", provider: "codex", done: true, status: 200, ms: 900, tokens: 1200, order,
    tries: [{ id: order[0].id, model: "gpt-5.5", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (r) => {
    const req = r.request();
    const url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" }, jsRoutings: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({
      groups: [], models: [], jsRoutings: [],
      pools: [{ provider: "codex", name: "Codex", icon: "generic", kind: "account", who: ["one@example.test", "two@example.test"], routing: world.routing, affinity: "" }],
    });
    if (url.pathname === "/api/providers") return json({ providers: [codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (req.method() === "POST" && url.pathname === "/api/provider/route") {
      world.posts.push(req.postDataJSON());
      world.routing = req.postDataJSON().routing;
      return json({ providers: [codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, width, world, view) {
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, world));
  await page.goto(`http://magpie.test/?view=${view}`);
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  return { page, errors, say };
}

const fits = (page) => page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a provider's editor offers Before expiry, one by one, with its words`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      for (const width of [900, 420]) {
        const where = `${width}px`;
        const world = { routing: "", posts: [] };
        const { page, errors, say } = await open(browser, lang, width, world, "providers");
        if (lang !== "en") for (const k of [NAME, HINT, MODE, STORY]) assert.notEqual(await say(k), k, `${lang} says ${k}`);
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts").first().waitFor();
        const adv = page.locator(".editor details.advanced");
        if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
        const field = page.locator(".editor label", { hasText: new RegExp(`^${await say("Routing")}$`) }).locator("xpath=following-sibling::div[1]");
        const opt = field.locator(`.segs .opt[data-seg="${ID}"]`).first();
        assert.equal((await opt.textContent()).trim(), await say(NAME), `${where}: named`);
        await opt.click();
        assert.ok(await opt.evaluate((b) => b.classList.contains("on")), `${where}: picked`);
        const hint = (await field.locator(".hint.expl").first().textContent()).trim();
        assert.equal(hint, `${await say(HINT)} ${await say(SIGNIN, { agent: "Codex" })}`, `${where}: its words, and where the sign-in goes`);
        assert.ok(await fits(page), `${where}: fits`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    });

    test(`${engine} ${lang}: the Routing page's row for Codex picks Before expiry, one by one`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      for (const width of [1100, 420]) {
        const where = `${width}px`;
        const world = { routing: "", posts: [] };
        const { page, errors, say } = await open(browser, lang, width, world, "groups");
        const pool = page.locator('.rt-pool[data-provider="codex"]');
        await pool.waitFor();
        const opt = pool.locator(`.opt[data-seg="${ID}"]`).first();
        assert.equal((await opt.textContent()).trim(), await say(NAME), `${where}: named`);
        await opt.click();
        await page.waitForFunction((id) => document.querySelector(`.rt-pool[data-provider="codex"] .opt[data-seg="${id}"]`)?.classList.contains("on"), ID);
        assert.deepEqual(world.posts.at(-1), { id: "codex", routing: ID }, `${where}: saved`);
        assert.ok(await fits(page), `${where}: fits`);
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    });

    test(`${engine} ${lang}: a request routed Before expiry, one by one says which one is in use and why`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      for (const width of [1100, 420]) {
        const where = `${width}px`;
        const { page, errors, say } = await open(browser, lang, width, { routing: ID, posts: [] }, "routing");
        const req = page.locator(".rt-req").first();
        await req.waitFor();
        if (await req.getAttribute("aria-pressed") !== "true") await req.click();
        await page.locator(".rt-steps li.why").first().waitFor();
        assert.equal((await page.locator(".rt-steps li.why").first().textContent()).trim(), await say(STORY, { who: "two@example.test" }), `${where}: story`);
        assert.equal((await page.locator(".rt-mode").textContent()).trim(), await say(MODE), `${where}: mode`);
        assert.ok(await fits(page), `${where}: fits`);
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    });
  }
}
