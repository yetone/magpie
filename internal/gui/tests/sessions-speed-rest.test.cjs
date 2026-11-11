// Run with Node's test runner and Playwright on the module path; see README.md.
// While no session has a speed, the Sessions page asks /api/speeds every
// few seconds, not every second: an open page resting on nothing still
// woke magpie once a second. A session the gateway starts hearing shows
// its speed within that rest, and the page then asks every second again.
// Every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = {
  agent: "claude", id: "s-1", cwd: "/work/app", title: "Session s-1", start: at(5), last: at(0),
  models: [{ model: "claude-opus-5-5", priced: true, input: 1000, output: 200, calls: 4 }],
  input: 1000, output: 200, cost: 0.1, messages: 12, size: 4096, files: 1, deletable: true,
  resume: "claude --resume s-1", path: "~/.claude/projects/x/s-1.jsonl",
};

function serve(lang, gw) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claudecode-color" }], agent: "claude", sessions: [sess], terminal: true, trash: [], trashDir: "~/trash" });
    }
    if (url.pathname === "/api/speeds") {
      gw.reads++;
      if (!gw.heard) return json([]);
      const curve = Array.from({ length: 120 }, (_, i) => (i < 110 ? null : 90));
      return json([{ agent: "claude", session: "s-1", live: true, rated: true, now: 90, avg: 90, peak: 90, output: 900, curve, idle: 0 }]);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const nav = { en: "Sessions", zh: "会话", "zh-TW": "會話", ja: "セッション", de: "Sitzungen" };

// until waits for ok(), asked every 50ms, failing after ms
async function until(ok, ms, what) {
  const end = Date.now() + ms;
  while (!ok()) {
    if (Date.now() > end) throw new Error("timed out: " + what());
    await new Promise((r) => setTimeout(r, 50));
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(nav)) {
    test(`${engine} ${lang}: a resting Sessions page asks for speeds every few seconds`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const gw = { reads: 0, heard: false };
      await page.route("**/*", serve(lang, gw));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: nav[lang], exact: true }).click();
      await page.locator('#view-sessions .row.sm-sess[data-id="s-1"]').waitFor();
      await until(() => gw.reads >= 1, 8000, () => `no read of /api/speeds`);
      // at once a second this is ~4 more reads; resting, at most one
      const was = gw.reads;
      await page.waitForTimeout(3500);
      assert.ok(gw.reads - was <= 1, `read ${gw.reads - was} times in 3.5s with nothing heard`);
      // the gateway hears the session: its speed shows within the rest
      gw.heard = true;
      const cell = page.locator('#view-sessions .row.sm-sess[data-id="s-1"] .sm-speed');
      await cell.locator(".sm-speed-val b").filter({ hasText: "90" }).waitFor({ timeout: 8000 });
      // and the page asks every second again: three more reads well
      // before a resting page's 15s
      const live = gw.reads;
      await until(() => gw.reads >= live + 3, 9000, () => `${gw.reads - live} reads once a speed was heard`);
      assert.deepEqual(errors, []);
    });
  }
}
