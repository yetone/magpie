// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions page's filter finds a session by what was said in it, not only
// by its title, folder, id or model (YOU on Discord: the gateway's sessions
// are named ses_…, and their words are how they are remembered). The page
// asks /api/sessions/search once typing pauses, a new query cancels the one
// in flight, one letter asks nothing, and each session found shows the words
// round its match, the match marked. A search cut short, or of an agent
// whose own files can't be read, says so. In every language, Chromium and
// WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const gw = (id, title, min) => ({
  agent: "pi", id, cwd: "", title, gateway: true, start: at(min + 5), last: at(min), models: [{ model: "m" }], size: 2048, deletable: true,
});
// what each session said, as the server would search it
const said = {
  "ses_01": "rename foo to bar in the parser",
  "ses_02": "why does the build fail? The Redis client times out after 30 seconds on every request to the cache, and only in CI, never on a laptop",
  "ses_03": "write a haiku about Kafka and autumn",
};

function serve(lang, log) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const sessions = [gw("ses_03", "write a haiku about Kafka and autumn", 1), gw("ses_02", "why does the build fail?", 5), gw("ses_01", "rename foo to bar in the parser", 9)];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "pi", count: sessions.length, deletable: true, name: "Pi", icon: "pi" }],
        agent: "pi", sessions, terminal: false, trash: [], trashDir: "~/trash", recording: true, recorded: true,
      });
    }
    if (url.pathname === "/api/sessions/search") {
      const q = url.searchParams.get("q");
      log.push({ agent: url.searchParams.get("agent"), q });
      // long enough for the next keystroke to land while it is out, or
      // held until the test lets it go
      await new Promise((r) => setTimeout(r, log.slow ? 900 : 250));
      await log.hold;
      const hits = [];
      for (const [id, text] of Object.entries(said)) {
        const i = text.toLowerCase().indexOf(q);
        if (i >= 0) {
          const after = text.slice(i + q.length);
          hits.push({ id, role: "user", kind: "text", before: text.slice(0, i), match: text.slice(i, i + q.length), after: after.length > 60 ? after.slice(0, 60) + "…" : after });
        }
      }
      return route.fulfill({ json: { hits, searched: log.cut ? 2 : 3, total: 3, cut: !!log.cut } }).catch(() => {});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const KEYS = [
  "Search sessions", "Searching what was said…", "Searched what was said in the latest {n} of {total} sessions.",
  "Couldn't search what was said: {error}",
  "{agent}'s own files of its sessions can't be read here, so only what magpie recorded through the gateway is searched.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the filter finds sessions by what was said`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, width] of [["en", 1100], ["zh", 1100], ["zh-TW", 560], ["ja", 560], ["de", 420]]) {
      await t.test(`${lang} at ${width}`, async () => {
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const aborted = [];
        page.on("requestfailed", (r) => { if (r.url().includes("/api/sessions/search")) aborted.push(new URL(r.url()).searchParams.get("q")); });
        const log = [];
        await page.route("**/*", serve(lang, log));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('#nav [data-view="sessions"]').first().click();
        const view = page.locator("#view-sessions");
        const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
        await row("ses_03").waitFor();
        const filter = view.locator(".sm-filter");
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);

        // a gateway session is titled by what it was asked, its id beside
        assert.equal(await row("ses_02").locator(".name").textContent(), "why does the build fail?");
        assert.match(await row("ses_02").locator(".sub").first().textContent(), /ses_02/);
        assert.equal(await filter.getAttribute("placeholder"), await tr("Search sessions"));

        // one letter asks nothing; typing asks once, when it pauses
        await filter.pressSequentially("r", { delay: 20 });
        await page.waitForTimeout(500);
        assert.equal(log.length, 0, "one letter is no search");
        await filter.pressSequentially("edis", { delay: 40 });
        await view.locator(".sm-search-note").waitFor();
        assert.equal(await view.locator(".sm-search-note").textContent(), await tr("Searching what was said…"));
        await row("ses_02").locator(".sm-hit mark").waitFor();
        assert.deepEqual(log.map((x) => x.q), ["redis"], "one request for the word typed");
        assert.equal(log[0].agent, "pi");
        assert.equal(await row("ses_02").locator(".sm-hit mark").textContent(), "Redis");
        assert.equal(await row("ses_03").count(), 0, "a session that didn't say it is left out");
        assert.equal(await view.locator(".sm-search-note").count(), 0, "nothing more to say once found");
        // the words round it stay on one line, inside the page
        const fit = await row("ses_02").evaluate((r) => {
          const hit = r.querySelector(".sm-hit");
          return { lines: Math.round(hit.getBoundingClientRect().height / parseFloat(getComputedStyle(hit).lineHeight || "15")), right: hit.getBoundingClientRect().right, page: document.querySelector("#view-sessions").getBoundingClientRect().right, over: document.documentElement.scrollWidth > innerWidth };
        });
        assert.ok(fit.right <= fit.page + 1 && !fit.over, `the snippet overflows: ${JSON.stringify(fit)}`);

        // a new query cancels the one in flight, and its answer never shows
        log.slow = true;
        await filter.fill("parser");
        for (let i = 0; i < 100 && !log.some((x) => x.q === "parser"); i++) await page.waitForTimeout(20); // past the pause: its request is out
        await filter.fill("kafka");
        await row("ses_03").locator(".sm-hit mark").waitFor();
        await page.waitForTimeout(1000);
        assert.deepEqual(log.slice(1).map((x) => x.q), ["parser", "kafka"]);
        assert.deepEqual(aborted, ["parser"], "the earlier search was cancelled");
        assert.equal(await row("ses_01").count(), 0, "the cancelled search's answer isn't shown");
        assert.equal(await row("ses_03").locator(".sm-hit mark").textContent(), "Kafka");

        // what a title or id matches is shown before any answer: the
        // search's is held, and the row is there with the search under way
        log.slow = false;
        log.cut = true;
        let release;
        log.hold = new Promise((r) => { release = r; });
        await filter.fill("ses_01");
        await row("ses_01").waitFor();
        assert.equal(await view.locator(".sm-search-note").textContent(), await tr("Searching what was said…"));
        release();
        log.hold = null;
        // a search cut short says how far it read
        await view.locator(".sm-search-note").filter({ hasText: await tr("Searched what was said in the latest {n} of {total} sessions.", { n: 2, total: 3 }) }).waitFor();

        // nothing found anywhere: no session matches, after the search
        await filter.fill("zebra");
        await view.locator(".empty-state").waitFor();
        assert.equal(await view.locator(".empty-state").textContent(), await tr("No session matches."));

        // Escape clears it and the list is whole again
        await filter.press("Escape");
        await row("ses_01").waitFor();
        assert.equal(await view.locator(".sm-hit").count(), 0);
        assert.equal(await view.locator(".sm-search-note").count(), 0);

        if (lang !== "en") {
          const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, KEYS]);
          assert.deepEqual(missing, [], `${lang} has every string`);
        }
        if (process.env.SCREENSHOT_DIR) {
          await filter.fill("redis");
          await row("ses_02").locator(".sm-hit mark").waitFor();
          await fs.mkdir(process.env.SCREENSHOT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${engine}-${lang}-${width}-sessions-search.png`) });
        }
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
