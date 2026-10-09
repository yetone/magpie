// GitHub sync is offered beside WebDAV and S3. The form sends a repository
// address, optional branch/folder, a separate token and the normal passphrase.
// The API is faked; this test never contacts GitHub.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const translationSource = require("node:fs").readFileSync(path.join(assets, "i18n.js"), "utf8");
const dictionary = new Function(translationSource.slice(0, translationSource.indexOf("\n};") + 3) + "; return I18N;")();

function serve(lang, posts, refusal = "") {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { on: false, keys: true, agents: true, library: true };
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
      const body = request.postDataJSON();
      posts.push(body);
      sync = { on: true, kind: "github", url: body.url, branch: body.branch, passwordSet: true, passphraseSet: true,
        keys: body.keys, agents: body.agents, library: body.library, ...(refusal ? { error: refusal } : {}) };
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [900, 440]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang} ${width}px: GitHub sync settings`, { timeout: 60000 }, async (t) => {
      const tr = (key) => dictionary[lang]?.[key] || key;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 600 }, reducedMotion: "reduce" })).newPage();
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      page.setDefaultTimeout(5000);
      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      await page.locator("#setTab-sync").click();
      const list = page.locator("#syncList");
      const first = list.locator(".row.pref").first();
      await first.waitFor({ state: "visible" });
      const left = () => page.locator("#view-settings").evaluate((v) => v.scrollHeight - v.clientHeight - v.scrollTop);
      const toEnd = async () => {
        await page.mouse.move(width / 2, 300);
        for (let i = 0; i < 120 && (await left()) > 0.5; i++) {
          await page.mouse.wheel(0, 80);
          await page.waitForTimeout(15);
        }
        await page.waitForTimeout(300);
      };
      await toEnd();
      assert.equal(await first.locator(".name").textContent(), tr("WebDAV, S3 or GitHub sync"));
      await first.locator(".val button").click();
      const form = list.locator(".sync-form");
      await form.locator(".segs .opt", { hasText: "GitHub" }).click();
      const labels = (await form.locator(":scope > label:visible").allTextContents()).filter(Boolean);
      assert.deepEqual(labels, ["Sync to", "Repository", "Branch", "Folder", "GitHub token", "Passphrase", "Also sync"].map(tr));
      assert.ok((await form.textContent()).includes(tr("Empty uses the repository's default branch. Empty repositories are initialized by the first sync; other branches must already exist.")));
      const box = (label) => form.locator(":scope > label:visible", { hasText: new RegExp("^" + label + "$") }).locator("xpath=following-sibling::div[1]").locator("input");
      const L = { repo: tr("Repository"), folder: tr("Folder"), token: tr("GitHub token"), phrase: tr("Passphrase") };
      assert.equal(await form.locator("select").count(), 0);
      assert.ok(await form.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the form fits a narrow window");
      await toEnd();
      await form.locator(".bar .primary").click();
      assert.equal(await form.locator(".editor-error").textContent(), tr("Name the GitHub repository as owner/repo"));
      assert.equal(posts.length, 0, "an incomplete binding posts nothing");
      await box(L.repo).fill("alice/private");
      await box(L.folder).fill("magpie-sync");
      await box(L.token).fill("fixture-token");
      await box(L.phrase).fill("correct horse");
      await toEnd();
      await form.locator(".bar .primary").click();
      assert.deepEqual(posts, [{ url: "github://alice/private/magpie-sync", branch: "", password: "fixture-token", passphrase: "correct horse", keys: true, agents: true, library: true }]);
      await page.waitForFunction((name) => document.querySelector("#syncList .row.pref .name")?.textContent === name, tr("GitHub sync"));
      assert.equal(await first.locator(".name").textContent(), tr("GitHub sync"));
      await first.locator(".val button").last().click();
      assert.equal(await box(L.repo).inputValue(), "alice/private");
      assert.equal(await box(L.token).inputValue(), "", "the saved token never returns to the browser");
      assert.equal(await box(L.token).getAttribute("placeholder"), tr("saved · type a new one to replace it"));
    });
  }
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: an empty repository's branch refusal is actionable`, { timeout: 60000 }, async (t) => {
      const tr = (key) => dictionary[lang]?.[key] || key;
      const refusal = "The GitHub repository is empty: leave Branch empty or use its default branch for the first sync";
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 440, height: 600 }, reducedMotion: "reduce" });
      await page.route("**/*", serve(lang, [], refusal));
      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      await page.locator("#setTab-sync").click();
      const first = page.locator("#syncList .row.pref").first();
      await first.locator(".val button").click();
      const form = page.locator("#syncList .sync-form");
      await form.locator(".segs .opt", { hasText: "GitHub" }).click();
      const box = (label) => form.locator(":scope > label:visible", { hasText: new RegExp("^" + tr(label) + "$") }).locator("xpath=following-sibling::div[1]").locator("input");
      await box("Repository").fill("alice/private");
      await box("Branch").fill("other");
      await box("GitHub token").fill("fixture-token");
      await box("Passphrase").fill("correct horse");
      await form.locator(".bar .primary").click();
      await page.waitForFunction((message) => document.querySelector("#syncList .row.pref .sub")?.textContent.includes(message), tr(refusal));
      assert.equal(await first.locator(".sub").textContent(), tr("Couldn't sync: {error}").replace("{error}", tr(refusal)));
      assert.equal(await first.locator(".name").textContent(), tr("GitHub sync"));
    });
  }
}
