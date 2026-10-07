// Run with Node's test runner and Playwright on the module path; see README.md.
// Google Vertex AI is asked at the user's own Google Cloud project with
// their Google credentials, not a key. Its editor, adding one or editing
// one saved, has Project ID (focused first, and needed), Location,
// Credentials file and Service account where another's has API key; Save
// posts them trimmed as vertex, with no key, and Test asks with what is
// typed. Endpoints shows the address the project and location make. Its
// rows say which credentials sign it, never that a key is missing. A click
// leaves the page where it is. English and Chinese; the pills and the fields
// fit 560px and 440px windows in Japanese and German too. No backend, the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SA = "magpie@my-project-123.iam.gserviceaccount.com";
const presets = (added) => [
  { id: "google", name: "Google Gemini", icon: "gemini-color", kind: "vendor", chat: "https://generativelanguage.googleapis.com/v1beta/openai", note: "Gemini Developer API", added: false },
  {
    id: "google-vertex", name: "Google Vertex AI", short: "Vertex AI", icon: "vertexai-color", kind: "vendor", added, noList: true, vertex: true,
    note: "your Google Cloud project, with gcloud's sign-in", website: "https://docs.cloud.google.com/gemini-enterprise-agent-platform/models",
    headerHints: ["X-Vertex-AI-LLM-Request-Type", "X-Vertex-AI-LLM-Shared-Request-Type"], models: ["gemini-3.8-flash", "gemini-2.5-pro"],
  },
];
// a Vertex AI provider as /api/providers has it: no key, its vertex as kept
const vertexRow = (id, name, host, vertex, ready = true) => ({
  id, name, icon: "vertexai-color", preset: "google-vertex", chat: "", responses: "", anthropic: "", catalog: "", host,
  models: [{ id: "gemini-3.8-flash", name: "gemini-3.8-flash", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready, vertex,
});
const saved = vertexRow("google-vertex", "Google Vertex AI", "aiplatform.us.rep.googleapis.com", { project: "my-project-123", location: "us", credentials: "~/keys/vertex-sa.json", impersonate: SA });
const adc = vertexRow("google-vertex-2", "Vertex EU", "aiplatform.eu.rep.googleapis.com", { project: "team-prod", location: "eu" });
const file = vertexRow("google-vertex-3", "Vertex dev", "aiplatform.googleapis.com", { project: "team-dev", location: "global", credentials: "~/keys/dev-sa.json" });
// providers.json edited by hand to have no project: nowhere to ask
const bare = vertexRow("google-vertex-4", "Vertex draft", "", {}, false);
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "", host: "relay.example.com",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
};

function serve(lang, web, provs, pres, posts) {
  const providers = { providers: provs, presets: pres, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length);
      posts.push({ action, body: route.request().postDataJSON() });
      // Vertex AI's one endpoint answers
      if (action === "test") return json({ results: [{ protocol: "gemini", ok: true, ms: 412, model: "gemini-3.8-flash" }] });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: {
    labels: ["Project ID", "Location", "Credentials file", "Service account"], key: "API key", endpoints: "Endpoints",
    account: "optional · a service account's email",
    hints: [/^Your Google Cloud project, with the Agent Platform API \(aiplatform\.googleapis\.com\) enabled: requests are made, and billed, there$/, /^global, us, eu or a region such as us-central1\. Each location serves its own models/,
      /Application Default Credentials are used \(gcloud auth application-default login\)/, /impersonated with the credentials above.*roles\/iam\.serviceAccountTokenCreator/],
    needed: "Google Vertex AI needs the id of your Google Cloud project", add: "Add", save: "Save", dup: "Duplicate", another: "Add another Google Vertex AI",
    more: "One more Google Vertex AI provider, with its own project, credentials, headers and models",
    copy: "A new provider with Google Vertex AI's project, credentials, headers and models, to change before adding",
    gemini: "Gemini generateContent — what Vertex AI serves at your Google Cloud project", ms: "412 ms",
    adc: "Google ADC", needsProject: "needs a project", noKey: ["no key", "needs a key"],
    as: `Signed as the service account ${SA}, with your Google credentials`, signed: "Signed with your Google credentials",
    adcLong: "Application Default Credentials", open: "Open the row and give your Google Cloud project's id",
  },
  zh: {
    labels: ["项目 ID", "位置", "凭据文件", "服务账号"], key: "API 密钥", endpoints: "端点",
    account: "可选 · 服务账号的邮箱",
    hints: [/^你的 Google Cloud 项目，需已启用 Agent Platform API（aiplatform\.googleapis\.com）：请求在这个项目中发出并计费$/, /^global、us、eu，或 us-central1 这样的区域。每个位置提供的模型各不相同/,
      /gcloud 的应用默认凭据（gcloud auth application-default login）/, /由上方的凭据模拟.*roles\/iam\.serviceAccountTokenCreator/],
    needed: "Google Vertex AI 需要你的 Google Cloud 项目 ID", add: "添加", save: "保存", dup: "复制", another: "再添加一个 Google Vertex AI",
    more: "再添加一个 Google Vertex AI 供应商，使用各自的项目、凭据、请求头和模型",
    copy: "新建供应商，沿用 Google Vertex AI 的项目、凭据、请求头和模型，添加前可修改",
    gemini: "Gemini generateContent — Vertex AI 在你的 Google Cloud 项目中提供", ms: "412 毫秒",
    adc: "Google 默认凭据", needsProject: "需要项目", noKey: ["无需密钥", "需要密钥"],
    as: `使用你的 Google 凭据，以服务账号 ${SA} 的身份签名`, signed: "使用你的 Google 凭据签名",
    adcLong: "应用默认凭据", open: "展开这一行并填写你的 Google Cloud 项目 ID",
  },
};
const FIELDS = ["vertex-project", "vertex-location", "vertex-credentials", "vertex-impersonate"];
const STRINGS = [
  "your Google Cloud project, with gcloud's sign-in", "Project ID", "Location", "Credentials file", "Service account", "optional · a service account's email",
  "Your Google Cloud project, with the Agent Platform API (aiplatform.googleapis.com) enabled: requests are made, and billed, there",
  "global, us, eu or a region such as us-central1. Each location serves its own models; global serves them all.",
  "Optional. Left empty, gcloud's Application Default Credentials are used (gcloud auth application-default login), or the file GOOGLE_APPLICATION_CREDENTIALS names. A service account's key file works too.",
  "Requests are then made as this service account, impersonated with the credentials above: their account needs roles/iam.serviceAccountTokenCreator on it.",
  "Google Vertex AI needs the id of your Google Cloud project", "Gemini generateContent — what Vertex AI serves at your Google Cloud project",
  "Google ADC", "needs a project", "Open the row and give your Google Cloud project's id", "Signed with your Google credentials",
  "Signed as the service account {account}, with your Google credentials", "Application Default Credentials",
  "One more {name} provider, with its own project, credentials, headers and models",
  "A new provider with {name}'s project, credentials, headers and models, to change before adding",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const start = async (t, lang, name, provs, pres, { web = false, width = 900 } = {}) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-vertex.png`) });
      }
      await browser.close();
    });
    // short, so the editor scrolls and a click that moved it would show
    const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    const errors = [], posts = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(lang, web, provs, pres, posts));
    await page.goto("http://magpie.test/?view=providers");
    return { page, errors, posts };
  };
  const edit = async (page, id) => {
    await page.locator(`.row.provider[data-id="${id}"]`).click();
    const ed = page.locator(".editor");
    await ed.locator("input.vertex-project").waitFor();
    return ed;
  };
  const values = (ed) => Promise.all(FIELDS.map((cls) => ed.locator("input." + cls).inputValue()));
  const endpointsOf = (ed) => ed.locator(".eps .ep").evaluateAll((es) => es.map((e) => [e.querySelector(".pl").textContent, e.querySelector("code").textContent]));
  // a click, the page left where it was
  const click = async (page, loc) => {
    await loc.scrollIntoViewIfNeeded();
    await page.waitForTimeout(200);
    const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
    await loc.click();
    await page.waitForTimeout(250);
    const after = await loc.evaluate((e) => e.getBoundingClientRect().top);
    assert(Math.abs(after - before) <= 1, `moved from ${before} to ${after}`);
  };
  // the body of the first post of action after the n-th, null if none comes
  const posted = async (page, posts, action, n, ms = 5000) => {
    for (let i = 0; i < ms / 50 && !posts.slice(n).some((p) => p.action === action); i++) await page.waitForTimeout(50);
    return posts.slice(n).find((p) => p.action === action)?.body ?? null;
  };
  const press = async (page, posts, label, action = "save", ms) => {
    const n = posts.length;
    await page.locator(".editor .bar").getByRole("button", { name: label, exact: true }).click();
    return posted(page, posts, action, n, ms);
  };
  const focused = (page, cls) => page.evaluate((cls) => !!document.activeElement?.classList.contains(cls), cls);

  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Google Vertex AI is added with its project, not a key`, async (t) => {
      const { page, errors, posts } = await start(t, lang, "add", [relay], presets(false));
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Vertex AI"]').click();
      const ed = page.locator(".editor.new");
      await ed.locator(".ehead b", { hasText: "Google Vertex AI" }).waitFor();
      await page.waitForFunction(() => document.activeElement?.classList.contains("vertex-project"));

      // its four fields where the key would be, in order, each labelled,
      // with its example and what it is for
      assert.equal(await ed.locator("input[type=password]").count(), 0, "no key to paste");
      const labels = await ed.locator("label").allTextContents();
      assert(!labels.includes(w.key), labels.join(" | "));
      const placeholders = ["my-project-123", "global", "~/.config/gcloud/application_default_credentials.json", w.account];
      let above = -Infinity;
      for (const [i, cls] of FIELDS.entries()) {
        const box = ed.locator("input." + cls);
        assert.equal(await box.getAttribute("placeholder"), placeholders[i]);
        assert.equal(await box.inputValue(), "");
        assert.equal(await ed.locator(`label[for="${await box.getAttribute("id")}"]`).textContent(), w.labels[i]);
        assert.match(await box.locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent(), w.hints[i]);
        const y = (await box.boundingBox()).y;
        assert(y > above, cls + " below the field before it");
        above = y;
      }
      // no address until there is a project to make one of
      const endpoints = ed.locator("label", { hasText: new RegExp("^" + w.endpoints + "$") });
      assert.equal(await endpoints.isVisible(), false);

      // none typed: said, nothing sent, the project focused and the
      // editor left where it was scrolled to
      await click(page, ed.locator("input.vertex-location"));
      const scroller = page.locator(".modal .editor > .ebody");
      await scroller.evaluate((e) => { e.scrollTop = e.scrollHeight; });
      await page.waitForTimeout(100);
      const top = await scroller.evaluate((e) => e.scrollTop);
      assert(top > 0, "the editor scrolls");
      assert.equal(await press(page, posts, w.add, "save", 400), null);
      assert.equal(await ed.locator(".editor-error").textContent(), w.needed);
      assert(await focused(page, "vertex-project"));
      assert.equal(await scroller.evaluate((e) => e.scrollTop), top, "the Add moved the editor");

      // typed with stray spaces: the address shows, and Enter sends them
      // trimmed as vertex, with no key
      const box = (cls) => ed.locator("input." + cls);
      await box("vertex-project").fill(" My-Project-123 ");
      assert.equal(await endpoints.isVisible(), true);
      assert.deepEqual(await endpointsOf(ed), [["Gemini", "https://aiplatform.googleapis.com/v1/projects/my-project-123/locations/global"]]);
      await box("vertex-location").fill(" us-central1 ");
      assert.deepEqual(await endpointsOf(ed), [["Gemini", "https://us-central1-aiplatform.googleapis.com/v1/projects/my-project-123/locations/us-central1"]]);
      await box("vertex-credentials").fill(" ~/keys/sa.json ");
      await box("vertex-impersonate").fill(` ${SA} `);
      const n = posts.length;
      await box("vertex-impersonate").press("Enter");
      const added = await posted(page, posts, "save", n);
      assert.equal(added.new, true);
      assert.equal(added.preset, "google-vertex");
      assert.deepEqual(added.vertex, { project: "My-Project-123", location: "us-central1", credentials: "~/keys/sa.json", impersonate: SA });
      assert.equal(added.key, "");

      // another preset's editor asks for its key, and none of these
      await page.locator(".editor.new").waitFor({ state: "detached" });
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Google Gemini"]').click();
      await ed.locator(".ehead b", { hasText: "Google Gemini" }).waitFor();
      assert.equal(await ed.locator("input[type=password]").count(), 1);
      assert.equal(await ed.locator(FIELDS.map((c) => "input." + c).join(", ")).count(), 0);

      for (const l of ["zh", "ja", "de"]) {
        const missing = await page.evaluate(([l, keys]) => keys.filter((k) => !I18N[l][k]), [l, STRINGS]);
        assert.deepEqual(missing, [], `every string has its ${l}`);
      }
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a saved Vertex AI provider's project is shown, tested and saved`, async (t) => {
      const { page, errors, posts } = await start(t, lang, "edit", [saved, adc, relay], presets(true));
      let ed = await edit(page, "google-vertex");
      assert.equal(await ed.locator("input[type=password]").count(), 0);
      assert.deepEqual(await values(ed), ["my-project-123", "us", "~/keys/vertex-sa.json", SA]);
      assert.deepEqual(await endpointsOf(ed), [["Gemini", "https://aiplatform.us.rep.googleapis.com/v1/projects/my-project-123/locations/us"]]);
      assert.equal(await ed.locator(".eps .ep .pl").getAttribute("title"), w.gemini);
      // the headers Vertex AI's pay-as-you-go tiers are asked with, a click away
      const hints = await ed.locator(".headers .hadds button").allTextContents();
      assert(hints.includes("+ X-Vertex-AI-LLM-Request-Type") && hints.includes("+ X-Vertex-AI-LLM-Shared-Request-Type"), hints.join(" | "));
      const bar = ed.locator(".bar");
      assert.equal(await bar.getByRole("button", { name: w.another, exact: true }).getAttribute("title"), w.more);
      assert.equal(await bar.getByRole("button", { name: w.dup, exact: true }).getAttribute("title"), w.copy);

      // a click in a field leaves the page where it is; the address
      // follows the location typed
      const box = (cls) => ed.locator("input." + cls);
      await click(page, box("vertex-location"));
      await box("vertex-location").fill("eu");
      assert.deepEqual(await endpointsOf(ed), [["Gemini", "https://aiplatform.eu.rep.googleapis.com/v1/projects/my-project-123/locations/eu"]]);
      await click(page, box("vertex-impersonate"));
      await box("vertex-impersonate").fill("");

      // Test asks with what is typed, before any Save
      const n = posts.length;
      await click(page, ed.locator(".eps button.action"));
      const tested = await posted(page, posts, "test", n);
      assert.equal(tested.id, "google-vertex");
      assert.equal(tested.typed, true);
      assert.deepEqual(tested.vertex, { project: "my-project-123", location: "eu", credentials: "~/keys/vertex-sa.json", impersonate: "" });
      assert(!tested.key);
      await ed.locator(".eps .ep .res.ok").waitFor();
      assert.equal(await ed.locator(".eps .ep .res.ok").textContent(), w.ms);

      // a header hint adds its header, its value typed next
      await ed.locator(".headers .hadds button", { hasText: "+ X-Vertex-AI-LLM-Request-Type" }).click();
      await page.waitForFunction(() => !!document.activeElement?.closest(".headers"));
      await page.keyboard.type("shared");
      const body = await press(page, posts, w.save);
      assert.equal(body.id, "google-vertex");
      assert.equal(body.preset, "google-vertex");
      assert.equal(body.key, "");
      assert.deepEqual(body.vertex, { project: "my-project-123", location: "eu", credentials: "~/keys/vertex-sa.json", impersonate: "" });
      assert.deepEqual(body.headers, { "X-Vertex-AI-LLM-Request-Type": "shared" });

      // Duplicate: a new one on the same project and credentials
      await page.locator(".editor").waitFor({ state: "detached" });
      ed = await edit(page, "google-vertex");
      await ed.locator(".bar").getByRole("button", { name: w.dup, exact: true }).click();
      await page.locator(".editor.new input.vertex-project").waitFor();
      assert.deepEqual(await values(page.locator(".editor.new")), ["my-project-123", "us", "~/keys/vertex-sa.json", SA]);
      const copy = await press(page, posts, w.add);
      assert.equal(copy.new, true);
      assert.equal(copy.copyOf, "google-vertex");
      assert.deepEqual(copy.vertex, { project: "my-project-123", location: "us", credentials: "~/keys/vertex-sa.json", impersonate: SA });

      // another saved one's editor has its own
      await page.locator(".editor").waitFor({ state: "detached" });
      ed = await edit(page, "google-vertex-2");
      assert.deepEqual(await values(ed), ["team-prod", "eu", "", ""]);
      assert.deepEqual(await endpointsOf(ed), [["Gemini", "https://aiplatform.eu.rep.googleapis.com/v1/projects/team-prod/locations/eu"]]);

      // its project taken out: no address and no Test, and a Save is
      // refused, nothing sent
      await box("vertex-project").fill("  ");
      assert.equal(await ed.locator("label", { hasText: new RegExp("^" + w.endpoints + "$") }).isVisible(), false);
      assert.equal(await ed.locator(".eps button.action").isVisible(), false);
      assert.equal(await press(page, posts, w.save, "save", 400), null);
      assert.equal(await ed.locator(".editor-error").textContent(), w.needed);
      assert(await focused(page, "vertex-project"));

      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Vertex AI row names its Google credentials, never a missing key`, async (t) => {
      const { page, errors } = await start(t, lang, "rows", [saved, adc, file, bare, relay], presets(true));
      await page.locator(".row.provider .key").first().waitFor();
      const pills = await page.evaluate(() => Object.fromEntries([...document.querySelectorAll(".row.provider")].map((r) => {
        const k = r.querySelector(".key"), s = k.firstElementChild;
        return [r.dataset.id, { cls: k.className, text: k.textContent, title: k.title, cut: s.scrollWidth > s.clientWidth }];
      })));
      // the service account it acts as, too long for the pill and said whole on hover
      assert.deepEqual(pills["google-vertex"], { cls: "key acct", text: SA, title: `${w.as} · ~/keys/vertex-sa.json`, cut: true });
      assert.deepEqual(pills["google-vertex-2"], { cls: "key acct", text: w.adc, title: `${w.adc} · ${w.signed} · ${w.adcLong}`, cut: false });
      assert.deepEqual(pills["google-vertex-3"], { cls: "key acct", text: "dev-sa.json", title: `${w.signed} · ~/keys/dev-sa.json`, cut: false });
      assert.deepEqual(pills["google-vertex-4"], { cls: "key none", text: w.needsProject, title: `${w.needsProject} · ${w.open}`, cut: false });
      assert.equal(pills.relay.text, "sk-…one");
      for (const id of ["google-vertex", "google-vertex-2", "google-vertex-3", "google-vertex-4"]) assert(!w.noKey.includes(pills[id].text), id);
      assert.deepEqual(errors, []);
    });
  }

  // every language, German's the longest: the pills say it whole, but for
  // a service account's email, longer than any pill holds, and the fields fit
  for (const lang of ["en", "zh", "ja", "de"]) for (const web of [false, true]) {
    test(`${engine} ${lang}: Vertex AI's pills and fields fit a narrow ${web ? "browser" : "window"}`, async (t) => {
      const { page, errors } = await start(t, lang, web ? "narrow-web" : "narrow", [saved, adc, bare, relay], presets(true), { web, width: 560 });
      await page.locator(".row.provider .key").first().waitFor();
      const cut = await page.evaluate(() => [...document.querySelectorAll(".row.provider .key > span")].filter((s) => s.scrollWidth > s.clientWidth).map((s) => s.textContent));
      assert.deepEqual(cut, [SA], "the pills cut");
      const ed = await edit(page, "google-vertex");
      for (const width of [560, 440]) {
        await page.setViewportSize({ width, height: 760 });
        await page.waitForTimeout(150);
        const bad = await ed.evaluate((ed, fields) => {
          const box = ed.getBoundingClientRect(), out = [];
          const inside = (e, what) => {
            const r = e.getBoundingClientRect();
            if (!r.width || r.left < box.left - 0.5 || r.right > box.right + 0.5) out.push(`${what} outside the editor: ${r.left}–${r.right} of ${box.left}–${box.right}`);
            return r;
          };
          for (const cls of fields) {
            const i = ed.querySelector("input." + cls), l = ed.querySelector(`label[for="${i.id}"]`), h = i.parentElement.querySelector(".hint");
            const ir = inside(i, cls), lr = inside(l, cls + "'s label");
            inside(h, cls + "'s hint");
            if (l.scrollWidth > l.clientWidth || l.scrollHeight > l.clientHeight) out.push(`${cls}'s label cut: ${l.textContent}`);
            if (h.scrollWidth > h.clientWidth) out.push(`${cls}'s hint cut`);
            if (lr.right > ir.left && lr.bottom > ir.top && lr.top < ir.bottom) out.push(`${cls}'s label over its box`);
            if (ir.width < 140) out.push(`${cls} ${ir.width}px wide`);
          }
          inside(ed.querySelector(".eps .ep"), "the endpoint");
          if (document.scrollingElement.scrollWidth > innerWidth) out.push("the page scrolls sideways");
          return out;
        }, FIELDS);
        assert.deepEqual(bad, [], `${width}px`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
