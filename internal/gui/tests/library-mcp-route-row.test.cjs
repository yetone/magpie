// Run with Node's test runner and Playwright on the module path; see README.md.
// MetalHqa on X: with the MCP router on, whether a server goes through it
// took opening the server's editor. Now every server's row says so while
// the router is on, and a click on it turns it the other way, saved as the
// editor's switch saves it (servers/router → ServerHub), without opening
// the editor. With the router off the rows say nothing of it. Every
// language, Chromium and WebKit, 1100 and 390 wide: the tag sits clear of
// its neighbours in the row, the cards keep their room, and nothing
// scrolls sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { reach } = require("./reader.cjs");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agents = [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, hub: "on" }];
const WORDS = ["Through the router", "Not through the router", "{name} goes through the MCP router", "{name} is given to each agent directly",
  "The agents reach it through the MCP router. Click to give each agent its own entry for it", "Each agent is given its own entry for it, as with the MCP router off"];

function server(lang, calls, hubOn) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents, mcpHub: hubOn, hubPath: "/mcp/magpie/hub/",
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      { name: "index", transport: "stdio", command: "/opt/index/mcp", agents: ["claude"] },
      { name: "a-server-with-a-rather-long-name-for-a-narrow-window", transport: "http", url: "https://example.com/mcp", agents: ["claude"], hub: "direct" },
      { name: "magpie-image", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "image"], agents: ["claude"] },
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/router") {
      const { name, on } = req.postDataJSON();
      calls.push(`router ${name} ${on}`);
      const s = lib.servers.find((x) => x.name === name);
      if (on) delete s.hub;
      else s.hub = "direct";
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
    }
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") calls.push(url.pathname);
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the tag's box against everything else drawn in its row, and the name it
// follows: nothing over it, and room to the name's text
const clear = (page) => page.evaluate(() => {
  const out = [];
  for (const b of document.querySelectorAll("#view-library .lib-server .lib-route")) {
    const r = b.getBoundingClientRect(), row = b.closest(".lib-server");
    const rr = row.getBoundingClientRect();
    if (r.left < rr.left || r.right > rr.right + 0.5) out.push(`${row.dataset.server}: out of its row`);
    for (const e of row.querySelectorAll(".lib-health, .lib-agents, .lib-switch, .lib-rowacts, .mark, .sub")) {
      if (e.contains(b)) continue;
      const o = e.getBoundingClientRect();
      if (!o.width || !o.height) continue;
      if (r.left < o.right - 0.5 && r.right > o.left + 0.5 && r.top < o.bottom - 0.5 && r.bottom > o.top + 0.5) out.push(`${row.dataset.server}: over ${e.className}`);
    }
    const nm = b.parentElement, text = document.createRange();
    text.selectNodeContents(nm.firstChild);
    const rects = [...text.getClientRects()], last = rects[rects.length - 1];
    if (last && Math.abs(last.top - r.top) < r.height && r.left - last.right < 4) out.push(`${row.dataset.server}: ${(r.left - last.right).toFixed(1)}px from its name`);
  }
  return out;
});
// boxed neighbours in the Library's body keep 10px between them (the
// owner, 11-23), as library-mcp-hub.test.cjs measures
const crowded = (page) => page.evaluate(() => {
  const boxed = (e) => { const c = getComputedStyle(e); return parseFloat(c.borderTopWidth) > 0 || c.backgroundColor !== "rgba(0, 0, 0, 0)"; };
  const kids = [...document.querySelectorAll("#view-library .lib-body:not(.lib-skel) > *")].filter((e) => e.getBoundingClientRect().height);
  const out = [];
  for (let i = 1; i < kids.length; i++) {
    if (!boxed(kids[i - 1]) || !boxed(kids[i])) continue;
    const gap = kids[i].getBoundingClientRect().top - kids[i - 1].getBoundingClientRect().bottom;
    if (gap < 10) out.push(`${kids[i - 1].className} | ${kids[i].className}: ${gap.toFixed(1)}px`);
  }
  return { n: kids.length, out };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server through the router or not, on its row", async (t) => {
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
          if (lang !== "en") for (const s of WORDS) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          for (const hubOn of [true, false]) {
            const calls = [];
            const ctx = await browser.newContext({ viewport: { width, height: 800 } });
            await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, calls, hubOn));
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            const rowOf = (name) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${name}"]`) });
            const row = rowOf("index");
            const route = (r = row) => r.locator(".lib-route");
            const ed = page.locator("#modal .lib-editor");
            await row.waitFor();

            if (!hubOn) {
              assert.equal(await page.locator("#view-library .lib-route").count(), 0, "the rows say nothing of a router that is off");
              await ctx.close();
              continue;
            }
            // every row says where it stands
            assert.equal(await page.locator("#view-library .lib-server .lib-route").count(), 3);
            const sw = route();
            assert.equal(await sw.getAttribute("role"), "switch");
            assert.equal(await sw.getAttribute("aria-checked"), "true");
            assert.equal((await sw.textContent()).trim(), w(WORDS[0]));
            assert.equal(await sw.getAttribute("title"), w(WORDS[4]));
            const long = route(rowOf("a-server-with-a-rather-long-name-for-a-narrow-window"));
            assert.equal(await long.getAttribute("aria-checked"), "false");
            assert.equal((await long.textContent()).trim(), w(WORDS[1]));
            assert.equal(await long.getAttribute("title"), w(WORDS[5]));
            assert.equal(await route(rowOf("magpie-image")).getAttribute("aria-checked"), "true");
            assert.deepEqual(await clear(page), [], "the tag clear of its neighbours");
            { const c = await crowded(page); assert.ok(c.n > 2, "the body is measured"); assert.deepEqual(c.out, [], "cards apart"); }

            // a click turns it, from the row: no editor opens
            await reach(page, sw);
            await sw.click();
            await page.locator("#view-library .lib-server .lib-route.lib-direct", { hasText: w(WORDS[1]) }).first().waitFor();
            await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-server .lib-route.lib-direct").length === 2);
            assert.deepEqual(calls.filter((c) => c.startsWith("router")), ["router index false"]);
            assert.equal(await ed.count(), 0, "the editor stayed shut");
            assert.equal(await route().getAttribute("aria-checked"), "false");
            assert.match(await page.locator("#status").textContent().catch(() => ""), new RegExp(w(WORDS[3], { name: "index" }).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
            // the editor's switch reads the same
            await reach(page, row.locator(".name"));
            await row.locator(".name").click({ position: { x: 4, y: 4 } });
            await ed.waitFor();
            assert.equal(await ed.getByRole("switch", { name: w("Through the MCP router"), exact: true }).getAttribute("aria-checked"), "false");
            await ed.locator(":scope > .bar button", { hasText: w("Cancel") }).click();
            await ed.waitFor({ state: "detached" });
            // and back
            await reach(page, route());
            await route().click();
            await page.waitForFunction(() => document.querySelector('#view-library .lib-server[data-server="index"] .lib-route')?.getAttribute("aria-checked") === "true");
            assert.deepEqual(calls.filter((c) => c.startsWith("router")), ["router index false", "router index true"]);
            assert.equal((await route().textContent()).trim(), w(WORDS[0]));
            assert.deepEqual(calls.filter((c) => !c.startsWith("router") && !c.endsWith("/mcp/check")), [], "nothing else saved");
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
            assert.deepEqual(await clear(page), [], "the tag clear of its neighbours, after");
            await ctx.close();
          }
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
