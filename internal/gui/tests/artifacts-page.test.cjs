// Run with Node's test runner and Playwright on the module path; see README.md.
// Work › Artifacts: the pages agents made with Magpie Artifacts, newest
// first. Its sidebar button sits after Library and goes with it when the
// Library is off; in the header's row Library's head leads to it, and
// Settings' search finds it. A row says who made it (an agent, or the page
// saving itself), when, and how many versions it
// has, and a click opens it at the gateway's address; its menu copies the
// link, opens an earlier version, shares it on usemagpie.ai and stops
// sharing it, and deletes it after asking. With none
// there it says what one is, offers asks to copy, and which agents can
// make one. In every language, both engines, a window and a narrow
// browser, with no sideways scroll. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const A = "a".repeat(26), B = "b".repeat(26);
const at = (id, n) => `http://127.0.0.1:3425/a/${id}${n ? "@" + n : ""}/`;
const now = Date.now();
const artifacts = [
  { id: A, title: "Quarterly report", created: new Date(now - 3 * 864e5).toISOString(), updated: new Date(now - 2 * 36e5).toISOString(), url: at(A),
    versions: [
      { n: 2, at: new Date(now - 2 * 36e5).toISOString(), label: "fixed totals", agent: "page", files: 2, size: 900, url: at(A, 2) },
      { n: 1, at: new Date(now - 3 * 864e5).toISOString(), agent: "codex", agentName: "Codex", files: 1, size: 400, url: at(A, 1) },
    ] },
  { id: B, title: "Launch plan", created: new Date(now - 5 * 864e5).toISOString(), updated: new Date(now - 5 * 864e5).toISOString(), url: at(B),
    versions: [{ n: 1, at: new Date(now - 5 * 864e5).toISOString(), agent: "claude", agentName: "Claude Code", files: 0, size: 300, url: at(B, 1) }] },
];

const words = {
  en: { nav: "Artifacts", page: "Saved from the page", versions: "2 versions", empty: "No artifacts yet", copy: "Copy link", earlier: "Earlier versions", del: "Delete", v1: "Version 1",
    share: "Share…", shareGo: "Share", shared: "Shared", copyShared: "Copy shared link", stop: "Stop sharing", ask: "Make a one-page report of this week's git commits, with a chart", can: "Agents that can make one: ", turnOn: "Turn it on in the Library", manage: "Manage", many: "25 agents can make one" },
  zh: { nav: "Artifacts", page: "由页面保存", versions: "2 个版本", empty: "还没有 Artifact", copy: "复制链接", earlier: "之前的版本", del: "删除", v1: "版本 1",
    share: "分享…", shareGo: "分享", shared: "已分享", copyShared: "复制分享链接", stop: "停止分享", ask: "把这周的 git 提交做成一页带图表的周报", can: "可以做 Artifact 的 Agent：", turnOn: "在资源库中开启", manage: "管理", many: "25 个 Agent 可以做 Artifact" },
  "zh-TW": { nav: "Artifacts", page: "由頁面儲存", versions: "2 個版本", empty: "還沒有 Artifact", copy: "複製連結", earlier: "之前的版本", del: "刪除", v1: "版本 1",
    share: "分享…", shareGo: "分享", shared: "已分享", copyShared: "複製分享連結", stop: "停止分享", ask: "把這週的 git 提交做成一頁帶圖表的週報", can: "可以做 Artifact 的 Agent：", turnOn: "在資源庫中開啟", manage: "管理", many: "25 個 Agent 可以做 Artifact" },
  ja: { nav: "Artifacts", page: "ページから保存", versions: "2 個のバージョン", empty: "Artifact はまだありません", copy: "リンクをコピー", earlier: "以前のバージョン", del: "削除", v1: "バージョン 1",
    share: "共有…", shareGo: "共有", shared: "共有中", copyShared: "共有リンクをコピー", stop: "共有を停止", ask: "今週の git コミットをグラフ付きの 1 ページのレポートにして", can: "Artifact を作れるエージェント：", turnOn: "ライブラリでオンにする", manage: "管理", many: "25 個のエージェントが Artifact を作れます" },
  de: { nav: "Artifacts", page: "Von der Seite gespeichert", versions: "2 Versionen", empty: "Noch keine Artifacts", copy: "Link kopieren", earlier: "Frühere Versionen", del: "Löschen", v1: "Version 1",
    share: "Teilen …", shareGo: "Teilen", shared: "Geteilt", copyShared: "Geteilten Link kopieren", stop: "Nicht mehr teilen", ask: "Mach aus den Git-Commits dieser Woche einen einseitigen Bericht mit Diagramm", can: "Agents, die eines erstellen können: ", turnOn: "In der Bibliothek einschalten", manage: "Verwalten", many: "25 Agents können eines erstellen" },
};
const LINK = "https://usemagpie.ai/a/abcdefghijklmnop";

function serve(lang, web, got, list, off) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},featuresOff:${JSON.stringify(off)}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }, { id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/artifacts") return json(list.agents ? { artifacts: list.items, agents: list.agents } : { artifacts: list.items });
    // an empty library, so Library's head is drawn
    if (url.pathname === "/api/library") return json({ dir: "/Users/me/.magpie/library", backups: "/Users/me/.magpie/backups", home: "/Users/me", agents: [], instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [], hooks: [] });
    if (url.pathname === "/api/artifacts/delete") { const { id } = req.postDataJSON(); got.deleted.push(id); list.items = list.items.filter((a) => a.id !== id); return json({ deleted: true }); }
    if (url.pathname === "/api/artifacts/share") { const { id } = req.postDataJSON(); got.shared.push(id); return json({ url: LINK, version: 2 }); }
    if (url.pathname === "/api/artifacts/unshare") { got.unshared.push(req.postDataJSON().id); return json({ unshared: true }); }
    if (url.pathname === "/api/open") { got.opened.push(req.postDataJSON().url); return json({}); }
    if (url.pathname === "/api/copy") { got.copied.push(req.postDataJSON().text); return json({}); }
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

// until waits for what the page sent to arrive here, not for a duration
async function until(fn) {
  for (let i = 0; i < 250 && !(await fn()); i++) await new Promise((r) => setTimeout(r, 20));
}

async function openPage(browser, { lang, web, width, items, off = [], agents }) {
  const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => { if (m.type() === "error" || /scroll not asked for/.test(m.text())) errors.push(m.text()); });
  const got = { opened: [], copied: [], deleted: [], shared: [], unshared: [] };
  const list = { items: structuredClone(items), agents };
  // in a browser an artifact opens in a new tab, not through /api/open
  await page.exposeFunction("__opened", (u) => { got.opened.push(u); });
  await page.addInitScript(() => { window.open = (u) => { window.__opened(String(u)); return null; }; });
  await page.route("**/*", serve(lang, web, got, list, off));
  await page.goto("http://magpie.test/?view=artifacts");
  return { context, page, errors, got };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Artifacts lists, opens, copies, opens a version and deletes`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [web, width] of [[false, 1000], [false, 560], [true, 420]]) {
        const where = `${web ? "browser" : "window"} ${width}px`;
        const { context, page, errors, got } = await openPage(browser, { lang, web, width, items: artifacts });
        const nav = page.locator('#nav button[data-view="artifacts"]');
        if (!web || width > 600) {
          assert.equal((await nav.textContent()).trim(), w.nav, where);
          assert.equal(await nav.evaluate((b) => b.previousElementSibling?.dataset.view), "library", `${where}: after Library`);
        }
        const rows = page.locator("#view-artifacts .art-row");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2, where);
        assert.equal(await rows.nth(0).locator(".name").textContent(), "Quarterly report", where);
        const sub = await rows.nth(0).locator(".sub").textContent();
        assert.ok(sub.startsWith(w.page + " · ") && sub.endsWith(" · " + w.versions), `${where}: the page saved the newest: ${sub}`);
        assert.ok((await rows.nth(1).locator(".sub").textContent()).startsWith("Claude Code · "), where);
        assert.ok(!(await rows.nth(1).locator(".sub").textContent()).includes(w.versions.slice(2)), `${where}: one version says no count`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${where}: scrolls sideways`);
        assert.ok(await page.locator("#view-artifacts .art-head").isHidden(), `${where}: a find field for two`);

        await rows.nth(0).locator(".name").click();
        await until(() => got.opened.length);
        assert.deepEqual(got.opened, [at(A)], `${where}: the row opens the latest`);

        const more = rows.nth(0).locator(".art-more");
        await more.click();
        await page.locator(".row-menu .rm-item", { hasText: w.copy }).click();
        await until(() => got.copied.length);
        assert.deepEqual(got.copied, [at(A)], `${where}: copies the link`);
        assert.equal(got.opened.length, 1, `${where}: the menu's click opened the row`);

        await more.click();
        await page.locator(".row-menu .rm-item", { hasText: w.earlier }).click();
        const items = page.locator(".proto-menu .pm-item");
        assert.equal(await items.count(), 2, where);
        assert.ok((await items.nth(1).locator(".pm-name").textContent()).startsWith(w.v1), where);
        assert.ok((await items.nth(0).locator(".pm-note").textContent()).startsWith(w.page + " · "), `${where}: version 2 was saved from the page`);
        assert.ok((await items.nth(1).locator(".pm-note").textContent()).startsWith("Codex · "), `${where}: version 1 by Codex`);
        await items.nth(1).click();
        await until(() => got.opened.length >= 2);
        assert.deepEqual(got.opened, [at(A), at(A, 1)], `${where}: opens version 1`);

        // one version: no Earlier versions
        await rows.nth(1).locator(".art-more").click();
        assert.equal(await page.locator(".row-menu .rm-item", { hasText: w.earlier }).count(), 0, where);
        await page.keyboard.press("Escape");

        await rows.nth(0).locator(".art-more").click();
        await page.locator(".row-menu .rm-item", { hasText: w.del }).click();
        const dialog = page.locator("dialog.action-confirm");
        await dialog.waitFor();
        assert.ok((await dialog.locator("h2").textContent()).includes("Quarterly report"), where);
        await dialog.locator("button.danger-fill").click();
        await page.waitForFunction(() => document.querySelectorAll("#view-artifacts .art-row").length === 1);
        assert.deepEqual(got.deleted, [A], where);
        assert.equal(got.opened.length, 2, `${where}: deleting opened nothing`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    });

    test(`${engine} ${lang}: Artifacts shares one on usemagpie.ai after asking, and stops sharing it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [web, width] of [[false, 1000], [true, 420]]) {
        const where = `${web ? "browser" : "window"} ${width}px`;
        const { context, page, errors, got } = await openPage(browser, { lang, web, width, items: artifacts });
        const row = page.locator("#view-artifacts .art-row").nth(0);
        await row.waitFor();
        assert.ok(!(await row.locator(".sub").textContent()).includes(w.shared), `${where}: shared before sharing`);
        const item = (name) => page.locator(".row-menu .rm-item", { hasText: name });
        await row.locator(".art-more").click();
        assert.equal(await item(w.copyShared).count(), 0, `${where}: a shared link to copy before sharing`);
        await item(w.share).click();
        const dialog = page.locator("dialog.action-confirm");
        await dialog.waitFor();
        assert.ok((await dialog.locator("p").textContent()).includes("usemagpie.ai"), `${where}: the ask names where it goes`);
        // sharing loses nothing: its button isn't the red one
        assert.equal(await dialog.locator("button.danger-fill").count(), 0, `${where}: a red Share`);
        // Cancel shares nothing
        await dialog.locator("button.text:not(.primary)").click();
        await dialog.waitFor({ state: "detached" });
        assert.deepEqual(got.shared, [], `${where}: shared on Cancel`);
        await row.locator(".art-more").click();
        await item(w.share).click();
        await dialog.locator("button.primary", { hasText: w.shareGo }).click();
        await until(() => got.copied.length);
        assert.deepEqual(got.shared, [A], where);
        assert.deepEqual(got.copied, [LINK], `${where}: the shared link is copied`);
        assert.ok((await row.locator(".sub").textContent()).includes(w.shared), `${where}: the row says it is shared`);
        await row.locator(".art-more").click();
        assert.equal(await item(w.share).count(), 0, `${where}: Share… on a shared one`);
        await item(w.copyShared).click();
        await until(() => got.copied.length >= 2);
        assert.deepEqual(got.copied, [LINK, LINK], where);
        await row.locator(".art-more").click();
        await item(w.stop).click();
        await dialog.waitFor();
        await dialog.locator("button.danger-fill").click();
        await until(() => got.unshared.length);
        assert.deepEqual(got.unshared, [A], where);
        await page.waitForFunction((s) => !document.querySelector("#view-artifacts .art-row .sub").textContent.includes(s), w.shared);
        assert.equal(got.opened.length, 0, `${where}: sharing opened the row`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${where}: scrolls sideways`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    });

    test(`${engine} ${lang}: Artifacts says when there are none, and goes with the Library`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      // with none, it says what one is: three asks to copy, which agents
      // can make one, and the way to the Library's server
      for (const [web, width] of [[false, 1000], [true, 420]]) {
        const where = `${web ? "browser" : "window"} ${width}px`;
        const { context, page, errors, got } = await openPage(browser, { lang, web, width, items: [], agents: ["Claude Code", "Codex"] });
        const empty = page.locator("#view-artifacts .art-empty");
        await empty.locator("b").waitFor();
        assert.equal(await empty.locator("b").textContent(), w.empty, where);
        const asks = empty.locator(".art-ask");
        assert.equal(await asks.count(), 3, where);
        assert.equal(await asks.first().textContent(), w.ask, where);
        const before = await asks.first().boundingBox();
        await asks.first().click();
        await until(() => got.copied.length);
        assert.deepEqual(got.copied, [w.ask], `${where}: the ask is copied as shown`);
        // the words stay; only the glyph turns to a check (owner, 11-24:
        // the button was left an empty box with a check)
        const glyph = () => asks.first().evaluate((b) => b.querySelector("path")?.getAttribute("d"));
        await until(async () => (await glyph()) === (await page.evaluate(() => CHECK)));
        assert.equal(await glyph(), await page.evaluate(() => CHECK), `${where}: a check once copied`);
        assert.equal(await asks.first().textContent(), w.ask, `${where}: the words stay once copied`);
        const after = await asks.first().boundingBox();
        assert.ok(Math.abs(after.width - before.width) < 1 && Math.abs(after.height - before.height) < 1, `${where}: the button kept its size`);
        await until(async () => (await glyph()) !== (await page.evaluate(() => CHECK)));
        assert.equal(await asks.first().locator("svg").count(), 1, `${where}: one glyph after`);
        // the names joined as the language joins a list
        const both = await page.evaluate(() => new Intl.ListFormat(intlLang(), { type: "conjunction" }).format(["Claude Code", "Codex"]));
        assert.ok(lang === "en" ? both === "Claude Code and Codex" : !both.includes(" and "), `${where}: ${both}`);
        assert.equal(await empty.locator(".art-who").textContent(), w.can + both + " · " + w.manage, where);
        const box = await empty.boundingBox();
        for (let i = 0; i < 3; i++) {
          const b = await asks.nth(i).boundingBox();
          assert.ok(b.x >= box.x && b.x + b.width <= box.x + box.width + 0.5, `${where}: ask ${i} inside the page`);
        }
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${where}: scrolls sideways`);
        await empty.locator(".art-who button", { hasText: w.manage }).click();
        await page.locator("#view-library").waitFor();
        assert.ok(await page.locator("#view-artifacts").isHidden(), `${where}: Manage stayed`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
      {
        const { context, page, errors } = await openPage(browser, { lang, web: false, width: 1000, items: [], agents: [] });
        const go = page.locator("#view-artifacts .art-who button");
        await go.waitFor();
        assert.equal(await go.textContent(), w.turnOn);
        assert.deepEqual(errors, []);
        await context.close();
      }
      {
        // many agents: a count on one line, the names on hover
        const names = Array.from({ length: 25 }, (_, i) => "Agent " + String(i).padStart(2, "0"));
        const { context, page, errors } = await openPage(browser, { lang, web: false, width: 1000, items: [], agents: names });
        const who = page.locator("#view-artifacts .art-who");
        await who.waitFor();
        assert.equal(await who.textContent(), w.many + " · " + w.manage);
        assert.ok((await who.locator("span").getAttribute("title")).includes("Agent 24"));
        // one line: the count and Manage side by side, the line no taller
        // than two of the count
        const [n, m, all] = await Promise.all([who.locator("span").boundingBox(), who.locator("button").boundingBox(), who.boundingBox()]);
        assert.ok(Math.abs(n.y + n.height / 2 - (m.y + m.height / 2)) < 4 && all.height < n.height * 2, `one line: ${JSON.stringify({ n, m, all })}`);
        assert.deepEqual(errors, []);
        await context.close();
      }
      {
        // the library unread: the page says nothing of the agents
        const { context, page, errors } = await openPage(browser, { lang, web: false, width: 1000, items: [] });
        await page.locator("#view-artifacts .art-empty .art-ask").first().waitFor();
        assert.equal(await page.locator("#view-artifacts .art-who").count(), 0);
        assert.deepEqual(errors, []);
        await context.close();
      }
      {
        const { context, page } = await openPage(browser, { lang, web: false, width: 1000, items: artifacts, off: ["library"] });
        await page.locator('#nav button[data-view="agents"]').waitFor();
        assert.ok(await page.locator('#nav button[data-view="artifacts"]').isHidden(), "the button shows with the Library off");
        assert.ok(await page.locator("#view-artifacts").isHidden(), "the page shows with the Library off");
        await context.close();
      }
    });

    // the sidebar has the tab; the header's row has no room for it, as for
    // Routing groups, so there Library's head opens the page and Library's
    // tab stands for both; a phone's head shows its glyph alone, the name
    // its label; and Settings' search finds the page by its name
    test(`${engine} ${lang}: Artifacts from the sidebar, Library's head and Settings' search`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [web, width] of [[false, 1200], [false, 860], [true, 420]]) {
        const where = `${web ? "browser" : "window"} ${width}px`;
        const { context, page, errors } = await openPage(browser, { lang, web, width, items: artifacts });
        await page.locator("#view-artifacts .art-row").first().waitFor();
        const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
        assert.equal(side, width >= 1024, `${where}: the sidebar from 1024 points`);
        const tab = page.locator('#nav button[data-view="artifacts"]');
        const on = () => page.evaluate(() => [...document.querySelectorAll("#nav button[data-view].on")].map((b) => b.dataset.view));
        assert.equal(await tab.isVisible(), side, `${where}: the tab where there is room`);
        assert.deepEqual(await on(), [side ? "artifacts" : "library"], `${where}: the tab shown as the page's`);

        await page.goto("http://magpie.test/?view=library");
        const into = page.locator("#view-library .lib-head .lib-toart");
        await page.locator("#view-library .lib-head").waitFor();
        assert.equal(await into.isVisible(), !side, `${where}: Library's head opens it where there is no tab`);
        if (side) await tab.click();
        else {
          assert.equal(await into.getAttribute("aria-label"), w.nav, where);
          assert.equal(await into.locator("span:not(.lib-mini)").isVisible(), !web, `${where}: the name beside the glyph, a phone's glyph alone`);
          await into.click();
        }
        await page.locator("#view-artifacts .art-row").first().waitFor();
        assert.equal(await page.locator("#view-library").isHidden(), true, where);
        assert.deepEqual(await on(), [side ? "artifacts" : "library"], `${where}: lit after the click`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${where}: scrolls sideways`);

        if (!web) {
          await page.goto("http://magpie.test/?view=settings");
          await page.locator("#setQ").fill(w.nav);
          const row = page.locator('#setElse .find-page[data-page="artifacts"] .find-row[data-key="artifacts"]');
          await row.waitFor();
          assert.equal(await row.locator(".name").textContent(), w.nav, where);
          await row.locator("button").first().click();
          await page.locator("#view-artifacts .art-row").first().waitFor();
        }
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    });

    test(`${engine} ${lang}: Artifacts finds by title once there are many`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const many = Array.from({ length: 10 }, (_, i) => ({ ...artifacts[1], id: String.fromCharCode(99 + i).repeat(26), title: i === 7 ? "Pricing table" : `Page ${i}` }));
      const { context, page, errors } = await openPage(browser, { lang, web: false, width: 1000, items: many });
      await page.locator("#view-artifacts .art-row").first().waitFor();
      const q = page.locator("#artQ");
      assert.ok(await q.isVisible());
      await q.fill("pricing");
      assert.equal(await page.locator("#view-artifacts .art-row").count(), 1);
      assert.equal(await page.locator("#view-artifacts .art-row .name").textContent(), "Pricing table");
      assert.deepEqual(errors, []);
      await context.close();
    });
  }
}
