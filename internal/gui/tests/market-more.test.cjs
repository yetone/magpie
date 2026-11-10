// Run with Node's test runner and Playwright on the module path; see README.md.
// The market's lists are long now (the owner: both markets had too little
// in them): every skill skills.sh lists and every server a search finds
// comes back, and the page shows 60 cards, then 60 more each Show more,
// added under the ones shown, which stay where they are. Skills' about is
// asked for the cards shown only, and a trending skill, sent with no count
// of installs, shows none. Every language, a narrow window, both engines.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const skills = Array.from({ length: 150 }, (_, i) => ({
  id: `o${i}/skills/s${i}`, name: `s${i}`, source: `o${i}/skills`, skillId: `s${i}`, installs: i < 140 ? 5000 - i : 0, icon: "",
}));
const servers = Array.from({ length: 130 }, (_, i) => ({
  id: `io.github.o${i}/m${i}`, name: `m${i}`, title: `Server ${i}`, description: `Server number ${i}.`, transport: "stdio", runs: "npx", inputs: [],
}));
const more = { en: "Show more ({n} left)", zh: "显示更多（还有 {n} 个）", "zh-TW": "顯示更多（還有 {n} 個）", ja: "さらに表示（残り {n} 件）", de: "Mehr zeigen (noch {n})" };

function serve(lang, about) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") {
      return json({
        dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
        agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
        instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
      });
    }
    if (url.pathname === "/api/library/market/servers") return json({ items: structuredClone(servers) });
    if (url.pathname === "/api/library/market/skills") return json({ items: structuredClone(skills) });
    if (url.pathname === "/api/library/market/about") {
      const { ids } = route.request().postDataJSON();
      about.push(ids);
      return json(Object.fromEntries(ids.map((id) => [id, `About ${id}`])));
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const kind of ["skills", "mcp"]) {
      test(`${engine} ${lang} ${kind}: a long market list shows 60 cards, and 60 more each Show more`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width: 520, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const about = [];
        await page.route("**/*", serve(lang, about));
        await page.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, kind);
        t.after(() => browser.close());
        await page.goto("http://magpie.test/?view=library");
        const box = page.locator(`#view-library .mk[data-market="${kind}"]`);
        const cards = box.locator(".mk-card");
        const button = box.locator(".mk-more");
        const total = kind === "skills" ? skills.length : servers.length;
        const label = (n) => more[lang].replace("{n}", n.toLocaleString(lang));

        await button.waitFor();
        assert.equal(await cards.count(), 60);
        assert.equal((await button.textContent()).trim(), label(total - 60));
        // the button fits the narrow window
        assert(await button.evaluate((b) => b.getBoundingClientRect().right <= document.documentElement.clientWidth));
        if (kind === "skills") {
          await page.waitForFunction(() => !document.querySelector(".mk-desc.wait"));
          assert.deepEqual(about.flat().sort(), skills.slice(0, 60).map((x) => x.id).sort());
        }

        // a click adds cards under the ones shown and moves nothing (the
        // button brought into view by the wheel, as a reader would)
        await box.hover();
        // (the page ends above a phone's tab bar, so its own foot, not the window's)
        while (await button.evaluate((b) => b.getBoundingClientRect().bottom > b.closest("main.view").getBoundingClientRect().bottom)) await page.mouse.wheel(0, 600);
        // the wheel's last scroll told the page, a frame on
        await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        const first = cards.nth(59);
        const at = await first.evaluate((e) => e.getBoundingClientRect().top);
        await button.click();
        await page.waitForFunction((k) => document.querySelectorAll(`.mk[data-market="${k}"] .mk-card`).length === 120, kind);
        assert.equal(await first.evaluate((e) => e.getBoundingClientRect().top), at);
        assert.equal(await cards.nth(60).getAttribute("data-id"), (kind === "skills" ? skills : servers)[60].id);
        assert.equal((await button.textContent()).trim(), label(total - 120));
        if (kind === "skills") {
          await page.waitForFunction(() => !document.querySelector(".mk-desc.wait"));
          assert.deepEqual(about.flat().sort(), skills.slice(0, 120).map((x) => x.id).sort());
          assert.equal(await box.locator(`.mk-card[data-id="${skills[100].id}"] .mk-desc`).textContent(), `About ${skills[100].id}`);
        }

        // the last page: every card, and no button
        await button.click();
        await page.waitForFunction(([k, n]) => document.querySelectorAll(`.mk[data-market="${k}"] .mk-card`).length === n, [kind, total]);
        assert.equal(await button.count(), 0);
        if (kind === "skills") {
          // a trending skill, sent with no count, shows none
          assert.equal(await box.locator(`.mk-card[data-id="${skills[145].id}"] .mk-installs`).count(), 0);
          assert.equal(await box.locator(`.mk-card[data-id="${skills[5].id}"] .mk-installs`).count(), 1);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
