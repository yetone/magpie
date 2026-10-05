// Run with Node's test runner and Playwright on the module path; see README.md.
// ADabbler (#896): the Library gives skills as links to its folder, and
// some want each agent to have a copy of its own. The Skills tab says how
// they're given, Links or Copies, and switching posts the library's way;
// By agent lists the agents that take skills, each with Library's way /
// Links / Copies (Claude Desktop only ever takes copies, and has no
// switch), and a pick posts that agent's way; a skill whose copy in an
// agent differs from the library's says so, and a click syncs. No select,
// no scroll, no left border, in English and Chinese. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ada";

function server(lang, posts) {
  const st = { copy: false, own: {}, behind: ["codex"] };
  const how = (id) => st.own[id] || (st.copy ? "copy" : "link");
  const agent = (id, name, extra = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, how: how(id), howOwn: !!st.own[id], ...extra });
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME, copySkills: st.copy,
    agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("claude-desktop", "Claude Desktop", { how: "copy", mustCopy: true })],
    instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [],
    skills: [
      { name: "pdf", description: "Read PDFs", kind: "", agents: ["claude", "codex"], behind: st.behind.length ? st.behind : undefined },
      { name: "tdd", description: "Tests first", kind: "", agents: ["claude"] },
    ],
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skills/how") {
      const b = req.postDataJSON();
      posts.push({ path: "how", ...b });
      if (!b.agent) st.copy = b.how === "copy";
      else if (b.how) st.own[b.agent] = b.how;
      else delete st.own[b.agent];
      return route.fulfill({ json: { ...lib(), result: { changed: ["claude", "codex"], problems: [] } } });
    }
    if (url.pathname === "/api/library/all/sync") {
      posts.push({ path: "sync" });
      st.behind = [];
      return route.fulfill({ json: { ...lib(), result: { changed: ["codex"], problems: [] } } });
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
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
// no coloured stripe down the left of anything it drew
const leftBorders = (page, sel) => page.evaluate((sel) => [...document.querySelectorAll(sel)].flatMap((e) => [e, ...e.querySelectorAll("*")])
  .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 0 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderTopColor)
  .map((e) => e.className), sel);

const words = {
  en: {
    label: "Give skills as", links: "Links", copies: "Copies", byAgent: "By agent", byAgentOne: "By agent (1)",
    linkNote: "Links follow the library's skill: an update reaches the agents at once.",
    copyNote: "Each agent gets a folder of its own, made again when the library's skill changes; edits made in a copy are replaced.",
    copied: "The agents get copies of their skills now", behind: "Copy out of date", behindTip: "The copy in Codex differs from the library's skill. Click to copy it again.",
    synced: "Copies updated", sheet: "How each agent gets its skills", libWay: "Library's way", only: "Only ever takes copies", getsCopies: "Gets copies", getsLinks: "Gets links",
    codexLinks: "Codex gets links to its skills now",
  },
  zh: {
    label: "Skills 启用方式", links: "符号链接", copies: "副本", byAgent: "按 Agent", byAgentOne: "按 Agent（1）",
    linkNote: "符号链接跟着资源库里的 Skill 走：一更新，各 Agent 立刻就用上。",
    copyNote: "每个 Agent 拿到一份独立的文件夹，资源库里的 Skill 变了就重新复制；在副本里做的修改会被覆盖。",
    copied: "现在各 Agent 拿到的是 Skills 的副本", behind: "副本已落后", behindTip: "Codex 里的副本和资源库里的 Skill 不一样了。点一下重新复制。",
    synced: "副本已更新", sheet: "各 Agent 的 Skills 启用方式", libWay: "跟随资源库", only: "只能用副本", getsCopies: "用副本", getsLinks: "用符号链接",
    codexLinks: "Codex 现在用的是符号链接",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": skills given as links or copies (#896)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts) => {
      const ctx = await browser.newContext({ viewport: { width: 900, height: 700 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };

    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], posts = [];
        const page = await open(lang, posts);
        const bar = page.locator("#lib-skillhow");
        assert.equal((await bar.locator(".label").textContent()).trim(), w.label);
        const segs = bar.locator(".segs .opt");
        assert.deepEqual(await segs.allTextContents(), [w.links, w.copies]);
        assert.equal((await bar.locator(".segs .opt.on").textContent()).trim(), w.links);
        assert.equal((await bar.locator(".note").textContent()).trim(), w.linkNote);
        assert.equal((await bar.locator(".lib-howagent").textContent()).trim(), w.byAgent);
        const before = await scrolled(page);

        // the library's way: copies
        await press(page, segs.nth(1));
        await page.waitForTimeout(300);
        assert.deepEqual(posts, [{ path: "how", agent: "", how: "copy" }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.copied);
        assert.equal((await bar.locator(".segs .opt.on").textContent()).trim(), w.copies);
        assert.equal((await bar.locator(".note").textContent()).trim(), w.copyNote);
        assert.equal(await scrolled(page), before, "the switch scrolls nothing");

        // a copy behind the library's says so, and a click syncs
        const pdf = page.locator("#view-library .lib-skill", { hasText: "pdf" });
        const behind = pdf.locator(".lib-behind");
        assert.equal((await behind.textContent()).trim(), w.behind);
        assert.equal(await behind.getAttribute("title"), w.behindTip);
        assert.equal(await page.locator("#view-library .lib-skill", { hasText: "tdd" }).locator(".lib-behind").count(), 0);
        assert.equal(await behind.getAttribute("role"), "button");
        await behind.focus();
        await behind.press(lang === "en" ? "Enter" : "Space");
        await page.waitForTimeout(300);
        assert.deepEqual(posts[1], { path: "sync" });
        assert.equal(await page.locator("#view-library .lib-behind").count(), 0, "no copy is behind after the sync");
        assert.equal((await page.locator("#status").textContent()).trim(), w.synced);
        assert.equal(await page.locator("#modal .lib-editor").count(), 0, "the tag's click doesn't open the skill");
        assert.equal(await scrolled(page), before, "the sync scrolls nothing");

        // one agent its own way
        await press(page, bar.locator(".lib-howagent"));
        const sheet = page.locator("#modal .lib-howsheet");
        await sheet.waitFor();
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.sheet);
        const rows = sheet.locator(".lib-how-row");
        assert.deepEqual(await rows.evaluateAll((r) => r.map((x) => x.dataset.agent)), ["claude", "codex", "claude-desktop"]);
        const codex = rows.nth(1), desktop = rows.nth(2);
        assert.equal((await codex.locator(".sub").textContent()).trim(), w.getsCopies);
        assert.deepEqual(await codex.locator(".segs .opt").allTextContents(), [w.libWay, w.links, w.copies]);
        assert.equal((await codex.locator(".segs .opt.on").textContent()).trim(), w.libWay);
        assert.equal((await desktop.locator(".sub").textContent()).trim(), w.only);
        assert.equal(await desktop.locator(".segs").count(), 0, "Claude Desktop has no switch");
        await press(page, codex.locator(".segs .opt").nth(1));
        await page.waitForTimeout(300);
        assert.deepEqual(posts[2], { path: "how", agent: "codex", how: "link" });
        assert.equal((await page.locator("#status").textContent()).trim(), w.codexLinks);
        assert.equal((await rows.nth(1).locator(".sub").textContent()).trim(), w.getsLinks);
        assert.equal((await rows.nth(1).locator(".segs .opt.on").textContent()).trim(), w.links);
        assert.equal((await rows.nth(0).locator(".sub").textContent()).trim(), w.getsCopies, "the others keep the library's way");
        assert.deepEqual(await leftBorders(page, "#modal .lib-howsheet"), []);
        // back to the library's way
        await press(page, rows.nth(1).locator(".segs .opt").nth(0));
        await page.waitForTimeout(300);
        assert.deepEqual(posts[3], { path: "how", agent: "codex", how: "" });
        assert.equal((await rows.nth(1).locator(".segs .opt.on").textContent()).trim(), w.libWay);
        await press(page, rows.nth(1).locator(".segs .opt").nth(1));
        await page.waitForTimeout(300);
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.equal(await page.getByRole("alertdialog").count(), 0, "saved skill choices close without a discard question");
        assert.equal((await bar.locator(".lib-howagent").textContent()).trim(), w.byAgentOne, "the button counts the agents with a way of their own");
        assert.equal(await scrolled(page), before, "the sheet scrolls nothing");

        await press(page, bar.locator(".lib-howagent"));
        await sheet.waitFor();
        await press(page, sheet.locator('.lib-how-row[data-agent="codex"] .segs .opt').nth(2));
        await page.waitForTimeout(300);
        if (lang === "en") await page.keyboard.press("Escape");
        else await page.evaluate(() => { show("agents"); });
        await sheet.waitFor({ state: "detached" });
        assert.equal(await page.getByRole("alertdialog").count(), 0, "Escape and page changes keep already saved choices");

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(await leftBorders(page, "#lib-skillhow, #view-library .lib-skill"), []);
      });
    }

    assert.deepEqual(errors, []);
  });
}
