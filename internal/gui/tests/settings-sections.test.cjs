// Run with Node's test runner and Playwright on the module path; see README.md.
// The Settings page in parts (#471: one long scroll of nine sections and
// some 45 rows, the rarely used ones screens away): a tab list at the top —
// General, Usage, Network and sharing, Models, Privacy, Observability, Sync
// and backup, About — each tab showing its part's rows alone, so any section
// is one click away once Settings is open. The part picked is kept in the
// address (?view=settings&tab=…), so a reload lands on it, and remembered,
// so the window opened again on Settings does too; a link with a tab opens
// on it. The tabs are a tab list (role, aria-selected, one in the Tab
// order, the arrows, Home and End move along them); a click or a key on one
// never scrolls the page; a part's controls still post what they did. The
// tabs fit — on a line more when they must — in the smallest window (560 x
// 420) at 100, 110 and 125% text size and in magpie web on a phone, nothing
// running off to the side. English and Chinese; no backend, the API is
// faked here. ARTIFACT_DIR gets a screenshot of each size.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posts, { web = false, size = 100 } = {}) {
  let cur = settingsPayload({ lang, textSize: size });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},textSize:${size}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: size }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body };
      }
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const TABS = ["general", "usage", "network", "models", "privacy", "otel", "sync", "about"];
const L = {
  en: ["General", "Usage", "Network and sharing", "Models", "Privacy", "Observability", "Sync and backup", "About"],
  zh: ["常规", "用量", "网络与共享", "模型", "隐私", "可观测性", "同步与备份", "关于"],
};
const LABEL = { en: "Settings", zh: "设置" };
// what each part shows: an element of its own rows, drawn by the page
const MARK = {
  general: "#themeSegs .opt", usage: "#currencySegs .opt", network: "#proxySegs", models: "#imagesList .row",
  privacy: "#redactList .row", otel: "#otelList .row", sync: "#syncList .row", about: "#about .row",
};

// the tab picked, the parts shown, the tab focused, and the address's tab
async function shown(page) {
  return page.evaluate(() => {
    const on = [...document.querySelectorAll("#setTabs [role=tab]")].filter((b) => b.getAttribute("aria-selected") === "true").map((b) => b.dataset.set);
    const parts = [...document.querySelectorAll("#view-settings > [role=tabpanel]")].filter((p) => p.getClientRects().length).map((p) => p.id.replace("setPage-", ""));
    const q = new URLSearchParams(location.search);
    return { on, parts, focus: document.activeElement?.dataset?.set || "", url: q.get("view") + "/" + q.get("tab") };
  });
}
const top = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);
// what runs off to the side: the page, the view, a tab past the view's edge
const overflow = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector("#view-settings");
  if (de.scrollWidth > de.clientWidth) out.push("page " + de.scrollWidth + ">" + de.clientWidth);
  if (v.scrollWidth > v.clientWidth) out.push("view " + v.scrollWidth + ">" + v.clientWidth);
  const r = v.getBoundingClientRect();
  for (const b of document.querySelectorAll("#setTabs [role=tab]")) {
    const br = b.getBoundingClientRect();
    if (br.left < r.left - 0.5 || br.right > r.right + 0.5) out.push("tab " + b.textContent);
  }
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings in parts, a tab each", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-settings-sections-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, posts, { width = 560, height = 420, zoom = 1, url = "/", web, size, ctx } = {}) => {
      const context = ctx || await browser.newContext({ viewport: { width, height }, deviceScaleFactor: zoom, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", server(lang, posts, { web, size }));
      await page.goto("http://magpie.test" + url);
      return page;
    };

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": a tab per part, kept across a reload", async () => {
        const posts = [];
        const page = await open(lang, posts, { width: 900, height: 520 });
        await page.locator("#prefs").click();
        await page.locator(MARK.general).first().waitFor();

        // a tab list at the top of the page, before any part
        assert.equal(await page.locator("#setTabs").getAttribute("role"), "tablist");
        assert.equal(await page.locator("#setTabs").getAttribute("aria-label"), LABEL[lang]);
        assert.deepEqual((await page.locator("#setTabs [role=tab]:visible").allTextContents()).map((s) => s.trim()), L[lang]);
        assert.equal(await page.evaluate(() => document.querySelector("#view-settings").firstElementChild.contains(document.querySelector("#setTabs"))), true, "the tabs come first");
        assert.deepEqual(await shown(page), { on: ["general"], parts: ["general"], focus: "", url: "settings/general" });
        for (const id of TABS) {
          assert.equal(await page.locator("#setTab-" + id).getAttribute("aria-controls"), "setPage-" + id);
          assert.equal(await page.locator("#setPage-" + id).getAttribute("aria-labelledby"), "setTab-" + id);
        }
        // no coloured stripe down a tab's left side
        const stripes = await page.evaluate(() => [...document.querySelectorAll("#setTabs button, #view-settings .set-page")]
          .filter((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; }).length);
        assert.equal(stripes, 0, "no left-border accents");

        // each tab shows its part alone, posts nothing, and moves nothing
        const tabY = (id) => page.locator("#setTab-" + id).evaluate((b) => b.getBoundingClientRect().top);
        for (const id of TABS) {
          const y0 = await tabY(id), posted = posts.length;
          await page.locator("#setTab-" + id).click();
          await page.waitForTimeout(200);
          const s = await shown(page);
          assert.deepEqual([s.on, s.parts, s.url], [[id], [id], "settings/" + id], id);
          assert(await page.locator(MARK[id]).first().isVisible(), id + ": its rows shown");
          for (const other of TABS) if (other !== id) assert.equal(await page.locator(MARK[other]).first().isVisible(), false, id + ": " + other + "'s rows hidden");
          assert.equal(await top(page), 0, id + ": the click must not scroll the page");
          assert(Math.abs((await tabY(id)) - y0) <= 1, id + ": the tab stays where it was");
          assert.equal(posts.length, posted, id + ": a tab posts nothing");
        }
        // the warm-ups are in Usage, still a tab each
        await page.locator("#setTab-usage").click();
        assert(await page.locator("#warmTab-codex").isVisible(), "the warm-ups under Usage");

        // a part's control posts what it did
        await page.locator("#setTab-general").click();
        await page.locator("#themeSegs .opt").nth(2).click();
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(40);
        assert.equal(posts.at(-1)?.theme, "dark", "the theme still saves");
        await page.locator("#setTab-usage").click();
        await page.locator("#currencySegs .opt").nth(1).click();
        for (let i = 0; i < 50 && posts.at(-1)?.currency !== "cny"; i++) await page.waitForTimeout(40);
        assert.equal(posts.at(-1).currency, "cny", "the currency still saves");

        // the keyboard: one tab in the Tab order, the arrows, Home and End
        await page.locator("#setTab-privacy").click();
        assert.deepEqual(await page.locator("#setTabs [role=tab]").evaluateAll((bs) => bs.filter((b) => b.tabIndex === 0).map((b) => b.dataset.set)), ["privacy"]);
        await page.locator("#setTab-privacy").focus();
        await page.keyboard.press("ArrowRight");
        assert.deepEqual((await shown(page)).parts, ["otel"]);
        assert.equal((await shown(page)).focus, "otel");
        await page.keyboard.press("End");
        assert.deepEqual((await shown(page)).parts, ["about"]);
        await page.keyboard.press("ArrowRight");
        assert.deepEqual((await shown(page)).parts, ["general"]);
        await page.keyboard.press("ArrowLeft");
        assert.deepEqual((await shown(page)).parts, ["about"]);
        await page.keyboard.press("Home");
        assert.deepEqual((await shown(page)).parts, ["general"]);
        await page.keyboard.press("ArrowLeft");
        await page.keyboard.press("ArrowLeft");
        assert.deepEqual((await shown(page)).on, ["sync"]);
        assert.equal(await top(page), 0, "the keys move nothing");

        // a reload comes back to the part
        await page.reload();
        await page.locator(MARK.sync).first().waitFor();
        assert.deepEqual(await shown(page), { on: ["sync"], parts: ["sync"], focus: "", url: "settings/sync" });

        // another view leaves the tab out of the address; Settings again
        // brings it back
        await page.locator('#nav [data-view="usage"]').click();
        assert.equal(new URL(page.url()).searchParams.get("tab"), null);
        await page.locator("#prefs").click();
        assert.deepEqual((await shown(page)).url, "settings/sync");

        // the window opened again on Settings (no tab in the address): the
        // part last picked
        const again = await open(lang, [], { url: "/?view=settings", ctx: page.context() });
        await again.locator(MARK.sync).first().waitFor();
        assert.deepEqual((await shown(again)).parts, ["sync"]);
        // a link to a part opens on it, and it is remembered
        const link = await open(lang, [], { url: "/?view=settings&tab=otel", ctx: page.context() });
        await link.locator(MARK.otel).first().waitFor();
        assert.deepEqual((await shown(link)).parts, ["otel"]);
        // one not known: the part remembered
        const bad = await open(lang, [], { url: "/?view=settings&tab=nope", ctx: page.context() });
        await bad.locator(MARK.otel).first().waitFor();
        assert.deepEqual((await shown(bad)).parts, ["otel"]);
        assert.deepEqual([...page.errors, ...again.errors, ...link.errors, ...bad.errors], []);
      });

      await t.test(lang + ": the tabs fit the smallest window and a phone", async () => {
        const fits = async (page, what) => {
          await page.locator("#prefs").click();
          await page.locator(MARK.general).first().waitFor();
          assert.deepEqual(await overflow(page), [], what + ": nothing runs off to the side");
          assert.equal(await page.locator("#setTabs [role=tab]:visible").count(), TABS.length, what + ": every tab shown");
          // the tabs take a few lines at most, and the part starts under them
          const h = await page.locator("#setTabs").evaluate((e) => e.getBoundingClientRect().height);
          assert(h <= 3 * 26, what + `: the tabs ${h}px tall`);
          const under = await page.evaluate(() => document.querySelector("#setPage-general").getBoundingClientRect().top - document.querySelector("#setTabs").getBoundingClientRect().bottom);
          assert(under >= 0, what + ": the part under the tabs");
          // the last tab, on the last line, reached with one click, moving nothing
          await page.locator("#setTab-about").click();
          await page.waitForTimeout(150);
          assert.deepEqual((await shown(page)).parts, ["about"], what);
          assert.equal(await top(page), 0, what + ": the click must not scroll the page");
          assert.deepEqual(await overflow(page), [], what + ": About fits");
          shots.push([lang + "-" + what.replace(/\W+/g, "-"), await page.screenshot()]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        };
        for (const size of [100, 110, 125]) {
          const zoom = size / 100;
          await fits(await open(lang, [], { width: Math.floor(560 / zoom), height: Math.floor(420 / zoom), zoom, size }), size + "%");
        }
        await fits(await open(lang, [], { width: 375, height: 667, web: true }), "web on a phone");
      });
    }
  });
}
