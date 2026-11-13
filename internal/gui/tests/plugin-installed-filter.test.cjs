// Run with Node's test runner and Playwright on the module path; see README.md.
// Installed is narrowed as Discover is (WJaunita45604 on X: 要求在已安装的
// 插件里面，也要按照发现界面里面的分类…并且可以筛选是否为官方插件，具体实现
// 分类方法与发现界面里的分类方法一致): the same strip of kinds, each with how
// many are installed, and the same Origin menu (magpie community, other
// authors, unofficial on GitHub), with nothing about installing. A plugin
// Discover lists is put where its card is; one it doesn't (a folder, an
// npm package nobody listed) by what it is. A pick that leaves nothing
// says so with Show all, and Discover's own picks are left alone. Seven
// plugins: a community subscription and middleware, another author's
// agent, two repositories tagged on GitHub (a provider and a middleware,
// from the dev instance's real /api/plugins/github answer), a middleware
// in a folder and an unlisted npm subscription. The API is faked and no
// request leaves the page. Every language, both engines, 1100px and 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const GH = require("./fixtures/plugins-github-31.json");
const LISTINGS = [
  { package: "@magpie-community/opencode-zed-auth", name: "Zed", community: true },
  { package: "@magpie-community/opencode-think-tags", name: "Think tags", community: true, kind: "middleware" },
  { package: "opencode-some-agent", name: "Some Agent", kind: "agent" },
];
const ghEntry = (repo) => { const r = GH.repos.find((x) => x.repo === repo); return { spec: r.spec, package: r.package, providers: [], version: "1.0.0", moved: [] }; };
const INSTALLED = [
  { spec: "@magpie-community/opencode-zed-auth", providers: [], version: "0.2.0", moved: [] },
  { spec: "@magpie-community/opencode-think-tags", providers: [], version: "0.1.2", moved: [], isMiddleware: true, middlewareOnly: true },
  { spec: "opencode-some-agent", providers: [], version: "1.0.0", moved: [], isAgent: true },
  ghEntry("mu-zi-lee/magpie-our-free-model"),
  ghEntry("ezdemo/magpie-haiku55-thinking"),
  { spec: "/Users/me/plugins/my-hook", ownName: "My hook", providers: [], moved: [], isMiddleware: true, middlewareOnly: true },
  { spec: "opencode-unlisted-auth", providers: [], version: "3.0.0", moved: [] },
];
// what Discover calls each: [kind, origin]
const WANT = {
  "Zed": ["pv", "ours"], "Think tags": ["mw", "ours"], "Some Agent": ["ag", "third"],
  "magpie-our-free-model": ["pv", "gh"], "magpie-opendesign-compatible-fix": ["mw", "gh"],
  "My hook": ["mw", "third"], "opencode-unlisted-auth": ["pv", "third"],
};

function server(lang, fetched) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: INSTALLED, picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: LISTINGS });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/github") return json(GH);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test" || !/^\/[\w./-]*$/.test(url.pathname)) { fetched.push(url.href); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const names = (k, src) => Object.keys(WANT).filter((n) => (!k || WANT[n][0] === k) && (!src || WANT[n][1] === src)).sort();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Installed is narrowed by Discover's kinds and origins", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 860 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], fetched = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, fetched));
        try {
          await page.goto("http://magpie.test/?view=plugins");
          const view = page.locator("#view-plugins");
          await view.locator(".pm-bar").first().waitFor();
          await view.locator(".pm-ghlook").waitFor({ state: "detached" });
          const tr = (s) => page.evaluate((s) => t(s), s);
          // the tabs are in the sidebar's list on some widths
          const tab = async (s) => page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: await tr(s) }).first().click();
          await tab("Installed");
          const head = view.locator(".pm-listhead");
          await head.locator(".pm-kinds").waitFor();
          const rows = view.locator(".pm-list .pm-row");
          const shown = async () => (await rows.locator(".who .name > span:first-child").allInnerTexts()).map((s) => s.trim()).sort();
          const kinds = head.locator(".pm-kinds .opt");
          const said = () => kinds.evaluateAll((bs) => bs.map((b) => [b.firstChild.textContent, b.querySelector("em").textContent]));
          const want = async (src) => Promise.all([["", "All"], ["pv", "Subscriptions"], ["mw", "Middleware"], ["ag", "Agents"]].map(async ([k, n]) => [await tr(n), String(names(k, src).length)]));
          const pop = head.locator(".pm-pop");
          const choose = async (name) => {
            await pop.click();
            await page.locator(".proto-menu .pm-item", { hasText: await tr(name) }).first().click();
            await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
          };

          // every one, and the strip counts each kind as Discover would
          assert.deepEqual(await shown(), names());
          assert.deepEqual(await said(), await want());
          assert.equal(await view.locator("select").count(), 0, "no native select");
          const overflow = () => page.evaluate(() => {
            const h = document.querySelector("#view-plugins .pm-listhead"), v = document.querySelector("#view-plugins");
            return [h.scrollWidth - h.clientWidth, v.scrollWidth - v.clientWidth, document.documentElement.scrollWidth - innerWidth].map((x) => Math.max(0, x));
          });
          assert.deepEqual(await overflow(), [0, 0, 0]);

          // a kind
          await kinds.nth(2).click();
          assert.deepEqual(await shown(), names("mw"));
          // and an origin: the strip counts what it leaves
          await pop.click();
          assert(!(await page.locator(".proto-menu .pm-item", { hasText: await tr("Not installed only") }).count()), "no Not installed only");
          await page.keyboard.press("Escape");
          await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
          await choose("magpie community");
          assert.deepEqual(await shown(), names("mw", "ours"));
          assert.deepEqual(await said(), await want("ours"));
          assert.equal((await pop.locator("b").textContent()).trim(), await tr("magpie community"));
          await choose("Unofficial, on GitHub");
          assert.deepEqual(await shown(), names("mw", "gh"));
          await kinds.nth(0).click();
          assert.deepEqual(await shown(), names("", "gh"));
          await choose("Other authors");
          assert.deepEqual(await shown(), names("", "third"));
          assert.deepEqual(await said(), await want("third"));

          // a pick that leaves nothing: said, and Show all puts back all
          await choose("Unofficial, on GitHub");
          await kinds.nth(3).click();
          assert.equal(await rows.count(), 0);
          const none = view.locator(".pm-ghnone");
          assert.equal((await none.locator("span").textContent()).trim(), await tr("No plugin matches"));
          await none.locator("button").click();
          assert.deepEqual(await shown(), names());
          assert.equal(await head.locator(".pm-kinds .opt.on").evaluate((b) => b.firstChild.textContent), await tr("All"));
          assert.equal((await pop.locator("b").textContent()).trim(), await tr("All"));

          // Discover's own picks are its own
          await kinds.nth(1).click();
          await tab("Discover");
          const dbar = view.locator(".pm-body > .pm-bar");
          await dbar.waitFor();
          assert.equal(await dbar.locator(".pm-kinds .opt.on").evaluate((b) => b.firstChild.textContent), await tr("All"));
          // and back, the pick kept
          await tab("Installed");
          await head.locator(".pm-kinds").waitFor();
          assert.deepEqual(await shown(), names("pv"));
          assert.deepEqual(await overflow(), [0, 0, 0]);
          assert.deepEqual(errors, []);
          assert.deepEqual(fetched, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
