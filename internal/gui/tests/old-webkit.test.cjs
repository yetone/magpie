// Run with Node's test runner and Playwright on the module path; see README.md.
// The page runs in the system's WebKit, which on macOS 12 can be Safari
// 15.0's (#220 uclort: on 12.7.6 the panel showed its tabs and headings and
// nothing else, no button working — a regex lookbehind in app.js is a
// syntax error before Safari 16.4, so the whole file never ran). What the
// scripts and styles may use is held to Safari 15.0 here:
// - every script parses, and none has syntax newer than it: a regex
//   lookbehind (16.4) or v flag (17) or modifiers, a class static block
//   (16.4), decorators, import attributes, `using`;
// - no built-in newer than it is used unless compat.js (loaded before the
//   rest) fills it in;
// - the styles have no :has() (15.4), :focus-visible only in rules of its
//   own (a list with it and more drops them all before 15.4), no nesting, no dvh
//   units, and container queries, subgrid and a color-mix() custom property
//   only with an @supports fallback in the same file;
// - in WebKit with those built-ins taken away, the panel and the window
//   draw every page with no error.
// Syntax is parsed with the Babel parser Playwright bundles, so no other
// module is needed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { webkit } = require("playwright");
const { babelParse } = require("playwright/lib/transform/babelBundle");

const assets = path.resolve(__dirname, "../assets");
const scripts = async () => (await fs.readdir(assets)).filter((f) => f.endsWith(".js")).sort();
const styles = async () => (await fs.readdir(assets)).filter((f) => f.endsWith(".css")).sort();
const read = (f) => fs.readFile(path.join(assets, f), "utf8");

// Syntax Safari 15.0 can't parse; one of these fails the whole file.
const LOOKBEHIND = /\(\?<[=!]/;
const MODIFIERS = /\(\?[ims]*-?[ims]+:/;
function badSyntax(ast) {
  const found = [];
  const at = (n) => n.loc ? `:${n.loc.start.line}` : "";
  const regex = (n, pattern, flags) => {
    if (LOOKBEHIND.test(pattern)) found.push(`${at(n)} regex lookbehind (Safari 16.4): /${pattern}/${flags}`);
    if (/v/.test(flags)) found.push(`${at(n)} regex v flag (Safari 17): /${pattern}/${flags}`);
    if (MODIFIERS.test(pattern)) found.push(`${at(n)} regex modifiers: /${pattern}/${flags}`);
  };
  const walk = (n) => {
    if (!n || typeof n.type !== "string") return;
    switch (n.type) {
      case "RegExpLiteral": regex(n, n.pattern, n.flags); break;
      case "StaticBlock": found.push(`${at(n)} class static block (Safari 16.4)`); break;
      case "Decorator": found.push(`${at(n)} decorator`); break;
      case "ClassAccessorProperty": found.push(`${at(n)} accessor field`); break;
      case "ImportAttribute": found.push(`${at(n)} import attributes`); break;
      case "VariableDeclaration": if (/using/.test(n.kind)) found.push(`${at(n)} ${n.kind} declaration`); break;
      case "NewExpression": case "CallExpression": {
        // new RegExp("…") takes the same syntax, a SyntaxError when it runs
        const a = n.arguments?.[0];
        if (n.callee?.type === "Identifier" && n.callee.name === "RegExp" && a) {
          const text = a.type === "StringLiteral" ? a.value : a.type === "TemplateLiteral" ? a.quasis.map((q) => q.value.cooked).join("") : null;
          const flags = n.arguments[1]?.type === "StringLiteral" ? n.arguments[1].value : "";
          if (text != null) regex(n, text, flags);
        }
        break;
      }
    }
    for (const k of Object.keys(n)) {
      if (k === "loc" || k === "extra" || k === "leadingComments" || k === "trailingComments") continue;
      const v = n[k];
      if (Array.isArray(v)) v.forEach(walk);
      else if (v && typeof v === "object") walk(v);
    }
  };
  walk(ast.program);
  return found;
}

// Built-ins Safari 15.0 lacks. `fill` is what compat.js tests for when it
// fills one in; the others must not be used at all.
const BUILTINS = [
  { name: "Array.prototype.at", re: /\.at\(/, since: "15.4", fill: "Array.prototype.at" },
  { name: "findLast", re: /\.findLast\(/, since: "15.4", fill: "Array.prototype.findLast" },
  { name: "findLastIndex", re: /\.findLastIndex\(/, since: "15.4", fill: "Array.prototype.findLastIndex" },
  { name: "Object.hasOwn", re: /\bObject\.hasOwn\(/, since: "15.4", fill: "Object.hasOwn" },
  { name: "structuredClone", re: /\bstructuredClone\(/, since: "15.4", fill: "window.structuredClone" },
  { name: "toSorted/toReversed/toSpliced", re: /\.to(Sorted|Reversed|Spliced)\(/, since: "16" },
  { name: "Array.prototype.with", re: /\]\.with\(|\bArray\.prototype\.with\b/, since: "16" },
  { name: "String isWellFormed/toWellFormed", re: /\.(is|to)WellFormed\(/, since: "16.4" },
  { name: "AbortSignal.timeout/any", re: /\bAbortSignal\.(timeout|any)\(/, since: "16/17.4" },
  { name: "URL.canParse", re: /\bURL\.canParse\(/, since: "17" },
  { name: "Set methods", re: /\.(union|intersection|difference|symmetricDifference|isSubsetOf|isSupersetOf|isDisjointFrom)\(/, since: "17" },
  { name: "Array.fromAsync", re: /\bArray\.fromAsync\(/, since: "16.4" },
  { name: "Object.groupBy/Map.groupBy", re: /\b(Object|Map)\.groupBy\(/, since: "17.4" },
  { name: "Promise.withResolvers", re: /\bPromise\.withResolvers\(/, since: "17.4" },
  { name: "Iterator helpers", re: /\bIterator\.from\(|\.(keys|values|entries)\(\)\.(map|filter|take|drop|toArray|reduce|some|every|find|forEach|flatMap)\(/, since: "18.4" },
  { name: "checkVisibility", re: /\.checkVisibility\(/, since: "17.4" },
  { name: "popover", re: /\.(showPopover|hidePopover|togglePopover)\(|\.popover\s*=/, since: "17" },
  { name: "RegExp.escape", re: /\bRegExp\.escape\(/, since: "18.2" },
  { name: "Promise.try", re: /\bPromise\.try\(/, since: "18.2" },
];
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:\\"'`])\/\/.*$/gm, "$1");

test("every script parses with no syntax newer than Safari 15.0", async () => {
  const bad = [];
  for (const f of await scripts()) {
    let ast;
    try { ast = babelParse(await read(f), f, false); } catch (e) { bad.push(`${f}: does not parse: ${e.message.split("\n")[0]}`); continue; }
    for (const x of badSyntax(ast)) bad.push(f + x);
  }
  assert.deepEqual(bad, []);
});

test("the checks catch what broke macOS 12", () => {
  const found = (src) => badSyntax(babelParse(src, "x.js", false));
  assert.equal(found('const re = /("[^"]*"|(?<==)\\S+)/g;').length, 1, "lookbehind");
  assert.equal(found("s.replace(/(?<![\\w-])fill/g, f);").length, 1, "negative lookbehind");
  assert.equal(found('const re = new RegExp("(?<=a)b");').length, 1, "lookbehind in new RegExp");
  assert.equal(found("class A { static { init(); } }").length, 1, "static block");
  assert.equal(found("const re = /[\\p{L}--a]/v;").length, 1, "v flag");
  assert.deepEqual(found('const re = /(?<name>a)(?=b)(?!c)\\k<name>/g; a ??= b; class B { #x; #m() {} }'), [], "what Safari 15 has");
});

test("no built-in newer than Safari 15.0 unless compat.js fills it in", async () => {
  const compat = await read("compat.js");
  const bad = [];
  for (const f of await scripts()) {
    if (f === "compat.js") continue;
    const lines = stripComments(await read(f)).split("\n");
    for (const b of BUILTINS) {
      if (b.fill && compat.includes(`if (!${b.fill})`)) continue;
      lines.forEach((l, i) => { if (b.re.test(l)) bad.push(`${f}:${i + 1} ${b.name} (Safari ${b.since}): ${l.trim().slice(0, 100)}`); });
    }
  }
  assert.deepEqual(bad, []);
});

test("compat.js is the first of the page's own scripts", async () => {
  const html = await read("index.html");
  // boot.js, theme.js and omarchy.js run in the head, before the first
  // paint: theme.js is ES5 with nothing newer than Safari 10.1's
  // URLSearchParams, and omarchy.js does nothing off Omarchy, whose
  // WebKitGTK is a current one
  const all = [...html.matchAll(/<script\s+src="([^"]+)"/g)].map((m) => m[1]);
  const srcs = all.filter((s) => s !== "boot.js" && s !== "theme.js" && s !== "omarchy.js");
  assert.equal(srcs[0], "compat.js", "loaded first: " + srcs.join(", "));
  for (const f of await scripts()) assert(all.includes(f), f + " is not loaded by index.html");
});

test("the styles hold up in Safari 15.0", async () => {
  const bad = [];
  for (const f of await styles()) {
    const css = (await read(f)).replace(/\/\*[\s\S]*?\*\//g, "");
    const lineOf = (i) => css.slice(0, i).split("\n").length;
    const each = (re, what) => { for (const m of css.matchAll(re)) bad.push(`${f}:~${lineOf(m.index)} ${what}: ${m[0].trim().slice(0, 90)}`); };
    each(/:has\(/g, ":has() (Safari 15.4)");
    each(/[\d.]+(dvh|svh|lvh|dvw|svw|lvw)\b/g, "dynamic viewport units (Safari 15.4)");
    each(/[^{}]*&[^{}]*\{/g, "nesting (Safari 16.5)");
    // a selector list with :focus-visible and more: all of it is dropped
    for (const m of css.matchAll(/([^{}]+)\{/g)) {
      const sel = m[1].trim();
      if (sel.startsWith("@")) continue;
      const parts = sel.split(",");
      if (parts.some((p) => p.includes(":focus-visible")) && !parts.every((p) => p.includes(":focus-visible"))) bad.push(`${f}:~${lineOf(m.index)} :focus-visible in a selector list (Safari 15.4 drops the rule): ${sel.slice(0, 90)}`);
      if (/:is\([^)]*:focus-visible/.test(sel)) bad.push(`${f}:~${lineOf(m.index)} :focus-visible inside :is(): ${sel.slice(0, 90)}`);
    }
    if (/@container\b/.test(css) && !/@supports not \(container-type: inline-size\)/.test(css)) bad.push(`${f}: @container (Safari 16) with no @supports not (container-type: inline-size) fallback`);
    if (/:\s*subgrid\b/.test(css) && !/@supports not \(grid-template-columns: subgrid\)/.test(css)) {
      // a subgrid that only lines rows up across cards may go without
      for (const m of css.matchAll(/grid-template-columns:\s*subgrid/g)) bad.push(`${f}:~${lineOf(m.index)} grid-template-columns: subgrid (Safari 16) with no fallback`);
    }
    // a custom property is never checked: one holding color-mix() goes
    // invalid where it is used, and whatever it painted goes clear
    if (/--[\w-]+:\s*color-mix\(/.test(css) && !/@supports not \(color: color-mix\(/.test(css)) bad.push(`${f}: a custom property with color-mix() (Safari 16.2) and no @supports not (color: color-mix(…)) fallback`);
  }
  assert.deepEqual(bad, []);
});

// In WebKit with what Safari 15.0 lacks taken away, every page is drawn with
// no error: compat.js fills them in, and nothing else needs them.
const TAKEN = `
  for (const k of ["at", "findLast", "findLastIndex", "toSorted", "toReversed", "toSpliced", "with"]) delete Array.prototype[k];
  delete Object.hasOwn; delete window.structuredClone; delete Promise.withResolvers; delete Object.groupBy;
`;
const state = {
  agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [{ key: "model", label: "model", value: "gpt-6", options: [{ value: "gpt-6", label: "gpt-6" }] }] }],
  profiles: [{ name: "work" }], settings: { lang: "en", theme: "light" },
};
async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/usage/quotas") return json([]);
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/providers") return json({ providers: [], presets: [], gateway: { running: true } });
  if (url.pathname === "/api/gateway/trace") {
    if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
  }
  if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
  if (url.pathname === "/api/sessions/manage") {
    const last = new Date(Date.now() - 3600e3).toISOString();
    return json({
      agents: [{ agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claudecode-color" }], agent: "claude", terminal: false, trashDir: "~/x/trash/sessions", trash: [],
      sessions: [{ agent: "claude", id: "s1", cwd: "/work/app", title: "fix the build", start: last, last, resume: "cd /work/app && claude --resume s1", path: "~/.claude/projects/-work-app/s1.jsonl", size: 2048, messages: 4, files: 1, deletable: true }],
    });
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
}

test("with Safari 15.0's built-ins, the panel and the window draw every page", async () => {
  const browser = await webkit.launch();
  try {
    for (const mode of ["panel", "window"]) {
      const page = await browser.newPage({ viewport: mode === "panel" ? { width: 380, height: 640 } : { width: 1100, height: 760 } });
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(TAKEN);
      await page.route("**/*", serve);
      await page.goto(`http://magpie.test/?mode=${mode}`);
      await page.locator("#agents .row").first().waitFor();
      assert.equal(await page.evaluate(() => [1, 2, 3].findLast((x) => x < 3)), 2, "compat.js fills findLast in");
      assert.deepEqual(await page.evaluate(() => { const a = { b: [1] }, c = structuredClone(a); return [c.b[0], c !== a && c.b !== a.b]; }), [1, true]);
      assert.equal(await page.evaluate(() => [1, 2, 3].at(-1)), 3);
      if (mode === "panel") {
        assert(await page.locator("#ptabs").isVisible(), "the panel's tabs are the page's own, drawn by app.js");
        for (const tab of ["usage", "routing", "agents"]) {
          const b = page.locator(`#ptabs [data-ptab="${tab}"]`);
          if (await b.isVisible()) await b.click();
          await page.waitForTimeout(150);
        }
        await page.locator("#profBtn").click();
        await page.locator("#save").click();
        assert(await page.locator(".profiles.naming > .chip-input").isVisible(), "Save current opens the name field");
      } else {
        for (const v of ["providers", "gateway", "routing", "usage", "sessions", "library", "agents"]) {
          await page.locator(`#nav [data-view="${v}"]`).click();
          await page.locator(`#view-${v}`).waitFor();
          await page.waitForTimeout(200);
        }
      }
      assert.deepEqual(errors, [], mode + ": no error on the page");
      await page.close();
    }
  } finally {
    await browser.close();
  }
});
