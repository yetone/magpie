// Run with Node's test runner and Playwright on the module path; see README.md.
// Sign-ins in sync and backups (qianxunji and curtinp118 on Discord: a VPS
// restored from a backup had to sign in to every subscription again). Each
// place that can carry them has them off until picked, and says what
// picking them lets in:
// - Sync's form: the Sign-ins tick, off; ticked, the line under it shows,
//   and Save posts signIns true. Opened again, it is ticked.
// - Export: the tick, off; ticked, the passphrase's line says the file
//   holds them, and Export posts signIns true.
// - Import: the tick, on (a file has them only when made with them); the
//   result says how many were added, updated and kept as they are here.
// - Back up by itself: Sign-ins in backups, Off; On with no passphrase set
//   asks for one (twice, the same), posting it with signIns true; the
//   head line and each version that has them say so, and a restore says
//   what its sign-ins did, or that another passphrase sealed them.
// In all five languages, at 1100 and 420px, Chromium and WebKit, nothing
// wider than the window. The API is faked; nothing is synced or backed up.
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

const hourAgo = new Date(Date.now() - 3600e3).toISOString();
const base = { on: true, kind: "webdav", url: "https://dav.example.invalid/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, last: new Date(Date.now() - 60000).toISOString() };
const backups = [{ name: "magpie-20261102-010000-auto.json", made: hourAgo, reason: "auto", providers: 3, size: 900, signIns: true }];
const signInsResult = { added: 2, updated: 1, kept: 1, codex: true };

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { ...base };
  let auto = { on: true, hours: 24, keep: 7, dir: "/home/me/auto-backups", custom: false, backups };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    const post = () => { const body = request.postDataJSON(); posts.push({ path: pathname, body }); return body; };
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname === "/api/davsync/save") {
      const b = post();
      sync = { ...sync, signIns: !!b.signIns };
      return json(sync);
    }
    if (pathname === "/api/backup/export") { post(); return json({ path: "/home/me/magpie.magpie-backup", signIns: 3 }); }
    if (pathname === "/api/backup/import") { post(); return json({ Added: 1, Replaced: 0, Profiles: 0, Agents: 0, SignIns: signInsResult }); }
    if (pathname === "/api/autobackup") return json(auto);
    if (pathname.startsWith("/api/autobackup/")) {
      const b = post();
      if (pathname.endsWith("/set") && b.signIns !== undefined) auto = { ...auto, signIns: b.signIns, passphrase: auto.passphrase || !!b.passphrase };
      if (pathname.endsWith("/restore")) return json({ view: auto, before: "", result: { Added: 0, Replaced: 1, Profiles: 0, Agents: 0, SignInsLocked: true } });
      return json(auto);
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
    for (const r of document.querySelectorAll("#syncList .row.pref, #syncList .sync-form")) {
      const b = r.getBoundingClientRect();
      if (b.right > innerWidth + 0.5) out.push((r.querySelector(".name")?.textContent || r.className) + " " + b.right);
    }
    return out;
  });
  assert.deepEqual(wide, [], "wider than the window");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = (k, vars) => tr(lang, k, vars);
      test(`${engine} ${width}px ${lang}: sign-ins go only when picked, and each place says what they let in`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-sync-signins.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const posted = (p) => posts.filter((x) => x.path === p).at(-1)?.body;
        const waitPost = async (p) => {
          for (let i = 0; i < 250 && !posted(p); i++) await page.waitForTimeout(20);
          return posted(p);
        };
        const click = async (loc) => { await reach(page, loc); await loc.click(); };

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const exact = (s) => new RegExp("^" + s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$");
        const rowNamed = (name) => list.locator(".row.pref").filter({ has: page.locator(".name", { hasText: exact(name) }) });
        const first = list.locator(".row.pref").first();
        await first.waitFor({ state: "visible" });
        assert.ok(!(await first.textContent()).includes(w("sign-ins")), "sign-ins named while off");

        // Sync's form
        await click(first.locator("button", { hasText: exact(w("Edit")) }));
        let form = list.locator(".sync-form");
        const siTick = form.locator("label.tick", { hasText: w("Sign-ins: subscription accounts and plugin sign-ins") }).locator("input");
        assert.equal(await siTick.isChecked(), false, "sync's sign-ins on before picked");
        const say = form.locator(".sync-signins-say");
        assert.equal(await say.isVisible(), false, "the line shows before the tick");
        await click(siTick);
        await say.waitFor({ state: "visible" });
        assert.equal(await say.textContent(), w("Another computer that syncs is signed in without signing in again. Anyone with the passphrase and the server's file can use these accounts. An account used on two computers can be signed out on one when the other renews it."));
        await noneWider(page);
        await click(form.locator("button.primary"));
        assert.equal((await waitPost("/api/davsync/save"))?.signIns, true);
        await form.waitFor({ state: "detached" });
        // opened again, it shows them picked and what that lets in
        await click(first.locator("button", { hasText: exact(w("Edit")) }));
        form = list.locator(".sync-form");
        assert.equal(await siTick.isChecked(), true, "sign-ins not shown as picked");
        assert.equal(await form.locator(".sync-signins-say").isVisible(), true);
        await click(first.locator("button", { hasText: exact(w("Close")) }));
        await form.waitFor({ state: "detached" });

        // Export
        const exportRow = rowNamed(w("Export"));
        await click(exportRow.locator("button"));
        form = list.locator(".sync-form");
        const exTick = form.locator("label.tick", { hasText: w("With the sign-ins: subscription accounts and plugin sign-ins") }).locator("input");
        assert.equal(await exTick.isChecked(), false, "export's sign-ins on before picked");
        const passHint = form.locator(".hint", { hasText: w("Needed to open the file. Subscriptions aren't in it: sign in to them on the other computer.") });
        await passHint.waitFor();
        await click(exTick);
        await form.locator(".hint", { hasText: w("Needed to open the file. The sign-ins are in it: anyone with the file and the passphrase can use those accounts, so pick a strong one.") }).waitFor();
        const [p1, p2] = [form.locator("input[type=password]").nth(0), form.locator("input[type=password]").nth(1)];
        await p1.fill("correct horse");
        await p2.fill("correct horse");
        await noneWider(page);
        await click(form.locator("button.primary"));
        assert.deepEqual(await waitPost("/api/backup/export"), { pass: "correct horse", keys: true, library: true, signIns: true });
        await form.locator(".done").waitFor();
        await click(exportRow.locator("button"));

        // Import
        await click(rowNamed(w("Import")).locator("button"));
        form = list.locator(".sync-form");
        const imTick = form.locator("label.tick", { hasText: w("Bring in the sign-ins too, when the file has them") }).locator("input");
        assert.equal(await imTick.isChecked(), true);
        await form.locator("input[type=file]").setInputFiles({ name: "magpie.magpie-backup", mimeType: "application/octet-stream", buffer: Buffer.from("sealed") });
        await form.locator(".fname", { hasText: "magpie.magpie-backup" }).waitFor();
        await form.locator("input[type=password]").fill("correct horse");
        await click(form.locator("button.primary"));
        assert.equal((await waitPost("/api/backup/import"))?.signIns, true);
        const done = form.locator(".done");
        await done.first().waitFor();
        const lines = await done.allTextContents();
        for (const l of [w("Sign-ins: {added} added, {updated} updated, {kept} kept as they are here", { added: 2, updated: 1, kept: 1 }),
          w("One kept as it is here was renewed later here, or is the account the agent itself is signed in with."),
          w("Codex is signed in with the backup's account.")]) assert.ok(lines.includes(l), `${l} in ${lines.join(" | ")}`);
        await noneWider(page);
        await click(rowNamed(w("Import")).locator("button"));

        // Back up by itself
        const auto = rowNamed(w("Back up by itself"));
        await auto.waitFor();
        assert.equal(await auto.locator(".who > .sub").textContent(), w("Your providers, settings and profiles, without API keys or sign-ins, into a folder on this computer"));
        const siRow = rowNamed(w("Sign-ins in backups"));
        assert.equal(await siRow.locator(".who > .sub").textContent(), w("Off: after a restore on another computer, sign in again"));
        assert.equal(await siRow.locator(".segs .opt.on").textContent(), w("Off"));
        await click(siRow.locator(".segs .opt", { hasText: exact(w("On")) }));
        const ask = list.locator(".auto-backup-phrase");
        await ask.waitFor();
        assert.equal(posts.filter((p) => p.path === "/api/autobackup/set").length, 0, "turned on before a passphrase");
        await ask.locator("input[type=password]").nth(0).fill("pp");
        await ask.locator("input[type=password]").nth(1).fill("other");
        await click(ask.locator("button.primary"));
        await ask.locator(".editor-error", { hasText: w("The two passphrases differ") }).waitFor();
        await ask.locator("input[type=password]").nth(1).fill("pp");
        await noneWider(page);
        await click(ask.locator("button.primary"));
        assert.deepEqual(await waitPost("/api/autobackup/set"), { signIns: true, passphrase: "pp" });
        await page.waitForFunction((s) => [...document.querySelectorAll("#syncList .auto-backup .sub")].some((x) => x.textContent === s),
          w("Your providers, settings and profiles, without API keys, and the sign-ins sealed with a passphrase, into a folder on this computer"));
        assert.equal(await ask.count(), 0);
        assert.equal(await rowNamed(w("Sign-ins in backups")).locator(".who > .sub").textContent(), w("Subscription accounts and plugin sign-ins, sealed with the passphrase set here"));
        // the versions are folded under Versions until it is clicked
        await click(rowNamed(w("Versions")).locator(".auto-backup-fold"));
        const version = list.locator(".auto-backup-version").first();
        assert.ok((await version.locator(".sub").textContent()).includes(w("sign-ins")));
        // a restore: asked, then says the sign-ins were sealed with another
        await click(version.locator("button"));
        const confirm = page.locator(".auto-backup-ask");
        await confirm.waitFor();
        assert.ok((await confirm.textContent()).includes(w("Its sign-ins are put in where they were renewed later than the ones here; none here is removed.")));
        await confirm.locator("button.primary").click();
        await page.locator(".auto-backup-said", { hasText: w("This backup's sign-ins are sealed with another passphrase, so they were left out.") }).first().waitFor();
        // Off again needs no passphrase
        await click(rowNamed(w("Sign-ins in backups")).locator(".segs .opt", { hasText: exact(w("Off")) }));
        for (let i = 0; i < 250 && posts.filter((p) => p.path === "/api/autobackup/set").length < 2; i++) await page.waitForTimeout(20);
        assert.deepEqual(posted("/api/autobackup/set"), { signIns: false });
        await noneWider(page);
        assert.deepEqual(errors, []);
      });
    }
  }
}
