// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's account whose sign-in its vendor refused (#1363, iamyhzhao:
// Grok moved onto its plugin, signed out after a restart) keeps its
// controls: "Sign-in required", Sign in again (the plugin's sign-in) and
// Remove (asked first, then login/forget), whether it is in use first,
// the agent's own, marked lapsed or only told so by its allowance read.
// Remove shows without a hover, and the reason on its allowance line sits
// below the controls, never over them. A
// signed-in plugin account in use first still has no Remove, as before.
// Chromium and WebKit, 420 and 1100 wide, English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const USER = "me@x.ai";
const LAPSE = USER + "'s Grok (SuperGrok) sign-in has expired — sign in again";

const plugins = [{ id: "grok", pid: "grok", name: "Grok (SuperGrok)", icon: "grok", spec: "@magpie-community/opencode-grok-auth", signedIn: true, models: 3,
  methods: [{ type: "oauth", label: "Grok CLI's sign-in" }] }];

const grok = (login) => ({
  id: "grok", name: "Grok (SuperGrok)", icon: "grok", chat: "plugin://grok/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "grok-4.7", name: "Grok 4.7", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  move: { state: "plugin" },
  account: { agent: "grok", agentName: "Grok (SuperGrok)", agentIcon: "grok", user: USER, plan: "SuperGrok", logins: [{ user: USER, plan: "SuperGrok", active: true, on: true, ...login }] },
});

function serve(lang, login, usage, posts) {
  const providers = () => ({ providers: [grok(login)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins, onPlugins: ["grok"] });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/login/usage") return json(usage);
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/plugin-signin/prompt") return json({ prompt: null, inputs: {} });
      if (url.pathname === "/api/plugin-signin") return json({ id: "s1", agent: "grok", state: "waiting", url: "https://fake.test/device" });
      return json(providers());
    }
    if (url.pathname.startsWith("/api/signin/")) return json({ id: "s1", agent: "grok", state: "waiting", url: "https://fake.test/device" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { required: "Sign-in required", again: "Sign in again", remove: "Remove", inUse: "In use" },
  zh: { required: "需要重新登录", again: "重新登录", remove: "移除", inUse: "使用中" },
};

const cases = [
  { name: "in use first, marked lapsed (the report)", login: { lapsed: LAPSE }, usage: {} },
  { name: "the agent's own, told by its allowance read", login: { own: true }, usage: { [USER]: { error: LAPSE, windows: [] } } },
];

const overlap = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's signed-out account keeps Sign in again and Remove", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = W[lang];
      for (const width of [420, 1100]) {
        const open = async (login, usage, posts) => {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, login, usage, posts));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Grok" }).click();
          const row = page.locator(".accts .acc", { hasText: USER });
          await row.waitFor();
          return { page, errors, row };
        };
        for (const c of cases) {
          await t.test(`${lang} ${width}: ${c.name}`, async () => {
            const posts = [];
            const { page, errors, row } = await open(c.login, c.usage, posts);
            await row.locator(".using", { hasText: w.required }).waitFor();
            const again = row.getByRole("button", { name: w.again, exact: true });
            const rm = row.getByRole("button", { name: w.remove, exact: true });
            assert.equal(await again.count(), 1, "Sign in again is on the row");
            assert.equal(await rm.count(), 1, "Remove is on the row");
            assert.equal(await row.locator(".using", { hasText: w.inUse }).count(), 0, "a signed-out account reads as in use");
            // the reason is said on the allowance line, below the controls,
            // and nothing covers them or runs out of the row
            const aq = row.locator(".aq");
            await aq.locator(".aq-none").waitFor();
            const box = await row.boundingBox(), line = await aq.boundingBox();
            for (const b of [again, rm]) {
              await b.scrollIntoViewIfNeeded();
              assert.ok(await b.isVisible());
              const r = await b.boundingBox();
              assert.ok(!overlap(r, line), `${await b.innerText()} is under the reason`);
              assert.ok(r.x >= box.x - 1 && r.x + r.width <= box.x + box.width + 1, `${await b.innerText()} runs out of the row`);
              assert.ok(r.y + r.height <= line.y + 1, `${await b.innerText()} is below the reason`);
            }
            // shown as it is, not only while the pointer is on the row
            await page.mouse.move(1, 1);
            for (const b of [again, rm]) assert.equal(await b.evaluate((el) => getComputedStyle(el).opacity), "1", `${await b.innerText()} shows only on hover`);
            const hit = await rm.evaluate((el) => { const r = el.getBoundingClientRect(); return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2) === el; });
            assert.ok(hit, "Remove is covered");
            if (process.env.ARTIFACT_DIR) await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-signed-out-${engine}-${lang}-${width}-${c.login.own ? "own" : "lapsed"}.png`) });

            // Remove asks first, then forgets the account
            await rm.click();
            assert.deepEqual(posts, [], "Remove waits for confirmation");
            await page.locator("dialog.action-confirm[open] button").last().click();
            for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
            assert.deepEqual(posts, [{ path: "/api/login/forget", body: { agent: "grok", user: USER } }]);

            // Sign in again runs the plugin's sign-in
            posts.length = 0;
            await page.locator(".accts .acc", { hasText: USER }).getByRole("button", { name: w.again, exact: true }).click();
            for (let i = 0; i < 50 && !posts.some((p) => p.path === "/api/plugin-signin"); i++) await page.waitForTimeout(50);
            assert.deepEqual(posts.map((p) => p.path), ["/api/plugin-signin/prompt", "/api/plugin-signin"]);
            assert.equal(posts[1].body.provider, "grok");
            assert.deepEqual(errors, []);
            await page.context().close();
          });
        }

        await t.test(`${lang} ${width}: signed in, in use first, has no Remove as before`, async () => {
          const posts = [];
          const { page, errors, row } = await open({}, { [USER]: { windows: [{ name: "7 days", used: 16 }] } }, posts);
          await row.locator(".using", { hasText: w.inUse }).waitFor();
          assert.equal(await row.getByRole("button", { name: w.remove, exact: true }).count(), 0);
          assert.equal(await row.getByRole("button", { name: w.again, exact: true }).count(), 0);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
