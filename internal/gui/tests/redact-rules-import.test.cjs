// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's Masking rules take many rules at once (hgk0x on X:
// with many fields to mask, adding rules one by one was slow). Import…
// beside the row's words opens a box under its fields; what is pasted
// there, a rule a line (a name, then a tab, =, : or comma, then the
// pattern; a bare pattern is CUSTOM), each pattern a regex or a prefix as
// the box's own choice says, is listed as it was read and added after the
// rules the list has, one it has or pasted twice passed over, in one save.
// A redraw keeps the box, what it holds and its choice; Escape and Cancel
// save nothing; a list magpie refuses is said in the box and the paste is
// kept. Copy writes the rules as JSON, which Import takes back as they
// were. Every language, Chromium and WebKit, narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, box) {
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: box.settings });
    if (url.pathname === "/api/settings") return json(box.settings);
    if (url.pathname === "/api/settings/redact-rules") {
      const rules = req.postDataJSON().rules;
      box.saved.push(rules);
      if (box.fail) return r.fulfill({ status: 400, json: { error: box.fail } });
      box.settings = { ...box.settings, redactRules: rules };
      return json(box.settings);
    }
    if (url.pathname === "/api/copy") {
      box.copied.push(req.postDataJSON().text);
      return json({});
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const ROW = "#redactList .row.rule-bulk";
const until = async (ok, what) => {
  for (let n = 0; n < 100 && !ok(); n++) await new Promise((r) => setTimeout(r, 50));
  assert(ok(), what);
};
const said = (page, s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
const preview = (page) => page.evaluate((sel) => [...document.querySelectorAll(`${sel} .rule-import-line`)].map((l) => [l.querySelector("b").textContent, l.querySelector("code").textContent]), ROW);
const drawnRules = (page) => page.evaluate(() => document.querySelectorAll("#redactList .row.rule-bulk ~ .row .rule-edit").length);
// nothing in the row past its section's edge, and no sideways scroll
const fits = (page) => page.evaluate((sel) => {
  const l = document.querySelector(sel).getBoundingClientRect();
  const over = [...document.querySelectorAll(`${sel} input, ${sel} button, ${sel} textarea, ${sel} .hint, ${sel} code`)]
    .filter((n) => n.getClientRects().length).filter((n) => { const b = n.getBoundingClientRect(); return b.left < l.left - 1 || b.right > l.right + 1; })
    .map((n) => n.className || n.tagName);
  return { over, page: document.documentElement.scrollWidth > document.documentElement.clientWidth };
}, ROW);

const RULES = [{ kind: "GATEWAY_KEY", prefix: "oc_sk_" }];
const PASTE = [
  "API_KEY=oc_live_[A-Za-z0-9]{20,}",
  "GW_TOKEN\tgw-[0-9a-f]{32}",
  "ghp_[A-Za-z0-9]{36}",
  "",
  "  API_KEY = oc_live_[A-Za-z0-9]{20,} ",
  "数据库密码：dbpw:(\\S{8,})",
].join("\n");
const WANT = [
  { kind: "API_KEY", regex: "oc_live_[A-Za-z0-9]{20,}" },
  { kind: "GW_TOKEN", regex: "gw-[0-9a-f]{32}" },
  { kind: "", regex: "ghp_[A-Za-z0-9]{36}" },
  { kind: "数据库密码", regex: "dbpw:(\\S{8,})" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang} 400px: masking rules are imported in bulk and copied out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 400, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      const box = { saved: [], copied: [], fail: "",
        settings: { lang, theme: "light", redact: true, redactPersonal: false, redactRules: RULES } };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, box));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=privacy");
      await page.locator(ROW).waitFor();
      await settingsDrawn(page);

      // closed until asked for: the row's fields as before, nothing native
      assert.equal(await page.locator(`${ROW} textarea`).count(), 0, "no import box at first");
      assert.equal(await page.locator(`${ROW} select`).count(), 0);
      const open = page.locator(`${ROW} .rule-tools .word-import-open`);
      assert.equal((await open.textContent()).trim(), await said(page, "Import…"));
      await reach(page, open);
      await open.click();
      const area = page.locator(`${ROW} .rule-import textarea`);
      await page.waitForFunction((sel) => document.activeElement?.matches(`${sel} .rule-import textarea`), ROW);
      assert.deepEqual(await fits(page), { over: [], page: false }, "the open box fits a narrow window");

      // each line as it was read, a regex by default; the repeat passed over
      await area.fill(PASTE);
      const count = page.locator(`${ROW} .word-import-count`);
      assert.equal(await count.textContent(), await said(page, "New rules: {n}", { n: 4 }));
      if (lang === "en") assert.equal(await count.textContent(), "New rules: 4");
      assert.deepEqual(await preview(page), [["API_KEY", WANT[0].regex], ["GW_TOKEN", WANT[1].regex], ["CUSTOM", WANT[2].regex], ["CUSTOM", WANT[3].regex]]);
      assert.deepEqual(await fits(page), { over: [], page: false }, "the read rules fit a narrow window");

      // Prefix: a line the list has as a prefix is passed over
      const bar = page.locator(`${ROW} .rule-import .word-import-bar`);
      await bar.locator(".segs button", { hasText: await said(page, "Prefix") }).click();
      await area.fill("GATEWAY_KEY=oc_sk_\nVAULT: hvs.");
      assert.equal(await count.textContent(), await said(page, "New rules: {n}", { n: 1 }));
      assert.deepEqual(await preview(page), [["VAULT", "hvs.…"]]);

      // a redraw keeps the box open, what it holds and Prefix
      await page.evaluate(() => renderSettings());
      await page.locator(`${ROW} .rule-import textarea`).waitFor({ state: "visible" });
      assert.equal(await area.inputValue(), "GATEWAY_KEY=oc_sk_\nVAULT: hvs.");
      assert.equal(await bar.locator(".segs button.on").textContent(), await said(page, "Prefix"));
      assert.equal(box.saved.length, 0, "typing in the box saves nothing");

      // back to Regex, the first paste, Import: one save, in order after the rule there was
      await bar.locator(".segs button", { hasText: await said(page, "Regex") }).click();
      await area.fill(PASTE);
      const go = page.locator(`${ROW} .word-import-bar button.primary`);
      await reach(page, go);
      await go.click();
      await until(() => box.saved.length === 1, `saved ${JSON.stringify(box.saved)}`);
      assert.deepEqual(box.saved[0], [...RULES, ...WANT]);
      await page.waitForFunction((want) => document.querySelector("#status").textContent === want, await said(page, "Rules added: {n}", { n: 4 }));
      assert.equal(await page.locator(`${ROW} textarea`).count(), 0, "the box closes once imported");
      await page.waitForFunction(() => document.querySelectorAll("#redactList .row.rule-bulk ~ .row .rule-edit").length === 5);

      // a long paste lists its first eight, and says how many more
      await open.click();
      await area.fill(Array.from({ length: 11 }, (_, i) => `R${i}=r${i}_[a-z]{9}`).join("\n"));
      assert.equal((await preview(page)).length, 8);
      assert.equal(await page.locator(`${ROW} .rule-import-list > .hint`).textContent(), await said(page, "and {n} more rules", { n: 3 }));
      await area.press("Escape");

      // Escape and Cancel close it and save nothing, emptied for next time
      await open.click();
      await area.fill("X=abc[0-9]+");
      await area.press("Escape");
      assert.equal(await page.locator(`${ROW} textarea`).count(), 0);
      await open.click();
      assert.equal(await area.inputValue(), "");
      await area.fill("X=abc[0-9]+");
      await bar.locator("button:not(.primary):not(.opt)").click();
      assert.equal(await page.locator(`${ROW} textarea`).count(), 0);
      assert.equal(box.saved.length, 1, "closing saves nothing");

      // a list magpie refuses: said in the box, the paste kept, the rules as they were
      box.fail = "the masking rule BAD has a regular expression that doesn't work: missing closing )";
      await open.click();
      await area.fill("BAD=ns-(x");
      await area.press(process.platform === "darwin" ? "Meta+Enter" : "Control+Enter");
      await page.waitForFunction((want) => document.querySelector("#redactList .rule-import .word-import-count")?.textContent === want, box.fail);
      assert.equal(await area.inputValue(), "BAD=ns-(x");
      assert(await count.evaluate((n) => n.classList.contains("err")));
      box.fail = "";
      await area.press("Escape");

      // Copy: JSON, which Import takes back with nothing new
      const copy = page.locator(`${ROW} .rule-tools .word-copy`);
      await copy.click();
      await until(() => box.copied.length === 1, "copied once");
      assert.deepEqual(JSON.parse(box.copied[0]), [...RULES, ...WANT]);
      await page.waitForFunction((want) => document.querySelector("#status").textContent === want, await said(page, "Rules copied: {n}", { n: 5 }));
      await open.click();
      await area.fill(box.copied[0]);
      assert.equal(await count.textContent(), await said(page, "No new rules: each is in the list already"));
      assert.equal(await go.isDisabled(), true);
      // and a copy from another magpie, a prefix rule among regexes, comes in as it was
      await area.fill(JSON.stringify([{ kind: "SHOP", prefix: "shpat_" }, { kind: "GW_TOKEN", regex: "gw-[0-9a-f]{32}" }]));
      assert.deepEqual(await preview(page), [["SHOP", "shpat_…"]]);
      await go.click();
      await until(() => box.saved.length === 3, `saved ${box.saved.length}`);
      assert.deepEqual(box.saved[2].at(-1), { kind: "SHOP", prefix: "shpat_" });

      // what was saved is drawn again on the next load
      await page.reload();
      await page.locator(ROW).waitFor();
      await settingsDrawn(page);
      assert.equal(await drawnRules(page), 6);
      assert.deepEqual(errors, []);
    });
  }
}
