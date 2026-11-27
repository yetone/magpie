// Run with Node's test runner and Playwright on the module path; see README.md.
// agy-cli (Antigravity through the local agy CLI) warns before it is first
// used, as Antigravity's own sign-in does (WdBlink_ on X: an agy-cli account
// was suspended after use from Claude Code). Its first sign-in, from the Add
// sheet's tile and from the Plugins page's Sign in (pluginSignIn), stops at
// the risk step and asks nothing of the plugin until "Sign in anyway"; with
// an agy-cli account signed in already it asks at once, and the move line
// over the list says no risk. In every language and both engines; the API is
// faked here. MAGPIE_ASSETS points at older assets to see it red.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const plugin = { id: "agy-cli", pid: "agy-cli", name: "Antigravity (local agy CLI)", icon: "antigravity-color", spec: "@magpie-community/opencode-agy-cli-auth@0.1.153", models: 4, methods: [{ type: "oauth", label: "agy" }] };
const agyRow = { id: "agy-cli", name: "Antigravity (local agy CLI)", icon: "antigravity-color", chat: "plugin://agy-cli/v1", models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "agy-cli", agentName: "Antigravity (local agy CLI)", user: "ada@g", logins: [{ user: "ada@g", active: true, on: true }] } };
const withdrawn = [{ id: "antigravity", name: "Antigravity", instead: "Antigravity (local agy CLI)", package: "@magpie-community/opencode-agy-cli-auth" }];

function serve(lang, asked, signedIn) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      return json({ providers: signedIn ? [agyRow] : [], presets: [], excluded: [], gateway: { running: true, window: true },
        plugins: [{ ...plugin, signedIn: !!signedIn }], withdrawn, agyMove: ["b@x.com"] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(route.request().postDataJSON().provider); return json({}); }
    if (url.pathname === "/api/plugin-signin") return json({ id: "s1", agent: "agy-cli", state: "waiting", url: "https://accounts.google.test/" });
    if (url.pathname.startsWith("/api/signin/")) return json({ id: "s1", agent: "agy-cli", state: "waiting" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the sign-in's risk step, in each language's own words
const L = {
  en: { risk: "Google may suspend an Antigravity account it sees used from another agent. Use one you can afford to lose.", anyway: "Sign in anyway" },
  zh: { risk: "Google 可能封禁经其他 Agent 使用的 Antigravity 账号。请用丢了也不心疼的账号。", anyway: "仍然登录" },
  "zh-TW": { risk: "Google 可能停權經其他 Agent 使用的 Antigravity 帳號。請用丟了也不心疼的帳號。" },
  ja: { risk: "Google は、他のエージェントから使われている Antigravity アカウントを停止することがあります。失っても困らないアカウントを使ってください。" },
  de: { risk: "Google kann ein Antigravity-Konto sperren, wenn es über einen anderen Agenten verwendet wird. Verwenden Sie eines, auf das Sie verzichten können." },
};

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
const tr = (page, s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);

async function open(browser, lang, asked, signedIn) {
  const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(15000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, asked, signedIn));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator("#movable .agy-move").waitFor({ timeout: 60000 });
  return { page, errors };
}

// with no provider the Add sheet is open from the start, and its button
// steps aside while the sheet's head is in sight; otherwise it opens it
async function addSheet(page) {
  await page.waitForFunction(() => typeof providers === "object" && providers);
  if (await page.locator("#addSheet").isHidden()) await page.locator("#addProvider").click();
  return page.locator("#addSheet");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) {
    test(`${engine} ${lang}: agy-cli's first sign-in says Google may suspend the account`, async (t) => {
      const w = L[lang];
      const browser = await launch(engine);
      t.after(() => browser.close());
      const asked = [];
      const { page, errors } = await open(browser, lang, asked, false);
      // the move line says it too while agy-cli has no account
      const move = page.locator("#movable .agy-move");
      await move.waitFor();
      assert.ok((await move.innerText()).includes(w.risk), `the move line doesn't say the risk: ${await move.innerText()}`);

      // from the Add sheet's tile
      const sheet = await addSheet(page);
      // beside the built-in Antigravity tile, which has the same short name
      const tile = sheet.locator('.tile[title*="local agy CLI"]');
      await tile.click();
      let box = sheet.locator(".signing");
      await box.waitFor();
      assert.equal((await box.locator(".n").textContent()).trim(), await tr(page, "{name} accounts can be suspended", { name: plugin.name }));
      assert.ok((await box.textContent()).includes(w.risk), `no risk step: ${await box.textContent()}`);
      assert.doesNotMatch(await box.textContent(), /magpie|safe|安全/i);
      assert.deepEqual(asked, [], "the plugin was asked before the risk was said");
      await box.locator("button", { hasText: await tr(page, "Cancel") }).click();
      await box.waitFor({ state: "detached" });

      // from the Plugins page's Sign in, which opens the same sheet
      await page.evaluate(() => pluginSignIn("agy-cli"));
      box = page.locator("#addSheet .signing");
      await box.waitFor();
      assert.ok((await box.textContent()).includes(w.risk), `Plugins' sign-in skips the risk: ${await box.textContent()}`);
      assert.deepEqual(asked, []);

      // Sign in anyway goes on to the plugin's sign-in
      await box.locator("button", { hasText: await tr(page, "Sign in anyway") }).click();
      for (let i = 0; i < 300 && !asked.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(asked, ["agy-cli"]);
      if (w.anyway) assert.equal(await tr(page, "Sign in anyway"), w.anyway);
      assert.deepEqual(errors, []);
    });
  }

  // b75fa328c's rule: never again beside an account signed in
  test(`${engine}: with an agy-cli account, another is asked at once and the move line says no risk`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const asked = [];
    const { page, errors } = await open(browser, "en", asked, true);
    const move = page.locator("#movable .agy-move");
    await move.waitFor();
    assert.ok(!(await move.innerText()).includes(L.en.risk), `said again beside an account: ${await move.innerText()}`);
    await (await addSheet(page)).locator('.tile[title*="local agy CLI"]').click();
    for (let i = 0; i < 300 && !asked.length; i++) await page.waitForTimeout(50);
    assert.deepEqual(asked, ["agy-cli"]);
    assert.ok(!(await page.locator("#addSheet").textContent()).includes(L.en.risk));
    assert.deepEqual(errors, []);
  });
}
