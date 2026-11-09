// Run with Node's test runner and Playwright on the module path; see README.md.
// mahiro233 on Discord (Kiro plugin 0.1.34): a moved Kiro opens its
// plugin's first way at once (#777), and that way asks first which sign-in
// to use. The question offered its three choices and Cancel only, so the
// plugin's other ways, its API key among them, couldn't be reached. The
// first question now has "Other ways to sign in…", as the waiting box has;
// a later question (the Start URL) doesn't. And the plugin's words show in
// the window's language when it gives them by language (labels, messages,
// hints), as the rest of the sheet does, else as written.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const plugins = [
  { id: "kiro", pid: "kiro", name: "Kiro", icon: "generic", spec: "@magpie-community/opencode-kiro-auth", signedIn: false, models: 3,
    methods: [
      { type: "oauth", label: "Kiro (Google, GitHub, AWS Builder ID, IAM Identity Center)", labels: { zh: "Kiro（Google、GitHub、AWS Builder ID、IAM Identity Center）" } },
      { type: "oauth", label: "Kiro CLI's or Kiro IDE's sign-in", labels: { zh: "Kiro CLI 或 Kiro IDE 已登录的账号" } },
      { type: "api", label: "Kiro API key (ksk_…)", labels: { zh: "Kiro API Key（ksk_…）" }, placeholder: "ksk_…" },
    ] },
];

const pick = {
  type: "select", key: "loginMethod", message: "Choose a sign-in method", messages: { zh: "选择登录方式" }, placeholder: "",
  options: [
    { label: "IAM Identity Center (organization Start URL)", value: "enterprise", hint: "Sign in to your organization with its AWS Start URL", labels: { zh: "IAM Identity Center（组织 Start URL）" }, hints: { zh: "用组织的 AWS Start URL 登录" } },
    { label: "AWS Builder ID", value: "builderid", hint: "Open AWS sign-in directly", labels: { zh: "AWS Builder ID" }, hints: { zh: "直接打开 AWS 登录" } },
    { label: "Kiro sign-in page", value: "portal", hint: "Google, GitHub or IAM Identity Center" },
  ],
};
const startURL = { type: "text", key: "startUrl", message: "Organization Start URL", messages: { zh: "组织 Start URL" }, placeholder: "https://d-xxxxxxxxxx.awsapps.com/start" };

function server(lang, asked) {
  const payload = () => ({ providers: [{ id: "deepseek", name: "DeepSeek", icon: "deepseek", chat: "https://api.deepseek.com/v1", models: [], agents: [], key: {} }], presets: [], excluded: [], gateway: { running: true, window: true }, plugins, onPlugins: ["kiro"] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugin-signin/prompt") {
      const b = body();
      asked.push(["prompt", b]);
      const inputs = { ...b.inputs, ...(b.key ? { [b.key]: b.value } : {}) };
      if (b.method !== 0) return json({ prompt: null, inputs });
      if (!inputs.loginMethod) return json({ prompt: pick, inputs });
      if (inputs.loginMethod === "enterprise" && !inputs.startUrl) return json({ prompt: startURL, inputs });
      return json({ prompt: null, inputs });
    }
    if (url.pathname === "/api/plugin-signin") { asked.push(["signin", body()]); return json({ id: "s1", agent: "kiro", state: "waiting", url: "https://fake.test/kiro" }); }
    if (/^\/api\/signin\/s\d+$/.test(url.pathname)) return new Promise(() => {}); // stays waiting
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: {
    ask: "Choose a sign-in method", other: "Other ways to sign in…", how: "How do you sign in to",
    options: ["IAM Identity Center (organization Start URL)", "AWS Builder ID", "Kiro sign-in page"],
    hint: "Sign in to your organization with its AWS Start URL",
    ways: ["Kiro (Google, GitHub, AWS Builder ID, IAM Identity Center)", "Kiro CLI's or Kiro IDE's sign-in", "Kiro API key (ksk_…)"],
    url: "Organization Start URL",
  },
  zh: {
    ask: "选择登录方式", other: "其他登录方式…", how: "用哪种方式登录",
    // the third gives no Chinese: shown as the plugin wrote it
    options: ["IAM Identity Center（组织 Start URL）", "AWS Builder ID", "Kiro sign-in page"],
    hint: "用组织的 AWS Start URL 登录",
    ways: ["Kiro（Google、GitHub、AWS Builder ID、IAM Identity Center）", "Kiro CLI 或 Kiro IDE 已登录的账号", "Kiro API Key（ksk_…）"],
    url: "组织 Start URL",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a moved plugin's first question offers its other ways, in the window's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        const box = sheet.locator(".signing");

        // one click opens the plugin's first way, which asks first
        await sheet.locator('.tile[data-pick="Kiro"]').click();
        await box.locator("span.n", { hasText: w.ask }).waitFor();
        assert.deepEqual(await box.locator(".choices button").allTextContents(), w.options);
        assert.equal(await box.locator(".choices button").first().getAttribute("title"), w.hint);
        if (process.env.ARTIFACT_DIR) await box.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-ask-ways-${engine}-${lang}.png`) });

        // its other ways, the key among them
        await box.locator("button.other-ways", { hasText: w.other }).click();
        await box.locator("span.n", { hasText: w.how }).waitFor();
        assert.deepEqual(await box.locator(".choices button[data-method]").allTextContents(), w.ways);
        await box.locator('.choices button[data-method="2"]').click();
        const key = sheet.locator('.signing input[type="password"]');
        await key.waitFor();
        assert.equal(await key.getAttribute("aria-label"), w.ways[2]);
        assert.equal(await key.getAttribute("placeholder"), "ksk_…");
        assert.equal(asked.filter(([k]) => k === "signin").length, 0, "the key way signed in before a key was typed");
        await box.locator("button.text:not(.primary)").first().click(); // Cancel

        // a later question is the way's own: no way out of it but Cancel
        await sheet.locator('.tile[data-pick="Kiro"]').click();
        await box.locator(".choices button", { hasText: w.options[0] }).click();
        const field = sheet.locator('.signing input[type="text"]');
        await field.waitFor();
        assert.equal(await field.getAttribute("aria-label"), w.url);
        assert.equal(await box.locator("button.other-ways").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  });
}
