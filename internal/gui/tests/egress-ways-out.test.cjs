// Run with Node's test runner and Playwright on the module path; see README.md.
// mamba on Discord: the Claude editor says where Claude's requests go out
// to the internet, the address and its region, and Check looks again now;
// a list of allowed ways out (regions, IPs, CIDR ranges) is typed in and
// saved on Enter, with the rest kept, and the way out is judged against it
// at once. A way out outside the user's list puts a line over Providers
// that names the address and the user's list, not Anthropic's. In every
// language, 1100 and 420 wide; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const L = {
  en: { way: "Way out", allowed: "Allowed ways out", check: "Check", failed: "Couldn't look it up", region: "Japan", mine: "isn't one of the ways out you allowed for Claude" },
  zh: { way: "网络出口", allowed: "允许的出口", check: "检查", failed: "检测失败", region: "日本", mine: "不在你为 Claude 允许的出口内" },
  "zh-TW": { way: "網路出口", allowed: "允許的出口", check: "檢查", failed: "檢測失敗", region: "日本", mine: "不在你為 Claude 允許的出口內" },
  ja: { way: "ネットワーク出口", allowed: "許可する出口", check: "確認", failed: "確認できませんでした", region: "日本", mine: "Claude に許可した出口に含まれていません" },
  de: { way: "Ausgang ins Internet", allowed: "Erlaubte Ausgänge", check: "Prüfen", failed: "Prüfung fehlgeschlagen", region: "Japan", mine: "keiner der Ausgänge, die du für Claude erlaubt hast" },
};

function serve(lang, egress, settings, saved, checks) {
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        saved.push(JSON.parse(r.request().postData()));
        Object.assign(settings, saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/providers") {
      const p = provPrefsProviders(["claude"], { claude: "Claude Code" });
      if (egress.now) p.providers[0].egress = egress.now;
      return json(p);
    }
    if (url.pathname === "/api/egress/check") {
      checks.push(r.request().method());
      // judged against the list saved, as egress.Of does
      const ways = settings.claudeWaysOut || [];
      if (egress.fails) return json(egress.now = { state: "unknown", error: "context deadline exceeded", via: "direct" });
      egress.now = ways.length && !ways.some((w) => w.toUpperCase() === "JP" || w === "203.0.113.9")
        ? { state: "unsupported", loc: "JP", ip: "203.0.113.9", mine: true, via: "direct" }
        : { state: "ok", loc: "JP", ip: "203.0.113.9", mine: ways.length > 0, via: "direct" };
      return json(egress.now);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/claude/mode") return json({ tui: true, why: "" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
const fits = (loc, width) => loc.evaluate((e, width) => {
  const r = e.getBoundingClientRect();
  return r.width > 0 && r.left >= -1 && r.right <= width + 1;
}, width);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) for (const width of [1100, 420]) {
    const w = L[lang];
    test(`${engine} ${lang} ${width}: the way out is shown with its address, and the allowed ways out are saved and judged`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], saved = [], checks = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
      const egress = { now: { state: "ok", loc: "JP", ip: "203.0.113.9", via: "direct" } };
      const settings = { lang, theme: "light", redact: true, claudeRegionHold: true };
      await page.route("**/*", serve(lang, egress, settings, saved, checks));

      await openProvPrefs(page, "claude");
      const way = page.locator(".editor .prov-prefs #claudeRunList > #claudeWayOutRow");
      await inEditor(page, way);
      assert.equal(await way.locator(".name").textContent(), w.way);
      const where = await way.locator(".sub").textContent();
      assert(where.includes("203.0.113.9") && where.includes(w.region), where);
      const check = way.locator(".val button");
      assert.equal(await check.textContent(), w.check);
      assert(await fits(check, width), `Check fits at ${width}`);
      await check.click();
      for (let i = 0; i < 50 && !checks.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(checks, ["POST"]);

      // the allowed ways out: empty at first, typed in, saved on Enter
      const ways = page.locator(".editor .prov-prefs #claudeRunList > #claudeWaysOutRow");
      assert.equal(await ways.locator(".name").textContent(), w.allowed);
      const box = ways.locator("input");
      assert.equal(await box.inputValue(), "");
      assert(await fits(box, width), `the field fits at ${width}`);
      await box.fill("us, 198.51.100.0/24");
      await box.press("Enter");
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.deepEqual(saved[0].claudeWaysOut, ["us", "198.51.100.0/24"]);
      assert.equal(saved[0].claudeRegionHold, true, "the hold kept");
      assert.equal(saved[0].redact, true, "the rest kept");
      // judged again at once: JP isn't in the list now
      for (let i = 0; i < 50 && checks.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(checks.length, 2, "looked again after the list changed");
      await page.waitForFunction(() => document.querySelector("#claudeWayOutRow .sub")?.classList.contains("warn"));
      await box.blur();
      assert.equal(saved.length, 1, "leaving the field unchanged saves nothing more");

      // the line over the list names the address and the user's list
      await page.goto("http://magpie.test/?view=providers");
      const line = page.locator("#movable .deprecation.egress");
      await line.waitFor();
      const text = await line.innerText();
      assert(text.includes(w.mine) && text.includes("203.0.113.9"), text);
      assert(!text.includes("Anthropic"), "names Anthropic's list: " + text);
      assert(await fits(line, width), `the line fits at ${width}`);
      if (process.env.ARTIFACT_DIR) await line.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `ways-out-${engine}-${lang}-${width}.png`) });
      assert.deepEqual(errors, []);
    });
  }
}

// mamba on Discord, again: the address showed only after Check. The
// Providers list read before the gateway's first lookup ended has no
// egress, and an open editor isn't read again, so the editor looks it up
// as it opens; a lookup that fails says so, and isn't asked again on the
// next draw.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) for (const fails of [false, true]) {
    const w = L[lang];
    test(`${engine} ${lang}: with no lookup in the list, the editor looks the way out up as it opens${fails ? ", and a failed one says so" : ""}`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], saved = [], checks = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); });
      const egress = { now: null, fails };
      const settings = { lang, theme: "light", redact: true };
      await page.route("**/*", serve(lang, egress, settings, saved, checks));

      await openProvPrefs(page, "claude");
      const way = page.locator(".editor .prov-prefs #claudeRunList > #claudeWayOutRow");
      await inEditor(page, way);
      for (let i = 0; i < 100 && !checks.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(checks, ["POST"], "looked up once, with no click");
      if (fails) await page.waitForFunction((s) => document.querySelector("#claudeWayOutRow .sub")?.textContent === s, w.failed);
      else await page.waitForFunction(() => document.querySelector("#claudeWayOutRow .sub")?.textContent.includes("203.0.113.9"));
      const where = await way.locator(".sub").textContent();
      if (!fails) assert(where.includes(w.region), where);
      // a draw of the lists again (a setting saved) asks nothing more
      await page.evaluate(() => savePrefs({ ...prefs }));
      await page.waitForTimeout(300);
      assert.deepEqual(checks, ["POST"], "not asked again on the next draw");
      assert.deepEqual(errors, []);
    });
  }
}
