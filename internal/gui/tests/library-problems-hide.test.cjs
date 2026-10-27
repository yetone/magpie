// Run with Node's test runner and Playwright on the module path; see README.md.
// Zachary-wW (#3745): "Some of the Library couldn't be written" had only
// Try again, and what it listed, Grok Build's AGENTS.md that magpie can't
// stat, failed again at every change, since each one writes every agent.
// Its × now hides what the card lists, on every tab, and sends nothing. A
// read keeps it hidden, and so does a change that fails only on what was
// hidden: the reporter's case, in the window, on Instructions and from a
// chip, the toast still naming the failure. The same failure under
// another error (a temp file's name, new at each write) stays hidden. A
// failure not hidden brings the card back with all it lists, and so does
// a hidden one whose item was written since and fails again. An agent's
// own skills stay, under their own title, with no ×. From the keyboard
// the focus goes to the card's next button, or the tab open, and neither
// a click nor a key moves the view. The × is named in English, Chinese
// (both), Japanese and German, inside the card in a browser at 980, 440
// and 320px and in the window at 980 and 560px, 44px wide on a phone,
// with the title's words whole. No backend: the API answers as
// internal/gui/library.go does.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, file = "AGENTS.md") => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, instructions: `${HOME}/.${id}/${file}` });
// What magpie's own sync gave for Grok Build in a sandbox HOME, moved to
// this one: its AGENTS.md a link into a folder magpie can't search (the
// report's), its folder read-only, where each write's temp file is named
// anew (four writes in a row), and its skills folder read-only.
const grok = { agent: "grok", what: "instructions", error: `stat ${HOME}/.grok/AGENTS.md: permission denied` };
const temps = [2481177194, 3572372181, 3495008769, 1673008888];
const grokTemp = (n) => ({ agent: "grok", what: "instructions", error: `open ${HOME}/.grok/.AGENTS.md.${temps[n % temps.length]}.tmp: permission denied` });
const grokSkill = { agent: "grok", what: "skill:s01", error: `symlink ${HOME}/.config/magpie/library/skills/s01 ${HOME}/.grok/skills/s01: permission denied` };
const own = { agent: "claude", what: "skill:impeccable", error: "Claude Code already has a skill of its own called impeccable", own: true };
const names = ["impeccable", ...Array.from({ length: 12 }, (_, i) => "s" + String(i).padStart(2, "0"))];

// the page as magpie has it: each skill's chips carry the failures that
// stand for it, and each read has a library folder of its own (the number
// is the test's), which the Library folder button names on every tab, for
// a test to wait on
const view = (st) => ({
  dir: `${HOME}/.config/magpie/library-${st.reads}`, backups: `${HOME}/.config/magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code", "CLAUDE.md"), agent("codex", "Codex"), agent("grok", "Grok Build")],
  instructions: {
    shared: "",
    sets: [{ id: "default", name: "", active: st.set === "default", text: "" }, { id: "work", name: "Work", active: st.set === "work", text: "Rules." }],
    agents: [{ agent: "grok", extra: "", icon: "xai", name: "Grok Build", on: true, own: 0, path: `${HOME}/.grok/AGENTS.md` }],
  },
  servers: [], foundServers: [], projects: [], foundSkills: [],
  skills: names.map((name) => {
    const m = Object.fromEntries(st.last.filter((p) => p.what === "skill:" + name).map((p) => [p.agent, p.error]));
    return { name, description: name === "impeccable" ? "Design" : "d", kind: "", agents: st.agents[name], ...(Object.keys(m).length ? { problems: m } : {}) };
  }),
});
// problems has omitempty: none is no key at all
const omit = (p) => (p.length ? { problems: p } : {});

// As gui/library.go answers. st.last is lastProblems: a read gets it with
// no result. Any other change is a full sync (library.change), whose
// result holds every failure that stands, and the page's list becomes it.
// An Instructions change writes the instructions alone: it keeps the rest
// of the list and puts its own in (instructionsView). st.fails(n) is what
// the n-th sync fails on.
function server(lang, st, posts, web) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const post = req.method() === "POST";
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library" && !post) {
      st.reads++;
      return route.fulfill({ json: { ...view(st), ...omit(st.last) } });
    }
    if (url.pathname === "/api/library/instructions/save" && post) {
      posts.push(url.pathname);
      const body = req.postDataJSON();
      if (body.activate) st.set = body.activate;
      const result = { changed: [], problems: st.fails(st.syncs++).filter((p) => p.what === "instructions") };
      st.last = [...st.last.filter((p) => p.what !== "instructions"), ...result.problems];
      return route.fulfill({ json: { part: "instructions", instructions: view(st).instructions, result, ...omit(st.last) } });
    }
    if (url.pathname.startsWith("/api/library/") && post) {
      posts.push(url.pathname);
      if (url.pathname === "/api/library/skills/agents") {
        const body = req.postDataJSON();
        st.agents[body.name] = body.agents;
      }
      const result = { changed: [], problems: st.fails(st.syncs++) };
      st.last = result.problems;
      return route.fulfill({ json: { ...view(st), result, ...omit(st.last) } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    // the rest the page reads (the markets' lists) gets nothing
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { hide: "Hide until something new can't be written", failed: "Some of the Library couldn't be written", own: "Some agents have skills of their own by the library's names", use: "Use the library's", instructions: "Instructions" },
  zh: { hide: "隐藏，直到又有新的内容未能写入", failed: "资源库有部分内容未能写入", own: "有些 Agent 已有与资源库同名的自带技能", use: "用资源库的", instructions: "指令" },
  "zh-TW": { hide: "隱藏，直到又有新的內容未能寫入" },
  ja: { hide: "新たに書き込めないものが出るまで非表示にする" },
  de: { hide: "Ausblenden, bis etwas Neues nicht geschrieben werden kann" },
};
const lib = "#view-library";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's write failures can be hidden until something new can't be written", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    // problems is what stands as the page opens, fails what each sync
    // after it fails on (the same, unless a test says otherwise)
    const open = async (lang, problems, { width = 980, tab = "skills", web = true, fails } = {}) => {
      const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
      await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(20000); // the page's first load, slow on a busy machine
      page.on("pageerror", (e) => errors.push(e.message));
      // s01 is given to Grok Build too, which its skills folder may refuse
      const st = { last: structuredClone(problems), fails: fails || (() => structuredClone(problems)), reads: 0, syncs: 0, set: "default", agents: Object.fromEntries(names.map((n) => [n, n === "s01" ? ["claude", "codex", "grok"] : ["claude", "codex"]])) };
      const posts = [];
      await page.route("http://magpie.test/**", server(lang, st, posts, web));
      await page.goto("http://magpie.test/");
      // a fresh browser's first page, on a busy machine, can take longer to
      // show than the 5s every other wait here gets
      await page.locator('button[data-view="library"]').click({ timeout: 20000 });
      await page.locator(`${lib} .lib-body:not(.lib-skel)`).first().waitFor();
      return { page, st, posts };
    };
    const card = (page) => page.locator(`${lib} > .lib-problems`);
    const title = (page) => card(page).locator(".lib-problems-head b").textContent();
    const hideOf = (page, lang) => card(page).getByRole("button", { name: words[lang].hide, exact: true });
    // a reader's click, where the button is: no scroll of Playwright's own
    const press = async (page, loc) => {
      const b = await loc.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
    };
    const scrollTop = (page) => page.evaluate((v) => document.querySelector(v).scrollTop, lib);
    // the view scrolled by the reader's wheel, over the list: a scroll set
    // by code is no one's, and the page puts it back (app.js)
    const wheel = async (page, dy) => {
      const want = (await scrollTop(page)) + dy;
      const b = await page.locator(lib).boundingBox();
      await page.mouse.move(b.x + 24, b.y + b.height - 30);
      await page.mouse.wheel(0, dy);
      await page.waitForFunction(([v, want]) => document.querySelector(v).scrollTop === want, [lib, want]);
    };
    // what the next two frames do after a click (a hold, keyboardFocus)
    const settled = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    // the page drawn from a read after the n-th: its folder is its own
    const drawnAfter = (page, n) => page.waitForFunction(([v, n]) => {
      const m = document.querySelector(v + " .lib-more")?.title.match(/library-(\d+)$/);
      return m && Number(m[1]) > n;
    }, [lib, n]);
    // the page left for another and opened again: open reads the library
    const reopen = async (page, st) => {
      const n = st.reads;
      await page.locator('button[data-view="usage"]').click();
      await page.locator('button[data-view="library"]').click();
      await drawnAfter(page, n);
    };
    // the × clicked from a view scrolled a little, where the card's going
    // could move it (Chromium anchors the scroll)
    const hidden = async (page, lang) => {
      const room = await page.evaluate((v) => { const e = document.querySelector(v); return e.scrollHeight - e.clientHeight; }, lib);
      if (room >= 12) await wheel(page, 12);
      const before = await scrollTop(page);
      await press(page, hideOf(page, lang));
      await card(page).waitFor({ state: "detached" });
      await settled(page);
      assert.equal(await scrollTop(page), before, "a click scrolls nothing");
    };
    const row = (page, name) => page.locator(`${lib} .lib-row`).filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
    // a chip's write, as a reader's hand makes it: the chips fan out under
    // the pointer (at once, with reduced motion) until the one wanted is
    // the one under its own middle
    const chip = async (page, name, id) => {
      const b = await row(page, name).locator(".lib-agents").boundingBox();
      await page.mouse.move(b.x + 6, b.y + b.height / 2);
      await page.waitForFunction(([v, name, id]) => {
        const c = [...document.querySelectorAll(v + " .lib-row")].find((r) => r.querySelector(".name")?.textContent === name)?.querySelector(`.lib-ag[data-agent="${id}"]`);
        const r = c?.getBoundingClientRect();
        return !!r && document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)?.closest(".lib-ag") === c;
      }, [lib, name, id]);
      await press(page, row(page, name).locator(`.lib-ag[data-agent="${id}"]`));
    };
    // the toast a change ends with; cleared first, so the one waited for is
    // the next change's, not the last one's
    const clearToast = (page) => page.evaluate(() => { document.querySelector("#status").textContent = ""; });
    const toast = async (page, want) => {
      await page.waitForFunction((want) => document.querySelector("#status").textContent === want, want);
      assert.match(await page.locator("#status").getAttribute("class"), /\bwarn\b/);
    };
    const grokSaid = `Grok Build: ${grok.error}`;

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": the × hides what couldn't be written, on every tab, and a read keeps it hidden", async () => {
        const { page, st, posts } = await open(lang, [grok, grokSkill]);
        await card(page).waitFor();
        assert.equal(await title(page), w.failed);
        const dot = row(page, "s01").locator('.lib-ag[data-agent="grok"]');
        assert.ok(await dot.evaluate((c) => c.classList.contains("warn")), "s01's Grok Build chip has its dot");
        await hidden(page, lang);
        assert.deepEqual(posts, [], "nothing is sent");
        assert.ok(await dot.evaluate((c) => c.classList.contains("warn")), "the chip keeps its dot");
        await press(page, page.locator(`${lib} .lib-tabs .opt`, { hasText: w.instructions }));
        await page.locator(`${lib} .lib-tabs .opt.on`, { hasText: w.instructions }).waitFor();
        assert.equal(await card(page).count(), 0, "nor on Instructions, where Grok Build's is");
        await reopen(page, st);
        assert.equal(await card(page).count(), 0, "nor once the library is read again");
        assert.deepEqual(posts, []);
        await page.close();
      });

      await t.test(lang + ": an agent's own skills stay, and the keyboard's place moves to them", async () => {
        const { page } = await open(lang, [grok, grokSkill, own]);
        await card(page).waitFor();
        assert.equal(await title(page), w.failed);
        await wheel(page, 12);
        await hideOf(page, lang).focus();
        await page.keyboard.press("Enter");
        await page.waitForFunction(([v, want]) => document.querySelector(v + " > .lib-problems .lib-problems-head b")?.textContent === want, [lib, w.own]);
        assert.equal(await card(page).locator("li").count(), 1);
        assert.equal(await card(page).getByRole("button", { name: w.use }).count(), 1);
        assert.equal(await hideOf(page, lang).count(), 0, "no × beside an agent's own skills");
        assert.equal(await page.evaluate(() => document.activeElement?.textContent), w.use);
        await settled(page);
        assert.equal(await scrollTop(page), 12, "the focus moved, the view didn't");
        await page.close();
      });
    }

    await t.test("the reporter's case, in the window: every change fails on Grok Build's file again, and the card stays hidden", async () => {
      const { page, posts } = await open("en", [grok], { tab: "instructions", web: false });
      await card(page).waitFor();
      assert.equal(await card(page).locator("li").textContent(), "Grok Build · Instructions" + grok.error);
      await hidden(page, "en");
      // another set switched on: the instructions are written into every
      // agent, and Grok Build's still can't be stat'ed
      const saved = page.waitForResponse((r) => r.url().endsWith("/api/library/instructions/save"));
      await press(page, page.locator(`${lib} .lib-set`).nth(1).locator(".lib-radio"));
      await saved;
      await page.waitForFunction((v) => !document.querySelector(v).hasAttribute("aria-busy"), lib);
      await page.locator(`${lib} .lib-set.on`, { hasText: "Work" }).waitFor();
      assert.equal(await card(page).count(), 0, "an Instructions change that fails on it alone");
      await toast(page, grokSaid);
      // a chip on Skills: a full sync, every agent written again
      await press(page, page.locator(`${lib} .lib-tabs .opt`, { hasText: "Skills" }));
      await page.locator(`${lib} .lib-tabs .opt.on`, { hasText: "Skills" }).waitFor();
      await clearToast(page);
      await chip(page, "s05", "codex");
      await toast(page, grokSaid);
      assert.equal(await card(page).count(), 0, "a chip's change that fails on it alone");
      assert.deepEqual(posts, ["/api/library/instructions/save", "/api/library/skills/agents"]);
      await page.close();
    });

    await t.test("a failure not hidden brings the card back with all it lists", async () => {
      let fails = [grok];
      const { page, posts } = await open("en", [grok], { fails: () => structuredClone(fails) });
      await card(page).waitFor();
      await hidden(page, "en");
      // Grok Build's skills folder can't be written now, with s01's link
      // gone from it: every sync fails to give it back
      fails = [grok, grokSkill];
      await chip(page, "s05", "codex");
      await card(page).waitFor();
      assert.deepEqual(posts, ["/api/library/skills/agents"]);
      assert.equal(await title(page), words.en.failed);
      assert.equal(await card(page).locator("li").textContent(), "Grok Build · Skill s01" + grokSkill.error);
      // Grok Build's, hidden before, is listed again, on its own tab
      assert.equal(await card(page).getByRole("button", { name: "Instructions: 1 more", exact: true }).count(), 1);
      assert.equal(await hideOf(page, "en").count(), 1);
      await page.close();
    });

    await t.test("the same failure under another error, a temp file named anew at each write, stays hidden", async () => {
      const { page, st, posts } = await open("en", [grokTemp(0)], { tab: "instructions", fails: (n) => [grokTemp(n + 1)] });
      await card(page).waitFor();
      await hidden(page, "en");
      const saved = page.waitForResponse((r) => r.url().endsWith("/api/library/instructions/save"));
      await press(page, page.locator(`${lib} .lib-set`).nth(1).locator(".lib-radio"));
      await saved;
      await page.waitForFunction((v) => !document.querySelector(v).hasAttribute("aria-busy"), lib);
      assert.notEqual(grokTemp(1).error, grokTemp(0).error);
      await toast(page, `Grok Build: ${grokTemp(1).error}`);
      assert.equal(await card(page).count(), 0, "its temp file's name new");
      await press(page, page.locator(`${lib} .lib-tabs .opt`, { hasText: "Skills" }));
      await page.locator(`${lib} .lib-tabs .opt.on`, { hasText: "Skills" }).waitFor();
      await clearToast(page);
      await chip(page, "s05", "codex");
      await toast(page, `Grok Build: ${grokTemp(2).error}`);
      assert.equal(await card(page).count(), 0, "and again at the next");
      assert.deepEqual(st.last, [grokTemp(2)]);
      assert.deepEqual(posts, ["/api/library/instructions/save", "/api/library/skills/agents"]);
      await page.close();
    });

    await t.test("a hidden failure whose item was written since is new when it fails again", async () => {
      let fails = [grok, grokSkill];
      const { page } = await open("en", [grok, grokSkill], { fails: () => structuredClone(fails) });
      await card(page).waitFor();
      await hidden(page, "en");
      fails = [grok]; // Grok Build's skills folder writable again: s01 is given
      await clearToast(page);
      await chip(page, "s05", "codex");
      await toast(page, grokSaid);
      assert.equal(await card(page).count(), 0, "its AGENTS.md alone, still hidden");
      fails = [grok, grokSkill]; // and not, s01's link gone again
      await chip(page, "s06", "codex");
      await card(page).waitFor();
      assert.equal(await card(page).locator("li").textContent(), "Grok Build · Skill s01" + grokSkill.error);
      await page.close();
    });

    await t.test("what another window's change left brings it back too: a failure not hidden, or one after none stood", async () => {
      const { page, st } = await open("en", [grok]);
      await card(page).waitFor();
      await hidden(page, "en");
      st.last = [grok, grokSkill];
      await reopen(page, st);
      await card(page).waitFor();
      assert.equal(await title(page), words.en.failed);
      await hidden(page, "en");
      st.last = [];
      await reopen(page, st);
      assert.equal(await card(page).count(), 0);
      st.last = [grok];
      await reopen(page, st);
      await card(page).waitFor();
      assert.equal(await title(page), words.en.failed);
      await page.close();
    });

    await t.test("from the keyboard, with nothing left, the focus goes to the tab open", async () => {
      const { page } = await open("en", [grok, grokSkill]);
      await card(page).waitFor();
      await wheel(page, 12);
      await hideOf(page, "en").focus();
      await page.keyboard.press(" ");
      await card(page).waitFor({ state: "detached" });
      assert.ok(await page.evaluate(() => document.activeElement?.matches("#view-library .lib-tabs .opt.on")), "the focus is on the tab open");
      await settled(page);
      assert.equal(await scrollTop(page), 12, "the focus moved, the view didn't");
      await page.close();
    });

    for (const lang of Object.keys(words)) {
      await t.test(lang + ": the × is named and inside the card, in a browser and in the window, the title's words whole", async () => {
        for (const [web, width] of [[true, 980], [true, 440], [true, 320], [false, 980], [false, 560]]) {
          const where = `${web ? "browser" : "window"} ${width}px`;
          const { page } = await open(lang, [grok, grokSkill], { width, web });
          await card(page).waitFor();
          assert.equal(await hideOf(page, lang).count(), 1, where);
          const fit = await card(page).evaluate((c) => {
            const box = c.getBoundingClientRect(), x = c.querySelector(".lib-x").getBoundingClientRect(), again = c.querySelector(".lib-problems-head .action").getBoundingClientRect();
            // a word too long for the title's width shows as overflow once
            // it may no longer break inside
            const b = c.querySelector(".lib-problems-head b");
            b.style.overflowWrap = "normal";
            const whole = b.scrollWidth <= b.clientWidth;
            b.style.overflowWrap = "";
            return {
              inside: x.left >= box.left && x.right <= box.right && x.top >= box.top && x.bottom <= box.bottom,
              apart: x.left >= again.right || x.right <= again.left || x.top >= again.bottom || x.bottom <= again.top,
              wide: c.scrollWidth > c.clientWidth + 1,
              whole,
              size: [Math.round(x.width), Math.round(x.height) === Math.round(again.height)],
            };
          });
          // a phone's: as big to tap as Try again beside it
          const size = web && width <= 760 ? [44, true] : fit.size;
          assert.deepEqual(fit, { inside: true, apart: true, wide: false, whole: true, size }, where);
          await page.close();
        }
      });
    }
    assert.deepEqual(errors, []);
  });
}
