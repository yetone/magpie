// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Sync's Back up by itself (jwaitq on Discord: 定期自动备份，支持
// 回退选择版本). Its rows come after Export and Import, in the list's own
// rows (a line between each): the interval and Versions kept as the app's
// segmented control (no <select>), the folder with Change…, Show and, once
// one is picked, Default, Back up now, and each version with Restore….
// Picking Off posts on:false and the row says none are made; 1 week posts
// hours 168; 14 posts keep 14; Default posts dir "". Restore… asks in the
// app's own dialog, saying what is here is backed up first; Cancel posts
// nothing, Restore posts the version's name, and the list then says what
// was put back and which version undoes it. In all five languages, at 1100
// and 420px, Chromium and WebKit, with nothing wider than the window. The
// API is faked; nothing is backed up.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, vars = {}) => {
  const s = lang === "en" ? k : I18N[lang][k];
  assert.equal(typeof s, "string", `${lang} has ${JSON.stringify(k)}`);
  return s.replace(/\{(\w+)\}/g, (_, v) => vars[v]);
};

const hourAgo = new Date(Date.now() - 3600e3).toISOString();
const dayAgo = new Date(Date.now() - 86400e3).toISOString();
const backups = [
  { name: "magpie-20261102-010000-auto.json", made: hourAgo, reason: "auto", providers: 3, size: 900 },
  { name: "magpie-20261101-010000-restore.json", made: dayAgo, reason: "restore", providers: 1, size: 700 },
];

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let view = { on: true, hours: 24, keep: 7, dir: "~/Library/Application Support/magpie/auto-backups-with-a-long-folder-name-to-wrap", custom: true, backups };
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
    if (pathname === "/api/davsync") return json({ on: false });
    if (pathname === "/api/autobackup") return json(view);
    if (pathname.startsWith("/api/autobackup/")) {
      const action = pathname.slice("/api/autobackup/".length);
      const body = request.postDataJSON();
      posts.push({ action, body });
      if (action === "set") {
        if (body.on !== undefined) view = { ...view, on: body.on };
        if (body.hours) view = { ...view, hours: body.hours };
        if (body.keep) view = { ...view, keep: body.keep };
        if (body.dir === "") view = { ...view, custom: false, dir: "~/Library/Application Support/magpie/auto-backups" };
      }
      if (action === "restore") {
        const before = { name: "magpie-20261102-020000-restore.json", made: new Date().toISOString(), reason: "restore", providers: 3, size: 900 };
        view = { ...view, backups: [before, ...view.backups] };
        return json({ view, before: before.name, result: { Added: 0, Replaced: 2, Profiles: 1, Agents: 0, Settings: true } });
      }
      return json(view);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = (k, vars) => tr(lang, k, vars);
      test(`${engine} ${width}px ${lang}: Back up by itself is set, and a version restored after asking`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-auto-backup.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const exact = (s) => new RegExp("^" + s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$");
        const rowNamed = (name) => list.locator(".row.pref").filter({ has: page.locator(".name", { hasText: exact(name) }) });
        const auto = rowNamed(w("Back up by itself"));
        await auto.waitFor({ state: "visible" });
        const sub = (r) => r.locator(".who > .sub").first().textContent();

        // after Export and Import, as rows of the list's own
        const names = await list.locator(":scope > .row.pref > .who > .name").allTextContents();
        assert.ok(names.indexOf(w("Back up by itself")) > names.indexOf(w("Import")), names.join(" | "));
        assert.equal(await list.locator(":scope > div:not(.row):not(.editor)").count(), 0, "no wrapper left in the list");
        assert.equal(await list.locator("select").count(), 0, "no native select");
        assert.equal(await sub(auto), w("Your providers, settings and profiles, without API keys or sign-ins, into a folder on this computer"));
        assert.equal(await auto.locator(".segs .opt.on").textContent(), w("1 day"));
        const kept = rowNamed(w("Versions kept"));
        assert.equal(await kept.locator(".segs .opt.on").textContent(), "7");
        const folder = rowNamed(w("Backup folder"));
        assert.match(await sub(folder), /auto-backups-with-a-long/);
        for (const b of ["Change…", "Show", "Default"]) await folder.locator("button", { hasText: w(b) }).waitFor();
        const versions = rowNamed(w("Versions"));
        assert.ok((await sub(versions)).startsWith(w("{n} backups · newest {when}", { n: 2, when: "" }).trim().replace(/\s*$/, "")), await sub(versions));
        assert.equal(await list.locator(".auto-backup-version").count(), 2);
        assert.ok((await sub(list.locator(".auto-backup-version").nth(1))).startsWith(w("What was here before a restore")));
        assert.ok((await sub(list.locator(".auto-backup-version").first())).includes(w("{n} providers", { n: 3 })));

        // nothing wider than the window, the long folder wrapping
        const wide = await page.evaluate(() => {
          const out = [];
          if (document.scrollingElement.scrollWidth > innerWidth) out.push("page " + document.scrollingElement.scrollWidth);
          for (const r of document.querySelectorAll("#syncList .row.pref")) {
            const b = r.getBoundingClientRect();
            if (b.right > innerWidth + 0.5 || r.scrollWidth > r.clientWidth + 1) out.push(r.querySelector(".name")?.textContent + " " + b.right + "/" + r.scrollWidth + ">" + r.clientWidth);
          }
          return out;
        });
        assert.deepEqual(wide, [], "rows wider than the window");

        // the interval, Off, the count, the default folder
        await auto.locator(".segs .opt", { hasText: w("1 week") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#syncList .row.pref.auto-backup").length > 0);
        assert.deepEqual(posts.at(-1), { action: "set", body: { on: true, hours: 168 } });
        await rowNamed(w("Versions kept")).locator(".segs .opt", { hasText: "14" }).click();
        await page.waitForFunction(() => [...document.querySelectorAll("#syncList .auto-backup .segs .opt.on")].some((o) => o.textContent === "14"));
        assert.deepEqual(posts.at(-1), { action: "set", body: { keep: 14 } });
        await rowNamed(w("Backup folder")).locator("button", { hasText: w("Default") }).click();
        await page.waitForFunction(() => [...document.querySelectorAll("#syncList .auto-backup-dir")].some((s) => s.textContent.endsWith("/auto-backups")));
        assert.deepEqual(posts.at(-1), { action: "set", body: { dir: "" } });
        assert.equal(await rowNamed(w("Backup folder")).locator("button", { hasText: w("Default") }).count(), 0, "Default is gone with the default folder");
        await rowNamed(w("Back up by itself")).locator(".segs .opt", { hasText: w("Off") }).click();
        await page.waitForFunction((s) => [...document.querySelectorAll("#syncList .auto-backup .sub")].some((x) => x.textContent === s), w("Off: no backups are made by themselves"));
        assert.deepEqual(posts.at(-1), { action: "set", body: { on: false } });
        assert.equal(await rowNamed(w("Versions kept")).count(), 0, "no count while off");
        // off, the versions there are still listed to restore
        assert.equal(await list.locator(".auto-backup-version").count(), 2);
        await rowNamed(w("Versions")).locator("button", { hasText: w("Back up now") }).click();
        for (let i = 0; i < 250 && !posts.some((p) => p.action === "now"); i++) await page.waitForTimeout(20);
        assert.deepEqual(posts.at(-1), { action: "now", body: {} });

        // Restore asks first; Cancel posts nothing
        const restores = () => posts.filter((p) => p.action === "restore").length;
        await list.locator(".auto-backup-version").first().locator("button", { hasText: w("Restore…") }).click();
        const ask = page.locator(".auto-backup-ask");
        await ask.waitFor({ state: "visible" });
        assert.ok((await ask.textContent()).includes(w("What is here now is backed up first: restore that one to undo this.")));
        await ask.locator("button", { hasText: w("Cancel") }).click();
        await ask.waitFor({ state: "detached" });
        assert.equal(restores(), 0);
        await list.locator(".auto-backup-version").first().locator("button", { hasText: w("Restore…") }).click();
        await ask.waitFor({ state: "visible" });
        await ask.locator("button.primary").click();
        await page.locator(".auto-backup-said").first().waitFor({ state: "visible" });
        assert.deepEqual(posts.filter((p) => p.action === "restore"), [{ action: "restore", body: { name: backups[0].name } }]);
        const said = (await page.locator(".auto-backup-said").allTextContents()).join("\n");
        assert.ok(said.includes(w("Providers: {added} added, {replaced} replaced; {profiles} profiles; {agents} agent settings changed", { added: 0, replaced: 2, profiles: 1, agents: 0 })), said);
        const undo = lang === "en" ? "What was here before is the version of {when}: restore it to undo." : I18N[lang]["What was here before is the version of {when}: restore it to undo."];
        for (const part of undo.split("{when}")) assert.ok(said.includes(part), part + " in " + said);
        assert.equal(await list.locator(".auto-backup-version").count(), 3, "the backup made first is listed, to undo with");
        assert.deepEqual(errors, []);
      });
    }
  }
}

// Sync drawn again while the first read of Back up by itself is still out
// (a sync's tick, a form opened): its rows are drawn when the read comes
// back, not left out until the next read half a minute on, which also
// shortened the page under a reader scrolled to its end.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Sync redrawn while the backups are being read still shows them`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let release;
    const held = new Promise((r) => { release = r; });
    const inner = serve("en", []);
    await page.route("**/*", async (route) => {
      if (new URL(route.request().url()).pathname === "/api/autobackup") await held;
      return inner(route);
    });
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.goto("http://magpie.test/");
    await page.locator("#prefs").click();
    await openPart(page, "sync");
    await page.locator("#syncList .row.pref").first().waitFor({ state: "visible" });
    await page.waitForFunction(() => typeof window.autoBackupFill === "function");
    await page.evaluate(() => { renderSync(); renderSync(); });
    release();
    await page.locator("#syncList .row.pref.auto-backup-version").first().waitFor({ state: "visible" });
    assert.equal(await page.locator("#syncList .row.pref.auto-backup-version").count(), 2);
    assert.equal(await page.locator("#syncList .row.pref.auto-backup").filter({ has: page.locator(".name", { hasText: /^Back up by itself$/ }) }).count(), 1);
    assert.deepEqual(errors, []);
  });
}
