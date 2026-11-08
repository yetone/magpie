// Run with Node's test runner and Playwright on the module path; see README.md.
// #595: a skill put in the library's own folder by hand is shown, said to
// be there, and brought in where it is; a library skill kept in
// ~/.agents/skills is lit, and can't be clicked, for the agents that read
// that folder themselves, and a click on another chip or All never sends
// them; a skill's Remove says what really happens to its folder. No click
// moves the page. In English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/loosheng";
const LIB = `${HOME}/.config/magpie/library`;
const agent = (id, name, icon, more = {}) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const base = () => ({
  dir: LIB, backups: `${HOME}/.config/magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", "claudecode-color"),
    agent("codex", "Codex", "codex-color"),
    agent("gemini", "Gemini CLI", "gemini-color"),
    agent("pi", "Pi", "pi", { mcp: "" }),
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], servers: [],
  skills: [
    // brought in from ~/.agents/skills: Codex and Gemini CLI read it
    { name: "lint", kind: "folder", description: "Lint", source: `${HOME}/.agents/skills/lint`, agents: ["claude"], always: ["codex", "gemini"] },
    // the library's own folder (a linked one moved in by hand)
    { name: "notes", kind: "", description: "Notes", agents: [] },
  ],
  foundSkills: [{ name: "hand", description: "By hand", agents: [], library: `${LIB}/skills/hand` }],
});

function server(lang, posts) {
  let lib = base();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/skills/agents") {
      const body = req.postDataJSON();
      posts.push(body);
      lib = structuredClone(lib);
      lib.skills.find((x) => x.name === body.name).agents = body.agents;
      return route.fulfill({ json: { ...lib, problems: [], result: { changed: [], problems: [] } } });
    }
    if (url.pathname === "/api/library/skills/import") {
      const body = req.postDataJSON();
      posts.push({ import: body.name });
      lib = structuredClone(lib);
      lib.foundSkills = [];
      lib.skills.push({ name: body.name, kind: "", description: "By hand", agents: [] });
      return route.fulfill({ json: { ...lib, problems: [], result: { changed: [], problems: [] } } });
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

const words = {
  en: {
    here: "in the library's folder, not listed",
    bring: "Lists it in the library where it is, nothing moved: you can give it to any agent",
    always: (a) => `${a} reads ~/.agents/skills itself, where this skill is kept — it has it whatever is ticked here`,
    stays: "It is taken out of every agent it was given to. The folder it was linked from stays where it is.",
    moved: "It is taken out of every agent it was given to, and its folder is moved to magpie's backups.",
    cancel: "Cancel",
  },
  zh: {
    here: "在资源库文件夹中，但未列入",
    bring: "在原处列入资源库，不移动文件：可分配给任何 Agent",
    always: (a) => `${a} 会自行读取此技能所在的 ~/.agents/skills，无论是否勾选都能使用`,
    stays: "将从所有已启用的 Agent 中移除，链接的原文件夹保持不动。",
    moved: "将从所有已启用的 Agent 中移除，文件夹移到 magpie 的备份。",
    cancel: "取消",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": skills in the library's folder and in ~/.agents/skills", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts) => {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("http://magpie.test/**", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      // the page's entrance played out: a button still sliding in isn't
      // stable, and Playwright scrolls the view to click it
      await page.waitForFunction(() => !document.querySelector("#view-library").getAnimations({ subtree: true }).some((a) => a.playState === "running" && isFinite(a.effect.getComputedTiming().endTime)));
      return page;
    };
    const row = (page, name) => page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
    const click = async (page, loc) => {
      // a click where the button is: Playwright's own click scrolls a
      // thing it finds still moving into view, which the reader's doesn't
      const top = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
      await loc.waitFor();
      const b = await loc.boundingBox();
      assert(b && b.y >= 0 && b.y + b.height <= page.viewportSize().height, `${loc} is out of view: ${JSON.stringify(b)}`);
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top);
    };
    const lit = (r) => r.locator(".lib-ag[data-agent]").evaluateAll((cs) => cs.filter((c) => c.getAttribute("aria-pressed") === "true").map((c) => c.dataset.agent));

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": a skill in the library's folder is shown, and brought in where it is", async () => {
        const posts = [];
        const page = await open(lang, posts);
        const r = row(page, "hand");
        await r.waitFor();
        assert.equal(await r.locator(".lib-src > span").first().textContent(), w.here);
        assert.match(await r.locator(".lib-src").first().textContent(), /library\/skills\/hand/);
        const b = r.locator("button.action");
        assert.equal(await b.getAttribute("title"), w.bring);
        await click(page, b);
        await page.waitForFunction(() => ![...document.querySelectorAll("#view-library .lib-row .name")].some((n) => n.textContent === "hand" && n.closest(".lib-row").querySelector("button.action")));
        assert.deepEqual(posts, [{ import: "hand" }]);
        await page.close();
      });

      await t.test(lang + ": the agents reading ~/.agents/skills have a skill kept there, whatever is ticked", async () => {
        const posts = [];
        const page = await open(lang, posts);
        const r = row(page, "lint");
        assert.deepEqual(await lit(r), ["claude", "codex", "gemini"]);
        for (const [id, name] of [["codex", "Codex"], ["gemini", "Gemini CLI"]]) {
          const c = r.locator(`.lib-ag[data-agent="${id}"]`);
          assert.equal(await c.isDisabled(), true);
          assert.equal(await c.getAttribute("title"), w.always(name));
        }
        assert.equal(await r.locator('.lib-ag[data-agent="pi"]').isDisabled(), false);
        // a click on Pi's chip sends Claude Code and Pi, not the two that
        // have it anyway
        await click(page, r.locator('.lib-ag[data-agent="pi"]'));
        await page.waitForFunction(() => document.querySelector("#status")?.textContent);
        assert.deepEqual(posts.map((p) => [...p.agents].sort()), [["claude", "pi"]]);
        // All takes it from the agents it can, and leaves the two lit
        await click(page, r.locator(".lib-ag.all"));
        await page.waitForFunction((n) => n === 2, posts.length).catch(() => {});
        await page.waitForTimeout(300);
        assert.equal(posts.length, 2);
        assert.deepEqual(posts[1].agents, []);
        assert.deepEqual(await lit(r), ["codex", "gemini"]);
        // the row's own click (open the SKILL.md) wasn't set off
        assert.equal(await page.locator("#modal").isHidden(), true);
        await page.close();
      });

      await t.test(lang + ": Remove says what happens to the skill's folder", async () => {
        const page = await open(lang, []);
        for (const [name, text] of [["lint", w.stays], ["notes", w.moved]]) {
          await click(page, row(page, name).locator(".lib-icon.danger"));
          assert.equal(await page.locator("#modal .lib-confirm").first().textContent(), text);
          await page.locator("#modal button", { hasText: new RegExp("^" + w.cancel + "$") }).click();
          await page.locator("#modal").waitFor({ state: "hidden" });
        }
        // nothing on the rows has a left-border accent
        const borders = await page.locator("#view-library .lib-row, #view-library .lib-row *").evaluateAll((els) => els.filter((e) => {
          const s = getComputedStyle(e);
          return parseFloat(s.borderLeftWidth) >= 2 && s.borderLeftStyle !== "none" && s.borderLeftColor !== s.borderRightColor;
        }).length);
        assert.equal(borders, 0);
        await page.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
