// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's member could not be read for what it is: the editor said
// its name, its provider and the reasoning it is sent at, and nothing of
// whether it sees images — which member a picture would reach was found out by
// sending one. Each member now carries the same chips the Gateway page's model
// list says a model in (modelInfo): its reasoning levels, whether it sees
// images and the window it holds. A member whose list says nothing of images
// is marked unknown, not "text only": magpie counts it text-only for a
// describer (gateway.blindTo), which is not its list saying it takes none. In
// English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// three members, written as groupsState() emits them: one that sees images,
// one whose list says it takes none, and one nothing was read of. `images` is
// omitempty, so a model that doesn't take them carries no images key at all;
// only a model nothing was read of carries imagesUnknown, and it is sent only
// when true — which is what the page reads as unknown.
const models = [
  { id: "p/eye", name: "Eye", providerName: "P", icon: "generic", context: 1048576, efforts: ["low", "medium", "high"], images: true },
  { id: "p/text", name: "Text", providerName: "P", icon: "generic", context: 200000 },
  { id: "p/mystery", name: "Mystery", providerName: "P", icon: "generic", context: 128000, imagesUnknown: true },
];
const group = {
  id: "g", name: "Mixed", routing: "", ready: true,
  members: models.map((m) => m.id),
  memberInfo: models.map((m) => ({
    id: m.id, ready: true, name: m.name, provider: "p", model: m.id, icon: "generic", context: m.context,
    ...(m.images ? { images: true } : {}), ...(m.imagesUnknown ? { imagesUnknown: true } : {}),
  })),
};

const words = {
  en: {
    images: "Accepts images", text: "Text only", unknown: "Images: not known", levels: "low–high",
    // the group member's chips say these are its models', not its own
    groupImages: "Accepts images (one of its models does)",
    groupLevels: "the levels every model in it has",
    groupContext: "the largest of its models'",
    // the chip a group member carries where a model's own picker sits, saying
    // which group it follows and where that group's levels are set
    follows: "Follows Inner",
    followsOn: "on Inner's own card",
  },
  zh: {
    images: "支持图片输入", text: "仅文本", unknown: "图片：未知", levels: "低–高",
    groupImages: "支持图片输入（组内有一个模型支持）",
    groupLevels: "组内每个模型都有的档位",
    groupContext: "组内模型中最大的",
    follows: "跟随 Inner 组",
    followsOn: "「Inner」那张卡片",
  },
};

// A member that is itself a routing group carries the same chips. It is in
// none of groups.models (groupsState leaves groups out), so the editor read
// nothing for it and its row was blank. It is read from its own memberInfo
// instead: the window the largest of its models has, whether any of them sees
// images, and the levels every one of them has.
const inner = {
  id: "g2", name: "Inner", routing: "", ready: true, hidden: false,
  members: ["p/eye", "p/text"],
  memberInfo: [
    { id: "p/eye", ready: true, name: "Eye", provider: "p", model: "p/eye", icon: "generic", context: 1048576, efforts: ["low", "medium", "high"], images: true },
    { id: "p/text", ready: true, name: "Text", provider: "p", model: "p/text", icon: "generic", context: 200000 },
  ],
};
// an outer group whose second member IS the inner group
const outer = {
  id: "g3", name: "Outer", routing: "", ready: true,
  members: ["p/text", "group/g2"],
  memberInfo: [
    { id: "p/text", ready: true, name: "Text", provider: "p", model: "p/text", icon: "generic", context: 200000 },
    // what agents are told of the group: the largest window of its models,
    // images because one of them sees, and the levels they all have
    { id: "group/g2", ready: true, name: "Inner", icon: "generic", group: true, on: 2, context: 1048576, efforts: ["low", "medium", "high"], images: true },
  ],
};

function serve(lang) {
  const groups = () => ({ models, pools: [], deciders: [], found: false, groups: [group, inner, outer] });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// each member row's chips: the badges drawn, the lines of its tooltip, and —
// for a member that is a group — the chip saying where its reasoning is set
const rows = (page) => page.locator(".rt-gedit .fbrow").evaluateAll((rs) => rs.map((r) => {
  const info = r.querySelector(".minfo"), inner = r.querySelector(".rt-inner");
  return {
    name: (r.querySelector(".n > span") || {}).textContent || "",
    badges: info ? [...info.querySelectorAll(".badge")].map((b) => b.textContent.trim() || "img") : [],
    title: info ? info.title : "",
    img: !!(info && info.querySelector(".mi-img")),
    follows: inner ? inner.textContent.trim() : "",
    followsTitle: inner ? inner.title : "",
  };
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's members say what each can do`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-member-info.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group[data-id=g]").waitFor();
      await page.locator(".rt-group[data-id=g]").click();
      await page.locator(".rt-gedit .fbrow").first().waitFor();

      const seen = await rows(page);
      assert.equal(seen.length, 3, "every member has its row");
      assert.deepEqual(seen.map((r) => r.name), ["Eye", "Text", "Mystery"], "the rows are the members, in order");

      // what it sees: the badge on the one that sees, and no badge on the others
      assert(seen[0].img, "the member that sees images carries the image chip");
      assert(!seen[1].img && !seen[2].img, "a member that does not see carries none");

      // and the same said in words, in the tooltip
      assert(seen[0].title.includes(w.images), `the seeing member says so: ${seen[0].title}`);
      assert(seen[1].title.includes(w.text), `the text-only member says so: ${seen[1].title}`);
      assert(seen[2].title.includes(w.unknown), `a member nothing was read of says unknown, not text only: ${seen[2].title}`);
      assert(!seen[2].title.includes(w.text), "and is not called text only");

      // the rest of the chips: its levels and the window it holds
      assert(seen[0].badges.includes(w.levels), `its reasoning levels: ${JSON.stringify(seen[0].badges)}`);
      assert(seen[0].badges.includes("1.0M"), `the window it holds: ${JSON.stringify(seen[0].badges)}`);
      assert.deepEqual(errors, [], "no page error");

      // the row is still the row it was: switching it off and removing it
      const off = page.locator(".rt-gedit .fbrow").first().locator(".rt-mon");
      await off.click();
      assert.equal(await off.getAttribute("aria-checked"), "false", "the member is switched off");
      await off.click();
      assert.equal(await off.getAttribute("aria-checked"), "true", "and on again");
      // the last row is wheeled up into view as a reader would: a scroll the
      // page wasn't asked for by the reader (Playwright's own) is put back
      // after the clicks above (scrollOnPurpose), which leaves the row under
      // the footer
      await page.mouse.move(550, 400);
      await page.mouse.wheel(0, 400);
      await page.locator(".rt-gedit .fbrow").last().locator("button.text", { hasText: lang === "zh" ? "移除" : "Remove" }).click();
      assert.equal((await rows(page)).length, 2, "the last member is removed");
    });

    test(`${engine} ${lang}: a member that is a group says what agents are told of it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-nested-member.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group[data-id=g3]").waitFor();
      await page.locator(".rt-group[data-id=g3]").click();
      await page.locator(".rt-gedit .fbrow").first().waitFor();

      const seen = await rows(page);
      assert.equal(seen.length, 2, "both members have a row");
      assert.deepEqual(seen.map((r) => r.name), ["Text", "Inner"], "the group member keeps its place and name");

      // the group member's row is not blank: it carries the chips a model's
      // row does, read off its memberInfo
      const groupRow = seen[1];
      assert(groupRow.badges.length > 0, `the group member carries chips: ${JSON.stringify(groupRow)}`);
      assert(groupRow.img, "the group member takes images (one of its models does)");
      assert(groupRow.badges.includes(w.levels), `its levels: ${JSON.stringify(groupRow.badges)}`);
      assert(groupRow.badges.includes("1.0M"), `the largest window of its models: ${JSON.stringify(groupRow.badges)}`);

      // and it says they are its models', not its own, in the tooltip
      assert(groupRow.title.includes(w.groupImages), `the group member says so: ${groupRow.title}`);
      assert(groupRow.title.includes(w.groupLevels), `the group's levels are said to be its models': ${groupRow.title}`);
      assert(groupRow.title.includes(w.groupContext), `the window is said to be the largest of its models': ${groupRow.title}`);

      // a plain member beside it is unchanged
      assert(!seen[0].img, "the plain text-only member carries no image chip");
      assert(seen[0].title.includes(w.text), `the plain member still says text only: ${seen[0].title}`);

      // where a model's own reasoning picker sits, a group carries a chip
      // saying which group it follows: its models reason as that group says
      // (provider.SaveGroup refuses "group/g2:high", and the gateway has
      // nowhere to put one), so the levels are set on that group's card
      assert.equal(groupRow.follows, w.follows, `the group member says which group it follows: ${JSON.stringify(groupRow)}`);
      assert(groupRow.followsTitle.includes(w.followsOn), `and where to set them: ${groupRow.followsTitle}`);
      // a plain member beside it keeps its own picker, not that chip
      assert.equal(seen[0].follows, "", "the plain member carries no such chip");

      // A nested jump must not replace a dirty outer draft before the answer.
      const name = page.locator(".rt-gedit input").first();
      await name.fill("Outer changed");
      const ask = page.locator("dialog.action-confirm[open]");
      await page.mouse.move(550, 400);
      await page.mouse.wheel(0, 400);
      const follows = page.locator(".rt-gedit .rt-inner");
      await follows.click();
      await ask.waitFor();
      await ask.locator("button").first().click();
      assert.equal(await name.inputValue(), "Outer changed", "Cancel keeps the outer draft");
      assert.equal(await follows.textContent(), w.follows, "Cancel stays in the outer editor");
      await follows.click();
      await ask.locator("button").last().click();
      await page.waitForFunction(() => document.querySelector(".rt-gedit input")?.value === "Inner");
      assert.equal(await page.locator(".rt-gedit .rt-inner").count(), 0, "Discard opens the inner editor");

      // A clean editor still opens the nested group in one click.
      await page.mouse.move(550, 400);
      await page.mouse.wheel(0, 400);
      await page.locator(".rt-group[data-id=g3]").click();
      await page.locator(".rt-gedit .rt-inner").waitFor();
      assert.equal(await name.inputValue(), "Outer", "the discarded name wasn't saved");
      // The last row sits under the footer in WebKit, as the test above found:
      // the wheel brings it up where a reader could click it
      await page.mouse.move(550, 400);
      await page.mouse.wheel(0, 400);
      await page.locator(".rt-gedit .fbrow").nth(1).locator(".rt-inner").click();
      await page.locator(".rt-gedit").first().waitFor();
      const openNames = await page.locator(".rt-gedit input").evaluateAll((is) => is.map((i) => i.value));
      assert(openNames.includes("Inner"), `the inner group's editor is open: ${JSON.stringify(openNames)}`);
      assert.equal(await ask.count(), 0, "the clean nested jump asks nothing");

      assert.deepEqual(errors, [], "no page error");
    });
  }
}
