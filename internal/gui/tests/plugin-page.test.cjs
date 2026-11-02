// Run with Node's test runner and Playwright on the module path; see README.md.
// A community plugin's own page in the window (pluginpages.js, P4): the
// example plugin (testdata/page-plugin) gets a tab in the sidebar's
// Plugins group and draws in a frame sandboxed to scripts, with the
// window's theme, its own options and the read it declared. The frame
// can't reach magpie's API: its policy stops the fetch, and with the
// policy gone the request it would send carries Origin: null, which
// apiFramed turns away (TestPluginPageNoAPI). A read it didn't declare is
// refused, and a message from another frame is nothing. A page that
// throws shows "This page stopped working" with Try again. The Plugins
// page says what a page may read before it is on. Every language, both
// engines, a narrow window. The API is faked here; the server's side is
// pluginpage_test.go.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const example = path.resolve(__dirname, "../testdata/page-plugin/page");
const bridge = path.resolve(__dirname, "../pagebridge.js");

const GLANCE = "a".repeat(32), PROBE = "b".repeat(32), OPEN = "c".repeat(32), BROKEN = "d".repeat(32);
const entry = (tok, name, title, reads, file = "index.html") => ({
  spec: "/x/" + name, name, key: "plugin-" + name, title, reads,
  url: `/_magpie/plugin/${tok}/${file}`, icon: name === "glance" ? `/_magpie/plugin/${tok}/icon.svg` : "",
});
// the example's title by language (magpie.page.titles); none in German,
// which reads its title
const TITLES = { zh: "用量速览", "zh-TW": "用量速覽", ja: "使用量の概要" };
const pages = [
  { ...entry(GLANCE, "glance", "Usage glance", ["usage:summary"]), titles: TITLES },
  // a page with a local service (magpie.page.service), as /api/plugin-pages
  // lists it to a browser: relative to the page's address
  { ...entry(PROBE, "probe", "Probe", []), service: "_magpie/service/" },
  entry(OPEN, "open", "Probe without a policy", []),
  entry(BROKEN, "broken", "Broken", []),
];

// what pluginpage.go's pagePolicy sends a browser, for this host: the
// page's folder under https and http (ownSources, #4198)
const policy = (tok) => {
  const dir = `magpie.test/_magpie/plugin/${tok}/`;
  const own = `https://${dir} http://${dir}`;
  return [
    "sandbox allow-scripts", "default-src 'none'", `script-src ${own}`, `style-src ${own} 'unsafe-inline'`,
    `img-src ${own} data:`, `font-src ${own}`, `media-src ${own}`, "connect-src 'none'", "frame-src 'none'",
    "worker-src 'none'", "object-src 'none'", "form-action 'none'", "base-uri 'none'", "frame-ancestors 'self'",
  ].join("; ");
};

// a page that tries every way out it has, and tells the window what came
// of each (its message has no magpie:1, so the window's bridge ignores it)
const PROBE_HTML = `<!doctype html><meta charset="utf-8"><script src="_magpie/bridge.js"></script><script src="probe.js"></script><style>.own { border-radius: 2px; }</style><h1>probe</h1><p id="p">probe</p><button id="b">b</button><button class="own">o</button>`;
const PROBE_JS = `(async () => {
  const out = {};
  await magpie.ready;
  out.service = magpie.service;
  try { const r = await fetch("/api/state?probe=1"); out.fetch = "status " + r.status; } catch (e) { out.fetch = "refused"; }
  try { out.storage = typeof localStorage.length; } catch (e) { out.storage = "refused"; }
  try { out.cookie = document.cookie; } catch (e) { out.cookie = "refused"; }
  try { out.top = String(parent.document.title); } catch (e) { out.top = "refused"; }
  try { await magpie.read("usage:summary"); out.read = "given"; } catch (e) { out.read = e.message; }
  try { await magpie.read("providers:list"); out.read2 = "given"; } catch (e) { out.read2 = e.message; }
  // what the page left unstyled is magpie's; what it styled is its own
  const cs = (q) => getComputedStyle(document.querySelector(q));
  out.look = { bg: cs("html").backgroundColor, fg: cs("body").color, font: cs("body").fontFamily, h1: cs("h1").fontSize, button: cs("#b").borderRadius, own: cs(".own").borderRadius };
  parent.postMessage({ probe: out }, "*");
})();`;
const BROKEN_HTML = `<!doctype html><meta charset="utf-8"><script src="_magpie/bridge.js"></script><script src="app.js"></script><p id="ok">drawn</p>`;

function server(lang, seen, plugins) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.searchParams.get("probe")) {
      seen.api.push(await req.allHeaders());
      return route.fulfill({ status: 403, body: "" });
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers" && seen.slow) await new Promise((r) => setTimeout(r, seen.slow));
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugin-pages") return json(seen.pages ? seen.pages() : pages);
    if (url.pathname === "/api/plugins/off" && seen.off) return json(seen.off(req.postDataJSON()));
    if (url.pathname === "/api/plugin-pages/read") {
      const b = req.postDataJSON();
      seen.reads.push(b);
      return json({ period: "today", calls: 12, errors: 0, input: 1000, output: 234, cost: 0.5, models: [] });
    }
    if (url.pathname === "/api/plugin-pages/options") {
      const b = req.postDataJSON();
      if (b.options) { seen.options.push(b); seen.saved = b.options; }
      return json(seen.saved || {});
    }
    if (url.pathname === "/api/plugins") return json(plugins);
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: plugins });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const m = url.pathname.match(/^\/_magpie\/plugin\/([0-9a-f]{32})\/(.+)$/);
    if (m) {
      const [, tok, rel] = m;
      const headers = { "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-cache" };
      if (tok !== OPEN) headers["Content-Security-Policy"] = policy(tok);
      const type = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(rel)];
      let body;
      if (rel === "_magpie/bridge.js") body = await fs.readFile(bridge);
      else if (tok === GLANCE) body = await fs.readFile(path.join(example, rel)).catch(() => null);
      else if ((tok === PROBE || tok === OPEN) && rel === "index.html") body = PROBE_HTML;
      else if ((tok === PROBE || tok === OPEN) && rel === "probe.js") body = PROBE_JS;
      else if (tok === BROKEN && rel === "index.html") body = BROKEN_HTML;
      else if (tok === BROKEN && rel === "app.js") body = seen.broken ? "throw new Error('fixture: the page broke')" : "1";
      if (body == null) return route.fulfill({ status: 404, body: "" });
      return route.fulfill({ status: 200, headers: { ...headers, "Content-Type": type }, body });
    }
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const plugins = { bun: true, bunVersion: "1.3.0", plugins: [
  { spec: "/x/magpie-usage-glance", providers: [], moved: [], inMagpieOnly: true, page: { title: "Usage glance", reads: ["usage:summary", "providers:list"] } },
  { spec: "/x/quiet-page", off: true, providers: [], moved: [], inMagpieOnly: true, page: { title: "Quiet", reads: [] } },
] };

const frameOf = (page, tok) => page.frames().find((f) => f.url().includes(tok));
async function frameReady(page, tok) {
  await page.waitForFunction((tok) => [...document.querySelectorAll("iframe.plugin-frame")].some((f) => f.src.includes(tok)), tok);
  for (let i = 0; i < 100 && !frameOf(page, tok); i++) await page.waitForTimeout(50);
  return frameOf(page, tok);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin's page draws in its frame, shut in`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        const where = `${engine} ${lang} ${width}`;
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const seen = { api: [], reads: [], options: [] };
        await page.route("**/*", server(lang, seen, plugins));
        await page.goto("http://magpie.test/");

        // the sidebar's Plugins group, after the built-in pages
        const head = page.locator("#nav .nav-gl[data-plugin-pages]");
        await head.waitFor({ state: "attached" });
        assert.equal(await head.textContent(), await page.evaluate(() => t("Plugins")), where);
        const tabs = page.locator("#nav button[data-plugin-page]");
        assert.equal(await tabs.count(), 4, where);
        assert.equal(await page.evaluate(() => { const h = document.querySelector("#nav .nav-gl[data-plugin-pages]"); return [...h.parentElement.children].indexOf(h) > [...h.parentElement.querySelectorAll("button[data-view='plugins']")].map((b) => [...b.parentElement.children].indexOf(b))[0]; }), true, `${where}: after the built-in pages`);
        assert.equal(await tabs.first().locator("img.nav-ic").getAttribute("src"), `/_magpie/plugin/${GLANCE}/icon.svg`, where);
        // titled in the window's language, else by its title
        assert.equal(await tabs.first().locator("span").textContent(), TITLES[lang] || "Usage glance", `${where}: the tab's title`);
        if (lang === "de") {
          // a new language retitles the tab, and back
          await page.evaluate(() => setLocale("ja"));
          await page.waitForFunction(() => document.querySelector("#nav button[data-plugin-page] span")?.textContent === "使用量の概要");
          await page.evaluate(() => setLocale("de"));
          await page.waitForFunction(() => document.querySelector("#nav button[data-plugin-page] span")?.textContent === "Usage glance");
        }
        // no frame until the page is looked at
        assert.equal(await page.locator("iframe.plugin-frame").count(), 0, `${where}: nothing loaded unseen`);

        // the example: its declared read, the theme, its options
        await page.evaluate(() => show("plugin-glance"));
        const glance = await frameReady(page, GLANCE);
        assert.equal(await page.locator("#view-plugin-glance iframe").getAttribute("sandbox"), "allow-scripts", where);
        await glance.waitForFunction(() => document.getElementById("cost")?.textContent === "$0.50");
        assert.equal(await glance.locator("#calls").textContent(), "12 calls, 1234 tokens", where);
        assert.deepEqual(seen.reads.map((r) => [r.spec, r.read]), [["/x/glance", "usage:summary"]], where);
        assert.equal(await glance.evaluate(() => document.documentElement.lang), lang, `${where}: the window's language`);
        assert.equal(await glance.evaluate(() => document.documentElement.dataset.theme), "light", where);
        await page.evaluate(() => { document.documentElement.dataset.theme = "dark"; });
        await glance.waitForFunction(() => document.documentElement.dataset.theme === "dark");
        assert.ok((await glance.evaluate(() => document.documentElement.style.getPropertyValue("--magpie-bg"))).length > 0, `${where}: the tokens`);
        await glance.locator("#hide").click();
        await glance.waitForFunction(() => document.getElementById("cost")?.textContent === "");
        assert.deepEqual(seen.options.map((o) => [o.spec, o.options.hideCost]), [["/x/glance", true]], `${where}: its own options`);
        // the frame fits the window
        assert.ok(await page.evaluate(() => { const v = document.querySelector("#view-plugin-glance"); const f = v.querySelector("iframe"); return f.getBoundingClientRect().width <= v.clientWidth + 1 && v.scrollWidth <= v.clientWidth + 1 && f.getBoundingClientRect().height > 200; }), `${where}: fits`);

        if (lang === "en" && width === 1100) {
          // every way out of the frame is shut; a read it didn't declare
          // is refused before it reaches the server
          const got = page.evaluate(() => new Promise((res) => addEventListener("message", (e) => { if (e.data?.probe) res(e.data.probe); })));
          await page.evaluate(() => show("plugin-probe"));
          const probe = await got;
          assert.equal(probe.fetch, "refused", `${where}: the policy stops a fetch of /api`);
          assert.equal(probe.storage, "refused", `${where}: no storage`);
          assert.equal(probe.cookie, "refused", `${where}: no cookie`);
          assert.equal(probe.top, "refused", `${where}: no reach into the window`);
          assert.equal(probe.read, "Probe doesn't ask to read usage:summary", where);
          assert.equal(probe.read2, "Probe doesn't ask to read providers:list", where);
          assert.equal(seen.api.length, 0, `${where}: nothing reached /api`);
          assert.equal(seen.reads.length, 1, `${where}: an undeclared read never reached the server`);
          // its service, where the window's server serves it: an absolute
          // address under the page's own
          assert.match(probe.service, new RegExp(`^https?://[^/]+/_magpie/plugin/${PROBE}/_magpie/service/$`), `${where}: magpie.service`);
          // a page with no stylesheet of its own reads as part of the window
          const win = await page.evaluate(() => {
            const d = document.createElement("div");
            d.style.cssText = "background: var(--bg); color: var(--fg)";
            document.body.append(d);
            const c = getComputedStyle(d);
            const r = { bg: c.backgroundColor, fg: c.color };
            d.remove();
            return r;
          });
          assert.equal(probe.look.bg, win.bg, `${where}: the window's background`);
          assert.equal(probe.look.fg, win.fg, `${where}: the window's text colour`);
          assert.match(probe.look.font, /apple-system|SF Pro|system-ui/, `${where}: the window's font, not a serif`);
          assert.equal(probe.look.h1, "20px", `${where}: magpie's heading`);
          assert.equal(probe.look.button, "6px", `${where}: magpie's button`);
          assert.equal(probe.look.own, "2px", `${where}: the page's own rule wins`);

          // with the policy gone, what the frame's fetch would send is what
          // apiFramed turns away
          const got2 = page.evaluate(() => new Promise((res) => addEventListener("message", (e) => { if (e.data?.probe) res(e.data.probe); })));
          await page.evaluate(() => show("plugin-open"));
          const open = await got2;
          assert.equal(open.fetch.startsWith("status") || open.fetch === "refused", true, where);
          assert.equal(seen.api.length, 1, `${where}: the fetch went out`);
          assert.equal(seen.api[0].origin, "null", `${where}: as Origin: null`);
          assert.equal(open.service, "", `${where}: a page with no service has no magpie.service`);
          assert.ok(!seen.api[0].cookie, `${where}: without a cookie`);

          // a message from another frame naming a page is nothing
          const before = seen.reads.length;
          await page.evaluate(() => {
            const f = document.createElement("iframe");
            f.srcdoc = `<script>parent.postMessage({magpie:1,id:1,method:"read",params:{name:"usage:summary"}},"*")<\/script>`;
            document.body.append(f);
          });
          await page.waitForTimeout(300);
          assert.equal(seen.reads.length, before, `${where}: another frame's read is not answered`);

          // a page that throws shows the kernel's broken state, and Try again
          // draws it once its code works
          seen.broken = true;
          await page.evaluate(() => show("plugin-broken"));
          const card = page.locator("#view-plugin-broken .slot-error");
          await card.waitFor();
          assert.equal(await card.locator(".name").textContent(), "This page stopped working", where);
          // the frame's own script's error is muted to it ("Script error."):
          // the window says the page hit one
          assert.equal(await card.locator(".sub").textContent(), "The page hit an error", where);
          seen.broken = false;
          await card.locator(".slot-again").click();
          const fixed = await frameReady(page, BROKEN);
          await fixed.locator("#ok").waitFor();
          assert.equal(await card.count(), 0, `${where}: the card gone`);
        }
        // the broken page's own throw, in its frame, is the only error
        assert.deepEqual(errors.filter((e) => e !== "fixture: the page broke"), [], where);
        await context.close();
      }
    }
  });

  // a page plugin switched on gets its tab at once, not when the Plugins
  // page is next opened (the owner: it took a long while to show), and
  // switched off loses it; the providers' answer, slow here, isn't
  // waited for
  test(`${engine}: a page plugin's tab comes and goes with it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const where = `${engine} ${lang}`;
      const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const mine = structuredClone(plugins);
      const quiet = mine.plugins.find((e) => e.spec === "/x/quiet-page");
      const seen = { api: [], reads: [], options: [] };
      seen.pages = () => quiet.off ? [] : [entry(PROBE, "quiet-page", "Quiet", [])];
      seen.off = (b) => { quiet.off = b.off; seen.slow = 4000; return mine; };
      await page.route("**/*", server(lang, seen, mine));
      await page.goto("http://magpie.test/?view=plugins");
      const view = page.locator("#view-plugins");
      const installed = await page.evaluate(() => t("Installed"));
      await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: installed }).first().click();
      const row = view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: "quiet-page" }) });
      const tab = page.locator('#nav button[data-plugin-page][data-view="plugin-quiet-page"]');
      const tr = (k) => page.evaluate((k) => t(k), k);
      await row.waitFor();
      assert.equal(await tab.count(), 0, where);
      await row.getByRole("button", { name: await tr("Switch on"), exact: true }).click();
      await tab.waitFor({ timeout: 2000 });
      assert.ok(await view.isVisible(), `${where}: the page stays`);
      await page.waitForTimeout(4200);
      seen.slow = 0;
      await row.getByRole("button", { name: await tr("Switch off"), exact: true }).click();
      await tab.waitFor({ state: "detached", timeout: 2000 });
      assert.deepEqual(errors, [], where);
      await page.context().close();
    }
  });

  test(`${engine}: Plugins says what a page may read before it is on`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [980, 420]) {
        const where = `${engine} ${lang} ${width}`;
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, { api: [], reads: [], options: [] }, plugins));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const installed = await page.evaluate(() => t("Installed"));
        await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: installed }).first().click();
        const row = (name) => view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: name }) });
        const glance = row("magpie-usage-glance");
        await glance.waitFor();
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        assert.deepEqual((await glance.locator(".pm-chip.kind").allInnerTexts()).map((x) => x.trim()), [await tr("Page")], where);
        const reads = [await tr("your usage totals, by model"), await tr("your providers' names")].join(await tr(", "));
        assert.equal((await glance.locator(".pm-page").innerText()).trim(), await tr("Adds a page to the sidebar, which can read {reads}", { reads }), where);
        if (lang !== "en") assert.ok(!(await glance.locator(".pm-page").innerText()).includes("Adds a page"), `${where}: translated`);
        // one switched off still says it
        const quiet = row("quiet-page");
        assert.equal((await quiet.locator(".pm-page").innerText()).trim(), await tr("Adds a page to the sidebar, which reads nothing of magpie's"), where);
        assert.ok(await view.evaluate((v) => v.scrollWidth <= v.clientWidth + 1), `${where}: fits`);
        // nothing drawn with a coloured left border
        const borders = await view.locator(".pm-row *").evaluateAll((els) => els.filter((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > 0 && s.borderLeftStyle !== "none" && parseFloat(s.borderRightWidth) === 0; }).length);
        assert.equal(borders, 0, where);
        assert.deepEqual(errors, [], where);
        await page.context().close();
      }
    }
  });
}
