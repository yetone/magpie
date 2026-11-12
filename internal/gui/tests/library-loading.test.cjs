// Run with Node's test runner and Playwright on the module path; see README.md.
// The Library while it loads: the real tabs where they'll stay, the tab
// last open picked, and the rows of that tab in outline, which come in only
// after a moment; a tab picked while waiting shows its own outline and is
// the one open once the library comes; the Library folder waits for it. When
// it comes the outline goes, the tabs get their counts and don't move. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("pi", "Pi")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
  skills: [{ name: "grilling", description: "Grill a plan", kind: "folder", agents: ["codex"], source: `${HOME}/skills/grilling` }],
  servers: [{ name: "fs", transport: "stdio", command: "npx", args: ["fs-mcp"], agents: ["codex"] }],
};

function server(gate) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang: "en", theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") { await gate; return route.fulfill({ json: lib }); }
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
  test(engine + ": the Library while it loads", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    // under the sidebar's width: the tabs here are the page's own strip
    const ctx = await browser.newContext({ viewport: { width: 860, height: 800 } });
    await ctx.addInitScript(() => { try { if (!sessionStorage.getItem("seeded")) { localStorage.setItem("magpie.libTab", "skills"); sessionStorage.setItem("seeded", "1"); } } catch {} });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let open;
    await page.route("**/*", server(new Promise((r) => { open = r; })));
    await page.goto("http://magpie.test/");
    // how the outline looks the moment it's put in the page
    await page.evaluate(() => {
      new MutationObserver((_, mo) => {
        const s = document.querySelector("#view-library .lib-skel");
        if (s) { window.firstOpacity = getComputedStyle(s).opacity; mo.disconnect(); }
      }).observe(document.querySelector("#view-library"), { childList: true, subtree: true });
    });
    await page.locator('button[data-view="library"]').click();

    const v = page.locator("#view-library");
    const skel = v.locator(".lib-skel");
    await skel.waitFor({ state: "attached" });
    // not seen at once: a quick load goes straight to the page
    assert.equal(await page.evaluate(() => window.firstOpacity), "0", "the outline comes in after a moment");
    await page.waitForFunction(() => getComputedStyle(document.querySelector("#view-library .lib-skel")).opacity === "1");
    assert.equal(await skel.getAttribute("aria-busy"), "true");

    const tabs = v.locator(".lib-head .lib-tabs .opt");
    assert.deepEqual(await tabs.allTextContents(), ["Instructions", "Memory", "MCP servers", "Skills", "Hooks", "Environment variables", "Subagents", "Claude plugins", "RTK", "billion-context"]);
    assert.equal(await v.locator(".lib-tabs .opt.on").textContent(), "Skills");
    assert.ok(await v.locator(".lib-head .lib-more").isDisabled(), "the folder waits for the library");
    // Skills in outline: rows with a switch, no section heading
    assert.equal(await skel.locator(".row-head").count(), 0);
    assert.ok(await skel.locator(".lib-sk-switch").count() >= 3);

    // another tab picked while waiting: its own outline, and it stays picked
    await tabs.filter({ hasText: "Instructions" }).click();
    assert.equal(await v.locator(".lib-tabs .opt.on").textContent(), "Instructions");
    assert.equal(await v.locator(".lib-skel .row-head").count(), 1, "Instructions' outline has the Agents heading");
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.libTab")), "instructions");

    const before = await v.locator(".lib-tabs").boundingBox();
    open();
    await v.locator(".lib-head .lib-more:not([disabled])").waitFor();
    assert.equal(await v.locator(".lib-skel").count(), 0, "the outline goes");
    assert.deepEqual(await v.locator(".lib-tabs .opt").allTextContents(), ["Instructions", "Memory", "MCP servers · 1", "Skills · 1", "Hooks", "Environment variables", "Subagents", "Claude plugins", "RTK", "billion-context", "Pi packages"]);
    assert.equal(await v.locator(".lib-tabs .opt.on").textContent(), "Instructions");
    assert.equal(await v.locator(".lib-head .lib-more").getAttribute("title"), "~/.magpie/library");
    const after = await v.locator(".lib-tabs").boundingBox();
    assert.deepEqual([after.x, after.y], [before.x, before.y], "the tabs don't move");
    await v.getByText("Write it once", { exact: false }).waitFor();
    assert.deepEqual(errors, []);
  });
}

// The page comes from a glance first, without what reading the agents'
// skill folders through takes, which with many big skills took minutes
// (#541): the Skills tab is drawn at once and says it is still looking,
// and the agents' own skills come in when the whole read does.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [980, 420]) {
    test(`${engine} ${width}px: the Library drawn from a glance first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let open;
      const whole = new Promise((r) => { open = r; });
      const full = { ...lib, foundSkills: [{ name: "deck", description: "Slides", agents: ["pi"] }] };
      const reads = [];
      const rest = server(Promise.resolve());
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        if (url.pathname === "/api/library") {
          const glance = url.searchParams.get("glance") === "1";
          reads.push(glance ? "glance" : "read");
          if (glance) return route.fulfill({ json: { ...lib, partial: true } });
          await whole;
          return route.fulfill({ json: full });
        }
        return rest(route);
      });
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      const v = page.locator("#view-library");
      await v.locator(".lib-head .lib-more:not([disabled])").waitFor();
      assert.equal(await v.locator(".lib-skel").count(), 0, "drawn from the glance, no outline");
      const wait = v.locator(".lib-found-wait");
      await wait.waitFor();
      assert.equal(await wait.getAttribute("aria-busy"), "true");
      assert.match(await wait.textContent(), /In your agents.*Looking through the agents' skill folders…/);
      await v.getByText("grilling", { exact: true }).first().waitFor();
      const box = await wait.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= width, "the row fits");
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll");

      open();
      await v.getByText("deck", { exact: true }).first().waitFor();
      assert.equal(await v.locator(".lib-found-wait").count(), 0, "the wait goes");
      assert.deepEqual(reads, ["glance", "read"]);
      assert.deepEqual(errors, []);
    });
  }
}
