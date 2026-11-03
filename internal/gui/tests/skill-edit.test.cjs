// Run with Node's test runner and Playwright on the module path; see README.md.
// refeccd on Discord: skills handed over to magpie from ~/.agents/skills
// could no longer be edited, and their SKILL.md showed as raw text. A
// skill's row opens its SKILL.md drawn as Markdown, its front matter as
// rows; Edit gives its text to change, ⌘S or Save sends skills/save with
// the text it was opened with, and the skill's other text files are a menu
// away (the app's menu, no native select). A click never scrolls the page.
// In every language, in Chromium and WebKit, wide and narrow. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/refeccd";
const DIR = `${HOME}/.magpie/library/skills/writer`;
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const SKILL = "---\nname: writer\ndescription: >\n  Write release notes\n  people read\n---\n\n# Writer\n\nUse **short** lines.\n\n## Steps\n\n- Read the `diff`\n- Write it\n\n```sh\ngit log -1\n```\n";
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [],
  skills: [{ name: "writer", description: "Write release notes people read", kind: "folder", source: DIR, agents: ["claude", "codex"] }],
});

function server(lang, posts, files) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skill") {
      const file = url.searchParams.get("file") || "SKILL.md";
      return route.fulfill({ json: { file, text: files[file], path: DIR, files: Object.keys(files) } });
    }
    if (url.pathname.startsWith("/api/library/skills/")) {
      const body = req.postDataJSON();
      posts.push({ what: url.pathname.split("/").pop(), body });
      files[body.file] = body.text;
      return route.fulfill({ json: { ...lib(), result: { changed: ["codex"] } } });
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

// a mouse press where the button is, once the dialog has stopped moving
const press = async (page, loc) => {
  await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && a.effect?.target?.closest?.("#modal")));
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const scrolls = (page) => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest("#modal")).map((e) => (e.id || e.className) + ":" + e.scrollTop).concat("doc:" + document.scrollingElement.scrollTop).join(" "));

const words = {
  en: { edit: "Edit", save: "Save", cancel: "Cancel", saved: "SKILL.md saved", reaches: "Saved in the library: every agent given writer gets it" },
  zh: { edit: "编辑", save: "保存", cancel: "取消", saved: "已保存 SKILL.md", reaches: "保存在资料库里：每个装了 writer 的 Agent 都会拿到" },
  "zh-TW": { edit: "編輯", save: "儲存", cancel: "取消", saved: "已儲存 SKILL.md", reaches: "儲存在資料庫裡：每個裝了 writer 的 Agent 都會拿到" },
  ja: { edit: "編集", save: "保存", cancel: "キャンセル", saved: "SKILL.md を保存しました", reaches: "ライブラリに保存：writer を渡したすべてのエージェントに届きます" },
  de: { edit: "Bearbeiten", save: "Speichern", cancel: "Abbrechen", saved: "SKILL.md gespeichert", reaches: "In der Bibliothek gespeichert: jeder Agent mit writer bekommt es" },
};

const shots = process.env.ARTIFACT_DIR;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill's SKILL.md reads as Markdown and is edited and saved in the library", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [860, 400]) for (const lang of Object.keys(words)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], posts = [];
        const files = { "SKILL.md": SKILL, "refs/style.md": "# Style\n", "scripts/run.sh": "echo hi\n" };
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, files));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = page.locator("#view-library .lib-skill", { has: page.locator(".name", { hasText: /^writer/ }) });
        await row.waitFor();
        const before = await scrolls(page);
        await press(page, row.locator(".who"));
        const modal = page.locator("#modal .lib-skillfile");
        const view = modal.locator(".lib-mdview");
        await view.waitFor();

        // drawn, not raw: headings, bold, code, a list; the front matter as rows
        assert.equal((await view.locator("h3").first().textContent()).trim(), "Writer");
        assert.equal(await view.locator("strong", { hasText: "short" }).count(), 1);
        assert.equal(await view.locator("li").count(), 2);
        assert.equal((await view.locator("pre code").textContent()).trim(), "git log -1");
        assert.deepEqual(await view.locator(".lib-fm dt").allTextContents(), ["name", "description"]);
        assert.equal((await view.locator(".lib-fm dd").nth(1).textContent()).trim(), "Write release notes people read");
        assert.equal(await view.innerText().then((s) => s.includes("---")), false, "the front matter's fences are shown");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        if (shots) await page.waitForTimeout(500);
        if (shots) await page.screenshot({ path: path.join(shots, `skill-edit-view-${engine}-${lang}-${width}.png`) });

        // Edit, then ⌘S: what it was opened with goes too
        await press(page, modal.locator(".bar button", { hasText: w.edit }));
        const ta = modal.locator("textarea.lib-skilltext");
        await ta.waitFor();
        assert.equal(await ta.inputValue(), SKILL);
        assert.equal((await modal.locator(".bar .lib-reaches").textContent()).startsWith(w.reaches), true);
        for (const b of await modal.locator(".bar button").all()) {
          const box = await b.boundingBox();
          assert(box && box.x >= 0 && box.x + box.width <= width, `a button out of view: ${JSON.stringify(box)}`);
        }
        if (shots) await page.screenshot({ path: path.join(shots, `skill-edit-edit-${engine}-${lang}-${width}.png`) });
        await ta.evaluate((e) => e.setSelectionRange(e.value.length, e.value.length));
        await ta.type("\nKeep it brief.\n");
        await ta.press("ControlOrMeta+s");
        await view.waitFor();
        assert.deepEqual(posts, [{ what: "save", body: { name: "writer", file: "SKILL.md", text: SKILL + "\nKeep it brief.\n", was: SKILL } }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.saved);
        assert.equal(await view.locator("p", { hasText: "Keep it brief." }).count(), 1, "the saved text is what is shown");

        // another file, from the app's menu; Cancel with nothing changed sends nothing
        await press(page, modal.locator(".lib-filepick"));
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".pm-name").allTextContents(), ["SKILL.md", "refs/style.md", "scripts/run.sh"]);
        await press(page, menu.locator(".pm-item", { hasText: "scripts/run.sh" }));
        await modal.locator("pre.lib-md", { hasText: "echo hi" }).waitFor();
        await press(page, modal.locator(".bar button", { hasText: w.edit }));
        await ta.waitFor();
        await press(page, modal.locator(".bar button", { hasText: w.cancel }));
        await modal.locator("pre.lib-md").waitFor();
        assert.equal(posts.length, 1, "Cancel sent something");

        // Save, the button, on a second file
        await press(page, modal.locator(".lib-filepick"));
        await press(page, menu.locator(".pm-item", { hasText: "refs/style.md" }));
        await view.locator("h3", { hasText: "Style" }).waitFor();
        await press(page, modal.locator(".bar button", { hasText: w.edit }));
        await ta.fill("# Style\n\nShort.\n");
        await press(page, modal.locator(".bar button.primary", { hasText: w.save }));
        await view.locator("p", { hasText: "Short." }).waitFor();
        assert.deepEqual(posts[1], { what: "save", body: { name: "writer", file: "refs/style.md", text: "# Style\n\nShort.\n", was: "# Style\n" } });
        assert.equal(await scrolls(page), before, "a click moved the page");
        const border = await page.evaluate(() => [...document.querySelectorAll("#modal .lib-skillfile, #modal .lib-skillfile *")].filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 || getComputedStyle(e).backgroundImage.includes("gradient")).length);
        assert.equal(border, 0, "a left-border stripe or a gradient");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
