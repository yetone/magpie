// Run with Node's test runner and Playwright on the module path; see README.md.
// Gateway mode's Sessions (paultian on Discord): magpie web as the gateway
// for other computers lists only the sessions that came through it, never
// this computer's agents' files (ownSessionsOff). The server says so with
// reads.gateway, so the page never asks which agents' history to read
// (there is none to read), its empty state speaks of the gateway's
// sessions, and Settings › Privacy leaves out the rows for agents' session
// history. Every language, Chromium and WebKit, a narrow window; the API
// is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();

function serve(lang, { sessions }) {
  const settings = { lang, theme: "light", web: true, gatewayMode: "on", gatewayOn: true, gatewayWhy: "on", sessionReads: { asked: false, all: false } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,gateway:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    // what the server answers in gateway mode: nothing of this computer's
    // agents, asked still false as the settings have it
    const reads = { asked: false, all: false, found: [], gateway: true };
    if (url.pathname === "/api/sessions/reads") return json(reads);
    if (url.pathname === "/api/sessions/manage") {
      const rows = sessions ? [{ agent: "pi", id: "ses_remote_1", gateway: true, cwd: "/srv/app", title: "rename foo to bar", start: at(10), last: at(5), models: [], resume: "", size: 10, messages: 2, files: 0, deletable: true }] : [];
      const agents = rows.length ? [{ agent: "pi", count: 1, deletable: true, name: "Pi", icon: "pi" }] : [];
      return json({ agents, agent: agents[0]?.agent || "", sessions: rows, terminal: false, trash: [], trashDir: "", recording: true, reads });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { empty: "The sessions of agents on other computers that use this gateway show up here, by the folder they ran in.", head: "Agents' session history", share: "Sharing" },
  zh: { empty: "其他电脑上通过此网关的智能体会话按运行目录列在这里。", head: "智能体的会话历史" },
  "zh-TW": { empty: "其他電腦上透過此閘道的智慧體會話按執行目錄列在這裡。", head: "智慧體的會話歷史" },
  ja: { empty: "このゲートウェイを使う他のコンピュータのエージェントのセッションが、実行されたフォルダごとにここに表示されます。", head: "エージェントのセッション履歴" },
  de: { empty: "Sitzungen der Agenten auf anderen Computern, die diesen Zugangspunkt nutzen, erscheinen hier, gruppiert nach dem Ordner, in dem sie liefen.", head: "Sitzungsverlauf der Agenten" },
};

async function open(engine, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${t.name.replace(/[^\w-]+/g, "-")}.png`) }).catch(() => {});
    }
    await browser.close();
  });
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  return { page, errors };
}

// nothing in sel is cut off or wider than the window
async function fits(page, sel) {
  const over = await page.$eval(sel, (box) => {
    const vw = document.documentElement.clientWidth;
    const bad = [...box.querySelectorAll("*")].filter((e) => { const b = e.getBoundingClientRect(); return b.width && (b.right > vw + 0.5 || b.left < -0.5); }).map((e) => e.className);
    return { vw, bad, scroll: document.documentElement.scrollWidth > vw };
  });
  assert.ok(!over.bad.length && !over.scroll, JSON.stringify(over));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of [["en", 900], ["en", 390], ["zh", 420], ["zh-TW", 420], ["ja", 420], ["de", 390]]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: gateway mode lists the gateway's sessions and never asks to read this computer's`, async (t) => {
      const { page, errors } = await open(engine, width, t);
      await page.route("**/*", serve(lang, { sessions: true }));
      await page.goto("http://magpie.test/?view=sessions");
      await page.locator('#view-sessions .row.sm-sess[data-id="ses_remote_1"]').waitFor();
      assert.equal(await page.locator("#view-sessions .sm-consent").count(), 0, "the consent card in gateway mode");
      await fits(page, "#view-sessions");
      assert.deepEqual(errors, []);
    });
    test(`${engine} ${lang} ${width}px: gateway mode with no session yet says where they come from`, async (t) => {
      const { page, errors } = await open(engine, width, t);
      await page.route("**/*", serve(lang, { sessions: false }));
      await page.goto("http://magpie.test/?view=sessions");
      const empty = page.locator("#view-sessions .empty-state");
      await empty.waitFor();
      assert.equal(await empty.locator("span").textContent(), w.empty);
      assert.equal(await page.locator("#view-sessions .sm-consent").count(), 0, "the consent card in gateway mode");
      await fits(page, "#view-sessions");
      assert.deepEqual(errors, []);
    });
    test(`${engine} ${lang} ${width}px: Settings › Privacy has no rows for this computer's session history in gateway mode`, async (t) => {
      const { page, errors } = await open(engine, width, t);
      const asked = [];
      page.on("request", (r) => { if (new URL(r.url()).pathname === "/api/sessions/reads") asked.push(r.method()); });
      await page.route("**/*", serve(lang, { sessions: true }));
      await page.goto("http://magpie.test/?view=settings&tab=privacy");
      await page.locator("#setPage-privacy").waitFor();
      await page.locator("#setPage-privacy .row-head .label", { hasText: "Sharing" }).or(page.locator("#shareList")).first().waitFor({ state: "attached" });
      // a reads answer the page asked for is drawn from before the check
      if (asked.length) await page.waitForResponse((r) => new URL(r.url()).pathname === "/api/sessions/reads").catch(() => {});
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => setTimeout(r))));
      assert.ok(await page.locator("#sessionReadsList").isHidden(), "the agents' rows");
      assert.ok(await page.locator("#setPage-privacy .row-head .label", { hasText: w.head }).isHidden(), "their heading");
      assert.equal(await page.locator("#sessionReadsList .row").count(), 0);
      if (w.share) assert.ok(await page.locator("#setPage-privacy .row-head .label", { hasText: w.share }).isVisible(), "the rest of Privacy stays");
      await fits(page, "#setPage-privacy");
      assert.deepEqual(errors, []);
    });
  }
}
