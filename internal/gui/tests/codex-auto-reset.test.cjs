// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex account spending a reset by itself (StringKe on Discord: resets
// gifted to the account, used once its week is out): beside "Use a reset"
// on the Usage page's card, and "Use one…" on the menu bar panel's, an
// "Auto-use" toggle, off until turned on, posts settings/codex-auto-reset
// for that account and shows itself pressed; turned off again the same way.
// A GLM team's resets, spent on its own site, a plugin's, and an account
// with no name get none, nor a button to use one. No click moves the page; no left-border accent. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "Me@example.com", plan: "Plus",
    windows: [{ name: "5 hours", used: 100, resetsAt: later }, { name: "7 days", used: 100, resetsAt: later }],
    resets: { count: 2, until: later } },
  { provider: "zhipu", name: "GLM Coding", icon: "zhipu-color", user: "team@example.com", plan: "Team",
    windows: [{ name: "5 hours", used: 30 }], resets: { count: 1, byWindow: true, fiveHour: 1 } },
  // a plugin's, its resets told but not spent from magpie: never sent to Codex's
  { provider: "codex-plugin", name: "Codex (plugin)", user: "Me@example.com", plan: "Plus",
    windows: [{ name: "7 days", used: 100, resetsAt: later }], resets: { count: 3, until: later } },
];

function serve(lang, posts) {
  let auto = [];
  const settings = () => ({ theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["codex", "zhipu", "codex-plugin"], trayUsageEvery: 3, codexAutoReset: auto });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/codex-auto-reset") {
      const body = req.postDataJSON();
      posts.push(body);
      const who = body.user.toLowerCase();
      auto = auto.filter((u) => u !== who);
      if (body.on) auto.push(who);
      return json(settings());
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { auto: "Auto-use", on: "Me@example.com uses a reset by itself once its week is used up", off: "Me@example.com no longer uses a reset by itself" },
  zh: { auto: "自动使用", on: "Me@example.com 的每周额度用完时会自动使用一次重置", off: "Me@example.com 不再自动使用重置" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a Codex account's resets used by themselves, turned on and off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-auto-reset-${i}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (url, viewport, posts) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto(url);
        return page;
      };
      const wait = async (posts, n) => { for (let i = 0; i < 60 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50)); };
      // a toggle's click: it posts, turns, and moves nothing on the page
      const flip = async (page, sel, posts, want, said) => {
        const b = page.locator(sel);
        const at = await b.evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);
        const n = posts.length;
        await b.click();
        await wait(posts, n + 1);
        assert.deepEqual(posts.at(-1), { user: "Me@example.com", on: want });
        await page.locator(sel + `[aria-pressed="${want}"]`).waitFor();
        assert.equal(await page.locator(sel).evaluate((e) => e.classList.contains("on")), want);
        assert.equal(await page.locator("#status").textContent(), said);
        await page.waitForTimeout(200);
        assert.deepEqual(await page.locator(sel).evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]), at, "the click moved the page");
      };

      // the Usage page's card
      const posts = [];
      const page = await open("http://magpie.test/?view=usage", { width: 900, height: 700 }, posts);
      const sel = ".quota-resets .auto-reset";
      await page.locator(sel).waitFor();
      assert.equal(await page.locator(sel).count(), 1, "the GLM team's resets have no toggle");
      assert.equal(await page.locator(".quota-resets").count(), 3, "every account's resets are told");
      assert.equal(await page.locator(".quota-resets button").count(), 2, "only Codex's resets are used from here");
      assert.equal((await page.locator(sel).textContent()).trim(), w.auto);
      assert.equal(await page.locator(sel).getAttribute("aria-pressed"), "false", "off until turned on");
      assert(await page.locator(sel).getAttribute("title"));
      // beside the button that uses one now
      assert.equal(await page.locator(sel).evaluate((e) => e.nextElementSibling?.className), "text");
      await flip(page, sel, posts, true, w.on);
      await flip(page, sel, posts, false, w.off);
      const border = await page.evaluate(() => [...document.querySelectorAll(".quota-resets, .quota-resets *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the menu bar panel's card
      const panelPosts = [];
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 600 }, panelPosts);
      const psel = ".pq-resets .pq-auto";
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      await panel.locator(psel).waitFor();
      assert.equal(await panel.locator(psel).getAttribute("aria-pressed"), "false");
      assert.equal((await panel.locator(psel).textContent()).trim(), w.auto);
      assert.equal(await panel.locator(".pq-resets").count(), 3);
      assert.equal(await panel.locator(".pq-resets button").count(), 2, "only Codex's resets are used from the panel");
      await flip(panel, psel, panelPosts, true, w.on);
      // still beside "Use one…", on one line
      const [a, u] = await panel.evaluate(() => [...document.querySelectorAll(".pq-resets button")].map((b) => b.getBoundingClientRect().top));
      assert.equal(Math.round(a), Math.round(u), "the toggle and Use one… sit on one line");

      const missing = await page.evaluate(() => [
        "Auto-use", "{who} no longer uses a reset by itself", "{who} uses a reset by itself once its week is used up",
        "On: a reset is used by itself when this account's week is used up and no other account can answer, one a week at most. Click to turn it off.",
        "Use a reset by itself when this account's week is used up and no other account can answer, one a week at most. The five hours running out never uses one.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
