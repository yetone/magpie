// Run with Node's test runner and Playwright on the module path; see README.md.
// With many agents a skill's row keeps its name (#936, liuweifeng): the
// row of agent chips took the whole row in an 866px window, so every
// skill's name and every group's repository were squeezed to nothing and
// the group heads read as more of the same rows. Each name is shown, the
// chips are kept inside the row, and a group's head names its repository
// and looks unlike the skills under it. English and Chinese; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const AGENTS = Array.from({ length: 30 }, (_, i) => agent(`a${i}`, `Agent ${i}`, i % 2 ? "openai" : "claudecode-color"));
const ALL = AGENTS.map((a) => a.id);

function server(lang, state) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
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

const SKILLS = [
  { name: "brainstorming", kind: "github", source: "https://github.com/obra/superpowers/tree/main/skills/brainstorming", repo: "obra/superpowers", description: "d", agents: ALL },
  { name: "writing-plans", kind: "github", source: "https://github.com/obra/superpowers/tree/main/skills/writing-plans", repo: "obra/superpowers", description: "d", agents: ALL.slice(0, 20) },
  { name: "tdd", kind: "github", source: "https://github.com/mattpocock/skills/tree/main/skills/tdd", repo: "mattpocock/skills", description: "d", agents: ALL },
  { name: "notes", kind: "", source: "", description: "d", agents: ALL.slice(3) },
];

// The hand's room is set in whole pixels from the row's width (handGap in
// plugins/library/client.js), never in container units: a chip's style
// in cqw is worked out again on every change of the window's width, and
// at 196 skills of 24 agents a resize took ~0.5s a step (lexrus on X).
test("the library's chips use no container units", async () => {
  const css = await fs.readFile(path.join(assets, "plugins/library/library.css"), "utf8");
  assert.equal(/[\d.](cqw|cqi|cqh|cqb|cqmin|cqmax)\b/.test(css.replace(/\/\*[\s\S]*?\*\//g, "")), false);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill keeps its name beside many agents", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const resized = {};
    for (const lang of ["en", "zh"]) for (const width of [866, 560]) {
      await t.test(`${lang} ${width}px`, async () => {
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: structuredClone(AGENTS),
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            skills: structuredClone(SKILLS),
          },
        };
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-grouphead").first().waitFor();
        // unfold every group, so its skills are drawn
        for (const h of await v.locator(".lib-grouphead:not(.open)").all()) await h.click();
        await v.locator(".lib-group .lib-row:not(.lib-grouphead) .name", { hasText: "notes" }).waitFor({ state: "attached" });
        const measure = () => v.locator(".lib-group .lib-row").evaluateAll((rs) => rs.map((r) => {
          const n = r.querySelector(".name"), rb = r.getBoundingClientRect(), nb = n.getBoundingClientRect();
          const chips = r.querySelector(":scope > .lib-agents");
          const cb = chips && chips.getBoundingClientRect();
          return {
            head: r.classList.contains("lib-grouphead"), text: n.textContent.trim(),
            nameW: nb.width, nameShown: nb.width >= Math.min(60, n.scrollWidth) && nb.left >= rb.left && nb.right <= rb.right + 0.5,
            chipsIn: !cb || (cb.left >= rb.left - 0.5 && cb.right <= rb.right + 0.5),
            hand: cb ? Math.round(cb.width) : 0,
            bg: getComputedStyle(r).backgroundColor, weight: getComputedStyle(n).fontWeight,
          };
        }));
        // the hand's room follows the window as it is resized (handGap), not
        // only the width it was drawn at
        if (width === 866) {
          await page.setViewportSize({ width: 560, height: 900 });
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
          const narrower = await measure();
          for (const r of narrower) assert(r.chipsIn, `${r.text}: its agents run out of its row once the window is narrower`);
          resized[lang] = narrower.map((r) => [r.text, r.hand]);
          await page.setViewportSize({ width, height: 900 });
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        }
        const rows = await measure();
        // a hand resized to 560px closes up as one drawn at 560px does
        if (width === 560 && resized[lang]) assert.deepEqual(resized[lang], rows.map((r) => [r.text, r.hand]), "the hands drawn at 866px and narrowed to 560px");
        const skills = rows.filter((r) => !r.head), heads = rows.filter((r) => r.head);
        assert.deepEqual(heads.map((h) => h.text), ["mattpocock/skills", "obra/superpowers", lang === "zh" ? "本机" : "On this computer"]);
        assert.deepEqual(skills.map((s) => s.text).sort(), ["brainstorming", "notes", "tdd", "writing-plans"]);
        for (const r of rows) {
          assert(r.nameShown, `${r.text}: its name is ${r.nameW}px wide, squeezed out of its row`);
          assert(r.chipsIn, `${r.text}: its agents run out of its row`);
        }
        // a group's head is not one more skill row
        for (const h of heads) assert(h.bg !== skills[0].bg || h.weight !== skills[0].weight, `${h.text}: its head looks like a skill's row`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
