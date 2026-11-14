// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex session that can't be moved to Codex's provider at all (a
// paginated rollout with no row in Codex's database, sessions.StuckError;
// @xMopf7RF on X) is refused with stuck and the provider it stays on. The
// page says why, in the reader's language, and what can be done instead:
// it stays where it was, and Delete clears it into magpie's trash. Every
// language, Chromium and WebKit, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, title, provider, min) => ({
  agent: "codex", id, title, cwd: "/work/app", start: at(min + 30), last: at(min), models: [], cost: 0, unpriced: 0,
  resume: "codex resume " + id, path: "~/.codex/sessions/x/" + id + ".jsonl.zst", size: 2048, messages: 4, files: 1, deletable: true,
  provider, uses_provider: "magpie",
});

function serve(lang, calls) {
  const sessions = [sess("a1", "an old chat", "zai", 60 * 24 * 200)];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "codex", count: sessions.length, deletable: true, name: "Codex", icon: "codex-color" }],
        agent: "codex", sessions, terminal: false, trash: [], trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/codex-provider") {
      calls.push(route.request().postDataJSON());
      return json({ moved: [], refused: [{ id: "a1", error: "rollout-x.jsonl.zst can't move to magpie: …", active: false, stuck: true, from: "zai" }] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { nav: "Sessions", button: "Move to magpie…", move: "Move", said: "an old chat can't be moved: Codex keeps its history by byte and has no record of it in its database to move it by. It stays on zai, unchanged, and Codex lists it when it uses zai again. To clear it from here, delete it: it goes to magpie's trash and can be restored." },
  zh: { nav: "会话", button: "迁移到 magpie…", move: "迁移", said: "「an old chat」无法迁移：Codex 按字节位置保存它的历史，而它的数据库里没有这个会话的记录可供迁移。它保持在 zai 上，不做任何改动；Codex 再次使用 zai 时会列出它。如果想把它从这里清掉，可以删除它：它会进入 magpie 的回收站，可以恢复。" },
  "zh-TW": { nav: "會話", button: "遷移到 magpie…", move: "遷移", said: "「an old chat」無法遷移：Codex 按位元組位置保存它的歷史，而它的資料庫裡沒有這個會話的紀錄可供遷移。它保持在 zai 上，不做任何改動；Codex 再次使用 zai 時會列出它。如果想把它從這裡清掉，可以刪除它：它會進入 magpie 的回收站，可以恢復。" },
  ja: { nav: "セッション", button: "magpie に移行…", move: "移行", said: "「an old chat」は移動できません：Codex はこの履歴をバイト位置で保持しており、データベースに移動の手がかりとなる記録がありません。zai のまま変更されず、Codex が再び zai を使うと一覧に表示されます。ここから消すには削除してください。magpie のゴミ箱に移され、復元できます。" },
  de: { nav: "Sitzungen", button: "Zu magpie umziehen…", move: "Umziehen", said: "„an old chat“ kann nicht verschoben werden: Codex speichert den Verlauf nach Byte-Position und hat in seiner Datenbank keinen Eintrag, über den er sich verschieben ließe. Die Sitzung bleibt unverändert bei zai, und Codex listet sie wieder, sobald es zai verwendet. Um sie hier zu entfernen, löschen Sie sie: Sie kommt in den Papierkorb von magpie und lässt sich wiederherstellen." },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a Codex session that can't move says why and what to do instead`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        const calls = [];
        await page.route("**/*", serve(lang, calls));
        await page.goto("http://magpie.test/?view=providers");
        const nav = page.locator("#nav").getByRole("button", { name: w.nav, exact: true });
        if (await nav.isVisible()) await nav.click();
        else await page.goto("http://magpie.test/?view=sessions");
        const view = page.locator("#view-sessions");
        const go = view.locator(".sm-provider").getByRole("button", { name: w.button, exact: true });
        await go.click();
        const ask = page.locator("#modal .sm-provider-ask");
        await ask.waitFor();
        await ask.getByRole("button", { name: w.move, exact: true }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(calls, [{ moves: [{ id: "a1", to: "magpie" }] }]);
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w.said)
          .catch(async () => assert.equal(await page.locator("#status").textContent(), w.said));
        const wide = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
        assert.equal(wide, false, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
