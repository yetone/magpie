// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions page's search also finds what was said in other agents'
// sessions (cryingLiu on X): under the list, the sessions of other agents
// whose conversation says the words, from magpie's index of them
// (/api/sessions/said), asked again while the sessions are still being
// listed and while the index is still reading. A hit,
// there or in the agent's own list, opens the conversation in the sheet at
// the message that said it (transcript&find=), its start left out and said
// so; the page itself never scrolls. In gateway mode, where no agent's own
// files are read, nothing is asked. Chromium and WebKit, every language,
// wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();

// a long conversation whose 31st message says the words
const PARTS = [];
for (let i = 0; i < 40; i++) {
  PARTS.push(i === 30
    ? { role: "user", kind: "text", text: "where is the Zanzibar key kept" }
    : { role: i % 2 ? "assistant" : "user", kind: "text", text: Array.from({ length: 4 }, (_, j) => `message ${i} line ${j}`).join("\n") });
}

function serve(lang, log, gateway) {
  const sessions = [
    { agent: "pi", id: "p-1", cwd: "/work/pi", title: "Rename foo", start: at(10), last: at(5), models: [], size: 4096, messages: 6, deletable: true, transcript: true },
    { agent: "pi", id: "p-2", cwd: "/work/pi", title: "Write tests", start: at(30), last: at(20), models: [], size: 4096, messages: 6, deletable: true, transcript: true },
  ];
  const agents = [
    { agent: "pi", count: 2, deletable: true, name: "Pi", icon: "pi" },
    { agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claude" },
  ];
  const reads = { asked: true, all: true, found: [], gateway };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/reads") return json(reads);
    if (url.pathname === "/api/sessions/manage") return json({ agents, agent: "pi", sessions, terminal: false, trash: [], trashDir: "~/trash", reads });
    if (url.pathname === "/api/sessions/search") {
      const q = url.searchParams.get("q");
      const hits = q === "zanzibar" ? [{ id: "p-1", role: "user", kind: "text", before: "where is the ", match: "Zanzibar", after: " key kept" }] : [];
      return json({ hits, searched: 2, total: 2 });
    }
    if (url.pathname === "/api/sessions/said") {
      const q = url.searchParams.get("q");
      log.said.push(q);
      const hit = { role: "user", kind: "text", before: "where is the ", match: "Zanzibar", after: " key kept" };
      const hits = q === "zanzibar" ? [
        { agent: "pi", id: "p-1", title: "Rename foo", last: at(5), transcript: true, hit },
        { agent: "claude", id: "c-1", title: "Keys in the vault", cwd: "/work/vault", last: at(60), transcript: true, hit },
      ] : [];
      // the first answer while the sessions are still being listed, the
      // next while the index reads them, then all read
      if (log.said.length === 1) return json({ hits: [], indexed: 0, total: 0, listing: true });
      return json({ hits, indexed: log.said.length === 2 ? 3 : 5, total: 5 });
    }
    if (url.pathname === "/api/sessions/transcript") {
      log.read.push(Object.fromEntries(url.searchParams));
      const find = url.searchParams.get("find");
      return json(find ? { parts: PARTS.slice(24), from: 24 } : { parts: PARTS });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const KEYS = ["Other agents", "Indexing sessions…", "Indexing {n} of {total} sessions…", "{n} earlier message not shown", "{n} earlier messages not shown"];
const nav = { en: "Sessions", zh: "会话", "zh-TW": "會話", ja: "セッション", de: "Sitzungen" };

async function open(browser, lang, width, gateway = false) {
  const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const log = { said: [], read: [] };
  await page.route("**/*", serve(lang, log, gateway));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator("#nav").getByRole("button", { name: nav[lang], exact: true }).click();
  const view = page.locator("#view-sessions");
  await view.locator('.row.sm-sess[data-id="p-1"]').waitFor();
  return { context, page, view, log, errors, tr: (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]) };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: search finds other agents' sessions and opens at the words`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, width] of [["en", 1100], ["zh", 1100], ["zh-TW", 560], ["ja", 560], ["de", 420]]) {
      await t.test(`${lang} at ${width}`, async () => {
        const { context, page, view, log, errors, tr } = await open(browser, lang, width);
        const filter = view.locator(".sm-filter");
        await filter.pressSequentially("zanzibar", { delay: 15 });

        // under the list, the other agents' sessions that said it, the
        // current agent's own left to its list; the index still reading
        const box = view.locator(".list.sm-else");
        await box.waitFor();
        await box.locator(".sm-else-head .note").filter({ hasText: await tr("Indexing sessions…") }).waitFor();
        assert.equal(await box.locator(".sm-else-head .name").textContent(), await tr("Other agents"));
        await box.locator(".sm-else-head .note").filter({ hasText: await tr("Indexing {n} of {total} sessions…", { n: 3, total: 5 }) }).waitFor();
        const rows = box.locator(".sm-else-row");
        assert.deepEqual(await rows.evaluateAll((l) => l.map((r) => r.dataset.agent + ":" + r.dataset.id)), ["claude:c-1"]);
        assert.equal(await rows.first().locator(".sm-hit mark").textContent(), "Zanzibar");
        assert.match(await rows.first().locator(".sub").first().textContent(), /Claude Code · .*\/work\/vault/);
        // asked again while it reads; then nothing more to say
        await box.locator(".sm-else-head .note").waitFor({ state: "detached", timeout: 8000 });
        assert.deepEqual(log.said, ["zanzibar", "zanzibar", "zanzibar"]);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
        const fit = await rows.first().evaluate((r) => ({ right: r.getBoundingClientRect().right, page: document.querySelector("#view-sessions").getBoundingClientRect().right }));
        assert(fit.right <= fit.page + 1, `the row overflows: ${JSON.stringify(fit)}`);

        // a hit opens the conversation at the message that said it
        const y0 = await page.evaluate(() => scrollY);
        await rows.first().click();
        const sheet = page.locator(".sess-sheet");
        await sheet.locator(".sess-found").waitFor();
        assert.deepEqual([log.read.at(-1).agent, log.read.at(-1).id, log.read.at(-1).find], ["claude", "c-1", "zanzibar"]);
        assert.equal((await sheet.locator(".sess-from").textContent()).trim(), await tr("{n} earlier messages not shown", { n: 24 }));
        assert.match(await sheet.locator(".sess-found").textContent(), /Zanzibar key kept/);
        assert.equal(await sheet.locator(".sess-found").count(), 1);
        const where = await sheet.locator(".sess-found").evaluate((n) => {
          const body = n.closest(".ebody");
          return { top: n.getBoundingClientRect().top - body.getBoundingClientRect().top, scrolled: body.scrollTop, h: body.clientHeight };
        });
        assert(where.scrolled > 0 && where.top >= 0 && where.top < 40, `the sheet starts at the words: ${JSON.stringify(where)}`);
        assert.equal(await page.evaluate(() => scrollY), y0, "the page never scrolled");
        await page.keyboard.press("Escape");
        await sheet.waitFor({ state: "detached" });

        // the agent's own hit opens there too
        await view.locator('.row.sm-sess[data-id="p-1"] .sm-hit').click();
        await sheet.locator(".sess-found").waitFor();
        assert.deepEqual([log.read.at(-1).agent, log.read.at(-1).id, log.read.at(-1).find], ["pi", "p-1", "zanzibar"]);
        await page.keyboard.press("Escape");
        await sheet.waitFor({ state: "detached" });

        // cleared, the other agents go with it
        await filter.press("Escape");
        await box.waitFor({ state: "detached" });

        if (lang !== "en") {
          const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, KEYS]);
          assert.deepEqual(missing, [], `${lang} has every string`);
        }
        if (process.env.SCREENSHOT_DIR) {
          await filter.fill("zanzibar");
          await box.waitFor();
          await fs.mkdir(process.env.SCREENSHOT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${engine}-${lang}-${width}-sessions-said.png`) });
        }
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });

  test(`${engine}: in gateway mode no other agent is searched`, async () => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    try {
      const { context, view, log, errors } = await open(browser, "en", 1000, true);
      await view.locator(".sm-filter").pressSequentially("zanzibar", { delay: 15 });
      await view.locator('.row.sm-sess[data-id="p-1"] .sm-hit mark').waitFor();
      await view.page().waitForTimeout(800);
      assert.deepEqual(log.said, []);
      assert.equal(await view.locator(".sm-else").count(), 0);
      assert.deepEqual(errors, []);
      await context.close();
    } finally {
      await browser.close();
    }
  });
}
