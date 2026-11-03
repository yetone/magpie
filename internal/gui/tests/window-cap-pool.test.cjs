// Run with Node's test runner and Playwright on the module path; see README.md.
// An Antigravity account's meters set their own caps too (pluviophile714 on
// Discord: the row's 上限 90% said 每个窗口可单独设上限：点击下方它的用量条,
// and a click on 剩余 97.8% · 7d did nothing). Each pool's 5 hours and 7
// days is a button that posts its pool's window ("gemini · 7 days": both
// pools have a "7 days"); a family's meter, its models shown as one (the
// pool read without its summary), posts every model's window at once. The
// menu is the app's own, the click moves nothing. Chromium and WebKit,
// narrow and wide, in every language the menu is in; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5 + 60e3).toISOString();
const USER = "pluvio@example.com";
// as /api/login/usage sends them, WithCapped's marks on
const model = (name, id, family, pool, used) => ({ name, model: id, family, pool, used, resetsAt: at(3), capId: name.toLowerCase(), capped: true, capsSome: true });
const own = (pool, name, used, h) => ({ name, pool, used, resetsAt: at(h), capId: `${pool} · ${name}`.toLowerCase(), capped: true, capsSome: true });
const models = (pool) => [
  model("Gemini 3.1 Pro (High)", "gemini-3.1-pro-high", "Gemini", pool && "Gemini", 1),
  model("Gemini 3.1 Pro (Low)", "gemini-3.1-pro-low", "Gemini", pool && "Gemini", 1),
  model("Claude Sonnet 4.6", "claude-sonnet-4-6", "Claude", pool && "Claude & GPT", 0),
];
// the reporter's: 2.2% of the Gemini week used, 97.8% left
const pooled = () => [...models(true), own("Gemini", "5 hours", 1, 3), own("Gemini", "7 days", 2.2, 100), own("Claude & GPT", "5 hours", 0, 3), own("Claude & GPT", "7 days", 0, 90)];

function serve(lang, posts, windows) {
  const settings = { lang, theme: "light", quotaLeft: true };
  const antigravity = {
    id: "antigravity", name: "Antigravity", icon: "antigravity-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gemini-3.1-pro-high", name: "Gemini 3.1 Pro (High)", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
    account: { agent: "antigravity", agentName: "Antigravity", user: USER, plan: "Google AI Pro", logins: [{ user: USER, plan: "Google AI Pro", active: true, on: true }] },
    accountCaps: { [USER]: 90 },
  };
  const providers = { providers: [antigravity], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "antigravity" ? { [USER]: { provider: "antigravity", plan: "Google AI Pro", windows: windows() } } : {});
    if (url.pathname === "/api/provider/windowcap" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const caps = { ...antigravity.accountWindowCaps?.[USER] };
      for (const w of body.windows || [body.window]) {
        if (body.cap) caps[w] = body.cap; else delete caps[w];
      }
      antigravity.accountWindowCaps = { [USER]: caps };
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the menu's head names the window: the pool's, or the family's
const words = {
  en: { week: "Cap for Gemini · 7 days", fam: "Cap for Gemini", left: "97.8% left", cap: "Cap 50%" },
  zh: { week: "Gemini · 7 天的上限", fam: "Gemini的上限", left: "剩余 97.8%", cap: "上限 50%" },
  "zh-TW": { week: null, fam: null, left: null, cap: "上限 50%" },
  ja: { week: null, fam: null, left: null, cap: null },
  de: { week: null, fam: null, left: null, cap: null },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      const open = async (t, name, windows) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-${name}-window-cap-pool.png`) });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, windows));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Antigravity" }).first().click();
        await page.locator(".editor .accts .acc .aq-w").first().waitFor();
        return { page, errors, posts };
      };
      const row = (page) => page.locator(`.editor .accts .acc[data-account-id="${USER}"]`);
      const meters = (page) => row(page).locator('.aq-set[role="button"]');
      const menu = (page) => page.locator(".proto-menu");
      const pick = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) }).click();
      const posted = async (posts, n) => {
        for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
        return posts.at(-1);
      };
      const still = async (page, b) => {
        await b.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const top = () => row(page).evaluate((e) => Math.round(e.getBoundingClientRect().top));
        const before = await top();
        await b.click();
        await menu(page).waitFor();
        assert.equal(await top(), before, "the row moved");
      };
      const label = `${engine} ${width} ${lang}`;

      test(`${label}: each pool's 5 hours and 7 days sets its own cap`, async (t) => {
        const { page, errors, posts } = await open(t, "pool", pooled);
        // Gemini 5h, Gemini 7d, Claude & GPT 5h, 7d: every meter a button
        assert.equal(await row(page).locator(".aq-win").count(), 4);
        assert.equal(await meters(page).count(), 4);
        const week = meters(page).nth(1);
        if (w.left) assert.match(await week.textContent(), new RegExp(w.left.replace(".", "\\.")));
        // the account's 90% marks the pool's meters too
        assert.equal(await row(page).locator(".aq-cap").count(), 4);
        await still(page, week);
        if (w.week) assert.equal(await menu(page).locator(".pm-head").textContent(), w.week);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const n = posts.length;
        await pick(page, "50%");
        assert.deepEqual(await posted(posts, n), { id: "antigravity", account: USER, window: "gemini · 7 days", cap: 50 });
        await row(page).locator(".aq-own").first().waitFor();
        assert.equal(await row(page).locator(".aq-own").count(), 1);
        if (w.cap) assert.equal(await meters(page).nth(1).locator(".aq-own").textContent(), w.cap);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no page scroll sideways");
        assert.deepEqual(errors, []);
      });

      test(`${label}: a family's meter sets each of its models' windows`, async (t) => {
        const { page, errors, posts } = await open(t, "family", () => models(false));
        assert.equal(await meters(page).count(), 2);
        const gemini = meters(page).first();
        await still(page, gemini);
        if (w.fam) assert.equal(await menu(page).locator(".pm-head").textContent(), w.fam);
        const n = posts.length;
        await pick(page, "60%");
        const body = await posted(posts, n);
        assert.deepEqual({ ...body, windows: [...body.windows].sort() }, { id: "antigravity", account: USER, window: body.window, windows: ["gemini 3.1 pro (high)", "gemini 3.1 pro (low)"], cap: 60 });
        await row(page).locator(".aq-own").first().waitFor();
        assert.equal(await row(page).locator(".aq-own").count(), 1);
        assert.deepEqual(errors, []);
      });
    }
  }
}
