// Run with Node's test runner and Playwright on the module path; see README.md.
// The installed lists are sorted as the reader picks (#481): the Library's
// MCP servers and the installed plugins by name A→Z (the plugins no longer
// in the order they were installed) or Z→A, and the Library's skills by
// where they came from, as before, or as one flat list by name, A→Z or
// Z→A. Each list keeps its own pick through a reload, and a pick leaves
// the control where it was on the screen. English and Chinese; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const servers = ["beta", "Alpha", "gamma"].map((name) => ({ name, transport: "stdio", command: "npx", args: [name], agents: ["claude"] }));
const gh = (repo, name) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/skills/${name}`, description: "d", agents: ["claude"] });
const skills = [
  gh("zed/tools", "apple"), gh("zed/tools", "pear"),
  gh("acme/kit", "mango"), gh("acme/kit", "cherry"),
  { name: "kiwi", kind: "folder", source: `${HOME}/skills/kiwi`, description: "d", agents: ["claude"] },
];
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code", "claudecode-color")],
  instructions: { agents: [], sets: [] }, servers: structuredClone(servers), foundServers: [], projects: [], foundSkills: [], skills: structuredClone(skills), problems: [],
});
// installed in this order: the market names one, a folder and a package
const plugins = [
  { spec: "opencode-zz-sub@1.0.0", version: "1.0.0", providers: [] },
  { spec: `${HOME}/plugins/opencode-mine`, version: "", providers: [] },
  { spec: "opencode-aa@2.0.0", version: "2.0.0", providers: [] },
];
const listings = [{ package: "opencode-zz-sub", name: "Bee Plan", summary: { en: "s" } }];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings, state: { bun: true, bunVersion: "1.3.0", plugins, picker: false } });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { source: "By source", az: "Names from A to Z", za: "Names from Z to A" },
  zh: { source: "按来源", az: "按名称 A→Z", za: "按名称 Z→A" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the installed lists, sorted as picked", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { if (!localStorage.getItem("magpie.libTab")) localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));

        // the control stays where it was on the screen while the list is drawn again
        const pick = async (sort, id) => {
          const box = page.locator(`.segs.sortby[data-sort="${sort}"]`);
          const opt = box.locator(".opt").nth(id);
          await page.waitForTimeout(400);
          const before = await box.evaluate((b) => [b.getBoundingClientRect().top, b.closest(".view").scrollTop]);
          await opt.click();
          await page.waitForTimeout(400);
          const after = await box.evaluate((b) => [b.getBoundingClientRect().top, b.closest(".view").scrollTop]);
          assert.deepEqual(after, before, sort + ": the click moved the page");
          assert.equal(await opt.evaluate((o) => o.classList.contains("on")), true);
        };
        const names = (sel) => page.locator(sel).evaluateAll((ns) => ns.map((n) => n.textContent.trim()));
        const tips = (sort) => page.locator(`.segs.sortby[data-sort="${sort}"] .opt`).evaluateAll((os) => os.map((o) => o.title));

        // the Library's MCP servers: A→Z first, whatever order magpie gave them in
        const serverNames = () => names("#view-library .lib-list .lib-row > .who > .name");
        const openLib = async () => {
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-row").first().waitFor();
        };
        await openLib();
        assert.deepEqual(await serverNames(), ["Alpha", "beta", "gamma"]);
        assert.deepEqual(await tips("libServers"), [w.az, w.za]);
        await pick("libServers", 1);
        assert.deepEqual(await serverNames(), ["gamma", "beta", "Alpha"]);

        // the skills: by source at first, then one flat list by name
        await page.locator("#view-library .lib-tabs .opt").nth(2).click();
        await page.locator("#view-library .lib-group").first().waitFor();
        const skillBox = page.locator('.segs.sortby[data-sort="libSkills"]');
        assert.deepEqual(await skillBox.locator(".opt").allTextContents(), [w.source, "A→Z", "Z→A"]);
        assert.equal(await skillBox.locator(".opt.on").textContent(), w.source);
        assert.deepEqual(await names("#view-library .lib-grouphead > .who > .name"), ["acme/kit", "zed/tools", lang === "zh" ? "本机" : "On this computer"]);
        const skillNames = () => names("#view-library .lib-groups .lib-row:not(.lib-grouphead):not(.lib-subhead) > .who > .name");
        await pick("libSkills", 1);
        assert.equal(await page.locator("#view-library .lib-group").count(), 0, "A→Z is one flat list");
        assert.deepEqual(await skillNames(), ["apple", "cherry", "kiwi", "mango", "pear"]);
        await pick("libSkills", 2);
        assert.deepEqual(await skillNames(), ["pear", "mango", "kiwi", "cherry", "apple"]);

        // each list keeps its own pick through a reload
        await openLib();
        assert.deepEqual(await skillNames(), ["pear", "mango", "kiwi", "cherry", "apple"]);
        await page.locator("#view-library .lib-tabs .opt").nth(1).click();
        await page.locator("#view-library .lib-list .lib-row").first().waitFor();
        assert.deepEqual(await serverNames(), ["gamma", "beta", "Alpha"]);
        await page.locator("#view-library .lib-tabs .opt").nth(2).click();
        await pick("libSkills", 0);
        await page.locator("#view-library .lib-group").first().waitFor();

        // the plugins: by name, not in the order they were installed in
        const pluginNames = () => names("#view-plugins .pm-list .pm-row > .who > .name > span:first-child");
        const openPlugins = async () => {
          await page.goto("http://magpie.test/?view=plugins");
          await page.locator("#view-plugins .lib-tabs .opt").nth(1).click();
          await page.locator("#view-plugins .pm-row").first().waitFor();
        };
        await openPlugins();
        assert.deepEqual(await pluginNames(), ["Bee Plan", "opencode-aa", "opencode-mine"]);
        await pick("plugins", 1);
        assert.deepEqual(await pluginNames(), ["opencode-mine", "opencode-aa", "Bee Plan"]);
        await openPlugins();
        assert.deepEqual(await pluginNames(), ["opencode-mine", "opencode-aa", "Bee Plan"]);
        await openLib();
        await page.locator("#view-library .lib-group").first().waitFor();

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
