// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's own account's balance query (Kylin_Cloud on X: one relay, keys
// of two accounts in one provider, both showing one account's balance).
// In a provider that reads balances, a key's Change key box has an
// Advanced fold, shut until the key has its own: User ID and Access token.
// Enter (or Save) posts keys/balance with the key's id; a click on the
// fold leaves the key's box open; the saved token is never shown, its
// field saying one is saved; Remove posts clear. A provider that reads no
// balance has no fold, and Change key posts what it did. In every
// language, at a wide and a narrow window; nothing scrolls.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = (takes) => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes, set: takes }, proxy: "",
  ...(takes && { keyBalanceAuth: true }),
  keyList: [
    { id: "aaaaaaaaaa", name: "Main", masked: "sk-…one", on: true, active: true },
    { id: "bbbbbbbbbb", name: "Team", masked: "sk-…two", on: true },
    { id: "dddddddddd", name: "Other", masked: "sk-…six", on: true, ...(takes && { balanceAuth: { user: "2", token: true } }) },
  ],
});

function serve(lang, posts, takes) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = () => ({ providers: [relay(takes)], presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname.startsWith("/api/keys/")) {
      posts.push({ path: url.pathname, ...JSON.parse(route.request().postData() || "{}") });
      return json(providers());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { edit: "Change key", adv: "Advanced", user: "User ID", token: "Access token", saved: "Access token saved", save: "Save", remove: "Remove", note: "Team's balance is read with its own access token", cleared: "Other's balance is read as the provider's" },
  zh: { edit: "更换密钥", adv: "高级", user: "用户 ID", token: "访问令牌", saved: "访问令牌已保存", save: "保存", remove: "移除", note: "Team 的余额改用它自己的访问令牌查询", cleared: "Other 的余额按供应商的设置查询" },
  "zh-TW": { edit: "更換金鑰", adv: "進階", user: "使用者 ID", token: "存取權杖", saved: "存取權杖已儲存", save: "儲存", remove: "移除", note: "Team 的餘額改用它自己的存取權杖查詢", cleared: "Other 的餘額按供應商的設定查詢" },
  ja: { edit: "キーを変更", adv: "詳細設定", user: "ユーザー ID", token: "アクセストークン", saved: "アクセストークン保存済み", save: "保存", remove: "削除", note: "Team の残高は自分のアクセストークンで照会します", cleared: "Other の残高はプロバイダーの設定で照会します" },
  de: { edit: "Schlüssel ändern", adv: "Erweitert", user: "Benutzer-ID", token: "Zugriffstoken", saved: "Zugriffstoken gespeichert", save: "Speichern", remove: "Entfernen", note: "Das Guthaben von Team wird mit dessen eigenem Zugriffstoken abgefragt", cleared: "Das Guthaben von Other wird wie beim Anbieter abgefragt" },
};

// .holds comes and goes with the wheel (app.js), and is no scroll
const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className.replace(/\s*\bholds\b/, "")}:${e.scrollTop}`)].join(" "));
const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
async function reach(page, row) {
  await page.mouse.move(200, 300);
  for (let i = 0; i < 30 && !(await row.evaluate((e) => e.getBoundingClientRect().bottom < innerHeight - 160)); i++) await page.mouse.wheel(0, 200);
  await page.waitForTimeout(200);
  await row.hover();
}
const posted = async (page, posts, n = 1) => { for (let i = 0; i < 60 && posts.length < n; i++) await page.waitForTimeout(50); };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 420]) {
      const w = want[lang];
      const open = async (t, name, id, takes = true) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, takes));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        const row = page.locator(`.acc[data-account-id="${id}"]`);
        await row.waitFor();
        await reach(page, row);
        const sc = await scrolled(page);
        await row.getByRole("button", { name: w.edit, exact: true }).click();
        await row.locator("input.key-value-in").waitFor();
        return { page, errors, posts, row, sc };
      };

      test(`${engine} ${lang} ${width}px: a key's own access token and user ID, under Advanced`, async (t) => {
        const { page, errors, posts, row, sc } = await open(t, "key-balance-auth", "bbbbbbbbbb");
        const fold = row.locator("details.key-bal-auth");
        assert.equal((await fold.locator("summary").textContent()).trim(), w.adv);
        assert.equal(await fold.evaluate((d) => d.open), false, "the fold is open on a key with none");
        await fold.locator("summary").click();
        await page.waitForFunction(() => document.querySelector("details.key-bal-auth")?.open);
        // the click left the key's box where it was
        assert.equal(await row.locator("input.key-value-in").count(), 1, "the key's box closed on a click on Advanced");
        const user = fold.getByRole("textbox", { name: w.user, exact: true });
        const token = fold.getByLabel(w.token, { exact: true });
        assert.equal(await user.getAttribute("placeholder"), w.user);
        assert.equal(await token.getAttribute("type"), "password");
        assert.equal(await token.getAttribute("placeholder"), w.token);
        await user.fill("2");
        assert.equal(await row.locator("input.key-value-in").count(), 1, "the key's box closed as the user ID took the focus");
        // no token yet: Save asks for it, posting nothing
        await fold.getByRole("button", { name: w.save, exact: true }).click();
        await page.waitForTimeout(150);
        assert.deepEqual(posts, []);
        assert.equal(await token.evaluate((e) => e === document.activeElement), true, "Save with no token didn't point at it");
        assert(!(await sideways(page)), "the fold scrolls the page sideways");
        await token.fill("tok-b");
        assert.equal(await scrolled(page), sc, "opening the fold or typing in it scrolled");
        await token.press("Enter");
        await posted(page, posts);
        assert.deepEqual(posts, [{ path: "/api/keys/balance", id: "relay", ref: "bbbbbbbbbb", token: "tok-b", user: "2" }]);
        await page.waitForFunction((n) => document.querySelector("#status")?.textContent.trim() === n, w.note);
        // saved, the fold is gone and the editor shorter, which a page
        // scrolled to its end can't stay at: the key's row is still in view
        const box = await row.boundingBox();
        assert(box && box.y >= 0 && box.y + box.height <= 800, `the key's row left the window: ${JSON.stringify(box)}`);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a key's saved access token is never shown, and Remove takes it off`, async (t) => {
        const { page, errors, posts, row } = await open(t, "key-balance-auth-saved", "dddddddddd");
        const fold = row.locator("details.key-bal-auth");
        assert.equal(await fold.evaluate((d) => d.open), true, "the fold is shut on a key with its own");
        assert.equal(await fold.getByRole("textbox", { name: w.user, exact: true }).inputValue(), "2");
        const token = fold.getByLabel(w.token, { exact: true });
        assert.equal(await token.inputValue(), "");
        assert.equal(await token.getAttribute("placeholder"), w.saved);
        assert(!(await sideways(page)), "the fold scrolls the page sideways");
        await fold.getByRole("button", { name: w.remove, exact: true }).click();
        await posted(page, posts);
        assert.deepEqual(posts, [{ path: "/api/keys/balance", id: "relay", ref: "dddddddddd", clear: true }]);
        await page.waitForFunction((n) => document.querySelector("#status")?.textContent.trim() === n, w.cleared);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: Escape in the fold leaves the key as it was`, async (t) => {
        const { page, errors, posts, row } = await open(t, "key-balance-auth-esc", "dddddddddd");
        const user = row.locator("details.key-bal-auth").getByRole("textbox", { name: w.user, exact: true });
        await user.fill("3");
        await user.press("Escape");
        await row.locator("input.key-value-in").waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, []);
        assert.equal(await page.locator("details.key-bal-auth").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a provider reading no balance has no fold under Change key`, async (t) => {
        const { page, errors, posts, row } = await open(t, "key-balance-auth-none", "bbbbbbbbbb", false);
        assert.equal(await row.locator("details.key-bal-auth").count(), 0);
        const field = row.locator("input.key-value-in");
        await field.fill("sk-new");
        await field.press("Enter");
        await posted(page, posts);
        assert.deepEqual(posts, [{ path: "/api/keys/replace", id: "relay", ref: "bbbbbbbbbb", key: "sk-new" }]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
