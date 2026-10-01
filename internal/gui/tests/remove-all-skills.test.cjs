// Run with Node's test runner and Playwright on the module path; see README.md.
// #449: taking every skill out of the library was a row's Remove at a time.
// "Remove all" beside "Turn all on/off" is red, asks first in the page (no
// window.confirm) saying which agents lose them and where their folders go,
// and Cancel posts nothing; confirming posts every skill's name at once;
// the clicks scroll nothing; in English and Chinese. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, skills = true) => ({ id, name, icon: "", skills: skills ? `${HOME}/.${id}/skills` : "" });
const sk = (name, kind, agents) => ({ name, description: name, kind, agents, source: kind === "folder" ? `${HOME}/src/${name}` : "" });
const lib = (skills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("pi", "Pi"), agent("goose", "Goose", false)],
  instructions: { agents: [] }, servers: [], skills, foundServers: [], projects: [], foundSkills: [],
});

function server(lang, posts) {
  let skills = [sk("grill-me", "local", ["claude"]), sk("tdd", "folder", []), sk("pdf", "local", ["codex", "pi"])];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(skills) });
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/library/skills/remove-all") skills = skills.filter((s) => !body.names.includes(s.name));
      return route.fulfill({ json: { ...lib(skills), result: { changed: ["claude", "codex", "pi"] } } });
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

const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));
// a click where the button is, as the reader's: Playwright's own click first
// scrolls the button into view, and the view here scrolls a little
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const rows = (page) => page.locator("#view-library .lib-row").count();

const words = {
  en: { rm: "Remove all", tip: "Take all 3 skills out of the library", ask: "Remove all 3 skills?", agents: /out of the library and out of Claude Code, Codex, Pi\./, kept: /2 folders are moved to magpie's backups/, linked: /1 linked from folders of your own are only unlinked/, cancel: "Cancel", go: "Remove all 3", done: "3 skills removed" },
  zh: { rm: "全部删除", tip: "从资源库中删除全部 3 个技能", ask: "删除全部 3 个技能？", agents: /从资源库中删除，并从 Claude Code, Codex, Pi 中移除/, kept: /2 个技能文件夹会移到 magpie 的备份中/, linked: /1 个链接自你自己文件夹的技能只会删除链接/, cancel: "取消", go: "删除全部 3 个", done: "已删除 3 个技能" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every skill out of the library at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts) => {
      const ctx = await browser.newContext({ viewport: { width: 860, height: 700 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-row").first().waitFor();
      return page;
    };

    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], posts = [];
        const page = await open(lang, posts);
        const rm = page.locator("#view-library .row-head button.lib-everyrm");
        assert.equal((await rm.textContent()).trim(), w.rm);
        assert.equal(await rm.getAttribute("title"), w.tip);
        const red = await page.evaluate(() => getComputedStyle(document.body).getPropertyValue("--red").trim());
        const color = await rm.evaluate((b) => getComputedStyle(b).color);
        const probe = await page.evaluate((c) => { const d = document.createElement("i"); d.style.color = c; document.body.append(d); const v = getComputedStyle(d).color; d.remove(); return v; }, red);
        assert.equal(color, probe, "the button is red");
        assert.equal(await rows(page), 3);
        const before = await scrolled(page);

        // it asks in the page first; Cancel leaves everything
        await press(page, rm);
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.ask);
        const said = (await sheet.locator(".lib-confirm").allTextContents()).join(" ");
        assert.match(said, w.agents);
        assert.match(said, w.kept);
        assert.match(said, w.linked);
        assert.equal(await scrolled(page), before, "the click scrolls nothing");
        await sheet.locator(".bar button", { hasText: w.cancel }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(200);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        assert.equal(await rows(page), 3);

        await press(page, rm);
        await sheet.waitFor();
        const go = sheet.locator(".bar button.danger-fill");
        assert.equal((await go.textContent()).trim(), w.go);
        await go.click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ path: "/api/library/skills/remove-all", body: { names: ["grill-me", "tdd", "pdf"] } }], "one post, with every skill");
        assert.equal(await rows(page), 0, "no skill is left");
        assert.equal((await page.locator("#status").textContent()).trim(), w.done);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
      });
    }

    assert.deepEqual(errors, []);
  });
}
