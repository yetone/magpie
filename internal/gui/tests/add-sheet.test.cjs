// Run with Node's test runner and Playwright on the module path; see README.md.
// The Providers page's add sheet as quiet rows (Image #24: 这个页面看起来有点
// 乱糟糟的, then #25's design A3): under Subscriptions, Vendors, Relays and On
// this machine, each named with a word on what it is, the choices sit three
// to a line as an icon and a short name, with no frame and no second line
// (the plans, a long name and the host are in the row's title). One already
// added is not faded and says no word: a small green dot after its name, a
// subscription's account count beside it when more than one. A vendor's
// global and China presets are one row with nothing added to it, the
// regions' hosts in its title, its editor switching between them with
// the key kept. A custom provider is one line
// under them all, gone while searching. English and Chinese; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const preset = (id, name, kind, added, extra = {}) => ({ id, name, icon: "openai", kind, chat: `https://api.${id}.example.com/v1`, added, ...extra });
const presets = [
  preset("anthropic", "Anthropic", "vendor", true),
  preset("openai", "OpenAI", "vendor", false),
  preset("deepseek", "DeepSeek", "vendor", false),
  preset("moonshot", "Kimi", "vendor", false),
  preset("moonshot-cn", "Kimi (China)", "vendor", true, { chat: "https://api.moonshot.cn/v1" }),
  preset("tencent-token-plan", "Tencent Cloud Token Plan", "vendor", false, { short: "Tencent Cloud" }),
  preset("openrouter", "OpenRouter", "relay", true),
  preset("siliconflow", "SiliconFlow", "relay", false),
  preset("ollama", "Ollama", "local", false, { noKey: true }),
];
const prov = (id, name, extra = {}) => ({ id, name, icon: "openai", preset: id, models: [], agents: [], key: { set: true, masked: "sk-…ab12" }, ...extra });
const providers = [
  prov("anthropic", "Anthropic"), prov("openrouter", "OpenRouter"),
  prov("claude", "Claude", { preset: "", account: { agent: "claude", logins: [{ user: "a" }, { user: "b" }] } }),
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: {
    kinds: [["Subscriptions", "sign in, no key"], ["Vendors", "the makers' own APIs"], ["Relays", "one key, many vendors"], ["On this machine", ""]],
    regionField: "Region", china: "China", custom: "Custom provider", customHint: "any OpenAI or Anthropic compatible URL",
  },
  zh: {
    kinds: [["订阅", "登录即可，无需密钥"], ["供应商", "模型厂商的 API"], ["中转", "一个密钥，多家模型"], ["本机", ""]],
    regionField: "区域", china: "中国", custom: "自定义供应商", customHint: "任意 OpenAI / Anthropic 兼容的 URL",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the add sheet is quiet rows", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile").first().waitFor();

        // the sections, each a name and a word on what it is
        const kinds = await sheet.locator(".kind").evaluateAll((ks) => ks.map((k) => [k.querySelector("b").textContent, k.querySelector("span")?.textContent || ""]));
        assert.deepEqual(kinds, w.kinds);

        // rows: no frame, no second line, three to a line
        const rows = sheet.locator(".tile");
        const look = await rows.evaluateAll((rs) => rs.map((r) => {
          const s = getComputedStyle(r);
          return { border: s.borderTopWidth, opacity: s.opacity, lines: r.querySelectorAll(".s, .tt").length, over: r.scrollWidth > r.clientWidth + 1, h: r.getBoundingClientRect().height };
        }));
        for (const l of look) {
          assert.equal(l.border, "0px");
          assert.equal(l.opacity, "1");
          assert.equal(l.lines, 0);
          assert.equal(l.over, false);
          assert(l.h <= 40, "row " + l.h + "px tall");
        }
        const tops = await sheet.locator(".kind").nth(1).evaluate((k) => [...k.nextElementSibling.children].map((r) => Math.round(r.getBoundingClientRect().top)));
        assert.equal(tops.filter((y) => y === tops[0]).length, 3, "three vendors on the first line");

        // added: a green dot and no word, a subscription's count beside it
        const row = (name) => rows.filter({ has: page.locator(".n", { hasText: new RegExp("^" + name + "$") }) });
        for (const name of ["Anthropic", "OpenRouter", "Claude", "Kimi"]) assert.equal(await row(name).locator(".have").count(), 1, name);
        assert.equal(await row("OpenAI").locator(".have").count(), 0);
        assert.equal(await sheet.locator(".tile .st.added").count(), 0);
        assert.equal(await row("Claude").locator(".cnt").textContent(), "2");
        assert.equal(await row("Anthropic").locator(".cnt").count(), 0);
        const dot = await row("Anthropic").locator(".have").evaluate((e) => { const r = e.getBoundingClientRect(); return [r.width, r.height, getComputedStyle(e).backgroundColor]; });
        assert.deepEqual(dot.slice(0, 2), [6, 6]);
        assert.notEqual(dot[2], "rgba(0, 0, 0, 0)");
        assert.match(await row("OpenAI").getAttribute("title"), /api\.openai\.example\.com/);
        // short names, the long one in the title
        assert.equal(await row("Grok").count(), 1);
        assert.match(await row("Grok").getAttribute("title"), /SuperGrok/);
        assert.match(await row("Tencent Cloud").getAttribute("title"), /Tencent Cloud Token Plan/);
        // a vendor's global and China presets: one row
        assert.equal(await sheet.locator(".tile .n", { hasText: "China" }).count(), 0);
        // with nothing added to the row (B1): the regions and hosts are in its title
        assert.equal(await row("Kimi").locator(".st").count(), 0);
        assert.equal((await row("Kimi").textContent()).trim(), "Kimi");
        const kt = await row("Kimi").getAttribute("title");
        assert.match(kt, new RegExp(w.china + " api\\.moonshot\\.cn"));
        assert.match(kt, /api\.moonshot\.example\.com/);

        // the custom provider: one line at the foot, gone while searching
        const foot = sheet.locator(".custom-foot");
        assert.equal(await foot.locator(".custom").textContent(), w.custom);
        assert.equal(await foot.locator(".hint").textContent(), w.customHint);
        await sheet.locator(".find").fill("deep");
        assert.deepEqual(await rows.locator(".n").allTextContents(), ["DeepSeek"]);
        assert.equal(await foot.count(), 0);
        await sheet.locator(".find").fill("china");
        assert.deepEqual(await rows.locator(".n").allTextContents(), ["Kimi"]);
        await sheet.locator(".find").fill("");

        // the pair's row adds the one not added (global here); its editor
        // switches to China keeping the key typed, with the page left where it was
        await row("Kimi").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".ehead b", { hasText: /^Kimi$/ }).waitFor();
        const seg = ed.locator(".segs.area");
        assert.equal(await ed.locator("label", { hasText: new RegExp("^" + w.regionField + "$") }).count(), 1);
        assert.equal(await seg.locator(".opt.on").textContent(), lang === "zh" ? "国际" : "Global");
        const key = ed.locator("input[type=password]").first();
        await key.fill("sk-test-kept");
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await seg.locator(".opt", { hasText: w.china }).click();
        await ed.locator(".ehead b", { hasText: "Kimi (China)" }).waitFor();
        assert.equal(await ed.locator("input[type=password]").first().inputValue(), "sk-test-kept");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y);

        // Escape closes it, folding back into Kimi's row (re-rendered meanwhile)
        // each move of the dialog as it is started: where it goes, where the
        // dialog sits at rest (its layout box, which no transform moves) and
        // where the row it came from is then, the sheet re-rendered
        await page.evaluate(() => {
          window.moves = [];
          const animate = Element.prototype.animate;
          Element.prototype.animate = function (frames, opts) {
            if (this.matches("#modal .dialog") && frames.some((f) => f.transform)) {
              const m = this.parentElement.getBoundingClientRect(), rowOf = (name) => [...document.querySelectorAll("#addSheet .tile")].find((r) => r.querySelector(".n").textContent === name)?.getBoundingClientRect().toJSON();
              window.moves.push({ frames: frames.map((f) => f.transform), ms: opts.duration, easing: opts.easing, out: this.parentElement.classList.contains("out"),
                box: { x: m.x + this.offsetLeft, y: m.y + this.offsetTop, width: this.offsetWidth, height: this.offsetHeight }, kimi: rowOf("Kimi"), deep: rowOf("DeepSeek") });
            }
            return animate.call(this, frames, opts);
          };
        });
        await page.keyboard.press("Escape");
        const fold = await page.evaluate(() => window.moves.at(-1));
        if (fold) fold.to = fold.frames.at(-1);
        assert(fold?.out, "closing");
        assert(fold.ms >= 300, "folds for " + fold?.ms + " ms");
        const { box, kimi } = fold;
        const [dx, dy, sc] = fold.to.match(/-?[\d.]+/g).map(Number);
        assert(Math.abs(box.x + box.width / 2 + dx - (kimi.x + kimi.width / 2)) < 2, "to Kimi's row across");
        assert(Math.abs(box.y + box.height / 2 + dy - (kimi.y + kimi.height / 2)) < 2, "to Kimi's row down");
        assert(sc < 1);
        await page.locator("#modal").waitFor({ state: "hidden" });

        // a row opens what it did: a new preset's editor, grown out of the row on a spring
        await row("DeepSeek").click();
        await page.locator(".editor.new .ehead b", { hasText: "DeepSeek" }).waitFor();
        const grow = await page.evaluate(() => window.moves.at(-1));
        if (grow) grow.from = grow.frames[0], grow.at = grow.box;
        assert(grow && !grow.out, "the dialog grows");
        assert.match(grow.easing, /^(linear|cubic-bezier)\(/);
        const [gx, gy] = grow.from.match(/-?[\d.]+/g).map(Number);
        const { at, deep } = grow;
        assert(Math.abs(at.x + at.width / 2 + gx - (deep.x + deep.width / 2)) < 2, "from DeepSeek's row across");
        assert(Math.abs(at.y + at.height / 2 + gy - (deep.y + deep.height / 2)) < 2, "from DeepSeek's row down");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
