// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie's own skill (magpie-quota, ttmouse on X) is first in the market's
// skills: until skills.sh lists it there is no count of installs to show and
// no skills.sh page to link, so its card and sheet show neither, where a
// skills.sh skill keeps both. Every language, a narrow window, both engines.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const items = [
  { id: "magpie-community/plugins/magpie-quota", name: "magpie-quota", source: "magpie-community/plugins", skillId: "magpie-quota", installs: 0, official: true, featured: true, description: "Check what is left of every subscription." },
  { id: "acme/skills/pdf", name: "pdf", source: "acme/skills", skillId: "pdf", installs: 1200, description: "PDFs" },
];

function serve(lang) {
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
    if (url.pathname === "/api/library/market/servers") return json({ items: [] });
    if (url.pathname === "/api/library/market/skills") return json({ items: structuredClone(items) });
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
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: magpie's own skill shows no installs and no skills.sh link`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 520, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "skills"); });
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=library");
      const view = page.locator("#view-library");
      const card = (id) => view.locator(`.mk[data-market="skills"] .mk-card[data-id="${id}"]`);
      const own = card("magpie-community/plugins/magpie-quota"), pdf = card("acme/skills/pdf");
      await own.waitFor();
      await pdf.waitFor();

      // first, marked official, nothing where a count would be
      assert.equal(await view.locator('.mk[data-market="skills"] .mk-card').first().getAttribute("data-id"), "magpie-community/plugins/magpie-quota");
      assert.equal(await own.locator(".mk-official").count(), 1);
      assert.equal(await own.locator(".mk-installs").count(), 0);
      assert.equal(await own.locator(".mk-add").count(), 1);
      assert.equal(await pdf.locator(".mk-installs").count(), 1);
      // nothing squeezed out of the narrow card
      for (const c of [own, pdf]) {
        const fits = await c.evaluate((e) => [...e.querySelectorAll(".mk-title, .mk-add")].every((x) => x.getBoundingClientRect().width > 0 && x.getBoundingClientRect().right <= e.getBoundingClientRect().right + 1));
        assert(fits, "a title or button is cut off");
      }

      const ext = () => page.locator(".mk-sheet .mk-ext");
      await own.locator(".mk-title").click();
      await page.locator(".mk-sheet").waitFor();
      assert.deepEqual(await ext().allTextContents(), ["github.com/magpie-community/plugins"]);
      assert.equal(await page.locator(".mk-sheet .mk-badge").count(), 1); // Official, no installs
      await page.keyboard.press("Escape");
      await page.locator(".mk-sheet").waitFor({ state: "detached" });

      await pdf.locator(".mk-title").click();
      await page.locator(".mk-sheet").waitFor();
      assert.deepEqual(await ext().allTextContents(), ["skills.sh", "github.com/acme/skills"]);
      assert.equal(await page.locator(".mk-sheet .mk-badge").count(), 1); // installs

      assert.deepEqual(errors, []);
    });
  }
}
