// Run with Node's test runner and Playwright on the module path; see README.md.
// A preset's or a subscription's model asked on the API the user picks
// (01huadalang on Discord: OpenCode Go's DeepSeek answers on Responses
// too, and magpie asked it on chat alone; a custom provider could pick).
// A model chip's right-click has "Asked on: Auto…" beside its test, for a
// provider with more than one API: it opens the app's menu (never a native
// <select>), Auto saying what the vendor's list says, and the pick is
// staged as Model settings stages it — the chip tagged with it, the page
// not moved — and sent with the Save as modelPrefs. A subscription's model
// gets the same, and Model settings' API row too. A provider with one API
// has nothing to pick. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const go = {
  ...base, id: "opencode-go", name: "OpenCode Go", preset: "opencode-go", icon: "opencode",
  chat: "https://opencode.ai/zen/go/v1", responses: "https://opencode.ai/zen/go/v1", anthropic: "https://opencode.ai/zen/go",
  models: [{ id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", on: true }, { id: "minimax-m3", name: "MiniMax M3", on: true, auto: ["anthropic"] }],
  key: { set: true, masked: "sk-…one" },
};
const sub = {
  ...base, id: "copilot", name: "GitHub Copilot", icon: "githubcopilot",
  chat: "https://api.githubcopilot.com", responses: "https://api.githubcopilot.com", anthropic: "https://api.githubcopilot.com",
  account: { agent: "copilot", user: "octo", accounts: [] },
  models: [{ id: "gpt-9", name: "GPT-9", on: true, api: "responses" }],
  key: { set: false, masked: "" },
};
const one = {
  ...base, id: "solo", name: "Solo", chat: "https://solo.example.com/v1", responses: "", anthropic: "",
  models: [{ id: "solo-1", name: "solo-1", on: true }], key: { set: true, masked: "sk-…two" },
};

function serve(lang, posts) {
  const providers = { providers: [go, sub, one], presets: [], excluded: [], gateway: { running: true, window: true } };
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
  en: { auto: "Asked on: Auto…", resp: "Asked on Responses…", head: "API this model is asked on", autoNote: "As its vendor's list says: Anthropic", save: "Save", staged: "deepseek-v4.1-flash is asked on Responses once saved" },
  zh: { auto: "协议：自动…", resp: "协议：Responses…", head: "这个模型请求所用的协议", autoNote: "按供应商模型列表：Anthropic", save: "保存", staged: "保存后 deepseek-v4.1-flash 将使用 Responses 协议" },
};

// a right-click on a chip out of sight: Playwright scrolls it in first, and
// Chromium sends that scroll's event a frame later, which closes the menu
// the click opened (a menu closes on a scroll). Scroll it in, let the
// frame pass, then click.
async function rclick(loc) {
  await loc.click({ button: "right", trial: true }); // scrolls it in, clicks nothing
  await loc.page().evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await loc.click({ button: "right" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a preset's and a subscription's model asked on the API picked`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-api-preset.png`) });
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
      const apiMenu = page.locator(".pop.proto-menu.model-api-menu");

      await open("OpenCode Go");
      // MiniMax's Auto says what the vendor's list says
      await rclick(chip("MiniMax M3"));
      await rowMenu.getByRole("menuitem", { name: w.auto }).click();
      await apiMenu.waitFor();
      assert.equal(await apiMenu.locator(".pm-head").textContent(), w.head);
      assert((await apiMenu.locator(".pm-item").first().textContent()).includes(w.autoNote), "Auto says the vendor's list");
      assert.equal(await page.locator(".editor select").count(), 0, "no native select");
      await page.keyboard.press("Escape");
      await apiMenu.waitFor({ state: "detached" });

      // DeepSeek picked onto Responses, the page where it was
      const ds = chip("DeepSeek V4.1 Flash");
      const top = () => page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".editor, .editor *")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
      const before = await top();
      const picked = await page.locator(".editor .mchips .mchip.on").count();
      await rclick(ds);
      await rowMenu.getByRole("menuitem", { name: w.auto }).click();
      await apiMenu.waitFor();
      const names = await apiMenu.locator(".pm-name").allTextContents();
      assert.equal(names.length, 4, `Auto and the provider's three APIs: ${names}`);
      const border = await page.evaluate(() => [...document.querySelectorAll(".pop.proto-menu, .pop.proto-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      await apiMenu.locator(".pm-item", { hasText: "Responses" }).click();
      await apiMenu.waitFor({ state: "detached" });
      await chip("DeepSeek V4.1 Flash").locator(".mapi-tag").waitFor();
      assert.equal(await chip("DeepSeek V4.1 Flash").locator(".mapi-tag").textContent(), "Responses");
      assert.equal(await page.locator("#status").textContent(), w.staged);
      assert.equal(await top(), before, "the page didn't move");
      assert.equal(await page.locator(".editor .mchips .mchip.on").count(), picked, "nothing picked or unpicked");
      assert.equal(posts.length, 0, "staged, not saved");
      // the menu now says it, and Model settings shows it too
      await rclick(chip("DeepSeek V4.1 Flash"));
      await rowMenu.getByRole("menuitem", { name: w.resp }).waitFor();
      await page.keyboard.press("Escape");
      await page.locator(".editor").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts[0]?.modelPrefs, { "deepseek-v4.1-flash": { api: "responses" } });

      // a subscription's model: its pick shown, and Model settings has the row
      await open("GitHub Copilot");
      assert.equal(await chip("GPT-9").locator(".mapi-tag").textContent(), "Responses");
      await rclick(chip("GPT-9"));
      await rowMenu.getByRole("menuitem", { name: w.resp }).waitFor();
      await page.keyboard.press("Escape");
      await page.locator(".editor").getByRole("button", { name: lang === "en" ? "Model settings" : "模型设置" }).click();
      await page.locator(".editor .mnames .mapi").waitFor();

      // one API: nothing to pick
      await page.keyboard.press("Escape");
      await open("Solo");
      await rclick(chip("solo-1"));
      await rowMenu.waitFor();
      // its test, Copy model ID (81879b90) and image input, no API to pick
      assert.equal(await rowMenu.getByRole("menuitem").count(), 3, "the test, the copy and image input only");
      assert.equal(await rowMenu.getByRole("menuitem", { name: /Asked on|协议/ }).count(), 0, "no API to pick");
      assert.deepEqual(errors, []);
    });
  }
}
