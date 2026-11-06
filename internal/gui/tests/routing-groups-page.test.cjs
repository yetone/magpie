// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing groups have a page of their own beside Routing (11-06): below the
// stage and every request they were a long scroll down, New group with
// them. The sidebar names the page under Models, after Routing; its
// toolbar is the filter, how many groups there are and how many are on,
// then Templates, Import, Select and New group, the one filled button;
// the list follows, then Settings (found groups, the names in agents'
// lists). The Routing page keeps the stage and the requests, and a
// request's group opens the groups page on its row. Every language,
// Chromium and WebKit, wide, a narrow window and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/n", name: "n", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const groups = [
  { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  { id: "mine", name: "Mine", members: ["a/n", "b/m"], ready: true, memberInfo: info(["a/n", "b/m"]) },
  { id: "off", name: "Off one", members: ["a/n"], disabled: true, ready: true, memberInfo: info(["a/n"]) },
];

const words = {
  en: { nav: "Routing groups", count: "3 groups · 2 on", fresh: "New group", settings: "Settings" },
  zh: { nav: "路由组", count: "3 个组 · 2 个开启", fresh: "新建组", settings: "设置" },
  "zh-TW": { nav: "路由組", count: "3 個組 · 2 個開啟", fresh: "新增組", settings: "設定" },
  ja: { nav: "ルーティンググループ", count: "3 個のグループ · 2 個がオン", fresh: "新しいグループ", settings: "設定" },
  de: { nav: "Weiterleitungsgruppen", count: "3 Gruppen · 2 an", fresh: "Neue Gruppe", settings: "Einstellungen" },
};

// enough requests that the Routing page scrolls
const now = Date.now();
const routes = Array.from({ length: 30 }, (_, i) => ({
  id: "r" + i, seq: i + 1, at: new Date(now - i * 60000).toISOString(), agent: "claude", asked: "group/mine",
  model: "a/n", provider: "a", status: 200, ms: 900, ttft: 300, in: 100, out: 20, done: true,
  group: { id: "mine", name: "Mine" },
}));

function serve(lang, web) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: routes.length, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the page's boot under the whole suite's load (#3478)
const boot = 15000;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const { width, web } of [{ width: 1280, web: false }, { width: 640, web: false }, { width: 420, web: true }]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px${web ? " web" : " window"}: Routing groups is a page of its own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}${web ? "-web" : ""}-routing-groups-page.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-reqs > *").first().waitFor({ state: "attached", timeout: boot });
        await page.locator(".rt-group", { hasText: "Mine" }).waitFor({ state: "attached", timeout: boot });

        // the Routing page: the stage and the requests, no groups, no jump
        assert.equal(await page.locator("#view-routing .rt-gsec, #view-routing .rt-group").count(), 0, "no groups on the Routing page");
        assert.equal(await page.locator("#rtGoGroups, #rtNewGroup").count(), 0, "its head has no way down to them");
        assert.equal(await page.locator("#view-groups .rt-gsec").count(), 1, "the groups are on their own page");

        // the sidebar (or the header's tabs) names the page, after Routing
        const nav = page.locator('button[data-view="groups"]');
        assert.equal(await nav.count(), 1);
        assert.equal((await nav.textContent()).trim(), w.nav);
        assert.equal(await page.evaluate(() => {
          const r = document.querySelector('button[data-view="routing"]');
          return r.nextElementSibling === document.querySelector('button[data-view="groups"]');
        }), true, "right after Routing");

        // the sidebar has the tab; the header's row has no room for it (at
        // 860px it pushed the logo out), so there Routing's head opens the
        // page and Routing's tab stands for both
        const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
        const into = page.locator("#rtToGroups");
        assert.equal(side, width >= 1024, "the sidebar from 1024 points");
        assert.equal(await nav.isVisible(), side, "the tab where there is room");
        assert.equal(await into.isVisible(), !side, "Routing's head opens them where there is no tab");
        if (side) await nav.click();
        else {
          assert.equal((await into.textContent()).replace("›", "").trim(), w.nav);
          await into.click();
        }
        const view = page.locator("#view-groups");
        await view.waitFor();
        const on = await page.evaluate(() => [...document.querySelectorAll("#nav button[data-view].on")].map((b) => b.dataset.view));
        assert.deepEqual(on, [side ? "groups" : "routing"], "the tab shown as the page's");
        assert.equal(await page.locator("#view-routing").isHidden(), true);
        const rows = view.locator(".rt-group");
        await rows.first().waitFor({ timeout: boot });
        assert.equal(await rows.count(), 3);

        // the toolbar: the filter, how many, then New group, the filled one
        const head = view.locator(".rt-gsec > .row-head").first();
        assert.equal(await head.locator(".rt-gfilter").count(), 1, "the filter");
        assert.equal((await head.locator(".rt-gcount").textContent()).trim(), w.count);
        const fresh = head.locator(".rt-gnew");
        assert.equal((await fresh.textContent()).trim(), w.fresh);
        assert.equal(await fresh.evaluate((e) => e.classList.contains("primary")), true, "New group is the filled button");
        assert.equal(await head.locator(".rt-gimportbtn").count(), 1, "Import");
        assert.equal(await head.locator(".rt-gselect").count(), 1, "Select");
        assert.equal(await head.locator("select").count(), 0, "no native select");
        const fb = await fresh.boundingBox();
        assert(fb && fb.x >= 0 && fb.x + fb.width <= width && fb.y >= 0 && fb.y + fb.height <= 700, "New group in sight on arrival");
        // the list right under the toolbar, Settings under the list
        const order = await page.evaluate(() => {
          const s = document.querySelector("#view-groups .rt-gsec"), kids = [...s.children];
          const at = (q) => kids.findIndex((k) => k.matches(q));
          return [at(".row-head"), at(".rt-groups"), at(".rt-gset-head"), at(".rt-gset")];
        });
        assert.deepEqual(order, [0, 1, 3, 4], "toolbar, list, (off), Settings, its card");
        assert.equal((await view.locator(".rt-gset-head .label").textContent()).trim(), w.settings);
        assert.equal(await view.locator(".rt-gset .rt-gfound").count(), 1, "found groups in Settings");
        assert.equal(await view.locator(".rt-gset .rt-gnames").count(), 1, "the names in Settings");
        const gb = await view.locator(".rt-groups").boundingBox(), hb = await head.boundingBox();
        assert(gb.y >= hb.y + hb.height - 1 && gb.y - (hb.y + hb.height) < 24, "the list right below the toolbar");
        const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        assert(over <= 0, `no sideways scroll (${over}px)`);
        const headOver = await head.evaluate((h) => h.scrollWidth - h.clientWidth);
        assert(headOver <= 0, `nothing overflows the toolbar (${headOver}px)`);
        const border = await fresh.evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; });
        assert.equal(border, false, "no left-border accent");

        // New group opens its editor at the list's top, on this page
        await fresh.click();
        await view.locator(".rt-gedit").first().waitFor();
        assert.equal(await page.locator("#view-routing .rt-gedit").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// a request's group on the Routing page opens the groups page on its row
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a request's group goes to its row on the groups page`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1280, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", false));
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-group", { hasText: "Mine" }).waitFor({ state: "attached", timeout: boot });
    const go = page.locator('#view-routing .rt-go[data-go="group:mine"]').first();
    await go.waitFor({ timeout: boot });
    await go.click();
    await page.locator("#view-groups").waitFor();
    assert.equal(await page.locator("#view-routing").isHidden(), true, "Routing is left");
    const row = page.locator('#view-groups .rt-group[data-id="mine"]');
    await row.waitFor();
    const rb = await row.boundingBox();
    assert(rb.y >= 0 && rb.y + rb.height <= 700, "its row in sight");
    // the window opened on a new group of a model: the groups page
    await page.goto("http://magpie.test/?view=groups&newgroup=a%2Fn");
    await page.locator("#view-groups .rt-gedit").first().waitFor({ timeout: boot });
    assert.equal(await page.locator("#view-groups").isVisible(), true);
    assert.deepEqual(errors, []);
  });
}

// The groups page draws itself again each second when the accounts' quotas
// change, as the Routing page did under streaming requests (routing-steady
// and routing-pointer-pin, gone with the groups' move): the quota line
// comes in under a group by itself, and with a group open in its editor
// and a name being typed, the field keeps its focus and what was typed,
// and the list stays where it is.
const withProvider = { models, pools: [], deciders: [], found: true, groups: groups.map((g) => ({ ...g, memberInfo: g.memberInfo.map((i) => ({ ...i, provider: i.id.split("/")[0], name: i.id.split("/")[0].toUpperCase() })) })) };
// two accounts: the line is what they have left together
const card = (amount) => ["a1@example.com", "a2@example.com"].map((user) => ({ provider: "a", name: "A", kind: "subscription", icon: "generic", plan: "Pro", user, readAt: new Date().toISOString(),
  windows: [{ name: "Credits", amount, limit: 500, unit: "credits", used: (100 * amount) / 500, resetsAt: new Date(Date.now() + 9 * 864e5).toISOString() }] }));
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1280, 420]) {
    test(`${engine} ${width}px: the groups page draws new quotas by itself, an open editor left alone`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 500 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const base = serve("en", width < 600);
      await page.route("**/*", (r) => {
        const at = new URL(r.request().url()).pathname;
        if (at === "/api/groups") return r.fulfill({ json: withProvider });
        if (at === "/api/usage/quotas") return r.fulfill({ json: [] });
        return base(r);
      });
      await page.goto("http://magpie.test/?view=groups");
      const row = page.locator('#view-groups .rt-group[data-id="mine"]');
      await row.waitFor({ timeout: boot });
      await page.waitForFunction(() => Array.isArray(quotas), null, { timeout: boot });
      // quotas come in: the line shows with no click and no reload
      await page.evaluate((q) => { quotas = q; }, card(400));
      await row.locator(".rt-gquota").waitFor({ timeout: 5000 });

      await row.getByRole("button", { name: "Edit", exact: true }).click();
      const name = page.locator("#view-groups .rt-gedit input").first();
      await name.waitFor();
      // focus, not a click: at a phone's height the editor's Save bar can
      // sit over it, and what is asked here is the redraw, not the reach
      await name.focus();
      await page.keyboard.type("Opus and more");
      const typed = await name.inputValue();
      const top = async () => (await name.boundingBox()).y;
      const before = await top(), scrolled = await page.locator("#view-groups").evaluate((v) => v.scrollTop);
      // new quotas twice, each a tick's redraw were the editor not open
      for (const amount of [300, 200]) {
        await page.evaluate((q) => { quotas = q; }, card(amount));
        await page.waitForTimeout(1300);
      }
      assert.equal(await name.evaluate((e) => e === document.activeElement), true, "the field kept its focus");
      assert.equal(await name.inputValue(), typed);
      assert.equal(await page.locator("#view-groups").evaluate((v) => v.scrollTop), scrolled, "the view didn't scroll");
      assert(Math.abs((await top()) - before) < 1, "the field stayed where it was");
      await page.keyboard.type("!");
      assert.equal(await name.inputValue(), typed + "!");
      assert.deepEqual(errors, []);
    });
  }
}
