// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group carries a note of the user's (ConatusMoon2222 on X: "请问
// 路由组可以添加备注吗?"). The Routing list shows it under the group's name,
// one muted line cut short with an ellipsis, the whole of it on hover, and
// only when there is one: a group without a note shows no line and no empty
// space for one. The editor has a Note field under Name; Save sends what is
// typed there, and clearing it sends none. A pick on a manual group's card
// and a routing picked from the row keep the note as it was. At a phone's
// width the note stays on one line, with no sideways scroll. In English,
// Chinese, Traditional Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/sol", name: "sol", providerName: "A", icon: "generic" },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic" },
];
const members = ["a/sol", "b/flash"];
const long = "For the office key only: Sol first, Flash when Sol is rate limited, and never on weekends because the plan resets on Monday morning";
const group = (id, name, extra) => ({ id, name, members, routing: "order", ready: true, memberInfo: members.map((m) => ({ id: m, ready: true })), ...extra });

const words = {
  en: { label: "Note", hint: "e.g. for the work key" },
  zh: { label: "备注", hint: "例如：给工作 Key 用" },
  "zh-TW": { label: "備註", hint: "例如：給工作 Key 用" },
  ja: { label: "メモ", hint: "例：仕事用のキー向け" },
  de: { label: "Notiz", hint: "z. B. für den Arbeitsschlüssel" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const g = {
    noted: group("noted", "Noted", { note: long }),
    plain: group("plain", "Plain", {}),
    hand: group("hand", "Hand", { routing: "manual", pick: "a/sol", picked: "a/sol", note: "pick by hand" }),
  };
  const groups = () => ({ models, pools: [], groups: Object.values(g) });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/save" && g[body.id]) {
        // as provider.SaveGroup: an empty note is no note
        g[body.id] = { ...g[body.id], ...body, picked: body.routing === "manual" ? body.pick : undefined };
        if (!body.note) delete g[body.id].note;
      }
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts));
  await page.goto("http://magpie.test/?view=groups");
  await page.locator('.rt-group[data-id="noted"]').waitFor();
  return { page, errors, posts };
}

// the note's line: one line, cut short, the name's line above it
const noteLine = (page, id) => page.locator(`.rt-group[data-id="${id}"] .rt-gnote`).evaluate((n) => {
  const cs = getComputedStyle(n), nm = n.parentElement.querySelector(".nm").getBoundingClientRect(), r = n.getBoundingClientRect();
  return { text: n.textContent, title: n.title, cut: n.scrollWidth > n.clientWidth, ellipsis: cs.textOverflow, oneLine: r.height <= parseFloat(cs.lineHeight || cs.fontSize) * 1.6 + 1,
    below: r.top >= nm.bottom - 1, inside: r.right <= n.closest(".rt-group").getBoundingClientRect().right + 0.5, color: cs.color };
});

const waitPost = async (page, posts, n) => {
  for (let i = 0; i < 150 && posts.length === n; i++) await page.waitForTimeout(20); // the post is the state waited for
  assert.ok(posts.length > n, "a save was posted");
  return posts.at(-1).body;
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a routing group's note shows on its row and is edited in its editor`, async (t) => {
      const { page, errors, posts } = await open(t, engine, lang);

      // the row: one muted line under the name, cut short, whole on hover
      const ln = await noteLine(page, "noted");
      assert.equal(ln.text, long);
      assert.equal(ln.title, long, "the whole note on hover");
      assert.ok(ln.cut && ln.ellipsis === "ellipsis", "a long note is cut short with an ellipsis");
      assert.ok(ln.oneLine && ln.below && ln.inside, JSON.stringify(ln));
      const nameColor = await page.locator('.rt-group[data-id="noted"] .nm b').evaluate((b) => getComputedStyle(b).color);
      assert.notEqual(ln.color, nameColor, "the note is quieter than the name");
      assert.equal(await page.locator('.rt-group[data-id="plain"] .rt-gnote').count(), 0, "no note, no line");

      // the editor: a Note field under Name with the note in it
      await page.locator('.rt-group[data-id="noted"]').click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      const labels = await ed.locator(":scope > label").allTextContents();
      assert.equal(labels[1], w.label, `the field after Name: ${labels}`);
      const field = ed.locator("input.rt-gnote-in");
      assert.equal(await field.inputValue(), long);
      assert.equal(await field.getAttribute("placeholder"), w.hint);
      await field.fill("  Office key, weekdays  ");
      let n = posts.length;
      await field.press("Enter"); // Enter in it saves, as in Name
      let body = await waitPost(page, posts, n);
      assert.equal(body.note, "Office key, weekdays");
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      await page.waitForFunction(() => document.querySelector('.rt-group[data-id="noted"] .rt-gnote')?.textContent === "Office key, weekdays");

      // cleared, it sends none and the row has no line
      await page.locator('.rt-group[data-id="noted"]').click();
      await ed.waitFor();
      await ed.locator("input.rt-gnote-in").fill("");
      n = posts.length;
      await ed.locator("button.primary").click();
      body = await waitPost(page, posts, n);
      assert.equal(body.note, "");
      await page.waitForFunction(() => !document.querySelector('.rt-group[data-id="noted"] .rt-gnote'));

      // a group without one: its field is empty and Save sends none
      await page.locator('.rt-group[data-id="plain"]').click();
      await ed.waitFor();
      assert.equal(await ed.locator("input.rt-gnote-in").inputValue(), "");
      n = posts.length;
      await ed.locator("button.primary").click();
      assert.equal((await waitPost(page, posts, n)).note, "");
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));

      // a pick on a manual group's card keeps its note
      n = posts.length;
      await page.locator('.rt-group[data-id="hand"] .rt-pick[data-member="b/flash"]').click();
      body = await waitPost(page, posts, n);
      assert.deepEqual({ pick: body.pick, note: body.note }, { pick: "b/flash", note: "pick by hand" });

      // a routing picked from the row keeps it too
      n = posts.length;
      await page.locator('.rt-group[data-id="hand"] button.rt-groute').click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      await menu.locator('.pm-item[aria-checked="false"]').first().click();
      body = await waitPost(page, posts, n);
      assert.notEqual(body.routing, "manual");
      assert.equal(body.note, "pick by hand", "a routing pick keeps the note");
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: at a phone's width a group's note stays one line`, async (t) => {
    const { page, errors } = await open(t, engine, "en", 390);
    const ln = await noteLine(page, "noted");
    assert.ok(ln.cut && ln.oneLine && ln.inside, JSON.stringify(ln));
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-note-390.png`) });
    }
    assert.deepEqual(errors, []);
  });
}
