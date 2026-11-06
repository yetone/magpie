// Run with Node's test runner and Playwright on the module path; see README.md.
// Group again (White Immortal on Discord: of mattpocock/skills added a while
// ago only 20 were grouped, the rest under On this computer, and nothing
// to group them again by). The Skills head's ⋯ menu has Group again
// (library-bar-more), which asks
// magpie to trace the skills to their repositories once more and draws
// the page it answers with: the 18 join the 20, and the status says how
// many did. While it works, the bar shows Grouping… in its place. Every language, at a narrow width too, in Chromium and WebKit;
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
// copies moved into the library: grouped by what magpie traced them to
const copy = (repo) => (name) => ({ name, kind: "folder", source: `${HOME}/.claude/skills/${name}`, repo, description: "d", agents: ["claude"] });
const NAMES = Array.from({ length: 38 }, (_, i) => "skill-" + String(i).padStart(2, "0"));

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
    if (url.pathname === "/api/library/skills/regroup") {
      posts.push(req.method());
      await new Promise((r) => { state.answer = r; });
      let n = 0;
      for (const s of state.lib.skills) if (!s.repo) { s.repo = "mattpocock/skills"; n++; }
      return json({ ...state.lib, regrouped: n });
    }
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
  en: { button: "Group again", busy: "Grouping…", done: "18 more skills grouped with their repositories" },
  zh: { button: "重新分组", busy: "分组中…", done: "又有 18 个技能归入了各自的仓库" },
  "zh-TW": { button: "重新分組", busy: "分組中…", done: "又有 18 個技能歸入了各自的倉庫" },
  ja: { button: "グループを作り直す", busy: "グループ化中…", done: "さらに 18 個のスキルをそれぞれのリポジトリにまとめました" },
  de: { button: "Neu gruppieren", busy: "Wird gruppiert…", done: "18 weitere Skills zu ihren Repositorys gruppiert" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Group again groups a pack's older copies with the rest of it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [1100, 440]) {
        await t.test(lang + " " + width, async () => {
          const w = L[lang];
          const state = {
            lib: {
              dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
              agents: [agent("claude", "Claude Code", "claudecode-color")],
              instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
              skills: [...NAMES.slice(0, 20).map(copy("mattpocock/skills")), ...NAMES.slice(20).map(copy(""))],
            },
          };
          const posts = [];
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, state, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const v = page.locator("#view-library");
          await v.locator(".lib-grouphead").first().waitFor();
          // 20 grouped with their repository, 18 on this computer
          const heads = () => v.locator(".lib-grouphead .sub").allTextContents();
          assert.equal((await heads()).length, 2);
          assert.match((await heads())[0], /20/);
          assert.match((await heads())[1], /18/);

          // under the heading's ⋯
          const item = async () => {
            await v.locator(".lib-skillshead .lib-barmore").click();
            const it = page.locator(".row-menu .rm-item.lib-regroup");
            await it.waitFor();
            return it;
          };
          const b = await item();
          assert.equal((await b.textContent()).trim(), w.button);
          assert.ok((await b.getAttribute("title")).length > 0);
          // the item is on the page at a narrow width, nothing wider than it
          const box = await b.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= width, `item at ${JSON.stringify(box)} in ${width}`);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true);

          await b.click();
          // busy on the bar while magpie fetches the repositories
          await page.waitForFunction(() => document.querySelector("#view-library .lib-skillshead .lib-regroup")?.disabled);
          assert.equal((await v.locator(".lib-skillshead .lib-regroup").textContent()).trim(), w.busy);
          state.answer();
          await page.waitForFunction((text) => document.querySelector("#status").textContent.trim() === text, w.done).catch(async () => {
            assert.equal((await page.locator("#status").textContent()).trim(), w.done);
          });
          // one group now, all 38 of it
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-grouphead").length === 0);
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-skill").length === 38);
          assert.equal(await v.locator(".lib-skillshead .lib-regroup").count(), 0, "done, it is back under ⋯");
          const again = await item();
          assert.equal((await again.textContent()).trim(), w.button);
          assert.equal(await again.isDisabled(), false);
          assert.deepEqual(posts, ["POST"]);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
