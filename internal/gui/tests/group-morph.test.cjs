// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's row opens into its editor and folds back into it as a
// cell does on iOS (the owner, 10-23): the slot goes from the height it had
// to its new one rather than jumping, the Save shows a spinner while the
// gateway saves, and the row saved comes back from the editor's height with
// a check by its name. With reduced motion asked for, nothing moves. In
// every language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = Array.from({ length: 6 }, (_, i) => ({ id: `p/m${i}`, name: `M${i}`, providerName: "P", icon: "generic" }));
const mk = (id, name, ms) => ({
  id, name, routing: "", ready: true, members: ms.map((m) => m.id),
  memberInfo: ms.map((m) => ({ id: m.id, ready: true, name: m.name, provider: "p", model: m.id, icon: "generic" })),
});
const groups = [mk("a", "Alpha", models.slice(0, 3)), mk("b", "Beta", models.slice(2, 5)), mk("c", "Gamma", models.slice(1, 3))];

function serve(lang, saved) {
  const data = () => ({ models, pools: [], deciders: [], found: false, groups });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (d) => r.fulfill({ json: d });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(data());
    // the save is held until the test lets it go, so the spinner is seen
    if (url.pathname === "/api/groups/save") return saved.then(() => json(data()));
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the height animation running on group b's slot, if any: its first and last
// heights, and the slot's height now
const slotMotion = (page) => page.evaluate(() => {
  const n = document.querySelector('#view-groups [data-gslot="b"]');
  const a = n?.getAnimations().find((x) => x.effect.getKeyframes().some((k) => k.height));
  const ks = a?.effect.getKeyframes();
  return {
    editor: !!n?.classList.contains("rt-gedit"),
    from: ks ? parseFloat(ks[0].height) : null,
    to: ks ? parseFloat(ks.at(-1).height) : null,
    now: n ? n.getBoundingClientRect().height : 0,
    running: a?.playState === "running",
  };
});

// magpie moves the page only for the reader, so what is out of the window
// is wheeled to as a reader would, then clicked
async function reach(page, loc) {
  const vb = await page.locator("#view-groups").boundingBox();
  await page.mouse.move(vb.x + vb.width / 2, vb.y + vb.height / 2);
  for (let i = 0; i < 150; i++) {
    const b = await loc.boundingBox();
    if (b && b.y >= vb.y && b.y + b.height <= vb.y + vb.height) break;
    await page.mouse.wheel(0, b && b.y < vb.y ? -80 : 80);
    await page.waitForTimeout(20);
  }
  await page.waitForTimeout(150);
  await loc.click();
}

const inSight = (page) => page.evaluate(() => {
  const r = document.querySelector('.rt-group[data-id="b"]').getBoundingClientRect();
  const v = document.querySelector("#view-groups").getBoundingClientRect();
  return r.top >= v.top - 1 && r.bottom <= v.bottom + 1;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    test(`${engine} ${lang} ${width}px: a group opens, folds and saves in motion`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let release;
      await page.route("**/*", serve(lang, new Promise((r) => { release = r; })));
      await page.goto("http://magpie.test/?view=groups");
      const row = page.locator(".rt-group[data-id=b]");
      await row.waitFor();
      const rowH = (await row.boundingBox()).height;

      // opening: the editor grows from the row's height to its own
      await reach(page, row);
      await page.locator(".rt-gedit").waitFor();
      const open = await slotMotion(page);
      assert.ok(open.editor, "b's slot is its editor");
      assert.ok(Math.abs(open.from - rowH) < 1.5, `the editor grows from the row's ${rowH}px, not ${open.from}`);
      assert.ok(open.to > rowH + 40, `to the editor's own height (${open.to})`);
      // on its way: a spring passes its height a little before it settles,
      // so it is the animation that is asked, not the height
      assert.ok(open.running, "and is on its way");
      await page.waitForFunction(() => !document.querySelector(".rt-gedit").getAnimations().length);
      const edH = (await page.locator(".rt-gedit").boundingBox()).height;

      // folding: the row comes back from the editor's height
      const cancel = await page.evaluate(() => t("Cancel"));
      await reach(page, page.locator(".rt-gedit button", { hasText: new RegExp(`^${cancel}$`) }).last());
      const fold = await slotMotion(page);
      assert.ok(!fold.editor, "b's slot is its row again");
      assert.ok(Math.abs(fold.from - edH) < 1.5, `the row folds from the editor's ${edH}px, not ${fold.from}`);
      assert.ok(Math.abs(fold.to - rowH) < 1.5, `to its own ${rowH}px (${fold.to})`);
      await page.waitForFunction(() => !document.querySelector('.rt-group[data-id="b"]').getAnimations().some((a) => a.effect.getKeyframes().some((k) => k.height)));
      // folded from its foot, the row is still in the window, not above it
      assert.ok(await inSight(page), "the folded row is in sight");

      // saving: a spinner while the gateway saves, then the editor folds into
      // the row, which shows a check by its name
      await reach(page, row);
      await page.locator(".rt-gedit").waitFor();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit").getAnimations().length);
      const saveText = await page.evaluate(() => t("Save"));
      const save = page.locator(".rt-gedit button", { hasText: new RegExp(`^${saveText}$`) });
      await reach(page, save);
      const spin = await save.evaluate((b) => b.classList.contains("busy") && getComputedStyle(b, "::before").animationName);
      assert.equal(spin, "spin", "Save spins while the gateway saves");
      const savedFrom = (await page.locator(".rt-gedit").boundingBox()).height;
      release();
      await page.locator('.rt-group[data-id="b"] .rt-gsaved').waitFor();
      const saved = await slotMotion(page);
      assert.ok(!saved.editor);
      assert.ok(Math.abs(saved.from - savedFrom) < 1.5, `the saved row folds from the editor's ${savedFrom}px, not ${saved.from}`);
      await page.waitForFunction(() => !document.querySelector('.rt-group[data-id="b"]').getAnimations().some((a) => a.effect.getKeyframes().some((k) => k.height)));
      assert.ok(await inSight(page), "the saved row is in sight");
      // the check goes again
      await page.locator(".rt-gsaved").waitFor({ state: "detached", timeout: 5000 });
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: with reduced motion asked for, a group opens and folds at once`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("en", Promise.resolve()));
    await page.goto("http://magpie.test/?view=groups");
    await page.locator(".rt-group[data-id=b]").click();
    await page.locator(".rt-gedit").waitFor();
    assert.equal((await slotMotion(page)).from, null, "no height animation opening");
    await page.locator(".rt-gedit button", { hasText: /^Cancel$/ }).click();
    assert.equal((await slotMotion(page)).from, null, "none folding");
  });
}
