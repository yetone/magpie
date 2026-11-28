// A contributor's card (/c/<id>: cards.js, public/card.js) and the wall's
// links to it, in real browsers: Chromium and WebKit, 1280 and 390 wide,
// each language, with script off and with reduced motion. Not a
// *.test.mjs, so CI's `node --test site/*.test.mjs` leaves it out; run it
// by hand where Playwright is on NODE_PATH, with WALL_JSON and REPLAY_JSON
// as wall.browser.mjs takes them:
//   node site/card.browser.mjs [screenshot-dir]
// Pages come from cards.js page(), headers and all, so the card is drawn
// under the CSP it ships with. Pictures come from a fake, never the
// network: a red square for GitHub, nothing for X (the card falls back to
// a letter).
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { createRequire } from "node:module";
import { deflateSync } from "node:zlib";
import assert from "node:assert/strict";
import { renderWall, cards } from "./credits.mjs";
import { page as cardPage } from "./cards.js";

const { chromium, webkit } = createRequire(import.meta.url)("playwright");
const pub = new URL("./public/", import.meta.url);
const shots = process.argv[2];
if (shots) await mkdir(shots, { recursive: true });
const data = JSON.parse(await readFile(process.env.WALL_JSON || new URL("contributors.json", pub), "utf8"));
const rp = JSON.parse(await readFile(process.env.REPLAY_JSON || new URL("contributors/replay.json", pub), "utf8"));
const times = Object.fromEntries(rp.v.map((v, i) => [v, rp.t0 + rp.t[i] * 60]));
const all = new Map(cards(data.people, times, data.plugins).map((c) => [c.id, c]));
const tables = Object.fromEntries(["en", "zh", "ja"].map((l) => [l, renderWall(data, l)]));

// a 64px red PNG, made here
function png(n, rgb) {
  const crc = (b) => { let c = ~0; for (const x of b) { c ^= x; for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1)); } return ~c >>> 0; };
  const chunk = (t, d) => { const b = Buffer.alloc(12 + d.length); b.writeUInt32BE(d.length); b.write(t, 4); d.copy(b, 8); b.writeUInt32BE(crc(b.subarray(4, 8 + d.length)), 8 + d.length); return b; };
  const ih = Buffer.alloc(13); ih.writeUInt32BE(n); ih.writeUInt32BE(n, 4); ih[8] = 8; ih[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.concat(Array(n).fill(Buffer.from(rgb)))]);
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ih), chunk("IDAT", deflateSync(Buffer.concat(Array(n).fill(row)))), chunk("IEND", Buffer.alloc(0))]);
}
const RED = png(64, [200, 40, 40]);

const types = { html: "text/html; charset=utf-8", js: "text/javascript", css: "text/css", json: "application/json", svg: "image/svg+xml", png: "image/png" };
const env = { ASSETS: { fetch: async (r) => {
  const m = new URL(r.url).pathname.match(/^\/contributors\/c\/(.+)\.json$/);
  const c = m && all.get(decodeURIComponent(m[1]));
  return c ? Response.json(c) : new Response("", { status: 404 });
} } };
const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  if (url.pathname.startsWith("/c/")) {
    const got = await cardPage(new Request("https://usemagpie.ai" + req.url, { headers: { "Accept-Language": req.headers["accept-language"] || "" } }), env);
    res.writeHead(got.status, Object.fromEntries(got.headers));
    res.end(Buffer.from(await got.arrayBuffer()));
    return;
  }
  let p = decodeURIComponent(url.pathname).replace(/^\/+/, "");
  if (!/\.\w+$/.test(p)) p += ".html";
  try {
    let body = await readFile(new URL(p, pub));
    const m = p.match(/^docs\/(?:(zh|ja)\/)?contributors\.html$/);
    if (m) body = String(body).replace(/<div id="wall">[\s\S]*?<\/div>/, () => `<div id="wall">${tables[m[1] || "en"]}</div>`);
    res.writeHead(200, { "content-type": types[p.split(".").pop()] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const base = `http://127.0.0.1:${server.address().port}`;

async function fakePictures(ctx) {
  // the last route added is asked first: the catch-all goes in first
  await ctx.route(/^https?:\/\/(?!127\.0\.0\.1)/, (r) => r.abort());
  await ctx.route(/^https:\/\/avatars\.githubusercontent\.com\//, (r) => r.fulfill({ status: 200, contentType: "image/png", headers: { "access-control-allow-origin": "*" }, body: RED }));
  await ctx.route(/^https:\/\/pbs\.twimg\.com\//, (r) => r.fulfill({ status: 404, body: "" }));
}

const GH = all.get("xiaozhu1337"), XP = all.get("tmr11235"), CJK = [...all.values()].find((c) => c.name === "小八");
assert.ok(GH && XP && CJK, "the cards this test reads aren't in the data");
const words = { en: ["Turn over", "Copied", "Card"], zh: ["翻面", "已复制", "贡献卡"], ja: ["裏返す", "コピーしました", "カード"] };
let fails = 0, runs = 0;

// what the front canvas shows at a point of the card's own units
const pixel = (page, sel, x, y) => page.$eval(sel, (cv, [x, y]) => {
  const k = cv.width / 1000;
  return Array.from(cv.getContext("2d").getImageData(Math.round(x * k), Math.round(y * k), 1, 1).data);
}, [x, y]);

for (const [ename, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  const browser = await engine.launch();
  for (const width of [1280, 390]) {
    for (const [lang, [flip, copied, link]] of Object.entries(words)) {
      for (const who of [GH, XP, CJK]) {
        if (who !== GH && (width !== 1280 || lang !== "en") && !(who === CJK && lang === "zh")) continue;
        const name = `${ename} ${width} ${lang} ${who.id}`;
        runs++;
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, locale: lang, acceptDownloads: true, deviceScaleFactor: 2 });
        await fakePictures(ctx);
        if (ename === "chromium") await ctx.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base });
        const page = await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(String(e)));
        page.on("console", (m) => { if (m.type() === "error" && !/Failed to load resource/.test(m.text())) errors.push(m.text()); });
        try {
          const res = await page.goto(`${base}/c/${encodeURIComponent(who.id)}?lang=${lang}`, { waitUntil: "load" });
          assert.equal(res.status(), 200);
          await page.waitForSelector(".cc-card[data-ready]", { timeout: 5000 });
          await page.waitForFunction(() => !document.querySelector(".cc-stage").getAnimations().length);
          assert.equal(await page.getAttribute(".cc-card", "aria-label"), flip);
          // drawn at the screen's pixels, the card's ratio
          const dims = await page.$eval(".cc-front canvas", (cv) => ({ w: cv.width, h: cv.height, cw: cv.clientWidth, ch: cv.clientHeight }));
          assert.ok(Math.abs(dims.w - dims.cw * 2) <= 1, `canvas not at the screen's pixels: ${JSON.stringify(dims)}`);
          assert.ok(Math.abs(dims.cw / dims.ch - 1000 / 631) < 0.01, `not a card's ratio: ${JSON.stringify(dims)}`);
          // the corner is cut round, the body black, the bird and name light
          assert.equal((await pixel(page, ".cc-front canvas", 2, 2))[3], 0, "the corner isn't round");
          const bodyPx = await pixel(page, ".cc-front canvas", 500, 20);
          assert.ok(bodyPx[0] < 40 && bodyPx[3] === 255, `the body isn't black metal: ${bodyPx}`);
          // the picture: GitHub's in grey (the fake is red), X's a letter
          const face = await pixel(page, ".cc-front canvas", 64 + 59, 178 + 30);
          if (who.github) assert.ok(face[0] === face[1] && face[1] === face[2] && face[0] > 60, `the picture isn't drawn in grey: ${face}`);
          else assert.ok(face[0] < 40, `a missing picture isn't left dark: ${face}`);
          // everything credited is listed as text
          assert.equal(await page.locator(".cc-list li").count(), who.lines.length);
          if (shots) await page.screenshot({ path: `${shots}/${ename}-${width}-${lang}-${who.id}-front.png` });
          // which side the screen shows: the front's wordmark is light where
          // the back's stripe is dark, either way round
          let sides = 0;
          const side = async () => {
            const shot = (await page.locator(".cc-card").screenshot(shots ? { path: `${shots}/${ename}-${width}-${lang}-${who.id}-side${++sides}.png` } : {})).toString("base64");
            return page.evaluate(async (b64) => {
              const im = new Image();
              im.src = "data:image/png;base64," + b64;
              await im.decode();
              const cv = document.createElement("canvas");
              cv.width = im.width; cv.height = im.height;
              const x = cv.getContext("2d");
              x.drawImage(im, 0, 0);
              let l = 0, r = 0;
              for (const [x0, x1] of [[0.13, 0.3], [0.7, 0.87]]) {
                const d = x.getImageData(Math.round(im.width * x0), Math.round(im.height * 0.11), Math.round(im.width * (x1 - x0)), Math.round(im.height * 0.06)).data;
                let n = 0;
                for (let i = 0; i < d.length; i += 4) if (d[i] > 150) n++;
                if (x0 < 0.5) l = n; else r = n;
              }
              return l > 20 ? "front" : r > 20 ? "mirrored front" : "back";
            }, shot);
          };
          let seen = await side();
          assert.equal(seen, "front", `at first the screen shows the ${seen}`);
          // turns over, and back
          await page.click(".cc-card");
          assert.equal(await page.getAttribute(".cc-card", "aria-pressed"), "true");
          await page.waitForTimeout(650);
          const turned = await page.$eval(".cc-flip", (el) => new DOMMatrix(getComputedStyle(el).transform).m11);
          assert.ok(turned < -0.99, `not turned over: ${turned}`);
          if (shots) await page.screenshot({ path: `${shots}/${ename}-${width}-${lang}-${who.id}-back.png` });
          seen = await side();
          assert.equal(seen, "back", `turned over, the screen shows the ${seen}`);
          await page.click(".cc-card");
          assert.equal(await page.getAttribute(".cc-card", "aria-pressed"), "false");
          await page.waitForTimeout(650);
          seen = await side();
          assert.equal(seen, "front", `turned back, the screen shows the ${seen}`);
          // saves both sides as one picture
          const [dl] = await Promise.all([page.waitForEvent("download"), page.click(".cc-save")]);
          assert.equal(dl.suggestedFilename(), `magpie-card-${who.id}.png`);
          const file = await readFile(await dl.path());
          assert.equal(file.toString("latin1", 1, 4), "PNG");
          assert.deepEqual([file.readUInt32BE(16), file.readUInt32BE(20)], [2000, (631 * 2 + 48) * 2]);
          if (shots && width === 1280) await (await import("node:fs/promises")).writeFile(`${shots}/${ename}-${lang}-${who.id}-saved.png`, file);
          // copies its link
          if (ename === "chromium") {
            await page.click(".cc-copy");
            await page.waitForFunction((t) => document.querySelector(".cc-copy").textContent === t, copied);
            assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `https://usemagpie.ai/c/${encodeURIComponent(who.id)}`);
          }
          // tilts under a mouse (not on a phone's width, where none is)
          if (width === 1280) {
            const box = await page.locator(".cc-card").boundingBox();
            await page.mouse.move(box.x + box.width * 0.9, box.y + box.height * 0.2);
            await page.waitForTimeout(350);
            const ry = await page.$eval(".cc-card", (el) => el.style.getPropertyValue("--ry"));
            assert.ok(parseFloat(ry) > 5, `doesn't tilt: ${ry}`);
            if (shots) await page.screenshot({ path: `${shots}/${ename}-${lang}-${who.id}-tilt.png` });
            await page.mouse.move(2, 2);
          }
          assert.deepEqual(errors, []);
        } catch (e) {
          fails++;
          console.log(`FAIL ${name}: ${e.message.split("\n")[0]}${errors.length ? ` (page: ${errors.join(" | ")})` : ""}`);
          await ctx.close();
          continue;
        }
        await ctx.close();
        console.log(`ok ${name}`);
      }
    }

    // the wall: a link to the card on the row landed on, a row pointed at,
    // and the rows a search narrows to
    for (const [lang, [, , link]] of Object.entries(words)) {
      const name = `${ename} ${width} ${lang} wall`;
      runs++;
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await fakePictures(ctx);
      const page = await ctx.newPage();
      try {
        const dir = lang === "en" ? "/docs/" : `/docs/${lang}/`;
        await page.goto(`${base}${dir}contributors#${XP.id}`, { waitUntil: "load" });
        const a = page.locator(`#${XP.id} .cw-card`);
        assert.equal(await a.getAttribute("href"), `/c/${XP.id}`);
        assert.equal(await a.textContent(), link);
        const before = await page.$eval(`#${XP.id}`, (el) => el.offsetHeight);
        // pinned by id: the row gets data-card once pointed at
        const oid = await page.locator("table.cw tbody tr:not([data-card])").nth(3).getAttribute("id");
        const other = page.locator(`[id="${oid}"]`);
        const h0 = await other.evaluate((el) => el.offsetHeight);
        await other.hover();
        assert.equal(await page.locator(`[id="${oid}"] .cw-card`).count(), 1, "no link on the row pointed at");
        const h1 = await other.evaluate((el) => [el.offsetHeight, el.cells[0].offsetWidth, el.querySelector(".cw-who > div").innerHTML]);
        assert.equal(h1[0], h0, `the row pointed at changed height: ${h0} → ${JSON.stringify(h1)}`);
        assert.ok(before > 0);
        await page.fill("#cw-q", GH.name);
        const shown = await page.locator("table.cw tbody tr:not([hidden])").count();
        assert.ok(shown >= 1 && shown <= 50);
        assert.equal(await page.locator("table.cw tbody tr:not([hidden]) .cw-card").count(), shown, "a row the search narrowed to has no link");
        if (shots) await page.screenshot({ path: `${shots}/${ename}-${width}-${lang}-wall.png` });
        await page.click(`[id="${GH.id}"] .cw-card`);
        await page.waitForSelector(".cc-card[data-ready]");
        assert.match(page.url(), new RegExp(`/c/${GH.id}$`));
      } catch (e) {
        fails++;
        console.log(`FAIL ${name}: ${e.message.split("\n")[0]}`);
        await ctx.close();
        continue;
      }
      await ctx.close();
      console.log(`ok ${name}`);
    }
  }

  // no such card; script off; reduced motion
  {
    const name = `${ename} gone, script off, still`;
    runs++;
    let ok = true;
    try {
      let ctx = await browser.newContext();
      let page = await ctx.newPage();
      const res = await page.goto(`${base}/c/nobody-at-all?lang=zh`);
      assert.equal(res.status(), 404);
      assert.equal(await page.getAttribute(".cc-gone a", "href"), "/docs/zh/contributors");
      await ctx.close();
      ctx = await browser.newContext({ javaScriptEnabled: false });
      page = await ctx.newPage();
      await page.goto(`${base}/c/${XP.id}`);
      assert.equal(await page.locator(".cc-stage").isVisible(), false, "a blank card shows with script off");
      assert.equal(await page.locator(".cc-list li").count(), XP.lines.length);
      await ctx.close();
      ctx = await browser.newContext({ reducedMotion: "reduce" });
      await fakePictures(ctx);
      page = await ctx.newPage();
      await page.goto(`${base}/c/${GH.id}`);
      await page.waitForSelector(".cc-card[data-ready]");
      await page.click(".cc-card");
      await page.waitForTimeout(300);
      const o = await page.$eval(".cc-back", (el) => [getComputedStyle(el).opacity, new DOMMatrix(getComputedStyle(el.parentNode).transform).m11]);
      assert.deepEqual(o, ["1", 1], "reduced motion turns the card rather than fading");
      await ctx.close();
    } catch (e) {
      fails++;
      ok = false;
      console.log(`FAIL ${name}: ${e.message.split("\n")[0]}`);
    }
    if (ok) console.log(`ok ${name}`);
  }
  await browser.close();
}
server.close();
console.log(`${runs - fails}/${runs} ok`);
process.exit(fails ? 1 : 0);
