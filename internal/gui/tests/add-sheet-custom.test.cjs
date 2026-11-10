// Run with Node's test runner and Playwright on the module path; see README.md.
// The add sheet's custom provider is in sight as the sheet opens (jackutan on
// X: hard to find): a button beside the search, above every preset, kept
// while searching, where it was one line under every section and gone while
// searching. A search that finds nothing still offers "Add it as a custom
// provider". Every language, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));

const preset = (id, name, kind) => ({ id, name, icon: "openai", kind, chat: `https://api.${id}.example.com/v1`, added: false });
// enough presets that the sections run past a short window
const presets = [];
for (const kind of ["vendor", "relay", "local"]) for (let i = 0; i < 18; i++) presets.push(preset(kind + i, kind[0].toUpperCase() + kind.slice(1) + " " + i, kind));
const providers = [{ id: "openrouter", name: "OpenRouter", icon: "openai", preset: "relay0", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { custom: "Custom provider", hint: "any OpenAI or Anthropic compatible URL", none: "Add it as a custom provider" },
  zh: { custom: "自定义供应商", hint: "任意 OpenAI / Anthropic 兼容的 URL", none: "添加为自定义供应商" },
  "zh-TW": { custom: "自定義供應商", hint: "任意 OpenAI / Anthropic 相容的 URL", none: "新增為自定義供應商" },
  ja: { custom: "カスタムプロバイダ", hint: "OpenAI または Anthropic 互換の任意の URL", none: "カスタムプロバイダとして追加" },
  de: { custom: "Benutzerdefinierter Anbieter", hint: "jede OpenAI- oder Anthropic-kompatible URL", none: "Als benutzerdefinierten Anbieter hinzufügen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the custom provider is in sight as the add sheet opens", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 761, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const ctx = await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile").first().waitFor();

        // beside the search, above the first preset, in the window as it opens
        const custom = sheet.locator(".row-head .custom");
        assert.equal(await custom.count(), 1);
        assert.equal((await custom.textContent()).trim(), w.custom);
        assert.equal(await custom.getAttribute("title"), w.hint);
        const box = await page.evaluate(() => {
          const r = (e) => e.getBoundingClientRect();
          const c = document.querySelector("#addSheet .row-head .custom"), head = c.parentElement, find = head.querySelector(".find");
          const tile = document.querySelector("#addSheet .tile");
          const els = [...head.children].map(r);
          return {
            c: r(c), find: r(find), tile: r(tile), head: r(head), sheet: r(document.querySelector("#addSheet")), vw: innerWidth, vh: innerHeight,
            overlap: els.some((a, i) => els.some((b, j) => i < j && a.width && b.width && a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5)),
            out: [...head.children].filter((e) => r(e).width && (r(e).left < r(head).left - 1 || r(e).right > Math.min(r(head).right, innerWidth) + 1)).map((e) => e.textContent || e.className),
            wraps: c.scrollWidth > c.clientWidth + 1, lines: Math.round(r(c).height),
          };
        });
        assert(box.c.top >= 0 && box.c.bottom <= box.vh, "in the window: " + JSON.stringify(box.c));
        assert(box.c.left >= box.sheet.left - 1 && box.c.right <= Math.min(box.sheet.right, box.vw) + 1, "inside the sheet: " + JSON.stringify([box.c, box.sheet]));
        assert(box.c.bottom <= box.tile.top, "above the presets");
        assert(Math.abs((box.c.top + box.c.bottom) / 2 - (box.find.top + box.find.bottom) / 2) <= 2, "on the search's line");
        assert(!box.overlap, "the head's controls apart");
        assert(box.out.length === 0, "the head's controls inside the sheet: " + box.out);
        assert(!box.wraps && box.lines <= 26, "one line, unclipped: " + box.lines);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}.png`) });
        // no second entry at the foot
        assert.equal(await sheet.locator(".custom-foot").count(), 0);

        // kept while a search finds something
        await sheet.locator(".find").fill(lang === "en" ? "relay 1" : "Relay 1");
        await sheet.locator(".tile").first().waitFor();
        assert(await custom.isVisible());
        // and one that finds nothing still offers it in its line
        await sheet.locator(".find").fill("zzqq");
        const none = sheet.locator(".none button", { hasText: w.none });
        await none.waitFor();
        assert(await custom.isVisible());
        await none.click();
        await page.locator(".editor.new").waitFor();
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });

        // the head's button opens the custom editor
        await sheet.locator(".find").fill("");
        await custom.click();
        await page.locator(".editor.new").waitFor();
        assert.deepEqual(errors, []);
      });
    }
  });
}
