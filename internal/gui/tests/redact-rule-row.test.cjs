// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Masking rules row (TechVerser on X: 这个地方错位了吗？): its words
// are shown in full, and its fields — a name, prefix or regex, what to match,
// Add — sit on one line under them, from the row's left edge to its right,
// none on top of another, the rules already added listed after it as before.
// In English and Chinese, in a wide window and a narrow one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const settings = (lang) => ({ lang, theme: "light", redact: true, redactRules: [{ kind: "GATEWAY_KEY", prefix: "oc_sk_" }] });
const words = {
  en: { name: "Masking rules", sub: "Secrets magpie doesn't know", regex: "Regex" },
  zh: { name: "自定义脱敏规则", sub: "", regex: "正则" },
};

function serve(lang) {
  const state = { agents: [], profiles: [], settings: settings(lang) };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings(lang));
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 640]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: the masking rules row lines up`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        const row = page.locator("#redactList .row.rule-row");
        await row.waitFor();
        await row.scrollIntoViewIfNeeded();

        const m = await row.evaluate((r) => {
          const box = (e) => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top, b: b.bottom, mid: (b.top + b.bottom) / 2 }; };
          const sub = r.querySelector(".sub");
          const val = r.querySelector(".val");
          return {
            name: r.querySelector(".name").textContent, sub: sub.textContent,
            cut: sub.scrollWidth > sub.clientWidth + 1 || sub.scrollHeight > sub.clientHeight + 1,
            who: box(r.querySelector(".who")), val: box(val), subBox: box(sub),
            tools: [...val.children].map((e) => ({ cls: e.className, ...box(e) })),
          };
        });
        assert.equal(m.name, w.name);
        assert(m.sub.includes(w.sub) && m.sub.length > 20, `the row's words: ${m.sub}`);
        assert(!m.cut, "the row's words are shown in full");
        assert.deepEqual(m.tools.map((e) => e.cls.split(" ")[0]), ["words", "segs", "words", "text"], "name, prefix or regex, match, Add");
        const mid = m.tools[0].mid;
        for (const e of m.tools) assert(Math.abs(e.mid - mid) < 2, `on one line: ${JSON.stringify(m.tools)}`);
        for (let i = 1; i < m.tools.length; i++) assert(m.tools[i].l >= m.tools[i - 1].r, `none on top of another: ${JSON.stringify(m.tools)}`);
        assert(m.tools[0].t >= m.subBox.b, "the fields are under the words");
        assert(Math.abs(m.tools[0].l - m.who.l) < 1.5, `from the row's left edge: ${m.tools[0].l} vs ${m.who.l}`);
        assert(Math.abs(m.tools[3].r - m.val.r) < 1.5, `to its right: ${m.tools[3].r} vs ${m.val.r}`);

        // the rule already added: its name, and its remove button on the right, on its line
        const next = await row.evaluate((r) => {
          const n = r.nextElementSibling, v = n.querySelector(".val").getBoundingClientRect(), who = n.querySelector(".who").getBoundingClientRect();
          return { name: n.querySelector(".name").textContent, same: Math.abs((v.top + v.bottom) / 2 - (who.top + who.bottom) / 2) < 12, right: v.left > who.left + 100 };
        });
        assert.equal(next.name, "GATEWAY_KEY");
        assert(next.same && next.right, "the added rule's row is as before");

        // Regex picked: the match's hint changes, nothing moves
        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings")?.closest("[class]")?.scrollTop]);
        const b = await row.locator(".segs button", { hasText: w.regex }).boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        assert.match(await row.locator("input.rule-match").getAttribute("placeholder"), /\[A-Za-z0-9\]/);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings")?.closest("[class]")?.scrollTop]), before, "the click moved nothing");
        assert.deepEqual(errors, []);
      });
    }
  }
}
