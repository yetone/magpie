// Run with Node's test runner and Playwright on the module path; see README.md.
// Raven (Discord): "Check for updates" only updated the skills the library
// had; a repository they came from (dontbesilent2025/dbskill) had added
// skills since, and the page never said. A check now lists them under "Also
// in their repositories", each with Add (for the agents that have the
// repository's others) and Ignore, and the status says how many there are;
// Add all and Ignore all are there for more than one. The row's buttons fit
// at narrow widths. In English and Chinese, in Chromium and WebKit. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/raven";
const REPO = "dontbesilent2025/dbskill";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const sk = (name) => ({ name, description: name + " skill", kind: "github", source: `https://github.com/${REPO}/tree/HEAD/skills/${name}`, agents: ["claude", "codex"], check: { name, status: "current" } });
const fresh = (name, description) => ({ id: `https://github.com/${REPO}/tree/HEAD/skills/${name}`, name, description, repo: REPO, path: "skills/" + name, from: `https://github.com/${REPO}/tree/HEAD/skills`, agents: ["claude", "codex"] });
const lib = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("pi", "Pi")],
  instructions: { agents: [] }, servers: [], skills: st.skills, foundServers: [], projects: [], foundSkills: [],
  newSkills: st.checked ? st.newSkills : [],
});

function server(lang, posts, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st) });
    if (url.pathname === "/api/library/skills/check") { st.checked = true; return route.fulfill({ json: lib(st) }); }
    if (url.pathname === "/api/library/skills/add-new" || url.pathname === "/api/library/skills/ignore-new") {
      const { names } = req.postDataJSON();
      posts.push({ what: url.pathname.split("/").pop(), names });
      const hit = st.newSkills.filter((n) => names.includes(n.id));
      st.newSkills = st.newSkills.filter((n) => !names.includes(n.id));
      if (url.pathname.endsWith("add-new")) st.skills = [...st.skills, ...hit.map((n) => sk(n.name))];
      return route.fulfill({ json: { ...lib(st), result: { changed: url.pathname.endsWith("add-new") ? ["claude", "codex"] : [] } } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};

const words = {
  en: { check: "Check for updates", said: "Every skill is up to date · 3 more skills in their repositories", head: "Also in their repositories", add: "Add", ignore: "Ignore", addAll: "Add all", ignoreAll: "Ignore all", added: "dbs-save is in the library now", ignored: "dbs-hook set aside", addTip: "Adds it to the library for Claude Code, Codex, which have its repository's other skills", allAdded: "1 skills added" },
  zh: { check: "检查更新", said: "所有技能都是最新的 · 同一仓库里还有 3 个技能", head: "同一仓库里的其他技能", add: "添加", ignore: "忽略", addAll: "全部添加", ignoreAll: "全部忽略", added: "dbs-save 已加入资源库", ignored: "已忽略 dbs-hook", addTip: "加入资源库并分配给 Claude Code, Codex（它们已有同仓库的其他技能）", allAdded: "已添加 1 个技能" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a check offers the skills a repository added since", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [860, 440]) for (const lang of ["en", "zh"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], posts = [];
        const st = { checked: false, skills: [sk("dbs"), sk("dbs-goal")], newSkills: [fresh("dbs-hook", "Hooks"), fresh("dbs-save", "Saves"), fresh("dbs-xhs-title", "Titles")] };
        // all of the page in view, the narrow one too
        const ctx = await browser.newContext({ viewport: { width, height: 1300 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
        const v = page.locator("#view-library");
        assert.equal(await v.locator(".lib-newskill").count(), 0, "nothing offered before a check");

        await v.locator(".row-head button.lib-updall", { hasText: w.check }).click();
        await v.locator(".lib-newskill").first().waitFor();
        assert.equal((await page.locator("#status").textContent()).trim(), w.said);
        assert.equal((await v.locator(".lib-newhead .label").textContent()).trim(), w.head);
        assert.deepEqual(await v.locator(".lib-newskill .name").allTextContents(), ["dbs-hook", "dbs-save", "dbs-xhs-title"]);
        assert.equal(await v.locator(".lib-newskill").nth(1).locator(".lib-srclink").textContent(), REPO + "/skills/dbs-save");
        assert.equal(await v.locator(".lib-newhead button", { hasText: w.addAll }).count(), 1);
        assert.equal(await v.locator(".lib-newhead button", { hasText: w.ignoreAll }).count(), 1);

        // each row's buttons are whole and inside the page, at this width
        const row = v.locator(".lib-newskill").nth(1);
        const add = row.locator("button.action", { hasText: w.add });
        assert.equal(await add.getAttribute("title"), w.addTip);
        for (const b of [add, row.locator("button", { hasText: w.ignore })]) {
          const box = await b.boundingBox();
          assert(box && box.width > 20 && box.x >= 0 && box.x + box.width <= width, `a button out of view: ${JSON.stringify(box)}`);
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");

        await press(page, add);
        await page.waitForTimeout(300);
        assert.deepEqual(posts, [{ what: "add-new", names: [fresh("dbs-save").id] }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.added);
        assert.deepEqual(await v.locator(".lib-newskill .name").allTextContents(), ["dbs-hook", "dbs-xhs-title"]);

        await press(page, v.locator(".lib-newskill").first().locator("button", { hasText: w.ignore }));
        await page.waitForTimeout(300);
        assert.deepEqual(posts[1], { what: "ignore-new", names: [fresh("dbs-hook").id] });
        assert.equal((await page.locator("#status").textContent()).trim(), w.ignored);
        assert.deepEqual(await v.locator(".lib-newskill .name").allTextContents(), ["dbs-xhs-title"]);
        assert.equal(await v.locator(".lib-newhead button", { hasText: w.addAll }).count(), 0, "Add all is for more than one");

        await press(page, v.locator(".lib-newskill button.action"));
        await page.waitForTimeout(300);
        assert.equal(await v.locator(".lib-newskill").count(), 0);
        assert.equal(await v.locator(".lib-newhead").count(), 0, "the heading goes with the last one");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
