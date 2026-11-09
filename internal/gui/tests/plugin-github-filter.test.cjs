// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover is one market: magpie's community, other authors and the
// repositories tagged magpie-plugin on GitHub, browsed with one bar
// (yetone: 社区插件、非官方插件散落的太厉害了，你要把它统筹起来). The bar's
// kinds say how many of each there are under what Origin leaves; Origin
// picks a source and, beside it, what isn't installed yet (the owner:
// 加一个未安装); a shelf's See all opens its kind whole; Sort puts them by
// name; a pick that leaves nothing says so with Show all, which puts back
// every pick. The page's search field keeps its node, focus and every
// key while npm's answer redraws the page under it, and nothing is left
// over at 420px. The repositories are the dev instance's real
// /api/plugins/github answer of 2026-10-13 (31: 27 providers, 3
// middleware, an agent); the API is faked and no request leaves the page.
// English, Chinese, Traditional Chinese, Japanese and German, 1100px,
// 980px and 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const GH = require("./fixtures/plugins-github-31.json");
// two of them already here, a provider and a middleware, installed by the
// spec the page would send for each: the strip says both sides of it, and
// the kinds narrow it either way
const HERE = ["mu-zi-lee/magpie-our-free-model", "ezdemo/magpie-haiku55-thinking"];
const INSTALLED = HERE
  .map((repo) => GH.repos.find((r) => r.repo === repo))
  .map((r) => ({ spec: r.spec, package: r.package, providers: [], version: "1.0.0", moved: [] }));

function server(lang, fetched, installed = INSTALLED) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: installed, picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [{ package: "@magpie-community/opencode-zed-auth", name: "Zed", community: true }] });
    // npm answers late, while the field is being typed in: the page is drawn again under it
    if (url.pathname === "/api/plugins/npm") { await new Promise((r) => setTimeout(r, 1500)); return json({ npm: {} }); }
    if (url.pathname === "/api/plugins/github") return json(GH);
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test" || !/^\/[\w./-]*$/.test(url.pathname)) { fetched.push(url.href); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const kindOf = (r) => r.kind === "middleware" ? "mw" : r.kind === "agent" ? "ag" : "pv";
const has = (r, s) => [r.repo, r.owner, r.package || "", r.description || ""].join(" ").toLowerCase().includes(s.toLowerCase());
const here = (r) => HERE.includes(r.repo);
const count = (rs, k) => rs.filter((r) => !k || kindOf(r) === k).length;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the plugins on GitHub are narrowed by kind and by a field of their own", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 980, 420]) {
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
          const bar = view.locator(".pm-bar");
          await bar.waitFor();
          await view.locator(".pm-ghlook").waitFor({ state: "detached" });
          const tr = (s) => page.evaluate((s) => t(s), s);
          if (lang !== "en") for (const s of ["Kind of plugin", "Origin", "Sort", "Not installed only", "Other authors", "See all", "No plugin matches", "Most popular"]) assert.notEqual(await tr(s), s, `${s} in ${lang}`);
          const cards = view.locator(".pm-sec[data-kind] .pm-card");
          const kinds = bar.locator(".pm-kinds .opt");
          const said = () => kinds.evaluateAll((bs) => bs.map((b) => [b.firstChild.textContent, b.querySelector("em").textContent]));
          const want = async (rs) => Promise.all([["", "All"], ["pv", "Subscriptions"], ["mw", "Middleware"], ["ag", "Agents"]].map(async ([k, n]) => [await tr(n), String(count(rs, k))]));
          // Zed, from magpie's community, is a subscription too
          const ZED = { kind: undefined, repo: "z/zed" };
          assert.deepEqual(await said(), await want([...GH.repos, ZED]));
          // nothing left over at this width: the kinds in Japanese are wide
          const overflow = () => page.evaluate(() => {
            const bar = document.querySelector("#view-plugins .pm-bar");
            const v = document.querySelector("#view-plugins");
            return [bar.scrollWidth - bar.clientWidth, v.scrollWidth - v.clientWidth, document.documentElement.scrollWidth - innerWidth];
          });
          assert.deepEqual((await overflow()).map((x) => Math.max(0, x)), [0, 0, 0], JSON.stringify(await overflow()));
          const pops = bar.locator(".pm-pop");
          const choose = async (i, name) => {
            await pops.nth(i).click();
            await page.locator(".proto-menu .pm-item", { hasText: await tr(name) }).first().click();
            if (await page.locator(".proto-menu").isVisible().catch(() => false)) await page.keyboard.press("Escape");
            await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
          };

          // Origin GitHub: the kinds count the 31 alone
          await choose(0, "Unofficial, on GitHub");
          assert.deepEqual(await said(), await want(GH.repos));
          assert.equal((await pops.nth(0).locator("b").textContent()).trim(), await tr("Unofficial, on GitHub"));
          // a kind on top: its shelf alone, whole
          await kinds.nth(2).click();
          assert.equal(await cards.count(), count(GH.repos, "mw"));
          assert.equal(await cards.locator(".pm-chip.kind.mw").count(), count(GH.repos, "mw"));
          // and only what isn't here yet: the middleware installed goes,
          // the kinds count what is left
          await choose(0, "Not installed only");
          const fresh = GH.repos.filter((r) => !here(r));
          assert.equal(await cards.count(), count(fresh, "mw"));
          assert.equal(await view.locator(".pm-card.have").count(), 0);
          assert.deepEqual(await said(), await want(fresh));
          assert.equal((await pops.nth(0).locator("b").textContent()).trim(), await tr("Unofficial, on GitHub") + " · " + await tr("Not installed"));

          // magpie's community has no middleware: said so, and Show all
          // puts back every pick
          await choose(0, "magpie community");
          assert.equal(await cards.count(), 0);
          const none = view.locator(".pm-ghnone");
          assert.equal((await none.locator("span").textContent()).trim(), await tr("No plugin matches"));
          await none.locator("button").click();
          assert.equal((await bar.locator(".pm-kinds .opt.on").evaluate((b) => b.firstChild.textContent)), await tr("All"));
          assert.equal((await pops.nth(0).locator("b").textContent()).trim(), await tr("All"));
          assert.deepEqual(await said(), await want([...GH.repos, ZED]));

          // a shelf's See all: its kind whole
          await choose(0, "Unofficial, on GitHub");
          const pv = view.locator('.pm-sec[data-kind="pv"]');
          assert.ok(await pv.locator(".pm-card").count() < count(GH.repos, "pv"));
          await pv.locator(".pm-more").click();
          assert.equal(await cards.count(), count(GH.repos, "pv"));
          assert.equal((await bar.locator(".pm-kinds .opt.on").evaluate((b) => b.firstChild.textContent)), await tr("Subscriptions"));
          // Sort by name
          await choose(1, "Name");
          const shown = await cards.locator(".pm-name b").allInnerTexts();
          assert.deepEqual(shown, await page.evaluate((a) => [...a].sort((x, y) => x.localeCompare(y, intlLang())), shown));
          assert.equal(shown.length, count(GH.repos, "pv"));

          // the page's search, typed while npm's answer redraws the page:
          // the same node keeps its focus and every key
          const q = view.locator(".pm-head .pm-find input");
          await q.evaluate((x) => { window.__field = x; });
          await q.click();
          await page.keyboard.type("doubao", { delay: 120 });
          await page.waitForTimeout(800);
          assert(await q.evaluate((x) => x === window.__field && document.activeElement === x && x.value === "doubao"), "the field kept its node, focus and text");
          const doubao = GH.repos.filter((r) => has(r, "doubao"));
          await page.waitForFunction((n) => document.querySelector("#view-plugins .pm-sec")?.querySelectorAll(".pm-card").length === n, doubao.length);
          for (const s of ["mu-zi-lee", "Cloudflare AI Gateway"]) {
            await q.fill(s);
            const rs = GH.repos.filter((r) => has(r, s));
            assert(rs.length > 0);
            await page.waitForFunction((n) => document.querySelector("#view-plugins .pm-sec")?.querySelectorAll(".pm-card").length === n, rs.length);
          }
          await q.fill("");
          await bar.waitFor();

          // nothing left over at any width
          const over = await overflow();
          assert.deepEqual(over.map((x) => Math.max(0, x)), [0, 0, 0], JSON.stringify(over));
          assert.deepEqual(errors, []);
          assert.deepEqual(fetched, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
