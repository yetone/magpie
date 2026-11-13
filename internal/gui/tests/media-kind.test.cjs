// Run with Node's test runner and Playwright on the module path; see README.md.
// An image model's chip says what it makes from its right-click, as a chat
// model's does (iceloon on X: a Grok channel's video models sat under Image
// models, and nothing there could mark them Video). On a provider set up
// with a base URL (marksKinds), an Image models chip's right-click has
// "Makes: Auto…": the pick opens the app's menu, is staged — the chip
// tagged, its tick unchanged, nothing saved — and goes with the Save as
// modelPrefs' kind. A Video models chip the user marked shows its tag and
// its pick. A subscription's image models have no such menu. In English,
// Chinese, Japanese and German, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1", responses: "", anthropic: "", marksKinds: true,
  models: [{ id: "grok-4.3", name: "grok-4.3", on: true }],
  drawIds: ["grok-imagine-image", "clip-x"], filmIds: ["film-z"], mediaKinds: { "film-z": "video" },
  key: { set: true, masked: "sk-…one" },
};
const sub = {
  ...base, id: "codex", name: "Codex Sub", icon: "openai",
  chat: "https://chatgpt.com/backend-api/codex", responses: "", anthropic: "",
  account: { agent: "codex", user: "octo", accounts: [] },
  models: [{ id: "gpt-9", name: "GPT-9", on: true }], drawIds: ["gpt-image-9"],
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
  en: { auto: "Makes: Auto…", video: "Video model", videoAct: "Makes: Video model…", tagVideo: "video", save: "Save", staged: "clip-x makes videos once saved" },
  zh: { auto: "类型：自动…", video: "视频模型", videoAct: "类型：视频模型…", tagVideo: "视频", save: "保存", staged: "保存后 clip-x 将作为视频模型" },
  ja: { auto: "種類：自動…", video: "動画モデル", videoAct: "種類：動画モデル…", tagVideo: "動画", save: "保存", staged: "保存すると clip-x は動画モデルになります" },
  de: { auto: "Erzeugt: Automatisch…", video: "Videomodell", videoAct: "Erzeugt: Videomodell…", tagVideo: "Video", save: "Speichern", staged: "clip-x erzeugt nach dem Speichern Videos" },
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
      test(`${engine} ${lang} ${width}: an image model's chip marked a video model, staged and saved`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-media-kind.png`) });
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
          await page.locator(".editor .mchips.drawers .mchip").first().waitFor();
        };
        const drawer = (id) => page.locator(".editor .mchips.drawers .mchip", { hasText: id });
        const film = (id) => page.locator(".editor .mchips.videomakers .mchip", { hasText: id });
        const rowMenu = page.locator(".pop.row-menu");
        const kindMenu = page.locator(".pop.proto-menu.model-kind-menu");

        await open("Relay");
        // a video model the user marked: its tag, and its pick in the menu
        assert.equal(await film("film-z").locator(".mkind-tag").textContent(), w.tagVideo);
        await rclick(film("film-z"));
        await rowMenu.getByRole("menuitem", { name: w.videoAct }).waitFor();
        await page.keyboard.press("Escape");
        await rowMenu.waitFor({ state: "detached" });

        const clip = drawer("clip-x");
        assert.equal(await clip.locator(".mkind-tag").count(), 0);
        const pressed = await clip.getAttribute("aria-pressed");
        await rclick(clip);
        await rowMenu.getByRole("menuitem", { name: w.auto }).waitFor();
        assert.equal(await rowMenu.getByRole("menuitem").count(), 1, "Makes… alone");
        await rowMenu.getByRole("menuitem", { name: w.auto }).click();
        await kindMenu.waitFor();
        assert.equal(await kindMenu.locator(".pm-item").count(), 3, "Auto, image and video");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        const box = await kindMenu.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width + 0.5, `the menu fits the window: ${JSON.stringify(box)}`);
        await kindMenu.locator(".pm-item", { hasText: w.video }).click();
        await kindMenu.waitFor({ state: "detached" });
        await clip.locator(".mkind-tag").waitFor();
        assert.equal(await clip.locator(".mkind-tag").textContent(), w.tagVideo);
        assert.equal(await page.locator("#status").textContent(), w.staged);
        assert.equal(await clip.getAttribute("aria-pressed"), pressed, "the right-click didn't tick or untick it");
        assert.equal(await drawer("grok-imagine-image").locator(".mkind-tag").count(), 0, "only the one marked");
        assert.equal(posts.length, 0, "staged, not saved");
        await page.locator(".editor").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts[0]?.modelPrefs, { "clip-x": { kind: "video" } });

        // a subscription's image models can't be marked: on a page loaded
        // afresh, the Save's own reply left aside
        await page.goto("http://magpie.test/?view=providers");
        await open("Codex Sub");
        await rclick(drawer("gpt-image-9"));
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        assert.equal(await rowMenu.count(), 0, "no menu to mark it");
        assert.deepEqual(errors, []);
      });
    }
  }
}
