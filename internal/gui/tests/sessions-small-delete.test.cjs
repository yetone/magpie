// Run with Node's test runner and Playwright on the module path; see README.md.
// The small sessions deleted together (yansheng1003 on X: 在会话管理中增加一个
// 低于10k/20k的会话删除的功能). While picking, the bar over the list has By
// tokens, magpie's own menu (never a native select) of 10K, 20K, 50K and
// 100K, each with how many of the sessions shown it picks. A choice picks
// every deletable session shown that used fewer tokens than it, counted as
// the row shows them (input and output; cache only with the Usage page's
// "with cache"), and only those. One written to in the last minute or
// streaming now is never picked, nor one magpie can't delete, and a filter
// picks among the ones it shows. Delete asks in magpie's dialog how many go,
// by what size, and that they go to magpie's trash, which puts them back;
// Cancel posts nothing; Delete posts sessions/delete with those ids. In all
// five languages, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, title, min, input, output, more = {}) => ({
  agent: "codex", id, cwd: "/work/app", title, start: at(min + 5), last: at(min), input, output,
  models: [{ model: "gpt-5.5", input, output, priced: true }],
  resume: `codex resume ${id}`, path: `~/.codex/sessions/2026/11/01/rollout-${id}.jsonl`, size: 4096, messages: 4, files: 1, deletable: true, ...more,
});

function fixture() {
  return {
    codex: [
      sess("s-run", "still running", 0.2, 300, 200),      // 500 tokens, written to just now
      sess("s-live", "streaming now", 5, 800, 200),       // 1K, its reply streaming through the gateway
      sess("s-tiny", "say hi", 30, 1200, 300),            // 1.5K
      sess("s-none", "no tokens", 40, 0, 0),              // —
      sess("s-cache", "mostly cache", 50, 3000, 1000, { cache_read: 400000 }), // 4K, 404K with cache
      sess("s-mid", "fix the login form", 60, 12000, 3000), // 15K
      sess("s-edge", "exactly twenty", 70, 15000, 5000),  // 20K: not under 20K
      sess("s-big", "refactor the router", 80, 60000, 20000), // 80K
      sess("s-ro", "read only small", 90, 100, 100, { deletable: false, read_only: true }),
    ],
    trash: [],
  };
}

function serve(lang, calls) {
  const store = fixture();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/speeds") {
      const curve = Array.from({ length: 120 }, (_, i) => (i < 110 ? null : 60));
      return json([{ agent: "codex", session: "s-live", live: true, rated: true, now: 60, avg: 60, peak: 60, output: 200, curve, idle: 0 }]);
    }
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "codex", count: store.codex.length, deletable: true, name: "Codex", icon: "codex" }],
        agent: "codex", sessions: store.codex, terminal: false, trash: store.trash, trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = req.postDataJSON();
      calls.push({ path: "delete", body });
      const out = { deleted: [], refused: [] };
      for (const id of body.ids) {
        const s = store.codex.find((x) => x.id === id);
        store.codex = store.codex.filter((x) => x.id !== id);
        store.trash.push({ key: `codex/20261101-${id}`, agent: "codex", id, title: s.title, cwd: s.cwd, last: s.last, deleted: at(0), size: s.size,
          items: [{ from: s.path, name: "0-" + id + ".jsonl" }], name: "Codex", icon: "codex" });
        out.deleted.push(id);
      }
      return json(out);
    }
    if (url.pathname.startsWith("/api/")) {
      if (req.method() === "POST" && url.pathname !== "/api/update/using" && url.pathname !== "/api/page-away") calls.push({ path: url.pathname });
      return json({});
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { nav: "Sessions", by: "By tokens", head: "Select sessions under", tok: (n) => `${n} tokens`, k: (n) => n === 1 ? "1 session" : `${n} sessions`,
    picked: (n) => `${n} selected`, del: "Delete", cancel: "Cancel", ask: (n) => `Delete ${n} sessions?`, each: (n) => `Each used fewer than ${n} tokens`,
    trash: "Trash puts them back", moved: (n) => `${n} sessions moved to magpie's trash` },
  zh: { nav: "会话", by: "按 token 数", head: "选中 token 数少于", tok: (n) => `${n} token`, k: (n) => `${n} 个会话`,
    picked: (n) => `已选 ${n} 个`, del: "删除", cancel: "取消", ask: (n) => `删除这 ${n} 个会话？`, each: (n) => `每个会话都少于 ${n} token`,
    trash: "回收站", moved: (n) => `${n} 个会话已移到 magpie 的回收站` },
  "zh-TW": { nav: "會話", by: "依 token 數", head: "選取 token 數少於", tok: (n) => `${n} token`, k: (n) => `${n} 個會話`,
    picked: null, del: "刪除", cancel: "取消", ask: (n) => `刪除這 ${n} 個會話？`, each: (n) => `每個會話都少於 ${n} token`, trash: "回收", moved: null },
  ja: { nav: null, by: "トークン数で", head: "トークン数がこれ未満のセッションを選択", tok: (n) => `${n} トークン`, k: (n) => `${n} 件のセッション`,
    picked: null, del: null, cancel: null, ask: (n) => `${n} 件のセッションを削除しますか？`, each: (n) => `いずれも ${n} トークン未満`, trash: null, moved: null },
  de: { nav: null, by: "Nach Tokens", head: "Sitzungen auswählen mit weniger als", tok: (n) => `${n} Token`, k: (n) => n === 1 ? "1 Sitzung" : `${n} Sitzungen`,
    picked: null, del: null, cancel: null, ask: (n) => `${n} Sitzungen löschen?`, each: (n) => `Jede mit weniger als ${n} Token`, trash: null, moved: null },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: the sessions under a size are picked from By tokens and deleted to magpie's trash`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 620 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-small-delete.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      // the nav's word in each language, from the page's own strings
      const nav = await page.evaluate(() => t("Sessions"));
      await page.locator("#nav").getByRole("button", { name: nav, exact: true }).click();
      const view = page.locator("#view-sessions");
      await view.locator('.row.sm-sess[data-id="s-big"]').waitFor();
      // the streaming one's speed is in before anything is picked
      await view.locator('.row.sm-sess[data-id="s-live"] .sm-speed').waitFor();
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      const count = async () => (await view.locator(".sm-bar .sm-count").textContent()).trim();
      const ticked = () => view.locator(".row.sm-sess input.sm-check").evaluateAll((cs) => cs.filter((c) => c.checked).map((c) => c.closest(".row").dataset.id).sort());

      // not until Select
      assert.equal(await view.locator(".sm-small").count(), 0);
      await view.locator(".sm-select").click();
      const by = view.locator(".sm-bar .sm-small");
      assert.equal((await by.textContent()).trim(), w.by);
      assert.equal(await by.getAttribute("aria-haspopup"), "menu");
      assert.equal(await page.locator("#view-sessions select").count(), 0, "no native select");

      // the menu: magpie's own, each size with how many it picks
      const menu = page.locator(".proto-menu.sm-small-menu");
      const open = async () => { await by.click(); await menu.waitFor(); };
      await open();
      assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.head);
      assert.deepEqual(await menu.locator(".pm-name").allTextContents(), ["10K", "20K", "50K", "100K"].map(w.tok));
      // under 10K: tiny, none, cache (4K without cache); under 20K mid too;
      // 50K edge (20K) too; 100K big too
      assert.deepEqual(await menu.locator(".pm-note").allTextContents(), [3, 4, 5, 6].map(w.k));
      const choose = async (i) => { await menu.locator(".pm-item").nth(i).click(); await menu.waitFor({ state: "detached" }); };
      await choose(1);
      assert.deepEqual(await ticked(), ["s-cache", "s-mid", "s-none", "s-tiny"], "under 20K, never the running, streaming or read-only one");
      assert.equal(await count(), await tr("{n} selected", { n: 4 }));
      if (w.picked) assert.equal(await count(), w.picked(4));

      // a smaller size picks only those, not added to the last
      await open();
      await choose(0);
      assert.deepEqual(await ticked(), ["s-cache", "s-none", "s-tiny"]);

      // Delete asks how many, by what size, and that Trash puts them back;
      // Cancel posts nothing
      const ask = page.locator("#modal .sm-ask");
      await view.locator(".sm-bar .sm-delete").click();
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask(3));
      assert.equal((await ask.locator(".sm-ask-small").textContent()).trim(), w.each("10K"));
      // one line across the dialog, not squeezed into its first column
      assert(await ask.locator(".sm-ask-small").evaluate((e) => e.getBoundingClientRect().height < 24 && e.getBoundingClientRect().width > e.parentElement.getBoundingClientRect().width * 0.6), "the size line spans the dialog");
      if (process.env.ARTIFACT_DIR) { await page.waitForTimeout(600); await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-ask.png`) }); }
      const confirm = await ask.locator(".lib-confirm").textContent();
      assert(confirm.includes("~/Library/Application Support/magpie/trash/sessions"), "they go to magpie's trash");
      if (w.trash) assert(confirm.includes(w.trash), "it says Trash puts them back: " + confirm);
      await ask.getByRole("button", { name: await tr("Cancel"), exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls, [], "Cancel deletes nothing");

      // picked by hand after, the dialog no longer names a size
      await view.locator('.row.sm-sess[data-id="s-mid"] input.sm-check').check();
      await view.locator(".sm-bar .sm-delete").click();
      await ask.waitFor();
      assert.equal(await ask.locator(".sm-ask-small").count(), 0);
      await ask.getByRole("button", { name: await tr("Cancel"), exact: true }).click();
      await ask.waitFor({ state: "detached" });

      // under a filter, only the ones it shows
      await view.locator(".sm-filter").fill("login");
      await page.waitForFunction(() => document.querySelectorAll("#view-sessions .row.sm-sess").length === 1);
      await open();
      assert.deepEqual(await menu.locator(".pm-note").allTextContents(), [0, 1, 1, 1].map(w.k));
      await choose(1);
      assert.equal(await count(), await tr("{n} selected", { n: 1 }));
      await view.locator(".sm-filter").fill("");
      await view.locator(".sm-filter").dispatchEvent("input");
      await view.locator('.row.sm-sess[data-id="s-big"]').waitFor();
      assert.deepEqual(await ticked(), ["s-mid"]);

      // under 20K, deleted: those ids posted, the rest stay
      await open();
      await choose(1);
      await view.locator(".sm-bar .sm-delete").click();
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask(4));
      assert.equal((await ask.locator(".sm-ask-small").textContent()).trim(), w.each("20K"));
      await ask.locator("button.danger-fill").click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 1);
      assert.deepEqual(calls[0].body.agent, "codex");
      assert.deepEqual(calls[0].body.ids.slice().sort(), ["s-cache", "s-mid", "s-none", "s-tiny"]);
      await view.locator('.row.sm-sess[data-id="s-tiny"]').waitFor({ state: "detached" });
      const moved = await tr("{n} sessions moved to magpie's trash", { n: 4 });
      if (w.moved) assert.equal(moved, w.moved(4));
      await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, moved);
      assert.deepEqual(await view.locator(".row.sm-sess").evaluateAll((rs) => rs.map((r) => r.dataset.id).sort()), ["s-big", "s-edge", "s-live", "s-ro", "s-run"]);

      // the bar fits a narrow window: nothing spills out of it
      await page.setViewportSize({ width: 420, height: 620 });
      await page.waitForTimeout(100);
      const spill = await view.locator(".sm-bar").evaluate((b) => [...b.children].filter((c) => !c.hidden && c.getBoundingClientRect().right > b.getBoundingClientRect().right + 1).map((c) => c.className));
      assert.deepEqual(spill, [], "the bar's buttons stay inside it");

      const keys = ["By tokens", "Select sessions under", "Each used fewer than {n} tokens"];
      if (lang !== "en") {
        const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l]?.[k]), [lang, keys]);
        assert.deepEqual(missing, [], "every string has its " + lang);
      }
      assert.deepEqual(errors, []);
    });
  }
}
