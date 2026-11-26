// Run with Node's test runner and Playwright on the module path; see README.md.
// The Agents and Plugins pages read themselves again without the refresh
// button (jimlee0824v2 on X: 「每次手動點刷新很麻煩」). The window back on
// Plugins (shown again, or focused) reads what's installed and its sign-ins
// again: a plugin installed from a terminal shows up, a subscription signed
// in elsewhere says so. At most every 15 s, and never while the reader is
// mid-way: a field being typed in, a menu open; the page doesn't scroll.
// Another page shown doesn't read the plugins. Agents opened from another
// page reads the agents again once their last read is 15 s old, not
// sooner. Chromium and WebKit, English, Chinese and Traditional Chinese;
// the API is faked here, and performance.now is moved on to pass the 15 s.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const AA = "opencode-aa-auth", NEW = "opencode-new-auth", THIRD = "opencode-third-auth", FOURTH = "opencode-fourth-auth";

function server(lang, ctl) {
  const entry = (spec, prov) => ({ spec, providers: [prov], version: "1.0.0", latest: "1.0.0", moved: [] });
  ctl.installed = [entry(AA, "AA One")];
  for (let i = 0; i < 20; i++) ctl.installed.push(entry("opencode-filler-" + String(i).padStart(2, "0"), "F" + i));
  // AA serves three subscriptions, none signed in
  ctl.subs = ["one", "two", "three"].map((n) => ({ id: "aa-" + n, pid: "aa-" + n, name: "AA " + n, icon: "generic", spec: AA, signedIn: false, models: 2, methods: [{ type: "api", label: "API key" }] }));
  ctl.seen = { plugins: 0, state: 0, npm: [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      if (url.searchParams.get("last") !== "1") ctl.seen.state++;
      return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: structuredClone(ctl.subs) });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/npm") { ctl.seen.npm.push(url.searchParams.get("names")); return json({ npm: {} }); }
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins") {
      ctl.seen.plugins++;
      return json({ bun: true, bunVersion: "1.3.0", plugins: structuredClone(ctl.installed), movable: [] });
    }
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const INSTALLED = { en: "Installed", zh: "已安装", "zh-TW": "已安裝" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Agents and Plugins pages read themselves again", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW"]) {
      await t.test(lang, async () => {
        const ctl = {};
        const page = await (await browser.newContext({ viewport: { width: 980, height: 560 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        // the 15 s between reads, passed without waiting them out
        await page.addInitScript(() => {
          const real = performance.now.bind(performance);
          let off = 0;
          performance.now = () => real() + off;
          window.__later = (ms) => { off += ms; };
        });
        await page.route("**/*", server(lang, ctl));
        const later = () => page.evaluate(() => window.__later(16e3));
        const back = () => page.evaluate(() => { document.dispatchEvent(new Event("visibilitychange")); window.dispatchEvent(new Event("focus")); });
        const settle = () => page.waitForTimeout(600);

        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: INSTALLED[lang] }).click();
        const row = (spec) => view.locator(".pm-row").filter({ has: page.locator(".name span", { hasText: new RegExp("^" + spec.replace(/[/@-]/g, "\\$&") + "$") }) });
        await row(AA).waitFor();
        await page.waitForFunction(() => document.querySelectorAll("#view-plugins .pm-row").length === 21);
        await settle();
        assert.equal(await row(AA).locator(".pm-sub.in").count(), 0);

        // the reader is down the list
        await view.evaluate((v) => { v.scrollTop = 240; });
        await page.waitForTimeout(100);
        const top = await view.evaluate((v) => v.scrollTop);
        assert(top > 0, "the view must have scrolled");

        // installed from a terminal, and signed in elsewhere, meanwhile
        ctl.installed.push({ spec: NEW, providers: ["New"], version: "1.0.0", latest: "1.0.0", moved: [] });
        ctl.subs[0].signedIn = true;
        const was = ctl.seen.plugins, npmWas = ctl.seen.npm.length;
        await later();
        await back();
        await row(NEW).waitFor();
        await row(AA).locator(".pm-sub.in").waitFor();
        assert.equal(ctl.seen.plugins, was + 1, "one read for visibilitychange and focus together");
        assert.equal(await view.evaluate((v) => v.scrollTop), top, "the read again must not scroll the page");
        // npm asked of the installed ones only, the server's hour-old answer
        assert.equal(ctl.seen.npm.length, npmWas + 1);
        assert.deepEqual(ctl.seen.npm.at(-1).split(",").sort(), ctl.installed.map((e) => e.spec).sort());

        // back again within 15 s: nothing read
        await back();
        await settle();
        assert.equal(ctl.seen.plugins, was + 1, "at most every 15 s");

        // a field being typed in is left alone
        ctl.installed.push({ spec: THIRD, providers: ["Third"], version: "1.0.0", latest: "1.0.0", moved: [] });
        await later();
        const find = view.locator(".pm-find input");
        await find.focus();
        await back();
        await settle();
        assert.equal(ctl.seen.plugins, was + 1, "not read under the reader's field");
        assert.equal(await row(THIRD).count(), 0);
        assert(await find.evaluate((f) => document.activeElement === f), "the field keeps its focus");
        await find.evaluate((f) => f.blur());
        await back();
        await row(THIRD).waitFor();
        assert.equal(ctl.seen.plugins, was + 2);

        // an open menu stays open, nothing read under it
        ctl.installed.push({ spec: FOURTH, providers: ["Fourth"], version: "1.0.0", latest: "1.0.0", moved: [] });
        await later();
        await row(AA).locator('button[aria-haspopup="menu"]').click();
        const menu = page.locator(".pop.proto-menu:not([hidden])");
        await menu.waitFor();
        const mark = await menu.evaluate((m) => { m.dataset.mark = "1"; return m.dataset.mark; });
        await back();
        await settle();
        assert.equal(ctl.seen.plugins, was + 2, "not read under an open menu");
        assert.equal(await menu.evaluate((m) => m.dataset.mark), mark, "the menu is the same one, still open");
        assert.equal(await row(FOURTH).count(), 0);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => !document.querySelector(".pop.proto-menu:not([hidden])"));

        // Agents: another page shown doesn't read the plugins
        await page.locator('#nav [data-view="agents"]').click();
        await page.locator("#view-agents").waitFor();
        const onAgents = ctl.seen.plugins;
        await later();
        await back();
        await settle();
        assert.equal(ctl.seen.plugins, onAgents, "Plugins not shown: not read");

        // Agents opened from another page reads the agents again once their
        // last read is 15 s old, not sooner
        await page.locator('#nav [data-view="plugins"]').click();
        await row(FOURTH).waitFor();
        let states = ctl.seen.state;
        await page.locator('#nav [data-view="agents"]').click();
        await settle();
        assert.equal(ctl.seen.state, states, "read under 15 s ago: not again");
        await page.locator('#nav [data-view="plugins"]').click();
        await later();
        states = ctl.seen.state;
        await page.locator('#nav [data-view="agents"]').click();
        for (let i = 0; i < 50 && ctl.seen.state === states; i++) await page.waitForTimeout(50);
        assert.equal(ctl.seen.state, states + 1, "Agents opened reads the agents again");
        assert.deepEqual(errors, []);
      });
    }
  });
}
