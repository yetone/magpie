// Node's test runner + Playwright; no user configuration or vendor is read.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [],
  balanceURL: "https://relay.example.com/api/usage/token", balancePath: "$data.total_available / 500000",
  balanceToken: { takes: true, set: true },
};
function server(posted, lang, provider = relay) {
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posted.push({ action: url.pathname.split("/").at(-1), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}
async function open(t, engine, lang, fixture) {
  const browser = await (engine === "webkit" ? webkit : chromium).launch();
  const page = await browser.newPage({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(6000);
  const errors = [], posted = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(posted, lang, fixture));
  t.after(async () => { try { assert.deepEqual(errors, []); } finally { await browser.close(); } });
  await page.goto("http://magpie.test/?view=providers");
  await page.locator(".row.provider", { hasText: "Relay" }).click();
  return { page, posted };
}
const headers = (page) => page.locator(".editor .headers .pair");
async function openTemplateMenu(page) {
  await page.locator(".bal-template").scrollIntoViewIfNeeded();
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.locator(".bal-template").click();
}
async function chooseTemplate(page, value) {
  await openTemplateMenu(page);
  await page.locator(".bal-template-menu .pm-item").nth(["", "newapi", "sub2api", "custom"].indexOf(value)).click();
}
async function newForm(page) {
  await page.evaluate(() => { adding = true; editing = { custom: true }; draft = null; renderProviders(); });
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) for (const lang of ["en", "zh"]) {
  test(`${engine}/${lang}: rounded platform menu, keyboard and dismissal`, async (t) => {
    const { page, posted } = await open(t, engine, lang);
    const picker = page.locator(".bal-template"), menu = page.locator(".bal-template-menu");
    assert.equal(await page.locator("select.bal-template").count(), 0);
    await picker.focus();
    await page.keyboard.press("Enter");
    assert.equal(await picker.getAttribute("aria-expanded"), "true");
    assert.equal(await menu.getByRole("menuitemradio").count(), 4);
    assert.equal(await menu.evaluate((e) => getComputedStyle(e).borderRadius), "11px");
    assert.equal(await menu.locator(".pm-item").first().evaluate((e) => getComputedStyle(e).borderRadius), "7px");
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("Enter");
    assert.equal(await picker.getAttribute("data-value"), "newapi");
    assert.equal(await picker.getAttribute("aria-expanded"), "false");
    assert.equal(await picker.evaluate((e) => e === document.activeElement), true);
    await openTemplateMenu(page);
    assert.equal(await menu.getByRole("menuitemradio", { checked: true }).count(), 1);
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      const a = await picker.boundingBox(), b = await menu.boundingBox();
      const x = Math.min(a.x, b.x) - 12, y = Math.min(a.y, b.y) - 12;
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-rounded-platform-menu.png`),
        clip: { x, y, width: Math.max(a.x + a.width, b.x + b.width) - x + 12, height: Math.max(a.y + a.height, b.y + b.height) - y + 12 } });
    }
    await page.keyboard.press("Escape");
    assert.equal(await menu.count(), 0);
    assert.equal(await picker.evaluate((e) => e === document.activeElement), true);
    await openTemplateMenu(page);
    await page.locator(".editor .ehead b").click();
    assert.equal(await menu.count(), 0);
    // The same popup fits a narrow window in the dark theme.
    await page.setViewportSize({ width: 390, height: 700 });
    await page.evaluate(() => document.documentElement.dataset.theme = "dark");
    await openTemplateMenu(page);
    const rect = await menu.boundingBox();
    assert(rect.x >= 0 && rect.x + rect.width <= 390 && rect.y + rect.height <= 700);
    await page.keyboard.press("Escape");
    assert.equal(posted.length, 0);
  });

  test(`${engine}/${lang}: standard templates, empty headers, custom values and save`, async (t) => {
    const { page, posted } = await open(t, engine, lang);
    const picker = page.locator(".bal-template"), url = page.locator(".bal-url"), field = page.locator(".bal-path");
    await chooseTemplate(page, "newapi");
    assert.equal(await url.inputValue(), "https://relay.example.com/api/user/self");
    assert.equal(await field.inputValue(), "$data.quota / 500000");
    assert.equal(await headers(page).count(), 1);
    assert.equal(await headers(page).first().locator("input").nth(0).inputValue(), "New-Api-User");
    assert.equal(await headers(page).first().locator("input").nth(1).inputValue(), "");
    assert.match(await page.locator(".bal-fix").textContent(), /New-Api-User/);
    assert.equal(posted.length, 0, "choosing a template never calls the backend");
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-newapi-template.png`), fullPage: true });
    }
    await chooseTemplate(page, "sub2api");
    assert.equal(await url.inputValue(), "https://relay.example.com/api/v1/user/profile");
    assert.equal(await field.inputValue(), "$data.balance");
    assert.equal(await headers(page).count(), 0, "only the generated empty header is removed");
    await chooseTemplate(page, "newapi");
    await headers(page).first().locator("input").nth(0).fill("new-api-user");
    await headers(page).first().locator("input").nth(1).fill("42");
    await chooseTemplate(page, "sub2api");
    assert.equal(await headers(page).count(), 1, "a filled account ID survives template switching");
    await chooseTemplate(page, "newapi");
    assert.equal(await headers(page).count(), 1, "case-insensitive header matching");
    assert.equal(await page.locator(".bal-fix").textContent(), "");
    await url.fill("https://custom.example:9443/tenant/query?account=42");
    await field.fill("$credits.left + data.quota / 500000");
    await chooseTemplate(page, "sub2api");
    assert.equal(await url.inputValue(), "https://custom.example:9443/tenant/query?account=42");
    assert.equal(await field.inputValue(), "$credits.left + data.quota / 500000");
    assert.match(await page.locator(".bal-template-note").textContent(), lang === "zh" ? /保留/ : /kept/);
    await chooseTemplate(page, "custom");
    await page.locator(".editor .bar .primary").click();
    await page.waitForFunction(() => !document.querySelector(".editor"));
    const saved = posted.find((p) => p.action === "save").body;
    assert.equal(saved.balanceURL, "https://custom.example:9443/tenant/query?account=42");
    assert.equal(saved.balancePath, "$credits.left + data.quota / 500000");
    assert.deepEqual(saved.headers, { "new-api-user": "42" });
    assert.equal("balanceTemplateState" in saved, false);
    // Previously saved custom values and other headers remain untouched.
    await page.evaluate(() => {
      editing = { id: "relay" }; draft = null; renderProviders();
      draft.balanceURL = "https://old.example:8443/tenant/balance";
      draft.balancePath = "$wallet.credit";
      draft.headers = [["new-api-user", "73"], ["X-Workspace", "team"]];
      draft.balanceTemplateState = undefined; renderProviders();
    });
    await chooseTemplate(page, "newapi");
    assert.equal(await url.inputValue(), "https://old.example:8443/tenant/balance");
    assert.equal(await field.inputValue(), "$wallet.credit");
    assert.equal(await headers(page).count(), 2);
    assert.equal(await headers(page).first().locator("input").nth(1).inputValue(), "73");
    assert.equal(await headers(page).nth(1).locator("input").nth(1).inputValue(), "team");
    await chooseTemplate(page, "custom");
    assert.equal(await url.inputValue(), "https://old.example:8443/tenant/balance");
    assert.equal(await field.inputValue(), "$wallet.credit");
  });

  test(`${engine}/${lang}: manual choice, late Base URL, linked origin and edited address`, async (t) => {
    const { page } = await open(t, engine, lang);
    await newForm(page);
    const base = page.locator(".editor input[type=url]").first(), token = page.locator(".editor input[type=password]").nth(1);
    const picker = page.locator(".bal-template"), url = page.locator(".bal-url"), field = page.locator(".bal-path");
    await token.fill("eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln");
    assert.equal(await picker.getAttribute("data-value"), "");
    assert.equal(await url.inputValue(), "", "a JWT does not select a platform");
    await chooseTemplate(page, "newapi");
    assert.equal(await url.inputValue(), "");
    assert.equal(await field.inputValue(), "$data.quota / 500000");
    await base.fill("not-a-url");
    assert.equal(await url.inputValue(), "");
    await base.fill("http://localhost:18080/v1/");
    assert.equal(await url.inputValue(), "http://localhost:18080/api/user/self");
    await base.fill("https://relay.example:8443/v1");
    assert.equal(await url.inputValue(), "https://relay.example:8443/api/user/self");
    await url.fill("https://panel.example/tenant/api/user/self");
    await base.fill("https://another.example/v1");
    assert.equal(await url.inputValue(), "https://panel.example/tenant/api/user/self");
    await page.evaluate(() => renderProviders());
    assert.equal(await picker.getAttribute("data-value"), "newapi", "the choice survives draft redraws");
    assert.equal(await headers(page).count(), 1);
    await token.fill("replacement-token");
    await token.fill("");
    assert.equal(await url.inputValue(), "https://panel.example/tenant/api/user/self", "removing a token preserves query settings");
    assert.equal(await field.inputValue(), "$data.quota / 500000");
    // Clearing a field explicitly makes it eligible for filling again.
    await page.locator(".editor details.more > summary").click();
    await url.fill("");
    await field.fill("");
    await chooseTemplate(page, "sub2api");
    assert.equal(await url.inputValue(), "https://another.example/api/v1/user/profile");
    assert.equal(await field.inputValue(), "$data.balance");
    await base.fill("https://linked.example:9443/v1");
    assert.equal(await url.inputValue(), "https://linked.example:9443/api/v1/user/profile");
    // A different protocol supplies the origin when the active one is empty.
    await newForm(page);
    await page.evaluate(() => { draft.anthropic = "http://fallback.example:8080/prefix"; renderProviders(); });
    await chooseTemplate(page, "sub2api");
    assert.equal(await url.inputValue(), "http://fallback.example:8080/api/v1/user/profile");
    const missing = await page.evaluate(() => ["Balance query template", "Choose a platform…", "Fill in the Base URL to complete the balance query address.", "Your custom balance address or field was kept; check it for the selected platform.", "Choose the panel yourself; the token's format does not identify it."].filter((k) => !I18N.zh[k]));
    assert.deepEqual(missing, []);
  });

  test(`${engine}/${lang}: reopening an incomplete account restores the empty row and omits it on save`, async (t) => {
    const { page, posted } = await open(t, engine, lang, { ...relay, balanceURL: "https://relay.example.com/api/user/self", balancePath: "$data.quota / 500000" });
    assert.equal(await page.locator(".bal-template").getAttribute("data-value"), "newapi");
    assert.equal(await headers(page).first().locator("input").nth(0).inputValue(), "New-Api-User");
    assert.equal(await headers(page).first().locator("input").nth(1).inputValue(), "");
    await page.locator(".editor .bar .primary").click();
    await page.waitForFunction(() => !document.querySelector(".editor"));
    assert.deepEqual(posted.find((p) => p.action === "save").body.headers, {});
  });
}
