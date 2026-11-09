// Run with Node's test runner and Playwright on the module path; see README.md.
// biubiu8802 on X: a skill downloaded from GitHub and added to the library
// from its folder couldn't follow GitHub. Installing it from GitHub links
// the library's to it (skill_link.go): the list of what the repository has
// says so before the click, "links to GitHub", with what happens in its
// title, the skill is picked for you, and after the install the status
// says it updates from GitHub now and its own files went to the backups.
// Every language, wide and narrow. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/biubiu";
const probe = {
  source: "biubiu/skills", kind: "github", candidates: [
    { path: "skills/pdf-pro", name: "pdf-pro", description: "PDFs, v2", have: false, links: true },
  ],
};
const lib = () => ({
  dir: `${HOME}/.config/magpie/library`, backups: `${HOME}/.config/magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills` }],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [],
});

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skills/probe") return route.fulfill({ json: probe });
    if (url.pathname === "/api/library/skills/install") {
      posts.push(req.postDataJSON().paths);
      return route.fulfill({ json: { ...lib(), result: { changed: ["claude"], problems: [], installed: [], had: [], skipped: [], linked: ["pdf-pro"], linkedKept: ["pdf-pro"] } } });
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
  en: { tag: "links to GitHub", why: "The library's pdf-pro came from a folder. This one takes its place and it updates from GitHub from then on; files of its own that differ are kept with the backups.", done: "updates from GitHub now: pdf-pro · files of their own kept with the backups: pdf-pro" },
  zh: { tag: "链接到 GitHub", why: "资源库里的 pdf-pro 来自一个文件夹。这个会取代它，之后它从 GitHub 更新；与此不同的原有文件会保留在备份里。", done: "现在从 GitHub 更新：pdf-pro · 原有文件已保留在备份里：pdf-pro" },
  "zh-TW": { tag: "連結到 GitHub", why: "資源庫裡的 pdf-pro 來自一個資料夾。這個會取代它，之後它從 GitHub 更新；與此不同的原有檔案會保留在備份裡。", done: "現在從 GitHub 更新：pdf-pro · 原有檔案已保留在備份裡：pdf-pro" },
  ja: { tag: "GitHub にリンク", why: "ライブラリの pdf-pro はフォルダから追加されたものです。これがその代わりになり、以後は GitHub から更新されます。異なる元のファイルはバックアップに保存されます。", done: "今後は GitHub から更新：pdf-pro · 元のファイルはバックアップに保存：pdf-pro" },
  de: { tag: "mit GitHub verknüpfen", why: "pdf-pro in der Bibliothek stammt aus einem Ordner. Dieser Skill tritt an seine Stelle und wird ab dann von GitHub aktualisiert; abweichende eigene Dateien bleiben in den Backups.", done: "wird jetzt von GitHub aktualisiert: pdf-pro · eigene Dateien in den Backups behalten: pdf-pro" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill the library has from a folder is linked to GitHub by installing it from there", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const run = async (lang, width) => {
      const posts = [];
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      const src = page.locator('#view-library input[data-lib="source"]');
      await src.fill("biubiu/skills");
      await src.press("Enter");
      const row = page.locator("#view-library .lib-pick");
      await row.first().waitFor();
      const w = words[lang];
      const tag = row.locator(".lib-tag");
      assert.equal((await tag.textContent()).trim(), w.tag);
      assert.equal(await tag.getAttribute("title"), w.why);
      const fit = await row.evaluate((r) => {
        const b = r.querySelector(".lib-tag").getBoundingClientRect(), box = r.getBoundingClientRect();
        return { inside: b.right <= box.right + 0.5, over: r.closest(".lib-picks").scrollWidth - r.closest(".lib-picks").clientWidth };
      });
      assert.equal(fit.inside, true, "the tag stays in its row");
      assert.equal(fit.over, 0, "the list doesn't scroll sideways");
      assert.equal(await row.locator("input").isChecked(), true, "picked for you");
      await page.locator("#view-library .lib-probefoot button.primary").click();
      await page.waitForTimeout(300);
      assert.deepEqual(posts, [["skills/pdf-pro"]]);
      const status = page.locator("#status");
      assert.equal((await status.textContent()).trim(), w.done);
      await ctx.close();
    };

    for (const lang of Object.keys(words)) {
      await t.test(lang + ", wide", () => run(lang, 980));
      await t.test(lang + ", narrow", () => run(lang, 420));
    }

    assert.deepEqual(errors, []);
  });
}
