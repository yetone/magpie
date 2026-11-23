// Run with Node's test runner and Playwright on the module path; see README.md.
// swang20201 on X (v0.1.3375, Docker gateway mode): an installed MCP server
// in Library could only be removed. Each server row has a switch: off
// posts servers/on {on:false}, says the settings are kept, and the row
// shows dimmed with its agent chips out of reach, under the fold of the
// ones turned off; on posts {on:true} and
// the row is as it was. The switch doesn't open the editor. In the app
// with an agent at 1100px and a browser in gateway mode at 420px, every
// language, both engines: no sideways scroll, no native select.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
// the router row is over the list, so a row may be under the fold: the
// reader wheels to it (the app puts Playwright's own scroll back)
const { reach } = require("./reader.cjs");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/srv/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: st.gateway, hubPath: "/mcp/magpie/hub/",
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(st.gateway ? { hub: "on" } : {}) },
    ...(st.align ? [{ id: "codex", name: "Codex", icon: "openai", mcp: `${HOME}/.codex/config.toml` }, { id: "cursor", name: "Cursor", icon: "cursor", gone: true }] : [])],
  instructions: { agents: [], sets: [] }, foundSkills: [], skills: [], problems: [], foundServers: [], projects: [],
  servers: [
    st.off
      ? { name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], env: { TOKEN: "x" }, agents: [], hidden: true, paused: ["claude"] }
      : { name: st.align ? "magpie-memory" : "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], env: { TOKEN: "x" }, agents: st.memoryAgents || ["claude"] },
    { name: "docs", transport: "http", url: "https://docs.example/mcp", agents: st.align ? ["claude", "cursor"] : ["claude"] },
  ],
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.gateway}${st.gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/servers/agents") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.memoryAgents = body.agents;
      return json({ ...fixture(st), result: { changed: [] } });
    }
    if (url.pathname === "/api/library/servers/on") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.off = !body.on;
      return json({ ...fixture(st), result: { changed: [] } });
    }
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names;
      st.checked.push(...names);
      return json({ servers: Object.fromEntries(names.map((n) => [n, { state: "ok", tools: 2 }])) });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-server, #view-library .lib-server > .lib-switch, #view-library .lib-server .lib-agents")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: MCP All and switches align across different agent counts`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["zh", "en", "zh-TW", "ja", "de"]) await t.test(lang, async (subtest) => {
      const st = { align: true, sent: [], checked: [] };
      const context = await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
      subtest.after(() => context.close());
      await context.addInitScript(() => localStorage.setItem("magpie.libTab", "mcp"));
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", server(lang, st));
      await page.goto("http://magpie.test/?view=library");
      const memory = page.locator('.lib-server[data-server="magpie-memory"]');
      const docs = page.locator('.lib-server[data-server="docs"]');
      await memory.waitFor();
      assert.equal(await memory.locator("[data-agent]").count(), 2);
      assert.equal(await docs.locator("[data-agent]").count(), 3);
      const aligned = async (selector) => {
        const layout = await page.waitForFunction((control) => {
          const positions = ["docs", "magpie-memory"].map((name) => document.querySelector(`.lib-server[data-server="${name}"] ${control}`)?.getBoundingClientRect().left);
          return positions.every((left) => left > 0) ? positions : null;
        }, selector);
        const [docsLeft, memoryLeft] = await layout.jsonValue();
        await layout.dispose();
        assert(Math.abs(memoryLeft - docsLeft) < 1, `${selector} shifts with agent count (${memoryLeft} vs ${docsLeft})`);
      };
      await aligned(".lib-switch");
      await aligned(".lib-ag.all");
      const codex = memory.locator('[data-agent="codex"]');
      const all = memory.locator(".lib-ag.all");
      await reach(page, all);
      await all.hover();
      await codex.hover();
      await aligned(".lib-ag.all");
      await codex.click();
      const allOff = page.waitForResponse((response) => response.url().endsWith("/servers/agents") && response.request().postDataJSON().agents.length === 0);
      await all.click();
      await allOff;
      assert.deepEqual(st.sent, [{ name: "magpie-memory", agents: ["claude", "codex"] }, { name: "magpie-memory", agents: [] }]);
      await page.setViewportSize({ width: 420, height: 900 });
      assert.deepEqual(await sideways(page), []);
    });
  });

  test(engine + ": an MCP server switched off and on", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const gateway = width < 800;
        const where = gateway ? "gateway mode" : "the app";
        const st = { off: false, sent: [], checked: [], gateway };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = v.locator('.lib-server[data-server="notes"]');
        await row.waitFor();
        const sw = row.locator(":scope > .lib-switch");
        assert.equal(await sw.count(), 1, `${where}: no switch on the row`);
        assert.equal(await sw.getAttribute("aria-checked"), "true");
        assert.equal(await sw.getAttribute("aria-label"), w("Turn {name} on or off", { name: "notes" }));
        assert.equal(await row.evaluate((r) => r.classList.contains("off")), false);
        assert.equal(await v.locator('.lib-server[data-server="docs"] > .lib-switch').getAttribute("aria-checked"), "true");
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, on`);

        // off: kept, dimmed, the editor not opened
        await reach(page, sw);
        await sw.click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{name} is off; its settings are kept", { name: "notes" }));
        assert.deepEqual(st.sent, [{ name: "notes", on: false }]);
        await page.waitForFunction(() => document.querySelector('#view-library .lib-server[data-server="notes"]')?.classList.contains("off"));
        // turned off, it is under the fold below the ones on, folded at first
        // (library-mcp-off-fold): opened to see it
        assert.equal(await v.locator(".lib-offhead .fold").getAttribute("aria-expanded"), "false");
        await v.locator(".lib-offhead .fold").click();
        const offRow = v.locator('.lib-server[data-server="notes"]');
        assert.equal(await offRow.locator(":scope > .lib-switch").getAttribute("aria-checked"), "false");
        assert.equal(await page.locator("#modal").evaluate((m) => !m.hidden && m.getBoundingClientRect().height > 0).catch(() => false), false, `${where}: the switch opened the editor`);
        if (!gateway) assert.equal(await offRow.locator(".lib-agents").evaluate((e) => getComputedStyle(e).pointerEvents), "none", "the chips of a server off still take clicks");
        assert.ok(Number(await offRow.locator(".name").evaluate((e) => getComputedStyle(e).opacity)) < 1, `${where}: not dimmed`);
        assert.equal(await v.locator('.lib-server[data-server="docs"]').evaluate((r) => r.classList.contains("off")), false);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, off`);

        // on again, as it was
        await reach(page, offRow.locator(":scope > .lib-switch"));
        await offRow.locator(":scope > .lib-switch").click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{name} is on again", { name: "notes" }));
        assert.deepEqual(st.sent, [{ name: "notes", on: false }, { name: "notes", on: true }]);
        await page.waitForFunction(() => !document.querySelector('#view-library .lib-server[data-server="notes"]')?.classList.contains("off"));
        assert.equal(await v.locator('.lib-server[data-server="notes"] > .lib-switch').getAttribute("aria-checked"), "true");

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
