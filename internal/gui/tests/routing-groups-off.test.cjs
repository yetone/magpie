// Run with Node's test runner and Playwright on the module path; see README.md.
// bigbenniu on X: the Essentials preset leaves routing groups off, and
// the Routing page then had nothing past its requests: no New group, no
// groups, and no word of why. With them off, where they were the page says
// so and has a button that turns them on (plugins/builtin {id:"groups"}),
// after which the groups and New group are back and the note is gone. With
// them on, there is no note. English and Chinese, Chromium and WebKit, wide
// and narrow, and every language has the strings.
// yyddbds on Discord: with them off, the sidebar still had a Routing
// groups tab. Off, it is gone (and Routing's own "Routing groups ›"), an
// address kept for the page opens Routing, and what is left of the page,
// the note and the providers' own accounts, is under Routing's requests.
// Turned on, the tab is back and the groups' page opens.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
];
const pools = [{ provider: "a", name: "A", icon: "generic", kind: "key", who: ["k1", "k2"], routing: "", affinity: "" }];
const mine = { id: "mine", name: "Mine", members: ["a/m", "b/m"], ready: true, memberInfo: [{ id: "a/m", ready: true }, { id: "b/m", ready: true }] };

function serve(lang, start, posts) {
  let on = start;
  const builtins = () => [{ id: "groups", on }];
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true, featuresOff: on ? [] : ["groups"] })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", builtins: builtins() } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/builtin") {
      posts.push(JSON.parse(r.request().postData() || "{}"));
      on = true;
      return json(builtins());
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups: [mine], pools });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { why: "Routing groups are off in Plugins › Built-in", on: "Turn on routing groups", done: "Routing groups are on" },
  zh: { why: "路由组在「插件 › 内置」中已关闭", on: "打开路由组", done: "路由组已开启" },
};
const keys = [
  "Turn on routing groups", "Routing groups are on",
  "Routing groups are off in Plugins › Built-in: the groups magpie finds, one model several providers serve, still route, but your own can't be made or changed here.",
];

// the pages' tabs a reader sees, the sidebar's or the header's
const tabs = (page) => page.evaluate(() => [...document.querySelectorAll("#nav button[data-view]")].filter((b) => !b.hidden && b.offsetParent && getComputedStyle(b).display !== "none").map((b) => b.dataset.view));
const shown = (page) => page.evaluate(() => [...document.querySelectorAll("main.view")].filter((v) => !v.hidden).map((v) => v.id));

async function open(engine, width, t, name) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  return page;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    // the sidebar from 1024 points, the header's tabs under it, and narrow
    for (const width of [1200, 900, 420]) {
      test(`${engine} ${lang} ${width}px: with routing groups off, their tab is gone, and Routing says so and turns them on`, async (t) => {
        const page = await open(engine, width, t, `${engine}-${lang}-${width}-routing-groups-off.png`);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, false, posts));
        await page.goto("http://magpie.test/?view=groups");

        const note = page.locator("#view-routing .rt-gsec .rt-goff");
        await note.waitFor();
        assert.deepEqual(await shown(page), ["view-routing"], "an address kept for the groups' page opens Routing");
        if (width >= 800) {
          const ts = await tabs(page);
          assert(ts.includes("routing"), ts.join());
          assert(!ts.includes("groups"), `no Routing groups tab: ${ts.join()}`);
        }
        assert.equal(await page.locator("#rtToGroups").isVisible(), false, "nor Routing's way to the page");
        // the providers' own accounts are still to be set, on Routing
        await page.locator("#view-routing .rt-pool[data-provider=\"a\"]").waitFor({ state: "visible" });
        const text = await note.innerText();
        assert(text.includes(w.why), text);
        const btn = note.locator("button", { hasText: w.on });
        assert(await btn.isVisible());
        assert.equal(await page.locator(".rt-gnew").isVisible(), false, "New group is off with them");
        assert.equal(await page.locator(".rt-group", { hasText: "Mine" }).isVisible(), false);
        assert(await note.evaluate((n) => n.getBoundingClientRect().right <= innerWidth + 1), "the note fits the window");
        assert(await btn.evaluate((b) => b.getBoundingClientRect().right <= innerWidth + 1), "so does its button");

        await btn.click();
        await note.waitFor({ state: "hidden" });
        assert.deepEqual(posts, [{ id: "groups", on: true }]);
        // on, the groups' page opens, with them and New group, its tab back
        await page.locator("#view-groups .rt-group", { hasText: "Mine" }).waitFor({ state: "visible" });
        await page.locator("#view-groups .rt-gnew").waitFor({ state: "visible" });
        assert.deepEqual(await shown(page), ["view-groups"]);
        if (width >= 1024) assert((await tabs(page)).includes("groups"), "the tab is back");
        await page.locator("#view-groups .rt-pool[data-provider=\"a\"]").waitFor({ state: "visible" });
        await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.done);
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: with routing groups on, there is no note`, async (t) => {
      const page = await open(engine, 1200, t, `${engine}-${lang}-routing-groups-on.png`);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, true, []));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-group", { hasText: "Mine" }).waitFor({ state: "visible" });
      assert.equal(await page.locator(".rt-goff").isVisible(), false);
      assert(await page.locator(".rt-gnew").isVisible());
      assert.deepEqual(await shown(page), ["view-groups"]);
      assert((await tabs(page)).includes("groups"), "the tab is there");
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: the note's strings are in every language`, async (t) => {
    const page = await open(engine, 900, t, `${engine}-routing-groups-off-i18n.png`);
    await page.route("**/*", serve("en", false, []));
    await page.goto("http://magpie.test/?view=groups");
    await page.locator(".rt-goff").waitFor();
    const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l]?.[k]).map((k) => `${l}: ${k}`)), keys);
    assert.deepEqual(missing, []);
  });
}
