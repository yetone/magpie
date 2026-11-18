// Run with Node's test runner and Playwright on the module path; see README.md.
// The MCP market by real use (gugediyige666 on X: rank the servers by
// installs): with nothing searched, a server that USED (20) computers'
// libraries had in the last week goes first, most used first, and the rest
// keep the market's own order, featured first. Each card with users says
// how many. Fewer than that, or no counts at all, keeps the curated order;
// a search keeps the registry's ranking and still shows the counts. Every
// language, a narrow window, both engines. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const servers = [
  { id: "context7", name: "context7", title: "Context7", featured: true, transport: "http", runs: "remote", inputs: [] },
  { id: "playwright", name: "playwright", title: "Playwright", featured: true, transport: "stdio", runs: "npx", inputs: [] },
  ...Array.from({ length: 12 }, (_, i) => ({ id: `io.github.o${i}/m${i}`, name: `m${i}`, title: `Server ${i}`, description: `Server number ${i}.`, transport: "stdio", runs: "npx", inputs: [] })),
];
const note = {
  en: ["Most used first, then featured and the MCP Registry", "Featured, and the official MCP Registry"],
  zh: ["最多人用的在前，然后是精选和 MCP Registry", "精选，以及官方 MCP Registry"],
  "zh-TW": ["最多人用的在前，然後是精選和 MCP Registry", "精選，以及官方 MCP Registry"],
  ja: ["利用者の多い順、続いておすすめと MCP Registry", "おすすめと公式 MCP Registry"],
  de: ["Die meistgenutzten zuerst, dann Empfohlene und die MCP Registry", "Empfohlen und die offizielle MCP Registry"],
};
const people = { en: "1,200 people use it, counted from the last week", zh: "1,200 人在用（近 7 天）", ja: "1,200 人が利用中（直近 7 日）" };

function serve(lang, counts, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/counts") {
      asked.push(1);
      return counts ? json({ plugins: {}, servers: counts, liked: [] }) : route.fulfill({ status: 500, json: { error: "offline" } });
    }
    if (url.pathname === "/api/library") {
      return json({
        dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
        agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
        instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
      });
    }
    if (url.pathname === "/api/library/market/servers") {
      const q = url.searchParams.get("q") || "";
      // a search: the registry's own order, the last one first
      return json({ items: q ? structuredClone(servers).filter((x) => x.name.startsWith(q)).reverse() : structuredClone(servers) });
    }
    if (url.pathname === "/api/library/icon") return route.fulfill({ status: 404, body: "" });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const cases = {
  used: { "io.github.o5/m5": { users: 50 }, "io.github.o9/m9": { users: 1200 }, playwright: { users: 3 } },
  thin: { "io.github.o5/m5": { users: 19 }, playwright: { users: 3 } },
  none: null,
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const [name, counts] of Object.entries(cases)) {
      test(`${engine} ${lang} ${name}: the MCP market goes by real use where there is enough of it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const asked = [];
        await page.route("**/*", serve(lang, counts, asked));
        await page.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        t.after(() => browser.close());
        await page.goto("http://magpie.test/?view=library");
        const box = page.locator('#view-library .mk[data-market="mcp"]');
        await box.locator(".mk-card").first().waitFor();
        const ids = () => box.locator(".mk-card").evaluateAll((cs) => cs.map((c) => c.dataset.id));
        const curated = servers.map((x) => x.id);
        if (name === "used") {
          assert.deepEqual(await ids(), ["io.github.o9/m9", "io.github.o5/m5", ...curated.filter((id) => id !== "io.github.o9/m9" && id !== "io.github.o5/m5")]);
          assert.equal((await box.locator(".mk-note").textContent()).trim(), note[lang][0]);
          const chip = box.locator('.mk-card[data-id="io.github.o9/m9"] .mk-installs');
          assert.equal((await chip.textContent()).trim(), "1.2K");
          if (people[lang]) assert.equal(await chip.getAttribute("title"), people[lang]);
          // a few users still say so, in the curated place
          assert.equal((await box.locator('.mk-card[data-id="playwright"] .mk-installs').textContent()).trim(), "3");
          assert.equal(await box.locator('.mk-card[data-id="context7"] .mk-installs').count(), 0);
          // the chip and the add button fit the narrow card
          for (const id of ["io.github.o9/m9", "playwright"]) {
            const fits = await box.locator(`.mk-card[data-id="${id}"]`).evaluate((c) => {
              const r = c.getBoundingClientRect();
              return [...c.querySelectorAll(".mk-foot > *")].every((e) => { const b = e.getBoundingClientRect(); return b.left >= r.left - 0.5 && b.right <= r.right + 0.5; });
            });
            assert(fits, id);
          }
        } else {
          assert.deepEqual(await ids(), curated);
          assert.equal((await box.locator(".mk-note").textContent()).trim(), note[lang][1]);
          assert.equal(await box.locator('.mk-card[data-id="io.github.o9/m9"] .mk-installs').count(), 0);
        }

        // a search keeps the registry's order, counts shown or not
        await box.locator('input[data-lib="market-mcp"]').fill("m");
        const found = servers.filter((x) => x.name.startsWith("m")).reverse().map((x) => x.id);
        await page.waitForFunction((n) => document.querySelectorAll('.mk[data-market="mcp"] .mk-card').length === n, found.length);
        assert.deepEqual(await ids(), found);
        if (name === "used") assert.equal(await box.locator('.mk-card[data-id="io.github.o9/m9"] .mk-installs').count(), 1);
        // asked once, not with every search
        assert.equal(asked.length, 1);
        assert.deepEqual(errors, []);
      });
    }
  }
}
