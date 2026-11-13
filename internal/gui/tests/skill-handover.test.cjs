// Run with Node's test runner and Playwright on the module path; see README.md.
// tnt1223 on Discord: skills kept in ~/.agents/skills, which some agents
// read themselves whatever is ticked, are handed over to the library. A
// row of one says "Manage in magpie" in its source line; the skills'
// heading moves all of them at once. Each asks first, in the page, saying
// where the folder goes (or that only a link goes), who keeps it and how
// to put it back; Cancel posts nothing; the move posts
// skills/hand-over once with the names. A row not kept there has no
// button, and no click scrolls the page. Five languages, Chromium and
// WebKit, wide and narrow. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tnt";
const SH = `${HOME}/.agents/skills`;
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const lib = (st) => ({
  sharedSkills: !!st.shared,
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  checkDue: false,
  skills: [
    st.moved.includes("orca")
      ? { name: "orca", description: "Orca", agents: ["claude", "codex", "gemini"] }
      : { name: "orca", description: "Orca", kind: "folder", source: `${SH}/orca`, shared: `${SH}/orca`, always: ["codex", "gemini"], agents: ["claude"] },
    st.moved.includes("grill")
      ? { name: "grill", description: "Grill", kind: "folder", source: `${HOME}/aimer/grill`, agents: ["codex", "gemini"] }
      : { name: "grill", description: "Grill", kind: "folder", source: `${HOME}/aimer/grill`, shared: `${SH}/grill`, sharedElsewhere: true, always: ["codex", "gemini"], agents: [] },
    { name: "pdf", description: "PDF", kind: "folder", source: `${HOME}/skills/pdf`, agents: ["claude"] },
  ],
});

function server(lang, posts, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st) });
    if (url.pathname.startsWith("/api/library/skills/")) {
      const what = url.pathname.split("/").pop(), body = req.postDataJSON();
      posts.push({ what, body });
      if (what === "hand-over") st.moved.push(...body.names);
      return route.fulfill({ json: { ...lib(st), result: { changed: ["codex", "gemini"] } } });
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

const scrolls = (page) => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => (e.id || e.className) + ":" + e.scrollTop).concat("doc:" + document.scrollingElement.scrollTop).join(" "));

const words = {
  en: { one: "Manage in magpie", all: "Manage 2 in magpie", ask: "Manage orca in magpie?", askAll: "Manage 2 skills in magpie?", go: "Move into the library", cancel: "Cancel",
    where: "~/.agents/skills/orca", lib: "~/.magpie/library/skills/orca", link: "~/aimer/grill", done: "orca is in magpie's library now", doneAll: "2 skills are in magpie's library now" },
  zh: { one: "交给 magpie 管理", all: "将 2 个交给 magpie 管理", ask: "将 orca 交给 magpie 管理？", askAll: "将 2 个技能交给 magpie 管理？", go: "移入资源库", cancel: "取消",
    where: "~/.agents/skills/orca", lib: "~/.magpie/library/skills/orca", link: "~/aimer/grill", done: "orca 已交给 magpie 的资源库管理", doneAll: "2 个技能已交给 magpie 的资源库管理" },
  "zh-TW": { one: "交給 magpie 管理", all: "將 2 個交給 magpie 管理", ask: "將 orca 交給 magpie 管理？", askAll: "將 2 個技能交給 magpie 管理？", go: "移入資源庫", cancel: "取消",
    where: "~/.agents/skills/orca", lib: "~/.magpie/library/skills/orca", link: "~/aimer/grill", done: "orca 已交給 magpie 的資源庫管理", doneAll: "2 個技能已交給 magpie 的資源庫管理" },
  ja: { one: "magpie で管理", all: "2 件を magpie で管理", ask: "orca を magpie で管理しますか？", askAll: "2 件のスキルを magpie で管理しますか？", go: "ライブラリへ移す", cancel: "キャンセル",
    where: "~/.agents/skills/orca", lib: "~/.magpie/library/skills/orca", link: "~/aimer/grill", done: "orca は magpie のライブラリで管理されています", doneAll: "2 件のスキルが magpie のライブラリで管理されています" },
  de: { one: "In magpie verwalten", all: "2 in magpie verwalten", ask: "orca in magpie verwalten?", askAll: "2 Skills in magpie verwalten?", go: "In die Bibliothek verschieben", cancel: "Abbrechen",
    where: "~/.agents/skills/orca", lib: "~/.magpie/library/skills/orca", link: "~/aimer/grill", done: "orca wird jetzt in der Bibliothek von magpie verwaltet", doneAll: "2 Skills werden jetzt in der Bibliothek von magpie verwaltet" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": skills in ~/.agents/skills are handed over to the library", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      await browser.close();
    });
    for (const width of [860, 400]) for (const lang of Object.keys(words)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], posts = [], st = { moved: [] };
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (n) => v.locator(".lib-skill", { has: page.locator(".name", { hasText: new RegExp("^" + n) }) });
        await row("orca").waitFor();

        // only the rows kept in ~/.agents/skills have it, in their source line
        const one = row("orca").locator(".lib-src button.lib-handover");
        assert.equal((await one.textContent()).trim(), w.one);
        assert.equal(await row("grill").locator(".lib-src button.lib-handover").count(), 1);
        assert.equal(await row("pdf").locator(".lib-handover").count(), 0, "a skill not kept there has the button");
        // it stays inside its row, at every width
        const rb = await row("orca").boundingBox(), bb = await one.boundingBox();
        assert(bb.x >= rb.x && bb.x + bb.width <= rb.x + rb.width + 1, `the button overflows its row at ${width}px`);
        // and the path beside it still shows
        const pb = await row("orca").locator(".lib-src .lib-path").boundingBox();
        assert(pb.width > 40, `the path gave way to the button at ${width}px: ${pb.width}px`);
        const before = await scrolls(page);

        // it asks first, saying where it goes and how to put it back
        await one.click();
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.ask);
        const said = (await sheet.locator(".lib-confirm").allTextContents()).join(" ");
        for (const x of [w.where, w.lib, "Codex", "Gemini CLI", "magpie library skill hand-back orca"]) assert(said.includes(x), `the sheet doesn't say ${x}: ${said}`);
        await sheet.locator(".bar button", { hasText: w.cancel }).click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts, [], "Cancel posts nothing");

        // the link-only one says the folder stays
        await row("grill").locator(".lib-handover").click();
        await sheet.waitFor();
        assert((await sheet.locator(".lib-confirm").first().textContent()).includes(w.link), "the link's folder isn't named");
        await sheet.locator(".bar button", { hasText: w.cancel }).click();
        await sheet.waitFor({ state: "detached" });

        // with every skill kept in ~/.agents/skills by the setting (ConatusMoon2222
        // on X) the sheet says that setting goes off; without it, it doesn't
        assert(!said.includes("~/.agents/skills now"), "the sheet names the setting while it is off");
        if (lang === "en") {
          st.shared = true;
          await page.reload();
          await page.locator('button[data-view="library"]').click();
          await row("orca").waitFor();
          await row("orca").locator(".lib-handover").click();
          await sheet.waitFor();
          assert.match((await sheet.locator(".lib-confirm").allTextContents()).join(" "), /That setting goes off: magpie's links there are taken away/);
          await sheet.locator(".bar button", { hasText: w.cancel }).click();
          await sheet.waitFor({ state: "detached" });
          st.shared = false;
          await page.reload();
          await page.locator('button[data-view="library"]').click();
          await row("orca").waitFor();
        }

        // moved: one post, the row's button gone, and said
        await one.click();
        await sheet.waitFor();
        const go = sheet.locator(".bar button.primary");
        assert.equal((await go.textContent()).trim(), w.go);
        await go.click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ what: "hand-over", body: { names: ["orca"] } }]);
        await row("orca").locator(".lib-handover").waitFor({ state: "detached" });
        assert.equal((await page.locator("#status").textContent()).trim(), w.done);
        assert.equal(await scrolls(page), before, "the clicks scrolled the page");
        await ctx.close();
      });
    }
    // the skills' heading moves every one at once
    await t.test("all at once", async () => {
      const w = words.en, posts = [], st = { moved: [] };
      const ctx = await browser.newContext({ viewport: { width: 860, height: 900 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server("en", posts, st));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      // Manage n in magpie waits under the heading's ⋯ (library-bar-more)
      const more = page.locator("#view-library .lib-skillshead .lib-barmore");
      await more.click();
      const all = page.locator(".row-menu .rm-item.lib-handover");
      await all.waitFor();
      assert.equal((await all.textContent()).trim(), w.all);
      await all.click();
      const sheet = page.locator("#modal .lib-editor");
      await sheet.waitFor();
      assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.askAll);
      assert.equal(posts.length, 0, "the heading's click posted before asking");
      await sheet.locator(".bar button.primary").click();
      await sheet.waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ what: "hand-over", body: { names: ["orca", "grill"] } }]);
      assert.equal((await page.locator("#status").textContent()).trim(), w.doneAll);
      // none left kept there: the menu has no Manage n any more
      await more.click();
      await page.locator(".row-menu").waitFor();
      assert.equal(await page.locator(".row-menu .rm-item.lib-handover").count(), 0);
      await ctx.close();
    });
    assert.deepEqual(errors, []);
  });
}
