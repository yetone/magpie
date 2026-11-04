// Run with Node's test runner and Playwright on the module path; see README.md.
// Each route keeps the folder its agent works in, its last two levels only
// (#1068, emo172: two Claude Code runs in two projects read as one). The
// Routing page's By session heads a session with its title ("Claude Code ·
// title") and the folder dim beside it, the session id under the pointer; a request's
// row says it under the pointer; the stage's agent, working in two folders
// at once, names both by their last level; the tray panel's agent says them
// under the pointer. A session that says no folder reads as before. In every
// language, at 1100 and 420. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "team", kind: "key", model: "m1" };
  const req = (id, session, folder, ago, extra = {}) => ({ id, seq: id, time: at(ago), agent: "claude", session, ...(folder ? { folder } : {}),
    model: "relay/m1", provider: "relay", order: [seat], tries: [{ id: seat.id, model: "m1", start: at(ago), done: true, status: 200, ms: 500 }],
    done: true, status: 200, ms: 500, tokens: 1000, ...extra });
  return [
    req(1, "s-magpie", "workspace/magpie", 5),
    req(2, "s-magpie", "workspace/magpie", 4, { sessionTitle: "Fix the gateway" }),
    req(3, "s-dial", "src/dial", 3),
    req(4, "s-none", "", 2),
    req(5, "", "", 1),
    req(6, "s-old", "", 0.5),
  ];
}

function serve(lang, routes) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 6, totals: { requests: 6, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    // a session named by its first prompt, as sessions.Titles gives it
    if (url.pathname === "/api/gateway/session-titles") return json({ titles: { "claude:s-old": "Rename foo\n  to bar" } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { folder: "Folder", session: "Session id" },
  zh: { folder: "文件夹", session: "会话 ID" },
  "zh-TW": { folder: "資料夾", session: "會話 ID" },
  ja: { folder: "フォルダ", session: "セッション ID" },
  de: { folder: "Ordner", session: "Sitzungs-ID" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: routing names each session's folder, never more of its path`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = words[lang], errors = [];
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, fixture()));
        try { await page.addInitScript(() => localStorage.setItem("magpie.routingBySession", "1")); } catch {}
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-session").nth(4).waitFor();
        await page.waitForFunction(() => [...document.querySelectorAll(".rt-session .nm")].some((e) => e.textContent.includes("Rename")));

        // By session: the agent and the session's title, its folder dim
        // beside it (flandern1211_58540 on Discord: "路由这里的会话名只能
        // 看到目录名和会话ID吗"); with no title, the folder stands in, and
        // with neither, the id
        const names = await page.locator(".rt-session").evaluateAll((es) => es.map((e) => {
          const nm = e.querySelector(".nm"), dir = e.querySelector(".dir");
          return { text: nm.textContent, title: nm.title, dir: dir && !dir.hidden ? dir.textContent : "", dirTitle: dir?.title || "" };
        }));
        const of = (re) => names.find((n) => re.test(n.title)) || assert.fail(`no group for ${re}: ${JSON.stringify(names)}`);
        assert.equal(of(/s-magpie$/).text, "Claude Code · Fix the gateway");
        assert.equal(of(/s-magpie$/).dir, "workspace/magpie", "the folder beside the title");
        assert.equal(of(/s-old$/).text, "Claude Code · Rename foo to bar", "the first prompt, on one line");
        assert.equal(of(/s-old$/).dir, "");
        assert.equal(of(/s-dial$/).text, "Claude Code · src/dial", "a folder stands in for the session id");
        assert.equal(of(/s-dial$/).dir, "", "the folder said once");
        assert.equal(of(/s-none$/).text, "Claude Code · s-none", "no folder: as before");
        assert.match(of(/s-old$/).title, new RegExp(`^Rename foo to bar\\n${w.session}: s-old$`));
        const clipped = await page.locator(".rt-session").evaluateAll((es) => es.some((e) => [...e.children].some((c) => c.getBoundingClientRect().right > e.getBoundingClientRect().right + 1)));
        assert.equal(clipped, false, "each heading's parts inside it");
        assert.match(of(/s-magpie$/).title, new RegExp(`${w.folder}: workspace/magpie\\n${w.session}: s-magpie$`));
        assert.doesNotMatch(of(/s-none$/).title, new RegExp(w.folder));

        // a request's row says its folder under the pointer
        const rowTitles = await page.locator(".rt-req").evaluateAll((es) => es.map((e) => e.title));
        assert(rowTitles.some((x) => x.endsWith(`\n${w.folder}: src/dial`)), JSON.stringify(rowTitles));
        assert.equal(rowTitles.filter((x) => x.includes(w.folder)).length, 3, "only the rows that have a folder");

        // the stage: one agent in two folders at once names both
        const node = page.locator(".rt-src").first();
        await page.waitForFunction(() => document.querySelector(".rt-src small")?.textContent.includes(","));
        assert.equal(await node.locator("small").textContent(), "magpie, dial");
        assert.match(await node.getAttribute("title"), new RegExp(`${w.folder}: workspace/magpie\\n${w.folder}: src/dial$`));

        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        const reqs = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(reqs.scroll <= reqs.client + 1, `request list overflows at ${width}: ${JSON.stringify(reqs)}`);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-session-folder.png`), fullPage: true });
        }

        // the tray panel's agent: its folders under the pointer
        const panel = await browser.newPage({ viewport: { width: Math.min(width, 440), height: 600 }, reducedMotion: "reduce" });
        panel.setDefaultTimeout(5000);
        panel.setDefaultNavigationTimeout(60000);
        panel.on("pageerror", (e) => errors.push(e.message));
        await panel.route("**/*", serve(lang, fixture()));
        await panel.goto("http://magpie.test/?mode=panel");
        await panel.locator('[data-ptab="routing"]').click();
        await panel.locator(".ps-ag").first().waitFor();
        assert.match(await panel.locator(".ps-ag").first().getAttribute("title"), new RegExp(`^Claude Code\\n${w.folder}: workspace/magpie\\n${w.folder}: src/dial$`));
        assert.deepEqual(errors, []);
      });
    }
  }
}
