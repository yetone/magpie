// Run with Node's test runner and Playwright on the module path; see README.md.
// DeepSeek Harness's own DeepSeek models, beside magpie's in its model
// picker, can be left out (MizuIro-H, #1642). They aren't magpie's catalog,
// so its model list has a switch of their own at the top: off, the picker
// offers magpie's alone, at once on closing the list and in the state read
// after; on again, they are back. The one the agent is set to stays (the
// server's agent.Picks). An agent with no models of its own has no switch,
// and the order view hides it. In English, Chinese, German and Japanese, at a
// narrow and a wide window; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// dsh's own as internal/agent's dshModels lists them
const own = [["deepseek-flash", "DeepSeek-V4.1-Flash"], ["deepseek-v4-pro", "DeepSeek-V4-Pro"]].map(([value, label]) => ({
  value, label, icon: "deepseek-color", group: "DeepSeek Harness", direct: "DeepSeek",
}));
const magpie = ["pro", "flash"].map((m) => ({ value: "magpie/deepseek/" + m, label: "deepseek-" + m, ref: "deepseek/" + m, note: "DeepSeek · via magpie", group: "DeepSeek", icon: "deepseek-color" }));
const list = magpie.map((o, i) => ({ id: o.ref, name: o.label, group: "DeepSeek", icon: "deepseek-color", inUse: i === 0 }));

function fixture(lang) {
  let ownHidden = false, late = 0;
  const posts = [];
  const count = { shown: list.length, listed: list.length };
  const options = () => [...(ownHidden ? [] : own), ...magpie];
  const state = () => ({
    agents: [
      { id: "dsh", name: "DeepSeek Harness", icon: "deepseek-color", path: "/fixture/dsh", wired: true, models: count,
        fields: [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: options() }] },
      { id: "codex", name: "Codex", icon: "codex-color", path: "/fixture/codex", wired: true, models: count,
        fields: [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: magpie }] },
    ],
    profiles: [], settings: { lang, theme: "light" },
  });
  async function serve(route) {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      // the state read after a switch comes late, so what the picker
      // offers before it is the page's own doing
      if (Date.now() < late) await new Promise((r) => setTimeout(r, late - Date.now()));
      return json(state());
    }
    const m = url.pathname.match(/^\/api\/agent-models\/(\w+)$/);
    if (m) {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push({ id: m[1], ...body });
        if (body.ownHidden !== undefined) {
          if (m[1] !== "dsh") return route.fulfill({ status: 500, json: { error: "not dsh's" } });
          ownHidden = body.ownHidden;
          late = Date.now() + 1500;
          return json({ ownHidden });
        }
        return json({ models: list, count });
      }
      return json({ models: list, ordered: false, only: false, own: m[1] === "dsh", ownHidden: m[1] === "dsh" && ownHidden });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts, ownHidden: () => ownHidden };
}

const W = {
  en: { show: "Show DeepSeek Harness's own models", order: "Order", all: "All" },
  zh: { show: "显示 DeepSeek Harness 自带的模型", order: "排序", all: "全部" },
  de: { show: "Eigene Modelle von DeepSeek Harness zeigen", order: "Reihenfolge", all: "Alle" },
  ja: { show: "DeepSeek Harness 自身のモデルを表示", order: "並び順", all: "すべて" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [420, 1100]) {
      test(`${engine} ${lang} ${width}: an agent's own models left out of its picker`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-own-models.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(row("dsh")).waitFor();
        const pop = page.locator(".am-pop:not(.leaving)");
        const open = async (id) => {
          const r = page.locator(row(id));
          const entry = r.locator(".ag-exp .ag-chips .ag-quiet");
          if (!(await entry.isVisible())) await r.locator(".ag-link").click();
          await entry.waitFor();
          await page.waitForFunction(() => document.getAnimations().length === 0);
          await entry.click();
          await pop.waitFor();
        };
        const close = async () => {
          await page.keyboard.press("Escape");
          await pop.waitFor({ state: "detached" });
        };
        // the model picker's rows, as its list draws them
        const picked = async () => {
          await page.locator(`${row("dsh")} .field[data-key="model"]`).click();
          await page.locator("#pop:not([hidden]) #list li").first().waitFor();
          const texts = await page.locator("#pop:not([hidden]) #list li").allTextContents();
          await page.keyboard.press("Escape");
          await page.locator("#pop").waitFor({ state: "hidden" });
          return texts.join("\n");
        };
        const settle = () => page.waitForTimeout(250);

        // before: dsh's own beside magpie's
        assert.match(await picked(), /DeepSeek-V4-Pro/);

        await open("dsh");
        const box = pop.locator(".am-own");
        const sw = box.locator(".lib-switch");
        assert.equal(await box.innerText(), w.show);
        assert.equal(await sw.getAttribute("role"), "switch");
        assert.equal(await sw.getAttribute("aria-checked"), "true");
        // whole on the screen, inside the list's box
        const pb = await pop.boundingBox(), bb = await box.boundingBox();
        assert(bb.x >= pb.x && bb.x + bb.width <= pb.x + pb.width + 0.5, `${JSON.stringify(bb)} in ${JSON.stringify(pb)}`);
        assert(pb.x >= 0 && pb.x + pb.width <= width, JSON.stringify(pb));
        assert.equal(await box.evaluate((e) => e.querySelector("span").scrollWidth <= e.querySelector("span").clientWidth), true, "its words cut short");

        // the order view is magpie's models alone: no switch there
        await pop.locator(".am-seg button", { hasText: w.order }).click();
        assert(await box.isHidden());
        await pop.locator(".am-seg button", { hasText: w.all }).click();
        assert(await box.isVisible());

        // off, by its words: sent, and gone from the picker at once
        await box.locator("span").click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { id: "dsh", ownHidden: true });
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        await close();
        let rows = await picked();
        assert.doesNotMatch(rows, /DeepSeek-V4-Pro|DeepSeek-V4\.1-Flash/, "dsh's own still offered");
        assert.match(rows, /deepseek-flash/, "magpie's are offered");
        // and so in the state read again
        await page.waitForTimeout(1800);
        rows = await picked();
        assert.doesNotMatch(rows, /DeepSeek-V4-Pro/);

        // opened again, the switch says so; on again, they are back
        await open("dsh");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        await sw.click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { id: "dsh", ownHidden: false });
        await close();
        // what was put back comes with the state
        await page.waitForTimeout(1800);
        assert.match(await picked(), /DeepSeek-V4-Pro/);

        // an agent with no models of its own has no switch
        await open("codex");
        assert.equal(await pop.locator(".am-own").count(), 0);
        await close();

        assert.equal(fx.posts.filter((p) => p.ownHidden !== undefined && p.id !== "dsh").length, 0);
        assert.equal(await page.locator(".status.err, #status.err").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
