// Run with Node's test runner and Playwright on the module path; see README.md.
// Dade on Discord: on the Library pages each tab's Backups (备份) sat at the
// very foot, under a long list, and was hard to find. It is at the top of
// every tab now, in the strip that stays as the page scrolls, right after
// Library folder, with the same look; nothing is left at the foot. It
// opens magpie's backups folder, waits for the library like Library folder
// does, fits a narrow window and browser without wrapping, and has no left
// border. In every language, in both engines. No backend: the API is faked
// here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const skills = Array.from({ length: 40 }, (_, i) => ({ name: `skill-${String(i).padStart(2, "0")}`, kind: "folder", description: `Skill number ${i}`, agents: ["claude"], source: `${HOME}/skills/skill-${i}` }));
const servers = Array.from({ length: 30 }, (_, i) => ({ name: `server-${String(i).padStart(2, "0")}`, transport: "stdio", command: "npx", args: ["-y", `server-${i}`], agents: ["claude"] }));
const view = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code", "claudecode-color")],
  instructions: { agents: [], sets: [] }, servers, foundServers: [], projects: [], foundSkills: [], skills, problems: [],
  hooks: Array.from({ length: 30 }, (_, i) => ({ name: `hook-${String(i).padStart(2, "0")}`, event: "turn-end", command: `echo ${i}`, agents: ["claude"] })),
};

const words = {
  en: { backups: "Backups", folder: "Library folder", note: "magpie keeps a copy of every file before it writes it." },
  zh: { backups: "备份", folder: "资源库文件夹", note: "magpie 写入任何文件前都会先备份一份。" },
  "zh-TW": { backups: "備份", folder: "資源庫資料夾", note: "magpie 寫入任何檔案前都會先備份一份。" },
  ja: { backups: "バックアップ", folder: "ライブラリフォルダ", note: "magpie はファイルに書き込む前に必ずコピーを保存します。" },
  de: { backups: "Backups", folder: "Bibliotheksordner", note: "magpie sichert jede Datei, bevor es sie schreibt." },
};

function serve(lang, web, opened, held) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") { await held; return json(view); }
    if (url.pathname === "/api/library/rtk") return json({ path: `${HOME}/.local/bin/rtk`, version: "0.51.0", url: "https://www.rtk-ai.app", agents: [{ id: "claude", name: "Claude Code", icon: "", on: true }] });
    if (url.pathname === "/api/library/reveal") { opened.push(req.postDataJSON()); return json({}); }
    if (url.pathname === "/api/library/market/servers" || url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Backups is at the top of every Library tab, after Library folder`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [web, width] of [[true, 1000], [true, 420], [false, 1000], [false, 800], [false, 560]]) {
        for (const tab of ["instructions", "mcp", "skills", "hooks", "rtk"]) {
          const where = `${web ? "browser" : "window"} ${width}px ${tab}`;
          const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
          await context.addInitScript((tb) => { try { localStorage.setItem("magpie.libTab", tb); } catch {} }, tab);
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          page.on("console", (m) => { if (/scroll not asked for/.test(m.text())) errors.push(m.text()); });
          const opened = [];
          let release;
          const held = new Promise((r) => { release = r; });
          await page.route("**/*", serve(lang, web, opened, held));
          await page.goto("http://magpie.test/?view=library");
          const v = page.locator("#view-library");
          const head = v.locator(".lib-head");
          const back = head.locator(".lib-backups");

          // it waits for the library, as Library folder does
          await back.waitFor();
          assert.ok(await back.isDisabled(), `${where}: Backups waits for the library`);
          release();
          await head.locator(".lib-backups:not([disabled])").waitFor();
          await v.locator(".lib-body").first().waitFor({ state: "attached" }).catch((e) => { throw new Error(where + ": " + e.message); });

          // found afresh in one synchronous step: a tab that lists the
          // agents' own items draws again when /api/library/native answers,
          // and a head found before that is no longer in the page (#4431)
          const got = await page.evaluate(() => {
            const h = document.querySelector("#view-library .lib-head");
            const b = h.querySelector(".lib-backups"), f = h.querySelector(".lib-more"), vw = h.closest(".view");
            const r = b.getBoundingClientRect(), fr = f.getBoundingClientRect();
            const c = getComputedStyle(b);
            return {
              label: b.textContent, title: b.title, after: b.previousElementSibling === f, sameRow: Math.abs(r.top - fr.top) < 2,
              look: ["fontSize", "height", "color"].every((k) => getComputedStyle(f)[k] === c[k]),
              inView: r.left >= 0 && r.right <= innerWidth + 0.5 && r.width > 0,
              wrapped: b.querySelector("span:last-child").getClientRects().length > 1 || b.scrollWidth > b.clientWidth + 1,
              stripe: c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor,
              side: vw.scrollWidth - vw.clientWidth, pageSide: document.scrollingElement.scrollWidth - innerWidth,
              foot: document.querySelectorAll("#view-library .lib-foot, #view-library .lib-link").length,
              icon: !!b.querySelector(".lib-mini svg"),
            };
          });
          assert.equal(got.label, w.backups, where);
          assert.equal(got.title, w.note + "\n~/.magpie/backups", where);
          assert(got.after, `${where}: Backups comes right after ${w.folder}`);
          assert(got.sameRow, `${where}: Backups sits on Library folder's line`);
          assert(got.look, `${where}: Backups looks like Library folder`);
          assert(got.icon, `${where}: Backups has its glyph`);
          assert(got.inView, `${where}: Backups runs out of the window`);
          assert(!got.wrapped, `${where}: Backups' label wraps`);
          assert(!got.stripe, `${where}: Backups has a left border`);
          assert.equal(got.side, 0, `${where}: the view scrolls sideways`);
          assert(got.pageSide <= 1, `${where}: the page scrolls sideways`);
          assert.equal(got.foot, 0, `${where}: something is left at the foot`);
          assert.equal(await head.getByRole("button", { name: w.folder, exact: true }).count(), 1, `${where}: Library folder by its name`);

          // it opens the backups folder, and the page stays where it was
          const top = await v.evaluate((e) => e.scrollTop);
          await Promise.all([
            page.waitForResponse((r) => new URL(r.url()).pathname === "/api/library/reveal"),
            head.getByRole("button", { name: w.backups, exact: true }).click(),
          ]);
          assert.deepEqual(opened, [{ path: view.backups }], where);
          assert.equal(await v.evaluate((e) => e.scrollTop), top, `${where}: the click moved the page`);
          assert.deepEqual(errors, [], where);
          await context.close();
        }
      }
    });
  }
}
