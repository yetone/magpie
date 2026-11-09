// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: 因为我们支持Agent插件了，这里是不是也应该显示插件中有更多？
// Install another agent ends with one row naming the agents a suggested
// plugin adds, not installed here, that opens Plugins at its Agents. The
// owner: 插件中还有更多，不应该折叠在安装更多Agent里. So the row is in
// sight with the commands folded, and the header counts the commands
// only. With no install command left, the row is there alone, no header;
// with no plugin agent left, there is no such row. In the window, wide and
// narrow, in every language, both engines. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_GUI_ASSETS || path.resolve(__dirname, "../assets");
const INSTALLS = [
  { id: "opencode", name: "OpenCode", icon: "opencode", commands: [{ via: "npm", command: "npm install -g opencode-ai" }] },
  { id: "crush", name: "Crush", icon: "crush", commands: [{ via: "brew", command: "brew install crush" }] },
];
const AGENTS = ["Kilo Code", "Aider", "PiG"];
// enough subscriptions above that Plugins' Agents start below the fold,
const listings = [
  ...Array.from({ length: 12 }, (_, i) => ({ package: `@magpie-community/opencode-p${i}-auth`, name: `Plan ${i}`, community: true, providers: ["p" + i], summary: { en: "Signs in" }, npm: { version: "0.1.0" } })),
  // and tools below, so Agents can come to the top
  ...Array.from({ length: 12 }, (_, i) => ({ package: `@magpie-community/tools-t${i}`, name: `Tools ${i}`, kind: "tools", community: true, summary: { en: "Tools" }, npm: { version: "0.1.0" } })),
  ...AGENTS.map((name) => ({ package: "@magpie-community/agent-" + name.toLowerCase().replace(/\W/g, ""), name, kind: "agent", community: true, summary: { en: "An agent" }, npm: { version: "0.1.0" } })),
];

function server(lang, ctl) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "pi", name: "Pi", path: "~/.pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/agents/install") return json(ctl.installs);
    if (url.pathname === "/api/plugins/agents") return json(ctl.agents);
    if (url.pathname === "/api/plugins") return json({ bun: true, plugins: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the language's own words for an English key, failing for one it hasn't
const said = (page, lang, key, vars = {}) => page.evaluate(([lang, key, vars]) => {
  const s = lang === "en" ? key : I18N[lang]?.[key];
  if (!s) return null;
  return s.replace(/\{(\w+)\}/g, (_, k) => vars[k]);
}, [lang, key, vars]);
const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Install another agent says Plugins has more agents, and opens them", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const ctl = { installs: INSTALLS, agents: AGENTS };
        const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, ctl));
        await page.goto("http://magpie.test/");
        const inst = page.locator("#agentsInstall");
        const head = inst.locator(".ag-install-head");
        const count = (n) => said(page, lang, "Install another agent ({n})", { n });
        await page.waitForFunction((want) => document.querySelector("#agentsInstall .ag-install-head")?.textContent === want, await count(2));
        await page.waitForFunction(() => document.querySelector("#agentsInstall .ag-install-plugins"));
        // folded, the plugins' row is in sight under the header
        assert.equal(await head.getAttribute("aria-expanded"), "false");
        assert.equal(await inst.locator(".ag-install-row").count(), 1, "folded, only the plugins' row");
        assert(await inst.locator(".ag-install-plugins").isVisible(), "the plugins' row is folded away");
        const gap = await inst.evaluate((s) => s.querySelector(".ag-install-plugins").getBoundingClientRect().top - s.querySelector(".ag-install-head").getBoundingClientRect().bottom);
        assert(gap >= 4 && gap <= 10, "the plugins' row sits " + gap + "px under the header");
        await head.click();
        await inst.locator(".ag-install-note").waitFor();
        // the commands first, then one row for the plugins' agents
        const rows = inst.locator(".ag-install-row");
        assert.equal(await rows.count(), 3);
        assert.equal(await inst.locator(".ag-install-row:last-child.ag-install-plugins").count(), 1, "the plugins' row isn't last");
        const more = inst.locator(".ag-install-plugins");
        const word = await said(page, lang, "More in Plugins");
        assert(word, "More in Plugins has no " + lang);
        assert.equal(await more.locator("b").textContent(), word);
        assert.equal(await more.locator(".ag-install-pnames").textContent(), AGENTS.join(await said(page, lang, ", ")));
        const tip = await said(page, lang, "Agents magpie has no setup of its own for: install a plugin for one in the Plugins tab, and it is listed here");
        assert(tip, "the tip has no " + lang);
        assert.equal(await more.getAttribute("title"), tip);
        // on one line, its words inside it
        const box = (l) => l.evaluate((e) => { const r = e.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom }; });
        const r = await box(more), n = await box(more.locator("b")), c = await box(more.locator(".chev"));
        assert(c.r <= r.r + 0.5 && n.l >= r.l, "off its row");
        assert(c.t < n.b && c.b > n.t, "the chevron is not on the name's line");
        assert(await more.locator("b").evaluate((b) => b.scrollWidth <= b.clientWidth + 1), "the name cut");
        assert(await noSideScroll(page), "the page scrolls sideways");
        await page.screenshot({ path: path.join(process.env.SHOTS || "/tmp", `plugin-agents-${engine}-${lang}-${width}.png`), fullPage: true }).catch(() => {});

        // to Plugins, Discover, Agents alone, in sight at the top
        await more.click();
        const view = page.locator("#view-plugins");
        await view.waitFor();
        const agents = view.locator(".pm-sec").filter({ has: page.locator(".pm-sechead h3", { hasText: new RegExp("^" + await said(page, lang, "Agents") + "$") }) });
        await agents.waitFor();
        await page.waitForFunction(() => {
          const v = document.querySelector("#view-plugins");
          const s = [...v.querySelectorAll(".pm-sec")].find((x) => x.querySelector(".pm-card .pm-chip.kind.ag"));
          return s && Math.abs(s.getBoundingClientRect().top - v.getBoundingClientRect().top) < 80;
        });
        assert.equal(await agents.locator(".pm-card").count(), AGENTS.length);
        // it stays there once the page has what it loads
        await page.waitForTimeout(300);
        const top = await agents.evaluate((s) => s.getBoundingClientRect().top - document.querySelector("#view-plugins").getBoundingClientRect().top);
        assert(Math.abs(top) < 80, "Agents moved away: " + top);
        await page.context().close();

        // no install command left: the section is there for the plugins' agents
        ctl.installs = [];
        const p2 = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
        p2.setDefaultTimeout(5000);
        p2.setDefaultNavigationTimeout(60000);
        p2.on("pageerror", (e) => errors.push(e.message));
        await p2.route("**/*", server(lang, ctl));
        await p2.goto("http://magpie.test/");
        await p2.locator("#agentsInstall .ag-install-plugins").waitFor();
        assert.equal(await p2.locator("#agentsInstall .ag-install-head").count(), 0, "a header over no commands");
        assert.equal(await p2.locator("#agentsInstall .ag-install-row").count(), 1);
        assert.equal(await p2.locator("#agentsInstall .ag-install-note").count(), 0, "a note about commands with none");
        await p2.context().close();

        // no plugin agent left: no such row, the count the commands'
        Object.assign(ctl, { installs: INSTALLS, agents: [] });
        const p3 = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
        p3.setDefaultTimeout(5000);
        p3.setDefaultNavigationTimeout(60000);
        p3.on("pageerror", (e) => errors.push(e.message));
        await p3.route("**/*", server(lang, ctl));
        await p3.goto("http://magpie.test/");
        await p3.waitForFunction((want) => document.querySelector("#agentsInstall .ag-install-head")?.textContent === want, await said(p3, lang, "Install another agent ({n})", { n: 2 }));
        await p3.locator("#agentsInstall .ag-install-head").click();
        assert.equal(await p3.locator("#agentsInstall .ag-install-row").count(), 2);
        assert.equal(await p3.locator("#agentsInstall .ag-install-plugins").count(), 0);
        await p3.context().close();
        assert.deepEqual(errors, []);
      });
    }
  });
}
