// Run with Node's test runner and Playwright on the module path; see README.md.
// zysicyj on X (Windows): the Library's warning listed ten agents, each
// with some forty skills of its own by the library's names, and each had
// to be settled with a click of its own. With more than one agent listed,
// the warning's heading has one quiet "All agents" menu: "Use the
// library's" posts skills/use-library-all, "Keep each agent's own" posts
// skills/keep-own-all, each with every agent's names, in one write. One
// agent alone, or a write that failed, gets no such menu. All five
// languages, Chromium and WebKit, at 980px and 360px. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:\\Users\\zysicyj";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}\\.${id}\\skills` });
const names = Array.from({ length: 6 }, (_, i) => "skill-" + i);
const ownOf = (id, name) => names.map((n) => ({ agent: id, what: "skill:" + n, error: `${name} already has a skill of its own called ${n}`, own: true }));
const own = [...ownOf("claude", "Claude Code"), ...ownOf("codex", "Codex"), ...ownOf("gemini", "Gemini CLI"), ...ownOf("claude-desktop", "Claude Desktop")];
const lib = (problems) => ({
  dir: `${HOME}\\.magpie\\library`, backups: `${HOME}\\.magpie\\backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("claude-desktop", "Claude Desktop")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [],
  skills: names.map((name) => ({ name, description: name, kind: "", agents: ["claude", "codex", "gemini", "claude-desktop"] })),
  problems,
});

function server(lang, posts, problems) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(problems) });
    if (url.pathname === "/api/library/skills/use-library-all" || url.pathname === "/api/library/skills/keep-own-all") {
      posts.push([url.pathname, req.postDataJSON()]);
      // Claude Desktop's are only ever kept: after "use" they still stand
      const left = url.pathname.endsWith("use-library-all") ? ownOf("claude-desktop", "Claude Desktop") : [];
      return route.fulfill({ json: { ...lib(left), result: { changed: ["claude"], problems: left } } });
    }
    if (url.pathname === "/api/library/skills/keep-own") {
      posts.push([url.pathname, req.postDataJSON()]);
      // one agent settled: the others' still stand, in the result too
      const left = problems.filter((p) => p.agent !== req.postDataJSON().agent);
      return route.fulfill({ json: { ...lib(left), result: { changed: [], problems: left } } });
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
  en: { all: "All agents", head: "For every agent listed", use: "Use the library's", keep: "Keep each agent's own", used: "Every agent listed has the library's skills now; their own are kept with the backups", kept: "Every agent listed keeps its own skills" },
  zh: { all: "全部 Agent", head: "对列出的每个 Agent", use: "用资源库的", keep: "保留各 Agent 自己的", used: "列出的每个 Agent 现在都用资源库的技能；它们自己的已存入备份", kept: "列出的每个 Agent 都保留了自己的技能" },
  "zh-TW": { all: "全部 Agent", head: "對列出的每個 Agent", use: "用資源庫的", keep: "保留各 Agent 自己的", used: "列出的每個 Agent 現在都用資源庫的技能；它們自己的已存入備份", kept: "列出的每個 Agent 都保留了自己的技能" },
  ja: { all: "すべてのエージェント", head: "一覧のすべてのエージェントに", use: "ライブラリのものを使う", keep: "各エージェントのものを残す", used: "一覧のすべてのエージェントがライブラリのスキルを使うようになりました。独自のものはバックアップに保存されています", kept: "一覧のすべてのエージェントが独自のスキルを残しました" },
  de: { all: "Alle Agenten", head: "Für jeden aufgeführten Agent", use: "Den der Bibliothek verwenden", keep: "Die eigenen jedes Agents behalten", used: "Jeder aufgeführte Agent hat jetzt die Skills der Bibliothek; seine eigenen liegen bei den Sicherungen", kept: "Jeder aufgeführte Agent behält seine eigenen Skills" },
};
const allOwn = { claude: names, codex: names, gemini: names, "claude-desktop": names };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every agent's own skills settled at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, problems, width = 980) => {
      const ctx = await browser.newContext({ viewport: { width, height: 700 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, problems));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };

    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [980, 360]) {
        for (const [which, route, done, left] of [["use", "/api/library/skills/use-library-all", "used", 1], ["keep", "/api/library/skills/keep-own-all", "kept", 0]]) {
          await t.test(`${lang} at ${width}px: ${w[which]} for every agent`, async () => {
            const posts = [];
            const page = await open(lang, posts, own, width);
            const card = page.locator("#view-library > .lib-problems");
            await card.locator("li").first().waitFor();
            assert.equal(await card.locator("li").count(), 4, "a row an agent");
            const head = card.locator(".lib-problems-head");
            // one control on the heading, the menu, and no Try again
            assert.deepEqual(await head.locator("button").allTextContents(), [w.all]);
            const all = head.locator(".lib-problems-all");
            assert.equal(await all.getAttribute("aria-haspopup"), "menu");
            // the heading's words and the control fit the card
            const fits = await head.evaluate((h) => {
              const c = h.closest(".lib-problems").getBoundingClientRect();
              return [...h.children].every((e) => { const r = e.getBoundingClientRect(); return r.left >= c.left - 0.5 && r.right <= c.right + 0.5; });
            });
            assert(fits, "the heading spills out of the card");
            await all.click();
            const menu = page.locator(".proto-menu");
            await menu.waitFor();
            assert.equal(await menu.locator(".pm-head").textContent(), w.head);
            assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), [w.use, w.keep]);
            await menu.getByRole("menuitemradio", { name: w[which] }).click();
            await page.locator("#status").filter({ hasText: w[done] }).waitFor();
            assert.deepEqual(posts, [[route, { own: allOwn }]]);
            // Claude Desktop's own stand after "use", alone, with no menu
            assert.equal(await card.locator("li").count(), left);
            if (left) assert.equal(await head.locator("button").count(), 0);
            await page.close();
          });
        }
      }

      // one agent's own click says what it did, not the other agents'
      // own skills that still stand (zysicyj on X: each click told of the
      // rest, "Claude Code: … (and 17 more)")
      await t.test(lang + ": one agent's click says what it did", async () => {
        const posts = [];
        const page = await open(lang, posts, own);
        const card = page.locator("#view-library > .lib-problems");
        await card.locator("li").first().waitFor();
        await card.locator("li").nth(1).locator(".lib-problems-fix button").last().click();
        await page.waitForFunction(() => /\b(ok|warn|err)\b/.test(document.querySelector("#status")?.className || ""));
        const said = await page.locator("#status").textContent();
        assert.match(await page.locator("#status").getAttribute("class"), /\bok\b/, said);
        assert.equal(posts.length, 1);
        assert.equal(posts[0][0], "/api/library/skills/keep-own");
        assert(posts[0][1].agent, "the click names its agent");
        assert(!/already has a skill of its own/.test(said), said);
        assert.equal(await card.locator("li").count(), 3);
        await page.close();
      });

      await t.test(lang + ": one agent alone gets no menu", async () => {
        const page = await open(lang, [], ownOf("codex", "Codex"));
        const card = page.locator("#view-library > .lib-problems");
        await card.locator("li").first().waitFor();
        assert.equal(await card.locator(".lib-problems-head button").count(), 0);
        await page.close();
      });

      await t.test(lang + ": beside a write that failed, Try again and no menu", async () => {
        const page = await open(lang, [], [...own, { agent: "codex", what: "mcp:fs", error: "permission denied" }]);
        const card = page.locator("#view-library > .lib-problems");
        await card.locator("li").first().waitFor();
        assert.equal(await card.locator(".lib-problems-all").count(), 0);
        await page.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
