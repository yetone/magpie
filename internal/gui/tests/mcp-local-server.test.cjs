// Run with Node's test runner and Playwright on the module path; see README.md.
// huhuy72219405 on X ("能不能把不同设备的mcp区分一下，有些mcp属于本地的，同步之后
// 其他设备不应该读取到"): an MCP server can be kept to this computer. Its
// editor has one switch, "Sync to your other computers", on by default;
// off saves the server as local and shows again off; the row says "Not
// synced" with a tag. It said "This computer only", and bin.90 on Discord
// read that as a sharing switch: the stored local keeps its meaning. A
// Remote magpie's server (bridge) holds this computer's key and is never
// synced: its switch is off and held, and the hint and the tag say why,
// where it showed on and went back off once saved (treeman_26897 on
// Discord: "无法打开 同步到其它电脑按钮"). Every language,
// Chromium and WebKit, 1100 and 390 wide; nothing scrolls sideways. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agents = [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }];

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents,
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "mine", transport: "stdio", command: "/opt/mine/mcp", agents: ["claude"] },
      { name: "home", transport: "http", url: "http://10.0.0.5:3425/mcp", headers: { Authorization: "Bearer sk-x" }, agents: ["claude"], local: true, bridge: "remote-magpie" }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(`${s.name} ${s.local}`);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      if (!s.local) delete lib.servers[i].local;
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server kept to this computer", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s, v) => tr(lang, s, v);
          if (lang !== "en") for (const s of ["Sync to your other computers", "Not synced", "Added from a Remote magpie with this computer's key for it, so it stays on this computer", "Sync never carries it to your other computers", "Off keeps it on this computer: sync never carries it to your other computers, and a sync from them neither removes it nor puts theirs in its place"]) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          const calls = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, calls));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const row = page.locator("#view-library .lib-server", { has: page.locator('.lib-health[data-server="mine"]') });
          const ed = page.locator("#modal .lib-editor");
          const sw = ed.getByRole("switch", { name: w("Sync to your other computers"), exact: true });
          const tag = row.locator(".lib-tag.lib-local");
          const open = async () => { await row.locator(".name").click(); await ed.waitFor(); };
          const save = async () => { await ed.locator(":scope > .bar button.primary").click(); await ed.waitFor({ state: "detached" }); };

          await row.waitFor();
          assert.equal(await tag.count(), 0, "a shared server has no tag");
          // on for a server synced as before; the hint says what off does
          await open();
          assert.equal(await sw.getAttribute("aria-checked"), "true");
          await ed.locator(".hint", { hasText: w("Off keeps it on this computer: sync never carries it to your other computers, and a sync from them neither removes it nor puts theirs in its place") }).waitFor();
          await sw.click();
          assert.equal(await sw.getAttribute("aria-checked"), "false");
          await save();
          assert.deepEqual(calls, ["mine true"]);
          // the row says so, and the editor shows it on again
          await tag.waitFor();
          assert.equal((await tag.textContent()).trim(), w("Not synced"));
          await open();
          assert.equal(await sw.getAttribute("aria-checked"), "false");
          // on again, saved as synced
          await sw.click();
          await save();
          assert.deepEqual(calls, ["mine true", "mine false"]);
          await tag.waitFor({ state: "detached" });
          // a Remote magpie's server: off and held, the hint and tag say why
          const brow = page.locator("#view-library .lib-server", { has: page.locator('.lib-health[data-server="home"]') });
          const btag = brow.locator(".lib-tag.lib-local");
          assert.equal(await btag.getAttribute("title"), w("Added from a Remote magpie with this computer's key for it, so it stays on this computer"));
          await brow.locator(".name").click();
          await ed.waitFor();
          assert.equal(await sw.getAttribute("aria-checked"), "false");
          assert.equal(await sw.getAttribute("aria-disabled"), "true");
          await ed.locator(".hint", { hasText: w("Added from a Remote magpie with this computer's key for it, so it stays on this computer") }).waitFor();
          await sw.dispatchEvent("click"); // Playwright won't click an aria-disabled one
          assert.equal(await sw.getAttribute("aria-checked"), "false", "a click doesn't turn it on");
          await save();
          assert.deepEqual(calls, ["mine true", "mine false", "home true"]);
          // Cancel keeps it as it was
          await open();
          await sw.click();
          await ed.locator(":scope > .bar button", { hasText: w("Cancel") }).click();
          assert.deepEqual(calls, ["mine true", "mine false", "home true"]);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
