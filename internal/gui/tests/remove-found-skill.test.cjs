// Run with Node's test runner and Playwright on the module path; see README.md.
// #1303 (sxwedo): a skill an agent has of its own, listed under "In your
// agents", could only be brought into the library. Its row has a Remove
// (trash) beside Bring in now: it asks first, saying which agents it leaves,
// that its folder goes to the backups (or that a folder it links to stays),
// that a shared entry goes too and another skill by its name stays; then it
// posts skills/remove-found, and the row goes. In English, Chinese,
// Japanese and German, and at 440px wide. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/sx";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const found = [
  { name: "grilling", description: "Grill a plan", agents: ["goose"], shared: `${HOME}/.agents/skills/grilling` },
  { name: "notes", description: "Mine", agents: ["claude"], copies: ["codex"], others: ["gemini"] },
  { name: "dev", description: "Work in progress", agents: ["claude"], link: `${HOME}/src/dev` },
];
const lib = (foundSkills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("goose", "Goose")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills,
});

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(found) });
    if (url.pathname === "/api/library/skills/remove-found") {
      const { name } = req.postDataJSON();
      posts.push(name);
      return route.fulfill({ json: { ...lib(found.filter((f) => f.name !== name)), result: { changed: ["claude", "codex"] } } });
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

const said = {
  en: {
    notes: ["It is taken out of Claude Code, Codex, and its folder is moved to magpie's backups.", "Gemini CLI has another skill by this name; that one stays."],
    dev: ["It is taken out of Claude Code. The folder it was linked from stays where it is."],
    grilling: ["It is taken out of Goose, and its folder is moved to magpie's backups.", `Its entry in ${HOME}/.agents/skills/grilling goes to the backups too, so no agent reads it from there.`],
    removed: "notes removed",
  },
  zh: {
    notes: ["将从 Claude Code, Codex 中移除，文件夹移到 magpie 的备份。", "Gemini CLI 中有另一个同名 Skill，那个保持不动。"],
    dev: ["将从 Claude Code 中移除。它链接到的原文件夹保持不动。"],
    grilling: ["将从 Goose 中移除，文件夹移到 magpie 的备份。", `它在 ${HOME}/.agents/skills/grilling 中的条目也会移到备份，不会再有 Agent 从那里读取。`],
  },
  ja: {
    notes: ["Claude Code, Codex から外され、フォルダは magpie のバックアップに移されます。", "Gemini CLI には同じ名前の別のスキルがあり、そちらはそのまま残ります。"],
    dev: ["Claude Code から外されます。リンク元のフォルダはそのまま残ります。"],
    grilling: ["Goose から外され、フォルダは magpie のバックアップに移されます。", `${HOME}/.agents/skills/grilling にあるエントリもバックアップに移され、どのエージェントもそこから読まなくなります。`],
  },
  de: {
    notes: ["Er wird aus Claude Code, Codex entfernt, und sein Ordner wird in die Backups von magpie verschoben.", "Gemini CLI hat einen anderen Skill mit diesem Namen; dieser bleibt."],
    dev: ["Er wird aus Claude Code entfernt. Der Ordner, auf den er verlinkt war, bleibt, wo er ist."],
    grilling: ["Er wird aus Goose entfernt, und sein Ordner wird in die Backups von magpie verschoben.", `Sein Eintrag in ${HOME}/.agents/skills/grilling kommt ebenfalls in die Backups, sodass kein Agent ihn mehr von dort liest.`],
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": remove a skill an agent has of its own", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, width = 980) => {
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };
    const row = (page, name) => page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
    const ask = async (page, name) => {
      await row(page, name).locator("button.lib-icon.danger").click();
      const ed = page.locator(".lib-editor").filter({ has: page.locator(".lib-confirm") });
      await ed.waitFor();
      const text = await ed.locator(".lib-confirm").allTextContents();
      return { ed, text };
    };

    for (const lang of ["en", "zh", "ja", "de"]) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await open(lang, posts);
        for (const name of ["dev", "grilling"]) {
          const { ed, text } = await ask(page, name);
          assert.deepEqual(text, said[lang][name], name);
          await ed.locator(".bar button:not(.primary)").click(); // Cancel
          await ed.waitFor({ state: "detached" });
        }
        const { ed, text } = await ask(page, "notes");
        assert.deepEqual(text, said[lang].notes);
        await ed.locator(".bar button.primary").click();
        await page.waitForTimeout(300);
        assert.deepEqual(posts, ["notes"], "only the one confirmed is removed");
        assert.equal(await row(page, "notes").count(), 0, "its row is gone");
        assert.equal(await page.locator("#view-library .lib-row").count(), 2);
        if (said[lang].removed) assert.equal((await page.locator("#status").textContent()).trim(), said[lang].removed);
      });
    }

    await t.test("440px wide", async () => {
      const page = await open("de", [], 440);
      for (const name of ["notes", "dev", "grilling"]) {
        const r = row(page, name);
        const box = await r.boundingBox(), rm = await r.locator("button.lib-icon.danger").boundingBox(), bring = await r.locator("button.action").boundingBox();
        assert(rm && rm.width >= 16 && rm.height >= 16, name + ": the trash is too small " + JSON.stringify(rm));
        assert(rm.x + rm.width <= box.x + box.width + 0.5 && bring.x >= box.x - 0.5, name + ": a button sticks out of the row");
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
    });

    assert.deepEqual(errors, []);
  });
}
