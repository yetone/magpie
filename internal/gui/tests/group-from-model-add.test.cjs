// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group made from a model is added (悠悠哥 on Discord: in a small
// window, the new group "Hy4 preview" of its one model, Hy4 preview from
// WorkBuddy, did nothing on Add). A group opened from a model — by the
// picker's or the provider editor's "Make a routing group of it", or the
// tray panel's ?newgroup= — had no fast members in its draft, so Add threw
// and posted nothing; and a hidden classifier left an empty cell in the
// editor's grid, so Levels' label sat at the right and its choices under
// the labels. Now each label is beside its field, Add posts the group, and
// what goes wrong is said where it is seen:
// a save the server refuses shows its error in the window, in sight, the
// editor stays, and Add takes another press (it stayed busy, deaf to
// clicks). In Chromium and WebKit, English and Chinese, in a
// small window and a large one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const MODEL = "workbuddy/hy4-preview";

function serve(lang, posts, refuse) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const groups = { groups: [], pools: [], deciders: [], models: [{ id: MODEL, name: "Hy4 preview", provider: "workbuddy", providerName: "WorkBuddy", icon: "generic" }] };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: JSON.parse(req.postData() || "{}") });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups/save" && refuse) return r.fulfill({ status: 400, contentType: "text/plain", body: "group hy4-preview: no such model" });
    if (url.pathname === "/api/groups" || url.pathname.startsWith("/api/groups/")) return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", groups: [] } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: { add: "Add", addModel: "Add another model" }, zh: { add: "添加", addModel: "再加一个模型" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const [width, height] of [[600, 420], [1000, 700]]) {
      test(`${engine} ${lang} ${width}x${height}: a routing group made from a model is added`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (posts, refuse) => {
          const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(20000); // the page's first load, slow on a busy machine
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, posts, refuse));
          await page.goto(`http://magpie.test/?view=routing&newgroup=${encodeURIComponent(MODEL)}`);
          const ed = page.locator("#view-routing:not([hidden]) .rt-gedit");
          await ed.waitFor();
          await ed.locator("input").first().fill("Hy4 preview");
          // the reader wheels down until Add itself is under the pointer's
          // reach, and it has settled, as the page moves for no one else
          const add = ed.getByRole("button", { name: words[lang].add, exact: true });
          const seen = () => add.evaluate((btn) => {
            const r = btn.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
            return document.elementFromPoint(x, y)?.closest("button") === btn ? { x, y } : null;
          });
          await page.mouse.move(width / 2, height / 2);
          let at = null;
          for (let i = 0; i < 60; i++) {
            const now = await seen();
            if (now && at && Math.abs(now.y - at.y) < 0.5) break;
            at = now;
            if (!now) await page.mouse.wheel(0, 120);
            await page.waitForTimeout(80);
          }
          assert.ok(at, "the wheel reaches Add, and nothing covers it");
          const clickAdd = () => page.mouse.click(at.x, at.y);
          return { page, ed, errors, clickAdd };
        };

        // Add posts the group of its one model
        let posts = [];
        let { page, ed, errors, clickAdd } = await open(posts, false);
        // every label beside its own field: a hidden classifier left its
        // cell behind, and Levels' label sat at the right, its choices below
        const rows = await ed.evaluate((e) => [...e.children].filter((c) => c.matches("label") && !c.hidden && c.offsetParent).map((l) => {
          const f = l.nextElementSibling, a = l.getBoundingClientRect(), b = f.getBoundingClientRect();
          return { label: l.textContent, beside: a.right <= b.left + 1 && Math.abs(a.top - b.top) < 12 };
        }));
        assert.ok(rows.length >= 6, JSON.stringify(rows));
        assert.deepEqual(rows.filter((r) => !r.beside), [], "each label beside its field");
        await clickAdd();
        for (let i = 0; i < 40 && !posts.some((p) => p.path === "/api/groups/save"); i++) await page.waitForTimeout(50);
        const saved = posts.find((p) => p.path === "/api/groups/save")?.body;
        assert.deepEqual(saved?.members, [MODEL], "the group is posted with its model");
        assert.deepEqual(saved.fast, [], "no member sent fast");
        assert.equal(saved.name, "Hy4 preview");
        assert.deepEqual(errors, [], "nothing throws");
        await page.context().close();

        // refused, the error is said in sight and the editor stays
        posts = [];
        ({ page, ed, errors, clickAdd } = await open(posts, true));
        await clickAdd();
        const st = page.locator("#status.err");
        await st.waitFor();
        assert.match(await st.textContent(), /no such model/);
        const box = await st.boundingBox();
        assert.ok(box && box.width > 0 && box.y >= 0 && box.y + box.height <= height + 1, `the error is in sight: ${JSON.stringify(box)}`);
        assert.equal(await ed.isVisible(), true, "the editor stays open");
        // and Add takes a second press, where it stayed busy and took none
        const saves = () => posts.filter((p) => p.path === "/api/groups/save").length;
        assert.equal(saves(), 1);
        for (let i = 0; i < 40 && await ed.locator(".bar .primary.busy").count(); i++) await page.waitForTimeout(50);
        await clickAdd();
        for (let i = 0; i < 40 && saves() < 2; i++) await page.waitForTimeout(50);
        assert.equal(saves(), 2, "a second Add is posted");
        assert.deepEqual(errors, [], "nothing throws");
      });
    }
  }
}
