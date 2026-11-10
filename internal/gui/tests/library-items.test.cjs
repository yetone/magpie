// Run with Node's test runner and Playwright on the module path; see README.md.
// The Library's items, three reports: #257, a skill with a long name, link
// and description kept inside its row ("linked from" on one line, the row's
// name not pushed out of it), light and dark, wide and narrow; what the last
// change couldn't write listed above the tabs' content — which agent, what,
// why — Try again posting all/sync with nothing moved, and a chip's write
// changing the list in place, the row clicked where it was; and an agent's
// icon whose picture failed to load at first shown once it does, not left
// empty until the page is loaded again. In English and Chinese. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/loosheng";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const LONG = "ego-browser-with-a-name-long-enough-to-run-past-the-row-and-on-and-on";
const skills = [
  {
    name: LONG, kind: "folder", agents: ["claude", "codex"],
    description: "When you need a browser, read this Skill by default: https://example.com/a/really/long/unbroken/url/that/goes/on/" + "x".repeat(120),
    source: "/Applications/ego lite.app/Contents/Frameworks/ego Framework.framework/Versions/A/Resources/skills/ego-browser/and/further/down",
  },
  ...Array.from({ length: 30 }, (_, i) => ({ name: "s" + String(i).padStart(2, "0"), description: "d", agents: i % 2 ? ["claude"] : ["claude", "codex"], kind: "folder", source: `${HOME}/skills/s${i}` })),
];
const problems = [
  { agent: "claude", what: "instructions", error: `open ${HOME}/.claude/CLAUDE.md: permission denied` },
  { agent: "codex", what: "mcp", error: "config.toml: toml: line 3: expected '=' after a key" },
  { agent: "zcode", what: "skill:" + LONG, error: "ZCode already has a skill of its own called " + LONG },
  { agent: `${HOME}/code/app`, what: "project:", error: "the folder is gone" },
];
const base = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "codex-color"), agent("pi", "Pi", "pi"), agent("zcode", "ZCode", "zcode")],
  instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: structuredClone(skills),
});

function server(lang, theme, posts, opts = {}) {
  let first = 0;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: { ...base(), problems } });
    if (url.pathname === "/api/library/all/sync") {
      posts.push(url.pathname);
      return route.fulfill({ json: { ...base(), result: { changed: ["claude"], problems: [] } } });
    }
    if (url.pathname === "/api/library/skills/agents") {
      const body = req.postDataJSON();
      posts.push(body);
      const more = [...problems, { agent: "pi", what: "skill:" + body.name, error: "read-only file system" }];
      const v = base();
      v.skills.find((s) => s.name === body.name).agents = body.agents;
      return route.fulfill({ json: { ...v, problems: more, result: { changed: [], problems: more.slice(-1) } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    // Claude Code's picture can't be had for a moment after it's first asked for
    if (opts.flaky && url.pathname.endsWith("/claudecode-color.svg")) {
      first ||= Date.now();
      if (Date.now() - first < 300) return route.fulfill({ status: 503, body: "" });
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// each is listed on the tab it is about; another tab names how many wait
// where (#1896: Claude Desktop's skills read as duplicated MCP servers)
const words = {
  en: {
    title: "Some of the Library couldn't be written", again: "Try again",
    lines: ["ZCode · Skill " + LONG, "~/code/app · The project"], away: ["Instructions: 1 more", "MCP servers: 1 more"],
    instr: ["Claude Code · Instructions"], instrAway: ["MCP servers: 1 more", "Skills: 2 more"],
  },
  zh: {
    title: "资源库有部分内容未能写入", again: "重试",
    lines: ["ZCode · 技能 " + LONG, "~/code/app · 项目"], away: ["指令：另有 1 项", "MCP 服务器：另有 1 项"],
    instr: ["Claude Code · 指令"], instrAway: ["MCP 服务器：另有 1 项", "技能：另有 2 项"],
  },
};
const lineOf = (ls) => ls.map((l) => [...l.childNodes].filter((c) => !c.classList.contains("lib-problems-err")).map((c) => c.textContent).join(""));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's items", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, { theme = "light", width = 980, posts = [], flaky = false } = {}) => {
      const ctx = await browser.newContext({ viewport: { width, height: 700 }, colorScheme: theme });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      // only the page's own address: a blob: URL (the grey copies) is left alone
      await page.route("http://magpie.test/**", server(lang, theme, posts, { flaky }));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };
    // every row inside the list and its own box: the label on one line, the
    // name, the description and the path within the row
    const fits = (page) => page.evaluate(() => {
      const bad = [];
      const view = document.querySelector("#view-library");
      if (view.scrollWidth > view.clientWidth + 1) bad.push("the page scrolls sideways");
      for (const row of view.querySelectorAll(".lib-row")) {
        const r = row.getBoundingClientRect();
        const name = row.querySelector(":scope > .who > .name")?.textContent;
        if (row.scrollWidth > row.clientWidth + 1) bad.push(name + ": wider than its row");
        for (const e of row.querySelectorAll(":scope > .who > .name, :scope > .who > .sub, :scope > .who > .lib-src, .lib-src > *")) {
          const b = e.getBoundingClientRect();
          if (b.top < r.top - 0.5 || b.bottom > r.bottom + 0.5 || b.right > r.right + 0.5) bad.push(name + ": " + e.className + " outside its row");
        }
        const label = row.querySelector(".lib-src > span");
        if (label && label.getClientRects().length && label.getBoundingClientRect().height > parseFloat(getComputedStyle(label).lineHeight || "16") * 1.5 + 2) bad.push(name + ": its label wraps");
      }
      const card = view.querySelector(".lib-problems");
      if (card && card.scrollWidth > card.clientWidth + 1) bad.push("the problems card is wider than itself");
      return bad;
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": long names, links and descriptions stay in their rows", async () => {
        for (const theme of ["light", "dark"]) for (const width of [980, 560]) {
          const page = await open(lang, { theme, width });
          const long = page.locator("#view-library .lib-row").filter({ hasText: LONG });
          await long.waitFor();
          assert.deepEqual(await fits(page), [], `${theme}, ${width}px`);
          const label = long.locator(".lib-src > span");
          assert.equal(await label.textContent(), lang === "zh" ? "链接自" : "linked from");
          await page.close();
        }
      });

      await t.test(lang + ": what couldn't be written is listed, Try again moves nothing", async () => {
        const posts = [];
        const page = await open(lang, { posts });
        const card = page.locator("#view-library > .lib-problems");
        await card.waitFor();
        assert.equal(await card.locator(".lib-problems-head b").textContent(), words[lang].title);
        assert.deepEqual(await card.locator("li").evaluateAll(lineOf), words[lang].lines);
        const errs = await card.locator(".lib-problems-err").allTextContents();
        assert.deepEqual(errs, problems.slice(2).map((p) => p.error));
        assert.deepEqual(await card.locator(".lib-problems-away button").allTextContents(), words[lang].away);
        // a dot says it, not a coloured stripe down its side
        const side = await card.evaluate((c) => getComputedStyle(c).borderLeftWidth);
        assert.equal(side, "0px");
        // on every tab, each where it belongs: the button opens its tab
        // and scrolls nothing
        const top0 = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
        await card.locator(".lib-problems-away button").first().click();
        await page.waitForFunction((want) => [...document.querySelectorAll("#view-library > .lib-problems li")].map((l) => l.textContent).join("|").startsWith(want), words[lang].instr[0]);
        assert.deepEqual(await card.locator("li").evaluateAll(lineOf), words[lang].instr);
        assert.deepEqual(await card.locator(".lib-problems-away button").allTextContents(), words[lang].instrAway);
        assert.equal(await page.locator("#view-library .lib-tabs .opt.on").textContent(), lang === "zh" ? "指令" : "Instructions");
        assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top0);
        await page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible").nth(3).click();
        await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();

        const again = card.getByRole("button", { name: words[lang].again });
        const before = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
        await again.click();
        await card.waitFor({ state: "detached" });
        assert.deepEqual(posts, ["/api/library/all/sync"]);
        assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), before);
        await page.close();
      });

      await t.test(lang + ": a chip's write changes the list in place, the row where it was", async () => {
        const posts = [];
        const page = await open(lang, { posts, width: 980 });
        const card = page.locator("#view-library > .lib-problems");
        await card.waitFor();
        // scrolled by the reader's wheel, the card above the view
        await page.mouse.move(400, 400);
        await page.mouse.wheel(0, 600);
        await page.waitForFunction(() => document.querySelector("#view-library").scrollTop > 400);
        await page.waitForTimeout(300);
        const name = await page.evaluate(() => [...document.querySelectorAll("#view-library .lib-row")].find((r) => { const b = r.getBoundingClientRect(); return /^s\d\d$/.test(r.querySelector(".name").textContent) && b.top > 250 && b.bottom < 600; }).querySelector(".name").textContent);
        const row = page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const had = skills.find((s) => s.name === name).agents;
        // the chips fan out under the pointer first, as a reader's hand does
        const b = await row.locator(".lib-agents").boundingBox();
        await page.mouse.move(b.x + 6, b.y + b.height / 2);
        await page.waitForTimeout(350);
        const pi = await row.locator('.lib-ag[data-agent="pi"]').boundingBox();
        const top = await row.evaluate((r) => r.getBoundingClientRect().top);
        await page.mouse.click(pi.x + pi.width / 2, pi.y + pi.height / 2);
        await page.waitForFunction(() => document.querySelectorAll("#view-library > .lib-problems li").length === 3);
        await page.waitForTimeout(100);
        assert.deepEqual(posts, [{ name, agents: [...had, "pi"] }]);
        assert.equal(await card.locator("li").last().textContent(), (lang === "zh" ? "Pi · 技能 " : "Pi · Skill ") + name + "read-only file system");
        const now = await row.evaluate((r) => r.getBoundingClientRect().top);
        assert.ok(Math.abs(now - top) < 1, `the row clicked stays where it was: ${top} → ${now}`);
        await page.close();
      });

      await t.test(lang + ": an agent's icon that failed at first is shown once it loads", async () => {
        const page = await open(lang, { flaky: true });
        await page.waitForFunction(() => {
          const ims = [...document.querySelectorAll('#view-library .lib-ag .ic[data-icon="claudecode-color"] > img:not(.grey)')];
          return ims.length > 0 && ims.every((i) => i.complete && i.naturalWidth > 0);
        }, null, { timeout: 4000 });
        // and a row drawn afterwards has it too
        await page.evaluate(() => { const v = document.querySelector("#view-library"); v.scrollTop = v.scrollHeight; });
        await page.waitForFunction(() => [...document.querySelectorAll('#view-library .lib-ag .ic[data-icon="claudecode-color"] > img:not(.grey)')].every((i) => i.complete && i.naturalWidth > 0), null, { timeout: 4000 });
        await page.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
