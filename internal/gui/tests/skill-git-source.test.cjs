// Run with Node's test runner and Playwright on the module path; see README.md.
// sunset on Discord: skills from GitLab, and from a company's own GitLab.
// The add box says any git repository's address works. A skill from one
// (kind "git") shows where it came from (a link for an https address, the
// address as text for an SSH one), has its own Update naming the
// repository, and Check for updates is offered for it. A probe of one
// names the repository. In English and Chinese, in Chromium and WebKit,
// wide and narrow. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/sunset";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const HTTPS = "https://gitlab.corp.example/team/skills/-/tree/HEAD/skills/pdf";
const SSH = "git@gitlab.corp.example:team/private.git";
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  checkDue: false,
  skills: [
    { name: "pdf", description: "PDFs", kind: "git", source: HTTPS, agents: ["claude"] },
    { name: "review", description: "Reviews", kind: "git", source: SSH, agents: ["claude"], edited: true },
  ],
});
const probe = { source: "https://gitlab.corp.example/team/skills", kind: "git", candidates: [{ path: "skills/a", name: "a", description: "a", have: false }] };

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skills/probe") { posts.push(req.postDataJSON()); return route.fulfill({ json: probe }); }
    if (url.pathname.startsWith("/api/library/skills/")) { posts.push(url.pathname.split("/").pop()); return route.fulfill({ json: { ...lib(), result: { changed: [] } } }); }
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
  en: { ph: "owner/repo, a GitHub or GitLab link, any git repository's address, or a folder on this computer", upd: "Update from gitlab.corp.example/team/skills", updSSH: "Update from " + SSH, check: "Check for updates", found: "1 skill in gitlab.corp.example/team/skills",
    tip: "You changed it since it was fetched from " + SSH + ". Updating asks before it replaces your changes.", ask: "Replace your changes to review with the version in " + SSH + "?" },
  zh: { ph: "owner/repo、GitHub 或 GitLab 链接、任意 git 仓库地址，或本机上的文件夹", upd: "从 gitlab.corp.example/team/skills 更新", updSSH: "从 " + SSH + " 更新", check: "检查更新", found: null,
    tip: "从 " + SSH + " 获取后你在本地改动过它。更新前会先询问，不会直接覆盖你的改动。", ask: "用 " + SSH + " 中的版本替换你对 review 的改动？" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill from a git repository shows it, updates from it, and the add box says so", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [900, 400]) for (const lang of ["en", "zh"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const src = v.locator('input[data-lib="source"]');
        await src.waitFor();
        assert.equal(await src.getAttribute("placeholder"), w.ph);

        const row = (name) => v.locator(".lib-skill", { has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        await row("pdf").waitFor();
        const link = row("pdf").locator("a.lib-srclink");
        assert.equal(await link.getAttribute("href"), HTTPS);
        assert.equal((await link.textContent()).trim(), HTTPS.replace("https://", ""));
        const ssh = row("review").locator(".lib-src .lib-srclink");
        assert.equal(await ssh.evaluate((e) => e.tagName), "SPAN", "an SSH address isn't a link");
        assert.equal((await ssh.textContent()).trim(), SSH);
        assert.equal(await row("pdf").locator(".lib-rowacts button.lib-icon").first().getAttribute("title"), w.upd);
        assert.equal((await row("review").locator(".lib-rowacts button.lib-icon").first().getAttribute("title")).split("\n")[0], w.updSSH);
        // under the heading's ⋯ (library-bar-more)
        await v.locator(".lib-skillshead .lib-barmore").click();
        const check = page.locator(".row-menu .rm-item.lib-check", { hasText: w.check });
        assert.equal(await check.count(), 1, "Check for updates is offered");
        await page.keyboard.press("Escape");
        await page.locator(".row-menu").waitFor({ state: "detached" });
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");

        await row("pdf").locator(".lib-rowacts button.lib-icon").first().click();
        await page.waitForTimeout(200);
        assert.deepEqual(posts, ["update"]);
        // one changed here names the repository it would be replaced from
        assert.equal(await row("review").locator(".lib-tag.lib-edited").getAttribute("title"), w.tip);
        await row("review").locator(".lib-rowacts button.lib-icon").first().click();
        const modal = page.locator(".lib-update-edited");
        await modal.waitFor();
        assert.equal((await modal.locator(".ehead b").textContent()).trim(), w.ask);
        assert.match(await modal.locator(".lib-confirm").textContent(), new RegExp(SSH.replace(/[.]/g, "\\.")));
        assert.deepEqual(posts, ["update"], "asking sends nothing");
        await page.keyboard.press("Escape");
        await modal.waitFor({ state: "detached" });

        await src.fill("https://gitlab.corp.example/team/skills");
        await src.press("Enter");
        const head = v.locator(".lib-probehead .note");
        await head.waitFor();
        assert.equal(posts[1].source, "https://gitlab.corp.example/team/skills");
        if (w.found) assert.equal((await head.textContent()).trim(), w.found);
        else assert.match(await head.textContent(), /gitlab\.corp\.example\/team\/skills/);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
