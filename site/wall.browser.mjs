// The contributors page's finder and row links in real browsers: Chromium
// and WebKit, each language, 1280 and 390 wide, and with script off. Not a
// *.test.mjs, so CI's `node --test site/*.test.mjs` (no browsers there)
// leaves it out; run it by hand where Playwright is on NODE_PATH, after
// `node site/credits-cli.mjs wall` has built public/contributors.json (or
// with WALL_JSON naming such a file):
//   node site/wall.browser.mjs [screenshot-dir]
// The server puts each language's table (renderWall) into the page's
// #wall, as worker.js withWall does with the one the deploy rendered.
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { createRequire } from "node:module";
import assert from "node:assert/strict";
import { renderWall } from "./credits.mjs";

const { chromium, webkit } = createRequire(import.meta.url)("playwright");
const pub = new URL("./public/", import.meta.url);
const shots = process.argv[2];
if (shots) await mkdir(shots, { recursive: true });
const data = JSON.parse(await readFile(process.env.WALL_JSON || new URL("contributors.json", pub), "utf8"));
const tables = Object.fromEntries(["en", "zh", "ja"].map((l) => [l, renderWall(data, l)]));

const types = { html: "text/html; charset=utf-8", js: "text/javascript", css: "text/css", json: "application/json", svg: "image/svg+xml", png: "image/png", txt: "text/plain" };
const server = createServer(async (req, res) => {
  let p = decodeURIComponent(new URL(req.url, "http://x").pathname).replace(/^\/+/, "");
  if (!/\.\w+$/.test(p)) p += ".html";
  try {
    let body = await readFile(new URL(p, pub));
    const m = p.match(/^docs\/(?:(zh|ja)\/)?contributors\.html$/);
    if (m) {
      body = String(body).replace(/<div id="wall">[\s\S]*?<\/div>/, () => `<div id="wall">${tables[m[1] || "en"]}</div>`);
    }
    res.writeHead(200, { "content-type": types[p.split(".").pop()] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const base = `http://127.0.0.1:${server.address().port}`;

const langs = { en: ["/docs/", "1 person", "Find a name"], zh: ["/docs/zh/", "1 人", "查找名字"], ja: ["/docs/ja/", "1 人", "名前で探す"] };
const ROW = "lijiho96940561";
let fails = 0, runs = 0;

const visible = (page, sel) => page.$eval(sel, (el) => {
  const r = el.getBoundingClientRect();
  const pad = parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
  return { top: r.top, bottom: r.bottom, pad, h: innerHeight };
});

for (const [ename, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  const browser = await engine.launch();
  for (const width of [1280, 390]) {
    for (const [lang, [dir, one, ph]] of Object.entries(langs)) {
      const name = `${ename} ${width} ${lang}`;
      runs++;
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      const page = await ctx.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      try {
        // a link to the row lands on it, under the sticky bar, highlighted
        await page.goto(base + dir + "contributors#" + ROW, { waitUntil: "load" });
        await page.waitForTimeout(300);
        let r = await visible(page, "#" + ROW);
        assert.ok(r.top >= r.pad - 2 && r.top < r.h / 2, `the linked row isn't in view: ${JSON.stringify(r)}`);
        assert.equal(await page.$eval("#" + ROW, (el) => el.matches(":target")), true, "not the target");
        const bg = await page.$eval(`#${ROW} > td`, (td) => getComputedStyle(td).backgroundColor);
        const plain = await page.$eval("table.cw tbody tr:not(:target) > td", (td) => getComputedStyle(td).backgroundColor);
        assert.notEqual(bg, plain, "the linked row isn't highlighted");
        if (shots) await page.screenshot({ path: `${shots}/${ename}-${width}-${lang}-anchor.png` });

        // the field, empty: every row, no count
        const input = page.locator("#cw-q");
        assert.equal(await input.isVisible(), true, "the field is hidden");
        assert.equal(await input.getAttribute("placeholder"), ph);
        const all = await page.locator("table.cw tbody tr").count();
        assert.ok(all > 2000, `only ${all} rows`);
        assert.equal(await page.locator(".cw-n").textContent(), "");
        const fb = await input.boundingBox();
        assert.ok(fb.x >= 0 && fb.x + fb.width <= width, `field off screen: ${JSON.stringify(fb)}`);

        // typing "lijiho" leaves that row, and says how many
        await page.evaluate(() => scrollTo({ top: document.querySelector("#list").offsetTop - 84, behavior: "instant" }));
        await input.fill("lijiho");
        const shown = await page.locator("table.cw tbody tr:visible").evaluateAll((rs) => rs.map((r) => r.id));
        assert.deepEqual(shown, [ROW]);
        assert.equal(await page.locator(".cw-n").textContent(), one);
        // any case, a leading @
        await input.fill("@LIJIHO96940561");
        assert.deepEqual(await page.locator("table.cw tbody tr:visible").evaluateAll((rs) => rs.map((r) => r.id)), [ROW]);
        assert.ok((await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)) <= 0, "the page scrolls sideways");
        if (shots) await page.screenshot({ path: `${shots}/${ename}-${width}-${lang}-find.png` });
        // nobody: the table goes, the count says 0
        await input.fill("zzqqxx-nobody");
        assert.equal(await page.locator("table.cw").isVisible(), false);
        assert.match(await page.locator(".cw-n").textContent(), /^0\b/);
        // Escape gives the whole list back
        await page.keyboard.press("Escape");
        assert.equal(await input.inputValue(), "");
        assert.equal(await page.locator("table.cw tbody tr:visible").count(), all);

        // a link to a row the filter hid brings the list back and lands on it
        await input.fill("zzqqxx-nobody");
        await page.evaluate(() => { location.hash = ""; });
        await page.evaluate((id) => { location.hash = id; }, ROW);
        await page.waitForFunction((id) => !document.getElementById(id).hidden, ROW);
        r = await visible(page, "#" + ROW);
        assert.ok(r.top >= r.pad - 2 && r.top < r.h / 2, `the hidden row wasn't brought into view: ${JSON.stringify(r)}`);
        assert.equal(await input.inputValue(), "");

        // another case lands on the row's own id
        await page.goto(base + dir + "contributors#Lijiho96940561", { waitUntil: "load" });
        await page.waitForFunction((id) => location.hash === "#" + id, ROW);
        assert.equal(errors.length, 0, errors.join("\n"));
      } catch (e) {
        fails++;
        console.log(`FAIL ${name}: ${e.message}`);
      }
      await ctx.close();

      // no script: the list is whole and the field never shows
      runs++;
      const off = await browser.newContext({ viewport: { width, height: 800 }, javaScriptEnabled: false });
      const p2 = await off.newPage();
      try {
        await p2.goto(base + dir + "contributors#" + ROW, { waitUntil: "load" });
        assert.equal(await p2.locator(".cw-find").isVisible(), false, "the field shows without script");
        assert.ok((await p2.locator("table.cw tbody tr:visible").count()) > 2000, "rows missing without script");
        const r = await visible(p2, "#" + ROW);
        assert.ok(r.top >= 0 && r.top < r.h, `the linked row isn't in view without script: ${JSON.stringify(r)}`);
        if (shots) await p2.screenshot({ path: `${shots}/${ename}-${width}-${lang}-noscript.png` });
      } catch (e) {
        fails++;
        console.log(`FAIL ${name} no script: ${e.message}`);
      }
      await off.close();
    }
  }
  await browser.close();
}
server.close();
console.log(`${runs - fails}/${runs} ok`);
process.exit(fails ? 1 : 0);
