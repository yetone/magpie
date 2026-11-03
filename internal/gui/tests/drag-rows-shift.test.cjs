// Run with Node's test runner and Playwright on the module path; see README.md.
// A row dragged while what is above its list grows or shrinks (yuancjun on
// Discord): the Routing page's live stage changes height as requests come
// in, and dragRows had measured the rows once, at the press, so the slot it
// committed was the one under the pointer before the shift, two or more off
// from where the row was seen to be let go, and the group's order changed to
// one nobody chose. Here a group's members are dragged in its editor and a
// block above the groups is put in, or taken out, mid-drag: the row stays
// under the pointer, and the order saved is the slot it was let go in.
// Chromium and WebKit, 1100px and 560px, en/zh/zh-TW/ja/de.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = Array.from({ length: 6 }, (_, i) => ({ id: `p/m${i}`, name: `m${i}`, providerName: "P", icon: "generic" }));
const members = models.map((m) => m.id);
const groups = () => ({
  models, pools: [],
  groups: [{ id: "g", name: "G", members, off: [], routing: "order", ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }],
});

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// grow: a 90px block put in above the groups; shrink: one there from the
// start taken out. A row in the middle, so the pointer is still over the
// list once it has moved
const cases = [
  { name: "grows", pre: false, drag: "m3", past: "m4", below: true, want: ["m0", "m1", "m2", "m4", "m3", "m5"] },
  { name: "shrinks", pre: true, drag: "m2", past: "m1", below: false, want: ["m0", "m2", "m1", "m3", "m4", "m5"] },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a member let go after a shift above its list lands where it was let go`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [1100, 560]) {
        for (const c of cases) {
          const at = `${width}px ${c.name}`;
          const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], posts = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, posts));
          await page.goto("http://magpie.test/?view=routing");
          const card = page.locator(".rt-group[data-id=g]");
          await card.waitFor();
          const shift = (on) => page.evaluate((on) => {
            let b = document.querySelector("#shift-block");
            if (on && !b) {
              b = document.createElement("div");
              b.id = "shift-block";
              b.style.height = "90px";
              document.querySelector(".rt-gsec").before(b);
            } else if (!on) b?.remove();
          }, on);
          if (c.pre) await shift(true);
          await card.click();
          const ed = page.locator(".rt-gedit");
          await ed.locator(".fbrow .rt-mhandle").nth(5).waitFor();
          const settle = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 50)))));
          await settle();
          const row = (n) => ed.locator(".fbl").first().locator(".fbrow", { has: page.locator(".n > span:first-child", { hasText: new RegExp(`^${n}$`) }) });
          // the reader wheels the members to the middle of the window, away
          // from the edges a drag scrolls at
          await page.mouse.move(width / 2, 500);
          for (let i = 0; i < 20; i++) {
            const top = (await row("m0").boundingBox()).y;
            if (top < 220) break;
            await page.mouse.wheel(0, Math.min(300, top - 150));
            await page.waitForTimeout(150);
          }
          const top0 = (await row("m0").boundingBox()).y, foot = (await row("m5").boundingBox());
          assert(top0 > 100 && foot.y + foot.height < 900, `${at}: the members in the middle of the window: ${top0}..${foot.y + foot.height}`);
          const order = () => ed.locator(".fbl").first().locator(".fbrow .n > span:first-child").allTextContents();
          assert.deepEqual(await order(), models.map((m) => m.name), `${at}: the order before`);

          const hb = await row(c.drag).locator(".rt-mhandle").boundingBox(), rb = await row(c.drag).boundingBox();
          const x = hb.x + hb.width / 2, y0 = hb.y + hb.height / 2, grab = y0 - rb.y;
          await page.mouse.move(x, y0);
          await page.mouse.down();
          const dir = c.below ? 1 : -1;
          for (let i = 1; i <= 3; i++) await page.mouse.move(x, y0 + dir * 4 * i);
          // what's above the list changes, the pointer still
          await shift(!c.pre);
          await settle();
          const held = await row(c.drag).boundingBox();
          assert(Math.abs(held.y - (y0 + dir * 12 - grab)) <= 2, `${at}: the row stays under the pointer as the list moves: ${held.y} vs ${y0 + dir * 12 - grab}`);
          // then let go just past the middle of the row named, where it is now
          const p = await row(c.past).boundingBox();
          const y1 = p.y + p.height / 2 + dir * 6 - rb.height / 2 + grab;
          const from = y0 + dir * 12;
          for (let i = 1; i <= 10; i++) await page.mouse.move(x, from + ((y1 - from) * i) / 10);
          await settle();
          await page.mouse.up();
          assert.deepEqual(await order(), c.want, `${at}: the order is the slot it was let go in`);
          assert.deepEqual(errors, [], `${at}: no page error`);
          await page.close();
        }
      }
    });
  }
}
