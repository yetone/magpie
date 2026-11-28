// Run with Node's test runner and Playwright on the module path; see README.md.
// Who GitHub sync's commits are by (IceCodeNew, #4579): under Advanced in
// Sync's form, GitHub only, a name and an email, GitHub Actions' bot until
// another is named, so no sync counts in the token owner's contributions.
// - a setup from before the choice: Advanced closed, the default shown,
//   no Use default, and Save posts no gitName or gitEmail;
// - an address that isn't one, a name without an email, or a name with <
//   in it is refused before anything is posted; a GitHub App's bot is
//   posted as typed;
// - opened again: Advanced open with the bot shown; Use default puts the
//   default back and Save posts both empty, to drop the bot;
// - WebDAV hides the row.
// In all five languages, at 1100 and 420px, Chromium and WebKit, nothing
// wider than the window. The API is faked; nothing reaches GitHub.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, openPart } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, vars = {}) => {
  const s = lang === "en" ? k : I18N[lang][k];
  assert.equal(typeof s, "string", `${lang} has ${JSON.stringify(k)}`);
  return s.replace(/\{(\w+)\}/g, (_, v) => vars[v]);
};

const bot = { name: "github-actions[bot]", email: "41898282+github-actions[bot]@users.noreply.github.com" };
const app = { name: "magpie-sync[bot]", email: "123456+magpie-sync[bot]@users.noreply.github.com" };
const base = { on: true, kind: "github", url: "github://alice/private/magpie-sync", passwordSet: true, passphraseSet: true,
  keys: true, agents: true, library: true, memory: true, commitDefault: bot, last: new Date(Date.now() - 60000).toISOString() };

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { ...base };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname === "/api/davsync/save") {
      const b = request.postDataJSON();
      posts.push(b);
      sync = { ...sync, gitName: b.gitName ?? sync.gitName, gitEmail: b.gitEmail ?? sync.gitEmail };
      if (!sync.gitName) delete sync.gitName, delete sync.gitEmail;
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function noneWider(page) {
  const wide = await page.evaluate(() => {
    const out = [];
    if (document.scrollingElement.scrollWidth > innerWidth) out.push("page " + document.scrollingElement.scrollWidth);
    for (const r of document.querySelectorAll("#syncList .sync-form, #syncList .sync-commit-as, #syncList .sync-commit-as input, #syncList .sync-commit-say")) {
      const b = r.getBoundingClientRect();
      if (b.right > innerWidth + 0.5) out.push(r.className + " " + b.right);
    }
    return out;
  });
  assert.deepEqual(wide, [], "wider than the window");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = (k, vars) => tr(lang, k, vars);
      test(`${engine} ${width}px ${lang}: who GitHub sync commits as`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-sync-commit-as.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const waitPost = async (n) => {
          for (let i = 0; i < 250 && posts.length < n; i++) await page.waitForTimeout(20);
          assert.equal(posts.length, n, "saves posted");
          return posts[n - 1];
        };
        const click = async (loc) => { await reach(page, loc); await loc.click(); };
        const exact = (s) => new RegExp("^" + s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$");

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const first = list.locator(".row.pref").first();
        await first.waitFor({ state: "visible" });
        const edit = async () => {
          await click(first.locator("button", { hasText: exact(w("Edit")) }));
          return list.locator(".sync-form");
        };

        // a setup from before the choice: the default, folded away, not posted
        let form = await edit();
        const fold = form.locator("details.sync-advanced");
        const row = form.locator(".sync-commit-as");
        const name = row.locator("input").first(), email = row.locator("input[type=email]");
        const reset = row.locator("button.sync-commit-default");
        const err = form.locator(".editor-error");
        assert.equal(await fold.evaluate((d) => d.open), false, "Advanced folded with the default");
        assert.equal(await row.isVisible(), false);
        await click(fold.locator("> summary"));
        await row.waitFor({ state: "visible" });
        assert.equal(await row.locator("> span").textContent(), w("Commit as"));
        assert.equal(await name.inputValue(), bot.name);
        assert.equal(await email.inputValue(), bot.email);
        assert.equal(await name.getAttribute("aria-label"), w("Name"));
        assert.equal(await email.getAttribute("aria-label"), w("Email"));
        assert.equal(await reset.isVisible(), false, "Use default with the default");
        assert.equal(await form.locator(".sync-commit-say").textContent(), w("A GitHub App's bot: <slug>[bot] and <app id>+<slug>[bot]@users.noreply.github.com"));
        await noneWider(page);
        await click(form.locator("button.primary"));
        const first1 = await waitPost(1);
        assert.ok(!("gitName" in first1) && !("gitEmail" in first1), "the default posted as a choice: " + JSON.stringify(first1));
        await form.waitFor({ state: "detached" });

        // refused before posting; a GitHub App's bot posted
        form = await edit();
        await click(fold.locator("> summary"));
        await name.fill(app.name);
        await email.fill("nope");
        await reset.waitFor({ state: "visible" });
        await click(form.locator("button.primary"));
        await page.waitForFunction((m) => document.querySelector("#syncList .sync-form .editor-error")?.textContent === m, w("The email commits are made as must be an address"));
        await email.fill("");
        await click(form.locator("button.primary"));
        await page.waitForFunction((m) => document.querySelector("#syncList .sync-form .editor-error")?.textContent === m, w("Give the name and the email commits are made as, or Use default"));
        await name.fill("Ada <x>");
        await email.fill(app.email);
        await click(form.locator("button.primary"));
        await page.waitForFunction((m) => document.querySelector("#syncList .sync-form .editor-error")?.textContent === m, w("The name commits are made as can't have < or > in it"));
        assert.equal(posts.length, 1, "a refused identity was posted");
        await name.fill(app.name);
        await noneWider(page);
        await click(form.locator("button.primary"));
        const second = await waitPost(2);
        assert.equal(second.gitName, app.name);
        assert.equal(second.gitEmail, app.email);
        await form.waitFor({ state: "detached" });

        // opened again: shown; Use default drops it
        form = await edit();
        assert.equal(await fold.evaluate((d) => d.open), true, "Advanced open with a bot named");
        await row.waitFor({ state: "visible" });
        assert.equal(await name.inputValue(), app.name);
        assert.equal(await email.inputValue(), app.email);
        await click(reset);
        assert.equal(await name.inputValue(), bot.name);
        assert.equal(await email.inputValue(), bot.email);
        await reset.waitFor({ state: "hidden" });
        assert.equal(await err.textContent(), "");
        await click(form.locator("button.primary"));
        const third = await waitPost(3);
        assert.equal(third.gitName, "");
        assert.equal(third.gitEmail, "");
        await form.waitFor({ state: "detached" });

        // WebDAV makes no commits
        form = await edit();
        const hidden = () => Promise.all([row, form.locator(".sync-commit-say")].map((l) => l.evaluate((e) => e.hidden)));
        assert.deepEqual(await hidden(), [false, false]);
        await click(form.locator(".segs .opt", { hasText: "WebDAV" }));
        assert.deepEqual(await hidden(), [true, true]);
        await click(form.locator(".segs .opt", { hasText: "GitHub" }));
        assert.deepEqual(await hidden(), [false, false]);
        await click(first.locator("button", { hasText: exact(w("Close")) }));
        await form.waitFor({ state: "detached" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
