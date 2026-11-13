// Run with Node's test runner and Playwright on the module path; see README.md.
// The add sheet's search shows its hint whole (WJaunita45604 on X:
// 输入框里面的提示的字显示不全). Since v0.1.4044 the hint is "Find a vendor,
// or paste a URL and key…", in Chinese 查找供应商，或粘贴地址和密钥…, and the
// box was 160px: cut off in every language. The box is now as wide as its
// hint; where even that has no room (a narrow window's title bar, with the
// tools floated there, or a phone-wide row beside Custom provider and
// Import…) it gives the shorter "Find a provider", whole too.
// The hint's width is measured here by drawing it in a span with the box's
// own font, not as the app measures it. Every language, both engines, at
// 1300px, 1024px, 900px, 420px and 360px, in the sheet's row and floated in
// the title bar; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const preset = (id, name, kind) => ({ id, name, icon: "openai", kind, chat: `https://api.${id}.example.com/v1`, added: false });
const presets = [
  ...Array.from({ length: 90 }, (_, i) => preset("vendor-" + i, "Vendor " + i, "vendor")),
  ...Array.from({ length: 30 }, (_, i) => preset("relay-" + i, "Relay " + i, "relay")),
];
const providers = [{ id: "openai", name: "OpenAI", icon: "openai", preset: "", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

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

// the hint, its width drawn in the box's font, and the room the box gives it
const hint = (page) => page.evaluate(() => {
  const q = document.querySelector("#addSheet .find"), cs = getComputedStyle(q);
  const s = document.createElement("span");
  s.textContent = q.placeholder;
  s.style.cssText = `position:absolute;visibility:hidden;white-space:pre;font:${cs.font};letter-spacing:${cs.letterSpacing}`;
  document.body.append(s);
  const need = s.getBoundingClientRect().width;
  s.remove();
  const room = q.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  const bar = document.querySelector(".top").getBoundingClientRect(), f = q.getBoundingClientRect();
  return { text: q.placeholder, need, room, afloat: f.top >= bar.top - 1 && f.bottom <= bar.bottom + 1 };
});

async function wheel(page, dy) {
  const v = await page.locator("#view-providers").boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  await page.mouse.wheel(0, dy);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the add sheet's search shows its hint whole", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1300, 1024, 900, 420, 360]) {
        await t.test(`${lang} ${width}px`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 700 } });
          const page = await context.newPage();
          page.setDefaultTimeout(8000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const sheet = page.locator("#addSheet");
          await sheet.locator(".tile").first().waitFor();
          await sheet.evaluate((el) => Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)));
          const long = await page.evaluate(() => t("Find a vendor, or paste a URL and key…"));
          const short = await page.evaluate(() => t("Find a provider"));
          if (lang !== "en") assert.notEqual(short, "Find a provider", "the short hint in " + lang);

          // in its row there is room for the whole hint, at every width
          await page.waitForFunction(() => document.querySelector("#addSheet .find").style.getPropertyValue("--hint-w"));
          let h = await hint(page);
          assert(!h.afloat);
          assert([long, short].includes(h.text), h.text);
          if (width === 1300 || width === 900) assert.equal(h.text, long, "the whole hint in its row");
          assert(h.need <= h.room + 0.5, `the hint fits: ${h.need} > ${h.room} (${h.text})`);

          // floated in the title bar: whichever hint it gives, whole
          for (let i = 0; i < 40; i++) {
            const top = await page.evaluate(() => document.querySelector("#addSheet").getBoundingClientRect().top + 60 - document.getElementById("view-providers").getBoundingClientRect().top);
            if (top < 0) break;
            await wheel(page, 120);
            await page.waitForTimeout(30);
          }
          await page.waitForTimeout(250);
          h = await hint(page);
          assert([long, short].includes(h.text), h.text);
          assert(h.need <= h.room + 0.5, `the hint fits ${h.afloat ? "in the bar" : "in the held head"}: ${h.need} > ${h.room} (${h.text})`);
          if (width >= 1300) assert.equal(h.text, long, "the whole hint where the bar has room");

          // squeezed: the window made narrow under it, the hint goes short
          // rather than cut, and comes back with the room
          await page.setViewportSize({ width: Math.max(320, width - 300), height: 700 });
          await page.waitForTimeout(250);
          h = await hint(page);
          assert(h.need <= h.room + 0.5, `the hint fits once narrowed: ${h.need} > ${h.room} (${h.text})`);
          await page.setViewportSize({ width, height: 700 });
          await page.waitForTimeout(250);
          h = await hint(page);
          assert(h.need <= h.room + 0.5, `the hint fits once widened: ${h.need} > ${h.room} (${h.text})`);
          if (width >= 1300) assert.equal(h.text, long, "the whole hint back with the room");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
