// Run with Node's test runner and Playwright on the module path; see README.md.
// Discover's Partners (yetone: 在插件页也增加一个赞助位…有些合作方不是做 LLM
// provider 的): the sponsors whose product is a plugin, first on the page
// under the add sheet's heading and its Become a sponsor. Who they are
// comes from usemagpie.ai through /api/providers' pluginPartners
// (provider.PluginPartners); one that names its languages is listed in
// those only, and its tagline is its own in the page's language, else its
// English one, on a quiet second line. Each row has the plugin's one
// button (Install, Installed), its site through usemagpie.ai/go, and opens
// the plugin's page. Counted as the add sheet's: shown once a showing,
// opened when opened; an install magpie counts itself (provider.AddPlugin),
// and the page's own openings are counted (provider.CountPage),
// so the page says nothing of it. A kind picked lists the partners of that
// kind, a source picked none, a search those it matches; with no partner
// there is no section. Every language, both engines, 980px and 420px; the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const pluginPartners = [
  { id: "notes", name: "Notes Sync", plugin: "@notes/magpie-sync", icon: "generic",
    notes: { en: "Your notes, in every agent", zh: "笔记进每个 agent", ja: "どのエージェントにもノートを" },
    website: "https://usemagpie.ai/go/notes/site", siteHost: "notes.example" },
  { id: "lens", name: "Lens", plugin: "lens-plugin", icon: "generic", notes: { en: "See every request" } },
  { id: "zhonly", name: "China Only", plugin: "zh-plugin", icon: "generic", langs: ["zh"], notes: { en: "Mainland tools", zh: "国内工具" } },
  { id: "have", name: "Have It", plugin: "have-plugin", icon: "generic" },
];
const listings = [
  { package: "@notes/magpie-sync", name: "Notes Sync", kind: "tools", summary: { en: "Notes for agents" }, npm: { version: "1.0.0", weekly: 10 } },
  { package: "agent-plugin", name: "Some Agent", kind: "agent", summary: { en: "An agent" }, npm: { version: "1.0.0", weekly: 20 } },
  { package: "have-plugin", name: "Have It", kind: "middleware", summary: { en: "A middleware" }, npm: { version: "1.0.0", weekly: 5 } },
];
const npm = Object.fromEntries(listings.map((l) => [l.package, l.npm]));

function server(lang, opts) {
  const state = { bun: true, bunVersion: "1.3.0", plugins: [{ spec: "have-plugin", version: "1.0.0", providers: [], moved: [] }] };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/api/partner") { opts.counted.push(req.postDataJSON()); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/page") { opts.pages.push(req.postDataJSON()); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/open") { opts.opened.push(req.postDataJSON()?.url); return json({}); }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], ...(opts.partners.length && { pluginPartners: opts.partners }) });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/github") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/add") {
      const spec = req.postDataJSON()?.spec;
      opts.added.push(spec);
      state.plugins.push({ spec, version: "0.1.0", providers: [], moved: [] });
      return json(state);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the rows each language lists, and Notes Sync's and Lens's taglines there
const L = {
  en: { rows: ["Notes Sync", "Lens", "Have It"], notes: ["Your notes, in every agent", "See every request"] },
  zh: { rows: ["Notes Sync", "Lens", "China Only", "Have It"], notes: ["笔记进每个 agent", "See every request", "国内工具"] },
  "zh-TW": { rows: ["Notes Sync", "Lens", "China Only", "Have It"], notes: ["笔记进每个 agent", "See every request", "国内工具"] },
  ja: { rows: ["Notes Sync", "Lens", "Have It"], notes: ["どのエージェントにもノートを", "See every request"] },
  de: { rows: ["Notes Sync", "Lens", "Have It"], notes: ["Your notes, in every agent", "See every request"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Discover lists the plugins' partners first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const opts = { partners: pluginPartners, counted: [], opened: [], added: [], pages: [] };
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, opts));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const sec = view.locator(".pm-partners");
        // npm's answers in, so the buttons say what they will
        await view.locator('.pm-card[data-pkg="agent-plugin"] .pm-act.get').waitFor();
        await sec.waitFor();
        const tr = (k) => page.evaluate((k) => t(k), k);

        // first in Discover, under the add sheet's heading and its way to join
        assert.equal(await view.locator(".pm-body > *").first().evaluate((e) => e.classList.contains("pm-partners")), true, "first");
        assert.equal(await sec.locator(".pm-sechead h3").textContent(), await tr("Partners"));
        assert.equal((await sec.locator(".sponsor-me").textContent()).trim(), await tr("Become a sponsor"));

        // its rows: in the page's language only, the tagline on a second line
        const rows = sec.locator(".pm-prow");
        assert.deepEqual(await rows.locator(".pm-rwho b").allTextContents(), w.rows);
        assert.deepEqual(await rows.locator(".pm-rwho span").allTextContents(), w.notes);
        const look = await rows.evaluateAll((rs) => rs.map((r) => ({ over: r.scrollWidth > r.clientWidth + 1, h: r.getBoundingClientRect().height, right: r.getBoundingClientRect().right })));
        for (const l of look) {
          assert.equal(l.over, false, "row overflows");
          assert(l.h <= 48, "row " + l.h + "px tall");
          assert(l.right <= width, "row past the window");
        }
        assert(await view.evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "page scrolls sideways");
        // each its plugin's one button
        const row = (name) => rows.filter({ has: page.locator(".pm-rwho b", { hasText: name }) });
        assert.equal((await row("Lens").locator(".pm-act").textContent()).trim(), await tr("Install"));
        assert.equal((await row("Have It").locator(".pm-act").textContent()).trim(), await tr("Installed"));

        // counted shown once, however often the page is drawn
        const shown = () => opts.counted.filter((c) => c.what === "shown");
        for (let i = 0; i < 80 && !shown().length; i++) await page.waitForTimeout(25);
        assert.deepEqual(shown(), [{ what: "shown", ids: pluginPartners.filter((p) => w.rows.includes(p.name)).map((p) => p.id) }]);

        // a kind picked: the partners of that kind; another: none
        const kinds = view.locator(".pm-kinds .opt");
        await kinds.filter({ hasText: await tr("Tools") }).click();
        assert.deepEqual(await rows.locator(".pm-rwho b").allTextContents(), ["Notes Sync"]);
        await kinds.filter({ hasText: await tr("Agents") }).click();
        await view.locator(".pm-sec[data-kind=ag]").waitFor();
        assert.equal(await sec.count(), 0, "a kind no partner is");
        await kinds.first().click();
        await sec.waitFor();
        assert.equal(shown().length, 1, "counted again on a redraw");
        // the page's opening counted once (provider.CountPage), not each redraw
        assert.deepEqual(opts.pages, [{ page: "plugins" }]);
        // an origin picked: none
        await view.locator(".pm-bar .pm-pop").first().click();
        await page.locator(".proto-menu .pm-item", { hasText: await tr("Other authors") }).click();
        await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
        assert.equal(await sec.count(), 0, "an origin picked");
        await view.locator(".pm-bar .pm-pop").first().click();
        await page.locator(".proto-menu .pm-item").first().click();
        await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
        await sec.waitFor();
        // Not installed only: a partner whose plugin is installed goes
        const only = async () => {
          await view.locator(".pm-bar .pm-pop").first().click();
          await page.locator(".proto-menu .pm-item", { hasText: await tr("Not installed only") }).click();
          await page.locator(".proto-menu").waitFor({ state: "hidden" }).catch(() => {});
        };
        await only();
        await page.waitForFunction(() => ![...document.querySelectorAll("#view-plugins .pm-prow b")].some((b) => b.textContent === "Have It"));
        assert.deepEqual(await rows.locator(".pm-rwho b").allTextContents(), w.rows.filter((n) => n !== "Have It"));
        await only();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-plugins .pm-prow b")].some((b) => b.textContent === "Have It"));

        // a search: the partners it matches, by tagline too
        const q = view.locator(".pm-find input");
        await q.fill(w.notes[0].split(/[ ,]/)[0].slice(0, 6));
        await page.waitForFunction(() => document.querySelectorAll("#view-plugins .pm-partners .pm-prow").length === 1);
        assert.deepEqual(await rows.locator(".pm-rwho b").allTextContents(), ["Notes Sync"]);
        await q.fill("zzqq-nothing");
        await view.locator(".pm-sec").first().waitFor();
        assert.equal(await sec.count(), 0, "a search no partner matches");
        await q.fill("");
        await sec.waitFor();

        // its site, through usemagpie.ai/go, opens no page of its own
        await row("Notes Sync").locator(".pm-psite").click();
        assert.equal(await row("Notes Sync").locator(".pm-psite").getAttribute("title"), "notes.example");
        for (let i = 0; i < 80 && !opts.opened.length; i++) await page.waitForTimeout(25);
        assert.deepEqual(opts.opened, ["https://usemagpie.ai/go/notes/site"]);
        assert.equal(await page.locator(".pm-detail").count(), 0);

        // Install installs it, and the page counts nothing for it
        await row("Lens").locator(".pm-act").click();
        const installed = await tr("Installed");
        await page.waitForFunction(([n, s]) => [...document.querySelectorAll("#view-plugins .pm-prow")].some((r) => r.querySelector(".pm-rwho b").textContent === n && r.querySelector(".pm-act").textContent.trim() === s), ["Lens", installed]);
        assert.deepEqual(opts.added, ["lens-plugin"]);
        assert.equal(opts.counted.filter((c) => c.what === "added").length, 0);

        // the row opens the plugin's page, counted
        await row("Notes Sync").locator(".pm-rwho b").click();
        await page.locator(".pm-detail").waitFor();
        assert.deepEqual(opts.counted.filter((c) => c.what === "opened"), [{ what: "opened", ids: ["notes"] }]);
        // the page shown again: counted shown again, once
        await page.evaluate(() => window.loadPlugins());
        for (let i = 0; i < 80 && shown().length < 2; i++) await page.waitForTimeout(25);
        assert.equal(shown().length, 2, "a new showing counts again");
        assert.deepEqual(shown()[1], shown()[0]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });

  test(engine + ": with no partner there is no section", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const opts = { partners: [], counted: [], opened: [], added: [], pages: [] };
      const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, opts));
      await page.goto("http://magpie.test/?view=plugins");
      const view = page.locator("#view-plugins");
      await view.locator('.pm-card[data-pkg="agent-plugin"] .pm-act.get').waitFor();
      assert.equal(await view.locator(".pm-partners").count(), 0);
      assert.deepEqual(opts.counted, []);
      assert.deepEqual(errors, []);
      await page.context().close();
    }
  });
}
