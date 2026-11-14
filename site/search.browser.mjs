// The docs' search in real browsers: Chromium and WebKit, each docs
// language, wide to 320px, light and dark. Not a *.test.mjs, so CI's
// `node --test site/*.test.mjs` (no browsers there) leaves it out; run it by
// hand where Playwright is on NODE_PATH:
//   node site/search.browser.mjs [screenshot-dir]
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { createRequire } from "node:module";
import assert from "node:assert/strict";

const { chromium, webkit } = createRequire(import.meta.url)("playwright");
const pub = new URL("./public/", import.meta.url);
const shots = process.argv[2];
if (shots) await mkdir(shots, { recursive: true });

const types = { html: "text/html; charset=utf-8", js: "text/javascript", css: "text/css", json: "application/json", svg: "image/svg+xml", png: "image/png", txt: "text/plain" };
let served = [];
const server = createServer(async (req, res) => {
  let p = decodeURIComponent(new URL(req.url, "http://x").pathname).replace(/^\/+/, "");
  served.push("/" + p);
  if (!/\.\w+$/.test(p)) p += ".html";
  try {
    const body = await readFile(new URL(p, pub));
    res.writeHead(200, { "content-type": types[p.split(".").pop()] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const base = `http://127.0.0.1:${server.address().port}`;

const langs = {
  en: { dir: "/docs/", q: "download source mirror", none: "Nothing found for “zzqqxx”", ph: "Search the docs" },
  zh: { dir: "/docs/zh/", q: "下载源 镜像", none: "没有找到与“zzqqxx”相关的内容", ph: "搜索文档" },
  ja: { dir: "/docs/ja/", q: "ダウンロード元 ミラー", none: "「zzqqxx」に一致する内容はありません", ph: "ドキュメントを検索" },
};
let fails = 0, runs = 0;

for (const [ename, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  const browser = await engine.launch();
  for (const scheme of ["light", "dark"]) {
    for (const width of [1280, 900, 390, 320]) {
      for (const [lang, L] of Object.entries(langs)) {
        const name = `${ename} ${scheme} ${width} ${lang}`;
        runs++;
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, colorScheme: scheme, reducedMotion: width === 390 ? "reduce" : "no-preference" });
        const page = await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(String(e)));
        page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
        try {
          served = [];
          await page.goto(base + L.dir + "mirrors", { waitUntil: "load" });
          // nothing of the search loads until it is wanted
          assert.ok(!served.some((p) => p.startsWith("/search/") || p === "/docs-search.js"), "the search loaded with the page");

          // one search button shows: the nav's when wide, the contents bar's on a phone
          const vis = await page.$$eval("button.search", (bs) => bs.filter((b) => b.offsetParent).map((b) => b.className));
          assert.deepEqual(vis, [width > 900 ? "icon-btn search in-nav" : "icon-btn search in-bar"], "visible search buttons");
          const btn = page.locator("button.search:visible");
          const box = await btn.boundingBox();
          assert.ok(box.x >= 0 && box.x + box.width <= width, `button off screen: ${JSON.stringify(box)}`);
          const sideways = () => page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
          assert.ok((await sideways()) <= 0, "the page scrolls sideways");

          // a pointer over the button starts the loading (the mouse, not
          // the locator: Playwright's scrolling into view moves the page
          // under a sticky bar, which no reader does)
          await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
          await page.waitForFunction(() => window.docsSearch, null, { timeout: 5000 });

          // scrolled down a little, then opened by the button: the page stays put
          await page.evaluate(() => scrollTo({ top: 600, behavior: "instant" }));
          const y0 = await page.evaluate(() => scrollY);
          assert.equal(y0, 600);
          await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
          const input = page.locator(".sx-panel input");
          await input.waitFor({ state: "visible" });
          assert.equal(await input.evaluate((i) => i === document.activeElement), true, "the field has the focus");
          assert.equal(await input.getAttribute("placeholder"), L.ph);
          assert.equal(await page.locator(".sx-item").count(), 0, "nothing listed before typing");

          await input.fill(L.q);
          await page.locator(".sx-item").first().waitFor();
          const first = await page.locator(".sx-item").first().getAttribute("href");
          const top3 = await page.locator(".sx-item").evaluateAll((as) => as.slice(0, 3).map((a) => a.getAttribute("href")));
          assert.ok(top3.includes(L.dir + "mirrors#settings"), `top results: ${top3}`);
          assert.ok((await page.locator(".sx-item mark").count()) > 0, "no marked words");
          assert.equal(await page.locator('.sx-item[aria-selected="true"]').getAttribute("href"), first, "the first result is picked");
          await page.keyboard.press("ArrowDown");
          assert.equal(await page.locator('.sx-item[aria-selected="true"]').getAttribute("id"), "sx-1", "ArrowDown picks the next");
          await page.keyboard.press("ArrowUp");
          const panel = await page.locator(".sx-panel").boundingBox();
          assert.ok(panel.x >= 8 && panel.x + panel.width <= width - 8, `panel out of the screen: ${JSON.stringify(panel)}`);
          assert.ok(panel.y + panel.height <= 800, "panel taller than the screen");
          assert.equal(await page.evaluate(() => scrollY), y0, "opening and typing scrolled the page");
          assert.ok((await sideways()) <= 0, "the page scrolls sideways with the dialog");
          // it is shown, faded in all the way
          await page.waitForFunction(() => getComputedStyle(document.querySelector(".sx")).opacity === "1", null, { timeout: 2000 });
          if (shots) await page.screenshot({ path: `${shots}/${ename}-${scheme}-${width}-${lang}.png` });

          // the panel is the card's colour, which the theme sets
          const bg = await page.locator(".sx-panel").evaluate((p) => getComputedStyle(p).backgroundColor);
          assert.equal(bg, scheme === "dark" ? "rgb(22, 22, 24)" : "rgb(255, 255, 255)");

          await input.fill("zzqqxx");
          await page.locator(".sx-none").waitFor();
          assert.equal(await page.locator(".sx-none").textContent(), L.none);

          // Escape closes it and gives the focus back to the button
          await page.keyboard.press("Escape");
          assert.equal(await page.locator(".sx").isHidden(), true, "Escape left it open");
          assert.equal(await btn.evaluate((b) => b === document.activeElement), true, "focus not back on the button");

          // / and Cmd/Ctrl+K open it; / typed in the field is a character
          await page.locator("article h1").click();
          await page.keyboard.press("/");
          await input.waitFor({ state: "visible" });
          assert.equal(await input.inputValue(), "zzqqxx", "the field keeps what was typed, selected");
          await page.keyboard.type("a/b");
          assert.equal(await input.inputValue(), "a/b");
          await page.keyboard.press("Escape");
          await page.keyboard.press("ControlOrMeta+k");
          await input.waitFor({ state: "visible" });

          // a click on the scrim closes it
          await page.mouse.click(4, 790);
          assert.equal(await page.locator(".sx").isHidden(), true, "the scrim's click left it open");

          // Enter on a section of this page goes to it, dialog closed
          await page.keyboard.press("ControlOrMeta+k");
          await input.fill(L.q);
          await page.locator(`.sx-item[href="${L.dir}mirrors#settings"]`).waitFor();
          const i = await page.locator(".sx-item").evaluateAll((as, h) => as.findIndex((a) => a.getAttribute("href") === h), L.dir + "mirrors#settings");
          for (let k = 0; k < i; k++) await page.keyboard.press("ArrowDown");
          await page.keyboard.press("Enter");
          await page.waitForFunction(() => location.hash === "#settings");
          assert.equal(await page.locator(".sx").isHidden(), true);
          const top = await page.locator("#settings").evaluate((h) => h.getBoundingClientRect().top);
          assert.ok(top >= 0 && top < 300, `#settings at ${top}`);

          // Enter on another page goes there
          await page.keyboard.press("ControlOrMeta+k");
          await input.fill(lang === "en" ? "update proxy" : lang === "zh" ? "代理" : "プロキシ");
          await page.locator(".sx-item").first().waitFor();
          const other = await page.locator('.sx-item[aria-selected="true"]').getAttribute("href");
          if (!other.startsWith(L.dir + "mirrors")) {
            await Promise.all([page.waitForURL((u) => u.pathname === other.split("#")[0]), page.keyboard.press("Enter")]);
          }
          assert.deepEqual(errors, []);
          console.log("ok  ", name);
        } catch (e) {
          fails++;
          console.log("FAIL", name, e.message.split("\n").slice(0, 6).join("\n     "));
          if (shots) await page.screenshot({ path: `${shots}/FAIL-${ename}-${scheme}-${width}-${lang}.png` }).catch(() => {});
        }
        await ctx.close();
      }
    }
  }
  await browser.close();
}
server.close();
console.log(`${runs - fails}/${runs} ok`);
process.exit(fails ? 1 : 0);
