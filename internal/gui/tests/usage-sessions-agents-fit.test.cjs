// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage › Sessions with more agents than fit on one line (#929, liuweifeng):
// the agent strip scrolls in itself, so the page never gets a sideways
// scrollbar, and the agent picked stays in view. English and Chinese,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const names = ["Pi", "WorkBuddy", "DeepSeek Harness", "Claude Code", "Codex", "omp", "OpenCode", "Qoder", "ZCode", "Claude Desktop", "Qoder CN", "Grok Build", "Kimi Code", "Cursor", "Zed", "Reasonix Studio"];
const agentID = i => i === names.length - 1 ? "reasonix" : "a" + i;
const agents = Object.fromEntries(names.map((n, i) => [agentID(i), n]));
const fixture = require("node:fs").readFileSync(path.resolve(__dirname, "../../sessions/testdata/reasonix-2.29.0/session.jsonl"), "utf8").trim().split("\n").map(JSON.parse);
const nativeUsage = JSON.parse(require("node:fs").readFileSync(path.resolve(__dirname, "../../sessions/testdata/reasonix-2.29.0/session.jsonl.telemetry.json"), "utf8")).usage;
const nativeTokens = {input:nativeUsage.promptTokens-nativeUsage.cacheHitTokens, output:nativeUsage.completionTokens, cache_read:nativeUsage.cacheHitTokens, cache_write:0};
const models = [...new Set(fixture.filter(m => m.role === "assistant").map(m => m.modelRef.replace(/^magpie\//, "")))];
const reasonix = { agent:"reasonix", id:"session-a", title:"hello there", cwd:"/work/app", last:new Date().toISOString(), ...nativeTokens, cost:0, priced:false, usage_incomplete:false, models };
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const today = iso(new Date());
const days = [{
  date: today,
  usage: names.map((_, i) => ({ agent: agentID(i), cwd: "/work/app", model: "m" + (i % 3), input: 1000 * (i + 1), output: 100, cache_read: 0, cache_write: 0, cost: 0.1, priced: true })),
  active: [],
}];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    if (url.pathname === "/api/sessions") return json({ sessions: names.map((name, i) => ({...reasonix, agent:agentID(i), name, title:i === names.length-1 ? reasonix.title : name, usage_incomplete:false, models:i === names.length-1 ? models.map(model => ({model, ...nativeTokens, priced:false})) : []})), dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: today, to: today, days, agents, sessions:[reasonix] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: many agents don't widen Usage › Sessions`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 860, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      await page.goto("http://magpie.test/?view=usage");
      const strip = page.locator("#sessAgent");
      await page.waitForFunction((n) => document.querySelectorAll("#sessAgent .opt").length === n, names.length + 1);
      const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      const all = strip.locator('.opt[data-agent="all"]');
      assert.equal(await all.getAttribute("aria-pressed"), "true", "All starts selected for assistive technology");
      assert.equal(await strip.getByRole("button", { pressed: false }).count(), names.length);
      for (const width of [860, 560]) {
        await page.setViewportSize({ width, height: 700 });
        await settle();
        const sideways = await page.evaluate(() => [document.documentElement, document.body, document.querySelector("main"), document.querySelector("#view-usage")]
          .filter(Boolean).filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => `${e.tagName}#${e.id} ${e.scrollWidth}>${e.clientWidth}`));
        assert.deepEqual(sideways, [], `${width}px: nothing scrolls sideways but the strip`);
        const box = await strip.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width, `${width}px: the agent strip fits the window: ${JSON.stringify(box)}`);
        assert(await strip.evaluate((e) => e.scrollWidth > e.clientWidth), "the strip itself scrolls");
      }
      // the last agent, clicked where the strip was scrolled to, stays there:
      // the strip drawn again kept its buttons and where it was scrolled
      await strip.evaluate((e) => { e.scrollLeft = e.scrollWidth; });
      const last = strip.locator(".opt").last();
      // Playwright's WebKit stops hit-testing a scroller's children once it
      // has been scrolled (a click lands on the strip, ~70% of runs; the
      // system's WKWebView takes it, tried from a WKWebView in AppKit), so
      // there the button is clicked as an element
      if (engine === "webkit") await last.evaluate((b) => b.click());
      else {
        const lb = await last.boundingBox();
        await page.mouse.click(lb.x + lb.width / 2, lb.y + lb.height / 2);
      }
      await page.waitForFunction(() => document.querySelector("#sessAgent .opt.on")?.textContent === "Reasonix Studio");
      await settle();
      assert.equal(await strip.getByRole("button", { pressed: true }).textContent(), "Reasonix Studio", "the reused button exposes its new selection");
      assert.equal(await all.getAttribute("aria-pressed"), "false", "the previous selection is cleared");
      const on = await strip.locator(".opt.on").boundingBox();
      const sb = await strip.boundingBox();
      const seen = Math.min(on.x + on.width, sb.x + sb.width) - Math.max(on.x, sb.x);
      assert(seen >= on.width * 0.75, `the picked agent stays in view after the strip is drawn again: ${JSON.stringify({ on, sb })}`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true);
      const row = page.locator(".sess-link", { hasText:"hello there" });
      await row.waitFor();
      const text = (await row.textContent()).toLowerCase();
      assert(text.includes("fake-model"), "Usage Models shows the model from native 2.29.0 files");
      assert.notEqual(await row.locator(".num b").textContent(), "—", "2.x wire records provide session tokens");
      assert(!(await row.textContent()).includes({en:"Partial usage history",zh:"用量历史不完整",ja:"使用履歴が不完全です",de:"Unvollständiger Nutzungsverlauf"}[lang]));
      assert.deepEqual(errors, []);
    });
  }
}

for (const lang of ["en", "zh"]) {
  test(`chromium ${lang}: unsupported Reasonix stores are not a normal empty filter`, async (t) => {
    const browser = await chromium.launch({ channel: "chromium" });
    t.after(() => browser.close());
    const page = await (await browser.newContext()).newPage();
    page.setDefaultTimeout(8000);
    const normal = serve(lang);
    await page.route("**/*", async route => {
      const u = new URL(route.request().url());
      if (u.pathname === "/api/sessions") return route.fulfill({json:{sessions:[],dirs:[],unsupported_reasonix:4}});
      if (u.pathname === "/api/sessions/stats") return route.fulfill({json:{from:today,to:today,days:[],agents:{reasonix:"Reasonix Studio",codex:"Codex"},sessions:[]}});
      return normal(route);
    });
    await page.addInitScript(() => { localStorage.setItem("magpie.usageTab","sessions");localStorage.setItem("magpie.sessRange","7d"); });
    await page.goto("http://magpie.test/?view=usage");
    const warning = lang === "zh" ? "无法读取" : "unreadable";
    await page.locator("#sessStats").getByText(warning,{exact:false}).waitFor();
    assert.match(await page.locator("#sessNote").textContent(), /unreadable|无法读取|読み取れない|unlesbare/);
    await page.locator('#sessAgent .opt[data-agent="reasonix"]').click();
    assert.match(await page.locator("#sessStats").textContent(), /unreadable|无法读取|読み取れない|unlesbare/);
    await page.locator('#sessAgent .opt[data-agent="codex"]').click();
    assert.doesNotMatch(await page.locator("#sessStats").textContent(), /unreadable|无法读取|読み取れない|unlesbare/);
  });
}
