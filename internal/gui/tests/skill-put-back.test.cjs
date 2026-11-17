// Run with Node's test runner and Playwright on the module path; see README.md.
// BeiWeiji on X: a skill brought into the library from "In your agents"
// had no way back, only off or Remove, and removed it was gone from the
// agents too. A skill brought in from the agents (back: the agents it came
// from) has Put back in their place of the trash: its sheet says where it
// goes, the button sends skills/remove (which gives it back to them as
// they had it), and it is under "In your agents" again. A skill from
// anywhere else keeps its trash. In every language, in Chromium and
// WebKit, wide and narrow. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/bei";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const lib = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], newSkills: [],
  checkDue: false,
  skills: [
    ...st.back ? [] : [{ name: "notes", description: "My notes", agents: ["claude", "codex"], back: ["claude", "codex"] }],
    { name: "pdf", description: "Read PDFs", kind: "github", source: "https://github.com/a/b/tree/main/pdf", agents: ["claude"] },
  ],
  foundSkills: st.back ? [{ name: "notes", description: "My notes", agents: ["claude", "codex"] }] : [],
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
      if (what === "remove") st.back = true;
      return route.fulfill({ json: { ...lib(st), result: { changed: ["claude", "codex"] } } });
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
  en: { title: "Put back in Claude Code, Codex", head: "Put notes back?", go: "Put it back", in: "In your agents",
    text: "It leaves the library and goes back to Claude Code, Codex as it was before you brought it in.", done: "notes went back to Claude Code, Codex" },
  zh: { title: "放回 Claude Code, Codex", head: "把 notes 放回？", go: "放回", in: "Agent 中已有",
    text: "它会离开资源库，按导入前的样子放回 Claude Code, Codex。", done: "notes 已放回 Claude Code, Codex" },
  "zh-TW": { title: "放回 Claude Code, Codex", head: "把 notes 放回？", go: "放回",
    text: "它會離開資源庫，依匯入前的樣子放回 Claude Code, Codex。", done: "notes 已放回 Claude Code, Codex" },
  ja: { title: "Claude Code, Codex に戻す", head: "notes を戻しますか？", go: "戻す",
    text: "ライブラリから外れ、取り込む前の状態で Claude Code, Codex に戻ります。", done: "notes を Claude Code, Codex に戻しました" },
  de: { title: "Zurück zu Claude Code, Codex", head: "notes zurückgeben?", go: "Zurückgeben",
    text: "Er verlässt die Bibliothek und kommt so zu Claude Code, Codex zurück, wie er vor dem Übernehmen war.", done: "notes ist wieder in Claude Code, Codex" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill brought in from the agents is put back", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [980, 400]) for (const lang of Object.keys(words)) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], posts = [], st = { back: false };
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
        await row("notes").waitFor();

        // the one from the agents: Put back, not a trash; the other keeps its trash
        const pb = row("notes").locator("button.lib-putback");
        assert.equal(await pb.getAttribute("title"), w.title);
        assert.equal(await row("notes").locator("button.lib-icon.danger").count(), 0);
        assert.equal(await row("pdf").locator("button.lib-putback").count(), 0);
        assert.equal(await row("pdf").locator("button.lib-icon.danger").count(), 1);
        const rb = await row("notes").boundingBox(), bb = await pb.boundingBox();
        assert(bb && bb.width >= 16 && bb.x + bb.width <= rb.x + rb.width + 0.5, `Put back sticks out of its row at ${width}px`);

        // its sheet says where it goes; Cancel sends nothing
        await pb.click();
        const ed = page.locator(".lib-putback-sheet");
        await ed.waitFor();
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), w.head);
        assert.equal((await ed.locator(".lib-confirm").textContent()).trim(), w.text);
        await ed.locator(".bar button:not(.primary)").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts, []);

        await pb.click();
        await ed.waitFor();
        const go = ed.locator(".bar button.primary");
        assert.equal((await go.textContent()).trim(), w.go);
        await go.click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ what: "remove", body: { name: "notes" } }]);
        await row("notes").waitFor({ state: "detached" });
        assert.equal((await page.locator("#status").textContent()).trim(), w.done);
        // and it is in the agents' list again
        const found = v.locator(".lib-foundhead");
        await found.waitFor();
        if (w.in) assert.equal((await found.locator(".label").textContent()).trim(), w.in);
        assert.equal(await v.locator(".lib-row .name", { hasText: /^notes$/ }).count(), 1);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
