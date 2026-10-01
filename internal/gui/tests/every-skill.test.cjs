// Run with Node's test runner and Playwright on the module path; see README.md.
// emo172 (#443): with 30+ skills, turning them all on or off was a row's All
// at a time. "Turn all on" beside "In the library" posts every agent shown
// that can take skills, with on, at once; "Turn all off" asks first in the
// page (no window.confirm), and Cancel posts nothing; each is greyed when
// there's nothing for it to do; the clicks scroll nothing; in English and
// Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, skills = true) => ({ id, name, icon: "", skills: skills ? `${HOME}/.${id}/skills` : "" });
const sk = (name, agents) => ({ name, description: name, kind: "folder", agents });
const lib = (skills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("pi", "Pi"), agent("goose", "Goose", false)],
  instructions: { agents: [] }, servers: [], skills, foundServers: [], projects: [], foundSkills: [],
});

function server(lang, posts) {
  let skills = [sk("grill-me", ["claude"]), sk("tdd", []), sk("pdf", ["codex", "pi"])];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(skills) });
    if (url.pathname === "/api/library/skills/agents-all") {
      const { agents, on } = req.postDataJSON();
      posts.push({ agents, on });
      skills = skills.map((s) => ({ ...s, agents: on ? [...new Set([...s.agents, ...agents])].sort() : s.agents.filter((a) => !agents.includes(a)) }));
      return route.fulfill({ json: { ...lib(skills), result: { changed: agents } } });
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
const lit = (page) => page.locator("#view-library .lib-ag.on[data-agent]").count();

const words = {
  en: { on: "Turn all on", off: "Turn all off", ask: "Turn off all 3 skills?", aside: /taken out of Claude Code, Codex, Pi/, cancel: "Cancel", onDone: "3 skills are on for all 3 agents", offDone: "3 skills are off for all 3 agents", tip: "Give every skill to all 3 agents that can take skills" },
  zh: { on: "全部启用", off: "全部关闭", ask: "关闭全部 3 个技能？", aside: /从 Claude Code, Codex, Pi 中移除/, cancel: "取消", onDone: "已为全部 3 个 Agent 启用 3 个技能", offDone: "已从全部 3 个 Agent 中关闭 3 个技能", tip: "为全部 3 个可用的 Agent 启用每个技能" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every skill on or off at once", async (t) => {
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
        const on = page.locator("#view-library .row-head button.lib-everyon");
        const off = page.locator("#view-library .row-head button.lib-everyoff");
        assert.equal((await on.textContent()).trim(), w.on);
        assert.equal((await off.textContent()).trim(), w.off);
        assert.equal(await on.getAttribute("title"), w.tip);
        assert.equal(await lit(page), 3);
        const before = await scrolled(page);

        await press(page, on);
        await page.waitForTimeout(300);
        assert.deepEqual(posts, [{ agents: ["claude", "codex", "pi"], on: true }], "one post, for the agents that take skills");
        assert.equal(await lit(page), 9, "every chip of every row is lit");
        assert.equal((await page.locator("#status").textContent()).trim(), w.onDone);
        assert.equal(await on.isDisabled(), true, "nothing left to turn on");
        assert.equal(await scrolled(page), before, "the click scrolls nothing");

        // off asks in the page first; Cancel leaves everything
        await press(page, off);
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.ask);
        assert.match(await sheet.locator(".lib-confirm").textContent(), w.aside);
        await sheet.locator(".bar button", { hasText: w.cancel }).click();
        await sheet.waitFor({ state: "detached" });
        assert.equal(posts.length, 1, "Cancel posts nothing");
        assert.equal(await lit(page), 9);

        await press(page, off);
        await sheet.waitFor();
        await sheet.locator(".bar button.danger-fill").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts[1], { agents: ["claude", "codex", "pi"], on: false });
        assert.equal(await lit(page), 0, "no chip is lit");
        assert.equal((await page.locator("#status").textContent()).trim(), w.offDone);
        assert.equal(await off.isDisabled(), true, "nothing left to turn off");
        assert.equal(await on.isDisabled(), false);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
      });
    }

    assert.deepEqual(errors, []);
  });
}
