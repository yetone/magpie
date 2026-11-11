// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner (11-02): Settings' lines that explain a setting are hidden, and
// shown on hover, so the page reads as its names and controls ("很简洁，很有
// 呼吸感"). In the window, in both engines, at 1100px and 420px, in every
// language, on Settings › Models:
// - at rest a row's explanation is out of sight (Image recognition MCP
//   server's, Image generation's), but still in the page, for a screen
//   reader and the search;
// - the pointer resting on the row shows it whole in a card under or over
//   the row, not over its name, and no row on the page moves;
// - the pointer leaving the row takes the card away;
// - a key reaching the row's control shows it too;
// - an error (a picked model that's gone) and a warning (the image model
//   picked is unticked in its provider) stay in sight at rest;
// - a search shows the explanations in place, and finds a row by them.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function settings(lang) {
  return {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "none", proxySource: "none",
    redact: true, redactPersonal: true, redactWords: [], version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    lanURLs: [], searchVendors: [], searchAPIs: [],
    // the picked model is gone: Image recognition says so, in red
    vision: "zz/gone", visionMissing: "zz/gone", visionAuto: "codex/gpt-5",
    visionModels: [{ id: "codex/gpt-5", name: "GPT-5", provider: "codex", providerName: "Codex" }],
    // the image model picked is unticked in its provider: a warning
    imageGen: "codex/gpt-image-2", imageGenHidden: true, imageGenAuto: "codex/gpt-image-2",
    imageGenModels: [{ id: "codex/gpt-image-2", name: "GPT Image", provider: "codex", providerName: "Codex", icon: "openai", vendorPicks: true }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(lang) });
    if (url.pathname === "/api/settings") return json(settings(lang));
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins/builtin") return json([]);
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

// every row's box on the part shown, to tell that none moved
const boxes = () => [...document.querySelectorAll("#setPage-models .row.pref")].map((r) => {
  const b = r.getBoundingClientRect();
  return [Math.round(b.top), Math.round(b.height)].join(",");
});
const inSight = (e) => { const b = e.getBoundingClientRect(); return b.height > 4 && b.width > 4; };

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  test(`${engine}: Settings' explanations show on hover and focus`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
        await page.route("**/*", serve(lang));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=models");
        await settingsDrawn(page);
        await openPart(page, "models");
        const mcp = page.locator("#visionMcpRow");
        await mcp.waitFor();
        const hint = mcp.locator(":scope > .who > .sub").first();
        const text = (await hint.textContent()).trim();
        assert.ok(text.length > 20, "the MCP server row has its explanation");

        // at rest: out of sight, but in the page
        assert.equal(await hint.evaluate(inSight), false, "the explanation shows at rest");
        assert.equal(await hint.evaluate((e) => e.isConnected && !e.hidden), true);
        // an error and a warning stay in sight
        const err = page.locator("#imagesList .vision-missing");
        await err.waitFor({ state: "attached" });
        assert.equal(await err.evaluate(inSight), true, "the gone model's error is out of sight");
        const warn = page.locator("#imagesList .image-gen-hidden");
        await warn.waitFor({ state: "attached" });
        assert.equal(await warn.evaluate(inSight), true, "the unticked image model's warning is out of sight");
        // a line with something to press in it stays in sight, as it is
        // drawn: given a link, the MCP line shows; the link gone, it rests
        // again (settingshints.js marks it .acts; Safari 15.0 has no :has())
        await hint.evaluate((e) => e.append(Object.assign(document.createElement("a"), { href: "#", textContent: "Open" })));
        await page.waitForFunction((e) => { const b = e.getBoundingClientRect(); return b.height > 4 && b.width > 4; }, await hint.elementHandle());
        await hint.evaluate((e) => e.querySelector("a").remove());
        await page.waitForFunction((e) => e.getBoundingClientRect().height <= 4, await hint.elementHandle());

        // the pointer resting on the row: a card with the whole line, and
        // nothing moves
        await mcp.evaluate((e) => e.scrollIntoView({ block: "center" }));
        await page.mouse.move(1, 1);
        const before = await page.evaluate(boxes);
        await mcp.locator(":scope > .who > .name").hover();
        const card = page.locator(".set-hint.on");
        await card.waitFor();
        assert.equal((await card.textContent()).includes(text), true, "the card doesn't say the row's line");
        const name = await mcp.locator(":scope > .who > .name").boundingBox(), cb = await card.boundingBox();
        assert.ok(cb.y >= name.y + name.height || cb.y + cb.height <= name.y, "the card covers the row's name");
        assert.ok(cb.x >= 15 && cb.x + cb.width <= width - 15, `the card runs out of the window: ${JSON.stringify(cb)}`);
        // its text starts where the row's name does (11-11, mmmg_btc on X: "设置里面的提示也没有对齐"),
        // where the window leaves room for the card's padding to the left of it
        const line = await card.locator("p").first().boundingBox();
        if (name.x >= 16 + 12) assert.ok(Math.abs(line.x - name.x) <= 1, `the card's text is at x=${line.x}, the row's name at x=${name.x}`);
        else assert.ok(line.x >= name.x - 1, `the card's text is at x=${line.x}, left of the row's name at x=${name.x}`);
        assert.deepEqual(await page.evaluate(boxes), before, "a row moved when the card showed");
        assert.equal(await card.evaluate((e) => getComputedStyle(e).pointerEvents), "none");

        // the pointer gone: the card goes
        await page.mouse.move(1, 1);
        await page.locator(".set-hint").waitFor({ state: "hidden" });

        // a key reaching the row's control shows it
        const ctl = mcp.locator(":scope > .val button").first();
        await ctl.focus();
        // (WebKit's Tab passes buttons by, as Safari's does; Option-Tab doesn't)
        const tab = engine === "webkit" ? "Alt+Tab" : "Tab";
        await page.keyboard.press("Shift+" + tab);
        await page.keyboard.press(tab);
        assert.equal(await mcp.evaluate((r) => r.contains(document.activeElement)), true, "Tab didn't come back to the row's copy button");
        await card.waitFor();
        assert.equal((await card.textContent()).includes(text), true, "focus showed another row's card");
        await page.mouse.move(1, 1);

        // the search: explanations in place, and a row found by one
        const words = text.split(/\s+/).find((w) => w.length > 5 && !/[<>:]/.test(w)) || text.slice(0, 6);
        await page.locator("#setQ").fill(words.toLowerCase());
        await page.locator("#view-settings.set-searching").waitFor();
        await mcp.waitFor();
        assert.equal(await hint.evaluate(inSight), true, "a search keeps the explanation out of sight");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
