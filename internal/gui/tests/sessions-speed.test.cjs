// Run with Node's test runner and Playwright on the module path; see README.md.
// A session the gateway is serving shows its output speed on its Sessions
// row: a curve of the last two minutes and tokens a second now, read from
// /api/speeds once a second; a click opens the round's numbers, with the
// gap where a tool ran. A session known by another key (s.keys) gets its
// speed too; a model with no rate yet shows "—", never a guess; a session
// with no speed has no cell. Every language, Chromium and WebKit, wide and
// narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, min, extra = {}) => ({
  agent: "claude", id, cwd: "/work/app", title: "Session " + id, start: at(min + 5), last: at(min),
  models: [{ model: "claude-opus-5-5", priced: true, input: 1000, output: 200, calls: 4 }],
  input: 1000, output: 200, cost: 0.1, messages: 12, size: 4096, files: 1, deletable: true,
  resume: `claude --resume ${id}`, path: `~/.claude/projects/x/${id}.jsonl`, ...extra,
});
// 120 seconds: 20 at 100, a 30 second gap, then 70 rising to the last at `now`
function curve(now) {
  const c = [];
  for (let i = 0; i < 120; i++) c.push(i < 20 ? null : i < 40 ? 100 : i < 70 ? null : 100 + (i - 70) * (now - 100) / 49);
  return c;
}

function serve(lang, reads) {
  const sessions = [sess("s-1", 0), sess("s-2", 1, { keys: ["claude|other-key"] }), sess("s-3", 30)];
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
      return json({
        agents: [{ agent: "claude", count: sessions.length, deletable: true, name: "Claude Code", icon: "claudecode-color" }],
        agent: "claude", sessions, terminal: true, trash: [], trashDir: "~/trash",
      });
    }
    if (url.pathname === "/api/speeds") {
      reads.n++;
      const now = reads.n < 3 ? 128 : 140;
      return json([
        { agent: "claude", session: "s-1", live: true, rated: true, now, avg: 109.4, peak: 315, output: 3700, curve: curve(now), idle: 0 },
        { agent: "claude", session: "other-key", live: false, rated: false, now: 0, avg: 0, peak: 0, output: 0, curve: curve(300).map((v, i) => i < 110 ? v : null), idle: 12 },
      ]);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { nav: "Sessions", unit: "tok/s", avg: "Round average", peak: "Fastest second", written: "Out so far", gap: "Tool 30s" },
  zh: { nav: "会话", unit: "token/秒", avg: "本轮平均", peak: "峰值", written: "已输出", gap: "工具 30 秒" },
  "zh-TW": { nav: "會話", unit: "token/秒", avg: "本輪平均", peak: "峰值", written: "已輸出", gap: "工具 30 秒" },
  ja: { nav: "セッション", unit: "トークン/秒", avg: "このターンの平均", peak: "ピーク", written: "出力済み", gap: "ツール 30 秒" },
  de: { nav: "Sitzungen", unit: "Token/s", avg: "Ø dieser Runde", peak: "Spitze", written: "Ausgegeben", gap: "Tool 30 s" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a session's speed on its row and in its popover`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-speed.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const reads = { n: 0 };
      await page.route("**/*", serve(lang, reads));
      await page.goto("http://magpie.test/?view=providers");
      assert.equal(reads.n, 0, "/api/speeds is read before the Sessions page is shown");
      await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
      const row = (id) => page.locator(`#view-sessions .row.sm-sess[data-id="${id}"]`);
      const cell = row("s-1").locator(".sm-speed");
      await cell.locator(".sm-speed-val b").filter({ hasText: "128" }).waitFor();
      assert.equal((await cell.locator(".sm-speed-val small").textContent()).trim(), w.unit);
      assert.equal(await cell.locator("svg.sm-curve polyline").count(), 2, "two runs of seconds");
      assert.equal(await cell.locator("svg.sm-curve line.sm-curve-gap").count(), 1, "one gap");
      // another key of the session's, a model with no rate: the shape, no number
      const other = row("s-2").locator(".sm-speed");
      await other.waitFor();
      assert.equal(await other.locator(".sm-speed-val").textContent(), "—");
      assert.ok(await other.locator("svg.sm-curve").count());
      assert.equal(await row("s-3").locator(".sm-speed").isHidden(), true, "a session with no speed shows none");

      // the popover: the number now, the curve, the tool's gap and the round
      await cell.click();
      const pop = page.locator(".pop.sm-speed-pop");
      await pop.waitFor();
      assert.equal(await cell.getAttribute("aria-expanded"), "true");
      assert.equal(await pop.locator(".sm-sp-unit").textContent(), w.unit);
      assert.match(await pop.locator(".sm-sp-big").textContent(), /^(128|140)$/);
      assert.equal(await pop.locator(".sm-sp-gap").textContent(), w.gap);
      assert.equal(await pop.locator(".sm-sp-dot").count(), 1, "a live session's last second is marked");
      const stats = await pop.locator(".sm-sp-k").allTextContents();
      assert.deepEqual(stats, [w.avg, w.peak, w.written]);
      const vals = await pop.locator(".sm-sp-v").allTextContents();
      assert.match(vals[0], /109/);
      assert.match(vals[1], /315/);
      assert.match(vals[2], /3[.,]?7/);
      // it follows the next reads, open
      await pop.locator(".sm-sp-big").filter({ hasText: "140" }).waitFor();
      const box = await pop.boundingBox(), vw = page.viewportSize().width;
      assert.ok(box.x >= 0 && box.x + box.width <= vw, `popover inside the window: ${JSON.stringify(box)}`);
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "detached" });
      assert.equal(await cell.getAttribute("aria-expanded"), "false");

      // narrow: the number stays, nothing runs past the row
      for (const width of [720, 520, 380]) {
        await page.setViewportSize({ width, height: 700 });
        await page.waitForFunction(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))));
        const m = await row("s-1").evaluate((r) => {
          const b = r.getBoundingClientRect();
          return [...r.children].filter((c) => c.offsetParent && (c.getBoundingClientRect().right > b.right + 1 || c.getBoundingClientRect().left < b.left - 1)).map((c) => c.className);
        });
        assert.deepEqual(m, [], `at ${width}px past the row's edge`);
        assert.ok(await cell.locator(".sm-speed-val").isVisible(), `at ${width}px the number shows`);
      }

      // off the page, no more reads
      await page.setViewportSize({ width: 1100, height: 700 });
      await page.locator("#nav").getByRole("button").first().click();
      await page.waitForTimeout(300);
      const was = reads.n;
      await page.waitForTimeout(2500);
      assert.ok(reads.n <= was + 1, `read ${reads.n - was} times off the page`);
      assert.deepEqual(errors, []);
    });
  }
}
