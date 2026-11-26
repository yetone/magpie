// Run with Node's test runner and Playwright on the module path; see README.md.
// Sharing a routing group (laincatx and earayu on X). A group's menu has
// Share…, a sheet that publishes it at a usemagpie.ai link (groups/publish)
// with a title, words and an author, copies the link, then shows it with
// Stop sharing; its Advanced shows what goes out (groups/public) and
// copies the group as text (groups/share). A group magpie found can't be
// shared. Import opens a box: a link or text is previewed first
// (groups/preview), each member as it would be here, sent to another
// provider's same model or left out from its menu, and Import adds what
// the preview showed (groups/import, with the picks). Under the box, the
// groups others shared (groups/community): a card previews its group, ♥
// likes it, and a group taken down is said in the reader's language (the
// server's said key). A group imported from a link whose author shared a
// newer version has a line under it (Not now: groups/passover).
// magpie://group opens the window at ?shared=<id>, the group's preview.
// No click moves the page. At 420px, in every language, Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
  { id: "b/x", name: "x", providerName: "B", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const group = (id, name, members, more = {}) => ({ id, name, members, ready: true, memberInfo: info(members), ...more });
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const KEYS = ["Share…", "Shared…", "Share {name}", "{name} is shared", "Title", "Description", "By", "Gallery", "Anonymous",
  "What it is for, and how it routes", "List it on usemagpie.ai/groups", "Link", "Advanced", "Copy as text",
  "What goes to usemagpie.ai: no key or account, and a provider you added yourself is named custom.",
  "{name} as text to paste into another magpie: its models, routing and rules, no key",
  "{name} copied: paste it into Import on another magpie's Routing page", "{name} is shared: its link is copied",
  "{name} updated at {url}", "Stop sharing", "{name} is no longer shared", "Update",
  "Its link stops opening and it leaves the gallery; groups others imported from it stay theirs",
  "A link anyone can import {name} from: its models, routing and rules, never a key",
  "Import", "Add a routing group someone shared from their magpie", "Paste a usemagpie.ai/g/… link, or a group copied as text",
  "A routing group someone shared: its models, routing and rules come with it, never a key", "Preview", "Back", "Cancel",
  "This is not a routing group shared from magpie: paste its link, or the text from Copy as text",
  "by {name}", "Its page", "Left out", "Leave out", "Not in your magpie", "1 model left out: not in your magpie",
  "{n} models left out: not in your magpie", "1 rule left out with them", "Added {name}", "Done", "Replaced {name}",
  "Not in your magpie, left out: {models}", "The same model from your own provider: {models}",
  "Shared by others", "Search shared groups", "Most used", "Newest", "Like", "{here} of {n} models in your magpie",
  "used by 1", "used by {n}", "Yours", "Imported", "No shared group matches", "No group is shared yet",
  "This group is no longer shared: its author stopped sharing it, or it was taken down", "Sharing isn't available in this magpie",
  "Couldn't reach usemagpie.ai: {error}", "None of its models is in your magpie: add a provider that serves one first",
  "Its author shared a newer version", "Review", "Not now", "Found by magpie: change it first to make it yours"];
const SHARED = '{"magpie":"routing-group","version":1,"group":{"name":"One","members":["a/x","a/m"]}}';
const CARD = { id: "g9", url: "https://usemagpie.ai/g/g9", title: "Cheap first", description: "deepseek, then x", author: "ann", version: 1, members: ["deepseek/deepseek-v4", "a/x"], routing: "order", uses: 3, likes: 2, here: 1 };

// plan is the preview of a shared group here: a/x is here, with b/x the
// same model; custom/y isn't; a pick in use goes with it
function plan(use) {
  const m = [{ shared: "a/x", here: "a/x", same: ["b/x"] }, { shared: "custom/y", here: "" }];
  if (use && "a/x" in use) m[0].here = use["a/x"];
  return { name: "One", routing: "order", members: m, rules: 1, dropped: 1, empty: !m[0].here };
}

function serve(lang, posts) {
  let list = [
    group("one", "One", ["a/x", "a/m"], { routing: "order" }),
    group("two", "Two", ["a/m"]),
    group("auto-x", "x", ["a/x", "b/x"], { auto: true }),
  ];
  let links = {};
  let from = { two: { id: "g7", version: 1, title: "Two", at: "2026-11-20T00:00:00Z" } };
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list, links, from });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    const body = () => JSON.parse(r.request().postData() || "{}");
    const act = url.pathname.startsWith("/api/groups/") && url.pathname.slice(12);
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/copy") { posts.push(["copy", body().text]); return json({}); }
    if (url.pathname === "/api/open") { posts.push(["open", body().url]); return json({}); }
    if (act) {
      const b = body();
      if (act !== "community" && act !== "updates") posts.push([act, b]);
      switch (act) {
        case "share": return json({ ...groups(), shared: SHARED });
        case "public": return json({ shared: { magpie: "routing-group", version: 1, group: { name: "One", members: ["custom/x", "deepseek/deepseek-v4"] } } });
        case "publish": {
          links = { ...links, [b.id]: { ...b.meta, id: "abc", url: "https://usemagpie.ai/g/abc", version: (links[b.id]?.version || 0) + 1 } };
          return json({ ...groups(), published: links[b.id] });
        }
        case "unpublish": { const { [b.id]: _, ...rest } = links; links = rest; return json(groups()); }
        case "community": return json({ groups: [CARD], more: false });
        case "like": return json({ likes: b.on ? 3 : 2 });
        case "updates": return json({ updates: { two: { id: "g7", version: 2, title: "Two" } } });
        case "passover": { from = { ...from, two: { ...from.two, seen: b.version } }; return json(groups()); }
        case "preview": case "import": {
          // a pasted link is its group's source, as the server reads it
          const source = b.source || String(b.text || "").match(/usemagpie\.ai\/g\/(\w+)/)?.[1];
          if (source === "gone") return json({ error: "gone", code: "group_gone", said: "This group is no longer shared: its author stopped sharing it, or it was taken down" }, 404);
          if (!source && !String(b.text).includes("routing-group")) return json({ error: "not shared", code: "share", share: "not_shared", format: "0", models: "" }, 400);
          const src = source ? { ...CARD, id: source, url: "https://usemagpie.ai/g/" + source } : null;
          if (act === "preview") return json({ text: b.text || "", source: src, plan: plan(b.use), into: b.into || "" });
          list = [...list, group("one-2", "One 2", ["b/x"])];
          return json({ ...groups(), imported: { id: "one-2", name: "One 2", missing: ["custom/y"], instead: [], rules: 1 } });
        }
      }
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, at) {
  const errors = [], posts = [];
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width: 420, height: 1800 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    // a step that timed out says what the page threw on the way
    if (errors.length) t.diagnostic("page errors: " + errors.join(" | "));
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${t.name.replace(/\W+/g, "-")}.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  // WebKit reports a ResizeObserver round left to the next frame as a
  // page error; see group-duplicate.test.cjs
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.stack || e.message); });
  await page.route("**/*", serve(lang, posts));
  // a web link opens in the browser (api's open): kept here, never fetched
  await page.addInitScript(() => { window.open = (u) => { window.opened = u; return null; }; });
  await page.goto("http://magpie.test/?view=groups" + (at || ""));
  await page.locator(".rt-groups > .rt-group").nth(1).waitFor();
  // the page settles its own scroll as it loads
  await page.waitForTimeout(500);
  const say = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v || {}]);
  const status = async (k, v) => {
    const m = await say(k, v);
    await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), m);
  };
  const scrolled = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + scrollX);
  // snap: a step as it looks, kept with ARTIFACT_DIR
  // (an element's shot scrolls it into view: every scroll is put back)
  const snap = async (what, sel) => {
    if (!process.env.ARTIFACT_DIR) return;
    await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
    const file = path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${what}.png`);
    if (!sel) return void (await page.screenshot({ path: file }));
    const was = await page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e, e.scrollTop]).map(([e, y], i) => (e.dataset.snapAt = i, [i, y])));
    await page.locator(sel).first().screenshot({ path: file, animations: "disabled" });
    await page.evaluate((was) => {
      for (const e of document.querySelectorAll("*")) if (e.scrollTop && e.dataset.snapAt === undefined) e.scrollTop = 0;
      for (const [i, y] of was) { const e = document.querySelector(`[data-snap-at="${i}"]`); e.scrollTop = y; delete e.dataset.snapAt; }
    }, was);
  };
  return { page, errors, posts, say, status, scrolled, snap };
}

async function within(loc, what) {
  const b = await loc.boundingBox();
  assert(b && b.x >= 0 && b.x + b.width <= 420, `${what} off the window: ${JSON.stringify(b)}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    test(`${engine} ${lang}: a group is shared at a link, then no longer`, async (t) => {
      const { page, errors, posts, say, status, scrolled, snap } = await open(t, engine, lang);
      const before = await scrolled();
      const menu = page.locator(".row-menu");

      // a group magpie found isn't the user's to share
      await page.locator(".rt-group[data-id=auto-x] .main").click({ button: "right" });
      await menu.waitFor();
      // off, saying why under its name
      const off = menu.locator("[role=menuitem].has-why", { hasText: await say("Share…") });
      assert(await off.isDisabled(), "a found group can be shared");
      assert.equal(await off.locator(".rm-why").textContent(), await say("Found by magpie: change it first to make it yours"));
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });

      await page.locator(".rt-group[data-id=one] .main").click({ button: "right" });
      await menu.waitFor();
      await within(menu, "menu");
      await menu.getByRole("menuitem", { name: await say("Share…"), exact: true }).click();
      const sheet = page.locator("#modal .rt-gshare");
      await sheet.waitFor();
      await within(sheet, "share sheet");
      assert.equal(await sheet.locator(".ehead b").textContent(), await say("Share {name}", { name: "One" }));
      const inputs = sheet.locator("input:not([type=checkbox])");
      assert.equal(await inputs.first().inputValue(), "One", "the title starts as the group's name");
      assert.equal(await sheet.locator(".rt-gshare-stop").count(), 0, "Stop sharing on a group not shared");
      await sheet.locator("textarea").fill("x first, then m");
      await inputs.nth(1).fill("yetone");
      await snap("sheet");

      // Advanced: what goes out, and the group as text
      await sheet.locator("details.advanced summary").click();
      await page.waitForFunction(() => document.querySelector(".rt-gshare-json")?.textContent.includes("custom/x"));
      await sheet.locator("details.advanced button.text").click();
      await status("{name} copied: paste it into Import on another magpie's Routing page", { name: "One" });
      assert.deepEqual(posts.splice(0), [["public", { id: "one" }], ["share", { id: "one" }], ["copy", SHARED]]);

      await sheet.locator(".rt-gshare-go").click();
      await status("{name} is shared: its link is copied", { name: "One" });
      assert.deepEqual(posts.splice(0), [
        ["publish", { id: "one", meta: { title: "One", description: "x first, then m", author: "yetone", public: true } }],
        ["copy", "https://usemagpie.ai/g/abc"],
      ]);
      // the sheet again, as shared: its link and Stop sharing
      await page.waitForFunction((h) => document.querySelector("#modal .rt-gshare .ehead b")?.textContent === h, await say("{name} is shared", { name: "One" }));
      assert.equal(await sheet.locator(".rt-gshare-link code").textContent(), "https://usemagpie.ai/g/abc");
      await snap("sheet-shared");
      assert.equal(await sheet.locator("textarea").inputValue(), "x first, then m");
      await sheet.locator(".rt-gshare-stop").click();
      await status("{name} is no longer shared", { name: "One" });
      assert.deepEqual(posts.splice(0), [["unpublish", { id: "one" }]]);
      await page.waitForFunction(() => !document.querySelector("#modal .rt-gshare"));

      assert.equal(await scrolled(), before, "a click moved the page");
      if (lang !== "en") {
        const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, KEYS]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a shared group is previewed, picked and imported`, async (t) => {
      const { page, errors, posts, say, status, scrolled, snap } = await open(t, engine, lang);
      const before = await scrolled();
      await page.locator(".rt-gimportbtn").click();
      const text = page.locator(".rt-gimport-text");
      await text.waitFor();
      await within(page.locator(".rt-gimport"), "import box");
      // the gallery under the box, aside while something is pasted
      await page.locator(".rt-ggal-card[data-id=g9]").waitFor();
      await snap("gallery", ".rt-gimport");
      await text.fill("a group, honest");
      assert(await page.locator(".rt-ggal").isHidden(), "the gallery stayed over pasted text");
      await page.locator(".rt-gfilter").fill("o");
      await page.locator(".rt-gfilter").fill("");
      assert.equal(await text.inputValue(), "a group, honest", "the redraw lost the text");
      await page.locator(".rt-gimport-go").click();
      await status("This is not a routing group shared from magpie: paste its link, or the text from Copy as text");
      assert.equal(await text.count(), 1, "a refused text closed the box");
      posts.length = 0;

      await text.fill("```\n" + SHARED + "\n```");
      await page.locator(".rt-gimport-go").click();
      const rows = page.locator(".rt-gimp-m");
      await rows.nth(1).waitFor();
      assert.deepEqual(posts.splice(0), [["preview", { use: {}, text: "```\n" + SHARED + "\n```" }]]);
      assert.equal(await rows.nth(1).locator(".to").textContent(), await say("Not in your magpie"));
      await snap("preview", ".rt-gimport");
      const notes = await page.locator(".rt-gimport .rt-gimport-note").allTextContents();
      for (const w of [await say("1 rule left out with them"), await say("1 model left out: not in your magpie")]) assert(notes.includes(w), `${notes.join(" | ")} lacks ${w}`);
      // a/x to B's x, then left out: each pick is previewed again
      const pick = rows.nth(0).locator(".rt-gimp-pick");
      const menu = page.locator(".row-menu");
      await pick.click();
      await menu.waitFor();
      await menu.getByRole("menuitem", { name: "B · x", exact: true }).click();
      await page.waitForFunction(() => document.querySelector(".rt-gimp-m .rt-gimp-pick")?.textContent.startsWith("B"));
      assert.deepEqual(posts.splice(0)[0][1].use, { "a/x": "b/x" });
      await pick.click();
      await menu.waitFor();
      await menu.getByRole("menuitem", { name: await say("Leave out"), exact: true }).click();
      await page.waitForFunction((w) => document.querySelector(".rt-gimp-m .rt-gimp-pick")?.textContent === w, await say("Left out"));
      assert(await page.locator(".rt-gimport-go").isDisabled(), "Import with nothing of the group here");
      assert.deepEqual(posts.splice(0)[0][1].use, { "a/x": "" });
      await pick.click();
      await menu.waitFor();
      await menu.getByRole("menuitem", { name: "B · x", exact: true }).click();
      await page.waitForFunction(() => document.querySelector(".rt-gimport-go") && !document.querySelector(".rt-gimport-go").disabled);
      posts.length = 0;

      await page.locator(".rt-gimport-go").click();
      await page.locator(".rt-gimport-done").waitFor();
      assert.deepEqual(posts.splice(0), [["import", { use: { "a/x": "b/x" }, text: "```\n" + SHARED + "\n```" }]]);
      assert.equal(await page.locator(".rt-gimport-done").textContent(), await say("Added {name}", { name: "One 2" }));
      assert.equal(await page.locator(".rt-gimport-missing").textContent(), await say("Not in your magpie, left out: {models}", { models: "custom/y" }));
      await page.locator(".rt-gimport").getByRole("button", { name: await say("Done"), exact: true }).click();
      assert.equal(await page.locator(".rt-gimport").count(), 0, "Done left the box");
      assert((await page.locator(".rt-groups > .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id))).includes("one-2"));

      assert.equal(await scrolled(), before, "a click moved the page");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the gallery, an update and a magpie://group link`, async (t) => {
      const { page, errors, posts, say, status, snap } = await open(t, engine, lang, "&shared=g9");
      // the link's group, previewed at once, and the address no longer asks it
      const hd = page.locator(".rt-gimp-hd");
      await hd.waitFor();
      assert.equal(await hd.locator("b").textContent(), "Cheap first");
      assert.equal(await hd.locator(".note").textContent(), await say("by {name}", { name: "ann" }));
      assert(!(await page.evaluate(() => location.search)).includes("shared"), "the address still asks the shared group");
      assert.deepEqual(posts.find((p) => p[0] === "preview"), ["preview", { use: {}, source: "g9" }]);
      posts.length = 0;
      await page.locator(".rt-gimp-page").click();
      assert.equal(await page.evaluate(() => window.opened), "https://usemagpie.ai/g/g9");
      await page.locator(".rt-gimport").getByRole("button", { name: await say("Back"), exact: true }).click();

      // the gallery: a card, its like, its preview
      const card = page.locator(".rt-ggal-card[data-id=g9]");
      await card.waitFor();
      await within(card, "card");
      const foot = await card.locator(".foot").textContent();
      for (const w of [await say("{here} of {n} models in your magpie", { here: 1, n: 2 }), await say("used by {n}", { n: 3 }), await say("by {name}", { name: "ann" })]) assert(foot.includes(w), `${foot} lacks ${w}`);
      const like = card.locator(".rt-ggal-like");
      await like.click();
      await page.waitForFunction(() => document.querySelector(".rt-ggal-like")?.getAttribute("aria-pressed") === "true");
      assert.equal(await like.textContent(), "3");
      assert.deepEqual(posts.splice(0), [["like", { source: "g9", on: true }]]);
      await card.click();
      await hd.waitFor();
      assert.deepEqual(posts.splice(0), [["preview", { use: {}, source: "g9" }]]);
      await page.locator(".rt-gimport").getByRole("button", { name: await say("Back"), exact: true }).click();

      // a group taken down, said in the reader's language
      await page.locator(".rt-gimport-text").fill("https://usemagpie.ai/g/gone");
      await page.locator(".rt-gimport-go").click();
      await status("This group is no longer shared: its author stopped sharing it, or it was taken down");
      await page.locator(".rt-gimport").getByRole("button", { name: await say("Cancel"), exact: true }).click();

      // the author shared a newer version of Two: Not now leaves it be
      const up = page.locator(".rt-group[data-id=two] .rt-gupdate");
      await up.waitFor();
      assert((await up.textContent()).includes(await say("Its author shared a newer version")));
      await snap("update", ".rt-group[data-id=two]");
      await up.locator(".rt-gupdate-later").click();
      await up.waitFor({ state: "detached" });
      assert.deepEqual(posts.filter((p) => p[0] === "passover"), [["passover", { id: "two", version: 2 }]]);
      assert.deepEqual(errors, []);
    });
  }
}
