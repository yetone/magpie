// Run with Node's test runner and Playwright on the module path; see README.md.
// StringKe and JasonLeeForOnly on Discord: after Bring in all, "Claude Code
// already has a skill of its own called impeccable" stood in the Library's
// warning with nothing to do about it. The warning row now says it in the
// reader's language and settles it in place: "Use the library's" posts
// skills/use-library (the agent's own kept aside), "Keep Claude Code's"
// posts skills/keep-own (the agent off the skill), the warning goes, and the
// click scrolls nothing. A skill found in an agent with a byte copy in
// another is shown as that agent's too, not as differing there. In English
// and Chinese. No backend: the API is faked here.
// SivanCola (#1896): Claude Desktop's own docx, frontend-design, pdf, pptx
// and xlsx (Anthropic's, built in) were five failed writes in English code
// type, with a Try again that changes nothing, on the MCP servers tab too.
// They are one row for Claude Desktop now, in the reader's language, kept
// with one button (Desktop's built-in skills aren't put aside), and the MCP
// servers tab only says how many wait on Skills.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/stringke";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
// the screenshot's five, as magpie's sync words them
const builtIn = ["docx", "frontend-design", "pdf", "pptx", "xlsx"];
const desktopProblems = builtIn.map((n) => ({ agent: "claude-desktop", what: "skill:" + n, error: "Claude Desktop already has a skill of its own called " + n, own: true }));
const problem = { agent: "claude", what: "skill:impeccable", error: "Claude Code already has a skill of its own called impeccable", own: true };
const lib = (agents, problems, foundSkills = []) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("claude-desktop", "Claude Desktop")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills,
  skills: [
    { name: "impeccable", description: "Design", kind: "", agents },
    ...builtIn.map((name) => ({ name, description: name, kind: "", agents: [...agents, "claude-desktop"] })),
    ...Array.from({ length: 12 }, (_, i) => ({ name: "s" + i, description: "d", kind: "", agents: ["codex"] })),
  ],
  problems,
});
const found = [
  { name: "polish", description: "Polish", agents: ["claude"], copies: ["codex"] },
  { name: "audit", description: "Audit", agents: ["claude"], others: ["gemini"] },
];

function server(lang, posts, problems = [problem]) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(["claude", "codex"], problems, found) });
    if (url.pathname === "/api/library/skills/use-library") {
      posts.push([url.pathname, req.postDataJSON()]);
      return route.fulfill({ json: { ...lib(["claude", "codex"], [], found), result: { changed: ["claude"], problems: [] } } });
    }
    if (url.pathname === "/api/library/skills/keep-own") {
      posts.push([url.pathname, req.postDataJSON()]);
      return route.fulfill({ json: { ...lib(["codex"], [], found), result: { changed: [], problems: [] } } });
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

const words = {
  en: {
    line: "Claude Code · Skill impeccable",
    err: "Claude Code has a skill of its own called impeccable, not the same as the library's",
    use: "Use the library's", keep: "Keep Claude Code's",
    used: "Claude Code has the library's impeccable now; its own is kept with the backups",
    kept: "Claude Code keeps its own impeccable",
    differs: "differs in Gemini CLI",
    desktopTitle: "Some agents have skills of their own by the library's names",
    desktopLine: "Claude Desktop · Skills docx, frontend-design, pdf, pptx, xlsx",
    desktopErr: "Claude Desktop has its own of these, so it doesn't get the library's",
    desktopKeep: "Keep Claude Desktop's",
    desktopKept: "Claude Desktop keeps its own docx, frontend-design, pdf, pptx, xlsx",
    away: "Skills: 5 more", mcp: "MCP servers",
  },
  zh: {
    line: "Claude Code · 技能 impeccable",
    err: "Claude Code 已有自己的同名技能 impeccable，与资源库中的不同",
    use: "用资源库的", keep: "保留 Claude Code 自己的",
    used: "Claude Code 已换成资源库的 impeccable，它自己的已放进备份",
    kept: "Claude Code 保留了自己的 impeccable",
    differs: "Gemini CLI 中不同",
    desktopTitle: "有些 Agent 已有与资源库同名的自带技能",
    desktopLine: "Claude Desktop · 技能 docx, frontend-design, pdf, pptx, xlsx",
    desktopErr: "Claude Desktop 已自带这些技能，因此不写入资源库的版本",
    desktopKeep: "保留 Claude Desktop 自己的",
    desktopKept: "Claude Desktop 保留了自己的 docx, frontend-design, pdf, pptx, xlsx",
    away: "技能：另有 5 项", mcp: "MCP 服务器",
  },
};

const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent's own skill in the library's way", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, problems, libTab = "skills") => {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 700 } });
      await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, libTab);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, problems));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel)" + (libTab === "skills" ? " .lib-row" : "")).first().waitFor();
      return page;
    };

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      for (const [which, route, done, agents] of [["use", "/api/library/skills/use-library", "used"], ["keep", "/api/library/skills/keep-own", "kept"]]) {
        await t.test(`${lang}: ${w[which]} settles it from the warning`, async () => {
          const posts = [];
          const page = await open(lang, posts);
          const card = page.locator("#view-library > .lib-problems");
          const li = card.locator("li");
          await li.waitFor();
          assert.equal(await li.evaluate((l) => [...l.children].filter((c) => !c.matches(".lib-problems-err, .lib-problems-fix")).map((c) => c.textContent).join("")), w.line);
          assert.equal(await li.locator(".lib-problems-err").textContent(), w.err);
          const buttons = li.locator(".lib-problems-fix button");
          assert.deepEqual(await buttons.allTextContents(), [w.use, w.keep]);
          // a dot says it, not a coloured stripe down its side
          assert.equal(await card.evaluate((c) => getComputedStyle(c).borderLeftWidth), "0px");
          assert.equal(await li.evaluate((c) => getComputedStyle(c).borderLeftWidth), "0px");
          const before = await scrolled(page);
          await li.getByRole("button", { name: w[which], exact: true }).click();
          await card.waitFor({ state: "detached" });
          assert.deepEqual(posts, [[route, { name: "impeccable", agent: "claude" }]]);
          await page.locator("#status").filter({ hasText: w[done] }).waitFor();
          assert.equal(await scrolled(page), before, "the click scrolled the page");
          await page.close();
        });
      }

      await t.test(lang + ": Claude Desktop's own skills are one row, kept with one button", async () => {
        const posts = [];
        const page = await open(lang, posts, desktopProblems);
        const card = page.locator("#view-library > .lib-problems");
        const li = card.locator("li");
        await li.first().waitFor();
        assert.equal(await card.locator(".lib-problems-head b").textContent(), w.desktopTitle);
        assert.equal(await li.count(), 1, "one row for the agent, not one a skill");
        assert.equal(await li.evaluate((l) => [...l.children].filter((c) => !c.matches(".lib-problems-err, .lib-problems-fix")).map((c) => c.textContent).join("")), w.desktopLine);
        assert.equal(await li.locator(".lib-problems-err").textContent(), w.desktopErr);
        // only keeping: Desktop's built-in skills aren't set aside
        assert.deepEqual(await li.locator(".lib-problems-fix button").allTextContents(), [w.desktopKeep]);
        // nothing failed, so nothing to try again
        assert.equal(await card.locator(".lib-problems-head button").count(), 0);
        const before = await scrolled(page);
        await li.getByRole("button", { name: w.desktopKeep, exact: true }).click();
        await card.waitFor({ state: "detached" });
        assert.deepEqual(posts, [["/api/library/skills/keep-own", { names: builtIn, agent: "claude-desktop" }]]);
        await page.locator("#status").filter({ hasText: w.desktopKept }).waitFor();
        assert.equal(await scrolled(page), before, "the click scrolled the page");
        await page.close();
      });

      await t.test(lang + ": the MCP servers tab doesn't list the skills, it says where they are", async () => {
        const page = await open(lang, [], desktopProblems, "mcp");
        const card = page.locator("#view-library > .lib-problems");
        await card.waitFor();
        assert.equal(await card.locator("li").count(), 0);
        const go = card.locator(".lib-problems-away button");
        assert.deepEqual(await go.allTextContents(), [w.away]);
        assert.equal(await page.locator("#view-library .lib-tabs .opt.on").textContent(), w.mcp);
        const before = await scrolled(page);
        await go.click();
        await card.locator("li").first().waitFor();
        assert.match(await page.locator("#view-library .lib-tabs .opt.on").textContent(), new RegExp("^" + (lang === "zh" ? "技能" : "Skills")));
        assert.equal(await card.locator("li").count(), 1);
        assert.equal(await scrolled(page), before, "the click scrolled the page");
        await page.close();
      });

      await t.test(lang + ": a byte copy in another agent isn't said to differ", async () => {
        const page = await open(lang, []);
        const polish = page.locator("#view-library .lib-row:not(.lib-hub)").filter({ has: page.locator(".name", { hasText: /^polish$/ }) });
        const audit = page.locator("#view-library .lib-row:not(.lib-hub)").filter({ has: page.locator(".name", { hasText: /^audit$/ }) });
        await polish.waitFor();
        assert.equal(await polish.locator(".lib-have > *").count(), 2, "Claude Code and Codex have it");
        assert.equal(await polish.getByText(w.differs.replace("Gemini CLI", "Codex")).count(), 0);
        assert.equal(await audit.getByText(w.differs).count(), 1);
        await page.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
