// Run with Node's test runner and Playwright on the module path; see README.md.
// myduso on Discord: under Claude's accounts a tick keeps them to Claude
// Code, magpie only switching its own sign-in and serving no request on
// them. It shows with one account too: what it stops is magpie serving
// them, not a switch between them. Ticking it posts provider/switchonly,
// and the redraw keeps it ticked; unticking posts it off. The page doesn't
// move. In English, Chinese, Japanese and German, Chromium and WebKit, at
// 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = ({ switchOnly = false, spare = true } = {}) => ({
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "", switchOnly,
  models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "work@example.com", plan: "MAX",
    logins: [{ user: "work@example.com", plan: "MAX", active: true, on: true }].concat(spare ? [{ user: "spare@example.com", plan: "PRO", on: true }] : []),
  },
});

function serve(lang, first, posts) {
  let list = [first];
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/switchonly") {
      const body = route.request().postDataJSON();
      posts.push(body);
      list = [{ ...list[0], switchOnly: body.switchOnly }];
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const label = {
  en: "Only switch Claude Code's sign-in",
  zh: "只切换 Claude Code 的登录",
  ja: "Claude Code のサインインだけを切り替える",
  de: "Nur die Anmeldung von Claude Code wechseln",
};
const strings = [
  "Only switch {agent}'s sign-in",
  "When an account is used up, magpie signs {agent} in to the next one. Requests on these accounts don't go through magpie.",
  "magpie serves no requests on these accounts",
  "magpie serves requests on these accounts again",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(label)) {
      const open = async (t, opts) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, claude(opts), posts));
        await page.goto("http://magpie.test/?view=providers");
        // the first draw waits on the page's first reads: a load's room, not a click's (#4470)
        const row = page.locator(".row.provider", { hasText: "Claude" }).first();
        await row.waitFor({ timeout: 60000 });
        await row.click();
        await page.locator(".editor .acc").first().waitFor();
        return { page, errors, posts };
      };

      test(`${engine} ${width} ${lang}: a tick keeps Claude's accounts to Claude Code`, async (t) => {
        const { page, errors, posts } = await open(t, {});
        const only = page.locator(".editor .accts .switch-only label");
        assert.equal((await only.textContent()).trim(), label[lang]);
        assert.ok(await only.getAttribute("title"), "it says what it does");
        assert.equal(await only.locator("input").isChecked(), false);
        if (lang === "en") {
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), strings);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        await only.scrollIntoViewIfNeeded();
        const box = await only.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the tick fits across ${width}`);
        const scrollers = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(document.scrollingElement.scrollTop));
        const before = await scrollers();
        await only.locator("input").click();
        await page.waitForFunction(() => document.querySelector(".editor .accts .switch-only input")?.checked === true);
        assert.deepEqual(posts, [{ id: "claude", switchOnly: true }]);
        assert.deepEqual(await scrollers(), before, "the page doesn't move");
        await page.locator(".editor .accts .switch-only input").click();
        await page.waitForFunction(() => document.querySelector(".editor .accts .switch-only input")?.checked === false);
        assert.deepEqual(posts, [{ id: "claude", switchOnly: true }, { id: "claude", switchOnly: false }]);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: kept to Claude Code is drawn ticked, and shown with one account`, async (t) => {
        const ticked = await open(t, { switchOnly: true });
        assert.equal(await ticked.page.locator(".editor .accts .switch-only input").isChecked(), true);
        const one = await open(t, { spare: false });
        assert.equal(await one.page.locator(".editor .accts .switch-only").count(), 1);
        assert.deepEqual([...ticked.errors, ...one.errors], []);
      });
    }
  }
}
