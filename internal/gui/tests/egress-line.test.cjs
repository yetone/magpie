// Run with Node's test runner and Playwright on the module path; see README.md.
// The "cyber canary" (@alterxyz4 on X): while the Claude accounts' way out
// to the internet is in a region Anthropic doesn't list, a quiet line over
// the Providers list says where, in the reader's language, and Check again
// looks it up now; once the way out is back in a supported region the line
// goes. A way out the lookup couldn't tell (Unknown) says nothing. The
// Claude editor's "Hold requests outside supported regions" is Off at
// first, and On saves claudeRegionHold with the rest kept; the line then
// says requests are held. Neither says anything of bans. In every
// language, 1100 and 420 wide; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const L = {
  en: { head: "Claude's way out to the internet is in China", why: "isn't on Anthropic's list of supported regions", held: "Claude requests are held until it changes", again: "Check again", name: "Hold requests outside supported regions", sub: "A lookup that fails holds nothing", off: "Off", on: "On" },
  zh: { head: /Claude 的网络出口在中国(大陆)?/, why: "不在 Anthropic 支持的地区列表中", held: "Claude 请求会被暂停", again: "重新检测", name: "在不支持的地区时暂停请求", sub: "检测失败时不会暂停任何请求", off: "关闭", on: "开启" },
  "zh-TW": { head: /Claude 的網路出口在中國(大陸)?/, why: "不在 Anthropic 支援的地區列表中", held: "Claude 請求會被暫停", again: "重新檢測", name: "在不支援的地區時暫停請求", sub: "檢測失敗時不會暫停任何請求", off: "關閉", on: "開啟" },
  ja: { head: /Claude のネットワーク出口が中国(本土)?にあります/, why: "サポート対象地域の一覧にありません", held: "Claude のリクエストは保留されます", again: "再確認", name: "サポート対象外の地域ではリクエストを保留", sub: "確認に失敗したときは何も保留しません", off: "オフ", on: "オン" },
  de: { head: "Claudes Weg ins Internet führt über China", why: "steht nicht auf Anthropics Liste unterstützter Regionen", held: "werden zurückgehalten", again: "Erneut prüfen", name: "Anfragen außerhalb unterstützter Regionen zurückhalten", sub: "Eine fehlgeschlagene Prüfung hält nichts zurück", off: "Aus", on: "An" },
};

function serve(lang, w, egress, settings, saved, checks) {
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
      egress.now = { state: "ok", loc: "US", via: "direct" };
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
const noBan = (s) => !/\bban|封|停用|sperr|BAN/i.test(s);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) for (const width of [1100, 420]) {
    const w = L[lang];
    test(`${engine} ${lang} ${width}: the line over the list says where Claude's way out is, and the hold can be turned on`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], saved = [], checks = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
      const egress = { now: { state: "unsupported", loc: "CN", via: "direct" } };
      const settings = { lang, theme: "light", redact: true, claudeNoExtraUsage: true };
      await page.route("**/*", serve(lang, w, egress, settings, saved, checks));

      // the hold, in the Claude editor: Off at first, On saved with the rest
      await openProvPrefs(page, "claude");
      const row = page.locator(".editor .prov-prefs #claudeRunList > #claudeRegionRow");
      await inEditor(page, row);
      assert.equal(await row.locator(".name").textContent(), w.name);
      const sub = await row.locator(".sub").textContent();
      assert(sub.includes(w.sub), sub);
      assert(noBan(sub), sub);
      for (const part of [".name", ".segs"]) {
        const fit = await row.locator(part).first().evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        assert(fit, `${part} fits at ${width}`);
      }
      const on = row.locator(".segs button", { hasText: w.on });
      assert.equal(await row.locator(".segs button", { hasText: w.off }).getAttribute("aria-pressed"), "true", "Off at first");
      const b = await on.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].claudeRegionHold, true);
      assert.equal(saved[0].claudeNoExtraUsage, true, "the row beside it kept");
      assert.equal(saved[0].redact, true, "the rest kept");

      // the line over the list
      await page.goto("http://magpie.test/?view=providers");
      const line = page.locator("#movable .deprecation.egress");
      await line.waitFor();
      const text = await line.innerText();
      // the region by the engine's own name for it (WebKit's ja says 中国本土)
      assert(typeof w.head === "string" ? text.includes(w.head) : w.head.test(text), text);
      assert(text.includes(w.why), text);
      assert(text.includes(w.held), "doesn't say requests are held: " + text);
      assert(noBan(text), text);
      const box = await line.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= width + 1, `the line runs off: ${JSON.stringify(box)}`);
      if (process.env.ARTIFACT_DIR) await line.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `egress-${engine}-${lang}-${width}.png`) });
      const again = line.locator("button.action");
      assert.equal(await again.innerText(), w.again);
      await again.click();
      await page.waitForFunction(() => !document.querySelector("#movable .deprecation.egress"));
      assert.deepEqual(checks, ["POST"]);
      assert.deepEqual(errors, []);
    });
  }
}

// A way out the lookup couldn't tell, or one in a supported region, draws
// no line; nor does the hold's being off say requests are held.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an unknown or supported way out draws no line, and with the hold off nothing is said held`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
    const egress = { now: { state: "unknown", loc: "XX", via: "direct" } };
    const settings = { lang: "en", theme: "light" };
    await page.route("**/*", serve("en", L.en, egress, settings, [], []));
    for (const now of [{ state: "unknown", loc: "XX" }, { state: "unknown", error: "timeout" }, { state: "ok", loc: "JP" }]) {
      egress.now = now;
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof providers === "object" && providers?.providers?.length);
      await page.waitForTimeout(200);
      assert.equal(await page.locator("#movable .deprecation.egress").count(), 0, JSON.stringify(now));
    }
    egress.now = { state: "unsupported", loc: "RU", via: "http://127.0.0.1:7890" };
    await page.goto("http://magpie.test/?view=providers");
    const line = page.locator("#movable .deprecation.egress");
    await line.waitFor();
    const text = await line.innerText();
    assert(text.includes("Russia"), text);
    assert(!text.includes(L.en.held), "said held with the hold off: " + text);
    assert.equal(await line.getAttribute("title"), "Looked up through http://127.0.0.1:7890");
    assert.deepEqual(errors, []);
  });
}
