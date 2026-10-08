// Run with Node's test runner and Playwright on the module path; see README.md.
// Repositories their authors tagged magpie-plugin on GitHub (yetone: 在插件页
//能否自动识别打了 magpie-plugin 的 github repo 来显示为非官方插件): Discover
// shows them in a section of their own after the community's, each card
// saying Unofficial, its owner and its stars. One whose package the market
// already lists is the market's card alone. Install sends the spec magpie
// gave: the npm name of one its author published there from the
// repository, else github:owner/repo; one installed by any spec naming the
// repository reads Installed. Its page warns that nobody reviewed it and
// says which of the two is installed, links GitHub (and npm when it is
// npm's), shows the README and when the repository was last pushed. A
// search finds them too. The repositories are /api/plugins/github's real
// answer for four repositories (asked of a magpie built to search topic:opencode-plugin, as
// none is tagged magpie-plugin yet); the API is faked here. English,
// Chinese, Japanese and German, wide and 440px, nothing scrolls on a click.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const REPOS = [
  { spec: "opencode-claude-auth", repo: "griffinmartin/opencode-claude-auth", url: "https://github.com/griffinmartin/opencode-claude-auth", owner: "griffinmartin", ownerAvatar: "https://avatars.githubusercontent.com/u/1090329?v=4", description: "OpenCode plugin that uses your existing Claude Code credentials — no separate login needed. ", stars: 1301, license: "MIT", pushed: "2026-09-22T23:47:15Z", package: "opencode-claude-auth", version: "2.2.1" },
  { spec: "@slkiser/opencode-quota", repo: "slkiser/opencode-quota", url: "https://github.com/slkiser/opencode-quota", owner: "slkiser", ownerAvatar: "https://avatars.githubusercontent.com/u/35721408?v=4", description: "OpenCode quota & tokens usage with zero context window pollution. Supports OpenCode Go, Cursor, GitHub Copilot, OpenAl, Kimi Code, Alibaba Coding Plan, Chutes Al, Google Antigravity, Z.ai Coding Plan and more.", stars: 1000, license: "MIT", pushed: "2026-10-06T13:25:50Z", package: "@slkiser/opencode-quota", version: "5.0.2" },
  { spec: "github:keli-wen/agy-staff", repo: "keli-wen/agy-staff", url: "https://github.com/keli-wen/agy-staff", owner: "keli-wen", ownerAvatar: "https://avatars.githubusercontent.com/u/103916249?v=4", description: "Hire Google's Antigravity CLI (agy) as a fast Gemini staffer for Claude Code and OpenAI Codex.", stars: 723, license: "MIT", pushed: "2026-10-06T16:29:15Z", package: "agy-staff", version: "0.7.4" },
  { spec: "@rama_nigg/open-cursor", repo: "Nomadcxx/opencode-cursor", url: "https://github.com/Nomadcxx/opencode-cursor", owner: "Nomadcxx", ownerAvatar: "https://avatars.githubusercontent.com/u/143774106?v=4", description: "Use Cursor Pro models in OpenCode via HTTP proxy with OAuth", stars: 704, license: "BSD-3-Clause", pushed: "2026-10-02T12:34:45Z", package: "@rama_nigg/open-cursor", version: "2.5.11" },
];
// the market lists the fourth's package: its card is the market's
const LISTINGS = [
  { package: "@rama_nigg/open-cursor", name: "Open Cursor", community: true, npm: { version: "2.5.11", weekly: 900, license: "BSD-3-Clause" } },
];
const README = { "opencode-claude-auth": "# opencode-claude-auth\n\nUses your existing Claude Code credentials.", "github:keli-wen/agy-staff": "# agy-staff\n\nHire agy as a staffer." };

function server(lang, asked) {
  // installed before: the quota plugin, by its repository's page URL
  const installed = [{ spec: "https://github.com/slkiser/opencode-quota", package: "@slkiser/opencode-quota", providers: [], version: "5.0.2", moved: [] }];
  const state = () => ({ bun: true, bunVersion: "1.4.2", plugins: installed, picker: false, movable: [] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: LISTINGS.map((l) => ({ ...l, npm: undefined })) });
    if (url.pathname === "/api/plugins/npm") return json({ npm: { "@rama_nigg/open-cursor": LISTINGS[0].npm } });
    if (url.pathname === "/api/plugins/github") { asked.push(["github"]); return json({ repos: REPOS, topic: "magpie-plugin" }); }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/page") {
      asked.push(["page", url.searchParams.get("name")]);
      return json({ readme: README[url.searchParams.get("name")] || "", updated: "0001-01-01T00:00:00Z" });
    }
    if (url.pathname === "/api/plugins/add") {
      const b = body();
      asked.push(["add", b]);
      const r = REPOS.find((x) => x.spec === b.spec);
      installed.push({ spec: b.spec, package: r.package, providers: [], version: r.version, moved: [] });
      return json(state());
    }
    if (url.pathname === "/api/open") { asked.push(["open", body()]); return route.fulfill({ status: 204 }); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") { asked.push(["fetched", url.href]); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { section: "Unofficial, on GitHub", chip: "Unofficial", install: "Install", installed: "Installed", updated: "Updated", warn: /Unofficial: griffinmartin tagged it magpie-plugin on GitHub/, fromNPM: /installed from npm/, fromGit: /installed from the repository as it stands/ },
  zh: { section: "非官方插件 · GitHub", chip: "非官方", install: "安装", installed: "已安装", updated: "更新于", warn: /非官方插件：griffinmartin 在 GitHub 上给它打了 magpie-plugin 标签/, fromNPM: /从这个仓库发布到 npm/, fromGit: /仓库当前的代码/ },
  ja: { section: "非公式 · GitHub", chip: "非公式", install: "インストール", installed: "インストール済み", updated: "更新日", warn: /非公式：griffinmartin が GitHub で magpie-plugin タグを付けた/, fromNPM: /npm に公開したもの/, fromGit: /リポジトリの現在のコード/ },
  de: { section: "Inoffiziell, auf GitHub", chip: "Inoffiziell", install: "Installieren", installed: "Installiert", updated: "Aktualisiert", warn: /Inoffiziell: griffinmartin hat es auf GitHub mit magpie-plugin getaggt/, fromNPM: /auf npm veröffentlicht/, fromGit: /das Repository, wie es gerade ist/ },
};

const shot = async (loc, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await loc.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

const scrolls = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => (e.id || e.className) + ":" + e.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": repositories tagged magpie-plugin, as unofficial plugins", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [980, 440]) {
        await t.test(lang + " " + width, async () => {
          const w = L[lang];
          const asked = [];
          const page = await (await browser.newContext({ viewport: { width, height: 860 } })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, asked));
          await page.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "discover"); } catch {} });

          await page.goto("http://magpie.test/?view=plugins");
          const view = page.locator("#view-plugins");
          await view.waitFor({ state: "visible" });

          // a section of their own, after the community's, before the field
          const sec = view.locator(".pm-sec", { has: page.locator("h3", { hasText: w.section }) });
          await sec.waitFor();
          const order = await view.locator(".pm-body > *").evaluateAll((es) => es.map((e) => e.className.split(" ")[0]));
          assert.ok(order.indexOf("pm-manual") > order.lastIndexOf("pm-sec"), "the field stays last: " + order);
          assert.match(await sec.locator(".pm-sechead span").innerText(), /magpie-plugin/);
          const cards = sec.locator(".pm-card");
          assert.deepEqual(await cards.locator(".pm-name b").allInnerTexts(), ["opencode-claude-auth", "opencode-quota", "agy-staff"], "the market's own package is its card alone");
          assert.equal(await view.locator(".pm-card", { hasText: "Open Cursor" }).count(), 1);

          const c = cards.first();
          assert.equal((await c.locator(".pm-by").innerText()).trim(), "griffinmartin");
          assert.equal((await c.locator(".pm-chip.warn").innerText()).trim(), w.chip);
          assert.match(await c.locator(".pm-chip.warn").getAttribute("title"), /magpie-plugin/);
          assert.equal((await c.locator(".pm-dl").innerText()).trim(), "1.3k");
          assert.equal((await c.locator(".pm-ver").innerText()).trim(), "v2.2.1");
          // installed by its page's URL: Installed all the same
          await cards.nth(1).locator(".pm-act.done").waitFor();
          assert.match(await cards.nth(1).locator(".pm-act").innerText(), new RegExp(w.installed));
          // nothing cut or spilling at this width
          for (const card of await cards.all()) {
            const fit = await card.evaluate((e) => [e.scrollWidth <= e.clientWidth + 1, ...[...e.querySelectorAll(".pm-chip, .pm-act, .pm-name b")].map((x) => x.getBoundingClientRect().width > 8 && x.getBoundingClientRect().right <= e.getBoundingClientRect().right + 1)]);
            assert.ok(fit.every(Boolean), "fits: " + fit);
          }
          await shot(sec, `plugins-github-${engine}-${lang}-${width}`);

          // its page: the warning, npm's and GitHub's links and the README,
          // when it was pushed
          // a reader scrolls to a card (narrow, below the fold); magpie
          // undoes a scroll nobody asked for, so Playwright's own wouldn't do
          const reach = async (card) => {
            const box = await view.boundingBox();
            await page.mouse.move(box.x + box.width / 2, Math.min(box.y + 200, 800));
            for (let i = 0; i < 60 && !(await card.evaluate((e) => { const r = e.getBoundingClientRect(), v = e.closest(".view").getBoundingClientRect(); return r.top >= v.top && r.bottom <= v.bottom; })); i++) {
              await page.mouse.wheel(0, 80);
              await page.waitForTimeout(15);
            }
          };
          // and clicks it where it is: Playwright's own click scrolls a card
          // already in sight in Chromium at this width, as a reader's doesn't
          const press = async (card) => {
            const b = await card.boundingBox();
            await page.mouse.click(b.x + b.width / 2, b.y + 20);
          };
          await reach(c);
          const before = await scrolls(page);
          await press(c);
          const dlg = page.locator("#modal .pm-detail");
          await dlg.locator(".pm-md", { hasText: "Uses your existing Claude Code credentials." }).waitFor();
          assert.deepEqual(asked.filter(([k]) => k === "page").pop(), ["page", "opencode-claude-auth"]);
          assert.match(await dlg.locator(".pm-note.warn").innerText(), w.warn);
          assert.match(await dlg.locator(".pm-note.warn").innerText(), w.fromNPM);
          assert.equal(await dlg.locator(".pm-links a", { hasText: "npm" }).getAttribute("href"), "https://www.npmjs.com/package/opencode-claude-auth");
          assert.equal(await dlg.locator(".pm-links a", { hasText: "GitHub" }).getAttribute("href"), "https://github.com/griffinmartin/opencode-claude-auth");
          const upd = dlg.locator(".pm-fact", { has: page.locator(".k", { hasText: w.updated }) }).locator(".v");
          assert.match(await upd.innerText(), /2026/, "the repository's last push");
          await shot(page.locator("#modal .dialog"), `plugins-github-page-${engine}-${lang}-${width}`);

          // Install from the page: npm's name
          const install = async (spec) => {
            const n = asked.filter(([k]) => k === "add").length;
            await dlg.locator(".pm-act", { hasText: new RegExp("^" + w.install + "$") }).click();
            for (let i = 0; i < 100 && asked.filter(([k]) => k === "add").length === n; i++) await new Promise((r) => setTimeout(r, 20));
            assert.deepEqual(asked.filter(([k]) => k === "add").pop()[1], { spec });
            await dlg.locator(".pm-act.done").waitFor();
            await dlg.locator(".bar button.text").last().click();
            await page.waitForFunction(() => document.querySelector("#modal").hidden);
          };
          await install("opencode-claude-auth");
          assert.equal(await scrolls(page), before, "a click scrolls nothing");
          await cards.first().locator(".pm-act.done").waitFor();

          // one npm hasn't from its repository: GitHub's, installed from there
          await reach(cards.nth(2));
          await press(cards.nth(2));
          await dlg.locator(".pm-md", { hasText: "Hire agy as a staffer." }).waitFor();
          assert.deepEqual(asked.filter(([k]) => k === "page").pop(), ["page", "github:keli-wen/agy-staff"]);
          assert.match(await dlg.locator(".pm-note.warn").innerText(), w.fromGit);
          assert.equal(await dlg.locator(".pm-links a", { hasText: "npm" }).count(), 0, "npm has no page for it");
          assert.equal(await dlg.locator(".pm-links a", { hasText: "GitHub" }).getAttribute("href"), "https://github.com/keli-wen/agy-staff");
          await install("github:keli-wen/agy-staff");
          await cards.nth(2).locator(".pm-act.done").waitFor();

          // a search finds them too
          await view.locator(".pm-find input").fill("quota");
          const found = view.locator(".pm-sec", { has: page.locator("h3", { hasText: w.section }) });
          await page.waitForFunction(() => document.querySelectorAll("#view-plugins .pm-sec .pm-card").length === 1);
          assert.deepEqual(await found.locator(".pm-name b").allInnerTexts(), ["opencode-quota"]);
          await view.locator(".pm-find input").fill("nothing-like-it");
          await page.waitForFunction(() => !document.querySelector("#view-plugins .pm-sec .pm-card .pm-chip.warn"));

          assert.equal(asked.filter(([k]) => k === "fetched").length, 0, "nothing fetched from outside: " + JSON.stringify(asked.filter(([k]) => k === "fetched")));
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
