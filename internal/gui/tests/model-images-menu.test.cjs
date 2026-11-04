// Run with Node's test runner and Playwright on the module path; see README.md.
// Whether a model sees images, from its chip's right-click (archsiono on X:
// the switch was only in Names, prices & levels, "谁能找到", and a
// right-click item was asked for). "Turn on image input" for a model that
// doesn't, "Turn off image input" for one that does; the pick is staged
// as Names, prices & levels' tick stages it — that tick follows, the page
// doesn't move, nothing is saved — and sent with the Save as modelPrefs'
// images. A subscription's models have it too. In English, Chinese,
// Japanese and German, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1", responses: "", anthropic: "", marksKinds: true,
  models: [{ id: "text-1", name: "text-1", on: true }, { id: "see-2", name: "see-2", on: true, images: true }],
  key: { set: true, masked: "sk-…one" },
};
const sub = {
  ...base, id: "copilot", name: "GitHub Copilot", icon: "githubcopilot",
  chat: "https://api.githubcopilot.com", responses: "", anthropic: "",
  account: { agent: "copilot", user: "octo", accounts: [] },
  models: [{ id: "gpt-9", name: "GPT-9", on: true, images: true }],
  key: { set: false, masked: "" },
};

function serve(lang, posts) {
  const providers = { providers: [relay, sub], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/save") { posts.push(route.request().postDataJSON()); return json(providers); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { on: "Turn on image input", off: "Turn off image input", names: "Names, prices & levels", images: "Accepts images", save: "Save", staged: "text-1 accepts images once saved", unstaged: "see-2 doesn't accept images once saved" },
  zh: { on: "开启图片输入", off: "关闭图片输入", names: "名称、价格与推理档位", images: "支持图片输入", save: "保存", staged: "保存后 text-1 将支持图片输入", unstaged: "保存后 see-2 将不支持图片输入" },
  ja: { on: "画像入力をオンにする", off: "画像入力をオフにする", names: null, images: null, save: "保存", staged: "保存すると text-1 は画像入力に対応します", unstaged: "保存すると see-2 は画像入力に対応しなくなります" },
  de: { on: "Bildeingabe einschalten", off: "Bildeingabe ausschalten", names: null, images: null, save: "Speichern", staged: "text-1 nimmt nach dem Speichern Bilder an", unstaged: "see-2 nimmt nach dem Speichern keine Bilder an" },
};

// scroll the chip in and let the frame pass before the right-click, or
// Chromium's late scroll event closes the menu it opened
async function rclick(loc) {
  await loc.click({ button: "right", trial: true });
  await loc.page().evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await loc.click({ button: "right" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}: image input turned on and off from a model's right-click`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-images-menu.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const open = async (name) => {
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor .mchips .mchip").first().waitFor();
        };
        const chip = (id) => page.locator(".editor .mchips .mchip", { hasText: id });
        const rowMenu = page.locator(".pop.row-menu");
        const item = (name) => rowMenu.getByRole("menuitem", { name, exact: true });

        await open("Relay");
        const top = () => page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".editor, .editor *")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
        const picked = await page.locator(".editor .mchips .mchip.on").count();

        // one that doesn't see images: turned on
        await rclick(chip("text-1"));
        await item(w.on).waitFor();
        assert.equal(await item(w.off).count(), 0);
        const box = await rowMenu.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width + 0.5, `the menu fits the window: ${JSON.stringify(box)}`);
        let before = await top();
        await item(w.on).click();
        await rowMenu.waitFor({ state: "detached" });
        assert.equal(await page.locator("#status").textContent(), w.staged);
        assert.equal(await top(), before, "the page didn't move");
        // the menu says it now
        await rclick(chip("text-1"));
        await item(w.off).waitFor();
        await page.keyboard.press("Escape");
        await rowMenu.waitFor({ state: "detached" });

        // one that does: turned off
        await rclick(chip("see-2"));
        before = await top();
        await item(w.off).click();
        await rowMenu.waitFor({ state: "detached" });
        assert.equal(await page.locator("#status").textContent(), w.unstaged);
        assert.equal(await top(), before, "the page didn't move");
        assert.equal(await page.locator(".editor .mchips .mchip.on").count(), picked, "nothing picked or unpicked");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        assert.equal(posts.length, 0, "staged, not saved");

        // Names, prices & levels' tick follows what was staged
        if (w.names) {
          await page.getByRole("button", { name: w.names, exact: true }).click();
          const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
          assert.equal(await row("text-1").getByRole("checkbox", { name: w.images, exact: true }).isChecked(), true);
          assert.equal(await row("see-2").getByRole("checkbox", { name: w.images, exact: true }).isChecked(), false);
        }

        await page.locator(".editor").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts[0]?.modelPrefs, { "text-1": { images: true }, "see-2": { images: false } });

        // a subscription's model has it too
        await page.goto("http://magpie.test/?view=providers");
        await open("GitHub Copilot");
        await rclick(chip("GPT-9"));
        await item(w.off).waitFor();
        assert.deepEqual(errors, []);
      });
    }
  }
}
